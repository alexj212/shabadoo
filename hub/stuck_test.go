package hub

import (
	"context"
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
