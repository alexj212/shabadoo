package main

import (
	"strings"
	"testing"
)

// Panes taken from the captured fixtures in tmux/composer_test.go and
// tmux/input_test.go rather than written here.
//
// That matters for one byte in particular: the separator after ❯ is U+00A0, a
// NON-BREAKING space. A parser expecting a plain space finds no input row at
// all, and `ComposerBusy` then answers "busy" for every pane on the fleet — the
// defect that skipped every nudge for ten hours. A hand-written fixture would
// contain U+0020, agree with whatever I assumed, and pass while the real thing
// behaved differently.
const (
	paneEmptyComposer = "  ⏵⏵ bypass permissions on (shift+tab to cycle)\n" +
		"❯ \n" +
		"────── homelab-wsl ─\n"

	paneBusyComposer = "  ⏵⏵ bypass permissions on (shift+tab to cycle)\n" +
		"❯ commit the doc change\n" +
		"────── homelab-wsl ─\n"

	panePermissionDialog = "Bash(rm -rf /tmp/x)\nDo you want to proceed?\n ❯ 1. Yes\n   2. No"

	paneTrustDialog = " Quick safety check: Is this a project you created or one you trust?\n" +
		" ❯ 1. Yes, I trust this folder\n   2. No, exit\n" +
		" Enter to confirm · Esc to cancel"
)

// The axis is whether the composer holds unsent text; the fixture is otherwise
// identical, byte for byte, between the two arms.
//
// The pair is required rather than tidy. A guard that refused everything would
// satisfy "a draft is protected" perfectly while making restart useless, and a
// guard that allowed everything satisfies nothing — only running both arms over
// the same pane distinguishes them.
func TestRestartRefusesAHalfTypedPromptAndAllowsAnEmptyOne(t *testing.T) {
	ok, reason := restartDecision(paneBusyComposer)
	if ok {
		t.Error("restart allowed on a pane with unsent text: the draft would be destroyed")
	}
	if !strings.Contains(reason, "composer") {
		t.Errorf("reason does not say what was at risk: %q", reason)
	}

	// The control, on the same rendering with an empty input row.
	if ok, reason := restartDecision(paneEmptyComposer); !ok {
		t.Errorf("an idle pane must be restartable, got refusal: %q", reason)
	}
}

// A dialog is a question waiting on a human. Restarting does not answer it, it
// discards it — the session returns never having been asked, and whoever was
// about to answer has nothing to answer.
func TestRestartRefusesAPaneAtAPrompt(t *testing.T) {
	for _, pane := range []string{panePermissionDialog, paneTrustDialog} {
		ok, reason := restartDecision(pane)
		if ok {
			t.Errorf("restart allowed on a pane at a prompt — the question would be discarded")
		}
		if !strings.Contains(reason, "prompt") {
			t.Errorf("reason does not name the prompt: %q", reason)
		}
	}
}

// A refusal and an unreadable answer are DIFFERENT, and collapsing them is the
// defect that shipped.
//
// The coordinator's generic write proxy discards the agent's result and answers
// 204, so every restart came back with an empty body. Decoding that into a
// plain bool yielded `restarted:false`, which the CLI printed as "skipped" —
// seconds after the session had in fact restarted. Measured at the time: the
// restarted pane's process was 20 seconds old against its neighbour's 119,482.
//
// The axis is what the coordinator said; the fixture is the same call in every
// arm. The pair matters because a decoder that answered "unknown" to everything
// would satisfy the empty-body case perfectly while destroying the feature.
func TestAnUnreadableAnswerIsUnknownNotSkipped(t *testing.T) {
	for _, body := range []string{"", "   ", "<!DOCTYPE html>", `{"ok":true}`} {
		got, reason, _ := decodeRestart([]byte(body))
		if got != restartUnknown {
			t.Errorf("decodeRestart(%q) = %q, want %q — an answer nobody could read "+
				"must not be reported as a refusal", body, got, restartUnknown)
		}
		if reason == "" {
			t.Errorf("decodeRestart(%q) gave no reason to act on", body)
		}
	}

	// Both controls, on the same decoder: a real refusal still reads as one,
	// and a real success still reads as one.
	if got, reason, _ := decodeRestart([]byte(`{"restarted":false,"reason":"waiting on a prompt"}`)); got != restartSkipped {
		t.Errorf("an explicit refusal decoded as %q (%s), want %q", got, reason, restartSkipped)
	} else if reason != "waiting on a prompt" {
		t.Errorf("the refusal lost its reason: %q", reason)
	}
	if got, _, _ := decodeRestart([]byte(`{"restarted":true,"name":"x"}`)); got != restartDone {
		t.Errorf("a success decoded as %q, want %q", got, restartDone)
	}
}

// Fails CLOSED, and this is the case no fixture of a working pane covers.
//
// An unreadable pane — mid-redraw, a pager, an overlay, anything whose input row
// this parser does not recognise — must refuse. `ComposerBusy` returns busy when
// it cannot tell, and that default is load-bearing here: a false busy delays a
// restart nobody was waiting on, while a false idle destroys work that cannot be
// recovered. The costs are not symmetric and the guard is aimed accordingly.
func TestRestartRefusesWhatItCannotRead(t *testing.T) {
	for _, pane := range []string{
		"",
		"some output with no input row at all\nand another line\n",
		"─────\n",
	} {
		if ok, _ := restartDecision(pane); ok {
			t.Errorf("restart allowed on an unreadable pane %q — must fail closed", pane)
		}
	}
}
