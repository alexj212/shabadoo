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
