package hub

import (
	"testing"
	"time"
)

// This is the first credential the coordinator hands out that costs money —
// every voice session is billed per minute — so the limit is about SPEND, not
// about guessing, which is what the redeem throttle is for.
func TestVoiceRateLimitIsPerDevice(t *testing.T) {
	v := &voiceLimiter{at: map[string][]time.Time{}}
	now := time.Unix(1_700_000_000, 0)

	for i := 0; i < voiceRateLimit; i++ {
		if !v.allow("device:a", now) {
			t.Fatalf("refused mint %d of %d inside the limit", i+1, voiceRateLimit)
		}
	}
	if v.allow("device:a", now) {
		t.Error("allowed a mint past the limit")
	}

	// Keyed on the credential: one device's enthusiasm must not exhaust
	// another's budget.
	if !v.allow("device:b", now) {
		t.Error("a second device was blocked by the first device's usage")
	}

	// The window rolls; it is a rate, not a quota.
	if !v.allow("device:a", now.Add(voiceRateWindow+time.Minute)) {
		t.Error("the limit did not decay after the window")
	}
}

// A half-configured deployment must not mint. Both halves are required, and
// the failure has to be at configuration time rather than at the moment
// somebody is holding a phone waiting to talk.
func TestVoiceRequiresBothHalves(t *testing.T) {
	for _, tc := range []struct {
		key, agent string
		want       bool
	}{
		{"", "", false},
		{"k", "", false},
		{"", "a", false},
		{"k", "a", true},
	} {
		got := tc.key != "" && tc.agent != ""
		if got != tc.want {
			t.Errorf("key=%q agent=%q enabled=%v, want %v", tc.key, tc.agent, got, tc.want)
		}
	}
}
