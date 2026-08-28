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
	"os/exec"
	"path/filepath"
	"runtime"
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
	pids map[int]toolState // pane pid -> what its MCP child is serving
}{}

const staleScanEvery = time.Minute

// staleToolPanes reports which panes hold an MCP child serving an older tool
// surface than this build advertises.
//
// No longer gated on the build stamp. That gate existed because the comparison
// WAS a clock: an unstamped binary could not order itself against anything, so
// reporting nothing was the honest answer. The child now states what it serves
// and this compares strings, so an unstamped build is as capable of answering
// as any other.
func staleToolPanes() map[int]toolState {
	staleToolCache.mu.Lock()
	defer staleToolCache.mu.Unlock()
	if time.Since(staleToolCache.at) < staleScanEvery && staleToolCache.pids != nil {
		return staleToolCache.pids
	}
	want := toolSurfaceHash()
	state := defaultStateDir()
	out := panesToolState(processTable(time.Now()), func(pid int) toolState {
		return surfaceOf(state, pid, want)
	})
	staleToolCache.at, staleToolCache.pids = time.Now(), out
	return out
}

// panesToolState maps a pane to what its MCP child is serving, keeping all
// three answers.
//
// panesWithSurface flattens to "stale or not", which is what the report needed
// when there were only two answers. Collapsing unknown into not-stale there
// would rebuild the exact defect this replaces — a check that cannot look
// rendering as a check that found nothing wrong.
func panesToolState(table []procInfo, state func(pid int) toolState) map[int]toolState {
	out := map[int]toolState{}
	for st, flag := range map[toolState]bool{toolStale: true, toolCurrent: true, toolUnknown: true} {
		_ = flag
		for pid := range panesWithSurface(table, func(p int) toolState {
			if state(p) == st {
				return toolStale // reuse the ancestor walk, asking one state at a time
			}
			return toolCurrent
		}) {
			// Worst answer wins: a pane with any stale child is stale; one with
			// no stale child but an unestablished one is unknown.
			if st > out[pid] {
				out[pid] = st
			}
		}
	}
	return out
}

// panesWithSurface maps each pane to what its MCP child is serving.
//
// The clock is gone from the decision. It used to ask "was this child started
// before the binary was built", which is true of every session after any
// upgrade — measured at 11 of 11 and then 12 of 12, across three releases that
// did not touch the tool list at all. A flag true of everything is not
// actionable, and telling somebody to restart on it spends a session's context
// for nothing.
//
// Now the child says what it serves and this compares strings. Three answers,
// and the third is the reason to build it this way: a child that predates the
// mechanism, or whose identity cannot be established, is UNKNOWN — never
// current, never stale.
func panesWithSurface(table []procInfo, state func(pid int) toolState) map[int]bool {
	out := map[int]bool{}
	by := make(map[int]procInfo, len(table))
	for _, p := range table {
		by[p.pid] = p
	}
	for _, p := range table {
		if !isMCPChild(p.args) || state(p.pid) != toolStale {
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
		for anc, hop := p, 0; hop < 6; hop++ {
			if anc.ppid <= 1 {
				break
			}
			out[anc.ppid] = true
			next, ok := by[anc.ppid]
			if !ok {
				break
			}
			anc = next
		}
	}
	return out
}

// isMCPChild recognises `…/shabadoo mcp`, however the process table spelled it.
func isMCPChild(args []string) bool {
	return len(args) >= 2 && filepath.Base(args[0]) == "shabadoo" && args[1] == "mcp"
}

// procInfo is one process, as much of it as staleness needs.
type procInfo struct {
	pid, ppid int
	started   time.Time
	args      []string
}

// processTable reads every process on this host.
//
// Two sources, because there are two kinds of host in this fleet and a
// /proc-only reader does NOT fail on macOS — it returns nothing, which becomes
// `tools_stale: false` on every session and reads as "all clean". That is worse
// than having no detector at all: nobody goes looking behind a clean answer.
// Found when the mac reported 0 of 5 sessions stale while wsl reported 11 of 11,
// which is not a plausible difference between two machines upgraded minutes
// apart.
//
// `ps` is the fallback on Linux too, not only the macOS path. If /proc is
// unreadable for some reason the honest move is to try the other reader rather
// than report a clean fleet.
func processTable(now time.Time) []procInfo {
	if runtime.GOOS == "linux" {
		if t := procTableProc(); len(t) > 0 {
			return t
		}
	}
	return procTablePS(now)
}

// procTablePS reads the process table by shelling out, which is this codebase's
// existing answer to "the OS knows something Go does not" — it already shells
// out to tmux and to tailscale.
func procTablePS(now time.Time) []procInfo {
	out, err := exec.Command("ps", "-Ao", "pid=,ppid=,etime=,command=").Output()
	if err != nil {
		return nil
	}
	return parsePS(string(out), now)
}

// parsePS turns `ps -Ao pid=,ppid=,etime=,command=` into a table.
//
// Elapsed time rather than start time deliberately: `ps -o lstart=` emits a
// locale-formatted date that has to be parsed back, and this only ever feeds a
// comparison against a build stamp hours or days away, so seconds-since-start
// is both simpler and impossible to misparse into a wrong year.
func parsePS(out string, now time.Time) []procInfo {
	var table []procInfo
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		ppid, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		elapsed, ok := parseETime(f[2])
		if !ok {
			continue
		}
		table = append(table, procInfo{
			pid: pid, ppid: ppid, started: now.Add(-elapsed), args: f[3:],
		})
	}
	return table
}

// parseETime reads ps's elapsed-time field, `[[dd-]hh:]mm:ss`.
func parseETime(s string) (time.Duration, bool) {
	var days int
	if i := strings.IndexByte(s, '-'); i >= 0 {
		d, err := strconv.Atoi(s[:i])
		if err != nil {
			return 0, false
		}
		days, s = d, s[i+1:]
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	var secs int
	for _, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 {
			return 0, false
		}
		secs = secs*60 + v
	}
	return time.Duration(days)*24*time.Hour + time.Duration(secs)*time.Second, true
}

// procTableProc reads the table from /proc, which is cheaper than a subprocess
// and is what the overwhelming majority of this fleet runs.
func procTableProc() []procInfo {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	boot := bootTime()
	var table []procInfo
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		cmd, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		ppid, started, ok := procStart(pid, boot)
		if !ok {
			continue
		}
		// cmdline is NUL-separated; "shabadoo\0mcp" is the shape we want.
		table = append(table, procInfo{
			pid: pid, ppid: ppid, started: started,
			args: strings.Split(strings.TrimRight(string(cmd), "\x00"), "\x00"),
		})
	}
	return table
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
