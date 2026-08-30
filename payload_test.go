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
// whose vendored copy was from 14 May.
//
// Pinned as the DISTINCTION, because either half passes alone: an installer
// that never writes satisfies the survival case, and one that always writes
// satisfies the upgrade case. Both must hold in the same test.
func TestInstallKeepsWhatWasEditedAfterTheBuild(t *testing.T) {
	dir := t.TempDir()
	built := time.Now()

	// Older than the build: an ordinary upgrade, and it MUST be replaced —
	// otherwise "protecting edits" quietly becomes "never updating anything".
	stale := filepath.Join(dir, "stale.md")
	if err := os.WriteFile(stale, []byte("old content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(stale, built.Add(-2*time.Hour), built.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if newerThanBuild(stale, built) {
		t.Fatal("a file older than the build must not be treated as an edit")
	}

	// Newer than the build: somebody corrected it after this binary was made,
	// so the binary's copy is the stale one.
	edited := filepath.Join(dir, "edited.md")
	if err := os.WriteFile(edited, []byte("a correction made after this build"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(edited, built.Add(time.Hour), built.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !newerThanBuild(edited, built) {
		t.Fatal("a file modified after the build must be protected — otherwise a " +
			"node restart silently reverts somebody's edit to a vendored snapshot")
	}

	// An unstamped build cannot establish the order, and cannot-tell must not
	// render as "mine is newer". A false keep leaves slightly stale guidance,
	// which payload_pending already reports; a false overwrite destroys work.
	if !newerThanBuild(stale, time.Time{}) {
		t.Fatal("with no build stamp the order is unknown, so the file must be kept")
	}

	// A file that does not exist yet is never "an edit": the fresh-machine case
	// must never be blocked.
	if newerThanBuild(filepath.Join(dir, "absent.md"), built) {
		t.Fatal("a missing file must be installed, not protected")
	}
}

// The named list is capped and sorted; the COUNT never is.
//
// Tested against capDrift directly rather than through a scan, because this
// build embeds six payload files and maxDrift is six — a test that waits for a
// seventh SKIPS, and a check that only ever skips is exactly as useless as one
// that only ever fails. That is the finding a peer filed this morning, met an
// hour later in my own test.
func TestDriftIsCappedAndSorted(t *testing.T) {
	long := []string{"z.md", "a.md", "m.md", "b.md", "y.md", "c.md", "x.md", "d.md"}
	got := capDrift(append([]string(nil), long...), 3)
	if len(got) != 3 {
		t.Fatalf("want 3 names, got %d: %v", len(got), got)
	}
	for i, want := range []string{"a.md", "b.md", "c.md"} {
		if got[i] != want {
			t.Fatalf("cap must take the SORTED head: got %v", got)
		}
	}
	// A list under the cap comes back whole, so "capped" never quietly becomes
	// "truncated to a fixed size".
	if g := capDrift([]string{"b.md", "a.md"}, 3); len(g) != 2 || g[0] != "a.md" {
		t.Fatalf("a short list must come back whole and sorted: %v", g)
	}
}

// Drift NAMES the files, and the count stays the total.
//
// A count of 1 stood on a real node while the file behind it was a skill whose
// vendored copy was three and a half months stale — and nothing on any surface
// said WHICH file, so nobody looked.
//
// Pinned as the pair that must differ: a clean tree names nothing, a dirty one
// names the file that is dirty. Asserting only the second passes for a scanner
// that always reports every file.
func TestDriftNamesTheFilesAndKeepsTheTotal(t *testing.T) {
	dir := t.TempDir()
	payload, err := mergePayloads()
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) == 0 {
		t.Skip("no embedded payload in this build")
	}
	for rel, body := range payload {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	clean := scanPayload(dir)
	if !clean.Known {
		t.Fatal("a fully installed tree must be knowable")
	}
	if clean.Pending != 0 || len(clean.Drift) != 0 {
		t.Fatalf("clean tree reports %d pending, drift %v", clean.Pending, clean.Drift)
	}

	// Edit exactly ONE file. The count and the name must both move, together.
	var victim string
	for rel := range payload {
		victim = rel
		break
	}
	if err := os.WriteFile(filepath.Join(dir, victim), []byte("edited by hand"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirty := scanPayload(dir)
	if dirty.Pending != 1 {
		t.Fatalf("one edit must be one pending, got %d", dirty.Pending)
	}
	if len(dirty.Drift) != 1 || dirty.Drift[0] != victim {
		t.Fatalf("drift must NAME the edited file: got %v, want [%s]", dirty.Drift, victim)
	}
	if len(clean.Drift) == len(dirty.Drift) {
		t.Fatal("a clean tree and a dirty one must not name the same number of files")
	}
}
