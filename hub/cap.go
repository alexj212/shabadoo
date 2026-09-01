package hub

// A cap on how many sessions are woken into a turn at once.
//
// WHY THIS EXISTS. One session issued a check-in fan-out — 25 tasks in a few
// minutes — which woke roughly two dozen idle sessions into simultaneous turns
// and consumed the account's usage window. Nothing opened a single window: the
// sessions were already there, already idle, costing nothing. So the thing to
// cap is ACTIVITY, not existence, and the enforcement point is the wake path
// rather than the launch path. A limit guarding `open` would have been fully
// satisfied while the window burned.
//
// WHAT IT COUNTS. Cache-read dominates output by ~327x on this fleet, so the
// cost driver is turns taken across large contexts. A session burns tokens when
// it works, so "is it working" is answered by its token counters GROWING between
// two agent reports — a direct measure of the thing being capped, available every
// five seconds, and needing no new field on the agent protocol.
//
// It deliberately does NOT key on Session.Status. That field is tmux's
// selected-window flag: `statusOf` returns "active" when w.Active, which means
// "this is the window you are looking at". Two sessions independently reported
// "2 of 29 active" and "1 of 26 active" as though they were activity
// measurements; both were counting foregrounded windows. Gating on it would cap
// against whichever pane somebody last looked at.
//
// A freshly nudged session has not burned anything yet — a turn takes seconds to
// start — so recent nudges count toward the limit too. Without that, a fan-out
// passes the gate two dozen times before the first report reflects any of it,
// which is exactly the thundering herd this exists to stop.

import (
	"os"
	"path/filepath"
	"sync"
	"time"
)

// wakeReason says who is asking, because the answer is not the same for all of
// them. It is a typed enum rather than a string for the reason the rest of this
// codebase gives: a string's zero value serialises as a value and reads as a
// deliberate choice, while an unset enum can be made to mean the safe thing.
type wakeReason int

const (
	// wakeUnknown is the zero value and is EXEMPT. A caller that forgot to say
	// why gets let through rather than silently held — see the fail-open note
	// on allow().
	wakeUnknown     wakeReason = iota
	wakeHumanSend              // a person typed it. Never held.
	wakeTaskEnd                // a task finished. Never held.
	wakeAgentSend              // session to session. Gated.
	wakeTaskCreate             // work handed to a peer. Gated.
	wakeStuckRetry             // the stuck watcher trying again. Gated.
	wakeStoppedCore            // waking a stopped project's core. Gated.
)

func (w wakeReason) exempt() bool {
	return w == wakeUnknown || w == wakeHumanSend || w == wakeTaskEnd
}

func (w wakeReason) String() string {
	switch w {
	case wakeHumanSend:
		return "human"
	case wakeTaskEnd:
		return "task-end"
	case wakeAgentSend:
		return "agent-send"
	case wakeTaskCreate:
		return "task-create"
	case wakeStuckRetry:
		return "stuck-retry"
	case wakeStoppedCore:
		return "stopped-core"
	}
	return "unspecified"
}

const (
	// capWorkWindow is how long after burning tokens a session still counts as
	// working. Agents report every 5s; a turn spans many reports, and a gap
	// between two of them must not read as finished.
	capWorkWindow = 90 * time.Second

	// capNudgeGrace covers the interval between waking a session and its first
	// report showing the burn.
	capNudgeGrace = 60 * time.Second

	// capOwnerProject is the project that owns this code. It is never held: the
	// session that must fix a broken cap cannot be the one the cap is holding,
	// for the same reason a filesystem's fsck does not live on that filesystem.
	capOwnerProject = "shabadoo"

	// capOffFile disables the cap entirely, checked on every gate call so it
	// takes effect without a restart. It is a FILE rather than a flag or an API
	// because a broken cap is exactly when sessions and dashboards do not work:
	//
	//     touch /docker/shabadoo/data/cap.off
	//
	// from a plain shell on the host, with no session, no credential and no
	// running coordinator required to prepare it.
	capOffFile = "cap.off"
)

type wakeCap struct {
	mu    sync.Mutex
	limit int
	dir   string
	now   func() time.Time

	spend   map[string]int64     // session -> total tokens at last report
	working map[string]time.Time // session -> when it was last seen burning
	nudged  map[string]time.Time // session -> when we last woke it
	queued  map[string]time.Time // session -> held since

	// Counters exist so "the cap is working" and "the cap is broken" are
	// distinguishable. A cap that has never fired looks exactly like one that
	// is not wired up, which is this fleet's most expensive recurring failure.
	Allowed  int64
	Held     int64
	Exempted int64
	// HighWater is the measurement Alex needs to stop guessing at the limit:
	// the most sessions ever seen working at once. Nobody has ever measured it.
	HighWater int
}

func newWakeCap(limit int, dir string, now func() time.Time) *wakeCap {
	if now == nil {
		now = time.Now
	}
	return &wakeCap{
		limit:   limit,
		dir:     dir,
		now:     now,
		spend:   map[string]int64{},
		working: map[string]time.Time{},
		nudged:  map[string]time.Time{},
		queued:  map[string]time.Time{},
	}
}

// observe reads an agent's report and records who burned tokens since the last
// one. Called on the report path, where the numbers already arrive.
func (c *wakeCap) observe(sessions []Session) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for _, s := range sessions {
		total := s.TokensIn + s.TokensOut + s.TokensCache
		if total == 0 {
			continue // no transcript: a worker, or nothing measured
		}
		prev, seen := c.spend[s.SessionID]
		c.spend[s.SessionID] = total
		if seen && total > prev {
			c.working[s.SessionID] = now
		}
	}
	// Recompute the high-water mark from the live set rather than incrementing,
	// so a session that stops working lowers the count.
	if n := c.concurrentLocked(now); n > c.HighWater {
		c.HighWater = n
	}
}

// concurrentLocked counts sessions currently working or just woken.
func (c *wakeCap) concurrentLocked(now time.Time) int {
	n := 0
	for id, t := range c.working {
		if now.Sub(t) > capWorkWindow {
			delete(c.working, id)
			continue
		}
		n++
	}
	for id, t := range c.nudged {
		if now.Sub(t) > capNudgeGrace {
			delete(c.nudged, id)
			continue
		}
		if _, alsoWorking := c.working[id]; !alsoWorking {
			n++
		}
	}
	return n
}

// disabled reports whether the kill switch is present.
//
// A stat error is NOT treated as "the switch is absent" — anything other than a
// confirmed absence disables the cap, because the failure directions are not
// symmetric and this is where that is decided.
func (c *wakeCap) disabled() bool {
	if c.dir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(c.dir, capOffFile))
	if err == nil {
		return true // switch is present
	}
	return !os.IsNotExist(err) // anything but a clean absence: fail open
}

// allow decides whether this wake may proceed. It returns how many sessions are
// ahead of a held one, so the hold can be reported rather than merely happening.
//
// IT FAILS OPEN, AND THAT IS CHOSEN RATHER THAN INHERITED. Every uncertain path
// here — no limit set, kill switch present or unreadable, an exempt caller, a
// session this cap has never heard of — lets the wake through.
//
//	Fail CLOSED on a bug: the fleet stops, and the sessions that could diagnose
//	it are the ones being held. Unrecoverable without a human on the box.
//	Fail OPEN on a bug: the cap silently does not bind, and we are back to an
//	uncapped afternoon. Recoverable, visible, and it costs one bad day.
//
// The asymmetry is not close. Anyone tightening this later should know the
// looseness was picked on purpose and not left by accident.
func (c *wakeCap) allow(sessionID, project, kind string, reason wakeReason) (ok bool, behind int) {
	if c == nil || sessionID == "" {
		return true, 0
	}
	if reason.exempt() {
		c.mu.Lock()
		c.Exempted++
		c.nudged[sessionID] = c.now() // it still costs a turn; it still counts
		c.mu.Unlock()
		return true, 0
	}
	// A core session is the addressable "you" of its machine and the only thing
	// permitted to start sessions there. Holding one means nobody can route,
	// unstick or reach that host — the single failure that turns a bug in this
	// file into a fleet-wide lockout with no way back in.
	if kind == "core" || project == capOwnerProject {
		c.mu.Lock()
		c.Exempted++
		c.nudged[sessionID] = c.now()
		c.mu.Unlock()
		return true, 0
	}
	if c.limit <= 0 || c.disabled() {
		return true, 0
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	// Already counted as working or recently woken: let it through. Holding a
	// session that is mid-turn buys nothing — the turn is already being paid for.
	if t, ok := c.working[sessionID]; ok && now.Sub(t) <= capWorkWindow {
		c.Allowed++
		return true, 0
	}
	if n := c.concurrentLocked(now); n >= c.limit {
		if _, already := c.queued[sessionID]; !already {
			c.queued[sessionID] = now
		}
		c.Held++
		return false, n
	}
	delete(c.queued, sessionID)
	c.nudged[sessionID] = now
	c.Allowed++
	return true, 0
}

// queuedSince reports when a session was first held, so a held session is
// distinguishable from an idle one and from a stuck one.
//
// Three states, and collapsing any two of them rebuilds the empty-versus-unknown
// failure in a new place: IDLE has nothing to do, QUEUED is held by this cap and
// needs no human, STUCK needs one. The stuck watcher escalates to a person at
// five minutes, so without this it would page somebody about mail the cap is
// deliberately holding — the calming feature generating the alarm.
func (c *wakeCap) queuedSince(sessionID string) (time.Time, bool) {
	if c == nil {
		return time.Time{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.queued[sessionID]
	return t, ok
}

// clearQueued forgets a hold, called when the mail is finally delivered.
func (c *wakeCap) clearQueued(sessionID string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.queued, sessionID)
	c.mu.Unlock()
}

// stats is what /healthz reports, so the cap's behaviour is observable without
// a session — which is the same reason the kill switch is a file.
func (c *wakeCap) stats() map[string]any {
	if c == nil {
		return map[string]any{"enabled": false}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return map[string]any{
		"enabled":    c.limit > 0 && !c.disabled(),
		"limit":      c.limit,
		"concurrent": c.concurrentLocked(c.now()),
		"high_water": c.HighWater,
		"allowed":    c.Allowed,
		"held":       c.Held,
		"exempted":   c.Exempted,
		"queued_now": len(c.queued),
	}
}
