package hub

// Chasing work that has gone quiet.
//
// A task nobody chases is a row in a table. The whole reason for recording a
// handoff is to be able to say "that never came back", and something has to
// actually say it — otherwise the task list is a place stale work goes to be
// technically visible.
//
// The shape is `blocked.go`'s, deliberately and in full: edge-triggered, a
// grace period, one reminder per interval, state cleared when the thing
// resolves. Every mistake in edge detection has the same shape — a notification
// every tick, forever — and it is not a shape anyone spots from one report.
//
// Who gets told is the difference from the blocked watcher. A stalled task is
// news for whoever ASKED, not for the machine it is stuck on: they delegated
// and moved on, and are the only party who can decide it matters.
//
// See docs/build-plan.md (Phase 4).

import (
	"context"
	"fmt"
	"sync"
	"time"
)

const (
	// taskStale is how long a task may sit untouched before it is worth
	// mentioning.
	//
	// Far longer than the blocked-session grace, because these are different
	// kinds of waiting. A dialog blocks a machine and is answered in seconds; a
	// task is somebody else's work, and chasing it after an hour would be
	// nagging rather than informing — and a notifier that mostly cries wolf gets
	// muted, which costs the one that mattered.
	taskStale = 6 * time.Hour

	// taskRepeat re-raises a task still untouched this long after the last
	// mention, so work stalled over a weekend is not represented by a single
	// alert from Friday.
	taskRepeat = 24 * time.Hour
)

// taskWatcher chases tasks that have gone quiet.
type taskWatcher struct {
	mu    sync.Mutex
	seen  map[string]time.Time // task id -> when last mentioned
	send  func(ctx context.Context, tenant, title, body, tag string) error
	now   func() time.Time
	audit func(ctx context.Context, tenant, target, detail string)
}

func newTaskWatcher(now func() time.Time) *taskWatcher {
	return &taskWatcher{seen: map[string]time.Time{}, now: now}
}

// observe examines the outstanding tasks and returns those worth mentioning.
//
// It takes the full list rather than reading the store itself, so it can be
// tested without a database and so the caller decides how often to look. State
// for a task no longer outstanding is dropped, which is what makes finishing
// one reset it: a task that stalls, is finished, and stalls again is two
// events rather than a permanent alarm.
func (w *taskWatcher) observe(ctx context.Context, tenant string, tasks []Task) []Task {
	now := w.now()

	w.mu.Lock()
	live := make(map[string]time.Time, len(tasks))
	var due []Task

	for _, k := range tasks {
		if terminalTask(k.State) {
			continue // finished work is not stale, it is finished
		}
		last, known := w.seen[k.ID]
		live[k.ID] = last

		quiet := now.Sub(time.Unix(k.UpdatedAt, 0))
		switch {
		case quiet < taskStale:
			// Touched recently. Forget any previous mention so that going quiet
			// again later is a fresh event rather than an immediate repeat.
			live[k.ID] = time.Time{}
		case !known || last.IsZero():
			live[k.ID] = now
			due = append(due, k)
		case now.Sub(last) >= taskRepeat:
			live[k.ID] = now
			due = append(due, k)
		}
	}
	w.seen = live
	w.mu.Unlock()

	// Outside the lock and detached from the caller's context: the notifier is a
	// network call and must not hold up whatever asked, nor fail because of it.
	for _, k := range due {
		go func(k Task) {
			c, cancel := context.WithTimeout(context.WithoutCancel(ctx), appriseTimeout)
			defer cancel()
			quiet := roundDuration(now.Sub(time.Unix(k.UpdatedAt, 0)))
			title := fmt.Sprintf("Task quiet for %s", quiet)
			body := fmt.Sprintf("%s\n\nWith %s, asked by %s, still %s after %s.",
				k.Brief, shortSessionName(k.SessionID), shortSessionName(k.RequestedBy),
				k.State, quiet)
			if k.Note != "" {
				body += "\n\nLast word: " + k.Note
			}
			if w.send != nil {
				_ = w.send(c, tenant, title, body, "")
			}
			if w.audit != nil {
				w.audit(c, tenant, k.ID, fmt.Sprintf("%s for %s", k.State, quiet))
			}
		}(k)
	}
	return due
}

// shortSessionName drops the `claude-` prefix and the hash suffix, which are the
// two parts of a session id nobody reads.
func shortSessionName(id string) string {
	if id == "" {
		return "nobody"
	}
	s := id
	if len(s) > 7 && s[:7] == "claude-" {
		s = s[7:]
	}
	if i := len(s) - 9; i > 0 && s[i] == '-' {
		hex := true
		for _, c := range s[i+1:] {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				hex = false
				break
			}
		}
		if hex {
			s = s[:i]
		}
	}
	return s
}

// SweepTasks chases anything that has gone quiet, across every tenant.
//
// Called from the coordinator's maintenance loop rather than from a request:
// tasks do not arrive on a timer the way agent reports do, so nothing else
// would ever look. The watcher is edge-triggered, so calling it often is
// harmless and calling it rarely only delays the first mention.
func (h *Hub) SweepTasks(ctx context.Context) {
	if h.tasks == nil {
		return
	}
	tenants, err := h.store.Tenants(ctx)
	if err != nil {
		return
	}
	for _, tenant := range tenants {
		tn := h.store.Tenant(tenant)
		tasks, err := tn.Tasks(ctx, "", "", false, 0)
		if err != nil || len(tasks) == 0 {
			continue
		}
		h.tasks.observe(ctx, tenant, tasks)
	}
}
