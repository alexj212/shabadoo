package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// POST /api/message/broadcast had NEVER delivered a message.
//
// broadcastMessage resolved env.ToSession before fanning out — a line copied
// from sendMessage, where it is exactly right. A broadcast carries a scope or a
// topic and no to_session, and ResolveSession returns ErrNoRecipient on empty
// input, so every call 400'd three lines before it reached Broadcast. Measured
// on the live deployment before the fix: zero `message.broadcast` rows in the
// audit table and zero stored messages carrying a topic, ever.
//
// It is pinned end-to-end through the HANDLER deliberately. A store-level test
// passes against the broken build, because the store was never the broken part
// — which is also why this survived unnoticed: every layer it was tested at
// worked.
func TestHumanBroadcastActuallyDelivers(t *testing.T) {
	ctx, now := context.Background(), time.Now()
	st := testStore(t)
	if err := st.ReplaceAgentSessions(ctx, "wsl", scopeFleet()[:4], now); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceAgentSessions(ctx, "mac", scopeFleet()[4:], now); err != nil {
		t.Fatal(err)
	}

	h := &humanAPI{hub: New(nil, st.s), store: st.s, now: func() time.Time { return now }}
	post := func(env Envelope) *httptest.ResponseRecorder {
		body, _ := json.Marshal(env)
		r := httptest.NewRequest("POST", "/api/message/broadcast", strings.NewReader(string(body)))
		r = r.WithContext(context.WithValue(r.Context(), identityKey{},
			Identity{Tenant: st.id, Sub: "device:laptop", Label: "laptop"}))
		rec := httptest.NewRecorder()
		h.broadcastMessage(rec, r)
		return rec
	}

	rec := post(Envelope{Scope: "kind:core", Title: "hold", Body: "report, do not act"})
	if rec.Code != http.StatusOK {
		t.Fatalf("a broadcast with no to_session was refused with %d: %s",
			rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	var out struct {
		ID         string `json:"id"`
		Recipients int    `json:"recipients"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Recipients != 2 {
		t.Fatalf("recipients = %d, want 2 (one core session per node)", out.Recipients)
	}

	// Reported is not delivered — the distinction this system keeps getting
	// wrong — so the count is checked against actual inboxes rather than
	// believed.
	for _, id := range []string{"claude-core-wsl", "claude-core-mac"} {
		msgs, err := st.Drain(ctx, id, now)
		if err != nil {
			t.Fatal(err)
		}
		if len(msgs) != 1 || msgs[0].Body != "report, do not act" {
			t.Errorf("%s drained %d message(s), want the broadcast", id, len(msgs))
		}
	}

	// The control: an ordinary session outside the scope got nothing. Without
	// it, a handler that fanned out to everybody would satisfy every assertion
	// above.
	if msgs, _ := st.Drain(ctx, "claude-shabadoo-wsl", now); len(msgs) != 0 {
		t.Errorf("a session outside kind:core received %d message(s)", len(msgs))
	}
}

// A human's own send is never bounded to one node — there is no node it is on.
// This is the arm that would fail if mayAddressFleet stopped recognising the
// `human:` sender and started treating the operator as an unplaceable session.
func TestAHumanMayAddressTheWholeFleet(t *testing.T) {
	ctx, now := context.Background(), time.Now()
	st := testStore(t)
	if err := st.ReplaceAgentSessions(ctx, "wsl", scopeFleet()[:4], now); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceAgentSessions(ctx, "mac", scopeFleet()[4:], now); err != nil {
		t.Fatal(err)
	}

	h := &humanAPI{hub: New(nil, st.s), store: st.s, now: func() time.Time { return now }}
	body, _ := json.Marshal(Envelope{Scope: "all", Body: "fleet-wide notice"})
	r := httptest.NewRequest("POST", "/api/message/broadcast", strings.NewReader(string(body)))
	r = r.WithContext(context.WithValue(r.Context(), identityKey{},
		Identity{Tenant: st.id, Sub: "device:laptop", Label: "laptop"}))
	rec := httptest.NewRecorder()
	h.broadcastMessage(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	var out struct {
		Recipients int `json:"recipients"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Recipients != 6 {
		t.Errorf("a human reached %d sessions with `all`, want 6", out.Recipients)
	}
}
