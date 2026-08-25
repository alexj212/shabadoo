package hub

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// This is the first credential the coordinator hands out that costs money —
// every voice session is billed per minute — so the limit is about SPEND, not
// about guessing, which is what the redeem throttle is for.
func TestVoiceRateLimitIsPerDevice(t *testing.T) {
	v := newRateLimiter(voiceRateWindow, voiceRateLimit)
	now := time.Unix(1_700_000_000, 0)

	for i := 0; i < voiceRateLimit; i++ {
		if !v.allow("device:a", now) {
			t.Fatalf("refused mint %d of %d inside the limit", i+1, voiceRateLimit)
		}
	}
	if v.allow("device:a", now) {
		t.Error("allowed a mint past the limit")
	}

	// Keyed on the credential: one device's enthusiasm must not exhaust
	// another's budget.
	if !v.allow("device:b", now) {
		t.Error("a second device was blocked by the first device's usage")
	}

	// The window rolls; it is a rate, not a quota.
	if !v.allow("device:a", now.Add(voiceRateWindow+time.Minute)) {
		t.Error("the limit did not decay after the window")
	}
}

// stubProvider stands in for ElevenLabs so the mint — headers, status
// handling, response shape — is reachable without an account. Everything about
// this call is testable except whether the real API matches its own docs.
func stubProvider(t *testing.T, h http.HandlerFunc) func() {
	t.Helper()
	srv := httptest.NewServer(h)
	prev := elevenLabsBase
	elevenLabsBase = srv.URL
	return func() { elevenLabsBase = prev; srv.Close() }
}

func TestMintSendsTheKeyAndReturnsTheURL(t *testing.T) {
	var gotKey, gotAgent, gotPath string
	defer stubProvider(t, func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("xi-api-key")
		gotAgent = r.URL.Query().Get("agent_id")
		gotPath = r.URL.Path
		w.Write([]byte(`{"signed_url":"wss://example.invalid/convai?token=abc"}`))
	})()

	prevKey := ElevenLabsKey
	ElevenLabsKey = "secret-key"
	defer func() { ElevenLabsKey = prevKey }()

	got, err := mintElevenLabsURL(context.Background(), "agent-123")
	if err != nil {
		t.Fatalf("mint failed: %v", err)
	}
	if got != "wss://example.invalid/convai?token=abc" {
		t.Errorf("signed url = %q", got)
	}
	// The key travels in a header, never in the query string — a URL ends up in
	// access logs and proxy traces.
	if gotKey != "secret-key" {
		t.Errorf("xi-api-key = %q, want the configured key", gotKey)
	}
	if gotAgent != "agent-123" {
		t.Errorf("agent_id = %q", gotAgent)
	}
	if gotPath != "/v1/convai/conversation/get-signed-url" {
		t.Errorf("path = %q", gotPath)
	}
}

// An upstream error must not be echoed verbatim. This is an authenticated call
// to an account-level API, and its error bodies are where account details leak
// into a response a client reads.
func TestMintDoesNotLeakUpstreamErrors(t *testing.T) {
	defer stubProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"detail":{"message":"key sk_live_abc123 for account alex@example.com is invalid"}}`))
	})()

	_, err := mintElevenLabsURL(context.Background(), "a")
	if err == nil {
		t.Fatal("a 401 from the provider was reported as success")
	}
	for _, secret := range []string{"sk_live_abc123", "alex@example.com"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("upstream error leaked %q: %v", secret, err)
		}
	}
}

// A 200 carrying no url is a failure, not an empty success. Returning "" would
// have the client open a socket to nowhere and blame itself.
func TestMintRejectsAResponseWithNoURL(t *testing.T) {
	for _, body := range []string{`{}`, `{"signed_url":""}`, `not json at all`} {
		stop := stubProvider(t, func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(body))
		})
		got, err := mintElevenLabsURL(context.Background(), "a")
		stop()
		if err == nil {
			t.Errorf("body %q returned %q with no error", body, got)
		}
	}
}

// The endpoint must refuse before calling anywhere when it is not configured —
// a half-configured deployment reaching out with an empty key would fail
// slowly, and against someone else's rate limit.
func TestVoiceEndpointRefusesWhenUnconfigured(t *testing.T) {
	called := false
	defer stubProvider(t, func(w http.ResponseWriter, r *http.Request) { called = true })()

	prevKey, prevAgent := ElevenLabsKey, ElevenLabsAgent
	defer func() { ElevenLabsKey, ElevenLabsAgent = prevKey, prevAgent }()

	for _, tc := range []struct{ key, agent string }{{"", ""}, {"k", ""}, {"", "a"}} {
		ElevenLabsKey, ElevenLabsAgent = tc.key, tc.agent
		h := &humanAPI{now: time.Now}
		rec := httptest.NewRecorder()
		h.voiceSession(rec, httptest.NewRequest("POST", "/api/voice/session", nil))

		if rec.Code != http.StatusNotFound {
			t.Errorf("key=%q agent=%q gave %d, want 404", tc.key, tc.agent, rec.Code)
		}
	}
	if called {
		t.Error("an unconfigured coordinator still called the provider")
	}
}

// The whole path a phone takes: authenticated request -> rate limit -> mint ->
// JSON the client can use, with an audit row behind it. Everything except
// whether the real provider matches its own documentation.
func TestVoiceSessionEndToEnd(t *testing.T) {
	defer stubProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"signed_url":"wss://example.invalid/c?token=xyz"}`))
	})()
	prevKey, prevAgent := ElevenLabsKey, ElevenLabsAgent
	ElevenLabsKey, ElevenLabsAgent = "k", "agent-xyz"
	defer func() { ElevenLabsKey, ElevenLabsAgent = prevKey, prevAgent }()

	st := testStore(t)
	h := &humanAPI{store: st.s, now: time.Now}
	voiceMints = newRateLimiter(voiceRateWindow, voiceRateLimit) // isolate from other tests

	call := func(sub, scope string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/voice/session", nil)
		r = r.WithContext(context.WithValue(r.Context(), identityKey{},
			Identity{Tenant: st.id, Sub: sub, Scope: scope, Label: "phone"}))
		rec := httptest.NewRecorder()
		h.voiceSession(rec, r)
		return rec
	}

	rec := call("device:phone", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct{ SignedURL, AgentID, Scope string }
	if err := json.Unmarshal(rec.Body.Bytes(), &struct {
		SignedURL *string `json:"signed_url"`
		AgentID   *string `json:"agent_id"`
		Scope     *string `json:"scope"`
	}{&out.SignedURL, &out.AgentID, &out.Scope}); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.SignedURL != "wss://example.invalid/c?token=xyz" || out.AgentID != "agent-xyz" {
		t.Errorf("body = %+v", out)
	}
	if out.Scope != "full" {
		t.Errorf("scope = %q, want full", out.Scope)
	}

	// A READ-ONLY device may mint. It is the one that most needs to ask what is
	// going on out loud, and what it can then DO is decided by its own token on
	// the tools, not here.
	if rec := call("device:readonly", ScopeRead); rec.Code != http.StatusOK {
		t.Errorf("a read-only device was refused a voice session: %d", rec.Code)
	}

	// Spending is audited. "Who started forty sessions on Tuesday" is a
	// question somebody eventually asks of a per-minute bill.
	entries, err := st.AuditTail(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	var mints int
	for _, e := range entries {
		if e.Action == "voice.session" {
			mints++
		}
	}
	if mints != 2 {
		t.Errorf("audited %d mints, want 2", mints)
	}

	// And the limit bites on the same path, returning 429 rather than spending.
	for i := 0; i < voiceRateLimit; i++ {
		call("device:phone", "")
	}
	if rec := call("device:phone", ""); rec.Code != http.StatusTooManyRequests {
		t.Errorf("past the limit got %d, want 429", rec.Code)
	}
}

// A mint that never reached the provider spent nothing, so it must not consume
// a limit whose entire purpose is bounding spend.
//
// This is not a hypothetical tidy-up. Configuring voice for the first time
// produced four consecutive 401s from a key missing convai_write, and each one
// cost a slot — so the retries that diagnose a broken key eat the budget for
// the retries that fix it, at exactly the moment that hurts most.
func TestFailedMintDoesNotConsumeQuota(t *testing.T) {
	var calls int
	defer stubProvider(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"detail":{"status":"missing_permissions",` +
			`"message":"The API key you used is missing the permission convai_write"}}`))
	})()

	prevKey, prevAgent := ElevenLabsKey, ElevenLabsAgent
	ElevenLabsKey, ElevenLabsAgent = "k", "agent-xyz"
	defer func() { ElevenLabsKey, ElevenLabsAgent = prevKey, prevAgent }()

	st := testStore(t)
	h := &humanAPI{store: st.s, now: time.Now}
	voiceMints = newRateLimiter(voiceRateWindow, voiceRateLimit)

	call := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/voice/session", nil)
		r = r.WithContext(context.WithValue(r.Context(), identityKey{},
			Identity{Tenant: st.id, Sub: "device:phone", Label: "phone"}))
		rec := httptest.NewRecorder()
		h.voiceSession(rec, r)
		return rec
	}

	// Far more failures than the limit. Every one must reach the provider and
	// come back 502 — never 429, which would mean a broken key had locked the
	// device out of finding out why.
	for i := 0; i < voiceRateLimit*2; i++ {
		if rec := call(); rec.Code != http.StatusBadGateway {
			t.Fatalf("failure %d gave %d, want 502 — the limit charged for a call that spent nothing",
				i+1, rec.Code)
		}
	}
	if calls != voiceRateLimit*2 {
		t.Errorf("provider saw %d calls, want %d", calls, voiceRateLimit*2)
	}

	// The budget is still whole, so a working key mints immediately.
	if n := len(voiceMints.at["device:phone"]); n != 0 {
		t.Errorf("%d reservations survived %d failures, want 0", n, voiceRateLimit*2)
	}
}

// refund removes exactly one reservation, not the device's whole history.
func TestRefundRemovesOneReservation(t *testing.T) {
	v := newRateLimiter(voiceRateWindow, voiceRateLimit)
	now := time.Unix(1_700_000_000, 0)

	v.allow("d", now)
	v.allow("d", now.Add(time.Second))
	v.allow("d", now.Add(2*time.Second))
	v.refund("d", now.Add(time.Second))

	if got := len(v.at["d"]); got != 2 {
		t.Fatalf("after one refund, %d reservations remain, want 2", got)
	}
	for _, ts := range v.at["d"] {
		if ts.Equal(now.Add(time.Second)) {
			t.Error("refund removed the wrong reservation")
		}
	}
	// Refunding something never reserved must not corrupt the record.
	v.refund("d", now.Add(time.Hour))
	if got := len(v.at["d"]); got != 2 {
		t.Errorf("an unmatched refund changed the count to %d", got)
	}
}

// The provider's explanation must reach the operator's log while still not
// reaching the client. Both halves matter: the first 401 in production said
// exactly what was wrong and it took a hand-run curl on the host to see it.
func TestProviderErrorIsLoggedButNotReturned(t *testing.T) {
	defer stubProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"detail":{"status":"missing_permissions","code":"unauthorized",` +
			`"message":"The API key sk_live_abc123 is missing the permission convai_write"}}`))
	})()

	var logged strings.Builder
	prev := log.Writer()
	log.SetOutput(&logged)
	defer log.SetOutput(prev)

	_, err := mintElevenLabsURL(context.Background(), "a")
	if err == nil {
		t.Fatal("a 401 was reported as success")
	}
	// The caller still learns only the status.
	if strings.Contains(err.Error(), "convai_write") {
		t.Errorf("the provider's message reached the client: %v", err)
	}
	// The operator learns why.
	out := logged.String()
	for _, want := range []string{"missing_permissions", "convai_write", "401"} {
		if !strings.Contains(out, want) {
			t.Errorf("the log does not explain the failure (missing %q): %s", want, out)
		}
	}
}
