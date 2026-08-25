package hub

import (
	"context"
	"testing"
	"time"
)

// Edge detection is the whole job. Every mistake here has the same shape — a
// notification every tick, forever — and it is not a shape anyone spots from a
// single observation.
func TestTaskWatcher(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	w := newTaskWatcher(func() time.Time { return now })
	ctx := context.Background()

	task := func(quiet time.Duration) Task {
		return Task{ID: "t1", SessionID: "claude-worker-1", RequestedBy: "claude-boss-1",
			State: TaskActive, Brief: "look at the build", UpdatedAt: now.Add(-quiet).Unix()}
	}

	// Recently touched: nothing to say, however often we look.
	for i := 0; i < 5; i++ {
		if due := w.observe(ctx, "alex", []Task{task(time.Minute)}); len(due) != 0 {
			t.Fatalf("a task touched a minute ago was chased")
		}
	}

	// Gone quiet past the threshold: mentioned once...
	if due := w.observe(ctx, "alex", []Task{task(taskStale + time.Minute)}); len(due) != 1 {
		t.Fatalf("a stale task was not raised: %d", len(due))
	}
	// ...and not again on every subsequent look, which is the failure mode.
	for i := 0; i < 5; i++ {
		now = now.Add(10 * time.Minute)
		if due := w.observe(ctx, "alex", []Task{task(taskStale + time.Hour)}); len(due) != 0 {
			t.Fatalf("a task already raised was raised again")
		}
	}

	// Still quiet a day later: one reminder, not a stream. Work stalled over a
	// weekend should not be represented by a single alert from Friday.
	now = now.Add(taskRepeat)
	if due := w.observe(ctx, "alex", []Task{task(taskStale + taskRepeat)}); len(due) != 1 {
		t.Errorf("a long-stalled task was never re-raised")
	}
}

// Touching a task resets it, so a task that stalls, moves, and stalls again is
// two events rather than a permanent alarm.
func TestTouchingATaskResetsIt(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	w := newTaskWatcher(func() time.Time { return now })
	ctx := context.Background()

	stale := Task{ID: "t1", SessionID: "s", State: TaskActive, Brief: "x",
		UpdatedAt: now.Add(-taskStale - time.Minute).Unix()}
	if due := w.observe(ctx, "alex", []Task{stale}); len(due) != 1 {
		t.Fatal("setup: expected the stale task to be raised")
	}

	// Somebody moves it.
	fresh := stale
	fresh.UpdatedAt = now.Unix()
	w.observe(ctx, "alex", []Task{fresh})

	// It goes quiet again: a fresh event, not silence inherited from before.
	now = now.Add(taskStale + time.Minute)
	quietAgain := stale
	quietAgain.UpdatedAt = now.Add(-taskStale - time.Minute).Unix()
	if due := w.observe(ctx, "alex", []Task{quietAgain}); len(due) != 1 {
		t.Error("a task that stalled a second time was never raised")
	}
}

// Finished work is not stale, it is finished.
func TestFinishedTasksAreNotChased(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	w := newTaskWatcher(func() time.Time { return now })

	for _, state := range []string{TaskDone, TaskDropped} {
		k := Task{ID: "t-" + state, SessionID: "s", State: state, Brief: "x",
			UpdatedAt: now.Add(-30 * 24 * time.Hour).Unix()}
		if due := w.observe(context.Background(), "alex", []Task{k}); len(due) != 0 {
			t.Errorf("a %s task from a month ago was chased", state)
		}
	}
}

func TestShortSessionName(t *testing.T) {
	for in, want := range map[string]string{
		"claude-homelab-wsl-4b602ded": "homelab-wsl",
		"claude-worker-1":             "worker-1",
		"":                            "nobody",
	} {
		if got := shortSessionName(in); got != want {
			t.Errorf("shortSessionName(%q) = %q, want %q", in, got, want)
		}
	}
}
