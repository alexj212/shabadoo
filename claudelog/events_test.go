package claudelog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func turnLine(t *testing.T, typ, role, text string) string {
	t.Helper()
	return fmt.Sprintf(
		`{"type":%q,"timestamp":"2026-08-30T10:00:00Z","message":{"role":%q,"content":[{"type":"text","text":%q}]}}`,
		typ, role, text)
}

// noise is a record a reader never sees: real transcripts are mostly these.
func noiseLine(t *testing.T) string {
	t.Helper()
	return `{"type":"file-history-snapshot","timestamp":"2026-08-30T10:00:00Z"}`
}

func writeJSONL(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// The tail must count READABLE TURNS, not lines.
//
// This is the defect the first implementation shipped with, and it is invisible
// from a fixture of pure messages: a transcript is mostly records nobody sees —
// tool plumbing, meta injections, snapshots — so a loop that stops after N lines
// returns nothing at all. Against a live 113 MB transcript it returned zero
// events for limit=4, because the last four lines were all noise.
//
// Pinned as the distinction rather than one side of it: a file whose messages
// are buried under noise must return the SAME turns as one without the noise.
func TestTailCountsTurnsNotLines(t *testing.T) {
	clean := writeJSONL(t,
		turnLine(t, "user", "user", "first"),
		turnLine(t, "assistant", "assistant", "second"),
	)
	buried := writeJSONL(t,
		turnLine(t, "user", "user", "first"),
		turnLine(t, "assistant", "assistant", "second"),
		noiseLine(t), noiseLine(t), noiseLine(t), noiseLine(t), noiseLine(t), noiseLine(t),
	)

	a, err := Events(clean, EventOpts{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Events(buried, EventOpts{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Events) != 2 {
		t.Fatalf("clean: want 2 turns, got %d", len(a.Events))
	}
	if len(b.Events) != len(a.Events) {
		t.Fatalf("noise changed the answer: %d turns buried vs %d clean — a tail that "+
			"counts lines returns nothing here", len(b.Events), len(a.Events))
	}
	if b.Events[0].Text != "first" || b.Events[1].Text != "second" {
		t.Fatalf("wrong turns or wrong order: %+v", b.Events)
	}
}

// Newest LAST, like every chat a person has used. Asserted as an ordering
// between two turns that must differ, because a reader that returned them in
// either order would satisfy any single-message fixture.
func TestOrderIsOldestFirst(t *testing.T) {
	p := writeJSONL(t,
		turnLine(t, "user", "user", "older"),
		turnLine(t, "assistant", "assistant", "newer"),
	)
	page, err := Events(p, EventOpts{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 || page.Events[0].Text != "older" || page.Events[1].Text != "newer" {
		t.Fatalf("want oldest first, got %+v", page.Events)
	}
	if page.Events[0].Offset >= page.Events[1].Offset {
		t.Fatalf("offsets must increase with the conversation: %d then %d",
			page.Events[0].Offset, page.Events[1].Offset)
	}
}

// The append contract, which is the whole reason the cursor exists: polling an
// unchanged file returns NOTHING, and polling after one turn was appended
// returns exactly that turn.
//
// Both halves are needed. A reader that always returns everything passes the
// second assertion; one that always returns nothing passes the first.
func TestCursorReturnsOnlyWhatWasAppended(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "t.jsonl")
	body := turnLine(t, "user", "user", "first") + "\n" + turnLine(t, "assistant", "assistant", "second") + "\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := Events(p, EventOpts{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}

	quiet, err := Events(p, EventOpts{After: first.Cursor, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(quiet.Events) != 0 {
		t.Fatalf("an unchanged file must yield nothing to poll, got %d: %+v",
			len(quiet.Events), quiet.Events)
	}

	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(noiseLine(t) + "\n" + turnLine(t, "assistant", "assistant", "third") + "\n")
	f.Close()

	got, err := Events(p, EventOpts{After: quiet.Cursor, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Events) != 1 || got.Events[0].Text != "third" {
		t.Fatalf("want exactly the appended turn, got %+v", got.Events)
	}
}

// `more` is what lets a client scrolling up stop. "There is more above" and
// "this is the beginning" must not render alike — an offset of 0 is also a real
// file position, so Prev alone cannot carry the difference.
func TestMoreSeparatesTheBeginningFromMore(t *testing.T) {
	lines := []string{}
	for i := 0; i < 6; i++ {
		lines = append(lines, turnLine(t, "user", "user", fmt.Sprintf("turn %d", i)))
	}
	p := writeJSONL(t, lines...)

	partial, err := Events(p, EventOpts{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !partial.More {
		t.Fatal("two of six turns: more must be true")
	}
	whole, err := Events(p, EventOpts{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if whole.More {
		t.Fatal("all six turns: more must be false, or a client scrolls up forever")
	}
	if whole.Prev != 0 {
		t.Fatalf("reaching the start must report prev 0, got %d", whole.Prev)
	}

	// And paging back from the partial page reaches the earlier turns rather
	// than repeating the ones already shown.
	older, err := Events(p, EventOpts{Before: partial.Prev, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(older.Events) == 0 {
		t.Fatal("paging back returned nothing")
	}
	for _, o := range older.Events {
		for _, n := range partial.Events {
			if o.Text == n.Text {
				t.Fatalf("paging back repeated a turn already shown: %q", o.Text)
			}
		}
	}
}

// A stale cursor past the end of a shrunken file must not be trusted: the
// transcript was rotated or replaced, and reading from that offset would splice
// two conversations together. Falls back to a tail.
func TestShrunkenFileResetsToTail(t *testing.T) {
	p := writeJSONL(t, turnLine(t, "user", "user", "only turn"))
	page, err := Events(p, EventOpts{After: 1 << 30, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.Events[0].Text != "only turn" {
		t.Fatalf("want a tail, got %+v", page.Events)
	}
}

// Clamping is in RUNES and reports the true length. A byte cut splits a
// multi-byte character into invalid UTF-8, which encoding/json then replaces
// silently — so the corruption reaches the client looking like content.
func TestClampIsRuneSafeAndReportsTheTruth(t *testing.T) {
	long := strings.Repeat("é", maxText+50)
	p := writeJSONL(t, turnLine(t, "user", "user", long))
	page, err := Events(p, EventOpts{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	e := page.Events[0]
	if !e.Truncated {
		t.Fatal("a clamped message must say so")
	}
	if e.Len != maxText+50 {
		t.Fatalf("true length must survive clamping: got %d, want %d", e.Len, maxText+50)
	}
	if !utf8.ValidString(e.Text) {
		t.Fatal("clamp emitted invalid UTF-8")
	}
	if got := utf8.RuneCountInString(e.Text); got > maxText+1 { // +1 for the ellipsis
		t.Fatalf("clamped text is %d runes, over the %d bound", got, maxText)
	}
}

// A tool call belongs under the turn that made it, collapsed. Pinned because
// tool blocks are most of a transcript's bytes: a reader that inlined them
// would blow the 8 MB ceiling this endpoint is bounded by.
func TestToolCallsAreCollapsedUnderTheirTurn(t *testing.T) {
	rec := `{"type":"assistant","timestamp":"2026-08-30T10:00:00Z","message":{"role":"assistant",` +
		`"content":[{"type":"text","text":"running it"},` +
		`{"type":"tool_use","name":"Bash","input":{"command":"ls -la"}}]}}`
	p := writeJSONL(t, rec)
	page, err := Events(p, EventOpts{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 {
		t.Fatalf("want one turn, got %d", len(page.Events))
	}
	e := page.Events[0]
	if e.Text != "running it" {
		t.Fatalf("text lost: %q", e.Text)
	}
	if len(e.Tools) != 1 || e.Tools[0].Name != "Bash" {
		t.Fatalf("tool call lost: %+v", e.Tools)
	}
	if !strings.Contains(e.Tools[0].Input, "ls -la") {
		t.Fatalf("tool input lost: %q", e.Tools[0].Input)
	}
}

// Unreadable lines are dropped, never rendered. A live transcript's last line is
// routinely a partial write, and a page whose final row read "could not parse"
// would show that every few seconds — training the reader to ignore the one time
// it means something.
func TestPartialLastLineIsDroppedNotRendered(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.jsonl")
	body := turnLine(t, "user", "user", "complete") + "\n" + `{"type":"assist`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	page, err := Events(p, EventOpts{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.Events[0].Text != "complete" {
		t.Fatalf("want only the complete turn, got %+v", page.Events)
	}
}
