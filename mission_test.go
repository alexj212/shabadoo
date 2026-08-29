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
