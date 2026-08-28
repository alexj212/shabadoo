package main

import (
	"os"
	"path/filepath"
	"testing"
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
