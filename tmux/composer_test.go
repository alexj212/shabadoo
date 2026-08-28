package tmux

import "testing"

// A nudge types into somebody else's terminal without a human asking, and the
// wire for it clears the input line first. So the question "is anyone mid-way
// through a prompt here" has to be answerable from a capture alone.
//
// The fixtures are drawn from Claude Code's composer box. Unrecognised must
// answer BUSY: a skipped nudge costs promptness on mail that is already stored,
// while a wrong "idle" erases somebody's draft with no way to get it back.
func TestComposerBusy(t *testing.T) {
	cases := []struct {
		name string
		pane string
		want bool
	}{
		{"empty composer", "" +
			"some transcript output\n" +
			"╭──────────────────────────────╮\n" +
			"│ >                            │\n" +
			"╰──────────────────────────────╯\n", false},

		{"composer holding a draft", "" +
			"some transcript output\n" +
			"╭──────────────────────────────╮\n" +
			"│ > half a question about the  │\n" +
			"╰──────────────────────────────╯\n", true},

		{"draft of a single character", "" +
			"╭──────────────────────────────╮\n" +
			"│ > y                          │\n" +
			"╰──────────────────────────────╯\n", true},

		{"a dialog, not a composer", "" +
			"╭──────────────────────────────╮\n" +
			"│ Do you want to proceed?      │\n" +
			"│ ❯ 1. Yes                     │\n" +
			"│   2. No                      │\n" +
			"╰──────────────────────────────╯\n", true},

		{"nothing recognisable", "just some output\nand more of it\n", true},

		{"empty pane", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ComposerBusy(c.pane); got != c.want {
				t.Errorf("ComposerBusy = %v, want %v", got, c.want)
			}
		})
	}
}

// The composer must be found at the BOTTOM. A box scrolled up into the
// transcript is not the input row any more, and typing at it would be typing
// into whatever replaced it.
func TestComposerBusyIgnoresScrolledBox(t *testing.T) {
	pane := "" +
		"│ > an old prompt from earlier │\n" +
		"assistant replied at length\n" +
		"and kept going\n" +
		"and going\n" +
		"and going\n" +
		"and going\n"
	if !ComposerBusy(pane) {
		t.Error("a composer row scrolled out of the last few lines must read as busy")
	}
}
