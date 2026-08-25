package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"shabadoo/hub"
)

// keeper builds a coreKeeper whose clock and launcher are under the test's
// control. Nothing here may start a real session: this repository has twice
// been bitten by a test that resolved to a live target.
func keeper(t *testing.T) (*coreKeeper, *int, *time.Time) {
	t.Helper()
	launches := 0
	now := time.Unix(1_700_000_000, 0)
	k := &coreKeeper{
		path:   "/state/wsl",
		now:    func() time.Time { return now },
		exists: func(string) bool { return true },
		launch: func(context.Context, string) error { launches++; return nil },
	}
	return k, &launches, &now
}

func coreSession() hub.Session {
	return hub.Session{SessionID: "claude-wsl-1", Kind: hub.KindCore, CWD: "/state/wsl"}
}
func otherSession() hub.Session {
	return hub.Session{SessionID: "claude-iptv-1", Kind: hub.KindClaude, CWD: "/w/iptv"}
}

func TestCoreKeeperStartsItWhenAbsent(t *testing.T) {
	k, launches, now := keeper(t)
	ctx := context.Background()

	// Running: nothing to do, on every report, forever.
	for i := 0; i < 5; i++ {
		k.observe(ctx, []hub.Session{coreSession(), otherSession()})
	}
	if *launches != 0 {
		t.Fatalf("started %d sessions while the core session was running", *launches)
	}

	// Gone. Started once, immediately — this is the case where a node is
	// otherwise unable to start anything at all.
	k.observe(ctx, []hub.Session{otherSession()})
	if *launches != 1 {
		t.Fatalf("launches = %d, want 1", *launches)
	}

	// Still gone on the reports that land INSIDE the backoff window. It must not
	// try again on each of them: a core session that fails instantly would
	// otherwise be relaunched every few seconds forever, and the supervisor
	// becomes the outage.
	for elapsed := time.Duration(0); elapsed+4*time.Second < coreBackoffMin; elapsed += 4 * time.Second {
		*now = now.Add(4 * time.Second)
		k.observe(ctx, []hub.Session{otherSession()})
	}
	if *launches != 1 {
		t.Errorf("launches = %d during backoff, want 1", *launches)
	}

	// Past the backoff, it tries again.
	*now = now.Add(coreBackoffMin)
	k.observe(ctx, []hub.Session{otherSession()})
	if *launches != 2 {
		t.Errorf("launches = %d after the backoff elapsed, want 2", *launches)
	}
}

// A node with no core project has not been given one — every install predating
// this is in that state. Retrying into a directory that does not exist would
// back off forever while logging a failure each time: noise about a decision
// nobody made.
func TestCoreKeeperIgnoresANodeWithNoProject(t *testing.T) {
	k, launches, now := keeper(t)
	k.exists = func(string) bool { return false }

	for i := 0; i < 10; i++ {
		*now = now.Add(time.Hour)
		k.observe(context.Background(), []hub.Session{otherSession()})
	}
	if *launches != 0 {
		t.Errorf("launched %d times into a directory that does not exist", *launches)
	}

	// And an unset path disables it entirely, rather than launching in "".
	k2, l2, _ := keeper(t)
	k2.path = ""
	k2.observe(context.Background(), nil)
	if *l2 != 0 {
		t.Errorf("launched with no core path configured")
	}
}

// Recovery must actually recover: once the session is back, the accrued backoff
// is forgotten, so the NEXT failure is handled promptly rather than inheriting
// a ten-minute delay from an unrelated earlier problem.
func TestCoreKeeperResetsAfterRecovery(t *testing.T) {
	k, launches, now := keeper(t)
	ctx := context.Background()

	// Fail enough times to climb the backoff.
	for i := 0; i < 4; i++ {
		k.observe(ctx, []hub.Session{otherSession()})
		*now = now.Add(coreBackoffMax)
	}
	climbed := *launches
	if climbed < 4 {
		t.Fatalf("expected repeated attempts, got %d", climbed)
	}

	k.observe(ctx, []hub.Session{coreSession()}) // back up

	// Down again: attended to at once, not after the old delay.
	k.observe(ctx, []hub.Session{otherSession()})
	if *launches != climbed+1 {
		t.Errorf("a fresh failure was delayed by stale backoff: launches = %d, want %d",
			*launches, climbed+1)
	}
}

// A launch that errors still counts as an attempt. Otherwise a launcher that
// fails synchronously — the most likely kind of failure — would retry with no
// delay at all, which is the loop backoff exists to prevent.
func TestCoreKeeperBacksOffWhenLaunchingFails(t *testing.T) {
	k, _, now := keeper(t)
	tries := 0
	k.launch = func(context.Context, string) error { tries++; return errors.New("no such binary") }
	ctx := context.Background()

	k.observe(ctx, []hub.Session{otherSession()})
	for i := 0; i < 3; i++ {
		*now = now.Add(time.Second)
		k.observe(ctx, []hub.Session{otherSession()})
	}
	if tries != 1 {
		t.Errorf("a failing launch was retried %d times without backing off", tries)
	}
}

func TestCoreBackoffGrowsAndIsCapped(t *testing.T) {
	if got := coreBackoff(1); got != coreBackoffMin {
		t.Errorf("first backoff = %s, want %s", got, coreBackoffMin)
	}
	if coreBackoff(3) <= coreBackoff(2) {
		t.Error("backoff does not grow")
	}
	if got := coreBackoff(50); got != coreBackoffMax {
		t.Errorf("backoff = %s at attempt 50, want it capped at %s", got, coreBackoffMax)
	}
}
