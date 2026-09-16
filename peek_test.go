package main

import "testing"

// Absent from the presence view is NOT an empty inbox.
//
// Reported from the field: `inbox --peek --session <id>` said nothing for a
// stopped session while `mail --session <id>` printed three waiting messages
// for the same id. The cause is that the presence view enumerates sessions the
// agents are currently REPORTING — a stopped session is not in it at all — and
// the lookup returned only a count, so "I never found it" and "I found it
// holding nothing" arrived as the same zero.
//
// The axis varied is whether the session appears in the list; the queue behind
// it is held constant and non-empty in both arms, which is what makes the two
// zeroes distinguishable at all.
func TestPeekTellsAbsentFromEmpty(t *testing.T) {
	peers := []peerRow{
		{SessionID: "claude-homelab-wsl-4b602ded", Pending: 3},
		{SessionID: "claude-wsl-1df88a1a", Pending: 0},
	}

	// Absent: the session is stopped, so it is not reported at all.
	if n, _, found := findPeer(peers, "claude-adept-package-manager-wsl-b260e718"); found {
		t.Errorf("a session absent from the presence view was reported as found (n=%d)", n)
	}

	// Present and genuinely empty — the control. Without it, a lookup that
	// always answered "not found" would satisfy the assertion above while
	// making every real peek useless.
	n, _, found := findPeer(peers, "claude-wsl-1df88a1a")
	if !found {
		t.Error("a session that IS in the view must be found")
	}
	if n != 0 {
		t.Errorf("pending = %d, want 0", n)
	}

	// And a present session with mail still reports its count.
	if n, _, found := findPeer(peers, "claude-homelab-wsl-4b602ded"); !found || n != 3 {
		t.Errorf("found=%v pending=%d, want true/3", found, n)
	}
}

// An empty presence view is the coordinator being unreachable or having nothing
// reported yet — never a statement that this session's inbox is empty.
func TestPeekOnAnEmptyViewIsNotFound(t *testing.T) {
	if _, _, found := findPeer(nil, "claude-anything-wsl-00000000"); found {
		t.Error("an empty presence view must not report a session as found")
	}
}
