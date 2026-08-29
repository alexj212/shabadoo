package hub

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func seedTasks(t *testing.T, tn *Tenant, n int) {
	t.Helper()
	base := time.Unix(1_700_000_000, 0)
	for i := 0; i < n; i++ {
		if _, err := tn.CreateTask(context.Background(), Task{
			SessionID: "s1", RequestedBy: "asker",
			Brief: strings.Repeat("x", 10),
		}, base.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
}

// History must return every row exactly once when paged all the way through.
// That is the property that makes a cursor worth having, and it is the
// direction that has to hold it: `before` orders by creation, which never
// changes, so it cannot skip or repeat a row while somebody edits one.
func TestCursorPagesHistoryExactlyOnce(t *testing.T) {
	tn := testStore(t)
	seedTasks(t, tn, 25)

	seen := map[string]int{}
	cursor := ""
	for i := 0; i < 20; i++ {
		list, page, err := tn.TasksPage(context.Background(),
			TaskQuery{Limit: 7, IncludeDone: true, Before: cursor})
		if err != nil {
			t.Fatalf("page %d: %v", i, err)
		}
		for _, k := range list {
			seen[k.ID]++
		}
		if len(list) == 0 {
			break
		}
		cursor = page.Next
	}
	if len(seen) != 25 {
		t.Errorf("saw %d distinct rows, want 25", len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("row %s returned %d times", id, n)
		}
	}
}

// A tail returns what has CHANGED since the cursor, and nothing otherwise.
//
// This is why the two directions order by different columns: a task that moves
// from open to blocked must come back to somebody tailing, and must NOT shift
// position for somebody paging history. Testing it by enumeration would be
// testing the wrong thing — a tail from "now" correctly returns nothing.
func TestCursorTailReturnsOnlyWhatChanged(t *testing.T) {
	tn := testStore(t)
	seedTasks(t, tn, 5)

	_, page, err := tn.TasksPage(context.Background(), TaskQuery{Limit: 10, IncludeDone: true})
	if err != nil {
		t.Fatal(err)
	}
	if page.Tail == "" {
		t.Fatal("an initial page did not hand back a forward cursor")
	}

	quiet, _, err := tn.TasksPage(context.Background(),
		TaskQuery{Limit: 10, IncludeDone: true, After: page.Tail})
	if err != nil {
		t.Fatal(err)
	}
	if len(quiet) != 0 {
		t.Errorf("a tail with nothing new returned %d rows", len(quiet))
	}

	// Change one, and only that one comes back.
	all, _, _ := tn.TasksPage(context.Background(), TaskQuery{Limit: 10, IncludeDone: true})
	target := all[2].ID
	later := time.Unix(1_700_000_000, 0).Add(time.Hour)
	if _, err := tn.UpdateTask(context.Background(), target, TaskBlocked, "waiting on a peer", later); err != nil {
		t.Fatal(err)
	}

	moved, _, err := tn.TasksPage(context.Background(),
		TaskQuery{Limit: 10, IncludeDone: true, After: page.Tail})
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 1 || moved[0].ID != target {
		t.Fatalf("tail returned %d rows, want exactly the one that changed", len(moved))
	}
	if moved[0].State != TaskBlocked {
		t.Errorf("the tail returned a stale copy: state %q", moved[0].State)
	}
}

// `next` must come back even when the page is empty, or catching up is inferred
// from a zero-length array rather than stated.
func TestCursorAlwaysReturnsNext(t *testing.T) {
	tn := testStore(t)
	list, page, err := tn.TasksPage(context.Background(), TaskQuery{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("expected an empty collection, got %d", len(list))
	}
	if page.Next == "" {
		t.Error("no cursor on an empty page — a client cannot tell 'current' " +
			"from 'this endpoint does not page'")
	}
}

// A cursor minted for one direction must be refused by the other, not silently
// honoured — paging the wrong way at a boundary looks like data.
func TestCursorRefusesTheWrongDirection(t *testing.T) {
	tn := testStore(t)
	seedTasks(t, tn, 3)

	_, page, err := tn.TasksPage(context.Background(), TaskQuery{Limit: 1, IncludeDone: true})
	if err != nil {
		t.Fatal(err)
	}
	// That is a history cursor; hand it to the tail.
	_, _, err = tn.TasksPage(context.Background(), TaskQuery{After: page.Next, IncludeDone: true})
	if !errors.Is(err, ErrCursorExpired) {
		t.Errorf("a backward cursor was accepted forward: %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "pages") {
		t.Errorf("the refusal does not say which way each pages: %v", err)
	}
}

// Garbage is refused as expired rather than restarting from the beginning,
// which would append a whole collection a second time and read as new activity.
func TestCursorRefusesGarbage(t *testing.T) {
	tn := testStore(t)
	if _, _, err := tn.TasksPage(context.Background(), TaskQuery{Before: "not-a-cursor"}); !errors.Is(err, ErrCursorExpired) {
		t.Errorf("garbage cursor: got %v, want ErrCursorExpired", err)
	}
}

// A short page must say WHY. "Ten because ten was the ceiling" and "ten because
// those ten were enormous" mean opposite things to a client tuning its scroll.
func TestCursorSaysWhyAPageWasShort(t *testing.T) {
	tn := testStore(t)
	seedTasks(t, tn, 10)

	_, page, err := tn.TasksPage(context.Background(), TaskQuery{Limit: 4, IncludeDone: true})
	if err != nil {
		t.Fatal(err)
	}
	if page.Clamped != "count" {
		t.Errorf("a full page must say it was count-limited, got %q", page.Clamped)
	}

	// A page served whole says nothing, so absence means complete.
	_, page, err = tn.TasksPage(context.Background(), TaskQuery{Limit: 50, IncludeDone: true})
	if err != nil {
		t.Fatal(err)
	}
	if page.Clamped != "" {
		t.Errorf("a complete page claimed to be clamped: %q", page.Clamped)
	}
}

// Truncation is declared with the TRUE size, never a short string passed off as
// the whole thing.
func TestCursorDeclaresTruncationWithTheRealSize(t *testing.T) {
	tn := testStore(t)
	long := strings.Repeat("y", 5000)
	if _, err := tn.CreateTask(context.Background(), Task{
		SessionID: "s1", RequestedBy: "asker", Brief: long,
	}, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	list, _, err := tn.TasksPage(context.Background(), TaskQuery{Limit: 5, IncludeDone: true})
	if err != nil || len(list) != 1 {
		t.Fatalf("list=%d err=%v", len(list), err)
	}
	k := list[0]
	if !k.BriefTruncated {
		t.Fatal("a 5000-byte brief was not declared truncated")
	}
	if k.BriefBytes != len(long) {
		t.Errorf("brief_bytes = %d, want the TRUE size %d", k.BriefBytes, len(long))
	}
	if len(k.Brief) >= len(long) {
		t.Error("nothing was actually cut")
	}
}
