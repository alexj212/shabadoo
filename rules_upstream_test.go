package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A payload skill that another repo owns must be reported as MATCHING or
// DIFFERING, and the two must be distinguishable.
//
// Pinned as a pair on purpose. A comparator that has gone blind — reading
// nothing and calling it equal — passes a fixture that only ever asserts "the
// vendored copy matches". The differing case is what catches it, and it is the
// case that actually shipped: a credential-handling rule hand-edited into the
// snapshot while the file its own project calls authoritative never got it.
func TestUpstreamComparesBothDirections(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "minutes")
	if err := os.MkdirAll(filepath.Join(repo, "skills", "minutes"), 0o755); err != nil {
		t.Fatal(err)
	}
	up := filepath.Join(repo, "skills", "minutes", "SKILL.md")
	if err := os.WriteFile(up, []byte("the source of record\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	roots := map[string]string{"minutes": repo}
	rel := filepath.Join("skills", "minutes", "SKILL.md")

	t.Run("identical is reported as matching", func(t *testing.T) {
		same, differ := compareUpstream(map[string][]byte{rel: []byte("the source of record\n")}, roots)
		if len(same) != 1 || len(differ) != 0 {
			t.Fatalf("vendored copy identical to source: same=%v differ=%v", same, differ)
		}
	})

	t.Run("a hand edit to the payload copy is reported as differing", func(t *testing.T) {
		same, differ := compareUpstream(map[string][]byte{rel: []byte("the source of record\nplus a rule only here\n")}, roots)
		if len(differ) != 1 || len(same) != 0 {
			t.Fatalf("payload edited away from source: same=%v differ=%v", same, differ)
		}
	})

	// No project of that name on this host is UNKNOWN, never "matches". It is
	// the same distinction payload_known draws, and collapsing it here would
	// report a stranger's machine — which has no sibling repos at all — as
	// verified.
	t.Run("no owning repo is neither match nor differ", func(t *testing.T) {
		same, differ := compareUpstream(map[string][]byte{rel: []byte("anything\n")}, map[string]string{})
		if len(same) != 0 || len(differ) != 0 {
			t.Fatalf("nothing to compare against: same=%v differ=%v", same, differ)
		}
	})
}
