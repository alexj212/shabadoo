package main

// A real agent against a real coordinator, in one process.
//
// Everything below the HTTP layer is the production code path: SSHSIG login,
// the SSE command stream, result correlation, and the periodic report. Only the
// two host-touching seams are stubbed — the op handler and the session
// reporter — because tmux is not available in a test and is not what this is
// testing. The agent plane had no end-to-end coverage at all before this: the
// units exercised the coordinator's half, and nothing exercised the agent's.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"shabadoo/hub"
	"shabadoo/node"
)

// writeAgentKey generates an ed25519 keypair, writes the private half in
// OpenSSH format where the agent expects it, and returns the authorized_agents
// line naming it.
func writeAgentKey(t *testing.T, dir, node string) (keyPath, authLine string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	keyPath = filepath.Join(dir, "agent_key")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return keyPath, string(ssh.MarshalAuthorizedKey(sshPub)[:len(ssh.MarshalAuthorizedKey(sshPub))-1]) + " " + node + "\n"
}

func TestAgentRoundTrip(t *testing.T) {
	dir := t.TempDir()
	keyPath, authLine := writeAgentKey(t, dir, "testnode")

	agentsPath := filepath.Join(dir, "authorized_agents")
	if err := os.WriteFile(agentsPath, []byte(authLine), 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := hub.Open(filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	auth, err := hub.NewAuthorizerFromFile(agentsPath)
	if err != nil {
		t.Fatal(err)
	}
	h := hub.New(auth, store)
	mux := http.NewServeMux()
	h.Routes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// The agent's two seams, stubbed.
	gotOp := make(chan string, 4)
	handle := func(ctx context.Context, op string, payload json.RawMessage) (any, error) {
		gotOp <- op
		var a struct {
			Text string `json:"text"`
		}
		json.Unmarshal(payload, &a)
		return map[string]string{"echo": a.Text}, nil
	}
	sessions := func(ctx context.Context) (any, error) {
		return []hub.Session{{
			SessionID: "claude-demo-testnode", Project: "demo",
			CWD: "/tmp/demo", Alias: "demo-testnode", Window: "claude:0",
			Status: "idle", InputState: "dialog",
		}}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.New(node.Config{
		Coord: srv.URL, Node: "testnode", Version: "test", KeyFile: keyPath,
	}, handle, sessions).Run(ctx)

	// Wait for the agent to log in and register its stream.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := h.Call(ctx, hub.DefaultTenant, "testnode", "ping",
			map[string]any{"text": "hello"}); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("agent never became callable")
		}
		time.Sleep(50 * time.Millisecond)
	}

	select {
	case op := <-gotOp:
		if op != "ping" {
			t.Errorf("handler saw op %q, want ping", op)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler never ran")
	}

	// The result must come back correlated to the caller, not just be delivered.
	raw, err := h.Call(ctx, hub.DefaultTenant, "testnode", "ping",
		map[string]any{"text": "round trip"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	var got struct {
		Echo string `json:"echo"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("result %s: %v", raw, err)
	}
	if got.Echo != "round trip" {
		t.Errorf("echo = %q, want %q", got.Echo, "round trip")
	}
	<-gotOp

	// The periodic report must reach the store, tagged with the agent the
	// coordinator authenticated — not one the payload claimed.
	for {
		list, err := store.Tenant(hub.DefaultTenant).ListSessions(ctx, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if len(list) == 1 {
			if list[0].Agent != "testnode" {
				t.Errorf("session agent = %q, want testnode", list[0].Agent)
			}
			if list[0].InputState != "dialog" {
				t.Errorf("input_state = %q, want dialog (it must survive the round trip)", list[0].InputState)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("report never landed: %d sessions", len(list))
		}
		time.Sleep(100 * time.Millisecond)
	}

	// A command for a node that is not connected must fail, not hang.
	if _, err := h.Call(ctx, hub.DefaultTenant, "othernode", "ping", nil); err == nil {
		t.Error("call to an unconnected node succeeded")
	}
}

// An unauthorized key must not get a stream, however well-formed its signature.
func TestAgentRejectedWithoutAuthorizedKey(t *testing.T) {
	dir := t.TempDir()
	keyPath, _ := writeAgentKey(t, dir, "intruder")
	_, otherLine := writeAgentKey(t, filepath.Join(t.TempDir()), "testnode")

	agentsPath := filepath.Join(dir, "authorized_agents")
	if err := os.WriteFile(agentsPath, []byte(otherLine), 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := hub.Open(filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	auth, err := hub.NewAuthorizerFromFile(agentsPath)
	if err != nil {
		t.Fatal(err)
	}
	h := hub.New(auth, store)
	mux := http.NewServeMux()
	h.Routes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ran := make(chan struct{}, 1)
	go node.New(node.Config{
		Coord: srv.URL, Node: "intruder", Version: "test", KeyFile: keyPath,
	}, func(context.Context, string, json.RawMessage) (any, error) {
		ran <- struct{}{}
		return nil, nil
	}, func(context.Context) (any, error) { return []hub.Session{}, nil }).Run(ctx)

	// Give it as long as an authorized agent needs to connect several times
	// over, then assert it never became callable. Calling immediately would
	// pass even if the key were accepted, by racing the first login.
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, err := h.Call(ctx, hub.DefaultTenant, "intruder", "ping", nil); err == nil {
			t.Fatal("an unauthorized agent became callable")
		}
		time.Sleep(100 * time.Millisecond)
	}
	select {
	case <-ran:
		t.Fatal("an unauthorized agent executed a command")
	default:
	}
}
