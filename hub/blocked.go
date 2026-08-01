package hub

// Telling someone a session is waiting for them.
//
// The dashboard already flags a blocked session — a badge on the row, a count
// in the header, `(n) shabadoo` in the tab title. All three require the page to
// be open. The whole reason a session blocks is that it needs a human who, by
// definition, is doing something else; a signal that only reaches a browser tab
// reaches nobody.
//
// Agents already classify every window in their periodic report, so the
// coordinator learns of a block within a few seconds without polling anything.
// This watches that stream and turns the transition into a notification.

import (
	"context"
	"fmt"
	"sync"
	"time"
)

const (
	// blockedGrace is how long a dialog must stand before it is worth
	// interrupting someone over.
	//
	// Most prompts are answered by whoever is already at the keyboard within
	// seconds — a permission dialog, a plan approval. Notifying immediately
	// would buzz a phone for every one of them, and a notification stream that
	// is mostly noise gets muted, which costs the signal that mattered. This is
	// several report intervals, so a prompt has to actually be *stuck*.
	blockedGrace = 90 * time.Second

	// blockedRepeat re-notifies about a session still blocked this long after
	// the last notification. Zero disables it.
	//
	// One reminder an hour is deliberately sparse: the first notification is
	// the useful one, and the rest exist only so a session blocked overnight is
	// not represented by a single alert from twelve hours ago.
	blockedRepeat = time.Hour
)

// BlockedGrace is how long a prompt must stand before anyone is told, exposed
// so the startup banner states the policy rather than leaving an operator to
// wonder why a prompt they answered quickly never notified.
var BlockedGrace = blockedGrace

// blockedWatcher turns agent reports into notifications about stuck sessions.
//
// Edge-triggered with a grace period rather than level-triggered: agents report
// every five seconds, and a session sitting at a prompt reports "dialog" every
// time. Notifying on the state rather than the transition would be a
// notification every five seconds, forever.
type blockedWatcher struct {
	mu    sync.Mutex
	seen  map[string]*blockedEntry
	send  func(ctx context.Context, tenant, title, body, tag string) error
	now   func() time.Time
	audit func(ctx context.Context, tenant, target, detail string)
}

type blockedEntry struct {
	since    time.Time
	notified time.Time // zero = not yet notified
}

func newBlockedWatcher(now func() time.Time) *blockedWatcher {
	return &blockedWatcher{seen: map[string]*blockedEntry{}, now: now}
}

// observe records one agent's report and notifies about anything newly stuck.
//
// The sessions slice is that agent's ENTIRE view, so a window missing from it
// is gone; combined with clearing state for anything no longer in a dialog,
// that means answering a prompt resets the timer and a later block notifies
// again. Without the reset, a session that blocked twice in an hour would
// mention it once.
func (b *blockedWatcher) observe(ctx context.Context, tenant, node string, sessions []Session) {
	if b == nil || b.send == nil {
		return
	}
	now := b.now()

	b.mu.Lock()
	// Forget everything this node previously reported, then re-add what is
	// still blocked. Cheaper to reason about than diffing, and it handles a
	// vanished window for free.
	prefix := tenant + "\x00" + node + "\x00"
	live := map[string]*blockedEntry{}
	for k, v := range b.seen {
		if len(k) < len(prefix) || k[:len(prefix)] != prefix {
			live[k] = v
		}
	}

	type due struct {
		sess    Session
		blocked time.Duration
		repeat  bool
	}
	var toSend []due

	for _, s := range sessions {
		if s.InputState != "dialog" {
			continue
		}
		key := prefix + s.Window
		e := b.seen[key]
		if e == nil {
			e = &blockedEntry{since: now}
		}
		live[key] = e

		switch {
		case e.notified.IsZero() && now.Sub(e.since) >= blockedGrace:
			e.notified = now
			toSend = append(toSend, due{sess: s, blocked: now.Sub(e.since)})
		case !e.notified.IsZero() && blockedRepeat > 0 && now.Sub(e.notified) >= blockedRepeat:
			e.notified = now
			toSend = append(toSend, due{sess: s, blocked: now.Sub(e.since), repeat: true})
		}
	}
	b.seen = live
	b.mu.Unlock()

	// Outside the lock and detached from the request: the notifier is a network
	// call, and an agent's report must not wait on it — nor fail because of it.
	for _, d := range toSend {
		go func(d due) {
			c, cancel := context.WithTimeout(context.WithoutCancel(ctx), appriseTimeout)
			defer cancel()

			name := d.sess.Alias
			if name == "" {
				name = d.sess.Project
			}
			title := fmt.Sprintf("%s is waiting", name)
			if d.repeat {
				title = fmt.Sprintf("%s is still waiting", name)
			}
			body := fmt.Sprintf("%s on %s has been at a prompt for %s.\n%s",
				name, node, roundDuration(d.blocked), d.sess.CWD)

			if err := b.send(c, tenant, title, body, "all"); err != nil {
				return
			}
			if b.audit != nil {
				b.audit(c, tenant, d.sess.Window, title)
			}
		}(d)
	}
}

// roundDuration renders a wait the way a person would say it. A notification
// reading "1m32.014s" is machine output in a human channel.
func roundDuration(d time.Duration) string {
	switch {
	case d < 2*time.Minute:
		return "a minute"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	case d < 2*time.Hour:
		return "an hour"
	default:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	}
}
