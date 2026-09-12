package hub

import (
	"strings"
	"testing"
	"time"
)

// With no version asked for, the NEWEST published set wins — every time.
//
// The iteration count is the test. The defect was a synthesized lookup key that
// omitted Component, so it never matched, and the selection collapsed to "take
// whatever the map yields first". Go randomises map order, so a single pass had
// roughly even odds of passing by luck against two candidates. Fifty passes do
// not.
//
// Measured cost: a node was handed a set 48 commits stale, over a newer local
// build, moments after a newer set was published — and the install reported
// itself as normal.
func TestToolSetPicksTheNewestNotAnArbitraryOne(t *testing.T) {
	s, err := OpenReleaseStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1_700_000_000, 0)
	const plat = "darwin/arm64"

	for _, c := range []string{"minutes", "minutes-capture"} {
		if _, err := s.PublishComponent("minutes", c, "stale-96e3ef3", plat,
			strings.NewReader("old"), base); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []string{"minutes", "minutes-capture"} {
		if _, err := s.PublishComponent("minutes", c, "fresh-ee5ec50", plat,
			strings.NewReader("new"), base.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}

	for i := 0; i < 50; i++ {
		set, ok := s.ToolSet("minutes", "", plat)
		if !ok {
			t.Fatalf("pass %d: no set found at all", i)
		}
		if len(set) != 2 {
			t.Fatalf("pass %d: got %d components, want the whole set of 2", i, len(set))
		}
		if set[0].Version != "fresh-ee5ec50" {
			t.Fatalf("pass %d: chose %q over the newer fresh-ee5ec50 — selection is "+
				"order-dependent, not newest-wins", i, set[0].Version)
		}
	}

	// The control: an EXPLICIT version must still win, including the old one.
	// Without this, a selector hardwired to return the newest passes the loop
	// above and silently ignores what the operator asked for.
	set, ok := s.ToolSet("minutes", "stale-96e3ef3", plat)
	if !ok || len(set) != 2 || set[0].Version != "stale-96e3ef3" {
		t.Fatalf("an explicitly requested version must be honoured, got ok=%v set=%v", ok, set)
	}
}
