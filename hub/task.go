package hub

// Delegated work that can be asked about later.
//
// Handing work to a peer was mail: acknowledged when drained, and after that
// the system knew nothing. There was no way to ask what had been handed off and
// never came back — the sender remembered, or nobody did. A hive with only
// messages is a group chat.
//
// The `tasks` table has been in the schema since the beginning with nothing
// reading or writing it. This is that, wired.
//
// See docs/direction.md and docs/build-plan.md (Phase 4).

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Task states. Deliberately few: every one a session must choose between is a
// decision it has to make correctly, and a vocabulary nobody can remember gets
// used wrongly rather than precisely.
const (
	TaskOpen    = "open"    // handed over, not yet picked up
	TaskActive  = "active"  // being worked on
	TaskBlocked = "blocked" // stalled on something, see the note
	TaskDone    = "done"
	TaskDropped = "dropped" // deliberately not doing it — an answer, not a failure
)

// terminal states need no chasing.
func terminalTask(state string) bool {
	return state == TaskDone || state == TaskDropped
}

func validTaskState(state string) bool {
	switch state {
	case TaskOpen, TaskActive, TaskBlocked, TaskDone, TaskDropped:
		return true
	}
	return false
}

// Task is one piece of delegated work.
type Task struct {
	ID          string `json:"id"`
	SessionID   string `json:"session_id"`   // who is meant to do it
	RequestedBy string `json:"requested_by"` // who asked
	Thread      string `json:"thread,omitempty"`
	State       string `json:"state"`
	Brief       string `json:"brief"`
	Note        string `json:"note,omitempty"` // the assignee's last word
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

// CreateTask records work handed to a session.
//
// The brief is required for the same reason a message body is: a task with
// nothing in it would be delivered, chased, and impossible to act on — which is
// exactly the defect that empty messages turned out to be.
func (t *Tenant) CreateTask(ctx context.Context, task Task, now time.Time) (Task, error) {
	if task.SessionID == "" {
		return Task{}, fmt.Errorf("task has no assignee")
	}
	if strings.TrimSpace(task.Brief) == "" {
		return Task{}, fmt.Errorf("task has no brief: nothing to act on and nothing to chase")
	}
	task.ID = newID()
	task.State = TaskOpen
	task.CreatedAt = now.Unix()
	task.UpdatedAt = now.Unix()

	_, err := t.s.db.ExecContext(ctx, `
		INSERT INTO tasks (tenant, id, session_id, requested_by, thread, state, brief, note,
		                   created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.id, task.ID, task.SessionID, task.RequestedBy, task.Thread, task.State,
		task.Brief, task.Note, task.CreatedAt, task.UpdatedAt)
	return task, err
}

// UpdateTask moves a task and records what the assignee said about it.
//
// The note matters most for `blocked`: a task stalled with no reason is a
// question for whoever asked, which is the situation this whole thing exists to
// remove.
func (t *Tenant) UpdateTask(ctx context.Context, id, state, note string, now time.Time) (Task, error) {
	if !validTaskState(state) {
		return Task{}, fmt.Errorf("unknown task state %q (open, active, blocked, done, dropped)", state)
	}
	res, err := t.s.db.ExecContext(ctx, `
		UPDATE tasks SET state = ?, note = CASE WHEN ? = '' THEN note ELSE ? END, updated_at = ?
		 WHERE tenant = ? AND id = ?`,
		state, note, note, now.Unix(), t.id, id)
	if err != nil {
		return Task{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Task{}, fmt.Errorf("no task %q", id)
	}
	return t.Task(ctx, id)
}

// Task reads one back.
func (t *Tenant) Task(ctx context.Context, id string) (Task, error) {
	row := t.s.db.QueryRowContext(ctx, taskCols+` WHERE tenant = ? AND id = ?`, t.id, id)
	return scanTask(row)
}

const taskCols = `
	SELECT id, session_id, requested_by, thread, state, brief, note, created_at, updated_at
	  FROM tasks`

type rowScanner interface{ Scan(...any) error }

func scanTask(r rowScanner) (Task, error) {
	var k Task
	err := r.Scan(&k.ID, &k.SessionID, &k.RequestedBy, &k.Thread, &k.State,
		&k.Brief, &k.Note, &k.CreatedAt, &k.UpdatedAt)
	return k, err
}

// Tasks lists work, newest first.
//
// `session` filters to one assignee, `requestedBy` to one asker, and both empty
// returns everything. The second of those is the question that could not be
// answered before: what did I hand off, and where did it get to.
func (t *Tenant) Tasks(ctx context.Context, session, requestedBy string, includeDone bool, limit int) ([]Task, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := taskCols + ` WHERE tenant = ?`
	args := []any{t.id}
	if session != "" {
		q += ` AND session_id = ?`
		args = append(args, session)
	}
	if requestedBy != "" {
		q += ` AND requested_by = ?`
		args = append(args, requestedBy)
	}
	if !includeDone {
		q += ` AND state NOT IN (?, ?)`
		args = append(args, TaskDone, TaskDropped)
	}
	q += ` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := t.s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Task{}
	for rows.Next() {
		k, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}
