package hub

import (
	"strings"
	"testing"
	"time"
)

// Publishing is ~70 MB across four platforms into a directory the nightly borg
// run covers, so unbounded growth is disk in every backup forever.
func TestReleasesPruneToKeepVersions(t *testing.T) {
	s, err := OpenReleaseStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1_700_000_000, 0)
	platforms := []string{"linux/amd64", "darwin/arm64"}

	for i := 0; i < keepVersions+3; i++ {
		for _, p := range platforms {
			if _, err := s.Publish(versionName(i), p,
				strings.NewReader("binary"), base.Add(time.Duration(i)*time.Minute)); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Each platform is pruned independently: one platform publishing often must
	// not evict another's only build.
	for _, p := range platforms {
		n := 0
		for _, rel := range s.List() {
			if rel.Platform == p {
				n++
			}
		}
		if n != keepVersions {
			t.Errorf("%s has %d versions, want %d", p, n, keepVersions)
		}
	}

	// The newest survives and the oldest is gone — pruning the wrong end would
	// delete exactly what every node is about to be told to install.
	newest := versionName(keepVersions + 2)
	if _, _, ok := s.Get(newest, "linux/amd64"); !ok {
		t.Errorf("pruned the newest release %s", newest)
	}
	if _, _, ok := s.Get(versionName(0), "linux/amd64"); ok {
		t.Errorf("kept the oldest release")
	}

	// A reopened store must agree, or a restart resurrects entries whose files
	// are gone.
	again, err := OpenReleaseStore(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.List()) != keepVersions*len(platforms) {
		t.Errorf("after reopen: %d releases, want %d",
			len(again.List()), keepVersions*len(platforms))
	}
}

func versionName(i int) string { return string(rune('a'+i)) + "000000" }

// "Reported none" and "could not tell us" must not render the same.
//
// The rule this pins was extracted by a peer session after three instances of
// the same shape shipped in one evening: a broadcast that reached zero
// recipients and said nothing, a resolver that listed the half of the fleet it
// could see as though that were the fleet, and a staleness detector that
// reported clean on the platform it could not inspect. Each took "I could not
// see" and rendered it as "there is nothing there".
//
// An empty capability list is the same trap: equally an old build that cannot
// report and a machine with nothing to report. A router reading the first when
// the second is true declines work a host could have done.
func TestCapabilitiesDistinguishUnknownFromNone(t *testing.T) {
	h := &Hub{byNode: map[string]*conn{}}

	h.byNode[nodeKey("t1", "modern")] = &conn{protocol: ProtocolCurrent, caps: nil}
	h.byNode[nodeKey("t1", "ancient")] = &conn{protocol: ProtocolBase, caps: nil}

	// Both report no capabilities...
	if got := h.NodeCapabilities("t1", "modern"); len(got) != 0 {
		t.Fatalf("modern node reported %v", got)
	}
	if got := h.NodeCapabilities("t1", "ancient"); len(got) != 0 {
		t.Fatalf("ancient node reported %v", got)
	}
	// ...but only one of them was asked in a language it speaks.
	if !h.CapabilitiesKnown("t1", "modern") {
		t.Error("a negotiating agent's empty list should be believable as 'none'")
	}
	if h.CapabilitiesKnown("t1", "ancient") {
		t.Error("a pre-negotiation build's silence was reported as a real answer — " +
			"this is the bug shape, not a missing feature")
	}

	// An offline node is not 'none' either; it is not there at all.
	if h.CapabilitiesKnown("t1", "absent") {
		t.Error("an unconnected node claimed its capabilities were known")
	}
}
