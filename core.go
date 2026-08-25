package main

// Keeping the node's core session alive.
//
// Every other session may exit and stay down until something needs it. The core
// session may not: it is the only actor permitted to start sessions on its
// node, so one that exited and stayed down would leave the machine unable to
// start anything, recoverable only by walking to it. "Always running" is
// load-bearing rather than a preference.
//
// The agent does this, not the ten-minute cron watchdog. It is already here,
// already reporting every few seconds, and already looking at the window list —
// so recovery is one report cycle instead of up to ten minutes of a node that
// cannot start anything.
//
// See docs/build-plan.md (Phase 2).

import (
	"context"
	"log"
	"time"

	"shabadoo/hub"
)

const (
	// coreBackoffMin is the first retry delay, and coreBackoffMax the ceiling.
	//
	// Backoff is not politeness here, it is the difference between a supervisor
	// and an outage: a core session that fails immediately — a missing binary, a
	// broken config — would otherwise be relaunched every five seconds forever,
	// and the thing meant to keep the node healthy becomes the thing hammering
	// it.
	coreBackoffMin = 10 * time.Second
	coreBackoffMax = 10 * time.Minute
)

// coreKeeper restarts this node's core session when it is not running.
type coreKeeper struct {
	path string // the node's main project; empty disables the keeper entirely

	attempts int
	next     time.Time

	// Seams, so this is testable without launching anything. A test that
	// actually started a session would be a test that touched a live machine,
	// which this repository has been bitten by twice.
	launch func(ctx context.Context, dir string) error
	now    func() time.Time
	exists func(dir string) bool
}

func newCoreKeeper(path string) *coreKeeper {
	return &coreKeeper{
		path:   path,
		launch: func(ctx context.Context, dir string) error { _, err := opOpen(ctx, dir); return err },
		now:    time.Now,
		exists: isDir,
	}
}

// observe acts on the current report.
//
// It asks whether the core session is running RIGHT NOW rather than whether it
// just ended, and the distinction matters. A session that ended is an event and
// suits deactivation; "always running" is a state, and asking about the state
// also covers the cases an event misses — an agent that starts while the core
// session is already down, and a relaunch that silently failed. Nothing is lost
// by not using the diff here: a session that ends is, on the next report, a
// session that is absent.
func (k *coreKeeper) observe(ctx context.Context, current []hub.Session) {
	if k.path == "" {
		return
	}
	for _, s := range current {
		if s.Kind == hub.KindCore {
			k.attempts = 0 // healthy: forget any backoff we had accrued
			return
		}
	}

	// A node with no core project is not broken, it is a node that has not been
	// given one — every install that predates this is in that state. Retrying a
	// launch into a directory that does not exist would back off forever while
	// logging a failure every time, which is noise about a decision nobody made.
	if !k.exists(k.path) {
		return
	}

	now := k.now()
	if !k.next.IsZero() && now.Before(k.next) {
		return
	}
	k.attempts++
	k.next = now.Add(coreBackoff(k.attempts))

	if err := k.launch(ctx, k.path); err != nil {
		log.Printf("node: core session restart failed (attempt %d, next in %s): %v",
			k.attempts, coreBackoff(k.attempts), err)
		return
	}
	log.Printf("node: core session was not running; started it in %s", k.path)
}

// coreBackoff doubles from the minimum up to the ceiling.
func coreBackoff(attempt int) time.Duration {
	d := coreBackoffMin
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= coreBackoffMax {
			return coreBackoffMax
		}
	}
	return d
}
