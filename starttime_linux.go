//go:build linux

package main

// Exact process start time, as a stable token.
//
// This is a KEY, not a clock. It exists so a record written by one process
// cannot be inherited by an unrelated one that happened to get the same pid:
// pids recycle, and a stale record read as current manufactures a confident
// wrong answer — strictly worse than the "unknown" it should have produced,
// because nothing downstream can tell it from a real one.
//
// Field 22 of /proc/<pid>/stat is starttime in clock ticks since boot: an exact
// integer, never re-derived, and stable for the life of the process. Elapsed
// time (what `ps -o etime=` gives, and what this file replaces for keying) moves
// every second, so a key built from it would fail to match itself between two
// scans and turn every child into "unknown" on alternate reads — which looks
// like the detector working while being just as broken.

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// processStartToken returns an opaque, host-stable identity for a running pid.
//
// Opaque on purpose: the two platforms report different units, and nothing
// compares these across hosts. Only equality on the same machine is meaningful.
func processStartToken(pid int) (string, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", fmt.Errorf("%w: /proc/%d/stat: %v", ErrIndeterminate, pid, err)
	}
	// comm (field 2) is parenthesised and may itself contain spaces or
	// parentheses, so fields are counted from the LAST ')' rather than split
	// from the start. A process named "sh (old)" is not hypothetical.
	i := strings.LastIndex(string(raw), ")")
	if i < 0 {
		return "", fmt.Errorf("%w: malformed stat for %d", ErrIndeterminate, pid)
	}
	fields := strings.Fields(string(raw)[i+1:])
	// After comm, field 3 is state; starttime is field 22 overall, so index 19
	// in what remains.
	const startIdx = 19
	if len(fields) <= startIdx {
		return "", fmt.Errorf("%w: stat for %d has %d fields after comm",
			ErrIndeterminate, pid, len(fields))
	}
	ticks, err := strconv.ParseUint(fields[startIdx], 10, 64)
	if err != nil {
		return "", fmt.Errorf("%w: starttime for %d: %v", ErrIndeterminate, pid, err)
	}
	return strconv.FormatUint(ticks, 10), nil
}
