package tmux

import "testing"

// Tails captured on wsl 2026-10-23 (2.1.x, U+0020 build): one pane mid-turn
// with a tool running, one finished. Project names replaced.
const (
	paneMidTurn = "  Running 1 shell command…\n" +
		"  ⎿  $ tmux capture-pane -p -t claude:x | tail -12\n" +
		"\n" +
		"✢ Transfiguring… (4m 26s · ↓ 15.0k tokens · thought for 5s)\n" +
		"  ⎿  Tip: Just want the answer? Run /focus now to hide the steps along the way\n" +
		"\n" +
		"────────────────────────────────────────\n" +
		"❯ \n" +
		"────────────────────────────────────────\n" +
		"   proj  ⎇ main ●  ✱ Opus 5.5\n" +
		"  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← 1 agent\n"

	paneTurnDone = "    The idle-detection and Mac-timeout problems are still uninvestigated.\n" +
		"\n" +
		"✻ Churned for 14s · done 4:01 AM\n" +
		"                                        new task? /clear to save 102.8k tokens\n" +
		"────────────────────────────────────────\n" +
		"❯ \n" +
		"────────────────────────────────────────\n" +
		"   proj  ⎇ main ●  ✱ Opus 5.5\n" +
		"  ⏵⏵ bypass permissions on (shift+tab to cycle)\n"
)

// The axis is the spinner's tense. Both panes have the same empty composer,
// which is exactly why a composer check could not tell them apart.
func TestTurnRunningSeesTheSpinnerAndNotTheFinishedLine(t *testing.T) {
	if !TurnRunning(paneMidTurn) {
		t.Error("a pane mid-turn read as idle")
	}
	if TurnRunning(paneTurnDone) {
		t.Error("a finished turn read as running")
	}
	// "Running 1 shell command…" alone has no "(<digit>", so it is not what
	// tipped the first case.
	if TurnRunning("  Running 1 shell command…\n❯ \n") {
		t.Error("a tool line without the spinner read as running")
	}
}

// Captured on the mac 2026-10-23 (task 281d6826): the spinner glyph cycles,
// and one frame is U+00B7, a middle dot.
func TestDarwinSpinnerFrameWithAMiddleDot(t *testing.T) {
	if !TurnRunning("· Onioning… (21s · ↓ 1.1k tokens)\n────\n❯ \n────\n") {
		t.Error("darwin spinner frame read as idle")
	}
}
