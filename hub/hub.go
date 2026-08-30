package hub

// The agent plane's transport.
//
// Agents dial the coordinator and hold a long-lived Server-Sent Events stream
// open to receive commands; results and reports come back as ordinary POSTs.
// The coordinator never dials an agent — that inversion is what removes the
// peer list, works from a laptop on hotel wifi, and needs no inbound port on
// any machine.
//
// SSE rather than WebSocket: it is stdlib-only, it passes through Cloudflare
// Tunnel with no upgrade negotiation, and reconnect is just re-issuing the GET.
// The cost is one extra connection per agent, which at a handful of hosts is
// not worth a dependency.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	// tokenTTL bounds an agent session. An agent re-signs a challenge to renew;
	// a stolen token stops working within a day even if nobody notices.
	tokenTTL = 24 * time.Hour

	// callTimeout bounds one command's round trip. A capture on a slow host or
	// a `reopen` that shells out to the launcher takes seconds, not minutes.
	callTimeout = 30 * time.Second

	// sendQueue is how many commands may be in flight to one agent before the
	// hub starts refusing. A wedged agent must not let the coordinator buffer
	// without limit.
	sendQueue = 64
)

// Version is the coordinator's own build, set by main at startup from the
// link-time stamp. It rides along in /api/sessions so the dashboard can show
// the hub's build next to each node's, which is the comparison that matters:
// the two are installed by the same command from the same binary, so a
// mismatch means one of them was installed from a different checkout.
var Version = "dev (unstamped)"

var (
	ErrAgentOffline = errors.New("agent is not connected")
	ErrAgentBusy    = errors.New("agent command queue is full")
	ErrBadToken     = errors.New("unknown or expired agent token")
)

// command is one instruction sent down an agent's stream.
type command struct {
	ID      string          `json:"id"`
	Op      string          `json:"op"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// result is an agent's reply, posted back up.
type result struct {
	ID      string          `json:"id"`
	OK      bool            `json:"ok"`
	Error   string          `json:"error,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// conn is one connected agent.
type conn struct {
	tenant   string
	node     string
	token    string
	platform string   // GOOS/GOARCH, reported at login — see NodePlatform
	caps     []string // what this host can do; lives and dies with the connection
	protocol int      // what this agent's build understands; 0 predates negotiation

	// payload is whether this node's installed ~/.claude matches the payload in
	// its own binary. Reported on the periodic report rather than at login,
	// because it changes the moment somebody runs `setup` and a badge that only
	// clears on reconnect would outlive the fix by up to a day.
	payload NodePayload
	expires  time.Time
	out      chan command
	closed   chan struct{}
	once     sync.Once
}

func (c *conn) close() {
	c.once.Do(func() { close(c.closed) })
}

// Hub tracks connected agents and correlates commands with their results.
type Hub struct {
	auth  *Authorizer
	store *Store
	now   func() time.Time

	mu      sync.Mutex
	byNode  map[string]*conn // keyed tenant\x00node — two tenants may both have a "wsl"
	byToken map[string]*conn
	pending map[string]chan result

	// releases holds binaries an operator published, for pushing to nodes.
	// nil until a release directory is configured.
	releases *ReleaseStore

	// watchers are browsers streaming the session view. nil is fine — it means
	// nobody is subscribed and SessionsChanged is a no-op.
	watchers *subscribers

	// blocked notices sessions stuck at a prompt. nil until a notifier is
	// configured — see EnableBlockedNotifications.
	blocked *blockedWatcher
	stuck   *stuckWatcher
	tasks   *taskWatcher
}

func New(auth *Authorizer, store *Store) *Hub {
	return &Hub{
		auth:     auth,
		store:    store,
		now:      time.Now,
		byNode:   map[string]*conn{},
		byToken:  map[string]*conn{},
		pending:  map[string]chan result{},
		watchers: newSubscribers(),
	}
}

// SetReleases gives the coordinator a place to keep published binaries.
func (h *Hub) SetReleases(r *ReleaseStore) { h.releases = r }

// Releases exposes the store for the human API.
func (h *Hub) Releases() *ReleaseStore { return h.releases }

// EnableBlockedNotifications starts telling a human when a session has been
// stuck at a prompt. Called only when a notifier is configured: without one
// there is nowhere to send, and a watcher that computed notifications and
// dropped them would be pure overhead.
func (h *Hub) EnableBlockedNotifications() {
	w := newBlockedWatcher(h.now)
	w.send = func(ctx context.Context, tenant, title, body, tag string) error {
		return postApprise(ctx, title, body, tag, "warning")
	}
	w.audit = func(ctx context.Context, tenant, target, detail string) {
		h.store.Tenant(tenant).Audit(ctx, AuditEntry{
			Actor: "coordinator", Action: "notify.blocked", Target: target, Detail: detail,
		}, h.now())
	}
	h.blocked = w

	// And a second observer on the same loop, which is not the nudge.
	//
	// The nudge is what makes a session notice mail, and it fails silently: a
	// skipped nudge and a delivered one look identical from every side. When it
	// broke, two sessions in a handoff sat waiting for ten hours and it took a
	// human asking one of them how it was doing.
	sw := newStuckWatcher(h.now)
	sw.send = func(ctx context.Context, tenant, title, body, tag string) error {
		return postApprise(ctx, title, body, tag, "warning")
	}
	// Try the mechanism again before interrupting a person. The condition the
	// watcher fires on is exactly the condition a nudge is safe to send under,
	// so the retry is free and silent — and it is what sweeps up a backlog the
	// arrival-time nudge can never revisit.
	sw.retry = func(ctx context.Context, tenant, sessionID string) {
		h.nudge(ctx, tenant, sessionID)
	}
	sw.audit = func(ctx context.Context, tenant, target, detail string) {
		h.store.Tenant(tenant).Audit(ctx, AuditEntry{
			Actor: "coordinator", Action: "stuck", Target: target, Detail: detail,
		}, h.now())
	}
	h.stuck = sw

	// The same notifier, and the same reason for gating on it: a watcher that
	// computed notifications and dropped them would be pure overhead.
	tw := newTaskWatcher(h.now)
	tw.send = func(ctx context.Context, tenant, title, body, tag string) error {
		return postApprise(ctx, title, body, tag, "warning")
	}
	tw.audit = func(ctx context.Context, tenant, target, detail string) {
		h.store.Tenant(tenant).Audit(ctx, AuditEntry{
			Actor: "coordinator", Action: "notify.task.quiet", Target: target, Detail: detail,
		}, h.now())
	}
	h.tasks = tw
}

// nodeKey scopes a node name to its tenant. Two tenants may each run a node
// called "wsl"; without this they would share one connection slot.
func nodeKey(tenant, node string) string { return tenant + "\x00" + node }

// Online reports which of a tenant's agents currently hold a stream open. This
// is the whole of presence — there is no heartbeat and no TTL to tune, because
// a dropped TCP connection is the signal.
func (h *Hub) Online(tenant string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := []string{}
	for _, c := range h.byNode {
		if c.tenant == tenant {
			out = append(out, c.node)
		}
	}
	return out
}

// IsOnline reports whether one agent is connected.
func (h *Hub) IsOnline(tenant, node string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.byNode[nodeKey(tenant, node)]
	return ok
}

// Call sends a command to an agent and waits for its result.
func (h *Hub) Call(ctx context.Context, tenant, node, op string, payload any) (json.RawMessage, error) {
	var raw json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		raw = b
	}

	// Refuse rather than degrade. An agent that predates pane addressing ignores
	// the field and writes to whichever pane is active — which is the failure
	// this exists to remove, and is invisible from every side. `upgrade --all`
	// is deliberately serial, so a mixed fleet is guaranteed during every
	// upgrade rather than hypothetical.
	if addressesAPane(raw) {
		if err := h.RequireProtocol(tenant, node, ProtocolPanes, "addressing a pane"); err != nil {
			return nil, err
		}
	}

	h.mu.Lock()
	c, ok := h.byNode[nodeKey(tenant, node)]
	if !ok {
		h.mu.Unlock()
		return nil, fmt.Errorf("%w: %s", ErrAgentOffline, node)
	}
	cmd := command{ID: newID(), Op: op, Payload: raw}
	reply := make(chan result, 1)
	h.pending[cmd.ID] = reply
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.pending, cmd.ID)
		h.mu.Unlock()
	}()

	select {
	case c.out <- cmd:
	default:
		return nil, fmt.Errorf("%w: %s", ErrAgentBusy, node)
	case <-c.closed:
		return nil, fmt.Errorf("%w: %s", ErrAgentOffline, node)
	}

	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	select {
	case r := <-reply:
		if !r.OK {
			return nil, errors.New(r.Error)
		}
		return r.Payload, nil
	case <-c.closed:
		return nil, fmt.Errorf("%w: %s (disconnected mid-call)", ErrAgentOffline, node)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ---------------------------------------------------------------------------
// HTTP surface (agent side)
// ---------------------------------------------------------------------------

// Routes registers the agent-plane endpoints.
//
// These are NOT behind the Access middleware: agents authenticate with SSH
// keys, not browser SSO. They are a separate authenticated surface on the same
// origin, and each one verifies its own credential.
// startedAt is process start, for the uptime the health endpoint reports.
var startedAt = time.Now()

// HealthRoutes registers the liveness endpoint.
//
// **Unauthenticated, and outside the identity middleware** — a monitor that
// needs a credential is a monitor nobody sets up, and under Cloudflare Access
// or device tokens there is no credential a container healthcheck could hold.
// That is the same reason /api/devices/redeem sits outside it.
//
// What it deliberately does NOT report: node names, project paths, session
// names, or anything else that would let an unauthenticated caller learn what
// this machine is working on. Counts and a build stamp only — enough to answer
// "is it up, is it the build I shipped, and can it reach its database", which
// is all a healthcheck is for.
func (h *Hub) HealthRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		// Proving the port is open proves almost nothing: the interesting
		// failure is a process that still answers while its database has gone
		// away underneath it.
		status, code := "ok", http.StatusOK
		if err := h.store.Ping(ctx); err != nil {
			status, code = "database unreachable", http.StatusServiceUnavailable
		}

		h.mu.Lock()
		agents := len(h.byNode)
		h.mu.Unlock()

		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]any{
			"status":  status,
			"version": Version,
			"uptime":  int(time.Since(startedAt).Seconds()),
			"agents":  agents,
			// Which background watchers are actually running.
			//
			// Not decoration. Restructuring one conditional silently switched
			// the blocked and stuck watchers off for several releases, and
			// nothing anywhere said so — a fleet with no notifications looks
			// exactly like a fleet where nothing was ever stuck. The startup log
			// said it and nobody reads a startup log. This is checkable from
			// outside, by anything, at any time.
			"watchers": h.activeWatchers(),
		})
	})
}

func (h *Hub) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /agent/hello", h.handleHello)
	mux.HandleFunc("POST /agent/login", h.handleLogin)
	mux.HandleFunc("GET /agent/stream", h.handleStream)
	mux.HandleFunc("POST /agent/result", h.handleResult)
	mux.HandleFunc("POST /agent/report", h.handleReport)
}

// handleHello issues a challenge. Unauthenticated by necessity — this is how
// an agent starts. It reveals nothing: a random nonce and the server's clock.
func (h *Hub) handleHello(w http.ResponseWriter, r *http.Request) {
	c, err := h.auth.Issue(h.now())
	if err != nil {
		http.Error(w, "challenge unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(w, c)
}

type loginReq struct {
	Challenge Challenge `json:"challenge"`
	PubKey    []byte    `json:"pubkey"`    // ssh wire format
	Signature []byte    `json:"signature"` // ssh.Marshal(ssh.Signature)
	Version   string    `json:"version"`

	// Platform is GOOS/GOARCH. Reported so the coordinator can pick the right
	// binary when asked to upgrade this node — sending a Mac a Linux build
	// would leave a host that cannot start and therefore cannot be told
	// anything again.
	Platform string `json:"platform,omitempty"`

	// Protocol is what this agent's build can be asked to do.
	//
	// Only a build stamp was exchanged before, which is a fact about a binary
	// rather than a contract about behaviour. `upgrade --all` is deliberately
	// serial, so mixed versions are GUARANTEED during every upgrade — and the
	// first operation that an old node silently mishandles rather than rejects
	// is a keystroke landing in the wrong pane, which is precisely the failure
	// pane addressing exists to fix.
	//
	// Absent means 0: every build that predates this. That is a legitimate
	// answer, not an error — it just cannot be asked for anything newer.
	Protocol int `json:"protocol,omitempty"`

	// Capabilities is what this node can do — detected by the agent, so it
	// reports what is true rather than what someone wrote down. Held for the
	// life of the connection and cleared when it drops: a capability is a fact
	// about a machine, so it does not age out on a timer the way a session's
	// self-declared status does, but a node nobody can reach can do nothing.
	Capabilities []string `json:"capabilities,omitempty"`
}

type loginResp struct {
	Node    string `json:"node"`
	Token   string `json:"token"`
	Expires int64  `json:"expires"`
}

func (h *Hub) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if !readJSON(w, r, &req) {
		return
	}
	now := h.now()

	agent, err := h.auth.Verify(req.Challenge, req.PubKey, req.Signature, now)
	if err != nil {
		// Deliberately uniform: which of "unknown key", "bad signature" and
		// "stale nonce" failed is useful to an attacker probing the key set.
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	c := &conn{
		tenant:   agent.Tenant,
		node:     agent.Name,
		token:    newToken(),
		platform: req.Platform,
		caps:     req.Capabilities,
		protocol: req.Protocol,
		expires:  now.Add(tokenTTL),
		out:      make(chan command, sendQueue),
		closed:   make(chan struct{}),
	}

	h.mu.Lock()
	// One connection per node. A reconnecting agent (or a replaced host)
	// supersedes the old one rather than both receiving half the commands.
	key := nodeKey(agent.Tenant, agent.Name)
	if old, ok := h.byNode[key]; ok {
		old.close()
		delete(h.byToken, old.token)
	}
	h.byNode[key] = c
	h.byToken[c.token] = c
	h.mu.Unlock()

	fp := ssh.FingerprintSHA256(agent.Key)
	tn := h.store.Tenant(agent.Tenant)
	if err := h.store.EnsureTenant(r.Context(), agent.Tenant, "", now); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := tn.SeenAgent(r.Context(), agent.Name, fp, req.Version, now); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tn.Audit(r.Context(), AuditEntry{
		Actor: "agent:" + agent.Name, Action: "login", Target: agent.Name, Detail: fp,
	}, now)
	// A node appearing is a change to the view, and it is not a report — without
	// this a browser would show a reconnected agent as offline until its first
	// report five seconds later.
	h.SessionsChanged(agent.Tenant)

	writeJSON(w, loginResp{Node: agent.Name, Token: c.token, Expires: c.expires.Unix()})
}

// handleStream is the long-lived downstream: commands as SSE events.
func (h *Hub) handleStream(w http.ResponseWriter, r *http.Request) {
	c, ok := h.authed(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Proxies that buffer would defeat the whole point of a live stream.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	// The stream *is* the presence signal, so its lifetime is the agent's.
	defer func() {
		h.disconnect(c)
		h.store.Tenant(c.tenant).DropAgentSessions(context.WithoutCancel(r.Context()), c.node)
	}()

	// Keepalives stop an idle connection being reaped by an intermediary. SSE
	// comments are ignored by the client but keep bytes flowing.
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-c.closed:
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case cmd := <-c.out:
			b, err := json.Marshal(cmd)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
	}
}

// handleResult receives one command's reply and hands it to the waiting Call.
func (h *Hub) handleResult(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authed(r); !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var res result
	if !readJSON(w, r, &res) {
		return
	}

	h.mu.Lock()
	reply, waiting := h.pending[res.ID]
	h.mu.Unlock()

	if waiting {
		// Buffered, so a Call that has already timed out cannot block the agent.
		select {
		case reply <- res:
		default:
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// reportReq is an agent's unsolicited push: its current window list.
type reportReq struct {
	Sessions []Session `json:"sessions"`

	// Payload is optional: an agent predating this simply omits it, and Known
	// stays false — which reads as "cannot tell", not as "clean".
	Payload *NodePayload `json:"payload,omitempty"`
}

// NodePayload separates "nothing pending" from "could not look".
//
// Pending is meaningless unless Known. A check that answers clean when it could
// not look is worse than an absent one, because nobody looks behind it.
type NodePayload struct {
	Known   bool `json:"payload_known"`
	Pending int  `json:"payload_pending"`

	// Drift names the files, capped by the node. A count is a question a reader
	// can defer forever; a list is one they can act on — and a count of 1 stood
	// on a node for months while the file behind it was a skill whose vendored
	// copy was three and a half months stale, with nothing anywhere saying
	// which. Pending stays the authoritative total.
	Drift []string `json:"payload_drift,omitempty"`
}

func (h *Hub) handleReport(w http.ResponseWriter, r *http.Request) {
	c, ok := h.authed(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req reportReq
	if !readJSON(w, r, &req) {
		return
	}
	now := h.now()
	ctx := r.Context()

	if req.Payload != nil {
		h.mu.Lock()
		if cc, ok := h.byNode[nodeKey(c.tenant, c.node)]; ok {
			cc.payload = *req.Payload
		}
		h.mu.Unlock()
	}

	// Replace this agent's view wholesale: a window that vanished must vanish
	// here too, and the agent is the authority on its own tmux server.
	//
	// In ONE transaction. As a DELETE plus N separate upserts, every reader
	// during a report saw an arbitrary prefix of this node's sessions — which
	// the recipient resolver then reported as "no session matches", listing the
	// half of the fleet it could see as though that were the fleet.
	tn := h.store.Tenant(c.tenant)
	for i := range req.Sessions {
		req.Sessions[i].Agent = c.node // never trust an agent's claim about which node it is
	}
	if err := tn.ReplaceAgentSessions(ctx, c.node, req.Sessions, now); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// After the store is consistent, so neither a notification nor a pushed
	// frame can arrive before the dashboard it points at can show the session.
	h.blocked.observe(ctx, c.tenant, c.node, req.Sessions)
	h.SessionsChanged(c.tenant)

	w.WriteHeader(http.StatusNoContent)
}

// authed resolves a bearer token to its connection.
func (h *Hub) authed(r *http.Request) (*conn, bool) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if tok == "" {
		return nil, false
	}
	h.mu.Lock()
	c, ok := h.byToken[tok]
	h.mu.Unlock()
	if !ok || h.now().After(c.expires) {
		return nil, false
	}
	return c, true
}

func (h *Hub) disconnect(c *conn) {
	h.mu.Lock()
	key := nodeKey(c.tenant, c.node)
	if cur, ok := h.byNode[key]; ok && cur == c {
		delete(h.byNode, key)
	}
	delete(h.byToken, c.token)
	h.mu.Unlock()
	c.close()
}

// Disconnect drops a node's live session: its stream closes and its bearer
// token stops working immediately.
//
// This is the half of revocation that `authorized_agents` cannot do. Removing a
// key from that file stops the NEXT login, so an agent already holding a token
// keeps driving every pane on its host until it happens to reconnect — which
// could be days. Removing the key and calling this is immediate and complete.
//
// It is deliberately not a permanent block: the file remains the single source
// of truth for who may connect, because two places to look is how a host nobody
// meant to authorize stays authorized. The agent WILL dial back in, and will be
// refused only if its key is gone.
func (h *Hub) Disconnect(tenant, node string) bool {
	h.mu.Lock()
	c, ok := h.byNode[nodeKey(tenant, node)]
	h.mu.Unlock()
	if !ok {
		return false
	}
	h.disconnect(c)
	h.SessionsChanged(tenant)
	return true
}

func newToken() string { return newID() + newID() }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// readJSON decodes a request body, rejecting anything oversized or unexpected.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

// addressesAPane reports whether a payload names a pane other than the first.
//
// Pane 0 and an absent pane both mean what every caller has always meant, so
// neither needs a newer agent — which keeps this from failing every write to a
// node during the window where it has not been upgraded yet.
func addressesAPane(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var probe struct {
		Pane *int `json:"pane"`
	}
	if json.Unmarshal(raw, &probe) != nil || probe.Pane == nil {
		return false
	}
	return *probe.Pane > 0
}


// activeWatchers names the background work this coordinator is doing, so an
// absence is visible rather than merely true.
func (h *Hub) activeWatchers() []string {
	out := []string{}
	if h.blocked != nil {
		out = append(out, "blocked")
	}
	if h.stuck != nil {
		out = append(out, "stuck")
	}
	if h.tasks != nil {
		out = append(out, "tasks")
	}
	if CIRepo != "" && AppriseURL != "" {
		out = append(out, "ci")
	}
	return out
}
