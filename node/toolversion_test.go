package node

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// An install reports what LANDED, so the thing it asks must answer honestly —
// and must fail rather than guess when it cannot.
//
// Pinned as a pair because the defect this replaces was a confident wrong
// answer: the install echoed the REQUESTED version back, so a node printed
// "installed v0.1.0-17-gee5ec50" while the binary on disk was a different build
// entirely. A version source that cannot fail reproduces exactly that.
func TestToolVersionReportsWhatTheBinarySaysOrFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX")
	}
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("it reports what the binary says", func(t *testing.T) {
		p := write("good", `echo '{"version":"v0.1.0-17-gee5ec50","built":"2026-09-11T00:00:00Z"}'`)
		got, built, err := toolVersion(context.Background(), p)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "v0.1.0-17-gee5ec50" {
			t.Errorf("reported %q, want the version the binary printed", got)
		}
		if built != "2026-09-11T00:00:00Z" {
			t.Errorf("built = %q, want the timestamp the binary printed", built)
		}
	})

	t.Run("unparseable output is an error, never a guess", func(t *testing.T) {
		p := write("garbage", `echo 'not json at all'`)
		if got, _, err := toolVersion(context.Background(), p); err == nil {
			t.Errorf("returned %q for unparseable output; an unverified install "+
				"must not render as a verified one", got)
		}
	})

	t.Run("a version-less manifest is an error too", func(t *testing.T) {
		p := write("noversion", `echo '{"built":"2026-09-11T00:00:00Z"}'`)
		if got, _, err := toolVersion(context.Background(), p); err == nil {
			t.Errorf("returned %q with no version field", got)
		}
	})

	t.Run("a binary that will not run is an error", func(t *testing.T) {
		if _, _, err := toolVersion(context.Background(), filepath.Join(dir, "absent")); err == nil {
			t.Error("no error for a binary that does not exist")
		}
	})
}

// compareBuilds decides whether an install moved forward or backward, so the
// case that matters most is the one where it CANNOT tell. A missing or
// unparseable stamp must come back unknown. Reporting it as "forward" would be
// the 37-commit silent revert again, one layer down.
//
// Forward and backward are a pair over the same two stamps, swapped, so an
// implementation that ignores the order and always answers one way fails.
// "same" uses one instant written in two offsets, because the stamps are
// compared as times, not strings.
func TestCompareBuildsOrdersByTimeAndAdmitsWhenItCannot(t *testing.T) {
	const older, newer = "2026-09-01T00:00:00Z", "2026-09-29T20:51:02-04:00"

	for _, c := range []struct {
		name, prev, next string
		want             string
		wantKnown        bool
	}{
		{"newer build is forward", older, newer, "forward", true},
		{"older build is backward", newer, older, "backward", true},
		{"one instant in two offsets is same", "2026-09-30T00:51:02Z", newer, "same", true},
		{"no previous stamp is unknown", "", newer, "", false},
		{"no new stamp is unknown", older, "", "", false},
		{"an unparseable stamp is unknown", "last tuesday", newer, "", false},
		{"a date with no time is unknown", "2026-09-01", newer, "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, known := compareBuilds(c.prev, c.next)
			if got != c.want || known != c.wantKnown {
				t.Errorf("compareBuilds(%q, %q) = (%q, %v), want (%q, %v)",
					c.prev, c.next, got, known, c.want, c.wantKnown)
			}
		})
	}
}
