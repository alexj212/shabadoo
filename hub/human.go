package hub

// The human plane: what the browser talks to.
//
// These are the flock's old endpoints, moved. The shape is deliberately close
// to what static/index.html already calls, so the dashboard is a re-point
// rather than a rewrite — but every write is now attributed to a verified
// human and recorded in the audit log, which the flock could not do.

import (
	"errors"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// writeAllowed reports whether this request's credential may change anything.
//
// Enforced by DEFAULT-DENY on method: a read-scoped credential may GET, and may
// POST only to the handful of endpoints on the allowlist below. Written this
// way round on purpose — if someone adds a new write endpoint and forgets about
// scopes, it is refused for read-only clients rather than quietly permitted.
// The opposite default is how "read-only" credentials end up writing.
func writeAllowed(r *http.Request) bool {
	id, ok := IdentityFrom(r.Context())
	if !ok || id.Scope != ScopeRead {
		return true // full scope, or a provider that does not do scopes at all
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	// Renewing its own token is not a write in any meaningful sense, and a
	// read-only device that cannot renew would silently expire in 90 days —
	// which is exactly the lockout the renew endpoint exists to prevent.
	//
	// Registering for push is on the same footing, and more so: a read-only
	// phone exists to be *told* when something needs attention. Refusing it a
	// push token would leave it able to see a blocked session only by opening
	// the app and looking, which is the thing notifications replace.
	switch r.URL.Path {
	case "/api/devices/renew", "/api/devices/self/push":
		return true
	}
	return false
}

// requireWrite refuses a mutating call from a read-only credential.
func requireWrite(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !writeAllowed(r) {
			// 403, deliberately distinct from the 401 an unauthenticated call
			// gets. The credential is fine — it simply may not do this, and a
			// client must not react by discarding it.
			http.Error(w, "forbidden: this credential is read-only "+
				"(the token is valid; do not discard it)", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// HumanRoutes registers the browser- and app-facing API on mux. The caller is
// responsible for wrapping it in an IdentityProvider middleware.
func HumanRoutes(mux *http.ServeMux, hub *Hub, store *Store, devices *DeviceStore) {
	h := &humanAPI{hub: hub, store: store, devices: devices, now: time.Now}

	mux.HandleFunc("GET /api/sessions", h.sessions)
	// The same view, pushed. The page prefers this and falls back to polling
	// /api/sessions when it fails — see hub/events.go.
	mux.HandleFunc("GET /api/events", h.events)
	mux.HandleFunc("GET /api/capture", h.capture)
	mux.HandleFunc("GET /api/claude/session", h.claudeSession)
	mux.HandleFunc("GET /api/claude/events", h.claudeEvents)
	mux.HandleFunc("GET /api/missions/log", h.missionLog)
	mux.HandleFunc("GET /api/missions/resolved", h.missionResolved)
	mux.HandleFunc("GET /api/audit", h.audit)
	mux.HandleFunc("GET /api/messages", h.messages)
	mux.HandleFunc("GET /api/tasks", h.tasks)
	mux.HandleFunc("GET /api/input-state", h.inputState)
	mux.HandleFunc("GET /api/folders", h.folders)

	mux.HandleFunc("POST /api/select", requireWrite(h.write("select")))
	mux.HandleFunc("POST /api/send", requireWrite(h.write("send")))
	mux.HandleFunc("POST /api/command", requireWrite(h.write("command")))
	// Raw keys answer a dialog the composer cannot: text sent to a modal is
	// swallowed, so a prompt needs the keypress itself.
	mux.HandleFunc("POST /api/keys", requireWrite(h.write("keys")))
	mux.HandleFunc("POST /api/kill", requireWrite(h.write("kill")))
	mux.HandleFunc("POST /api/reopen", requireWrite(h.write("reopen")))
	mux.HandleFunc("POST /api/open", requireWrite(h.write("open")))

	mux.HandleFunc("POST /api/message/send", requireWrite(h.sendMessage))
	mux.HandleFunc("POST /api/message/broadcast", requireWrite(h.broadcastMessage))

	// Device enrolment for the iOS app. Minting a code requires an already
	// authenticated human — that gate is the whole point.
	mux.HandleFunc("POST /api/devices/code", requireWrite(h.enrolCode))
	mux.HandleFunc("GET /api/devices", h.listDevices)
	mux.HandleFunc("POST /api/devices/revoke", requireWrite(h.revokeDevice))
	// Renewal keeps a live credential alive. Without it a 90-day TTL means the
	// only recovery is restarting the coordinator with --bootstrap, which is a
	// trip to a terminal — impossible from the phone that just expired.
	mux.HandleFunc("POST /api/devices/renew", h.renewDevice)
	// Where to send this device's notifications. Self-service and repeatable:
	// iOS hands the app a new APNs token on reinstall, on restore, and at its
	// own discretion, so the app re-registers on every launch.
	mux.HandleFunc("PUT /api/devices/self/push", h.setPushToken)

	// Shipping a new binary to a node. Both are writes in the fullest sense —
	// publishing decides what code every host will run, and upgrading makes one
	// run it — so both are behind requireWrite and both are audited.
	mux.HandleFunc("GET /api/releases", h.listReleases)
	mux.HandleFunc("POST /api/releases", requireWrite(h.publishRelease))
	mux.HandleFunc("POST /api/upgrade", requireWrite(h.upgradeNode))
	// Cutting a node off now. See Hub.Disconnect for why this is separate from
	// authorized_agents rather than replacing it.
	mux.HandleFunc("POST /api/nodes/disconnect", requireWrite(h.disconnectNode))

	// Minting a voice session is NOT behind requireWrite: a read-only phone is
	// exactly the one that benefits from being able to ask what is going on
	// out loud. What the voice agent can then DO is decided by the device's own
	// scope, because its tools call this same API with this same token.
	mux.HandleFunc("POST /api/voice/session", h.voiceSession)
}

func (h *humanAPI) disconnectNode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Node string `json:"node"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Node == "" {
		http.Error(w, "node is required", http.StatusBadRequest)
		return
	}
	dropped := h.hub.Disconnect(tenantOf(r.Context()), req.Node)

	// Audited whether or not it was connected: "I cut that host off at 04:12"
	// is the claim someone will want to check, and an attempt that found
	// nothing connected is part of that story.
	detail := "disconnected"
	if !dropped {
		detail = "was not connected"
	}
	h.scope(r.Context()).Audit(r.Context(), AuditEntry{
		Actor: actor(r.Context()), Action: "node.disconnect", Target: req.Node, Detail: detail,
	}, h.now())

	writeJSON(w, map[string]any{"node": req.Node, "disconnected": dropped})
}

func (h *humanAPI) listReleases(w http.ResponseWriter, r *http.Request) {
	rs := h.hub.Releases()
	if rs == nil {
		writeJSON(w, map[string]any{"releases": []Release{}})
		return
	}
	// Which node is on what, alongside what is available: the question anyone
	// asks of a release list is "who needs upgrading", and answering it from
	// two endpoints is how a stale answer gets acted on.
	nodes := map[string]map[string]string{}
	for _, n := range h.hub.Online(tenantOf(r.Context())) {
		platform := h.hub.NodePlatform(tenantOf(r.Context()), n)
		if platform == "" {
			// An older agent. Naming that is the difference between "why is
			// this column blank" and one manual install.
			platform = "(build predates upgrade support)"
		}
		nodes[n] = map[string]string{"platform": platform}
	}
	writeJSON(w, map[string]any{"releases": rs.List(), "nodes": nodes})
}

func (h *humanAPI) publishRelease(w http.ResponseWriter, r *http.Request) {
	rs := h.hub.Releases()
	if rs == nil {
		http.Error(w, "this coordinator has no release directory configured",
			http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	// tool and component are empty for shabadoo itself, which keeps every
	// release published before tool sets existed addressable unchanged.
	rel, err := rs.PublishComponent(q.Get("tool"), q.Get("component"),
		q.Get("version"), q.Get("platform"), r.Body, h.now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.scope(r.Context()).Audit(r.Context(), AuditEntry{
		Actor: actor(r.Context()), Action: "release.publish",
		Target: rel.Version, Detail: rel.Platform + " " + rel.SHA256[:12],
	}, h.now())
	writeJSON(w, rel)
}

func (h *humanAPI) upgradeNode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Node    string `json:"node"`
		Version string `json:"version"`
		Tool    string `json:"tool"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Node == "" {
		http.Error(w, "node is required", http.StatusBadRequest)
		return
	}
	tenant := tenantOf(r.Context())

	// Another tool is INSTALLED, not swapped under a running process, so it
	// takes the simpler path and never borrows the restart dance.
	if req.Tool != "" {
		set, err := h.hub.UpgradeNodeTool(r.Context(), tenant, req.Node, req.Tool, req.Version)
		detail := req.Tool
		if len(set) > 0 {
			detail = fmt.Sprintf("%s %s (%d components)", req.Tool, set[0].Version, len(set))
		}
		if err != nil {
			detail += " — " + err.Error()
		}
		h.hub.store.Tenant(tenant).Audit(r.Context(), AuditEntry{
			Actor: actor(r.Context()), Action: "node.install_tool",
			Target: req.Node, Detail: detail,
		}, h.now())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"tool": req.Tool, "components": len(set), "version": set[0].Version})
		return
	}

	rel, err := h.hub.upgradeNode(r.Context(), tenant, req.Node, req.Version)
	// Audited either way. An upgrade that failed is more interesting than one
	// that worked, and a node left mid-swap is exactly what someone reading
	// this log later will be trying to reconstruct.
	detail := rel.Version + " " + rel.Platform
	if err != nil {
		detail += " [failed: " + err.Error() + "]"
	}
	h.scope(r.Context()).Audit(r.Context(), AuditEntry{
		Actor: actor(r.Context()), Action: "node.upgrade", Target: req.Node, Detail: detail,
	}, h.now())

	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"node": req.Node, "version": rel.Version, "platform": rel.Platform})
}

// scopeName renders a scope for humans; the full scope is the empty string,
// which reads badly in an audit log.
func scopeName(scope string) string {
	if scope == ScopeRead {
		return "read"
	}
	return "full"
}

type humanAPI struct {
	hub     *Hub
	store   *Store
	devices *DeviceStore
	now     func() time.Time
}

// actor names the human behind a request, for the audit log. Requests that
// somehow arrive without a verified identity are attributed as such rather
// than silently recorded as nobody.
func actor(ctx context.Context) string {
	id, ok := IdentityFrom(ctx)
	if !ok {
		return "unverified"
	}

	// A device is named by its LABEL and id, not by an email. A device inherits
	// the email of whoever minted its code, so several devices legitimately
	// share one — attributing by email made every enrolled device show up as
	// the same actor, which quietly made the audit log unable to answer the
	// question it exists for. The short id disambiguates two devices a person
	// gave the same name, and ties a row to something revocable.
	if deviceID, isDevice := strings.CutPrefix(id.Sub, "device:"); isDevice {
		label := id.Label
		if label == "" {
			label = "unnamed device"
		}
		return fmt.Sprintf("%s (device %s)", label, shortID(deviceID))
	}

	if id.Email != "" {
		return id.Email
	}
	if id.Sub != "" {
		return id.Sub
	}
	return "unverified"
}

// shortID trims an opaque id to something readable in a log line while staying
// long enough to identify one device among a handful.
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// tenantOf resolves which tenant's data a request may touch.
//
// It comes from the verified identity and nothing else — never from a query
// parameter or header the client controls. In a hosted deployment that is the
// entire isolation boundary between customers.
func tenantOf(ctx context.Context) string {
	if id, ok := IdentityFrom(ctx); ok && id.Tenant != "" {
		return id.Tenant
	}
	return DefaultTenant
}

// scope returns the store handle for this request's tenant.
func (h *humanAPI) scope(ctx context.Context) *Tenant {
	return h.store.Tenant(tenantOf(ctx))
}

// nodeView is one agent's contribution to the merged dashboard, preserving the
// flock's response shape.
type nodeView struct {
	Node   string `json:"node"`
	Online bool   `json:"online"`
	// Version is the build the node reported at its last login, "" if it has
	// never logged in. Surfaced so a silently downgraded host is visible as
	// something other than healthy.
	Version string `json:"version,omitempty"`

	// Capabilities is what this machine can do, detected by its agent. Present
	// only while the node is connected — a node nobody can reach can do
	// nothing, so an offline host advertising a microphone would be an
	// invitation to route work that cannot arrive.
	Capabilities []string `json:"capabilities,omitempty"`

	// CapabilitiesKnown separates "reported none" from "could not tell us".
	//
	// An absent list means nothing on its own: it is equally an old build that
	// cannot report and a machine with nothing to report. Rendering the second
	// as the first is the same mistake as a staleness detector reporting clean
	// on a platform it cannot inspect, and a resolver listing the half of the
	// fleet it can see as though that were the fleet — all three shipped in one
	// evening, which is what turned it into a rule. See Conventions.
	CapabilitiesKnown bool      `json:"capabilities_known"`

	// PayloadPending is how many ~/.claude files on that node differ from the
	// payload in its own binary — non-zero means somebody should run `setup`
	// there. Absent unless PayloadKnown, because "0 pending" and "could not
	// look" are different answers and only one of them means the node is fine.
	PayloadKnown   bool `json:"payload_known"`
	PayloadPending int  `json:"payload_pending,omitempty"`
	// PayloadDrift names which files differ, so "1 pending" is actionable
	// rather than a number somebody defers.
	PayloadDrift []string `json:"payload_drift,omitempty"`
	Sessions          []Session `json:"sessions"`
}

func (h *humanAPI) sessions(w http.ResponseWriter, r *http.Request) {
	payload, err := h.sessionsPayload(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, payload)
}

// sessionsPayload builds what the dashboard renders.
//
// Shared by the poll and the event stream deliberately: two renderings of the
// same view drift, and the drift is invisible until whichever one you are not
// looking at is the one you need.
func (h *humanAPI) sessionsPayload(ctx context.Context) (map[string]any, error) {
	now := h.now()
	tenant := tenantOf(ctx)
	all, err := h.scope(ctx).ListSessions(ctx, now)
	if err != nil {
		return nil, err
	}
	// Best effort: a node list is more useful without versions than not at all.
	versions, err := h.scope(ctx).AgentVersions(ctx)
	if err != nil {
		versions = map[string]string{}
	}

	online := map[string]bool{}
	for _, n := range h.hub.Online(tenant) {
		online[n] = true
	}

	byNode := map[string][]Session{}
	for _, s := range all {
		byNode[s.Agent] = append(byNode[s.Agent], s)
	}
	// An agent that is connected but has reported nothing yet still belongs in
	// the view — losing a machine should be visible, not silently absent.
	for node := range online {
		if _, ok := byNode[node]; !ok {
			byNode[node] = []Session{}
		}
	}

	// Sorted, because Go randomises map iteration on every call and this slice
	// is rendered as the page's top-level layout: without it the node sections
	// swap places under the reader roughly one frame in twelve, and with three
	// nodes there are six orderings to shuffle between. Sessions within a node
	// are already ordered by the query (agent, win_index).
	//
	// Alphabetical rather than online-first, so a node's position never moves
	// for a given set of nodes — a row that jumps when a machine reconnects is
	// the same complaint in a smaller form. Offline is shown by the badge.
	nodes := make([]nodeView, 0, len(byNode))
	for node, sessions := range byNode {
		nodes = append(nodes, nodeView{
			Node:              node,
			Online:            online[node],
			Version:           versions[node],
			Capabilities:      h.hub.NodeCapabilities(tenant, node),
			PayloadKnown:      h.hub.NodeInstalledPayload(tenant, node).Known,
			PayloadPending:    h.hub.NodeInstalledPayload(tenant, node).Pending,
			PayloadDrift:      h.hub.NodeInstalledPayload(tenant, node).Drift,
			CapabilitiesKnown: h.hub.CapabilitiesKnown(tenant, node),
			Sessions:          sessions,
		})
	}

	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Node < nodes[j].Node })

	return map[string]any{
		"now":     now.Unix(),
		"version": Version,
		"nodes":   nodes,
	}, nil
}

// capture proxies a pane read to the agent that owns it.
func (h *humanAPI) capture(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lines, _ := strconv.Atoi(q.Get("lines"))
	window, _ := strconv.Atoi(q.Get("window"))

	raw, err := h.hub.Call(r.Context(), tenantOf(r.Context()), q.Get("node"), "capture", map[string]any{
		"session": q.Get("session"),
		"window":  window,
		"lines":   lines,
		"color":   q.Get("color") == "1",
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	var out struct {
		Text string `json:"text"`
	}
	json.Unmarshal(raw, &out)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(out.Text))
}

// inputState reports whether a pane's keyboard belongs to the message composer
// or to a modal dialog, so the dashboard can offer the right control: a text
// box submits nothing while a prompt is up.
func (h *humanAPI) inputState(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	window, _ := strconv.Atoi(q.Get("window"))
	raw, err := h.hub.Call(r.Context(), tenantOf(r.Context()), q.Get("node"), "input_state", map[string]any{
		"session": q.Get("session"),
		"window":  window,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw)
}

// folders lists startable folders on a node: the boot list, plus anywhere a
// session has run, each flagged if a window is already open there. Without it
// "open a session" means typing an absolute path, which no phone user will do.
func (h *humanAPI) folders(w http.ResponseWriter, r *http.Request) {
	raw, err := h.hub.Call(r.Context(), tenantOf(r.Context()), r.URL.Query().Get("node"), "folders", nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw)
}

func (h *humanAPI) claudeSession(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	raw, err := h.hub.Call(r.Context(), tenantOf(r.Context()), q.Get("node"), "claude_session", map[string]any{
		"path": q.Get("path"),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw)
}

// claudeEvents serves a page of a session's conversation.
//
// Read-only and behind the same identity middleware as everything else on this
// plane, but it widens the read surface knowingly: the transcript store holds
// file contents, memory directories and anything ever pasted into a prompt, for
// every session ever run in that folder, indefinitely. `/api/capture` is bounded
// by what is still in tmux scrollback; this is not.
//
// Paging parameters are passed through rather than validated here — the agent
// owns the bounds, because it is the one that knows the file. Validating in two
// places is how the two disagree.
func (h *humanAPI) claudeEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	args := map[string]any{"path": q.Get("path")}
	for _, k := range []string{"after", "before", "limit", "at"} {
		if v := q.Get(k); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				http.Error(w, k+" must be a number", http.StatusBadRequest)
				return
			}
			args[k] = n
		}
	}
	raw, err := h.hub.Call(r.Context(), tenantOf(r.Context()), q.Get("node"), "claude_events", args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw)
}

// writeReq is the body every pane-write endpoint accepts.
type writeReq struct {
	Node    string   `json:"node"`
	Session string   `json:"session,omitempty"`
	Window  int      `json:"window,omitempty"`
	Text    string   `json:"text,omitempty"`
	Enter   bool     `json:"enter,omitempty"`
	Command string   `json:"command,omitempty"`
	Keys    []string `json:"keys,omitempty"`
	Name    string   `json:"name,omitempty"`
	Path    string   `json:"path,omitempty"`

	// To names a pane instead of addressing it by coordinates: "homelab", an
	// alias, or a full session id. Resolved HERE so that every client — the
	// dashboard, the phone, a voice agent, the CLI — agrees on what a name
	// means. Three clients inventing three fuzzy-match rules is how the same
	// phrase types into the wrong project.
	To string `json:"to,omitempty"`
}

// write returns a handler that proxies one mutating op to its agent and
// records it. Every keystroke that reaches a pane leaves a trace with the
// human who sent it — the flock had no equivalent.
func (h *humanAPI) write(op string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req writeReq
		if !readJSON(w, r, &req) {
			return
		}

		// A name instead of coordinates. Ambiguity is an error listing the
		// candidates, never a best guess — the caller asks again, or the agent
		// asks the human which one they meant.
		if req.To != "" {
			pane, err := h.scope(r.Context()).ResolvePane(r.Context(), req.To, h.now())
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			req.Node, req.Session, req.Window = pane.Agent, pane.TmuxSession, pane.Index
			if req.Name == "" {
				req.Name = pane.Name
			}
		}

		detail := req.Text
		switch op {
		case "command":
			detail = req.Command
		case "keys":
			detail = strings.Join(req.Keys, " ")
		}
		target := req.Node + ":" + req.Session
		if req.Name != "" {
			target = req.Node + ":" + req.Name
		}

		payload := map[string]any{
			"session": req.Session,
			"window":  req.Window,
			"text":    req.Text,
			"enter":   req.Enter,
			"command": req.Command,
			"keys":    req.Keys,
			"name":    req.Name,
			"path":    req.Path,
		}

		_, err := h.hub.Call(r.Context(), tenantOf(r.Context()), req.Node, op, payload)

		entry := AuditEntry{Actor: actor(r.Context()), Action: op, Target: target, Detail: detail}
		if err != nil {
			entry.Detail = detail + " [failed: " + err.Error() + "]"
		}
		// Audited whether or not it succeeded: an attempt is as interesting as
		// a success when reading back what happened.
		h.scope(r.Context()).Audit(r.Context(), entry, h.now())

		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *humanAPI) audit(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	entries, err := h.scope(r.Context()).AuditTail(r.Context(), limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"entries": entries})
}

// messages is the timeline view, and the per-session thread when `session` is
// given. Read-only: looking at mail must never consume it.
// tasks answers the question the waiting queue does not: what did I hand off,
// and where did it get to.
//
// Read-only and on the human plane because the asker is a person away from
// their desk — the same data has existed on the agent plane since tasks
// shipped, which meant a phone could see what was waiting on IT and never what
// it was waiting ON. Requested by a client author who put it better than that:
// the queue answers a different question.
//
// Creating and closing tasks stays off the human plane deliberately: a task is
// a handoff between sessions, and a person driving one from outside would be
// recording work nobody was asked to do.
func (h *humanAPI) tasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 0
	if n, err := strconv.Atoi(q.Get("limit")); err == nil {
		limit = n
	}
	tn := h.hub.store.Tenant(tenantOf(r.Context()))
	list, page, err := tn.TasksPage(r.Context(), TaskQuery{
		Session:     q.Get("session"),
		RequestedBy: q.Get("requested_by"),
		IncludeDone: q.Get("include_done") == "1",
		Limit:       limit,
		After:       q.Get("after"),
		Before:      q.Get("before"),
	})
	if errors.Is(err, ErrCursorExpired) {
		// 410, and it names WHICH END it is handing back. `restart_from` alone
		// is ambiguous: giving a backward-paging client a tail cursor silently
		// reverses its direction, which is a worse failure than the expiry it
		// is recovering from, because it looks like data.
		newest, oldest := tn.taskEnds(r.Context(), q.Get("session"), q.Get("requested_by"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusGone)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": "cursor expired", "detail": err.Error(),
			"newest": newest, "oldest": oldest,
		})
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if list == nil {
		list = []Task{} // an empty list, never null: a client must not have to
		// distinguish "no tasks" from "field absent" at the JSON layer.
	}
	out := map[string]any{"tasks": list, "next": page.Next}
	if page.Clamped != "" {
		out["clamped"] = page.Clamped
	}
	writeJSON(w, out)
}

func (h *humanAPI) messages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))

	var (
		msgs []Envelope
		err  error
	)
	if sess := q.Get("session"); sess != "" {
		msgs, err = h.scope(r.Context()).Conversation(r.Context(), sess, limit)
	} else {
		msgs, err = h.scope(r.Context()).Replay(r.Context(), limit, h.now().Add(-24*time.Hour))
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if msgs == nil {
		msgs = []Envelope{}
	}
	writeJSON(w, map[string]any{"messages": msgs})
}

func (h *humanAPI) sendMessage(w http.ResponseWriter, r *http.Request) {
	var env Envelope
	if !readJSON(w, r, &env) {
		return
	}
	now := h.now()
	env.FromSession = "human:" + actor(r.Context())

	// Same resolution as the agent plane: a person typing a project name into
	// the dashboard should reach that session, and a typo should bounce.
	to, err := h.scope(r.Context()).ResolveSession(r.Context(), env.ToSession, now)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	env.ToSession = to

	id, err := h.scope(r.Context()).Send(r.Context(), env, now)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.scope(r.Context()).Audit(r.Context(), AuditEntry{
		Actor: actor(r.Context()), Action: "message.send",
		Target: env.ToSession, Detail: env.Title,
	}, now)

	writeJSON(w, map[string]any{"id": id})
}

func (h *humanAPI) broadcastMessage(w http.ResponseWriter, r *http.Request) {
	var env Envelope
	if !readJSON(w, r, &env) {
		return
	}
	now := h.now()
	env.FromSession = "human:" + actor(r.Context())

	// Same resolution as the agent plane: a person typing a project name into
	// the dashboard should reach that session, and a typo should bounce.
	to, err := h.scope(r.Context()).ResolveSession(r.Context(), env.ToSession, now)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	env.ToSession = to

	id, n, err := h.scope(r.Context()).Broadcast(r.Context(), env, now)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.scope(r.Context()).Audit(r.Context(), AuditEntry{
		Actor: actor(r.Context()), Action: "message.broadcast",
		Target: env.Topic, Detail: env.Title,
	}, now)

	writeJSON(w, map[string]any{"id": id, "recipients": n})
}

// enrolCode mints a pairing code for a new app install. It is reachable only
// behind the identity middleware, so the code always inherits a tenant that
// the requesting human already belongs to.
func (h *humanAPI) enrolCode(w http.ResponseWriter, r *http.Request) {
	if h.devices == nil {
		http.Error(w, "device enrolment is not enabled", http.StatusNotFound)
		return
	}
	id, _ := IdentityFrom(r.Context())

	// The scope is chosen by the enrolling human, at mint time. A device never
	// asks for its own permissions — that request would be worth exactly as
	// much as the client's honesty.
	var req struct {
		Scope string `json:"scope"`
		Label string `json:"label"`
	}
	if r.ContentLength > 0 {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req)
	}
	scope := ScopeFull
	if req.Scope == ScopeRead {
		scope = ScopeRead
	}
	// A read-only credential must not be able to mint a full-access one, or the
	// restriction is a formality: escalation would be two calls away.
	if id.Scope == ScopeRead {
		scope = ScopeRead
	}

	label := strings.TrimSpace(req.Label)
	code := h.devices.NewNamedEnrolCode(id, scope, label)

	// The label is in the audit row because "who granted access to what" is the
	// question this endpoint's trail has to answer months later.
	h.scope(r.Context()).Audit(r.Context(), AuditEntry{
		Actor: actor(r.Context()), Action: "device.code", Target: label,
		Detail: "scope=" + scopeName(scope),
	}, h.now())

	writeJSON(w, map[string]any{
		"code": code, "expires_in": int(enrolCodeTTL.Seconds()),
		"scope": scope, "label": label,
	})
}

// renewDevice extends the calling device's own credential. There is no device
// id in the body on purpose: a caller may only renew the token it presented, so
// holding one token never becomes a way to keep somebody else's alive.
func (h *humanAPI) renewDevice(w http.ResponseWriter, r *http.Request) {
	if h.devices == nil {
		http.Error(w, "device enrolment is not enabled", http.StatusNotFound)
		return
	}
	id, _ := IdentityFrom(r.Context())

	// Identity.Sub is "device:<id>" only when a device token authenticated this
	// request. Under Access or network-trust there is no credential of ours to
	// extend, and silently succeeding would be a lie.
	deviceID, ok := strings.CutPrefix(id.Sub, "device:")
	if !ok {
		http.Error(w, "this session is not authenticated by a device token", http.StatusBadRequest)
		return
	}

	dev, err := h.devices.Renew(deviceID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	// A browser authenticated by cookie needs the cookie's own expiry moved
	// too, or the credential stays valid server-side while the browser quietly
	// stops sending it — a renewal that appears to work and expires anyway.
	if c, cerr := r.Cookie(TokenCookie); cerr == nil {
		setTokenCookie(w, r, c.Value, dev.Expires)
	}
	h.scope(r.Context()).Audit(r.Context(), AuditEntry{
		Actor: actor(r.Context()), Action: "device.renew", Target: dev.ID,
	}, h.now())

	writeJSON(w, map[string]any{"expires": dev.Expires.Unix(), "label": dev.Label})
}

// setPushToken records where to send this device's notifications.
//
// Authenticated by the device's own token and keyed off the verified identity,
// never off the body: a device id in a request would let any enrolled client
// point another device's notifications at a token it controls.
func (h *humanAPI) setPushToken(w http.ResponseWriter, r *http.Request) {
	if h.devices == nil {
		http.Error(w, "device enrolment is not enabled", http.StatusNotFound)
		return
	}
	id, _ := IdentityFrom(r.Context())
	deviceID, ok := strings.CutPrefix(id.Sub, "device:")
	if !ok {
		http.Error(w, "this session is not authenticated by a device token", http.StatusBadRequest)
		return
	}

	var req struct {
		PushToken string `json:"push_token"`
		Platform  string `json:"platform"` // "ios" — recorded in the audit line only
		// Environment is which APNs gateway minted this token. Apple runs two
		// and their tokens are not interchangeable, so a sender that guesses
		// fails silently for half its devices.
		Environment string `json:"environment"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	// An empty token is a deliberate deregistration, not an error: the app
	// turning notifications off should have a way to say so that is not
	// "revoke my credential".
	dev, err := h.devices.SetPushToken(deviceID, req.PushToken, req.Environment)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	action, detail := "device.push", strings.TrimSpace(req.Platform+" "+dev.PushEnv)
	if dev.PushToken == "" {
		action = "device.push.clear"
	}
	h.scope(r.Context()).Audit(r.Context(), AuditEntry{
		Actor: actor(r.Context()), Action: action, Target: dev.ID, Detail: detail,
	}, h.now())

	writeJSON(w, map[string]any{
		"push": dev.PushToken != "", "label": dev.Label, "environment": dev.PushEnv,
	})
}

func (h *humanAPI) listDevices(w http.ResponseWriter, r *http.Request) {
	if h.devices == nil {
		writeJSON(w, map[string]any{"devices": []deviceView{}})
		return
	}
	id, _ := IdentityFrom(r.Context())
	// Which of these is the caller. Revoking your own credential signs you out
	// mid-click, and a list that cannot point at "you" makes that a surprise
	// rather than a decision.
	selfID, _ := strings.CutPrefix(id.Sub, "device:")

	devs := h.devices.List(tenantOf(r.Context()))
	out := make([]deviceView, 0, len(devs))
	for _, d := range devs {
		out = append(out, deviceView{
			Device: d,
			Push:   d.PushToken != "",
			Self:   selfID != "" && d.ID == selfID,
		})
	}
	// Stable order, for the same reason the node list is sorted: this renders
	// as a list a person reads, and map/insertion order is not an order.
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	writeJSON(w, map[string]any{"devices": out})
}

// deviceView is a device as the API reports it: whether it can be notified,
// never the token that would notify it.
type deviceView struct {
	Device
	Push bool `json:"push"`
	Self bool `json:"self"`
}

func (h *humanAPI) revokeDevice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceID string `json:"device_id"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if h.devices == nil {
		http.Error(w, "device enrolment is not enabled", http.StatusNotFound)
		return
	}
	// Only devices belonging to this tenant may be revoked.
	owned := false
	for _, d := range h.devices.List(tenantOf(r.Context())) {
		if d.ID == req.DeviceID {
			owned = true
			break
		}
	}
	if !owned {
		http.Error(w, "unknown device", http.StatusNotFound)
		return
	}
	h.devices.Revoke(req.DeviceID)
	h.scope(r.Context()).Audit(r.Context(), AuditEntry{
		Actor: actor(r.Context()), Action: "device.revoke", Target: req.DeviceID,
	}, h.now())
	w.WriteHeader(http.StatusNoContent)
}
