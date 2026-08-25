package node

import (
	"os/exec"
	"sort"
	"testing"
)

// Sorted and stable. This value is stored and compared, so a set that reordered
// itself between logins would read as a change on every reconnect.
func TestCapabilitiesAreSortedAndStable(t *testing.T) {
	got := Capabilities()
	if !sort.StringsAreSorted(got) {
		t.Errorf("capabilities are not sorted: %v", got)
	}
	again := Capabilities()
	if len(again) != len(got) {
		t.Fatalf("two calls disagreed: %d then %d", len(got), len(again))
	}
	for i := range got {
		if got[i] != again[i] {
			t.Fatalf("two calls disagreed at %d: %q vs %q", i, got[i], again[i])
		}
	}
}

// It must report what is actually here. A capability list that is right in
// principle and wrong on this machine would route work to a host that cannot do
// it — which fails at the far end, after the handoff, where it is most
// expensive to diagnose.
func TestCapabilitiesMatchWhatIsInstalled(t *testing.T) {
	have := map[string]bool{}
	for _, c := range Capabilities() {
		have[c] = true
	}
	for name, bin := range software {
		_, err := exec.LookPath(bin)
		if err == nil && !have[name] {
			t.Errorf("%s is installed (%s) but not reported", name, bin)
		}
		if err != nil && have[name] {
			t.Errorf("%s is reported but %s is not on PATH", name, bin)
		}
	}
}

// Presence only, deliberately: no versions. Establishing one means executing
// every binary and parsing output each tool formats differently, to answer a
// question routing does not ask.
func TestCapabilitiesCarryNoVersions(t *testing.T) {
	for _, c := range Capabilities() {
		for _, ch := range c {
			if ch >= '0' && ch <= '9' && c != "yt-dlp" {
				t.Errorf("capability %q looks like it carries a version", c)
				break
			}
		}
	}
}
