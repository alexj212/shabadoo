package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Reading takes the NEAREST card; writing scaffolds where you stand.
//
// Pinned as a PAIR because the defect returned the same answer for both: a
// resolver that hands back the project root passes any assertion made only
// about a project-root session, and every nested mission then prints its
// parent's card. Fourteen missions here are nested, and the session that
// reported it could self-check only through `session_list` — because the
// command built for that question was answering about a different file.
func TestMissionDirReadsNearestAndWritesHere(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "missions", "software")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(dir, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "MISSION.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(root, "# parent\nstatus: active\n")
	write(nested, "# child\nstatus: paused\n")

	parentRead := missionDirFrom(root, root, "read")
	childRead := missionDirFrom(nested, root, "read")

	if childRead != nested {
		t.Errorf("nested read resolved to %q, want its own dir %q", childRead, nested)
	}
	// The discriminating assertion. Without it, a resolver that always returns
	// the root satisfies nothing above and reintroduces the bug exactly.
	if parentRead == childRead {
		t.Fatalf("parent and child resolved to the SAME dir (%q) — this is the "+
			"defect: every nested mission shows its parent's card", childRead)
	}

	// A nested dir with no card of its own must NOT silently adopt the
	// parent's. It reports nothing, which is the honest answer.
	bare := filepath.Join(root, "missions", "empty")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := missionDirFrom(bare, root, "read"); got == root {
		t.Errorf("a dir with no card adopted the parent's (%q)", got)
	}

	// Writing always scaffolds where you are standing, card or no card.
	if got := missionDirFrom(nested, root, "write"); got != nested {
		t.Errorf("write resolved to %q, want %q — scaffolding into the parent "+
			"creates the collision the read side just stopped falling into", got, nested)
	}
	if got := missionDirFrom(bare, root, "write"); got != bare {
		t.Errorf("write into an empty dir resolved to %q, want %q", got, bare)
	}
}
