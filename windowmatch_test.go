package main

import (
	"strings"
	"testing"
)

// The live window list at the moment this defect was found, kept verbatim.
//
// It is a real capture rather than an invention, which matters here: the whole
// failure depends on which hex digits the hashes happen to contain, and a
// fixture composed by hand would agree with whatever the author assumed about
// them. These are the names `tmux list-windows` printed.
var liveWindows = []string{
	"wsl-1df88a1a",
	"homelife-wsl-1a170f99",
	"homelab-wsl-4b602ded",
	"devops-wsl-0bcb99a1",
	"shabadoo-wsl-1ef3aefe",
	"adept-registry-wsl-0cfcf2e0",
	"adept-daemon-wsl-01832744",
	"xen-wsl-b37ed3b0",
	"node-wsl-256b2b88",
	"observability-wsl-1c7230ff",
	"backups-wsl-cda194b9",
	"dev-env-wsl-6ac07f29",
	"runner-wsl-0d908647",
	"patching-wsl-bf0281e2",
	"investigations-wsl-016f76da",
	"youtrack-wsl-d8fc9aa1",
	"customer-factory-wsl-f8525d9c",
	"mcp-wsl-299a7b89",
}

// A number typed as if it were a window index must never resolve to a window.
//
// Reported as a near-miss by a peer: a handoff identified its own session as
// "window claude:17", the number was off by one, and window 17 was a session
// holding a maintenance window. The peer resolved by name instead and nothing
// was lost — but the danger they described was not the danger present.
//
// `win close` has no index path at all. A bare number was substring-matched
// against the whole window name INCLUDING the 8-hex hash, and against this
// list every one of these resolved UNIQUELY, so the ambiguity guard never
// fired and the kill was silent:
//
//	17 -> homelife       (window 17 is patching)
//	18 -> adept-daemon   (window 18 is investigations)
//	19 -> backups        (window 19 is youtrack)
//	16 -> investigations (window 16 does not exist)
//	23 -> observability  (window 23 does not exist)
//
// The axis this varies is whether the pattern's digits occur ONLY inside the
// hash. The control below holds that fixture constant and asks for a friendly
// substring, because a matcher that had simply stopped resolving anything
// would satisfy the first half while being far worse than the bug.
func TestABareNumberDoesNotMatchAHash(t *testing.T) {
	for _, n := range []string{"16", "17", "18", "19", "23", "0", "7"} {
		got, err := matchWindow(liveWindows, n)
		if err == nil {
			t.Errorf("matchWindow(%q) resolved to %q; a number must not address a window "+
				"— it is not an index, and matching it against a hash kills an unrelated session", n, got)
		}
	}

	// The control, on the identical fixture.
	got, err := matchWindow(liveWindows, "homelife")
	if err != nil || got != "homelife-wsl-1a170f99" {
		t.Fatalf("a friendly substring must still resolve: got %q err=%v", got, err)
	}
}

// The refusal has to say what to do instead, or the next person retries with
// another number.
func TestTheRefusalNamesTheRemedy(t *testing.T) {
	_, err := matchWindow(liveWindows, "17")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "name") {
		t.Errorf("refusal does not point at the name: %v", err)
	}
}

// An exact name always wins, before any other rule — so a project whose folder
// really is all digits stays addressable, and nothing that worked by full name
// stops working.
func TestAnExactNameWinsEvenWhenItIsAllDigits(t *testing.T) {
	names := append([]string{"17"}, liveWindows...)
	got, err := matchWindow(names, "17")
	if err != nil || got != "17" {
		t.Fatalf("an exact window name must win outright: got %q err=%v", got, err)
	}
}

// Substring matching still works on the part a person actually addresses, and
// an ambiguous one is still refused rather than guessed.
func TestFriendlySubstringResolvesAndAmbiguityIsRefused(t *testing.T) {
	got, err := matchWindow(liveWindows, "patching")
	if err != nil || got != "patching-wsl-bf0281e2" {
		t.Fatalf("patching should resolve: got %q err=%v", got, err)
	}

	if _, err := matchWindow(liveWindows, "wsl"); err == nil {
		t.Error("`wsl` matches every window and must be refused, never guessed")
	}

	if _, err := matchWindow(liveWindows, "adept"); err == nil {
		t.Error("`adept` matches two windows and must be refused")
	}
}

// A hash is an implementation detail of the naming formula, not something a
// person addresses a session by. Matching it is what turned "17" into a unique
// hit on an unrelated project.
func TestAHashFragmentNoLongerResolves(t *testing.T) {
	if got, err := matchWindow(liveWindows, "1a170f99"); err == nil {
		t.Errorf("a bare hash fragment resolved to %q; only the full exact name should", got)
	}
	// ...but the full name it belongs to still does.
	if got, err := matchWindow(liveWindows, "homelife-wsl-1a170f99"); err != nil || got != "homelife-wsl-1a170f99" {
		t.Fatalf("the full exact name must still resolve: got %q err=%v", got, err)
	}
}

// An empty pattern is a substring of every name. Kept from the original guard.
func TestAnEmptyPatternIsRefused(t *testing.T) {
	for _, p := range []string{"", "   "} {
		if got, err := matchWindow(liveWindows, p); err == nil {
			t.Errorf("empty pattern %q resolved to %q", p, got)
		}
	}
	if _, err := matchWindow(nil, "homelife"); err == nil {
		t.Error("no windows at all must be an error, not a match")
	}
}
