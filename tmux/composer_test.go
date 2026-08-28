package tmux

import "testing"

// Every rendering of Claude Code's input row this has been seen to take, with
// an empty and a non-empty instance of each.
//
// The pairing is the point, and it is the lesson from the first version of this
// check. That one encoded the linux box and shipped green: its fixtures all
// matched linux, so nothing noticed that on darwin BOTH the empty and the
// typed-in pane parse to "cannot tell" and the node silently stops being
// nudged. A fixture test passes happily when the two cases are
// indistinguishable — which is the exact failure. So the property asserted
// below is that they must DIFFER, not that either matches a string.
var composerRenderings = []struct {
	name  string
	empty string
	busy  string
}{
	{
		// linux: a drawn box, ASCII ">", draft bounded by the closing edge.
		name:  "boxed ascii",
		empty: "some transcript output\n" +
			"╭──────────────────────────────╮\n" +
			"│ >                            │\n" +
			"╰──────────────────────────────╯\n",
		busy: "some transcript output\n" +
			"╭──────────────────────────────╮\n" +
			"│ > half a question about the  │\n" +
			"╰──────────────────────────────╯\n",
	},
	{
		// darwin, measured on a real pane: NO box characters anywhere, the
		// prompt is U+276F, horizontal rules above and below, and the row has
		// no closing delimiter — the draft runs to end of line.
		name: "unboxed heavy angle",
		empty: "──────────────────────────────────────── mac ─\n" +
			"❯ \n" +
			"──────────────────────────────────────────────\n" +
			"   mac  ✱ Opus 5                        /rc\n" +
			"  ⏵⏵ bypass permissions on\n",
		busy: "──────────────────────────────────────── mac ─\n" +
			"❯ half a question about the\n" +
			"──────────────────────────────────────────────\n" +
			"   mac  ✱ Opus 5                        /rc\n" +
			"  ⏵⏵ bypass permissions on\n",
	},
}

func TestComposerBusyDistinguishesEmptyFromTyped(t *testing.T) {
	for _, r := range composerRenderings {
		t.Run(r.name, func(t *testing.T) {
			gotEmpty, gotBusy := ComposerBusy(r.empty), ComposerBusy(r.busy)
			if gotEmpty == gotBusy {
				t.Fatalf("empty and typed-in both read as busy=%v — the check "+
					"cannot see this rendering at all, so this node would "+
					"silently never be nudged", gotEmpty)
			}
			if gotEmpty {
				t.Error("an empty composer must be nudgeable")
			}
			if !gotBusy {
				t.Error("a draft must suppress the nudge; typing there erases it")
			}
		})
	}
}

// Unrecognised must answer BUSY. A skipped nudge costs promptness on mail that
// is already stored; a wrong "idle" erases somebody's draft irrecoverably.
//
// This also covers a case nobody designed for: a pane in copy-mode, a pager, or
// a search overlay is neither a composer nor a dialog, and keys sent there land
// in the pager — where C-u is a half-page scroll. Falling through to busy makes
// that safe by construction rather than by having thought of it.
func TestComposerBusyFailsClosed(t *testing.T) {
	for _, c := range []struct{ name, pane string }{
		{"a dialog", "╭────────────────────────╮\n" +
			"│ Do you want to proceed? │\n" +
			"│ ❯ 1. Yes                │\n" +
			"│   2. No                 │\n" +
			"╰────────────────────────╯\n"},
		{"the resume prompt", "This session is 18d 4h old and 678.8k tokens.\n" +
			"❯ 1. Resume from summary (recommended)\n" +
			"  2. Resume full session as-is\n" +
			"  Enter to confirm · Esc to cancel\n"},
		{"a pager", "lines of output\n:\n"},
		{"nothing recognisable", "just some output\nand more of it\n"},
		{"empty pane", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if !ComposerBusy(c.pane) {
				t.Error("must read as busy: typing here is not safe")
			}
		})
	}
}

// The composer is at the BOTTOM. A row scrolled up into the transcript is not
// the input row any more, and typing at it types into whatever replaced it.
func TestComposerBusyIgnoresScrolledRow(t *testing.T) {
	pane := "❯ an old prompt from earlier\n" +
		"assistant replied at length\nand kept going\nand going\n" +
		"and going\nand going\nand going\n"
	if !ComposerBusy(pane) {
		t.Error("an input row scrolled out of the last few lines must read as busy")
	}
}

// A select list's cursor is not an input row, and the two are now told apart
// without relying on a box that only one platform draws.
//
// These are the cases that broke the first attempt at a cross-platform parser:
// the remote-control menu selects with an INDENTED ASCII ">", and darwin's
// resume prompt selects with "❯ 1." at column 0 — the same glyph and column as
// that platform's real composer.
func TestComposerDraftRejectsMenuCursors(t *testing.T) {
	for _, c := range []struct{ name, line string }{
		{"indented ascii cursor", "  > Continue"},
		{"numbered item, unboxed", "❯ 1. Resume from summary (recommended)"},
		{"numbered item, boxed", "│ ❯ 1. Yes                     │"},
		{"numbered item, ascii", "> 2. Resume full session as-is"},
		{"prose quoting a marker", "the shell prompt >is not this"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, ok := composerDraft(c.line); ok {
				t.Error("read as an input row; typing here types into a menu")
			}
		})
	}
}

func TestComposerDraftAcceptsBothRenderings(t *testing.T) {
	for _, c := range []struct {
		name, line, want string
	}{
		{"boxed empty", "│ >                          │", ""},
		{"boxed draft", "│ > half a question          │", "half a question"},
		{"unboxed empty", "❯ ", ""},
		{"unboxed draft", "❯ half a question", "half a question"},
		{"unboxed ascii", "> half a question", "half a question"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := composerDraft(c.line)
			if !ok {
				t.Fatal("not recognised as an input row")
			}
			if got != c.want {
				t.Errorf("draft = %q, want %q", got, c.want)
			}
		})
	}
}
