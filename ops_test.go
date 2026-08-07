package main

import "testing"

// The dismissal must fire for this modal and refuse to fire for anything else.
// It presses a key on somebody's live pane, so the interesting cases are all
// the ones where it must stay its hand.
func TestRemoteControlDialogRecognition(t *testing.T) {
	real := `
  Remote Control

  This session is available in the Claude mobile app and at https://claude.ai/code/session_01ABC.

    Disconnect this session
    Show QR code   Scan with your phone to open this session
  > Continue

  Enter to select · Esc to continue
`
	for _, tc := range []struct {
		name string
		pane string
		want bool
	}{
		{"the real menu", real, true},

		// A permission prompt is a modal, but not THIS modal. Pressing Escape
		// on one discards an in-flight turn, which is the operator's call.
		{"some other modal", "│ Do you want to proceed? │\n  Esc to cancel", false},

		// The title alone is not enough. This repository discusses the feature
		// constantly, and matching on prose would press Escape into a pane that
		// is only talking about it.
		{"prose naming it", "Remote Control drops periodically.\n╭───╮\n│ > │\n╰───╯", false},

		// Half the markers is not the menu either.
		{"title in a modal, no menu", "│ Remote Control │\n  Esc to cancel", false},
	} {
		if got := isRemoteControlDialog(tc.pane); got != tc.want {
			t.Errorf("%s: isRemoteControlDialog = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestIsRemoteControl(t *testing.T) {
	for _, ok := range []string{"/remote-control", "  /remote-control  ", "/Remote-Control"} {
		if !isRemoteControl(ok) {
			t.Errorf("%q should be recognised", ok)
		}
	}
	// Narrow on purpose: this is the only command that gets a key pressed on
	// its behalf, so anything else must fall through to today's behaviour.
	for _, no := range []string{"/clear", "/remote-control off", "remote-control", ""} {
		if isRemoteControl(no) {
			t.Errorf("%q should NOT trigger the dismissal", no)
		}
	}
}
