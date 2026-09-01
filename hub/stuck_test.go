package hub

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func stuckFixture(now time.Time) (*stuckWatcher, *[]string) {
	var sent []string
	w := newStuckWatcher(func() time.Time { return now })
	w.send = func(_ context.Context, _, title, _, _ string) error {
		sent = append(sent, title)
		return nil
	}
	return w, &sent
}

// Every mistake in edge detection has the same shape, so all of it is pinned:
// this watcher exists because the nudge failed silently, and a watcher that
// itself fails silently would be worse than none.
func TestStuckWatcherIsEdgeTriggeredAfterGrace(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	online := func(string) bool { return true }
	sess := []Session{{SessionID: "s1", Agent: "wsl", Alias: "homelab", Pending: 2, InputState: "composer"}}

	w, sent := stuckFixture(base)
	w.observe(context.Background(), "t", sess, online)
	if len(*sent) != 0 {
		t.Fatal("notified immediately; a recipient may legitimately be mid-turn")
	}

	// Still inside the grace period.
	w.now = func() time.Time { return base.Add(stuckGrace - time.Second) }
	w.observe(context.Background(), "t", sess, online)
	if len(*sent) != 0 {
		t.Fatal("notified before the grace period elapsed")
	}

	// Past it: exactly one notification, not one per sweep.
	w.now = func() time.Time { return base.Add(stuckGrace + time.Second) }
	w.observe(context.Background(), "t", sess, online)
	w.observe(context.Background(), "t", sess, online)
	w.observe(context.Background(), "t", sess, online)
	if len(*sent) != 1 {
		t.Fatalf("sent %d notifications for one stuck session, want 1 — level-triggering "+
			"would notify every two minutes forever", len(*sent))
	}
}

// Draining resets the state, so being stuck twice is two events rather than a
// continuation of the first.
func TestStuckWatcherResetsWhenMailIsPickedUp(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	online := func(string) bool { return true }
	stuck := []Session{{SessionID: "s1", Agent: "wsl", Pending: 1, InputState: "composer"}}
	drained := []Session{{SessionID: "s1", Agent: "wsl", Pending: 0, InputState: "composer"}}

	w, sent := stuckFixture(base)
	w.observe(context.Background(), "t", stuck, online)
	w.now = func() time.Time { return base.Add(stuckGrace + time.Second) }
	w.observe(context.Background(), "t", stuck, online)
	if len(*sent) != 1 {
		t.Fatalf("want 1 notification, got %d", len(*sent))
	}

	w.observe(context.Background(), "t", drained, online) // picked up
	w.observe(context.Background(), "t", stuck, online)   // stuck again, clock restarts
	if len(*sent) != 1 {
		t.Error("notified again immediately; draining must restart the grace period")
	}
	w.now = func() time.Time { return base.Add(2*stuckGrace + 2*time.Second) }
	w.observe(context.Background(), "t", stuck, online)
	if len(*sent) != 2 {
		t.Error("a second stall must be a second event, not a continuation")
	}
}

// Two states that are NOT stuck, and reporting either would train somebody to
// ignore this notifier.
func TestStuckWatcherIgnoresOfflineAndBlocked(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	late := func() time.Time { return base.Add(stuckGrace * 3) }

	for _, c := range []struct {
		name   string
		sess   Session
		online func(string) bool
	}{
		{"offline: mail is MEANT to wait, the delivery row is the wait",
			Session{SessionID: "s1", Agent: "mac", Pending: 3, InputState: "composer"},
			func(string) bool { return false }},
		{"on a dialog: that is blockedWatcher's job, and two names for one state is noise",
			Session{SessionID: "s1", Agent: "wsl", Pending: 3, InputState: "dialog"},
			func(string) bool { return true }},
		{"no mail at all",
			Session{SessionID: "s1", Agent: "wsl", Pending: 0, InputState: "composer"},
			func(string) bool { return true }},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, sent := stuckFixture(base)
			w.observe(context.Background(), "t", []Session{c.sess}, c.online)
			w.now = late
			w.observe(context.Background(), "t", []Session{c.sess}, c.online)
			if len(*sent) != 0 {
				t.Errorf("notified: %v", *sent)
			}
		})
	}
}

// The retry must come before the human, and must happen once.
//
// The nudge fires on ARRIVAL only, so mail missed once is never retried and a
// backlog never clears itself — which is exactly what ten hours of skipped
// nudges left behind. Retrying is free: the condition this watcher fires on is
// the same condition a nudge is already safe to send under.
func TestStuckWatcherRetriesTheNudgeBeforeTellingAnyone(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	online := func(string) bool { return true }
	sess := []Session{{SessionID: "s1", Agent: "wsl", Pending: 1, InputState: "composer"}}

	w, sent := stuckFixture(base)
	var retries int
	w.retry = func(context.Context, string, string) { retries++ }

	w.observe(context.Background(), "t", sess, online)
	if retries != 0 {
		t.Fatal("retried immediately; the recipient may simply be mid-turn")
	}

	w.now = func() time.Time { return base.Add(stuckRetry + time.Second) }
	w.observe(context.Background(), "t", sess, online)
	w.observe(context.Background(), "t", sess, online)
	if retries != 1 {
		t.Fatalf("retried %d times, want exactly 1 — a retry every sweep is the "+
			"level-triggered failure in a new place", retries)
	}
	if len(*sent) != 0 {
		t.Error("a human was told before the retry had a chance to work")
	}

	// Still stuck after the retry: now a person is the right escalation.
	w.now = func() time.Time { return base.Add(stuckGrace + time.Second) }
	w.observe(context.Background(), "t", sess, online)
	if len(*sent) != 1 {
		t.Errorf("want 1 notification once the retry failed, got %d", len(*sent))
	}
}

// Both actions must be recorded, or this watcher is as unobservable as the
// nudge it exists to catch — which is the failure that produced it.
func TestStuckWatcherAuditsWhatItDid(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	online := func(string) bool { return true }
	sess := []Session{{SessionID: "s1", Agent: "wsl", Pending: 2, InputState: "composer"}}

	w, _ := stuckFixture(base)
	var entries []string
	w.audit = func(_ context.Context, _, target, detail string) {
		entries = append(entries, target+": "+detail)
	}
	w.retry = func(context.Context, string, string) {}

	w.observe(context.Background(), "t", sess, online)
	w.now = func() time.Time { return base.Add(stuckRetry + time.Second) }
	w.observe(context.Background(), "t", sess, online)
	if len(entries) != 1 || !strings.Contains(entries[0], "re-nudged") {
		t.Fatalf("the retry was not recorded: %v", entries)
	}

	w.now = func() time.Time { return base.Add(stuckGrace + time.Second) }
	w.observe(context.Background(), "t", sess, online)
	if len(entries) != 2 || !strings.Contains(entries[1], "notified") {
		t.Fatalf("the escalation was not recorded: %v", entries)
	}
	// The count travels with it: "notified about 2 unread" is actionable,
	// "notified" alone sends somebody back to the API to find out about what.
	if !strings.Contains(entries[1], "2 unread") {
		t.Errorf("audit detail lacks the count: %q", entries[1])
	}
}

// A notification that failed to send must not be recorded as having been sent.
// A log claiming somebody was told is worse than a silent failure, because it
// stops anybody looking further.
func TestStuckWatcherDoesNotAuditAFailedNotification(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	w := newStuckWatcher(func() time.Time { return base })
	w.send = func(context.Context, string, string, string, string) error {
		return errors.New("notifier unreachable")
	}
	var entries int
	w.audit = func(context.Context, string, string, string) { entries++ }

	sess := []Session{{SessionID: "s1", Agent: "wsl", Pending: 1, InputState: "composer"}}
	w.observe(context.Background(), "t", sess, func(string) bool { return true })
	w.now = func() time.Time { return base.Add(stuckGrace + time.Second) }
	w.observe(context.Background(), "t", sess, func(string) bool { return true })

	if entries != 0 {
		t.Error("recorded a notification that was never delivered")
	}
}

// A session held by the wake cap is QUEUED, not STUCK, and must not page anyone.
//
// Without this the cap added to calm the fleet becomes the thing that generates
// the alarm: mail is held on purpose, the hold outlasts stuckGrace, and a human
// is notified about a wait the system chose. Three states have to stay
// distinguishable — idle has nothing to do, queued needs nobody, stuck needs a
// person.
//
// Pinned as a PAIR. An assertion that a queued session is silent passes just as
// well for a watcher that has stopped notifying anybody, so the control runs the
// identical fixture with the hold removed and requires the notification.
func TestQueuedByTheCapIsNotStuck(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	online := func(string) bool { return true }
	sess := []Session{{SessionID: "s1", Agent: "wsl", Alias: "iptv", Pending: 2, InputState: "composer"}}
	past := base.Add(stuckGrace + time.Minute)

	t.Run("held by the cap: silent", func(t *testing.T) {
		w, sent := stuckFixture(base)
		w.queued = func(string) bool { return true }
		w.observe(context.Background(), "t", sess, online)
		w.now = func() time.Time { return past }
		w.observe(context.Background(), "t", sess, online)
		if len(*sent) != 0 {
			t.Fatalf("paged a human about mail the cap is deliberately holding: %v", *sent)
		}
	})

	t.Run("control: the same wait with no hold DOES notify", func(t *testing.T) {
		w, sent := stuckFixture(base)
		w.queued = func(string) bool { return false }
		w.observe(context.Background(), "t", sess, online)
		w.now = func() time.Time { return past }
		w.observe(context.Background(), "t", sess, online)
		if len(*sent) == 0 {
			t.Fatal("genuinely stuck mail must still reach a person, or the " +
				"assertion above only proves the watcher went quiet")
		}
	})
}
