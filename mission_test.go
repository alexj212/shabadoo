package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func writeMission(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "MISSION.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The parse, and the fields a reader depends on.
func TestReadMission(t *testing.T) {
	dir := writeMission(t, `# The session framework itself.
status: blocked
updated: 2026-08-29

## Now
Making a project's state legible from outside it.

## Blocked on
waiting on the darwin set from mac

## Log
- 2026-08-29 shipped the convention
`)
	m := readMission(dir)
	if m == nil {
		t.Fatal("no mission parsed")
	}
	if m.Status != "blocked" {
		t.Errorf("status = %q", m.Status)
	}
	if m.Now != "Making a project's state legible from outside it." {
		t.Errorf("now = %q", m.Now)
	}
	if m.Blocked != "waiting on the darwin set from mac" {
		t.Errorf("blocked = %q", m.Blocked)
	}
	if m.Updated != "2026-08-29" {
		t.Errorf("updated = %q", m.Updated)
	}
}

// Absent and idle are different answers, and this is the pair that proves the
// parser can tell them apart rather than defaulting one to the other.
func TestReadMissionSeparatesAbsentFromSaid(t *testing.T) {
	if m := readMission(t.TempDir()); m != nil {
		t.Error("a project with no MISSION.md must report nothing, not an empty mission")
	}
	if m := readMission(""); m != nil {
		t.Error("no root must report nothing")
	}
	// A file that says nothing is the same as no file — otherwise every empty
	// template in a repo would claim its project had spoken.
	if m := readMission(writeMission(t, "\n\n## Log\n")); m != nil {
		t.Error("a file with no headline, status or Now must report nothing")
	}

	said := readMission(writeMission(t, "# x\nstatus: paused\n"))
	if said == nil || said.Status != "paused" {
		t.Fatal("a project that said 'paused' must be distinguishable from one that said nothing")
	}
	if said.Blocked != "" {
		t.Error("a paused project with no Blocked section must not report a blocker")
	}
}

// An unknown status is dropped rather than passed through: the set is closed so
// a client can switch on it, and a vocabulary that grows by typo stops being one.
func TestReadMissionRefusesAnUnknownStatus(t *testing.T) {
	m := readMission(writeMission(t, "# x\nstatus: in-progress\n\n## Now\nsomething\n"))
	if m == nil {
		t.Fatal("the rest of the file must still parse")
	}
	if m.Status != "" {
		t.Errorf("status = %q, want empty — 'in-progress' is not one of the four", m.Status)
	}
	if m.Now != "something" {
		t.Error("one bad field must not discard the others")
	}
}

// A malformed file must never break reporting for the projects beside it.
func TestReadMissionSurvivesGarbage(t *testing.T) {
	for _, body := range []string{
		"", "###\n", "status:\n", "# \n## Now\n## Blocked on\n",
		"binary\x00\x01\x02 nonsense",
	} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("panicked on %q: %v", body, r)
				}
			}()
			readMission(writeMission(t, body))
		}()
	}
}

// These ride every agent report, so one project writing an essay must not make
// the report expensive for the ten beside it.
func TestReadMissionClampsLongFields(t *testing.T) {
	long := ""
	for i := 0; i < 500; i++ {
		long += "word "
	}
	m := readMission(writeMission(t, "# x\nstatus: active\n\n## Now\n"+long+"\n"))
	if m == nil {
		t.Fatal("no mission")
	}
	if n := utf8.RuneCountInString(m.Now); n > 240 {
		t.Errorf("Now is %d runes; it rides every report", n)
	}
	if !utf8.ValidString(m.Now) {
		t.Error("clamping produced invalid UTF-8 — a byte cut split a character")
	}

	// The case a byte cut actually breaks: prose that is not ASCII.
	wide := ""
	for i := 0; i < 400; i++ {
		wide += "é—ü "
	}
	w := readMission(writeMission(t, "# x\nstatus: active\n\n## Now\n"+wide+"\n"))
	if w == nil || !utf8.ValidString(w.Now) {
		t.Error("clamping multi-byte prose produced invalid UTF-8")
	}
}

// The owner is what the whole grouping rests on, so what is pinned is that
// owners which MUST render differently do parse differently. A test asserting
// one line yields one owner passes just as happily when the parser has stopped
// telling any of them apart.
func TestWaitingSeparatesTheOwnersThatMustDiffer(t *testing.T) {
	m := readMission(writeMission(t, "# x\nstatus: active\n\n## Now\nn\n\n## Waiting on\n"+
		"- you: run the smoke test — untested integrated; 30s\n"+
		"- mac: darwin capture set\n"+
		"- nobody: background room audio\n"+
		"- an unattributed blocker nobody was named for\n"))
	if m == nil || len(m.Waiting) != 4 {
		t.Fatalf("want 4 entries, got %+v", m)
	}
	for i, want := range []string{"you", "mac", "nobody", ""} {
		if got := m.Waiting[i].Owner; got != want {
			t.Errorf("entry %d owner = %q, want %q", i, got, want)
		}
	}
	// The distinction that costs something when it collapses: an unattributed
	// blocker is not resolved work. If these ever render alike, a line nobody
	// has been named for disappears into the "needs no one" group.
	if m.Waiting[3].Owner == m.Waiting[2].Owner {
		t.Error(`unattributed and "nobody" must not be the same owner — one ` +
			`needs somebody, the other is a decision that it does not`)
	}
	if m.Waiting[0].Item != "run the smoke test — untested integrated; 30s" {
		t.Errorf("owner not stripped from item: %q", m.Waiting[0].Item)
	}
}

// Blocked is DERIVED from the list, which is what keeps the two from
// disagreeing — and it must skip the group that is deliberately unblocked.
func TestBlockedSkipsWorkNobodyIsWaitingOn(t *testing.T) {
	m := readMission(writeMission(t, "# x\nstatus: active\n\n## Now\nn\n\n## Waiting on\n"+
		"- nobody: background room audio\n- mac: darwin capture set\n"))
	if m.Blocked != "mac: darwin capture set" {
		t.Errorf(`Blocked = %q — a "nobody" entry is open work, not a blocker`, m.Blocked)
	}
}

// Prose contains colons far more often than it contains owners, so the
// discriminator is a SINGLE SHORT TOKEN before one. That is syntactic, and its
// limit is worth pinning rather than papering over: a one-word prose lead-in is
// indistinguishable from an owner, and nothing on a node knows which names are
// sessions. So "note:" reads as an owner, and that is accepted rather than
// guessed around — a heuristic trying to tell them apart would be confidently
// wrong on the case it cannot see, which is worse than a rule an author learns
// once.
func TestWaitingOwnerIsAShortLeadingToken(t *testing.T) {
	long := strings.Repeat("x", 40)
	for _, c := range []struct{ line, owner, item string }{
		{"shipped the dialect: the iOS client is unblocked", "", "shipped the dialect: the iOS client is unblocked"},
		{"mac: darwin capture set", "mac", "darwin capture set"},
		{"note: this one is fine", "note", "this one is fine"}, // THE BOUNDARY
		{long + ": tail", "", long + ": tail"},
	} {
		m := readMission(writeMission(t, "# x\nstatus: active\n\n## Now\nn\n\n## Waiting on\n- "+c.line+"\n"))
		if m == nil || len(m.Waiting) != 1 {
			t.Fatalf("%q: want 1 entry, got %+v", c.line, m)
		}
		if m.Waiting[0].Owner != c.owner || m.Waiting[0].Item != c.item {
			t.Errorf("%q -> owner %q item %q; want owner %q item %q",
				c.line, m.Waiting[0].Owner, m.Waiting[0].Item, c.owner, c.item)
		}
	}
}

// These ride EVERY agent report, every five seconds, for every project.
func TestWaitingIsBounded(t *testing.T) {
	body := "# x\nstatus: active\n\n## Now\nn\n\n## Waiting on\n"
	for i := 0; i < 40; i++ {
		body += "- you: " + strings.Repeat("é", 400) + "\n"
	}
	m := readMission(writeMission(t, body))
	if len(m.Waiting) > 6 {
		t.Errorf("got %d entries, want at most 6", len(m.Waiting))
	}
	for _, w := range m.Waiting {
		if n := utf8.RuneCountInString(w.Item); n > 120 {
			t.Errorf("item is %d runes, want at most 120", n)
		}
		if !utf8.ValidString(w.Item) {
			t.Error("clamp emitted invalid UTF-8 — cut on a byte boundary")
		}
	}
}

// Dropped, truncated and intact are THREE states and none may render as another.
// Asserting only that a long file yields six rows passes for the parser that
// silently discarded the rest — which is the behaviour being replaced.
func TestWaitingReportsWhatItDiscarded(t *testing.T) {
	body := "# x\nstatus: active\n\n## Now\nn\n\n## Waiting on\n" +
		"- you: intact\n" +
		"- you: " + strings.Repeat("c", 400) + "\n"
	for i := 0; i < 6; i++ { // takes it past the six-row cap
		body += "- you: filler\n"
	}
	m := readMission(writeMission(t, body))

	if len(m.Waiting) != 6 {
		t.Fatalf("want 6 reported, got %d", len(m.Waiting))
	}
	if m.Dropped != 2 {
		t.Errorf("Dropped = %d, want 2 — a row discarded without being counted "+
			"is a blocker present in the file, absent from the dashboard, and "+
			"findable by nobody", m.Dropped)
	}
	if m.Waiting[0].Truncated {
		t.Error("an intact row must not be marked truncated")
	}
	if !m.Waiting[1].Truncated {
		t.Error("a cut row must say so; the reader cannot see what was removed")
	}
	// The distinction itself: present-but-shortened is not the same answer as
	// not-here-at-all, and a build that reported one number for both would
	// satisfy every assertion above except this one.
	if m.Dropped > 0 && !m.Waiting[1].Truncated {
		t.Error("dropped and truncated must be independently observable")
	}
}

// An empty line is not a discarded row.
func TestBlankLinesDoNotCountAsDropped(t *testing.T) {
	m := readMission(writeMission(t, "# x\nstatus: active\n\n## Now\nn\n\n## Waiting on\n"+
		"- you: one\n\n\n- you: two\n\n"))
	if m.Dropped != 0 {
		t.Errorf("Dropped = %d, want 0 — blank lines would inflate the count "+
			"and make the warning cry wolf", m.Dropped)
	}
}

// The client asked for exactly one property from this payload: two entries on
// the same date must be distinguishable, so a phone can keep its own watermark
// and render "seven new since Tuesday" with no server-side read tracking.
//
// So what is pinned is the DISTINCTION, not the hash. Asserting a known id would
// pass for an implementation that returned the same id for everything.
func TestLogIDsSeparateEntriesThatMustDiffer(t *testing.T) {
	m := readMission(writeMission(t, "# x\nstatus: active\n\n## Now\nn\n\n## Log\n"+
		"- 2026-08-29 shipped the paging dialect\n"+
		"- 2026-08-29 nudges were dead for ten hours\n"+ // SAME DATE
		"- 2026-08-28 shipped the paging dialect\n"+ // SAME TEXT
		"- undated but still an entry\n"))
	if len(m.Log) != 4 {
		t.Fatalf("want 4 entries, got %d: %+v", len(m.Log), m.Log)
	}
	seen := map[string]int{}
	for i, e := range m.Log {
		if e.ID == "" {
			t.Fatalf("entry %d has no id; the watermark cannot work", i)
		}
		if prev, dup := seen[e.ID]; dup {
			t.Errorf("entries %d and %d share id %s — same-date or same-text "+
				"entries must be distinguishable", prev, i, e.ID)
		}
		seen[e.ID] = i
	}
	if m.Log[0].Date != "2026-08-29" || m.Log[0].Text != "shipped the paging dialect" {
		t.Errorf("date not split from text: %+v", m.Log[0])
	}
	// Undated is a state, not a reason to drop the line.
	if m.Log[3].Date != "" || m.Log[3].Text != "undated but still an entry" {
		t.Errorf("undated entry mangled: %+v", m.Log[3])
	}
}

// An id must be STABLE across an append, or every prepend marks the whole
// history unseen and the watermark is worse than useless.
func TestLogIDsSurviveAnAppend(t *testing.T) {
	body := "# x\nstatus: active\n\n## Now\nn\n\n## Log\n- 2026-08-28 older line\n"
	before := readMission(writeMission(t, body))
	after := readMission(writeMission(t,
		"# x\nstatus: active\n\n## Now\nn\n\n## Log\n- 2026-08-29 newer line\n- 2026-08-28 older line\n"))
	if before.Log[0].ID != after.Log[1].ID {
		t.Errorf("id changed when an entry was prepended (%s -> %s); a positional "+
			"id would do this, and it marks every old entry as new",
			before.Log[0].ID, after.Log[1].ID)
	}
}

// Clamped at the client's request, and the true length travels with it.
func TestLogClampsAndSaysSo(t *testing.T) {
	long := strings.Repeat("é", 400)
	m := readMission(writeMission(t, "# x\nstatus: active\n\n## Now\nn\n\n## Log\n"+
		"- 2026-08-29 short\n- 2026-08-29 "+long+"\n"))
	if m.Log[0].Truncated {
		t.Error("a short line must not be marked truncated")
	}
	e := m.Log[1]
	if !e.Truncated {
		t.Error("a cut line must say so; a client cannot tell short from cut")
	}
	if e.Length != 400 {
		t.Errorf("Length = %d, want the TRUE length 400 — the point is to report "+
			"what was removed, not what remains", e.Length)
	}
	if n := utf8.RuneCountInString(e.Text); n > 200 {
		t.Errorf("clamped to %d runes, want at most 200", n)
	}
	if !utf8.ValidString(e.Text) {
		t.Error("clamp cut a multi-byte rune")
	}
}

// Declared, absent, and the closed-set states around it. What is pinned is that
// an owner survives and that ABSENT stays absent — a parser defaulting the owner
// to anything would satisfy a test that only checked the declared case, and
// "nobody declared" is the state the dashboard's warning depends on.
func TestOwnerIsReadAndAbsenceIsPreserved(t *testing.T) {
	with := readMission(writeMission(t,
		"# x\nstatus: active\nowner: minutes-mac\nupdated: 2026-08-29\n\n## Now\nn\n"))
	if with.Owner != "minutes-mac" {
		t.Errorf("Owner = %q, want minutes-mac", with.Owner)
	}
	without := readMission(writeMission(t, "# x\nstatus: active\n\n## Now\nn\n"))
	if without.Owner != "" {
		t.Errorf("Owner = %q for a file that declares none — absent must stay "+
			"absent, or a project with no writer is indistinguishable from one "+
			"whose writer is agreed", without.Owner)
	}
	if with.Status != "active" || without.Status != "active" {
		t.Error("the owner key must not disturb the other header keys")
	}
}

// A hand-written MISSION.md wraps, and a wrapped entry is ONE entry.
//
// Pinned as a pair rather than as an example, because either half passes alone:
// a parser that joins everything gives the same count as one that joins wraps,
// and a parser that joins nothing gives the same TEXT for an unwrapped file.
// The distinction is that two bullets and one wrapped bullet must not produce
// the same thing — which is exactly what the old parser did, and why a real
// blocker was reported as dropped to make room for the second half of the row
// above it.
func TestWrappedEntriesAreOneEntry(t *testing.T) {
	wrapped := readMission(writeMission(t, `# p
status: active

## Waiting on
- you: the first half of a single item
  and the second half of that same item
- mac: a different item entirely
`))
	split := readMission(writeMission(t, `# p
status: active

## Waiting on
- you: the first half of a single item
- and the second half of that same item
- mac: a different item entirely
`))
	if wrapped == nil || split == nil {
		t.Fatal("both files must parse")
	}
	if len(wrapped.Waiting) == len(split.Waiting) {
		t.Fatalf("a wrapped entry and two bullets must not parse alike: both gave %d",
			len(wrapped.Waiting))
	}
	if len(wrapped.Waiting) != 2 {
		t.Fatalf("wrapped: want 2 entries, got %d: %+v", len(wrapped.Waiting), wrapped.Waiting)
	}
	if len(split.Waiting) != 3 {
		t.Fatalf("split: want 3 entries, got %d", len(split.Waiting))
	}
	// The continuation must land in the entry it belongs to, not merely vanish.
	if !strings.Contains(wrapped.Waiting[0].Item, "second half") {
		t.Fatalf("continuation lost: %q", wrapped.Waiting[0].Item)
	}
	// And the entry keeps its owner — the defect rendered the continuation as
	// an unattributed blocker, which is a row nobody wrote and nobody can answer.
	if wrapped.Waiting[0].Owner != "you" || wrapped.Waiting[1].Owner != "mac" {
		t.Fatalf("owners wrong: %+v", wrapped.Waiting)
	}
}

// The same rule for the log, and the id is what makes it matter: the id is a
// hash of the entry's text, so an id computed before the continuation was read
// would key the entry to half of itself and every client's "since I last
// looked" watermark would move on a reflow that changed nothing.
func TestWrappedLogEntriesAreOneEntry(t *testing.T) {
	wrapped := readMission(writeMission(t, `# p
status: active

## Log
- 2026-08-29 shipped the thing, and here is the rest
  of that same sentence continuing on
- 2026-08-28 an earlier thing
`))
	if wrapped == nil {
		t.Fatal("must parse")
	}
	if len(wrapped.Log) != 2 {
		t.Fatalf("want 2 log entries, got %d: %+v", len(wrapped.Log), wrapped.Log)
	}
	if !strings.Contains(wrapped.Log[0].Text, "same sentence") {
		t.Fatalf("continuation lost: %q", wrapped.Log[0].Text)
	}
	if wrapped.Log[0].Date != "2026-08-29" {
		t.Fatalf("date lost: %q", wrapped.Log[0].Date)
	}
	// The joined text is what the id is keyed to. Same content wrapped
	// differently is the same entry, which is the property a watermark needs.
	unwrapped := readMission(writeMission(t, `# p
status: active

## Log
- 2026-08-29 shipped the thing, and here is the rest of that same sentence continuing on
- 2026-08-28 an earlier thing
`))
	if unwrapped.Log[0].ID != wrapped.Log[0].ID {
		t.Fatalf("reflow changed the id: %s vs %s", wrapped.Log[0].ID, unwrapped.Log[0].ID)
	}
}

// A wrapped row must not spend a slot against the six-row cap. This is the
// defect as it actually presented: a file with five real blockers reported one
// as dropped, so the dashboard both hid a real row and said a row was hidden —
// for a row that did not exist.
func TestWrappingDoesNotSpendTheCap(t *testing.T) {
	var b strings.Builder
	b.WriteString("# p\nstatus: active\n\n## Waiting on\n")
	for i := 0; i < 6; i++ {
		b.WriteString("- you: item number here\n  wrapped onto a second line\n")
	}
	m := readMission(writeMission(t, b.String()))
	if m == nil {
		t.Fatal("must parse")
	}
	if len(m.Waiting) != 6 {
		t.Fatalf("want 6 entries, got %d", len(m.Waiting))
	}
	if m.Dropped != 0 {
		t.Fatalf("six wrapped rows fit the six-row cap; reported %d dropped", m.Dropped)
	}
}

// A scoped session reports ITS OWN mission, not the repo's.
//
// Asserted as a pair on recon-wsl's request, and the request is the point: a
// fixture saying "the child reports the child's card" passes just as happily
// when the resolver has gone blind and hands the same card to everything. What
// has to be true is that the parent and the child produce DIFFERENT cards —
// which is exactly what went red here, and what no single-sided fixture would
// have caught.
//
// The cost of the version this replaces was measured on a live fleet: seven
// sessions under one repo all advertising the parent's card, three of them
// saying `done` on disk while reading as `active`, and the parent's one blocker
// counted seven times while six of the children's were shown nowhere.
func TestScopedSessionReportsItsOwnMission(t *testing.T) {
	root := t.TempDir()
	// A project root is a CLAUDE.md at a git root, which is what projectRoot
	// looks for; without both, the walk passes straight over it.
	mustWrite(t, filepath.Join(root, "CLAUDE.md"), "# parent\n")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "MISSION.md"),
		"# parent mission\nstatus: active\n\n## Waiting on\n- you: the parent's blocker\n")

	child := filepath.Join(root, "missions", "recon")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(child, "MISSION.md"),
		"# child mission\nstatus: done\n\n## Waiting on\n- mac: the child's own blocker\n")

	parent := missionFor(root, projectRoot(root))
	scoped := missionFor(child, projectRoot(child))
	if parent == nil || scoped == nil {
		t.Fatal("both must parse")
	}
	// The distinction, not one side of it.
	if parent.Headline == scoped.Headline {
		t.Fatalf("parent and child report the same card: %q", parent.Headline)
	}
	if scoped.Status != "done" {
		t.Fatalf("child status: want done, got %q — finished work reading as in-flight "+
			"is the failure `done` exists to prevent", scoped.Status)
	}
	if len(scoped.Waiting) != 1 || scoped.Waiting[0].Owner != "mac" {
		t.Fatalf("child blockers wrong: %+v", scoped.Waiting)
	}

	// And a scoped session with NO mission of its own still falls back to the
	// root, because most sessions are not scoped and must keep behaving as they
	// do today.
	bare := filepath.Join(root, "missions", "nothing-here")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	fell := missionFor(bare, projectRoot(bare))
	if fell == nil || fell.Headline != parent.Headline {
		t.Fatalf("unscoped fallback broken: %+v", fell)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
