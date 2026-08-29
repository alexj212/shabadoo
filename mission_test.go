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
