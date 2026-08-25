package main

// Noticing that a window went away.
//
// The agent reports its windows every few seconds, and until now each report
// was stateless: a window that closed simply stopped appearing, and nothing
// anywhere knew it had ever been there. That absence is the missing piece under
// two separate features — a session that exits should be recorded as
// deactivated rather than reopened ten minutes later by the watchdog, and the
// node's own core session should be restarted rather than left down.
//
// Both need the same observation, which is why it is here on its own rather
// than inside either of them.
//
// See docs/direction.md and docs/build-plan.md (Phase 0).

import (
	"log"

	"shabadoo/hub"
)

// windowWatcher remembers the previous report so the next one can be a diff.
type windowWatcher struct {
	prev map[string]hub.Session
}

func newWindowWatcher() *windowWatcher {
	return &windowWatcher{}
}

// observe records the current view and returns the sessions that have gone
// since the last one.
//
// # The hard part is not the diff
//
// A tmux server that restarts, or a machine that reboots, makes EVERY window
// vanish at once — and `tmux.Sessions` reports "no server running" as zero
// windows rather than as an error, so at this layer a dead tmux and an empty
// tmux are the same observation. Treating that as a pile of deactivations would
// silently disable every session on the host after a reboot, which is a far
// worse failure than missing one close.
//
// So a report that goes from some windows to NONE is treated as losing tmux,
// not as losing sessions. The cost is real and accepted: closing your last
// remaining window is indistinguishable from tmux dying, so it is not recorded.
// That is the safe direction — the same reasoning as `guardDialog` failing
// open, where a missed detection restores the old behaviour and a false one
// causes damage.
//
// The first report after the agent starts is likewise not a diff. There is no
// previous view to compare against, and inventing one would deactivate every
// session on the host every time the agent restarted.
func (w *windowWatcher) observe(current []hub.Session) []hub.Session {
	now := make(map[string]hub.Session, len(current))
	for _, s := range current {
		now[s.SessionID] = s
	}

	prev := w.prev
	w.prev = now

	switch {
	case prev == nil:
		return nil // first report: nothing to compare against
	case len(now) == 0 && len(prev) > 0:
		return nil // tmux went away, not every session at once
	}

	var gone []hub.Session
	for id, s := range prev {
		if _, still := now[id]; !still {
			gone = append(gone, s)
		}
	}
	return gone
}

// watchedReporter wraps the session reporter so every report is also a diff.
//
// It sits here rather than in the node package because what to DO about a
// window that went away is a decision about this host's sessions, and the node
// package deliberately knows nothing about them — it moves bytes.
func watchedReporter() func(current []hub.Session) {
	w := newWindowWatcher()
	return func(current []hub.Session) {
		for _, s := range w.observe(current) {
			// Phases 2 and 3 act on this. Logging it first is not a placeholder:
			// an event nobody can see is one nobody can debug, and the first
			// question when a session is unexpectedly down will be whether the
			// agent noticed it going.
			log.Printf("node: session ended: %s (%s) in %s", s.Alias, s.Kind, s.CWD)
		}
	}
}
