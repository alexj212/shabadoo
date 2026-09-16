package main

import (
	"strings"
	"testing"
)

// A row struck through as a whole must not leave half its markup behind.
//
// Four sessions in one night wrote a resolved row this way, because the skill
// asks that resolved rows stay visible and says nothing about where the markers
// go. `ownerToken` already strips the opening `~~` so the owner parses — but its
// closing partner stayed in the item and rendered as a stray `~~` everywhere the
// row is shown.
//
// The axis varied is WHERE the strikethrough sits: around the whole row, or
// inside the item. The pair is required because the tempting fix — strip every
// `~~` from item text — passes the first arm while destroying the second, and
// the second is markup the author wrote deliberately.
func TestAWholeRowStrikethroughLeavesNoOrphanedMarker(t *testing.T) {
	var m Mission
	m.addWaiting("- ~~you: the gitlab1 admin bit — who holds it?~~ **RESOLVED 2026-09-15**")

	if len(m.Waiting) != 1 {
		t.Fatalf("got %d rows, want 1", len(m.Waiting))
	}
	w := m.Waiting[0]
	if w.Owner != "you" {
		t.Errorf("owner = %q, want %q", w.Owner, "you")
	}
	if strings.Contains(w.Item, "~~") {
		t.Errorf("item kept an orphaned strikethrough marker: %q", w.Item)
	}
	if !strings.Contains(w.Item, "RESOLVED") {
		t.Errorf("item lost the content after the strikethrough: %q", w.Item)
	}
}

// The control: a strikethrough the author put INSIDE the item is balanced and
// deliberate. It survives untouched, and the owner still parses.
func TestAStrikethroughInsideTheItemIsLeftAlone(t *testing.T) {
	var m Mission
	m.addWaiting("- **you**: ~~the recommended form~~ **RESOLVED 2026-09-15**")

	if len(m.Waiting) != 1 {
		t.Fatalf("got %d rows, want 1", len(m.Waiting))
	}
	w := m.Waiting[0]
	if w.Owner != "you" {
		t.Errorf("owner = %q, want %q", w.Owner, "you")
	}
	if n := strings.Count(w.Item, "~~"); n != 2 {
		t.Errorf("item has %d `~~` markers, want both kept: %q", n, w.Item)
	}
}

// An ordinary row is untouched by any of this — the case that must keep working
// while the two above are being fixed.
func TestAnOrdinaryRowIsUnchanged(t *testing.T) {
	var m Mission
	m.addWaiting("- me: an ordinary unresolved row · risk: none · cost: none")

	if len(m.Waiting) != 1 {
		t.Fatalf("got %d rows, want 1", len(m.Waiting))
	}
	w := m.Waiting[0]
	if w.Owner != "me" {
		t.Errorf("owner = %q, want %q", w.Owner, "me")
	}
	if !strings.HasPrefix(w.Item, "an ordinary unresolved row") {
		t.Errorf("item = %q", w.Item)
	}
}
