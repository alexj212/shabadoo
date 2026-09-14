package hub

import (
	"os"
	"path/filepath"
	"testing"
)

func holdAt(t *testing.T, body string) *holdState {
	t.Helper()
	dir := t.TempDir()
	if body != "" {
		if err := os.WriteFile(filepath.Join(dir, holdFile), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return newHoldState(dir)
}

// A hold must never hold the sessions used to lift it.
//
// This is the assertion whose failure is a lockout, and it is asserted against a
// hold that covers EVERYTHING — an exemption that only works when the hold is
// narrow is not an exemption. The control is the point: without an ordinary
// session being held in the identical state, every assertion here passes for a
// hold that holds nothing at all.
func TestHoldNeverHoldsWhatWouldLiftIt(t *testing.T) {
	h := holdAt(t, "all\n")

	t.Run("control: an ordinary session IS held", func(t *testing.T) {
		if !h.holds("claude-iptv-1", "iptv-wsl", "iptv", KindClaude) {
			t.Fatal("an ordinary session under `all` must be held, or nothing " +
				"below proves anything")
		}
	})
	t.Run("a core session is never held", func(t *testing.T) {
		if h.holds("claude-wsl-1", "wsl", "wsl", KindCore) {
			t.Error("core session held — nobody could route or lift the hold")
		}
	})
	t.Run("this project is never held", func(t *testing.T) {
		if h.holds("claude-shabadoo-1", "shabadoo-wsl", "shabadoo", KindClaude) {
			t.Error("the project owning the switch was held by it")
		}
	})
}

// Naming one session holds that session and nothing else.
func TestHoldMatchesByAliasProjectOrID(t *testing.T) {
	h := holdAt(t, "patching\n")
	for _, tc := range []struct {
		name            string
		id, alias, proj string
		want            bool
	}{
		{"by project", "claude-patching-1", "patching-wsl", "patching", true},
		{"by alias", "claude-x-1", "patching", "somethingelse", true},
		{"an unrelated session", "claude-iptv-1", "iptv-wsl", "iptv", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := h.holds(tc.id, tc.alias, tc.proj, KindClaude); got != tc.want {
				t.Errorf("holds = %v, want %v", got, tc.want)
			}
		})
	}
}

// FAILS OPEN. A hold stuck ON is a fleet that stops with nothing saying why; a
// hold stuck OFF is the day that prompted the feature — visible and
// recoverable.
func TestHoldFailsOpen(t *testing.T) {
	t.Run("no file means no hold", func(t *testing.T) {
		if holdAt(t, "").holds("claude-iptv-1", "iptv-wsl", "iptv", KindClaude) {
			t.Error("held with no hold file present")
		}
	})
	t.Run("a nil hold holds nothing", func(t *testing.T) {
		var h *holdState
		if h.holds("claude-iptv-1", "iptv-wsl", "iptv", KindClaude) {
			t.Error("an unconfigured hold held a session")
		}
	})
	t.Run("comments and blanks are ignored, not matched", func(t *testing.T) {
		h := holdAt(t, "# all\n\n")
		if h.holds("claude-iptv-1", "iptv-wsl", "iptv", KindClaude) {
			t.Error("a commented-out `all` still held the fleet")
		}
	})
}

// set replaces, and an empty list removes the file rather than leaving an empty
// one somebody has to interpret.
func TestHoldSetAndRelease(t *testing.T) {
	h := holdAt(t, "")
	if err := h.set([]string{"all"}); err != nil {
		t.Fatal(err)
	}
	if !h.holds("claude-iptv-1", "iptv-wsl", "iptv", KindClaude) {
		t.Fatal("set([all]) did not take effect")
	}
	if err := h.set(nil); err != nil {
		t.Fatal(err)
	}
	if h.holds("claude-iptv-1", "iptv-wsl", "iptv", KindClaude) {
		t.Error("release did not lift the hold")
	}
	if _, err := os.Stat(filepath.Join(h.dir, holdFile)); !os.IsNotExist(err) {
		t.Error("release left a file behind: released and held-by-nobody must be " +
			"the same state on disk")
	}
}
