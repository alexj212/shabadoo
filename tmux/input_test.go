package tmux

import "testing"

// InputState reads another program's UI, so these cases are captured from real
// Claude Code panes. The asymmetry matters: missing a dialog only restores the
// old silent-swallow behaviour, but a false dialog would block real messages.
func TestInputState(t *testing.T) {
	cases := []struct {
		name string
		pane string
		want string
	}{
		{
			name: "idle composer",
			pane: "● Hello — receiving you loud and clear.\n" +
				"────────────────────\n❯ \n────────────────────\n" +
				"  entertest  ✱ Opus 5  § 0 tokens",
			want: InputComposer,
		},
		{
			name: "trust dialog",
			pane: " Quick safety check: Is this a project you created or one you trust?\n" +
				" ❯ 1. Yes, I trust this folder\n   2. No, exit\n" +
				" Enter to confirm · Esc to cancel",
			want: InputDialog,
		},
		{
			name: "status modal",
			pane: "   Settings  Status   Config   Usage   Stats\n" +
				"   Version:          2.1.220\n   Esc to cancel",
			want: InputDialog,
		},
		{
			name: "permission prompt",
			pane: "Bash(rm -rf /tmp/x)\nDo you want to proceed?\n ❯ 1. Yes\n   2. No",
			want: InputDialog,
		},
		{
			name: "busy is not a dialog — queued text still lands",
			pane: "● Working on it…\n✻ Cogitating (esc to interrupt)\n❯ ",
			want: InputComposer,
		},
		{
			name: "dismissed dialog scrolled out of the visible tail",
			pane: "Enter to confirm · Esc to cancel\n" + longTail(25) + "❯ ",
			want: InputComposer,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := InputState(tc.pane); got != tc.want {
				t.Errorf("InputState() = %q, want %q", got, tc.want)
			}
		})
	}
}

func longTail(n int) string {
	var s string
	for range n {
		s += "output line\n"
	}
	return s
}

// The `/remote-control` menu is a SELECT LIST, and its footer shares nothing
// with the confirm-style prompts the original markers were written from
// ("Enter to select · Esc to continue" against "Enter to confirm" / "Esc to
// cancel"). So it classified as `composer`: a modal owned the keyboard while
// the guard that exists to catch precisely that waved sends straight into it.
//
// Transcribed from a screenshot of the real dialog — this class of bug does
// not surface any other way. The other cases are here so a widened marker set
// cannot pass by matching everything.
func TestInputStateDetectsSelectListModals(t *testing.T) {
	remoteControl := `
  Remote Control

  This session is available in the Claude mobile app and at https://claude.ai/code/session_01ABC.

    Disconnect this session
    Show QR code   Scan with your phone to open this session
  > Continue

  Enter to select · Esc to continue
`
	for _, tc := range []struct{ name, pane, want string }{
		{"remote-control menu", remoteControl, InputDialog},
		{"confirm-style prompt", "│ Do you want to proceed? │\n  Esc to cancel", InputDialog},
		{"plain composer", "> hello\n╭───╮\n│ > │\n╰───╯", InputComposer},
		// Prose ABOUT a dialog is not a dialog. These markers are footer text,
		// so a pane merely discussing them must stay a composer — otherwise the
		// guard refuses real messages and the watcher notifies about nothing.
		{"transcript discussing it", "I added \"Enter to select\" to the marker list.\n" +
			"╭──────────╮\n│ > ..... │\n╰──────────╯", InputComposer},
	} {
		if got := InputState(tc.pane); got != tc.want {
			t.Errorf("%s: InputState = %q, want %q", tc.name, got, tc.want)
		}
	}
}
