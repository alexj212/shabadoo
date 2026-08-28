package main

import (
	"encoding/json"
	"runtime"
	"testing"
	"time"
)

// A session's MCP tool surface is fixed when the session starts, so upgrading
// the binary reaches nothing already running. The failure is invisible from
// inside the affected session — which is where it matters.
//
// This runs against the REAL process table, because what is under test is
// whether the mapping from an MCP process to a pane is right on a live machine.
// A fixture would agree with whatever its author assumed, and the first two
// attempts assumed wrongly: `shabadoo mcp` is a GRANDCHILD of the pane process
// (pane shell -> claude -> mcp), so keying on the immediate parent found
// nothing at all on a host with eleven demonstrably stale sessions.
func TestStaleToolDetection(t *testing.T) {
	table := processTable(time.Now())

	// Declaring every child stale must flag panes; declaring every child
	// current must flag none. Both directions, because a one-sided check passes
	// for a function that returns everything it saw AND for one that returns
	// nothing — which are the two ways this has actually been broken.
	all := panesWithSurface(table, func(int) toolState { return toolStale })
	none := panesWithSurface(table, func(int) toolState { return toolCurrent })
	unknown := panesWithSurface(table, func(int) toolState { return toolUnknown })

	if len(none) != 0 {
		t.Errorf("children reporting the current surface flagged %d panes", len(none))
	}
	if len(unknown) != 0 {
		t.Errorf("children whose surface could not be established flagged %d panes — "+
			"cannot-tell must never render as stale", len(unknown))
	}
	if len(all) == 0 {
		t.Skip("no shabadoo mcp processes running here, so there is nothing to map")
	}
	t.Logf("%d pane(s) map to an MCP child", len(all))
}

func TestPSTableFindsMCPChildren(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)

	// `ps -Ao pid=,ppid=,etime=,command=` as macOS emits it: right-aligned
	// numbers, an absolute command path, and etime in three different widths.
	const sample = `  501     1    03:11:07 /bin/zsh --login
  733   501 2-04:22:10 claude --dangerously-skip-permissions
  899   733    11:12:00 /usr/local/bin/shabadoo mcp
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
	//
	// The state function is injected, because staleness is no longer a fact
	// about a clock: the child records what surface it serves and the detector
	// compares strings. Here every MCP child is declared stale so the ANCESTOR
	// WALK is what is under test, which is the part that has actually been wrong.
	stale := panesWithSurface(table, func(int) toolState { return toolStale })
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

// Three states, and the third is the reason the mechanism was rebuilt.
//
// "Cannot tell" must never render as "current": a child predating this
// mechanism, or one whose identity cannot be established, is UNKNOWN. Reporting
// it clean is how tools_stale was wrong in the first place — it answered a
// question nobody asked (was this started before the build) and every caller
// read it as the answer to the one they did.
func TestPanesFlaggedOnlyWhenTheChildSaysStale(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	const sample = `  501     1    03:11:07 /bin/zsh --login
  899   501    01:12:00 /usr/local/bin/shabadoo mcp
`
	table := parsePS(sample, now)

	for _, c := range []struct {
		name  string
		state toolState
		want  bool
	}{
		{"serving the current surface", toolCurrent, false},
		{"serving an older surface", toolStale, true},
		{"identity or record unavailable", toolUnknown, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := panesWithSurface(table, func(int) toolState { return c.state })
			if got[501] != c.want {
				t.Errorf("pane flagged=%v, want %v", got[501], c.want)
			}
		})
	}
}

// The hash must cover more than the tool NAMES. Adding a parameter, widening an
// enum or rewriting a description the model reads to decide when to call all
// change what a session is serving while leaving the name list identical — and
// changing a tool is far more common than adding one.
func TestToolSurfaceHashCoversMoreThanNames(t *testing.T) {
	base := toolSurfaceHash()
	if base == "" {
		t.Fatal("no hash produced")
	}
	if again := toolSurfaceHash(); again != base {
		t.Fatal("hash is not stable between calls; it would mark every session stale")
	}

	tools := mcpTools()
	if len(tools) == 0 {
		t.Fatal("no tools to fingerprint")
	}
	// A description-only change must move the hash. Verified against the real
	// marshalling rather than a hand-built fixture, since it is the marshalling
	// that decides what is covered.
	raw, err := json.Marshal(tools)
	if err != nil {
		t.Fatal(err)
	}
	tools[0].Description += " (reworded)"
	changed, err := json.Marshal(tools)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == string(changed) {
		t.Fatal("descriptions are not marshalled, so the hash cannot see them")
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
	if got := panesWithSurface(nil, func(int) toolState { return toolStale }); len(got) != 0 {
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
	stale := func(int) toolState { return toolStale }
	procPanes, psPanes := panesWithSurface(fromProc, stale), panesWithSurface(fromPS, stale)
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
