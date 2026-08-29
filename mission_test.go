package main

import (
	"os"
	"unicode/utf8"
	"path/filepath"
	"testing"
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
