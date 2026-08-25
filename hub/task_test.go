package hub

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The question this exists to answer: what did I hand off, and where did it get
// to. Before this the sender remembered, or nobody did.
func TestTaskLifecycle(t *testing.T) {
	ctx, s, now := context.Background(), testStore(t), time.Now()

	task, err := s.CreateTask(ctx, Task{
		SessionID: "claude-worker-1", RequestedBy: "claude-boss-1",
		Brief: "Look at the failing build",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if task.State != TaskOpen {
		t.Errorf("new task state = %q, want %q", task.State, TaskOpen)
	}

	// The assignee sees it on their plate...
	mine, err := s.Tasks(ctx, "claude-worker-1", "", false, 0)
	if err != nil || len(mine) != 1 {
		t.Fatalf("assignee sees %d tasks (err %v), want 1", len(mine), err)
	}
	// ...and the asker can see where it got to, which is the whole point.
	delegated, _ := s.Tasks(ctx, "", "claude-boss-1", false, 0)
	if len(delegated) != 1 {
		t.Fatalf("asker sees %d delegated tasks, want 1", len(delegated))
	}

	// Blocked keeps the reason. A task stalled with no note is a question for
	// whoever asked, which is the situation this removes.
	blocked, err := s.UpdateTask(ctx, task.ID, TaskBlocked, "waiting on credentials", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if blocked.State != TaskBlocked || blocked.Note != "waiting on credentials" {
		t.Errorf("blocked task = %+v", blocked)
	}

	// An update with no note must not erase the last thing the assignee said.
	moved, _ := s.UpdateTask(ctx, task.ID, TaskActive, "", now.Add(2*time.Minute))
	if moved.Note != "waiting on credentials" {
		t.Errorf("an empty note erased the previous one: %q", moved.Note)
	}

	// Finished work leaves the outstanding list but is still findable.
	if _, err := s.UpdateTask(ctx, task.ID, TaskDone, "fixed", now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if open, _ := s.Tasks(ctx, "claude-worker-1", "", false, 0); len(open) != 0 {
		t.Errorf("a finished task is still outstanding: %d", len(open))
	}
	if all, _ := s.Tasks(ctx, "claude-worker-1", "", true, 0); len(all) != 1 {
		t.Errorf("a finished task became unfindable: %d", len(all))
	}
}

// A task with nothing in it would be delivered, chased, and impossible to act
// on — the same defect empty messages turned out to be.
func TestTaskRefusesAnEmptyBrief(t *testing.T) {
	ctx, s, now := context.Background(), testStore(t), time.Now()

	for _, brief := range []string{"", "   ", "\n"} {
		if _, err := s.CreateTask(ctx, Task{SessionID: "claude-w-1", Brief: brief}, now); err == nil {
			t.Errorf("a task with brief %q was accepted", brief)
		}
	}
	if _, err := s.CreateTask(ctx, Task{Brief: "real work"}, now); err == nil {
		t.Error("a task with no assignee was accepted")
	}
}

// An unknown state must be refused rather than stored. A vocabulary that
// accepts anything is one nobody can query against.
func TestTaskRefusesAnUnknownState(t *testing.T) {
	ctx, s, now := context.Background(), testStore(t), time.Now()
	task, err := s.CreateTask(ctx, Task{SessionID: "claude-w-1", Brief: "x"}, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.UpdateTask(ctx, task.ID, "nearly-done", "", now)
	if err == nil {
		t.Fatal("an invented state was accepted")
	}
	if !strings.Contains(err.Error(), "open") {
		t.Errorf("the error does not say what the valid states are: %v", err)
	}
	if _, err := s.UpdateTask(ctx, "no-such-task", TaskDone, "", now); err == nil {
		t.Error("updating a task that does not exist reported success")
	}
}
