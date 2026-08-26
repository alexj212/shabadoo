package hub

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testStore(t *testing.T) *Tenant {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s.Tenant(DefaultTenant)
}

func msg(to, body string) Envelope {
	return Envelope{ToSession: to, Body: body, FromSession: "sender"}
}

// Property 1: a message for an offline session waits and is delivered when it
// comes back. Nothing about Send or Drain consults connection state — the
// delivery row is the wait.
func TestOfflineSessionMailWaits(t *testing.T) {
	ctx, s, now := context.Background(), testStore(t), time.Now()

	if _, err := s.Send(ctx, msg("claude-iptv-1", "check the crawler"), now); err != nil {
		t.Fatal(err)
	}

	// Hours pass; the session was never connected.
	later := now.Add(6 * time.Hour)
	got, err := s.Drain(ctx, "claude-iptv-1", later)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Body != "check the crawler" {
		t.Fatalf("drained %d messages: %+v", len(got), got)
	}
}

// Property 2 + 3: draining is idempotent, and a drained message is never
// redelivered. Redelivery would re-inject duplicate work into a live Claude
// conversation, which is the worse failure.
func TestDrainedMessageIsNeverRedelivered(t *testing.T) {
	ctx, s, now := context.Background(), testStore(t), time.Now()

	for _, body := range []string{"one", "two", "three"} {
		if _, err := s.Send(ctx, msg("sess", body), now); err != nil {
			t.Fatal(err)
		}
	}

	first, err := s.Drain(ctx, "sess", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 3 {
		t.Fatalf("first drain = %d, want 3", len(first))
	}

	second, err := s.Drain(ctx, "sess", now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatalf("second drain returned %d messages; drained mail must not come back", len(second))
	}
}

// Mail that arrives after a drain is delivered on the next one — the ack marks
// individual deliveries, not the session.
func TestMailAfterDrainStillArrives(t *testing.T) {
	ctx, s, now := context.Background(), testStore(t), time.Now()

	s.Send(ctx, msg("sess", "before"), now)
	if got, _ := s.Drain(ctx, "sess", now); len(got) != 1 {
		t.Fatalf("first drain = %d, want 1", len(got))
	}

	s.Send(ctx, msg("sess", "after"), now.Add(time.Minute))
	got, err := s.Drain(ctx, "sess", now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Body != "after" {
		t.Fatalf("second drain = %+v, want just 'after'", got)
	}
}

// Property 4: a session that never drains cannot grow the database without
// limit. Oldest undrained mail is shed first.
func TestRetentionCapsPerRecipient(t *testing.T) {
	ctx, s, now := context.Background(), testStore(t), time.Now()

	for i := range maxPerRecipient + 20 {
		env := msg("hoarder", "m")
		// Distinct timestamps so "oldest" is well defined.
		if _, err := s.Send(ctx, env, now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}

	n, err := s.Pending(ctx, "hoarder", now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != maxPerRecipient {
		t.Fatalf("pending = %d, want exactly %d", n, maxPerRecipient)
	}

	// The cap must shed the *oldest* mail. Shedding the newest would silently
	// discard whatever just arrived while keeping a stale backlog.
	got, err := s.Drain(ctx, "hoarder", now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	oldestKept := got[0].CreatedAt
	newestSent := now.Add(time.Duration(maxPerRecipient+19) * time.Second).Unix()
	if got[len(got)-1].CreatedAt != newestSent {
		t.Errorf("newest message was shed; survivors end at %d, want %d",
			got[len(got)-1].CreatedAt, newestSent)
	}
	if oldestKept != now.Add(20*time.Second).Unix() {
		t.Errorf("survivors start at %d, want the 20 oldest dropped (%d)",
			oldestKept, now.Add(20*time.Second).Unix())
	}
}

// Expired mail is neither drained nor counted, and Vacuum reclaims it.
func TestExpiredMailIsNotDelivered(t *testing.T) {
	ctx, s, now := context.Background(), testStore(t), time.Now()

	s.Send(ctx, msg("sess", "stale"), now)

	past := now.Add(messageTTL + time.Hour)
	got, err := s.Drain(ctx, "sess", past)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("drained %d expired messages", len(got))
	}
	if n, _ := s.Pending(ctx, "sess", past); n != 0 {
		t.Fatalf("pending = %d, want 0", n)
	}

	removed, err := s.s.Vacuum(ctx, past)
	if err != nil {
		t.Fatal(err)
	}
	if removed.Messages != 1 {
		t.Fatalf("vacuumed %d message(s), want 1", removed.Messages)
	}
}

// Deleting a message must take its deliveries with it, or Vacuum leaves orphan
// rows that Pending would keep counting.
func TestVacuumCascadesToDeliveries(t *testing.T) {
	ctx, s, now := context.Background(), testStore(t), time.Now()
	s.Send(ctx, msg("sess", "x"), now)

	past := now.Add(messageTTL + time.Hour)
	if _, err := s.s.Vacuum(ctx, past); err != nil {
		t.Fatal(err)
	}

	var orphans int
	if err := s.s.db.QueryRow(`SELECT COUNT(*) FROM deliveries`).Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if orphans != 0 {
		t.Fatalf("%d orphan delivery rows survived vacuum", orphans)
	}
}

func TestSendRequiresRecipient(t *testing.T) {
	ctx, s := context.Background(), testStore(t)
	if _, err := s.Send(ctx, Envelope{Body: "nowhere"}, time.Now()); err != ErrNoRecipient {
		t.Fatalf("err = %v, want ErrNoRecipient", err)
	}
}

// Broadcast fans out to current subscribers only.
func TestBroadcastReachesSubscribers(t *testing.T) {
	ctx, s, now := context.Background(), testStore(t), time.Now()

	s.Subscribe(ctx, "a", "deploys")
	s.Subscribe(ctx, "b", "deploys")
	s.Subscribe(ctx, "c", "other")

	_, n, err := s.Broadcast(ctx, Envelope{Topic: "deploys", Body: "ovh3 is live"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("fanned out to %d, want 2", n)
	}

	for _, sess := range []string{"a", "b"} {
		got, _ := s.Drain(ctx, sess, now)
		if len(got) != 1 {
			t.Errorf("%s drained %d, want 1", sess, len(got))
		}
	}
	if got, _ := s.Drain(ctx, "c", now); len(got) != 0 {
		t.Errorf("non-subscriber c received %d messages", len(got))
	}
}

// A session subscribing later must not receive earlier broadcasts — otherwise
// a new session wakes to a day of backlog.
func TestBroadcastDoesNotBackfill(t *testing.T) {
	ctx, s, now := context.Background(), testStore(t), time.Now()

	s.Broadcast(ctx, Envelope{Topic: "deploys", Body: "old news"}, now)
	s.Subscribe(ctx, "latecomer", "deploys")

	got, _ := s.Drain(ctx, "latecomer", now.Add(time.Minute))
	if len(got) != 0 {
		t.Fatalf("latecomer received %d backfilled messages", len(got))
	}
}

func TestUnsubscribeStopsDelivery(t *testing.T) {
	ctx, s, now := context.Background(), testStore(t), time.Now()
	s.Subscribe(ctx, "a", "deploys")
	s.Unsubscribe(ctx, "a", "deploys")

	_, n, _ := s.Broadcast(ctx, Envelope{Topic: "deploys", Body: "x"}, now)
	if n != 0 {
		t.Fatalf("fanned out to %d after unsubscribe", n)
	}
}

// Presence is connection liveness: when an agent drops, its sessions must stop
// appearing alive.
func TestDropAgentSessions(t *testing.T) {
	ctx, s, now := context.Background(), testStore(t), time.Now()

	s.UpsertSession(ctx, Session{SessionID: "s1", Agent: "wsl", Project: "iptv"}, now)
	s.UpsertSession(ctx, Session{SessionID: "s2", Agent: "wsl", Project: "homelab"}, now)
	s.UpsertSession(ctx, Session{SessionID: "s3", Agent: "mac", Project: "bin"}, now)

	if err := s.DropAgentSessions(ctx, "wsl"); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListSessions(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Agent != "mac" {
		t.Fatalf("sessions after drop = %+v", got)
	}
}

// Re-reporting a window updates it rather than duplicating it.
func TestUpsertSessionIsIdempotent(t *testing.T) {
	ctx, s, now := context.Background(), testStore(t), time.Now()

	s.UpsertSession(ctx, Session{SessionID: "s1", Agent: "wsl", Status: "idle"}, now)
	s.UpsertSession(ctx, Session{SessionID: "s1", Agent: "wsl", Status: "busy"}, now.Add(time.Minute))

	got, _ := s.ListSessions(ctx, now)
	if len(got) != 1 {
		t.Fatalf("sessions = %d, want 1", len(got))
	}
	if got[0].Status != "busy" {
		t.Errorf("status = %q, want busy", got[0].Status)
	}
}

// ListSessions carries each session's undrained count — what the dashboard and
// the nudge path both read.
func TestListSessionsCarriesPendingCount(t *testing.T) {
	ctx, s, now := context.Background(), testStore(t), time.Now()

	s.UpsertSession(ctx, Session{SessionID: "s1", Agent: "wsl"}, now)
	s.Send(ctx, msg("s1", "a"), now)
	s.Send(ctx, msg("s1", "b"), now)

	got, err := s.ListSessions(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Pending != 2 {
		t.Fatalf("session = %+v, want pending 2", got)
	}
}

func TestConversationIncludesBothDirections(t *testing.T) {
	ctx, s, now := context.Background(), testStore(t), time.Now()

	s.Send(ctx, Envelope{FromSession: "a", ToSession: "b", Body: "ping"}, now)
	s.Send(ctx, Envelope{FromSession: "b", ToSession: "a", Body: "pong"}, now.Add(time.Second))
	s.Send(ctx, Envelope{FromSession: "c", ToSession: "d", Body: "unrelated"}, now)

	got, err := s.Conversation(ctx, "a", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("conversation = %d messages, want 2: %+v", len(got), got)
	}
}

// Replay is read-only: looking at the timeline must not consume anyone's mail.
func TestReplayDoesNotConsume(t *testing.T) {
	ctx, s, now := context.Background(), testStore(t), time.Now()
	s.Send(ctx, msg("sess", "x"), now)

	if _, err := s.Replay(ctx, 50, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Pending(ctx, "sess", now); n != 1 {
		t.Fatalf("pending = %d after replay, want 1", n)
	}
}

func TestAuditRoundTrip(t *testing.T) {
	ctx, s, now := context.Background(), testStore(t), time.Now()

	s.Audit(ctx, AuditEntry{Actor: "alex@example.com", Action: "send",
		Target: "wsl:iptv", Detail: "restart the crawler"}, now)
	s.Audit(ctx, AuditEntry{Actor: "alex@example.com", Action: "kill", Target: "wsl:homelab"}, now.Add(time.Second))

	got, err := s.AuditTail(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("audit = %d entries, want 2", len(got))
	}
	if got[0].Action != "kill" {
		t.Errorf("newest = %q, want kill (newest first)", got[0].Action)
	}
}

func TestRetrievalLogging(t *testing.T) {
	ctx, s, now := context.Background(), testStore(t), time.Now()

	if err := s.RecordRetrieval(ctx, "claude-iptv-1", "project:homelab",
		"traefik file rules", "3 hits", now); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.s.db.QueryRow(`SELECT COUNT(*) FROM retrievals`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("retrievals = %d, want 1", n)
	}
}

func TestStats(t *testing.T) {
	ctx, s, now := context.Background(), testStore(t), time.Now()

	s.SeenAgent(ctx, "wsl", "SHA256:abc", "1.0", now)
	s.UpsertSession(ctx, Session{SessionID: "s1", Agent: "wsl"}, now)
	s.Send(ctx, msg("s1", "a"), now)

	st, err := s.Stats(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if st.Agents != 1 || st.Sessions != 1 || st.Messages != 1 || st.Pending != 1 {
		t.Fatalf("stats = %+v", st)
	}
}

// The store must survive a restart with mail intact — durability is the whole
// reason this is SQLite rather than a map.
func TestMailSurvivesReopen(t *testing.T) {
	ctx, now := context.Background(), time.Now()
	dir := t.TempDir()
	path := filepath.Join(dir, "hub.db")

	db1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s1 := db1.Tenant(DefaultTenant)
	if _, err := s1.Send(ctx, msg("sess", "persist me"), now); err != nil {
		t.Fatal(err)
	}
	db1.Close()

	db2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	s2 := db2.Tenant(DefaultTenant)

	got, err := s2.Drain(ctx, "sess", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Body != "persist me" {
		t.Fatalf("after reopen drained %+v", got)
	}
}

// Audit and retrieval rows grow with USE, not time: every pane write, every
// login, every refused enrolment is a row. Only messages were ever expired, so
// these were the two tables that grew without bound — and adding an audited,
// rate-limited enrolment endpoint made it worse, since a grinder writes a row
// as fast as it is refused.
func TestVacuumTrimsAuditAndRetrievals(t *testing.T) {
	ctx, s, now := context.Background(), testStore(t), time.Now()

	old := now.Add(-AuditRetention - time.Hour)
	recent := now.Add(-time.Hour)

	// Two of each, one side of the cutoff apiece.
	if err := s.Audit(ctx, AuditEntry{Actor: "ancient", Action: "send"}, old); err != nil {
		t.Fatal(err)
	}
	if err := s.Audit(ctx, AuditEntry{Actor: "recent", Action: "send"}, recent); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordRetrieval(ctx, "ancient", "global", "q", "", old); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordRetrieval(ctx, "recent", "global", "q", "", recent); err != nil {
		t.Fatal(err)
	}

	res, err := s.s.Vacuum(ctx, now)
	if err != nil {
		t.Fatalf("vacuum: %v", err)
	}
	if res.Audit != 1 {
		t.Errorf("removed %d audit row(s), want 1", res.Audit)
	}
	if res.Retrievals != 1 {
		t.Errorf("removed %d retrieval(s), want 1", res.Retrievals)
	}

	// The point of a retention window is that everything inside it survives.
	// A vacuum that trimmed recent evidence would be worse than none: the audit
	// log is the answer to "who drove that pane", and it is only worth having
	// if you can trust what is still in it.
	entries, err := s.AuditTail(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Actor != "recent" {
		t.Fatalf("retention ate recent evidence: %+v", entries)
	}
}

// Retention is policy, so it must actually follow the configured value rather
// than a baked-in constant — `hub --audit-retention-days` sets this.
func TestAuditRetentionIsConfigurable(t *testing.T) {
	ctx, s, now := context.Background(), testStore(t), time.Now()

	saved := AuditRetention
	defer func() { AuditRetention = saved }()
	AuditRetention = time.Hour

	if err := s.Audit(ctx, AuditEntry{Actor: "two-hours-old", Action: "send"},
		now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	res, err := s.s.Vacuum(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Audit != 1 {
		t.Errorf("a 1h retention kept a 2h-old row: removed %d", res.Audit)
	}
}

// A self-declared status must survive the agent report that replaces every
// session row — which is why it is a separate table. A column on `sessions`
// would be erased every five seconds and the feature would look flaky rather
// than broken.
func TestSessionStatusSurvivesAgentReport(t *testing.T) {
	ctx, s, now := context.Background(), testStore(t), time.Now()

	s.SeenAgent(ctx, "wsl", "SHA256:x", "v1", now)
	s.UpsertSession(ctx, Session{SessionID: "s1", Agent: "wsl", Alias: "homelab"}, now)

	if err := s.SetSessionStatus(ctx, "s1", "waiting on the iptv peer", now); err != nil {
		t.Fatal(err)
	}
	// Exactly what an agent does every 5s: drop its view, then re-report it.
	if err := s.DropAgentSessions(ctx, "wsl"); err != nil {
		t.Fatal(err)
	}
	s.UpsertSession(ctx, Session{SessionID: "s1", Agent: "wsl", Alias: "homelab"}, now)

	got, err := s.ListSessions(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Note != "waiting on the iptv peer" {
		t.Fatalf("note lost across a report: %+v", got)
	}

	// Empty clears it — how a session says it finished rather than stopped.
	if err := s.SetSessionStatus(ctx, "s1", "  ", now); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListSessions(ctx, now); got[0].Note != "" {
		t.Errorf("blank status did not clear the note: %q", got[0].Note)
	}
}

// A session that sets a status and then dies would otherwise claim to be
// mid-task forever, and a peer would act on it.
func TestSessionStatusAgesOut(t *testing.T) {
	ctx, s, now := context.Background(), testStore(t), time.Now()
	s.UpsertSession(ctx, Session{SessionID: "s1", Agent: "wsl"}, now)
	s.SetSessionStatus(ctx, "s1", "building the index", now.Add(-statusTTL-time.Minute))

	got, err := s.ListSessions(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Note != "" {
		t.Errorf("a stale status is still being served: %q", got[0].Note)
	}
}

// Routing by domain is the whole premise: sessions are experts in projects, so
// an agent must be able to say "ask homelab" without first learning a
// hash-suffixed id it can only get by listing and string-matching itself.
func TestResolveSession(t *testing.T) {
	ctx, s, now := context.Background(), testStore(t), time.Now()

	for _, x := range []struct{ id, agent, project, alias, cwd string }{
		{"claude-homelab-wsl-1111", "wsl", "homelab", "homelab-wsl", "/home/a/homelab"},
		{"claude-homelife-wsl-2222", "wsl", "homelife", "homelife-wsl", "/home/a/homelife"},
		{"claude-iptv-wsl-3333", "wsl", "iptv", "iptv-wsl", "/home/a/iptv"},
		{"claude-iptv-mac-4444", "mac", "iptv", "iptv-mac", "/Users/a/iptv"},
	} {
		if err := s.UpsertSession(ctx, Session{
			SessionID: x.id, Agent: x.agent, Project: x.project, Alias: x.alias, CWD: x.cwd,
		}, now); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct{ want, expect, errIs string }{
		{"claude-homelab-wsl-1111", "claude-homelab-wsl-1111", ""}, // exact id
		{"homelab", "claude-homelab-wsl-1111", ""},                 // project name
		{"homelab-wsl", "claude-homelab-wsl-1111", ""},             // alias
		{"HOMELAB", "claude-homelab-wsl-1111", ""},                 // case-insensitive
		{"iptv", "", "ambiguous"},                                  // two hosts run it
		{"iptv-mac", "claude-iptv-mac-4444", ""},                   // disambiguated by alias
		{"homel", "", "ambiguous"},                                 // homelab + homelife
		{"nope", "", "no session matches"},
		// The cwd is deliberately NOT matched: every session on a Linux host
		// lives under /home/<user>, so this would otherwise hit everything.
		{"home/a", "", "no session matches"},
		// An offline session addressed precisely still gets its mail — the
		// delivery row is what makes it wait.
		{"claude-offline-dm-9999", "claude-offline-dm-9999", ""},
	} {
		got, err := s.ResolveSession(ctx, tc.want, now)
		if tc.errIs != "" {
			if err == nil || !strings.Contains(err.Error(), tc.errIs) {
				t.Errorf("resolve(%q) err = %v, want %q", tc.want, err, tc.errIs)
			}
			continue
		}
		if err != nil {
			t.Errorf("resolve(%q): %v", tc.want, err)
		} else if got != tc.expect {
			t.Errorf("resolve(%q) = %q, want %q", tc.want, got, tc.expect)
		}
	}
}

// A misaddressed message must bounce. Before resolution existed, Send stored a
// delivery row for whatever string it was handed — so a project name, which is
// the first thing anyone would try, produced a message that was stored,
// reported as sent, and drained by nobody.
func TestUnroutableRecipientBounces(t *testing.T) {
	ctx, s, now := context.Background(), testStore(t), time.Now()
	s.UpsertSession(ctx, Session{SessionID: "claude-homelab-wsl-1111",
		Agent: "wsl", Project: "homelab", Alias: "homelab-wsl"}, now)

	if _, err := s.ResolveSession(ctx, "hoemlab", now); err == nil {
		t.Fatal("a typo resolved successfully")
	} else if !strings.Contains(err.Error(), "homelab-wsl") {
		t.Errorf("the bounce does not name the candidates: %v", err)
	}
}

// A keystroke addressed by name must land on exactly one live pane, or refuse.
//
// This is the one place in the design where being wrong means text typed into a
// live session somebody else is using, so it resolves on the SERVER: a voice
// agent, a phone and the CLI each inventing a fuzzy-match rule is how the same
// phrase reaches different projects.
func TestResolvePane(t *testing.T) {
	ctx, s, now := context.Background(), testStore(t), time.Now()
	for _, x := range []struct{ id, agent, project, alias string }{
		{"claude-homelife-wsl-1", "wsl", "homelife", "homelife-wsl"},
		{"claude-homelife-mcp-wsl-2", "wsl", "homelife-mcp", "homelife-mcp-wsl"},
		{"claude-iptv-wsl-3", "wsl", "iptv", "iptv-wsl"},
	} {
		if err := s.UpsertSession(ctx, Session{SessionID: x.id, Agent: x.agent,
			Project: x.project, Alias: x.alias, TmuxSession: "claude", Index: 4}, now); err != nil {
			t.Fatal(err)
		}
	}

	// The exact case the iOS session raised: "homelife" is a prefix of two
	// sessions, and an exact project match must win rather than being called
	// ambiguous.
	pane, err := s.ResolvePane(ctx, "homelife", now)
	if err != nil {
		t.Fatalf("exact project match refused: %v", err)
	}
	if pane.SessionID != "claude-homelife-wsl-1" {
		t.Errorf("resolved to %s, want the exact match", pane.SessionID)
	}
	// It returns the coordinates a write needs, not just an id.
	if pane.Agent != "wsl" || pane.TmuxSession != "claude" || pane.Index != 4 {
		t.Errorf("pane coordinates incomplete: %+v", pane)
	}

	// A genuinely ambiguous stem refuses and names the candidates.
	if _, err := s.ResolvePane(ctx, "homel", now); err == nil {
		t.Error("an ambiguous name resolved instead of refusing")
	} else if !strings.Contains(err.Error(), "homelife-mcp-wsl") {
		t.Errorf("the refusal does not name the candidates: %v", err)
	}

	// Mail may be addressed to an offline session and wait for it. A keystroke
	// cannot wait for anything, so a well-formed id that is not live refuses.
	if _, err := s.ResolvePane(ctx, "claude-gone-dm-9", now); err == nil {
		t.Error("resolved a pane that is not live")
	}
}

// The Mail panel's whole job is telling a delivered handoff from a stored one.
// Before this, a message that nobody ever drained looked exactly like one that
// was picked up and acted on — which is the state you are in when you ask
// "did that reach homelab?".
func TestReplayReportsAcknowledgement(t *testing.T) {
	ctx, s, now := context.Background(), testStore(t), time.Now()

	for _, id := range []string{"claude-homelab-wsl-1", "claude-iptv-wsl-2"} {
		if err := s.UpsertSession(ctx, Session{
			SessionID: id, Agent: "wsl", Project: strings.Split(id, "-")[1], Alias: id,
		}, now); err != nil {
			t.Fatal(err)
		}
	}

	// Resolve first, exactly as the handler does — Send stores what it is
	// handed, and addressing by project is the handler's job.
	send := func(to, body string) {
		id, err := s.ResolveSession(ctx, to, now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Send(ctx, Envelope{
			FromSession: "claude-shabadoo-wsl-9", ToSession: id, Body: body}, now); err != nil {
			t.Fatal(err)
		}
	}
	send("homelab", "please look at this")
	send("iptv", "and this")

	byBody := func() map[string]Envelope {
		msgs, err := s.Replay(ctx, 50, now.Add(-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]Envelope{}
		for _, e := range msgs {
			m[e.Body] = e
		}
		return m
	}

	// Nothing drained yet: one recipient each, none acknowledged.
	for body, e := range byBody() {
		if e.Recipients != 1 || e.Acked != 0 || e.AckedAt != 0 {
			t.Errorf("%q before drain: recipients=%d acked=%d at=%d, want 1/0/0",
				body, e.Recipients, e.Acked, e.AckedAt)
		}
	}

	// homelab drains; iptv does not. That asymmetry is the whole feature — if
	// both read the same afterwards, the panel would be decoration.
	if _, err := s.Drain(ctx, "claude-homelab-wsl-1", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	got := byBody()
	if e := got["please look at this"]; e.Acked != 1 || e.AckedAt == 0 {
		t.Errorf("drained message reports acked=%d at=%d, want 1 and a timestamp", e.Acked, e.AckedAt)
	}
	if e := got["and this"]; e.Acked != 0 || e.AckedAt != 0 {
		t.Errorf("undrained message reports acked=%d at=%d, want 0/0", e.Acked, e.AckedAt)
	}

	// Conversation must agree with Replay. Two renderings of one fact drift,
	// and the drift is invisible until you are relying on the one you did not
	// happen to be looking at.
	thread, err := s.Conversation(ctx, "claude-homelab-wsl-1", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(thread) != 1 {
		t.Fatalf("thread has %d messages, want 1", len(thread))
	}
	if thread[0].Acked != 1 || thread[0].Recipients != 1 {
		t.Errorf("Conversation disagrees with Replay: %+v", thread[0])
	}
}

// An empty message must be refused, not delivered.
//
// From the field: three of these were accepted, stored, acknowledged with an id
// and delivered. The sender believed it had handed off work; the recipient got a
// notification containing nothing. It succeeded at every layer, which is what
// made it invisible — it was only caught because the recipient asked for a
// resend and got a second empty one.
func TestEmptyMessagesAreRefused(t *testing.T) {
	ctx, s, now := context.Background(), testStore(t), time.Now()

	if err := s.UpsertSession(ctx, Session{
		SessionID: "claude-target-1", Agent: "wsl", Project: "target", Alias: "target",
	}, now); err != nil {
		t.Fatal(err)
	}

	for _, body := range []string{"", "   ", "\n\t\n"} {
		_, err := s.Send(ctx, Envelope{ToSession: "claude-target-1", Body: body}, now)
		if !errors.Is(err, ErrEmptyMessage) {
			t.Errorf("Send with body %q returned %v, want ErrEmptyMessage", body, err)
		}
		if _, _, err := s.Broadcast(ctx, Envelope{Topic: "t", Body: body}, now); !errors.Is(err, ErrEmptyMessage) {
			t.Errorf("Broadcast with body %q returned %v, want ErrEmptyMessage", body, err)
		}
	}

	// A title is not a substitute. A subject line with no content is the same
	// failure in a smaller form: nothing to act on.
	if _, err := s.Send(ctx, Envelope{ToSession: "claude-target-1", Title: "just a title"}, now); !errors.Is(err, ErrEmptyMessage) {
		t.Errorf("a title-only message was accepted: %v", err)
	}

	// And nothing was stored by any of those attempts.
	msgs, err := s.Drain(ctx, "claude-target-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Errorf("%d empty message(s) reached the inbox", len(msgs))
	}

	// A real message still goes through — the guard must not refuse everything.
	if _, err := s.Send(ctx, Envelope{ToSession: "claude-target-1", Body: "real work"}, now); err != nil {
		t.Errorf("a normal message was refused: %v", err)
	}
}

// A node's session list must never be observable half-written.
//
// Reported from the field: `session_send to="minutes"` was refused as unknown
// while `minutes` was in `session_list`, and the refusal listed 8 of the 16
// live sessions as though that were the fleet. The cause was the report handler
// doing DELETE-then-N-upserts without a transaction, so any concurrent reader
// caught an arbitrary prefix. Agents report every 5 s and the node in question
// had 11 windows, so the exposed window was a real fraction of the time.
//
// The resolver is what made it visible, by turning an incomplete view into a
// confident claim about the world. The dashboard raced identically and merely
// flickered, which is why it went unnoticed there.
func TestReportedSessionsAreNeverHalfVisible(t *testing.T) {
	tn := testStore(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)

	const node, count = "wsl", 11
	sessions := make([]Session, count)
	for i := range sessions {
		sessions[i] = Session{
			SessionID: fmt.Sprintf("claude-p%d-wsl-0000000%d", i, i),
			Agent:     node,
			Project:   fmt.Sprintf("p%d", i),
			Alias:     fmt.Sprintf("p%d-wsl", i),
			Status:    "idle",
		}
	}
	if err := tn.ReplaceAgentSessions(ctx, node, sessions, now); err != nil {
		t.Fatal(err)
	}

	// Hammer the report path while reading, the way a live agent and a live
	// resolver do.
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				if err := tn.ReplaceAgentSessions(ctx, node, sessions, now); err != nil {
					return
				}
			}
		}
	}()
	defer func() { close(stop); <-done }()

	for i := 0; i < 300; i++ {
		got, err := tn.ListSessions(ctx, now)
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if len(got) != count {
			t.Fatalf("read %d saw %d of %d sessions — a reader observed a "+
				"half-written report, which is what makes the resolver refuse "+
				"a project that exists", i, len(got), count)
		}
	}
}

// The resolver must find a project by name while its node is reporting.
//
// The layer above the race, pinned separately: the store being consistent is
// only interesting because this is what depends on it, and addressing BY
// PROJECT is the documented primary path that broke.
func TestResolveFindsAProjectDuringAReport(t *testing.T) {
	tn := testStore(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)

	sessions := make([]Session, 11)
	for i := range sessions {
		sessions[i] = Session{
			SessionID: fmt.Sprintf("claude-p%d-wsl-0000000%d", i, i),
			Agent:     "wsl",
			Project:   fmt.Sprintf("p%d", i),
			Alias:     fmt.Sprintf("p%d-wsl", i),
			Status:    "idle",
		}
	}
	// The last one is the analogue of `minutes`: high window index, so it is
	// exactly the entry a truncated read loses.
	target := sessions[len(sessions)-1].Project

	if err := tn.ReplaceAgentSessions(ctx, "wsl", sessions, now); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				_ = tn.ReplaceAgentSessions(ctx, "wsl", sessions, now)
			}
		}
	}()
	defer func() { close(stop); <-done }()

	for i := 0; i < 200; i++ {
		if _, err := tn.ResolveSession(ctx, target, now); err != nil {
			t.Fatalf("attempt %d: project %q was refused while its node was "+
				"reporting: %v", i, target, err)
		}
	}
}

// Draining mail must not fail because something else wrote.
//
// Reported from the field: `POST /message/drain` returned "database is locked
// (517)" — SQLITE_BUSY_SNAPSHOT. A DEFERRED transaction takes its read snapshot
// at the first SELECT and asks for the write lock later, so a commit in between
// makes the snapshot unresolvably stale. busy_timeout does NOT cover this: there
// is no lock to wait for.
//
// Drain is exactly that shape — SELECT the mail, then ack it — and it started
// failing once agent reports became transactional and began committing a write
// every few seconds. The report was correct that drain is the call where this
// matters most, though not for the reason feared: the ack and the read are in
// one transaction, so a failure rolls back and acks nothing. The cost is a
// failed drain, not lost mail.
func TestDrainSurvivesConcurrentWrites(t *testing.T) {
	tn := testStore(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)

	sessions := make([]Session, 11)
	for i := range sessions {
		sessions[i] = Session{
			SessionID: fmt.Sprintf("claude-p%d-wsl-0000000%d", i, i),
			Agent:     "wsl",
			Project:   fmt.Sprintf("p%d", i),
			Alias:     fmt.Sprintf("p%d-wsl", i),
		}
	}
	const inbox = "claude-p0-wsl-00000000"

	// A writer committing constantly, as two reporting agents do.
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				_ = tn.ReplaceAgentSessions(ctx, "wsl", sessions, now)
			}
		}
	}()
	defer func() { close(stop); <-done }()

	for i := 0; i < 60; i++ {
		if _, err := tn.Send(ctx, Envelope{
			FromSession: "claude-p1-wsl-00000001", ToSession: inbox,
			Title: "t", Body: "b",
		}, now); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
		if _, err := tn.Drain(ctx, inbox, now); err != nil {
			t.Fatalf("drain %d failed while another writer was committing: %v\n"+
				"a deferred transaction cannot upgrade its snapshot, and "+
				"busy_timeout does not cover SQLITE_BUSY_SNAPSHOT", i, err)
		}
	}
}
