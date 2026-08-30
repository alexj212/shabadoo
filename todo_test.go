package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// These rows are prose full of em dashes and accented names, so a byte-based
// wrap emits invalid UTF-8 — which renders as replacement characters in the one
// column that carries the risk and the cost.
//
// Pinned as a distinction rather than an example: asserting "this string wraps
// to these lines" passes just as happily for a wrapper that has stopped
// splitting at all. What must be true is that a string too long for the column
// produces MORE lines than one that fits, and that every line is valid UTF-8
// and within the column.
func TestWrapIsRuneSafeAndActuallyWraps(t *testing.T) {
	const w = 24
	short := "revoke the token · 2 min"
	long := "revoke the Telegram bot token — still live in dm's .env · 2 min via @BotFather"

	a, b := wrapRunes(short, w), wrapRunes(long, w)
	if len(a) != 1 {
		t.Fatalf("a string that fits must not wrap: %d lines %q", len(a), a)
	}
	if len(b) <= len(a) {
		t.Fatalf("a long string must produce more lines than a short one: %d vs %d", len(b), len(a))
	}
	for _, lines := range [][]string{a, b} {
		for _, l := range lines {
			if !utf8.ValidString(l) {
				t.Fatalf("wrap emitted invalid UTF-8: %q", l)
			}
			if utf8.RuneCountInString(l) > w {
				t.Fatalf("line over the column width (%d): %q", w, l)
			}
		}
	}
	// Nothing may be dropped. A wrapper that silently loses the tail is the
	// worst version of this: the row still reads as a complete sentence.
	if !strings.Contains(strings.Join(b, " "), "@BotFather") {
		t.Fatalf("wrap lost the tail: %q", b)
	}
}

// A single token longer than the column must be split rather than blow the
// table out — URLs and absolute paths reach this, and a row wider than the
// terminal wraps into unreadable nonsense that looks like a rendering bug.
func TestWrapSplitsAnOverlongWord(t *testing.T) {
	const w = 12
	lines := wrapRunes(strings.Repeat("é", 40), w)
	if len(lines) < 4 {
		t.Fatalf("want the word split across lines, got %d: %q", len(lines), lines)
	}
	total := 0
	for _, l := range lines {
		if utf8.RuneCountInString(l) > w {
			t.Fatalf("line over width: %q", l)
		}
		if !utf8.ValidString(l) {
			t.Fatalf("split mid-rune: %q", l)
		}
		total += utf8.RuneCountInString(l)
	}
	if total != 40 {
		t.Fatalf("split lost or duplicated runes: %d of 40", total)
	}
}

// An age a person reads at a glance, and — the part that matters — zero and
// unknown must not render alike. A row with no measured age prints nothing
// rather than "0s", because "0s" is a measurement and this is its absence.
func TestShortAgeSeparatesUnknownFromZero(t *testing.T) {
	if got := shortAge(0); got != "" {
		t.Fatalf("unmeasured age must render empty, got %q", got)
	}
	if got := shortAge(-5); got != "" {
		t.Fatalf("a negative age is not a duration, got %q", got)
	}
	for _, c := range []struct {
		in   int64
		want string
	}{{30, "30s"}, {300, "5m"}, {7200, "2h"}, {3 * 86400, "3d"}} {
		if got := shortAge(c.in); got != c.want {
			t.Fatalf("shortAge(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}
