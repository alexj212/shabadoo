package node

import (
	"testing"
	"time"
)

// The reconnect loop is what keeps a node from disappearing after a coordinator
// restart, and it is the one thing in this package with no other coverage. Two
// properties matter: it must be bounded (a down coordinator must not become a
// retry storm) and it must be jittered (agents that all lost the same
// coordinator must not return in lockstep).
func TestBackoff(t *testing.T) {
	for attempt := range 20 {
		d := backoff(attempt)
		if d <= 0 {
			t.Fatalf("backoff(%d) = %v, must be positive", attempt, d)
		}
		if d > backoffCap {
			t.Errorf("backoff(%d) = %v, exceeds cap %v", attempt, d, backoffCap)
		}
	}

	// Early attempts should be quick — a coordinator that blips must not cost
	// a minute of absence.
	if d := backoff(0); d > 2*time.Second {
		t.Errorf("backoff(0) = %v, want a prompt first retry", d)
	}

	// Growth: a late attempt must wait materially longer than the first.
	if backoff(10) <= backoff(0) {
		t.Errorf("backoff does not grow: backoff(10)=%v backoff(0)=%v", backoff(10), backoff(0))
	}

	// Jitter: repeated calls at the same attempt must not all be identical.
	seen := map[time.Duration]int{}
	for range 50 {
		seen[backoff(5)]++
	}
	if len(seen) == 1 {
		t.Error("backoff(5) is not jittered — every agent would retry in lockstep")
	}
}
