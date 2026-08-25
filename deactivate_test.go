package main

import (
	"os"
	"path/filepath"
	"testing"
)

// withStateDir points the boot list (and therefore the deactivated list beside
// it) at a temp directory. Nothing here may touch the real one: this machine's
// boot list decides what actually starts, and a test that edited it would
// change what happens at the next reboot.
func withStateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLAUDE_SESSIONS_LIST", filepath.Join(dir, "folders"))
	if got := filepath.Dir(deactivatedPath()); got != dir {
		t.Fatalf("state dir is %s, not the temp dir — refusing to touch the real one", got)
	}
	return dir
}

func TestDeactivationRoundTrip(t *testing.T) {
	withStateDir(t)
	proj := t.TempDir()

	if isDeactivated(proj) {
		t.Fatal("a folder is deactivated before anything happened to it")
	}

	markDeactivated(proj)
	if !isDeactivated(proj) {
		t.Fatal("closing a session was not recorded")
	}

	// Twice must not duplicate: the agent reports every few seconds and could
	// see the same absence more than once across a restart.
	markDeactivated(proj)
	list, _ := readBootList(deactivatedPath())
	if len(list) != 1 {
		t.Errorf("recorded %d entries for one folder: %v", len(list), list)
	}

	// Explicitly starting it forgets the intent. The file says "do not start
	// this on your own", never "refuse to start this".
	clearDeactivated(proj)
	if isDeactivated(proj) {
		t.Error("opening the folder did not clear its deactivation")
	}
}

// Paths are compared through symlinks, for the same reason folder discovery is:
// one source holds the path somebody typed and the other holds a resolved one,
// so a string match misses and the machine reopens a session just closed.
func TestDeactivationMatchesThroughSymlinks(t *testing.T) {
	withStateDir(t)
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks unavailable here")
	}

	markDeactivated(link)
	if !isDeactivated(real) {
		t.Error("deactivated via a symlink, not recognised via the real path")
	}
	clearDeactivated(real)
	if isDeactivated(link) {
		t.Error("cleared via the real path, still deactivated via the symlink")
	}
}

// An unreadable or absent list means nothing is deactivated. Failing open is
// the safe direction: the cost is a session reopening, which is exactly
// today's behaviour, while failing closed would silently stop a machine
// starting anything.
func TestMissingListMeansNothingIsDeactivated(t *testing.T) {
	withStateDir(t)
	if isDeactivated(t.TempDir()) {
		t.Error("a folder is deactivated with no list on disk")
	}
	if isDeactivated("") {
		t.Error("an empty path reported as deactivated")
	}
}
