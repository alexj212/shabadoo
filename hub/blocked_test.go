package hub

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The watcher's whole job is edge detection. Agents report every five seconds,
// so every mistake here has the same shape: a notification per report, forever.
func TestBlockedWatcher(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	w := newBlockedWatcher(func() time.Time { return now })

	var sent []string
	w.send = func(_ context.Context, _, title, _, _ string) error {
		sent = append(sent, title)
		return nil
	}
	// send runs in a goroutine; drain by observing then yielding.
	observe := func(sessions ...Session) {
		w.observe(context.Background(), "alex", "wsl", sessions)
		time.Sleep(20 * time.Millisecond)
	}
	tick := func(d time.Duration) { now = now.Add(d) }

	dialog := Session{Window: "claude:3", Alias: "homelab", InputState: "dialog"}
	composer := Session{Window: "claude:3", Alias: "homelab", InputState: "composer"}

	// A prompt answered before the grace period elapses never notifies. This is
	// the common case by far — someone at the keyboard hitting enter.
	observe(dialog)
	tick(30 * time.Second)
	observe(dialog)
	tick(30 * time.Second)
	observe(composer)
	if len(sent) != 0 {
		t.Fatalf("notified about a prompt answered inside the grace period: %v", sent)
	}

	// One that stands past the grace period notifies exactly once, no matter how
	// many reports arrive while it stands.
	observe(dialog)
	tick(blockedGrace + time.Second)
	observe(dialog)
	if len(sent) != 1 {
		t.Fatalf("sent = %v, want one notification", sent)
	}
	for i := 0; i < 5; i++ {
		tick(5 * time.Second)
		observe(dialog)
	}
	if len(sent) != 1 {
		t.Fatalf("re-notified while the same prompt stood: %v", sent)
	}

	// Still standing an hour later: one reminder, not a stream.
	tick(blockedRepeat)
	observe(dialog)
	if len(sent) != 2 || sent[1] != "homelab is still waiting" {
		t.Fatalf("sent = %v, want a single reminder", sent)
	}

	// Answering resets the timer, so the NEXT block is notified about too — a
	// session that blocks twice in a morning is two events, not one.
	observe(composer)
	observe(dialog)
	tick(blockedGrace + time.Second)
	observe(dialog)
	if len(sent) != 3 || sent[2] != "homelab is waiting" {
		t.Fatalf("sent = %v, want a fresh notification after the prompt was answered", sent)
	}

	// A window that disappears must not keep state that would misdate a later
	// window reusing the same index.
	observe()
	observe(dialog)
	tick(10 * time.Second)
	observe(dialog)
	if len(sent) != 3 {
		t.Fatalf("a reused window notified from stale state: %v", sent)
	}
}

// Nodes are independent: one agent's report must not clear another's state.
func TestBlockedWatcherIsPerNode(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	w := newBlockedWatcher(func() time.Time { return now })
	var sent int
	w.send = func(context.Context, string, string, string, string) error { sent++; return nil }

	s := Session{Window: "claude:1", Alias: "proj", InputState: "dialog"}
	w.observe(context.Background(), "alex", "wsl", []Session{s})
	now = now.Add(blockedGrace / 2)

	// The mac reporting must not reset wsl's clock.
	w.observe(context.Background(), "alex", "mac", []Session{s})
	now = now.Add(blockedGrace/2 + time.Second)
	w.observe(context.Background(), "alex", "wsl", []Session{s})
	time.Sleep(20 * time.Millisecond)

	if sent != 1 {
		t.Fatalf("sent = %d, want 1 (wsl's prompt, aged across the mac's report)", sent)
	}
}

// The notification leads with the question when the agent recognised one.
// A phone buzzing with "something is waiting" sends you to a screen to find
// out what; one carrying the question is sometimes the whole interaction.
func TestBlockedNotificationCarriesTheQuestion(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	w := newBlockedWatcher(func() time.Time { return now })
	var bodies []string
	w.send = func(_ context.Context, _, _, body, _ string) error {
		bodies = append(bodies, body)
		return nil
	}

	s := Session{Window: "claude:3", Alias: "homelab", InputState: "dialog",
		CWD: "/srv/homelab", Asking: "Do you want to create deploy.sh?"}
	w.observe(context.Background(), "alex", "wsl", []Session{s})
	now = now.Add(blockedGrace + time.Second)
	w.observe(context.Background(), "alex", "wsl", []Session{s})
	time.Sleep(20 * time.Millisecond)

	if len(bodies) != 1 {
		t.Fatalf("got %d notifications, want 1", len(bodies))
	}
	if !strings.HasPrefix(bodies[0], "Do you want to create deploy.sh?") {
		t.Errorf("the question is not first:\n%s", bodies[0])
	}
	for _, want := range []string{"homelab", "wsl", "/srv/homelab"} {
		if !strings.Contains(bodies[0], want) {
			t.Errorf("notification lost %q:\n%s", want, bodies[0])
		}
	}

	// With no recognised question it must fall back rather than emit a blank
	// leading line — an unrecognised modal is the common case, not an error.
	bodies = nil
	now = now.Add(2 * blockedRepeat)
	plain := s
	plain.Asking = ""
	w.observe(context.Background(), "alex", "wsl", []Session{plain})
	time.Sleep(20 * time.Millisecond)
	if len(bodies) != 1 || strings.HasPrefix(bodies[0], "\n") {
		t.Errorf("fallback body is wrong: %q", bodies)
	}
}
