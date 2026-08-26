package main

import (
	"runtime"
	"testing"
	"time"
)

// A session's MCP tool surface is fixed when the session starts, so upgrading
// the binary reaches nothing already running. The failure is invisible from
// inside the affected session — which is where it matters — and was found only
// by a session being told about three new tools and not finding them.
//
// This runs against the real process table, because the thing being tested is
// whether the mapping from an MCP process to a pane is right on a live machine.
// A fixture would have agreed with whatever I assumed, and the first two
// attempts assumed wrongly: `shabadoo mcp` is a GRANDCHILD of the pane process
// (pane shell -> claude -> mcp), so keying on the immediate parent found
// nothing at all on a host with eleven demonstrably stale sessions.
func TestStaleToolDetection(t *testing.T) {
	// Every process on this machine started before "now", so anything running
	// an MCP child must be flagged.
	current := staleToolPanesSince(time.Now())

	// ...and nothing predates a stamp from 2001, so the same scan must be empty.
	// Without this the test would pass on a function that returned every pid it
	// saw, which is the failure mode a one-directional check cannot see.
	ancient := staleToolPanesSince(time.Unix(1_000_000_000, 0))

	if len(ancient) != 0 {
		t.Errorf("an impossibly old build stamp flagged %d panes — the comparison "+
			"is not being made", len(ancient))
	}
	if len(current) == 0 {
		t.Skip("no shabadoo mcp processes running here, so there is nothing to detect")
	}
	t.Logf("%d pane(s) hold a tool surface older than a just-built binary", len(current))
}

// An unstamped binary cannot order itself against anything. Guessing would mark
// every session stale, which is advice to recycle a whole fleet drawn from a
// comparison that never happened.
func TestUnstampedBuildFlagsNothing(t *testing.T) {
	prev := buildTime
	buildTime = ""
	defer func() { buildTime = prev }()

	if got := staleToolPanes(); got != nil {
		t.Errorf("an unstamped build flagged %d panes", len(got))
	}
}

// The detector was /proc-only, with no build tag, so on macOS os.ReadDir("/proc")
// simply failed and the empty result became `tools_stale: false` on every
// session — a whole node reporting "all clean" when it had not looked.
//
// It surfaced as mac reporting 0 of 5 stale while wsl reported 11 of 11, minutes
// after both were upgraded. That is not a plausible difference between two
// machines; it is one of them not measuring. A detector that answers "fine" when
// it means "I cannot tell" is worse than an absent one, because nobody checks
// behind a clean answer.
//
// So the macOS reader is pinned against real `ps` output rather than trusted.
func TestPSTableFindsMCPChildren(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)

	// `ps -Ao pid=,ppid=,etime=,command=` as macOS emits it: right-aligned
	// numbers, an absolute command path, and etime in three different widths.
	const sample = `  501     1    03:11:07 /bin/zsh --login
  733   501 2-04:22:10 claude --dangerously-skip-permissions
  899   733    11:12:00 /Users/someone/bin/shabadoo mcp
  950     1       04:31 /usr/bin/ssh-agent -l
`
	table := parsePS(sample, now)
	if len(table) != 4 {
		t.Fatalf("parsed %d processes, want 4: %+v", len(table), table)
	}

	// Elapsed time is turned into a start time, and the three width variants
	// must all land in the right place.
	want := map[int]time.Duration{
		501: 3*time.Hour + 11*time.Minute + 7*time.Second,
		733: 2*24*time.Hour + 4*time.Hour + 22*time.Minute + 10*time.Second,
		899: 11*time.Hour + 12*time.Minute,
		950: 4*time.Minute + 31*time.Second,
	}
	for _, p := range table {
		if got := now.Sub(p.started); got != want[p.pid] {
			t.Errorf("pid %d started %v ago, want %v", p.pid, got, want[p.pid])
		}
	}

	// The whole point: the mcp process is found, and the pane shell above it is
	// what gets flagged — not the mcp process, and not claude.
	stale := stalePanesIn(table, now)
	if !stale[501] {
		t.Error("the pane shell was not flagged; the ancestor walk did not reach it")
	}
	if !stale[733] {
		t.Error("claude, the intermediate hop, was not flagged")
	}
	if stale[950] {
		t.Error("an unrelated process was flagged")
	}
	if stale[1] {
		t.Error("init was flagged, which would mark every pane on the machine")
	}
}

// A build older than every process flags nothing. Without this the walk could
// return every ancestor it saw and still pass the test above.
func TestPSTableRespectsTheBuildStamp(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	const sample = `  501     1    03:11:07 /bin/zsh --login
  899   501    01:12:00 /Users/someone/bin/shabadoo mcp
`
	table := parsePS(sample, now)
	if got := stalePanesIn(table, now.Add(-24*time.Hour)); len(got) != 0 {
		t.Errorf("a build stamp older than every process flagged %d panes", len(got))
	}
	if got := stalePanesIn(table, now); len(got) == 0 {
		t.Error("a just-built stamp flagged nothing, so no comparison is happening")
	}
}

func TestParseETime(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"04:31", 4*time.Minute + 31*time.Second, true},
		{"03:11:07", 3*time.Hour + 11*time.Minute + 7*time.Second, true},
		{"2-04:22:10", 2*24*time.Hour + 4*time.Hour + 22*time.Minute + 10*time.Second, true},
		{"12-00:00:00", 12 * 24 * time.Hour, true},
		{"", 0, false},
		{"31", 0, false},      // ps always emits at least mm:ss
		{"1:2:3:4", 0, false}, // more fields than the format has
		{"aa:bb", 0, false},
	}
	for _, c := range cases {
		got, ok := parseETime(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseETime(%q) = %v,%v want %v,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

// A process table that could not be read must not be reported as a clean fleet.
// This is the shape of the original macOS defect, held separately from the
// parser so it stays true however the table is sourced.
func TestEmptyTableFlagsNothingRatherThanEverything(t *testing.T) {
	if got := stalePanesIn(nil, time.Now()); len(got) != 0 {
		t.Errorf("an empty process table flagged %d panes", len(got))
	}
}

// Cross-check the two readers against each other on a host where both work.
//
// The macOS path cannot be exercised here, and a fixture only ever agrees with
// whatever its author assumed — which is precisely how the /proc-only version
// shipped looking correct. Linux can run both readers over the same live
// process table, so the `ps` parser is checked against something other than my
// own idea of what ps prints.
func TestBothReadersAgree(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("only linux has both readers")
	}
	fromProc := procTableProc()
	if len(fromProc) == 0 {
		t.Skip("no /proc here")
	}
	fromPS := procTablePS(time.Now())
	if len(fromPS) == 0 {
		t.Fatal("ps returned nothing on a host where /proc returned processes — " +
			"this is the reader macOS depends on entirely")
	}

	// Compare the answer that matters rather than the raw tables: process lists
	// taken microseconds apart legitimately differ, but which panes hold a stale
	// MCP child must not.
	built := time.Now()
	procPanes, psPanes := stalePanesIn(fromProc, built), stalePanesIn(fromPS, built)
	if len(procPanes) == 0 {
		t.Skip("no shabadoo mcp processes running, nothing to compare")
	}
	for pid := range procPanes {
		if !psPanes[pid] {
			t.Errorf("pane %d is stale according to /proc and clean according to ps", pid)
		}
	}
	for pid := range psPanes {
		if !procPanes[pid] {
			t.Errorf("pane %d is stale according to ps and clean according to /proc", pid)
		}
	}
	t.Logf("both readers agree on %d stale pane(s)", len(procPanes))
}
