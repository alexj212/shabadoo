package hub

// Pushing the session view to browsers instead of being asked for it.
//
// The dashboard polled `/api/sessions` every three seconds whether or not
// anything had changed. That is fine on a workstation and wrong on a phone,
// which is the client this is mostly used from: every poll is a radio wakeup, a
// TLS stream, and a full render, for a payload that is usually identical to the
// last one.
//
// Agents already report on a timer, so the coordinator knows the exact moment
// the view changes. This turns that into a push.
//
// Two properties the polling version had for free, which have to be built here:
//
//   - **It must degrade.** A proxy that buffers `text/event-stream` turns this
//     into a page that never updates — a silent failure, and the exact hazard
//     already documented for the agent stream. The browser falls back to polling
//     when the stream fails or goes quiet, so the worst case is the old
//     behaviour rather than a dead dashboard.
//   - **A slow client must not hold anything up.** Sends are non-blocking onto a
//     small buffer; a subscriber that cannot keep up loses intermediate frames,
//     which for a full-state snapshot means it simply gets the next one.

import (
	"fmt"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"
)

const (
	// eventKeepalive bounds silence on the stream. Proxies and phone radios
	// drop connections that say nothing, and a comment line is the cheapest
	// thing that keeps one alive.
	eventKeepalive = 25 * time.Second

	// eventCoalesce is how long to wait after a change before sending.
	//
	// Every agent reports every 5s, so with several nodes the changes arrive in
	// a cluster; without this a five-node deployment would send five near
	// identical snapshots in a row. It also bounds a misbehaving agent's ability
	// to drive renders in the browser.
	eventCoalesce = 400 * time.Millisecond

	// eventQueue is per-subscriber. Small on purpose: these are full-state
	// snapshots, so a backed-up client wants the newest one, not a queue of
	// stale ones.
	eventQueue = 1
)

// subscribers tracks which browsers want to hear about which tenant.
type subscribers struct {
	mu   sync.Mutex
	subs map[string]map[chan struct{}]struct{} // tenant -> set
}

func newSubscribers() *subscribers {
	return &subscribers{subs: map[string]map[chan struct{}]struct{}{}}
}

func (s *subscribers) add(tenant string) chan struct{} {
	ch := make(chan struct{}, eventQueue)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subs[tenant] == nil {
		s.subs[tenant] = map[chan struct{}]struct{}{}
	}
	s.subs[tenant][ch] = struct{}{}
	return ch
}

func (s *subscribers) remove(tenant string, ch chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.subs[tenant], ch)
	if len(s.subs[tenant]) == 0 {
		delete(s.subs, tenant)
	}
}

// notify wakes a tenant's subscribers. Never blocks: a channel that is already
// signalled needs no second signal, because what it delivers is "go read the
// current state", not a message.
func (s *subscribers) notify(tenant string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs[tenant] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (s *subscribers) count(tenant string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs[tenant])
}

// SessionsChanged tells a tenant's watching browsers that the view moved.
// Called by the report handler, which is the only thing that changes it.
func (h *Hub) SessionsChanged(tenant string) {
	if h.watchers != nil {
		h.watchers.notify(tenant)
	}
}

// events streams the session view as server-sent events.
//
// Same payload as GET /api/sessions, from the same builder, so a browser can
// use either and see the same thing.
func (h *humanAPI) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		// No streaming available (an HTTP/1.0 client, some middlewares). Say so
		// rather than hanging: the page falls back to polling on an error.
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	tenant := tenantOf(r.Context())

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	// Nginx and friends buffer event streams by default, which presents as a
	// page that connects and never updates. Traefik does not need this; it is
	// here because the deployment in front of this is not guaranteed to be.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	ch := h.hub.watchers.add(tenant)
	defer h.hub.watchers.remove(tenant, ch)

	var lastFingerprint [32]byte

	// seq makes the stream self-checking, and it exists because silence here is
	// ambiguous in a way the keepalive could not resolve.
	//
	// Frames that would render identically are skipped, deliberately — an idle
	// fleet must not cost more than the poll this replaced. But that means a
	// client which has received nothing cannot tell "nothing has changed" from
	// "I am no longer receiving frames", and `: ping` cannot help because a
	// comment carries no state and EventSource never surfaces one to the page
	// anyway. Clients were resolving it with a silence timer, which is a guess
	// dressed as a policy.
	//
	// With a monotonic sequence on every frame and the current value in the
	// keepalive, a client compares the two: equal means genuinely idle, greater
	// means it missed something and should resync. Same argument as the frame
	// header on a capture stream — throwing ordering away at the boundary makes
	// it unrecoverable everywhere downstream. Proposed by a client author who
	// had hit the ambiguity from the outside.
	seq := 0

	send := func(force bool) bool {
		payload, err := h.sessionsPayload(r.Context())
		if err != nil {
			return true // transient; keep the stream open rather than dropping it
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return true
		}

		// Skip a frame that renders identically to the last one.
		//
		// Agents report every 5s whether anything changed or not, so without
		// this an idle dashboard receives a frame every couple of seconds
		// forever — more traffic than the 3s poll this replaced, which would
		// make the whole exercise pointless on the phone it is for.
		//
		// The fingerprint covers what the page RENDERS, so `now` and
		// `updated_at` are excluded: both change on every report and neither is
		// read by the dashboard. The page advances relative times locally
		// between frames.
		fp := renderFingerprint(payload)
		if !force && fp == lastFingerprint {
			return true
		}
		lastFingerprint = fp

		// The sequence rides in the payload rather than SSE's own `id:` field.
		// `id:` is Last-Event-ID, which the browser replays on reconnect and
		// this server has no history to honour — claiming resumability it
		// cannot provide would be worse than not offering it.
		seq++
		if body, err = json.Marshal(withSeq(payload, seq)); err != nil {
			return true
		}
		if _, err := w.Write(append(append([]byte("data: "), body...), '\n', '\n')); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// The first frame goes immediately: a browser that has just connected has
	// nothing on screen, and waiting for the next agent report would leave it
	// blank for up to five seconds.
	if !send(true) {
		return
	}

	keepalive := time.NewTicker(eventKeepalive)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return

		case <-ch:
			// Coalesce a burst: several agents reporting at once is one change
			// as far as anyone looking at the page is concerned.
			timer := time.NewTimer(eventCoalesce)
			select {
			case <-timer.C:
			case <-r.Context().Done():
				timer.Stop()
				return
			}
			// Drain anything that arrived during the wait; the snapshot we are
			// about to send already includes it.
			select {
			case <-ch:
			default:
			}
			if !send(false) {
				return
			}

		case <-keepalive.C:
			// A comment line. Not an event, so the browser's onmessage does not
			// fire and the page does not re-render for a heartbeat.
			// A NAMED event, not a comment: EventSource delivers `event: ping`
			// to a listener and drops a `:` comment on the floor, so only this
			// shape can carry the sequence to the page. The bare comment stays
			// alongside it for any client written against the old wire — both
			// are ignored by a client that does not know them.
			ping := fmt.Sprintf(": ping\nevent: ping\ndata: {\"seq\":%d}\n\n", seq)
			if _, err := w.Write([]byte(ping)); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// renderFingerprint hashes the parts of a payload the dashboard actually
// renders, so a frame that would look identical can be skipped.
//
// It zeroes `updated_at` — the page never reads it, and it advances on every
// agent report — and ignores `now` by hashing only the node list. Node order is
// map iteration order, so it is sorted first; without that, an unchanged view
// would fingerprint differently on most frames and the dedupe would do nothing.
func renderFingerprint(payload map[string]any) [32]byte {
	nodes, _ := payload["nodes"].([]nodeView)
	stripped := make([]nodeView, len(nodes))
	copy(stripped, nodes)
	sort.Slice(stripped, func(i, j int) bool { return stripped[i].Node < stripped[j].Node })

	for i := range stripped {
		sessions := make([]Session, len(stripped[i].Sessions))
		copy(sessions, stripped[i].Sessions)
		for j := range sessions {
			sessions[j].UpdatedAt = 0
		}
		sort.Slice(sessions, func(a, b int) bool { return sessions[a].SessionID < sessions[b].SessionID })
		stripped[i].Sessions = sessions
	}

	body, err := json.Marshal(stripped)
	if err != nil {
		// Cannot compare, so never dedupe: sending a redundant frame is
		// harmless, skipping a real change is not.
		return [32]byte{}
	}
	return sha256.Sum256(body)
}

// WatcherCount reports how many browsers are streaming a tenant's view. Used by
// the health endpoint, and useful for answering "is anyone actually watching".
func (h *Hub) WatcherCount(tenant string) int {
	if h.watchers == nil {
		return 0
	}
	return h.watchers.count(tenant)
}

// withSeq attaches the stream's frame counter to a payload without the callers
// of sessionsPayload having to know about it.
//
// A map rather than a struct field because the payload is shared with
// /api/sessions, which has no stream and therefore no sequence — and a field
// that is always zero there would be a number that means nothing, which is the
// distinction this whole codebase keeps having to make.
func withSeq(payload any, seq int) map[string]any {
	raw, err := json.Marshal(payload)
	if err != nil {
		return map[string]any{"seq": seq}
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return map[string]any{"seq": seq}
	}
	m["seq"] = seq
	return m
}
