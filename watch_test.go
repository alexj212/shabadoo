package main

import (
	"testing"

	"shabadoo/hub"
)

func sess(id string) hub.Session {
	return hub.Session{SessionID: id, Alias: id, Kind: hub.KindClaude, CWD: "/w/" + id}
}

func ids(list []hub.Session) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		out = append(out, s.SessionID)
	}
	return out
}

// The diff itself is easy. Everything interesting is about when NOT to report,
// because a false "this session ended" is what silently disables a host.
func TestWindowWatcher(t *testing.T) {
	w := newWindowWatcher()

	// The first report is not a diff. Without this the agent would announce that
	// every session on the host had ended, every time it restarted.
	if gone := w.observe([]hub.Session{sess("a"), sess("b")}); len(gone) != 0 {
		t.Fatalf("first report reported %v as ended", ids(gone))
	}

	// Steady state: nothing changes, nothing is reported.
	if gone := w.observe([]hub.Session{sess("a"), sess("b")}); len(gone) != 0 {
		t.Fatalf("unchanged report announced %v", ids(gone))
	}

	// One window closes.
	gone := w.observe([]hub.Session{sess("a")})
	if len(gone) != 1 || gone[0].SessionID != "b" {
		t.Fatalf("gone = %v, want [b]", ids(gone))
	}

	// And it is reported once, not on every subsequent report — the same
	// edge-triggered property the blocked-session watcher needs, for the same
	// reason: agents report every few seconds, forever.
	if again := w.observe([]hub.Session{sess("a")}); len(again) != 0 {
		t.Fatalf("re-reported %v after it had already gone", ids(again))
	}

	// A new window appears; nothing has ended.
	if gone := w.observe([]hub.Session{sess("a"), sess("c")}); len(gone) != 0 {
		t.Fatalf("an arrival was reported as an ending: %v", ids(gone))
	}
}

// The failure this design exists to avoid. A tmux restart or a reboot makes
// every window vanish at once, and `tmux.Sessions` reports a dead server as
// zero windows rather than as an error — so at this layer the two are the same
// observation. Reporting them as endings would deactivate every session on the
// host after a reboot.
func TestTmuxGoingAwayIsNotEverySessionEnding(t *testing.T) {
	w := newWindowWatcher()
	w.observe([]hub.Session{sess("a"), sess("b"), sess("c")})

	if gone := w.observe(nil); len(gone) != 0 {
		t.Fatalf("an empty report announced %v as ended", ids(gone))
	}

	// Coming back is not an ending either, and the watcher must have kept up:
	// having seen an empty world, it should treat what returns as the new one.
	if gone := w.observe([]hub.Session{sess("a")}); len(gone) != 0 {
		t.Fatalf("tmux returning announced %v", ids(gone))
	}
	// ...and from there, ordinary diffing resumes.
	if gone := w.observe(nil); len(gone) != 0 {
		t.Fatalf("second empty report announced %v", ids(gone))
	}
}

// The accepted cost, pinned so it is a decision rather than a surprise: closing
// your LAST window is indistinguishable from tmux dying, so it is not reported.
// Missing one close is recoverable; deactivating a whole host is not.
func TestClosingTheLastWindowIsNotReported(t *testing.T) {
	w := newWindowWatcher()
	w.observe([]hub.Session{sess("only")})

	if gone := w.observe(nil); len(gone) != 0 {
		t.Errorf("closing the last window reported %v — the tmux-loss rule must win here", ids(gone))
	}
}
