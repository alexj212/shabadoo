package hub

import (
	"context"
	"strings"
	"testing"
	"time"
)

// deferFixture stands up a hub whose node has a core session, and returns the
// tenant plus that core session's id.
//
// No agent is connected, deliberately: nudge early-exits at the online check,
// so the warning is still SENT and readable while nothing tries to type into a
// pane that does not exist.
type deferEnv struct {
	h     *Hub
	tn    *Tenant
	core  string
	p     stoppedProject
	clock *time.Time
}

func deferFixture(t *testing.T) *deferEnv {
	t.Helper()
	tn := testStore(t)
	h := New(&Authorizer{}, tn.s)

	// A controllable clock, because `created_at` is unix SECONDS and the read
	// path orders by it with no tiebreaker — two sends inside one tick are
	// unorderable, so "the newest warning" is arbitrary between them. That is a
	// property of this fixture rather than of the feature, and it cost a
	// confusing red before it was diagnosed rather than guessed at.
	clock := new(time.Time)
	*clock = time.Now()
	h.now = func() time.Time { return *clock }
	now := h.now()

	core := "claude-wsl-core"
	if err := tn.ReplaceAgentSessions(context.Background(), "wsl", []Session{
		{SessionID: core, Agent: "wsl", Project: "wsl", Kind: KindCore},
	}, now); err != nil {
		t.Fatal(err)
	}
	return &deferEnv{h: h, tn: tn, core: core, clock: clock, p: stoppedProject{
		Node: "wsl", Path: "/c/projects/devops", Project: "devops",
		SessionID: "claude-devops-stopped", Deactivated: true,
	}}
}

// warn mirrors the real call site: the mail is STORED against the session it
// would have, and only then is the core session asked. Counting after the send
// is what makes the queued figure include this message.
func (d *deferEnv) warn(t *testing.T, env Envelope) {
	t.Helper()
	*d.clock = d.clock.Add(2 * time.Second) // keep successive rows orderable
	env.ToSession = d.p.SessionID
	if _, err := d.tn.Send(context.Background(), env, d.h.now()); err != nil {
		t.Fatal(err)
	}
	if err := d.h.askCoreToStart(context.Background(), DefaultTenant, d.p, env); err != nil {
		t.Fatal(err)
	}
}

func (d *deferEnv) lastWarning(t *testing.T) Envelope {
	t.Helper()
	msgs, err := d.tn.Conversation(context.Background(), d.core, 50)
	if err != nil || len(msgs) == 0 {
		t.Fatalf("no warning reached the core session (err %v)", err)
	}
	return msgs[0] // newest first
}

// The warning has to carry the SUBJECT, because the decision it asks for needs
// it. Without one a core session can only guess or wake the project to find
// out: measured at three of its own turns plus a peer's, to learn a fact the
// coordinator held when it warned.
//
// Pinned as a pair. "The body names the title" passes just as happily for a
// warning that dumps the whole message in, so the control asserts the BODY is
// still withheld — a subject informs a decision, a body delivers the handoff to
// a session that has not agreed to take it.
func TestStoppedWarningNamesTheSubjectAndNotTheBody(t *testing.T) {
	d := deferFixture(t)
	d.warn(t, Envelope{
		FromSession: "claude-dev-env-wsl", Title: "Three DNS doc corrections",
		Body: "SECRET-PAYLOAD-DO-NOT-INLINE", Type: "info", Tag: "dns",
	})

	got := d.lastWarning(t)
	if !strings.Contains(got.Body, "Three DNS doc corrections") {
		t.Errorf("warning does not name the subject, which is the fact the "+
			"decision needs:\n%s", got.Body)
	}
	if strings.Contains(got.Body, "SECRET-PAYLOAD-DO-NOT-INLINE") {
		t.Errorf("warning inlined the message body; a subject informs a "+
			"decision, a body delivers the work:\n%s", got.Body)
	}
	for _, want := range []string{"info", "dns"} {
		if !strings.Contains(got.Body, want) {
			t.Errorf("warning omits %q, which the sender set", want)
		}
	}
}

// An absent title SAYS it is absent. A blank line there reads as "the
// coordinator did not tell me" — empty rendered as unknown, inside the one
// message whose entire job is to inform a judgment.
func TestUntitledMailSaysSoRatherThanRenderingBlank(t *testing.T) {
	d := deferFixture(t)
	d.warn(t, Envelope{FromSession: "claude-peer", Body: "no title on this one"})

	got := d.lastWarning(t)
	if !strings.Contains(got.Body, "no title") {
		t.Errorf("an untitled message must say so, not render an empty "+
			"subject:\n%s", got.Body)
	}
	if strings.Contains(got.Body, "Subject: \n") {
		t.Errorf("blank subject line rendered:\n%s", got.Body)
	}
}

// Two warnings for one project must be TELLABLE APART. Ninety seconds apart and
// byte-identical, the reader could not distinguish a retry from a follow-up and
// correctly reported that to a human as an unknown.
//
// The pair is the point: asserting the second warning says "2" passes for an
// implementation that always says 2, so the first is asserted to differ from it.
func TestSecondWarningIsDistinguishableFromTheFirst(t *testing.T) {
	d := deferFixture(t)

	d.warn(t, Envelope{FromSession: "a", Title: "First thing", Body: "one"})
	first := d.lastWarning(t)

	d.warn(t, Envelope{FromSession: "b", Title: "Second thing", Body: "two"})
	second := d.lastWarning(t)

	if first.Title == second.Title && first.Body == second.Body {
		t.Fatal("two warnings are byte-identical: a retry and a follow-up " +
			"cannot be told apart, which is the reported defect")
	}
	if !strings.Contains(second.Title, "2") {
		t.Errorf("second warning does not carry the queued count in its title: %q",
			second.Title)
	}
	if strings.Contains(first.Title, "2") {
		t.Errorf("first warning claims a count of 2 with one message queued: %q",
			first.Title)
	}
}

// A count that could not be taken is UNKNOWN, never zero and never omitted.
// "Nothing else is waiting" and "I could not look" lead to opposite decisions.
func TestUncountablePendingReportsUnknown(t *testing.T) {
	d := deferFixture(t)
	d.tn.s.Close() // the store is gone; the count cannot be taken

	line := d.h.queuedLine(context.Background(), DefaultTenant, d.p)
	if !strings.Contains(line, "unknown") {
		t.Errorf("a failed count must read as unknown, not as none: %q", line)
	}
}
