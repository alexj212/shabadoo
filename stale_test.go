package main

import (
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
