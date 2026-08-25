package hub

// A windowed per-key rate limiter.
//
// Extracted from the voice mint, which needed one first and is now one caller
// of two. The alternative was a second implementation of the same twenty lines,
// and two renderings of one idea drift — with the drift invisible until you
// rely on the one you were not looking at.
//
// Both callers are bounding a real cost rather than guessing-resistance: voice
// mints arrive with a bill, and messages between sessions can start sessions.
// The redeem throttle is the thing that resists guessing, and it is separate on
// purpose.

import (
	"sync"
	"time"
)

// rateLimiter allows `limit` events per `window`, per key.
type rateLimiter struct {
	mu     sync.Mutex
	at     map[string][]time.Time
	window time.Duration
	limit  int
}

func newRateLimiter(window time.Duration, limit int) *rateLimiter {
	return &rateLimiter{at: map[string][]time.Time{}, window: window, limit: limit}
}

// allow records an event and reports whether it was within the limit.
//
// It reserves the slot before the work happens rather than recording after it
// succeeds. Check-then-record reads better and is wrong under concurrency:
// callers can all pass the check before any of them records, which is a hole in
// the one guarantee this exists to give. Work that then fails gives the slot
// back — see refund.
func (r *rateLimiter) allow(key string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	cutoff := now.Add(-r.window)
	kept := r.at[key][:0]
	for _, t := range r.at[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= r.limit {
		r.at[key] = kept
		return false
	}
	r.at[key] = append(kept, now)
	return true
}

// refund returns a reservation for work that never happened.
//
// Charging for it is wrong in the way that bites hardest: when something is
// broken, the retries that diagnose it eat the budget for the retries that fix
// it.
func (r *rateLimiter) refund(key string, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()

	ts := r.at[key]
	for i := len(ts) - 1; i >= 0; i-- {
		if ts[i].Equal(at) {
			r.at[key] = append(ts[:i], ts[i+1:]...)
			return
		}
	}
}
