package tmux

import (
	"strings"
	"testing"
)

// Input rows captured with `capture-pane -e` on wsl, 2026-10-23, Claude Code
// on the U+00A0 build. Only the words were changed. The escape sequences and
// the separator byte are as captured.
const (
	// A prompt suggestion: faint, cursor at column 2 in the live pane.
	rowSuggestion = "\x1b[39m❯ \x1b[2mjournal today was productive\x1b[0m\x1b[39m\x1b[49m"
	// A real draft: the same row at normal intensity, cursor at its end.
	rowTyped = "\x1b[39m❯ retry the deploy"
	// Empty, as homelab rendered it: coloured marker, nothing after.
	rowEmpty  = "\x1b[38;5;246m❯ \x1b[39m"
	ruleColor = "\x1b[38;5;244m────────────────────────────────\x1b[39m"
)

func framed(row string) string {
	return "\x1b[38;5;246m✻\x1b[39m Churned for 14s\n" + ruleColor + "\n" + row + "\n" + ruleColor + "\n" +
		"  \x1b[38;5;231m\x1b[48;5;136m wsl \x1b[39m\n" +
		"  \x1b[38;5;211m⏵⏵ bypass permissions on\x1b[38;5;246m (shift+tab to cycle)\x1b[39m\n"
}

// The axis is faint versus normal intensity on the same row. Everything else
// is held: same frame, same marker, same separator byte.
func TestASuggestionIsNotADraftButTypedTextIs(t *testing.T) {
	if d, ok := ComposerDraft(Typed(framed(rowSuggestion))); !ok || d != "" {
		t.Errorf("suggestion read as draft %q (found=%v); want an empty composer", d, ok)
	}
	if d, ok := ComposerDraft(Typed(framed(rowTyped))); !ok || d != "retry the deploy" {
		t.Errorf("typed text read as %q (found=%v)", d, ok)
	}
	if d, ok := ComposerDraft(Typed(framed(rowEmpty))); !ok || d != "" {
		t.Errorf("empty row read as %q (found=%v)", d, ok)
	}
	// Plain keeps the suggestion. That is the defect, and the reason the
	// composer decisions read Typed.
	if d, _ := ComposerDraft(Plain(framed(rowSuggestion))); d == "" {
		t.Error("Plain dropped faint text; it must only strip escapes")
	}
}

// 38;5;2 is colour number 2, not the faint attribute. Reading it as faint would
// hide real text drawn in that colour.
func TestColourArgumentsAreNotAttributes(t *testing.T) {
	for _, in := range []string{"\x1b[38;5;2mkept\x1b[39m", "\x1b[48;2;2;2;2mkept\x1b[49m"} {
		if got := Typed(in); got != "kept" {
			t.Errorf("Typed(%q) = %q, want kept", in, got)
		}
	}
	if got := Typed("a\x1b[2mb\x1b[22mc\x1b[2md\x1b[mE"); got != "acE" {
		t.Errorf("faint on/off tracking: got %q, want acE", got)
	}
}

// A dialog footer may be faint. Plain must keep it, or InputState stops
// seeing dialogs.
func TestPlainKeepsFaintFooters(t *testing.T) {
	got := Plain("\x1b[2mEnter to confirm · Esc to cancel\x1b[0m")
	if !strings.Contains(got, "Enter to confirm") {
		t.Errorf("Plain lost faint text: %q", got)
	}
}

// Captured on the mac 2026-10-23 (task 281d6826), words replaced: the same
// faint suggestion after U+00A0, cursor at column 2.
func TestDarwinSuggestionIsDroppedToo(t *testing.T) {
	row := "\x1b[39m❯ \x1b[2mlaunch it in the simulator\x1b[0m"
	if d, ok := ComposerDraft(Typed(framed(row))); !ok || d != "" {
		t.Errorf("darwin suggestion read as %q (found=%v)", d, ok)
	}
}
