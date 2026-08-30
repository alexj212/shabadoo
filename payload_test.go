package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// "Nothing pending" and "could not look" must never render alike. This is the
// whole reason PayloadState carries Known: a node that failed to check would
// otherwise report zero pending and read exactly like a node that is current.
func TestPayloadStateSeparatesCleanFromUnknown(t *testing.T) {
	if st := scanPayload(""); st.Known {
		t.Error("no claude dir means cannot tell, not clean")
	}

	// A directory that does not exist is answerable: every payload file is
	// simply absent, so setup would install all of them.
	missing := scanPayload(filepath.Join(t.TempDir(), "nope"))
	if !missing.Known {
		t.Fatal("an absent directory is a knowable answer, not an unknown one")
	}
	if missing.Pending == 0 {
		t.Error("every payload file is pending against an empty target")
	}
}

// After installing the payload, nothing is pending. This is the property that
// makes the badge trustworthy — the same one `doctor` reporting zero changes on
// a correct host gives.
func TestPayloadStateIsCleanAfterInstall(t *testing.T) {
	dir := t.TempDir()
	payload, err := mergePayloads()
	if err != nil {
		t.Fatalf("mergePayloads: %v", err)
	}
	for rel, content := range payload {
		dst := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st := scanPayload(dir)
	if !st.Known {
		t.Fatal("a fully installed target must be knowable")
	}
	if st.Pending != 0 {
		t.Errorf("pending = %d after installing every file, want 0", st.Pending)
	}

	// And a single edited file must be seen. Without this the test above passes
	// for a scanner that always answers zero.
	for rel := range payload {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte("changed"), 0o644); err != nil {
			t.Fatal(err)
		}
		break
	}
	if got := scanPayload(dir); got.Pending != 1 {
		t.Errorf("pending = %d after editing one file, want 1", got.Pending)
	}
}

// A file edited after this binary was built must survive a payload install.
//
// The binary's payload is a SNAPSHOT: `make vendor` copies the live ~/.claude
// into the overlay by hand, so a file edited after the build is newer than the
// copy inside it. Installing then reverts a real edit to a stale one, on a
// restart nobody connected to the change. Found with a skill corrected at 12:32
// whose vendored copy was from 14 May — the next restart would have thrown away
// three and a half months and logged it as an install.
//
// Pinned as the DISTINCTION, because either half passes alone: an installer
// that never writes anything satisfies the survival case, and one that always
// writes satisfies the upgrade case. Both must hold in the same test.
func TestInstallKeepsWhatWasEditedAfterTheBuild(t *testing.T) {
	dir := t.TempDir()
	built := time.Now()

	// Older than the build: an ordinary upgrade, and it MUST be replaced —
	// otherwise "protecting edits" quietly becomes "never updating anything".
	stale := filepath.Join(dir, "stale.md")
	mustWriteFile(t, stale, "old content")
	if err := os.Chtimes(stale, built.Add(-2*time.Hour), built.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if newerThanBuild(stale, built) {
		t.Fatal("a file older than the build must not be treated as an edit")
	}

	// Newer than the build: somebody corrected it after this binary was made,
	// so the binary's copy is the stale one.
	edited := filepath.Join(dir, "edited.md")
	mustWriteFile(t, edited, "a correction made after this build")
	if err := os.Chtimes(edited, built.Add(time.Hour), built.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !newerThanBuild(edited, built) {
		t.Fatal("a file modified after the build must be protected — otherwise a " +
			"node restart silently reverts somebody's edit to a vendored snapshot")
	}

	// An unstamped build cannot establish the order, and cannot-tell must not
	// render as "mine is newer". The safe side is keeping the file: a false
	// keep leaves slightly stale guidance, which payload_pending already
	// reports; a false overwrite destroys work.
	if !newerThanBuild(stale, time.Time{}) {
		t.Fatal("with no build stamp the order is unknown, so the file must be kept")
	}

	// And a file that does not exist yet is never "an edit" — the fresh-machine
	// case must never be blocked.
	if newerThanBuild(filepath.Join(dir, "absent.md"), built) {
		t.Fatal("a missing file must be installed, not protected")
	}
}

func mustWriteFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
