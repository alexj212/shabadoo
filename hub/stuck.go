package hub

// Mail that arrived and was never picked up.
//
// A handoff is meant to close by itself: A sends work, the recipient is nudged,
// it drains, does the work, and A is told when the task ends. The nudge is the
// only thing that starts that, and it fails SILENTLY — a skipped nudge and a
// delivered one look identical from every side, including from inside the
// session that never heard anything.
//
// That is not hypothetical. A non-breaking space in Claude Code's input row
// made every pane read as "somebody is typing here", so the nudge was skipped
// fleet-wide for ten hours. Two sessions in a handoff both sat waiting. Nothing
// failed, nothing was logged, and it surfaced only because a human asked a
// session how it was doing and it checked its inbox.
//
// So the loop needs a second observer that is not the nudge. Undrained mail on
// a session that is ONLINE, IDLE and not blocked on a dialog is a stuck loop,
// whatever the cause — a nudge that never fired, one that landed in a pager, a
// session that drained and crashed. The cause does not need diagnosing to know
// somebody should look.
//
// Shape borrowed wholesale from blockedWatcher, deliberately: every mistake in
// edge detection has the same form and it is not one anybody spots from a
// single observation.

import (
	"context"
	"fmt"
	"sync"
	"time"
)

const (
	// stuckGrace is how long mail may sit before anyone is told.
	//
	// Longer than blockedGrace because the recipient may legitimately be
	// mid-turn: a session working on something takes minutes, and its inbox
	// drains on the NEXT prompt, not this one. Shorter than the task chase,
	// which is about somebody else's work rather than about delivery.
	stuckGrace = 5 * time.Minute

	// stuckRetry is when the nudge is tried again, before any human is told.
	// Short, because the retry is free and silent: the pane is idle with an
	// empty composer, which is the same condition the nudge already requires.
	stuckRetry = 2 * time.Minute

	// stuckRepeat re-raises mail still sitting this long after the last
	// notification. As with blocked, the first is the useful one.
	stuckRepeat = time.Hour
)

type stuckWatcher struct {
	mu   sync.Mutex
	seen map[string]*stuckEntry
	send func(ctx context.Context, tenant, title, body, tag string) error
	// retry re-attempts the nudge before a human is told. The nudge fires on
	// ARRIVAL only, so mail that was missed once is never retried and a backlog
	// never clears itself — which is what ten hours of skipped nudges left
	// behind, and what nothing would have swept up.
	retry func(ctx context.Context, tenant, sessionID string)
	now   func() time.Time
}

type stuckEntry struct {
	since    time.Time
	retried  bool
	notified time.Time
}

func newStuckWatcher(now func() time.Time) *stuckWatcher {
	return &stuckWatcher{seen: map[string]*stuckEntry{}, now: now}
}

// observe takes the whole tenant's session list, because undrained mail is a
// fact the coordinator holds rather than one an agent reports — an agent has no
// idea what is in its sessions' inboxes.
func (s *stuckWatcher) observe(ctx context.Context, tenant string, sessions []Session, online func(agent string) bool) {
	if s == nil || s.send == nil {
		return
	}
	now := s.now()

	s.mu.Lock()
	live := map[string]*stuckEntry{}
	var due []struct {
		sess   Session
		waited time.Duration
		repeat bool
	}

	for _, sess := range sessions {
		// Only a session that could have taken the mail and did not.
		//
		// Offline is not stuck: mail for an offline session is meant to wait,
		// and its delivery row IS the wait. A session on a dialog is blocked,
		// which is blockedWatcher's job — reporting it twice under two names
		// would train somebody to ignore both.
		if sess.Pending == 0 || !online(sess.Agent) || sess.InputState == "dialog" {
			continue
		}
		key := tenant + "\x00" + sess.SessionID
		e := s.seen[key]
		if e == nil {
			e = &stuckEntry{since: now}
		}
		live[key] = e

		waited := now.Sub(e.since)

		// Try the mechanism again before escalating to a person.
		//
		// The condition this watcher fires on — online, idle, not on a dialog,
		// mail waiting — is exactly the condition under which a nudge is safe
		// to send, so retrying costs nothing and closes the loop without
		// anybody being interrupted. A human is for when the retry does not
		// work either, which is the case that needs a different kind of help.
		if !e.retried && waited >= stuckRetry && s.retry != nil {
			e.retried = true
			s.retry(ctx, tenant, sess.SessionID)
			continue
		}

		switch {
		case e.notified.IsZero() && waited >= stuckGrace:
			e.notified = now
			due = append(due, struct {
				sess   Session
				waited time.Duration
				repeat bool
			}{sess, waited, false})
		case !e.notified.IsZero() && stuckRepeat > 0 && now.Sub(e.notified) >= stuckRepeat:
			e.notified = now
			due = append(due, struct {
				sess   Session
				waited time.Duration
				repeat bool
			}{sess, waited, true})
		}
	}
	// Anything that drained, went offline or answered a dialog loses its state,
	// so the next time it happens is a new event rather than a continuation.
	s.seen = live
	s.mu.Unlock()

	for _, d := range due {
		name := d.sess.Alias
		if name == "" {
			name = d.sess.Project
		}
		again := ""
		if d.repeat {
			again = " (still)"
		}
		title := fmt.Sprintf("%s has %d unread message(s)%s", name, d.sess.Pending, again)
		body := fmt.Sprintf(
			"%s on %s has been idle at a prompt for %s with mail it has not picked up.\n\n"+
				"A handoff normally closes itself: the coordinator types `check inbox` into "+
				"the pane, the session drains and answers. This one did not, so either the "+
				"nudge is not reaching that pane or the session stopped without draining.\n\n"+
				"`shabadoo tail %s` shows what is on screen; `shabadoo mail` shows what is waiting.",
			name, d.sess.Agent, roundDuration(d.waited), name)
		_ = s.send(ctx, tenant, title, body, "")
	}
}

// SweepStuck checks every tenant for mail nobody picked up.
//
// Driven by a timer rather than by an agent report, because undrained mail is a
// fact this side holds: an agent has no idea what is in its sessions' inboxes,
// so the report cannot carry it.
//
// Two minutes, not the hourly maintenance tick. A stuck handoff is two sessions
// waiting on each other, and the cost of finding out an hour late is an hour of
// nothing happening — which is precisely the failure this exists to end.
func (h *Hub) SweepStuck(ctx context.Context) {
	if h.stuck == nil {
		return
	}
	tenants, err := h.store.Tenants(ctx)
	if err != nil {
		return
	}
	for _, tenant := range tenants {
		sessions, err := h.store.Tenant(tenant).ListSessions(ctx, h.now())
		if err != nil {
			continue // a failed read is not a stuck loop
		}
		h.stuck.observe(ctx, tenant, sessions, func(agent string) bool {
			return h.IsOnline(tenant, agent)
		})
	}
}
