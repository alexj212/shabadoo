package hub

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func capAt(t *testing.T, limit int, now *time.Time) *wakeCap {
	t.Helper()
	return newWakeCap(limit, t.TempDir(), func() time.Time { return *now })
}

// fill puts n distinct sessions into the working set, which is what makes the
// cap bind. Named because "the input that makes it queue" has to be nameable.
func fill(c *wakeCap, n int, now time.Time) {
	c.mu.Lock()
	for i := 0; i < n; i++ {
		c.working[string(rune('a'+i))+"-busy"] = now
	}
	c.mu.Unlock()
}

// BOTH DIRECTIONS. A cap that has never queued anything is untested, and one
// that queues everything passes a one-sided example just as happily — so the
// two inputs are named and both are run.
func TestCapHoldsAtTheLimitAndPassesBelowIt(t *testing.T) {
	now := time.Now()

	t.Run("below the limit it passes", func(t *testing.T) {
		c := capAt(t, 3, &now)
		fill(c, 2, now) // two working, limit three
		ok, behind := c.allow("claude-iptv", "iptv", "claude", wakeAgentSend)
		if !ok {
			t.Fatalf("two working under a limit of three must pass, held behind %d", behind)
		}
		if _, held := c.queuedSince("claude-iptv"); held {
			t.Error("a session that passed must not be recorded as queued")
		}
	})

	t.Run("at the limit it holds", func(t *testing.T) {
		c := capAt(t, 3, &now)
		fill(c, 3, now) // three working, limit three
		ok, behind := c.allow("claude-iptv", "iptv", "claude", wakeAgentSend)
		if ok {
			t.Fatal("three working at a limit of three must hold")
		}
		if behind != 3 {
			t.Errorf("held session should be told what it is behind, got %d", behind)
		}
		if _, held := c.queuedSince("claude-iptv"); !held {
			t.Error("a held session must be QUEUED — distinguishable from idle and from stuck")
		}
	})

	t.Run("a slot freeing releases it", func(t *testing.T) {
		c := capAt(t, 3, &now)
		fill(c, 3, now)
		if ok, _ := c.allow("claude-iptv", "iptv", "claude", wakeAgentSend); ok {
			t.Fatal("precondition: should be held")
		}
		now = now.Add(capWorkWindow + time.Second) // the three fall out of the window
		if ok, _ := c.allow("claude-iptv", "iptv", "claude", wakeAgentSend); !ok {
			t.Fatal("once the working set ages out the hold must release")
		}
		if _, held := c.queuedSince("claude-iptv"); held {
			t.Error("a released session must stop reading as queued")
		}
	})
}

// THE ASSERTION WHOSE FAILURE IS A LOCKOUT.
//
// A core session is the addressable "you" of its machine and the only thing
// permitted to start sessions there. If the cap holds one, nobody can route,
// unstick or reach that host — and it happens when the cap is binding, which is
// the worst moment. Every exemption is asserted against a FULL cap, because an
// exemption that only works when the cap is not binding is not an exemption.
func TestCapNeverHoldsTheSessionsNeededToFixIt(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name    string
		session string
		project string
		kind    string
		why     wakeReason
	}{
		{"a core session, or nobody can reach that host", "claude-wsl", "wsl", "core", wakeAgentSend},
		{"the other host's core session", "claude-mac", "mac", "core", wakeStuckRetry},
		{"the project that owns the cap", "claude-shabadoo-wsl", "shabadoo", "claude", wakeAgentSend},
		{"a human's own send", "claude-iptv", "iptv", "claude", wakeHumanSend},
		{"a task-end notice, which reports rather than asks", "claude-iptv", "iptv", "claude", wakeTaskEnd},
		{"a caller that did not say why", "claude-iptv", "iptv", "claude", wakeUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := capAt(t, 3, &now)
			fill(c, 99, now) // maximally over the limit
			ok, _ := c.allow(tc.session, tc.project, tc.kind, tc.why)
			if !ok {
				t.Fatalf("%s must never be held by the cap", tc.name)
			}
			if _, held := c.queuedSince(tc.session); held {
				t.Errorf("%s must not be recorded as queued either", tc.name)
			}
		})
	}

	// And the control: with the exemptions removed the same call IS held.
	// Without this the test above passes for a cap that holds nothing at all.
	t.Run("control: an ordinary session in the same state is held", func(t *testing.T) {
		c := capAt(t, 3, &now)
		fill(c, 99, now)
		if ok, _ := c.allow("claude-iptv", "iptv", "claude", wakeAgentSend); ok {
			t.Fatal("an ordinary agent-send at a full cap must be held, or the " +
				"exemption assertions above prove nothing")
		}
	})
}

// The kill switch must work from a plain shell with no session and no
// credential, because a broken cap is exactly when those do not work.
func TestKillSwitchFileDisablesTheCap(t *testing.T) {
	now := time.Now()
	dir := t.TempDir()
	c := newWakeCap(3, dir, func() time.Time { return now })
	fill(c, 99, now)

	if ok, _ := c.allow("claude-iptv", "iptv", "claude", wakeAgentSend); ok {
		t.Fatal("precondition: a full cap must hold before the switch is touched")
	}
	if err := os.WriteFile(filepath.Join(dir, capOffFile), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, _ := c.allow("claude-iptv", "iptv", "claude", wakeAgentSend); !ok {
		t.Fatal("touching cap.off must disable the cap with no restart")
	}
}

// FAIL OPEN. Every uncertain path lets the wake through: failing closed on a bug
// holds the sessions that would fix it, which needs a human on the box.
func TestCapFailsOpen(t *testing.T) {
	now := time.Now()
	t.Run("a zero limit disables it", func(t *testing.T) {
		c := capAt(t, 0, &now)
		fill(c, 99, now)
		if ok, _ := c.allow("claude-iptv", "iptv", "claude", wakeAgentSend); !ok {
			t.Fatal("limit 0 means no cap")
		}
	})
	t.Run("a nil cap passes everything", func(t *testing.T) {
		var c *wakeCap
		if ok, _ := c.allow("claude-iptv", "iptv", "claude", wakeAgentSend); !ok {
			t.Fatal("an unconfigured cap must not hold anything")
		}
		if _, held := c.queuedSince("claude-iptv"); held {
			t.Fatal("an unconfigured cap holds nothing, so nothing is queued")
		}
	})
}

// The activity signal: token counters GROWING between reports, not tmux's
// selected-window flag. Pinned as a pair — a session that grew must count and a
// session that did not must not, or a detector that answers "working" to
// everything passes the first assertion alone.
func TestObserveCountsGrowthNotPresence(t *testing.T) {
	now := time.Now()
	c := capAt(t, 3, &now)

	first := []Session{
		{SessionID: "a", TokensCache: 1000},
		{SessionID: "b", TokensCache: 5000},
	}
	c.observe(first) // first sight establishes a baseline; nobody is "working" yet
	if got := c.stats()["concurrent"].(int); got != 0 {
		t.Fatalf("a first report is a baseline, not activity: concurrent=%d", got)
	}

	c.observe([]Session{
		{SessionID: "a", TokensCache: 9000}, // grew: working
		{SessionID: "b", TokensCache: 5000}, // unchanged: not working
	})
	if got := c.stats()["concurrent"].(int); got != 1 {
		t.Fatalf("only the session that burned tokens counts: concurrent=%d", got)
	}
	if got := c.stats()["high_water"].(int); got != 1 {
		t.Fatalf("high water is the measurement that stops the limit being a guess: %d", got)
	}
}
