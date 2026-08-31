package main

import (
	"testing"
	"time"
)

// `update` must distinguish a NEWER published release from an OLDER one.
//
// Pinned as a pair because a single-sided fixture cannot see the defect that
// shipped: the code asked only whether the published tag DIFFERED from the
// running one and advised installing it either way. Every assertion of the form
// "a different release is offered" passed happily while the tool was telling a
// v0.4.77 machine to install v0.4.75 — which would have removed a
// credential-handling rule that shipped in between.
func TestPublishedIsOlderSeparatesBothDirections(t *testing.T) {
	built := time.Date(2026, 8, 31, 11, 42, 50, 0, time.UTC)
	old := buildTime
	buildTime = built.Format(time.RFC3339)
	t.Cleanup(func() { buildTime = old })

	if !publishedIsOlder(built.Add(-24 * time.Hour)) {
		t.Error("a release published before this build is a DOWNGRADE and must be refused")
	}
	if publishedIsOlder(built.Add(24 * time.Hour)) {
		t.Error("a release published after this build is an upgrade and must not be refused")
	}

	// Unknown is not a downgrade. Refusing here would leave a build with no
	// comparable stamp unable to ever update itself, which is the opposite
	// failure and a worse one: "cannot tell" must not become "refuse".
	if publishedIsOlder(time.Time{}) {
		t.Error("an unknown publish time must not be reported as a downgrade")
	}
	buildTime = ""
	if publishedIsOlder(built.Add(-24 * time.Hour)) {
		t.Error("an unstamped build cannot order anything and must not refuse")
	}
}
