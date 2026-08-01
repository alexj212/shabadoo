package hub

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// hubFixture is a coordinator plus the machinery to drive a fake agent at it.
type hubFixture struct {
	hub    *Hub
	store  *Tenant
	server *httptest.Server
	signer ssh.Signer
	pub    ssh.PublicKey
}

func newHubFixture(t *testing.T) *hubFixture {
	t.Helper()
	pub, signer := testKey(t)
	agents, err := ParseAuthorizedAgents([]byte(authorizedLine(pub, "wsl")))
	if err != nil {
		t.Fatal(err)
	}
	store := testStore(t)
	hub := New(NewAuthorizer(agents), store.s)

	mux := http.NewServeMux()
	hub.Routes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return &hubFixture{hub: hub, store: store, server: srv, signer: signer, pub: pub}
}

// login performs the challenge/sign/token exchange the way a node does.
func (f *hubFixture) login(t *testing.T) string {
	t.Helper()

	resp, err := http.Post(f.server.URL+"/agent/hello", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	var c Challenge
	json.NewDecoder(resp.Body).Decode(&c)
	resp.Body.Close()

	sig, err := f.signer.Sign(rand.Reader, c.blob())
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(loginReq{
		Challenge: c,
		PubKey:    f.pub.Marshal(),
		Signature: ssh.Marshal(sig),
		Version:   "test",
	})
	resp, err = http.Post(f.server.URL+"/agent/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: HTTP %d", resp.StatusCode)
	}
	var lr loginResp
	json.NewDecoder(resp.Body).Decode(&lr)
	if lr.Node != "wsl" {
		t.Fatalf("node = %q, want wsl", lr.Node)
	}
	return lr.Token
}

// fakeAgent opens the SSE stream and answers commands with handle().
// It returns a stop function.
func (f *hubFixture) fakeAgent(t *testing.T, token string, handle func(command) result) func() {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", f.server.URL+"/agent/stream", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := f.server.Client().Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("stream: HTTP %d", resp.StatusCode)
	}

	ready := make(chan struct{})
	go func() {
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		var once bool
		for sc.Scan() {
			line := sc.Text()
			if !once {
				close(ready) // first line means the stream is live
				once = true
			}
			data, found := strings.CutPrefix(line, "data: ")
			if !found {
				continue
			}
			var cmd command
			if err := json.Unmarshal([]byte(data), &cmd); err != nil {
				continue
			}
			res := handle(cmd)
			res.ID = cmd.ID
			b, _ := json.Marshal(res)
			rr, _ := http.NewRequest("POST", f.server.URL+"/agent/result", bytes.NewReader(b))
			rr.Header.Set("Authorization", "Bearer "+token)
			rr.Header.Set("Content-Type", "application/json")
			if pr, err := f.server.Client().Do(rr); err == nil {
				pr.Body.Close()
			}
		}
	}()

	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("stream did not open")
	}
	// Give the handler goroutine a moment to register the connection.
	for range 50 {
		if f.hub.IsOnline(DefaultTenant, "wsl") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cancel
}

// The whole agent path: SSH-key login, stream, command dispatch, result.
func TestHubRoundTrip(t *testing.T) {
	f := newHubFixture(t)
	token := f.login(t)

	stop := f.fakeAgent(t, token, func(cmd command) result {
		if cmd.Op != "capture" {
			return result{OK: false, Error: "unexpected op " + cmd.Op}
		}
		return result{OK: true, Payload: json.RawMessage(`{"text":"pane contents"}`)}
	})
	defer stop()

	raw, err := f.hub.Call(context.Background(), DefaultTenant, "wsl", "capture", map[string]string{"session": "claude"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	var got struct {
		Text string `json:"text"`
	}
	json.Unmarshal(raw, &got)
	if got.Text != "pane contents" {
		t.Errorf("payload = %+v", got)
	}
}

// An agent that reports an error gets it surfaced to the caller, not swallowed.
func TestHubPropagatesAgentError(t *testing.T) {
	f := newHubFixture(t)
	token := f.login(t)
	stop := f.fakeAgent(t, token, func(command) result {
		return result{OK: false, Error: "no such window"}
	})
	defer stop()

	_, err := f.hub.Call(context.Background(), DefaultTenant, "wsl", "kill", nil)
	if err == nil || !strings.Contains(err.Error(), "no such window") {
		t.Fatalf("err = %v, want the agent's message", err)
	}
}

func TestCallOfflineAgent(t *testing.T) {
	f := newHubFixture(t)
	_, err := f.hub.Call(context.Background(), DefaultTenant, "wsl", "capture", nil)
	if !errors.Is(err, ErrAgentOffline) {
		t.Fatalf("err = %v, want ErrAgentOffline", err)
	}
}

func TestCallUnknownNode(t *testing.T) {
	f := newHubFixture(t)
	token := f.login(t)
	defer f.fakeAgent(t, token, func(command) result { return result{OK: true} })()

	if _, err := f.hub.Call(context.Background(), DefaultTenant, "mac", "capture", nil); !errors.Is(err, ErrAgentOffline) {
		t.Fatalf("err = %v, want ErrAgentOffline", err)
	}
}

// Every agent endpoint must reject a bad or absent bearer token.
func TestAgentEndpointsRejectBadToken(t *testing.T) {
	f := newHubFixture(t)

	cases := []struct{ method, path string }{
		{"GET", "/agent/stream"},
		{"POST", "/agent/result"},
		{"POST", "/agent/report"},
	}
	for _, c := range cases {
		for _, tok := range []string{"", "Bearer nonsense", "Bearer " + newToken()} {
			req, _ := http.NewRequest(c.method, f.server.URL+c.path, strings.NewReader("{}"))
			if tok != "" {
				req.Header.Set("Authorization", tok)
			}
			resp, err := f.server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s %s with %q: status %d, want 401", c.method, c.path, tok, resp.StatusCode)
			}
		}
	}
}

// An unlisted key must not obtain a token.
func TestLoginRejectsUnlistedKey(t *testing.T) {
	f := newHubFixture(t)

	stranger, strangerSigner := testKey(t)
	resp, _ := http.Post(f.server.URL+"/agent/hello", "application/json", strings.NewReader("{}"))
	var c Challenge
	json.NewDecoder(resp.Body).Decode(&c)
	resp.Body.Close()

	sig, _ := strangerSigner.Sign(rand.Reader, c.blob())
	body, _ := json.Marshal(loginReq{Challenge: c, PubKey: stranger.Marshal(), Signature: ssh.Marshal(sig)})
	resp, err := http.Post(f.server.URL+"/agent/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

// Presence is connection liveness: when the stream drops, the agent's sessions
// must stop appearing in the dashboard.
func TestDisconnectClearsPresenceAndSessions(t *testing.T) {
	f := newHubFixture(t)
	token := f.login(t)
	stop := f.fakeAgent(t, token, func(command) result { return result{OK: true} })

	ctx, now := context.Background(), time.Now()
	f.store.UpsertSession(ctx, Session{SessionID: "s1", Agent: "wsl", Project: "iptv"}, now)

	if !f.hub.IsOnline(DefaultTenant, "wsl") {
		t.Fatal("agent not online after connect")
	}

	stop()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !f.hub.IsOnline(DefaultTenant, "wsl") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if f.hub.IsOnline(DefaultTenant, "wsl") {
		t.Fatal("agent still online after stream closed")
	}

	for time.Now().Before(deadline) {
		got, _ := f.store.ListSessions(ctx, now)
		if len(got) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	got, _ := f.store.ListSessions(ctx, now)
	t.Fatalf("%d sessions survived disconnect: %+v", len(got), got)
}

// A reconnecting agent supersedes its old connection rather than both
// receiving half the commands.
func TestReconnectSupersedesOldConnection(t *testing.T) {
	f := newHubFixture(t)

	first := f.login(t)
	stopFirst := f.fakeAgent(t, first, func(command) result {
		return result{OK: true, Payload: json.RawMessage(`"first"`)}
	})
	defer stopFirst()

	second := f.login(t)
	stopSecond := f.fakeAgent(t, second, func(command) result {
		return result{OK: true, Payload: json.RawMessage(`"second"`)}
	})
	defer stopSecond()

	raw, err := f.hub.Call(context.Background(), DefaultTenant, "wsl", "ping", nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if string(raw) != `"second"` {
		t.Errorf("answered by %s, want the newest connection", raw)
	}

	// The superseded token must stop working.
	req, _ := http.NewRequest("POST", f.server.URL+"/agent/report", strings.NewReader(`{"sessions":[]}`))
	req.Header.Set("Authorization", "Bearer "+first)
	resp, _ := f.server.Client().Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("superseded token status = %d, want 401", resp.StatusCode)
	}
}

// A report replaces the agent's window list, so a closed window disappears.
func TestReportReplacesSessionList(t *testing.T) {
	f := newHubFixture(t)
	token := f.login(t)
	defer f.fakeAgent(t, token, func(command) result { return result{OK: true} })()

	post := func(sessions []Session) {
		b, _ := json.Marshal(reportReq{Sessions: sessions})
		req, _ := http.NewRequest("POST", f.server.URL+"/agent/report", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := f.server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("report status = %d", resp.StatusCode)
		}
	}

	post([]Session{{SessionID: "s1", Project: "iptv"}, {SessionID: "s2", Project: "homelab"}})
	got, _ := f.store.ListSessions(context.Background(), time.Now())
	if len(got) != 2 {
		t.Fatalf("sessions = %d, want 2", len(got))
	}

	post([]Session{{SessionID: "s1", Project: "iptv"}})
	got, _ = f.store.ListSessions(context.Background(), time.Now())
	if len(got) != 1 || got[0].SessionID != "s1" {
		t.Fatalf("after report sessions = %+v, want only s1", got)
	}
}

// An agent must not be able to claim it owns another node's sessions.
func TestReportCannotSpoofAnotherNode(t *testing.T) {
	f := newHubFixture(t)
	token := f.login(t)
	defer f.fakeAgent(t, token, func(command) result { return result{OK: true} })()

	b, _ := json.Marshal(reportReq{Sessions: []Session{
		{SessionID: "s1", Agent: "mac", Project: "someone-elses"},
	}})
	req, _ := http.NewRequest("POST", f.server.URL+"/agent/report", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	got, _ := f.store.ListSessions(context.Background(), time.Now())
	if len(got) != 1 {
		t.Fatalf("sessions = %+v", got)
	}
	if got[0].Agent != "wsl" {
		t.Errorf("agent = %q, want wsl — an agent's claim about its own node is not trusted", got[0].Agent)
	}
}

// A Call whose agent never answers must time out rather than hang forever.
func TestCallTimesOutOnSilentAgent(t *testing.T) {
	f := newHubFixture(t)
	token := f.login(t)
	// Handler that never posts a result.
	stop := f.fakeAgent(t, token, func(command) result {
		time.Sleep(2 * time.Second)
		return result{OK: true}
	})
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := f.hub.Call(ctx, DefaultTenant, "wsl", "slow", nil); err == nil {
		t.Fatal("Call returned success from a silent agent")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Call took %v to give up", elapsed)
	}
}

// Login is audited — every agent that connects leaves a record.
func TestLoginIsAudited(t *testing.T) {
	f := newHubFixture(t)
	f.login(t)

	got, err := f.store.AuditTail(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Action != "login" || got[0].Actor != "agent:wsl" {
		t.Fatalf("audit = %+v", got)
	}
	if !strings.HasPrefix(got[0].Detail, "SHA256:") {
		t.Errorf("detail = %q, want the key fingerprint", got[0].Detail)
	}
}
