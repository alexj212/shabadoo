package main

// Sessions holding a tool surface older than the binary.
//
// Each Claude session launches `shabadoo mcp` as a child at START, and that
// child advertises its tool list once. Upgrading the binary does nothing for it:
// the session keeps the surface it was born with until the window is recycled.
//
// So a release that adds a tool reaches nobody already running, and the failure
// is invisible from exactly where it matters — inside the affected session,
// which has no way to know its own surface is behind. It was found by a session
// being told about three new tools, trying one, and not finding it.
//
// The fix is a restart. What was missing is knowing a restart is needed, which
// is what this reports.
//
// See docs/build-plan.md.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// staleToolCache holds the last scan. Recomputed on a slow timer rather than
// every report: this changes only when the binary is replaced or a window is
// recycled, and walking every process five times a second to learn a fact that
// changes twice a week would be a poor trade.
var staleToolCache = struct {
	mu   sync.Mutex
	at   time.Time
	pids map[int]bool // pane pid -> its tool surface predates this build
}{}

const staleScanEvery = time.Minute

// staleToolPanes reports which panes hold an MCP child older than this build.
//
// Returns nothing when the build carries no timestamp. An unstamped binary
// cannot order itself against anything, and guessing would mark every session
// stale — advice to recycle a whole fleet, from a comparison that never made.
func staleToolPanes() map[int]bool {
	if buildTime == "" {
		return nil
	}
	built, err := time.Parse(time.RFC3339, buildTime)
	if err != nil {
		return nil
	}
	staleToolCache.mu.Lock()
	defer staleToolCache.mu.Unlock()
	if time.Since(staleToolCache.at) < staleScanEvery && staleToolCache.pids != nil {
		return staleToolCache.pids
	}
	out := staleToolPanesSince(built)
	staleToolCache.at, staleToolCache.pids = time.Now(), out
	return out
}

// staleToolPanesSince is the testable half.
//
// Split out because `go test` does not apply the Makefile's ldflags, so
// buildTime is empty under test and the wrapper above returns immediately —
// which made a first attempt at this report zero stale sessions on a host with
// eleven, and look like a mapping bug rather than a harness one.
func staleToolPanesSince(built time.Time) map[int]bool {

	out := map[int]bool{}
	boot := bootTime()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return out
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		cmd, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		// cmdline is NUL-separated; "shabadoo\0mcp" is the shape we want.
		args := strings.Split(strings.TrimRight(string(cmd), "\x00"), "\x00")
		if len(args) < 2 || filepath.Base(args[0]) != "shabadoo" || args[1] != "mcp" {
			continue
		}
		_, started, ok := procStart(pid, boot)
		if !ok || !started.Before(built) {
			continue
		}
		// Walk UP, do not assume the parent.
		//
		// tmux reports a pane's pid as the shell it started, and the tree is
		// `pane shell -> claude -> shabadoo mcp` — so the MCP process is a
		// grandchild, not a child. Keying on the immediate parent found nothing
		// at all, on a host where eleven sessions were demonstrably stale.
		//
		// Bounded, because the chain ends at init and marking init would mark
		// every pane on the machine.
		for anc, hop := pid, 0; hop < 6; hop++ {
			ppid, _, ok := procStart(anc, boot)
			if !ok || ppid <= 1 {
				break
			}
			out[ppid] = true
			anc = ppid
		}
	}
	return out
}

// procStart reads a process's parent and start time from /proc/<pid>/stat.
//
// Field 22 is start time in clock ticks since boot. The comm field (2) may
// contain spaces and parentheses, so parsing starts after the last ')' rather
// than splitting the whole line — the classic way to misread this file.
func procStart(pid int, boot time.Time) (ppid int, started time.Time, ok bool) {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, time.Time{}, false
	}
	i := strings.LastIndexByte(string(raw), ')')
	if i < 0 {
		return 0, time.Time{}, false
	}
	f := strings.Fields(string(raw)[i+1:])
	if len(f) < 20 {
		return 0, time.Time{}, false
	}
	ppid, err = strconv.Atoi(f[1]) // field 4 overall
	if err != nil {
		return 0, time.Time{}, false
	}
	ticks, err := strconv.ParseInt(f[19], 10, 64) // field 22 overall
	if err != nil {
		return 0, time.Time{}, false
	}
	// 100 ticks per second is the near-universal Linux configuration, and being
	// a few seconds out cannot matter for a comparison against a build stamp
	// that is hours or days away.
	return ppid, boot.Add(time.Duration(ticks) * time.Second / 100), true
}

// bootTime reads when this kernel started, so a process's tick offset can be
// turned into a wall-clock time.
func bootTime() time.Time {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if rest, found := strings.CutPrefix(line, "btime "); found {
			if secs, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64); err == nil {
				return time.Unix(secs, 0)
			}
		}
	}
	return time.Time{}
}
