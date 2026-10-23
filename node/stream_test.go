package node

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// sseServer opens a stream and then either keeps it alive with pings or goes
// silent, holding the connection open — the shape of a stream whose host slept.
func sseServer(t *testing.T, ping time.Duration) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, ": connected\n\n")
		w.(http.Flusher).Flush()
		var tick <-chan time.Time
		if ping > 0 {
			tk := time.NewTicker(ping)
			defer tk.Stop()
			tick = tk.C
		}
		for {
			select {
			case <-r.Context().Done():
				return
			case <-tick:
				fmt.Fprint(w, ": ping\n\n")
				w.(http.Flusher).Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The pair varies one thing, whether keepalives arrive; the connection stays
// open in both. A watchdog that fired on any stream would fail the second arm,
// and one that never fired would fail the first.
func TestStreamGivesUpOnASilentConnectionButNotAPingingOne(t *testing.T) {
	const idle = 200 * time.Millisecond

	silent := New(Config{Coord: sseServer(t, 0).URL}, nil, nil)
	silent.idle = idle
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	err := silent.stream(ctx)
	if err == nil || !strings.Contains(err.Error(), "silent") {
		t.Fatalf("silent stream: want a silence error, got %v", err)
	}
	if took := time.Since(start); took > 1500*time.Millisecond {
		t.Fatalf("silent stream took %s to be noticed with idle %s", took, idle)
	}

	pinging := New(Config{Coord: sseServer(t, idle/4).URL}, nil, nil)
	pinging.idle = idle
	ctx2, cancel2 := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel2()
	err = pinging.stream(ctx2)
	if err != nil && strings.Contains(err.Error(), "silent") {
		t.Fatalf("pinging stream was dropped as silent: %v", err)
	}
	if ctx2.Err() == nil {
		t.Fatalf("pinging stream ended before its context: %v", err)
	}
}

// A stream that stayed up resets the backoff; one that dropped quickly, or
// never opened, keeps climbing.
func TestNextAttemptResetsOnlyAfterAStableStream(t *testing.T) {
	now := time.Now()
	if got := nextAttempt(7, now.Add(-streamStable-time.Second), now); got != 1 {
		t.Errorf("after a stable stream: attempt %d, want 1", got)
	}
	if got := nextAttempt(7, now.Add(-time.Second), now); got != 8 {
		t.Errorf("after a short-lived stream: attempt %d, want 8", got)
	}
	if got := nextAttempt(7, time.Time{}, now); got != 8 {
		t.Errorf("after a stream that never opened: attempt %d, want 8", got)
	}
}

// A suppressed repeat must still be accounted for when the next line is
// logged, or a quiet log reads as "no failures" when it means "the same one".
func TestUnloggedReportFailuresAreCountedOnTheNextLine(t *testing.T) {
	var buf bytes.Buffer
	out0, flags0 := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() { log.SetOutput(out0); log.SetFlags(flags0) }()

	c := New(Config{}, nil, nil)
	for range 4 {
		c.reportProblem("deadline exceeded")
	}
	c.reportProblem("connection reset")
	c.reportProblem("connection reset")
	c.reportOK()

	out := buf.String()
	if !strings.Contains(out, "report failed (5 in a row): connection reset (3 earlier failure(s) since") {
		t.Errorf("the changed error should count the 3 repeats it skipped:\n%s", out)
	}
	if !strings.Contains(out, "recovered after 6 failure(s) (1 earlier failure(s) since") {
		t.Errorf("recovery should count the 1 repeat it skipped:\n%s", out)
	}
	if strings.Count(out, "report failed") != 2 {
		t.Errorf("repeats should still be suppressed:\n%s", out)
	}
}
