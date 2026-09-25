package tmux

import (
	"strings"
	"testing"
)

// The Enter that submits a command must not arrive in the same read() as the
// text, or a TUI mid-render treats the newline as part of a paste and the line
// sits in the composer looking typed and never sent. awaitComposer waits for
// the text to appear first; composerHolds is the predicate it waits on.
//
// Asserted as a PAIR, over every rendering the composer is known to take.
//
// A single-sided fixture — "a pane holding the text settles" — passes just as
// happily when composerHolds has gone blind and answers true for everything,
// which is the failure that matters here: it would restore the original defect
// silently, because the send still succeeds and nothing downstream can tell a
// submitted line from an inserted one. So the empty pane must NOT settle for
// the same probe. The axis varied is whether the text is present; the pane's
// rendering, platform and separator byte are held constant by reusing the
// captured fixtures rather than writing new ones.
func TestComposerHoldsDistinguishesTypedFromEmpty(t *testing.T) {
	for _, r := range composerRenderings {
		t.Run(r.name, func(t *testing.T) {
			draft, ok := ComposerDraft(r.busy)
			if !ok {
				t.Fatalf("busy fixture has no readable input row; composer_test covers why")
			}
			probe := settleProbe(draft)
			if probe == "" {
				t.Fatalf("busy fixture yielded an empty probe from draft %q", draft)
			}
			if !composerHolds(r.busy, probe) {
				t.Errorf("typed pane did not settle for probe %q (draft %q)", probe, draft)
			}
			if composerHolds(r.empty, probe) {
				t.Errorf("EMPTY pane settled for probe %q — the predicate cannot tell "+
					"typed from untyped, so the wait would return immediately and the "+
					"submit race is back", probe)
			}
		})
	}
}

// A long command wraps, so only its head is on the input row. Matching the
// whole thing would never settle and would pay the full timeout on every send —
// which is the fail-open path, i.e. the defect, reached by being too strict.
func TestSettleProbeTruncatesSoAWrappedLineStillSettles(t *testing.T) {
	const cmd = "commit the doc change and then push it to origin main"
	probe := settleProbe(cmd)
	if n := len([]rune(probe)); n != settleProbeRunes {
		t.Fatalf("probe is %d runes, want %d: %q", n, settleProbeRunes, probe)
	}
	if !strings.HasPrefix(cmd, probe) {
		t.Fatalf("probe %q is not a prefix of %q", probe, cmd)
	}

	// The input row shows only what fitted. Reuse the captured linux rendering's
	// own bytes — including the U+00A0 separator — so this does not quietly
	// become a hand-written fixture.
	wrapped := "❯ " + probe + "\n"
	if !composerHolds(wrapped, probe) {
		t.Errorf("a pane showing only the wrapped head %q did not settle", probe)
	}

	// Control: the same row holding a DIFFERENT command must not settle, or the
	// truncation has made the probe match anything short.
	other := "❯ push the other branch\n"
	if composerHolds(other, probe) {
		t.Errorf("an unrelated command settled for probe %q", probe)
	}
}

// An unreadable input row reports "not holding", so the caller keeps looking
// until its deadline rather than treating cannot-tell as done. The fail-open
// decision lives in awaitComposer's timeout, never in this predicate — if it
// leaked to here, every pane this program cannot parse would submit instantly
// and the fix would be inert on exactly the panes it was written for (darwin
// parsed to "cannot tell" for a day; that is not hypothetical).
func TestComposerHoldsTreatsAnUnreadableRowAsNotSettled(t *testing.T) {
	for _, pane := range []string{
		"",
		"some tool output\nmore output\n",
		"  > Continue\n  Disconnect this session\n", // an indented menu, not a composer
	} {
		if composerHolds(pane, "check inbox") {
			t.Errorf("pane with no input row settled: %q", pane)
		}
	}
}
