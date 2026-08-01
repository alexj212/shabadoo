package hub

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// A subscriber that cannot keep up must never hold up the thing notifying it.
// The report handler calls SessionsChanged inline, so a blocking send here
// would stall an agent's report behind a slow browser.
func TestNotifyNeverBlocks(t *testing.T) {
	s := newSubscribers()
	ch := s.add("alex")
	defer s.remove("alex", ch)

	done := make(chan struct{})
	go func() {
		// Far more notifications than the queue depth, with nothing reading.
		for i := 0; i < 1000; i++ {
			s.notify("alex")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("notify blocked on a subscriber that was not reading")
	}

	// And the signal survives: what it means is "go read the current state",
	// so one pending wakeup is all a subscriber needs.
	select {
	case <-ch:
	default:
		t.Fatal("no wakeup pending after 1000 notifications")
	}
}

// Tenants are isolated: one tenant's agents must not wake another's browsers,
// which would leak the fact that something happened even without the payload.
func TestNotifyIsPerTenant(t *testing.T) {
	s := newSubscribers()
	alex := s.add("alex")
	other := s.add("other")
	defer s.remove("alex", alex)
	defer s.remove("other", other)

	s.notify("alex")

	select {
	case <-alex:
	default:
		t.Error("the notified tenant's subscriber was not woken")
	}
	select {
	case <-other:
		t.Error("another tenant's subscriber was woken")
	default:
	}
}

// remove must actually drop the subscriber, or a browser that closed its tab
// leaks a channel for the lifetime of the process.
func TestRemoveDropsSubscriber(t *testing.T) {
	s := newSubscribers()
	ch := s.add("alex")
	if got := s.count("alex"); got != 1 {
		t.Fatalf("count = %d, want 1", got)
	}
	s.remove("alex", ch)
	if got := s.count("alex"); got != 0 {
		t.Fatalf("count after remove = %d, want 0", got)
	}
	// The tenant's map is dropped too, so a coordinator that has served a
	// thousand browsers is not carrying a thousand empty maps.
	s.mu.Lock()
	_, present := s.subs["alex"]
	s.mu.Unlock()
	if present {
		t.Error("empty tenant entry was kept")
	}
}

// Concurrent add/remove/notify is the normal case — browsers connect and drop
// while agents report — so the registry must be race-free.
func TestSubscribersConcurrent(t *testing.T) {
	s := newSubscribers()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			ch := s.add("alex")
			s.remove("alex", ch)
		}()
		go func() {
			defer wg.Done()
			s.notify("alex")
		}()
	}
	wg.Wait()
	if got := s.count("alex"); got != 0 {
		t.Fatalf("leaked %d subscribers", got)
	}
}

// A nil watcher set must be a no-op, not a panic: Hub values built by tests
// (and the fallback paths) do not always have one.
func TestSessionsChangedWithNoWatchers(t *testing.T) {
	h := &Hub{}
	h.SessionsChanged("alex") // must not panic
	if got := h.WatcherCount("alex"); got != 0 {
		t.Errorf("WatcherCount = %d, want 0", got)
	}
}

// Agents report every 5s whether anything changed or not. Without dedupe an
// idle dashboard receives a frame every couple of seconds forever — MORE
// traffic than the 3s poll this replaced, which would defeat the whole point
// on the phone it is for.
func TestRenderFingerprintIgnoresNoise(t *testing.T) {
	payload := func(updated int64, order bool) map[string]any {
		a := nodeView{Node: "wsl", Online: true, Sessions: []Session{
			{SessionID: "s1", Alias: "homelab", UpdatedAt: updated},
			{SessionID: "s2", Alias: "iptv", UpdatedAt: updated},
		}}
		b := nodeView{Node: "mac", Online: true, Sessions: []Session{
			{SessionID: "s3", Alias: "ios", UpdatedAt: updated},
		}}
		nodes := []nodeView{a, b}
		if order {
			nodes = []nodeView{b, a} // map iteration order is not stable
		}
		return map[string]any{"now": updated, "nodes": nodes}
	}

	base := renderFingerprint(payload(100, false))

	if renderFingerprint(payload(999, false)) != base {
		t.Error("updated_at/now changed the fingerprint; every report would send a frame")
	}
	if renderFingerprint(payload(100, true)) != base {
		t.Error("node ordering changed the fingerprint; dedupe would never fire")
	}

	// A change the page WOULD render must still come through.
	changed := payload(100, false)
	changed["nodes"].([]nodeView)[0].Sessions[0].InputState = "dialog"
	if renderFingerprint(changed) == base {
		t.Error("a session entering a dialog did not change the fingerprint")
	}

	gone := payload(100, false)
	gone["nodes"].([]nodeView)[0].Sessions = gone["nodes"].([]nodeView)[0].Sessions[:1]
	if renderFingerprint(gone) == base {
		t.Error("a vanished window did not change the fingerprint")
	}

	offline := payload(100, false)
	offline["nodes"].([]nodeView)[0].Online = false
	if renderFingerprint(offline) == base {
		t.Error("a node going offline did not change the fingerprint")
	}
}

// The node list is the page's top-level layout, and Go randomises map
// iteration on every call — so an unsorted slice swaps the sections under the
// reader roughly one frame in twelve, and with three nodes there are six
// orderings to shuffle between. Reported from use as "the session list bounces
// around too much".
func TestSessionsPayloadNodeOrderIsStable(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	now := time.Now()

	// Deliberately inserted out of alphabetical order, and enough of them that
	// a random iteration order is overwhelmingly unlikely to be sorted.
	for _, n := range []string{"wsl", "mac", "dm", "pi", "builder", "laptop"} {
		if err := s.SeenAgent(ctx, n, "SHA256:x", "v1", now); err != nil {
			t.Fatal(err)
		}
		if err := s.UpsertSession(ctx, Session{
			SessionID: "s-" + n, Agent: n, Window: "claude:0",
		}, now); err != nil {
			t.Fatal(err)
		}
	}

	h := &humanAPI{hub: New(nil, s.s), store: s.s, now: time.Now}
	rctx := context.WithValue(ctx, identityKey{}, Identity{Tenant: s.id})

	var first []string
	for i := 0; i < 20; i++ {
		payload, err := h.sessionsPayload(rctx)
		if err != nil {
			t.Fatal(err)
		}
		var order []string
		for _, n := range payload["nodes"].([]nodeView) {
			order = append(order, n.Node)
		}
		if i == 0 {
			first = order
			if !sort.StringsAreSorted(order) {
				t.Fatalf("nodes are not sorted: %v", order)
			}
			continue
		}
		if strings.Join(order, ",") != strings.Join(first, ",") {
			t.Fatalf("node order changed between calls:\n  %v\n  %v", first, order)
		}
	}
}
