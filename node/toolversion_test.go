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
		got, err := toolVersion(context.Background(), p)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "v0.1.0-17-gee5ec50" {
			t.Errorf("reported %q, want the version the binary printed", got)
		}
	})

	t.Run("unparseable output is an error, never a guess", func(t *testing.T) {
		p := write("garbage", `echo 'not json at all'`)
		if got, err := toolVersion(context.Background(), p); err == nil {
			t.Errorf("returned %q for unparseable output; an unverified install "+
				"must not render as a verified one", got)
		}
	})

	t.Run("a version-less manifest is an error too", func(t *testing.T) {
		p := write("noversion", `echo '{"built":"2026-09-11T00:00:00Z"}'`)
		if got, err := toolVersion(context.Background(), p); err == nil {
			t.Errorf("returned %q with no version field", got)
		}
	})

	t.Run("a binary that will not run is an error", func(t *testing.T) {
		if _, err := toolVersion(context.Background(), filepath.Join(dir, "absent")); err == nil {
			t.Error("no error for a binary that does not exist")
		}
	})
}
