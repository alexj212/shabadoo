package main

import "testing"

// Who owns a row, taken from the forms this fleet actually writes.
//
// The census that produced these cases, counted across every card rather than
// imagined: you 27, nobody 19, me 8, **you (Alex)** 4, devops 4, jeff 3,
// **devops** 3, you+mia 1, **observability** 1, mission-control 1.
//
// Eight rows are bold-wrapped and they failed TWO different ways: `**devops**`
// has no space so it parsed, yielding an owner rendered literally as
// `**devops**`; `**you (Alex)**` has one, so the colon was rejected entirely and
// the row read `(nobody named)` — a row with a real owner reported as ownerless,
// which is worse than blank because it reads as a measured absence.
//
// The control is the half the old rule got RIGHT and this must not lose: prose
// containing a colon is not an owner.
func TestOwnerToken(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"you", "you", true},
		{"nobody", "nobody", true},
		{"me", "me", true},
		{"devops", "devops", true},
		{"you+mia", "you+mia", true},
		{"mission-control", "mission-control", true},

		// The reported defect: emphasis is presentation, not identity.
		{"**devops**", "devops", true},
		{"**observability**", "observability", true},
		{"**you (Alex)**", "you (alex)", true},
		{"you (Alex)", "you (alex)", true},
		{"observability (session)", "observability (session)", true},
		{"~~you", "you", true},

		// Controls. Each must stay NOT an owner, or the fix has traded a
		// missing owner for an invented one, which is the worse direction.
		{"shipped", "shipped", true}, // a single token IS taken as an owner; that is the old contract
		{"the paging dialect which fixes", "", false},
		{"**two words** here", "", false},
		{"you (Alex) and somebody", "", false},
		{"", "", false},
		{"**", "", false},
	} {
		got, ok := ownerToken(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("ownerToken(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
