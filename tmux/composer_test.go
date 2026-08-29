package tmux

import (
	"os/exec"
	"strings"
	"testing"
)

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
	// captured marks a rendering taken from a live pane on this fleet, byte
	// for byte, rather than composed. Those are asserted at the BYTE level
	// below — see TestCapturedFixturesKeepTheNonBreakingSpace.
	captured bool
}{
	{
		// CAPTURED from a live pane on linux, byte for byte. The previous
		// fixture here was hand-written — a boxed ASCII "│ > " composer that
		// this machine has never drawn — and it passed while the real thing did
		// not match at all.
		//
		// The separator after ❯ is U+00A0, a NON-BREAKING space (c2 a0). A
		// parser requiring a plain space finds no input row, falls through to
		// "cannot tell", and every nudge on the fleet is skipped. That is not
		// hypothetical: it happened for ten hours and was found by a human
		// asking a session how it was doing.
		name:     "unboxed heavy angle, non-breaking space (linux)",
		captured: true,
		empty: "  \u23f5\u23f5 bypass permissions on (shift+tab to cycle)\n" +
			"\u276f\u00a0\n" +
			"\u2500\u2500\u2500\u2500\u2500\u2500 homelab-wsl \u2500\n",
		busy: "  \u23f5\u23f5 bypass permissions on (shift+tab to cycle)\n" +
			"\u276f\u00a0commit the doc change\n" +
			"\u2500\u2500\u2500\u2500\u2500\u2500 homelab-wsl \u2500\n",
	},
	{
		// CAPTURED on darwin, BOTH states, from that node's own hexdump: no box
		// characters anywhere in the pane, horizontal rules above and below,
		// and no closing delimiter — the draft runs to end of line.
		//
		// The separator is U+00A0 HERE TOO. This case said "plain space" for a
		// day, and the reason is the whole point of the file: I built it from a
		// sentence that node wrote — "the composer row reads ❯ with nothing
		// following it" — and a prose rendering of a non-breaking space is a
		// plain space by the time it has crossed a message and an editor. The
		// busy line arrived the same way and degraded the same way, a commit
		// after I wrote that fixtures must be captures.
		//
		// So a description of a capture is not a capture. The bytes came back
		// as `e2 9d af c2 a0` on both panes, which is what is encoded now.
		name:     "unboxed heavy angle, non-breaking space (darwin)",
		captured: true,
		empty: "\u2500\u2500\u2500\u2500 mac \u2500\n" +
			"\u276f\u00a0\n" +
			"\u2500\u2500\u2500\u2500\u2500\n" +
			"   mac  Opus 5\n",
		busy: "\u2500\u2500\u2500\u2500 mac \u2500\n" +
			"\u276f\u00a0delete the two test runs\n" +
			"\u2500\u2500\u2500\u2500\u2500\n" +
			"   mac  Opus 5\n",
	},
	{
		// A boxed ASCII composer, kept because older builds drew one and the
		// parser must still read it — but it is now the ONLY fixture here that
		// no machine in this fleet currently produces, and it is labelled so
		// nobody mistakes it for evidence.
		name:  "boxed ascii (historical, not observed on this fleet)",
		empty: "\u256d\u2500\u2500\u2500\u256e\n\u2502 >        \u2502\n\u2570\u2500\u2500\u2500\u256f\n",
		busy:  "\u256d\u2500\u2500\u2500\u256e\n\u2502 > half a question \u2502\n\u2570\u2500\u2500\u2500\u256f\n",
	},
}

// A captured fixture must still CONTAIN what was captured. The renderings above
// are the evidence this whole file rests on, and they are the one thing here
// that can rot without any test noticing: the parser stays correct, the pairing
// assertion stays green, and the fixtures quietly stop exercising the byte that
// actually broke the fleet.
//
// That is not a hypothetical failure mode, it is what happened. The darwin pair
// was carried here inside a peer's prose, arrived with U+0020, and was committed
// as "a real capture" — one commit after I wrote that fixtures must be captures.
// Nothing failed, because a plain space parses fine; the fixture had simply
// stopped testing the case it was added for.
//
// So the separator is asserted as BYTES rather than trusted to look right. A
// glyph cannot be reviewed: U+00A0 and U+0020 are the same picture.
func TestCapturedFixturesKeepTheNonBreakingSpace(t *testing.T) {
	for _, r := range composerRenderings {
		if !r.captured {
			continue
		}
		t.Run(r.name, func(t *testing.T) {
			for _, state := range []struct{ name, pane string }{
				{"empty", r.empty},
				{"busy", r.busy},
			} {
				if !strings.Contains(state.pane, "\u276f\u00a0") {
					t.Errorf("%s: no \u276f followed by U+00A0 — a captured "+
						"fixture must carry the bytes it was captured with",
						state.name)
				}
				if strings.Contains(state.pane, "\u276f ") {
					t.Errorf("%s: the separator has degraded to U+0020. This "+
						"renders identically and parses fine, so nothing else "+
						"here will fail — and the fixture has stopped covering "+
						"the byte that disabled every nudge on the fleet",
						state.name)
				}
			}
		})
	}
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

// Read the input row of every live pane on this machine.
//
// This is the test that was missing, and its absence cost ten hours of silently
// skipped nudges on the whole fleet. Every fixture above agreed with the parser
// because I wrote both; a machine running ten Claude sessions does not agree
// with anything, it just reports what is there.
//
// The assertion is deliberately weak — SOME pane must have a recognisable input
// row — because the strong version cannot be written: a pane may legitimately
// be mid-turn, in a pager, or holding a dialog. But zero recognisable rows
// across a host full of sessions can only mean the parser has stopped matching
// reality, which is exactly the state it was in.
func TestComposerDraftReadsLivePanes(t *testing.T) {
	out, err := exec.Command("tmux", "list-panes", "-a", "-F", "#{session_name}:#{window_index}.#{pane_index}").Output()
	if err != nil {
		t.Skipf("no tmux server here: %v", err)
	}
	targets := strings.Fields(strings.TrimSpace(string(out)))
	if len(targets) == 0 {
		t.Skip("no panes")
	}

	rows, panes := 0, 0
	for _, target := range targets {
		cap, err := exec.Command("tmux", "capture-pane", "-p", "-t", target).Output()
		if err != nil {
			continue
		}
		panes++
		lines := strings.Split(strings.TrimRight(string(cap), "\n"), "\n")
		if n := len(lines); n > 6 {
			lines = lines[n-6:]
		}
		for _, l := range lines {
			if _, ok := composerDraft(l); ok {
				rows++
				break
			}
		}
	}
	if panes == 0 {
		t.Skip("no panes could be captured")
	}
	t.Logf("recognised an input row in %d of %d live panes", rows, panes)
	if rows == 0 {
		t.Errorf("no input row recognised in ANY of %d live panes — the parser no "+
			"longer matches what this machine draws, and every nudge is being "+
			"skipped as 'cannot tell'", panes)
	}
}

// The draft has to come back out for the nudge to put it back, so the exported
// reader must agree with the parser on every rendering — including the one that
// disabled every nudge on the fleet for ten hours.
func TestComposerDraftIsReadableForRestoration(t *testing.T) {
	for _, r := range composerRenderings {
		t.Run(r.name, func(t *testing.T) {
			empty, ok := ComposerDraft(r.empty)
			if !ok {
				t.Fatal("no input row found in the empty pane; nothing could be nudged here")
			}
			if empty != "" {
				t.Errorf("empty composer yielded draft %q", empty)
			}

			busy, ok := ComposerDraft(r.busy)
			if !ok {
				t.Fatal("no input row found in the typed-in pane")
			}
			if busy == "" {
				t.Fatal("a pane with text yielded no draft — restoring it would " +
					"put back nothing, which is the data loss the guard existed to prevent")
			}
			if busy == empty {
				t.Error("empty and typed-in produced the same draft")
			}
		})
	}
}

// A pane whose input row cannot be read must yield nothing AND say so, because
// the caller refuses to type there — that is still the cannot-tell case.
func TestComposerDraftReportsWhenItCannotSee(t *testing.T) {
	for _, pane := range []string{"", "just output\nand more\n", "  ❯ 1. Resume\n  2. No\n"} {
		if d, ok := ComposerDraft(pane); ok {
			t.Errorf("claimed an input row in %q, draft %q", pane, d)
		}
	}
}
