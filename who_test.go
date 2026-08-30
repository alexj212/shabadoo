package main

import "testing"

// The DIFFERENCE, never the list.
//
// Each node reports a dozen shared capabilities — git, docker, java — and
// printing them all buries the one line that answers the question actually
// being asked: the same project runs on two hosts, which one do I want. That
// answer is what one has and the other does not.
//
// Pinned as a pair, because either half passes alone: returning everything
// satisfies "the distinguishing capability appears", and returning nothing
// satisfies "shared capabilities are absent".
func TestDistinctCapsIsTheDifferenceNotTheList(t *testing.T) {
	nodes := []cliNode{
		{Node: "mac", CapsKnown: true, Capabilities: []string{"git", "docker", "apple-toolchain", "swift"}},
		{Node: "wsl", CapsKnown: true, Capabilities: []string{"git", "docker", "erlang"}},
	}
	mac := distinctCaps(nodes[0], nodes)
	wsl := distinctCaps(nodes[1], nodes)

	if len(mac) != 2 || mac[0] != "apple-toolchain" || mac[1] != "swift" {
		t.Fatalf("mac's distinguishing capabilities: got %v", mac)
	}
	if len(wsl) != 1 || wsl[0] != "erlang" {
		t.Fatalf("wsl's distinguishing capabilities: got %v", wsl)
	}
	for _, shared := range []string{"git", "docker"} {
		for _, c := range append(append([]string{}, mac...), wsl...) {
			if c == shared {
				t.Fatalf("%q is on both hosts and must not distinguish either", shared)
			}
		}
	}

	// A node that could not establish its capabilities contributes NOTHING
	// rather than an empty set — "reports none" and "could not look" must not
	// read alike, and this is exactly where somebody is choosing between hosts.
	unknown := []cliNode{
		{Node: "a", CapsKnown: false, Capabilities: []string{"git"}},
		{Node: "b", CapsKnown: true, Capabilities: []string{"git", "erlang"}},
	}
	if got := distinctCaps(unknown[0], unknown); got != nil {
		t.Fatalf("an unestablished node must contribute nothing, got %v", got)
	}
	// And a node that DOES know still cannot be compared against one that does
	// not: treating the unknown peer's capabilities as absent would report
	// `git` — which both hosts have — as distinguishing b. The whole comparison
	// is abandoned rather than answered wrongly, because "which host should I
	// pick" deserves no answer over a bad one.
	if got := distinctCaps(unknown[1], unknown); got != nil {
		t.Fatalf("a difference against an unknown peer is not a difference: got %v", got)
	}
}
