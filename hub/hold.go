package hub

// A hold delivers mail and asks the recipient not to act on it.
//
// WHY THIS EXISTS, in the operator's words: "sessions were sent messages
// running off on tasks... I wanted sessions to wait and work on one mission and
// inbox handling had mission agents working when I wanted to be in loop of
// actions."
//
// Measured rather than assumed. On the day that prompted it there were only TWO
// task creations — it was not a task fan-out at all. There were 140 nudges
// across 16 distinct sessions, and a nudge comes from peer-to-peer mail. So
// sessions were not being handed work; they were messaging each other, and
// every message woke the recipient into a turn.
//
// The wake cap already paces HOW MANY wake at once. It does nothing about
// WHETHER they act, which is why the fleet still felt runaway with the cap
// running. This is the other half.
//
// It is guidance, not enforcement — a session can disregard it, exactly as it
// could disregard the "ASSESS ONLY" prose that served this purpose before. What
// changes is that the ask now arrives in the one place a session cannot miss:
// the drain output itself, on every delivery, rather than in a message somebody
// has to remember to send.

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// holdFile is read on every check so an edit takes effect with no restart, and
// so an operator can set it from a plain shell when nothing else works:
//
//	echo all > /docker/shabadoo/data/hold      # the whole fleet
//	echo patching >> /docker/shabadoo/data/hold # one session
//	rm /docker/shabadoo/data/hold               # release
//
// A file AND an API, because each covers the other's gap: the API is how you
// hold the fleet from a phone, and the file is how you hold it when the API is
// what is misbehaving.
const holdFile = "hold"

// holdOwnerProject is never held, for the reason the wake cap gives: the
// session that must lift a stuck hold cannot be one the hold is holding.
const holdOwnerProject = "shabadoo"

type holdState struct {
	mu  sync.Mutex
	dir string

	// Consulted counts checks, and Held counts holds applied. Both exist so
	// that "the hold is working" and "the hold is not wired up" are
	// distinguishable from outside — this codebase's most expensive recurring
	// failure, met here in a feature whose whole job is to be believed.
	Consulted int64
	Held      int64
}

func newHoldState(dir string) *holdState { return &holdState{dir: dir} }

// entries reads the hold file. A missing file is no hold; an unreadable one is
// ALSO no hold.
//
// FAILS OPEN, and that direction is chosen rather than inherited. A hold stuck
// ON is a fleet that stops working with nothing saying why, recoverable only by
// somebody at the machine. A hold stuck OFF is the day that prompted this
// feature: visible, recoverable, and costing one bad afternoon.
func (h *holdState) entries() []string {
	if h == nil || h.dir == "" {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(h.dir, holdFile))
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		if s := strings.TrimSpace(line); s != "" && !strings.HasPrefix(s, "#") {
			out = append(out, s)
		}
	}
	return out
}

// holds reports whether this session should be asked to report rather than act.
func (h *holdState) holds(sessionID, alias, project, kind string) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	h.Consulted++
	h.mu.Unlock()

	// Never held. A hold that silences the sessions used to lift it is a
	// lockout, and it arrives at the worst moment because that is when a hold
	// is on. Same list the wake cap exempts, for the same reason.
	if kind == KindCore || project == holdOwnerProject {
		return false
	}
	for _, e := range h.entries() {
		if strings.EqualFold(e, "all") ||
			strings.EqualFold(e, sessionID) ||
			strings.EqualFold(e, alias) ||
			strings.EqualFold(e, project) {
			h.mu.Lock()
			h.Held++
			h.mu.Unlock()
			return true
		}
	}
	return false
}

// set writes the hold list. An empty list removes the file entirely, so
// "released" and "held by nobody" are the same state on disk rather than an
// empty file somebody has to interpret.
func (h *holdState) set(names []string) error {
	if h == nil || h.dir == "" {
		return nil
	}
	path := filepath.Join(h.dir, holdFile)
	if len(names) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return os.WriteFile(path, []byte(strings.Join(names, "\n")+"\n"), 0o644)
}

// stats is what /healthz reports, so a hold is visible without a credential —
// which is exactly what you have when the thing you are trying to diagnose is
// the fleet not doing anything.
func (h *holdState) stats() map[string]any {
	if h == nil {
		return map[string]any{"enabled": false}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	e := h.entries()
	return map[string]any{
		"enabled":   len(e) > 0,
		"holding":   e,
		"consulted": h.Consulted,
		"held":      h.Held,
	}
}
