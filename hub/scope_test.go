package hub

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A fleet shaped like the real one: two nodes, a core session on each, and a
// project that exists on BOTH — which is the case that makes scoping hard and
// the one `who` already had to learn to render.
func scopeFleet() []Session {
	return []Session{
		{SessionID: "claude-core-wsl", Agent: "wsl", Project: "wsl", Kind: KindCore},
		{SessionID: "claude-shabadoo-wsl", Agent: "wsl", Project: "shabadoo", Kind: KindClaude},
		{SessionID: "claude-hub-wsl", Agent: "wsl", Project: "shabadoo/hub", Kind: KindClaude},
		{SessionID: "claude-minutes-wsl", Agent: "wsl", Project: "minutes", Kind: KindClaude},
		{SessionID: "claude-core-mac", Agent: "mac", Project: "mac", Kind: KindCore},
		{SessionID: "claude-minutes-mac", Agent: "mac", Project: "minutes", Kind: KindClaude},
	}
}

func got(t *testing.T, sessions []Session, scope, from string) []string {
	t.Helper()
	sc, err := parseScope(scope)
	if err != nil {
		t.Fatalf("parseScope(%q): %v", scope, err)
	}
	to, err := selectRecipients(sessions, sc, from)
	if err != nil {
		t.Fatalf("selectRecipients(%q, from %q): %v", scope, from, err)
	}
	return to
}

func joined(s []string) string { return strings.Join(s, ",") }

// The axis is THE NODE, and it is varied alone: both arms ask from the same
// privileged sender, over the same fleet, for the same kind of selector — only
// the node named changes. A selector that had stopped telling nodes apart, or
// one that matched everything, fails one arm or the other.
//
// The confound held constant is the sender's authority: an under-privileged
// sender would also produce a short list on the second arm, for an entirely
// different reason, and the pair would still look like it passed.
func TestNodeScopeSelectsTheNodeItNames(t *testing.T) {
	f := scopeFleet()

	wsl := got(t, f, "node:wsl", "human:alex")
	want := "claude-core-wsl,claude-hub-wsl,claude-minutes-wsl,claude-shabadoo-wsl"
	if joined(wsl) != want {
		t.Errorf("node:wsl reached %q, want %q", joined(wsl), want)
	}

	mac := got(t, f, "node:mac", "human:alex")
	if w := "claude-core-mac,claude-minutes-mac"; joined(mac) != w {
		t.Errorf("node:mac reached %q, want %q", joined(mac), w)
	}

	for _, id := range wsl {
		for _, other := range mac {
			if id == other {
				t.Errorf("session %s reached by both node scopes: the selector is not discriminating on node", id)
			}
		}
	}
}

// `project:` matches a session scoped into a subfolder, because that is a real
// arrangement here — and the pair proves it is a PREFIX match rather than a
// match-anything: `minutes` must not be dragged in by `shabadoo`.
func TestProjectScopeReachesSubfoldersAndNothingElse(t *testing.T) {
	f := scopeFleet()

	to := got(t, f, "project:shabadoo", "human:alex")
	if w := "claude-hub-wsl,claude-shabadoo-wsl"; joined(to) != w {
		t.Errorf("project:shabadoo reached %q, want %q (the subfolder session included)", joined(to), w)
	}

	// Both nodes' copies of one project, which is the case `who` exists for.
	both := got(t, f, "project:minutes", "human:alex")
	if w := "claude-minutes-mac,claude-minutes-wsl"; joined(both) != w {
		t.Errorf("project:minutes reached %q, want %q", joined(both), w)
	}
}

func TestKindScopeSelectsKind(t *testing.T) {
	to := got(t, scopeFleet(), "kind:core", "human:alex")
	if w := "claude-core-mac,claude-core-wsl"; joined(to) != w {
		t.Errorf("kind:core reached %q, want %q", joined(to), w)
	}
}

// The exemption and its control, in the identical state.
//
// A core session may address another node; an ordinary session on the same
// node, naming the same scope, over the same fleet, may not. Without the
// control arm the first assertion passes just as happily against a build that
// checks nobody's authority at all.
func TestOnlyACoreSessionMayAddressAnotherNode(t *testing.T) {
	f := scopeFleet()

	to := got(t, f, "all", "claude-core-wsl")
	if len(to) != 5 {
		t.Errorf("a core session reached %d sessions with `all`, want 5 (everyone but itself)", len(to))
	}

	sc, _ := parseScope("all")
	if _, err := selectRecipients(f, sc, "claude-shabadoo-wsl"); err == nil {
		t.Fatal("an ordinary session addressed the whole fleet: the bound is not applied")
	}

	// And a human always may — the operator's own send is never bounded.
	if to := got(t, f, "all", "human:alex"); len(to) != 6 {
		t.Errorf("a human reached %d sessions with `all`, want 6", len(to))
	}
}

// An ordinary session is bounded, not silenced: within its own node it works.
// This is the arm that proves the refusal above is about REACH rather than
// about ordinary sessions being unable to broadcast at all.
func TestAnOrdinarySessionMayStillAddressItsOwnNode(t *testing.T) {
	to := got(t, scopeFleet(), "node:wsl", "claude-shabadoo-wsl")
	if w := "claude-core-wsl,claude-hub-wsl,claude-minutes-wsl"; joined(to) != w {
		t.Errorf("an ordinary session reached %q on its own node, want %q", joined(to), w)
	}
}

// The refusal NAMES what it would have reached. Silently dropping the off-node
// half would tell a sender it reached `minutes` when it reached one of two —
// which is the exact failure shape this whole file exists to avoid.
func TestOffNodeRefusalNamesTheSessionsItWouldHaveReached(t *testing.T) {
	sc, _ := parseScope("project:minutes")
	_, err := selectRecipients(scopeFleet(), sc, "claude-minutes-wsl")
	if err == nil {
		t.Fatal("a wsl session silently reached the mac copy of its project")
	}
	if !strings.Contains(err.Error(), "claude-minutes-mac") {
		t.Errorf("refusal does not name the off-node session, so the sender cannot tell what it missed: %v", err)
	}
}

// A sender the index cannot place is refused rather than bounded to nothing or
// widened to everything. Fails closed, which is the opposite of the wake cap
// and deliberate: the cost is one refused broadcast, not a fleet fan-out.
func TestAnUnplaceableSenderIsRefused(t *testing.T) {
	sc, _ := parseScope("all")
	if _, err := selectRecipients(scopeFleet(), sc, "claude-ghost-nowhere"); err == nil {
		t.Fatal("a sender absent from the session index addressed the fleet")
	}
}

func TestSenderIsNeverItsOwnRecipient(t *testing.T) {
	for _, id := range got(t, scopeFleet(), "all", "claude-core-wsl") {
		if id == "claude-core-wsl" {
			t.Fatal("the sender received its own broadcast")
		}
	}
}

func TestUnparseableScopeIsRefusedRatherThanWidened(t *testing.T) {
	for _, bad := range []string{"", "everyone", "node", "node:", "host:wsl", "  "} {
		if sc, err := parseScope(bad); err == nil {
			t.Errorf("scope %q parsed as %v, want a refusal — a scope nobody understands must not become a fan-out", bad, sc)
		}
	}
}

// A scope that matches nothing returns a MEASURED zero: the recipients were
// computed from sessions the agents are reporting, so zero is a fact about the
// fleet. Distinguishing this from the topic path's zero is the entire point of
// the feature, so it is pinned rather than assumed.
func TestAScopeMatchingNothingIsAMeasuredZero(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	tn := testStore(t)
	if err := tn.ReplaceAgentSessions(ctx, "wsl", scopeFleet()[:4], now); err != nil {
		t.Fatal(err)
	}

	id, n, err := broadcastScoped(ctx, tn, Envelope{
		FromSession: "human:alex", Scope: "node:nosuchnode", Body: "hello",
	}, now)
	if err != nil {
		t.Fatalf("a scope matching nothing errored rather than reporting zero: %v", err)
	}
	if n != 0 {
		t.Errorf("recipients = %d, want 0", n)
	}
	if id == "" {
		t.Error("no message id returned: the message should still be stored and auditable")
	}
}

// End to end through the store: a scoped broadcast puts one message row in
// front of every matching session, and each of them can drain it.
func TestScopedBroadcastIsDrainableByEveryRecipient(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	tn := testStore(t)
	if err := tn.ReplaceAgentSessions(ctx, "wsl", scopeFleet()[:4], now); err != nil {
		t.Fatal(err)
	}
	if err := tn.ReplaceAgentSessions(ctx, "mac", scopeFleet()[4:], now); err != nil {
		t.Fatal(err)
	}

	_, n, err := broadcastScoped(ctx, tn, Envelope{
		FromSession: "human:alex", Scope: "project:minutes",
		Title: "re-vendor", Body: "the skill moved",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("recipients = %d, want 2", n)
	}

	for _, id := range []string{"claude-minutes-wsl", "claude-minutes-mac"} {
		msgs, err := tn.Drain(ctx, id, now)
		if err != nil {
			t.Fatal(err)
		}
		if len(msgs) != 1 || msgs[0].Body != "the skill moved" {
			t.Errorf("%s drained %d message(s), want the broadcast", id, len(msgs))
		}
	}

	// A session outside the scope has an empty inbox — the control that keeps
	// the assertion above from passing against a fan-out to everybody.
	if msgs, _ := tn.Drain(ctx, "claude-shabadoo-wsl", now); len(msgs) != 0 {
		t.Errorf("a session outside the scope received %d message(s)", len(msgs))
	}
}
