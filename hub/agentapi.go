package hub

// Messaging on behalf of a session.
//
// The human plane's /api/message/* endpoints act as a person. These act as a
// *session* — they are what the MCP bridge inside each Claude window calls,
// reaching the coordinator through its host's node rather than over the
// network. They replace the NATS subjects one for one:
//
//	claude.inbox.<session>     → POST /agent/message/send
//	claude.broadcast.<topic>   → POST /agent/message/broadcast
//	durable consumer pull      → POST /agent/message/drain
//	CLAUDE_PRESENCE KV         → GET  /agent/peers
//
// Authentication is the agent's bearer token, so every call is already scoped
// to a tenant and a node. A session cannot address another tenant's inbox
// because it cannot name one.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// AgentAPIRoutes registers the session-messaging endpoints on the agent plane.
func (h *Hub) AgentAPIRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /agent/message/send", h.agentSend)
	mux.HandleFunc("POST /agent/message/broadcast", h.agentBroadcast)
	mux.HandleFunc("POST /agent/message/drain", h.agentDrain)
	mux.HandleFunc("POST /agent/subscribe", h.agentSubscribe)
	mux.HandleFunc("POST /agent/unsubscribe", h.agentUnsubscribe)
	mux.HandleFunc("GET /agent/peers", h.agentPeers)
	mux.HandleFunc("POST /agent/status", h.agentStatus)
	mux.HandleFunc("POST /agent/task/create", h.agentTaskCreate)
	mux.HandleFunc("POST /agent/task/update", h.agentTaskUpdate)
	mux.HandleFunc("POST /agent/task/list", h.agentTaskList)
	h.notifyRoutes(mux)
}

// How often one session may send.
//
// This is the loop guard, and it is here BEFORE the change that makes it
// urgent. Today mail is passive: a message waits in an inbox. Once mail can
// start a stopped session, an inbound message causes code to run — a Claude
// session launching with permissions disabled — and A→B→A becomes unbounded
// spend with nothing in the way.
//
// The previous implementation had a recursion guard. It was deleted as dead
// code when agents began dialling out, because the fan-out it protected went
// with it. The hazard did not go away; it changed shape.
//
// # Why a rate limit rather than a hop chain
//
// The plan called for provenance — a chain of the sessions a message passed
// through, refused past a depth. Building it showed why that cannot work here:
// there is no mechanical causal link between a message a session RECEIVES and
// one it later SENDS. Only the sender could supply it, and a guard that depends
// on the thing it is guarding against to declare itself is not a guard.
//
// A rate limit needs no cooperation. It does not identify the cycle, which a
// chain would have, but it bounds the damage, which is what a guard is for.
//
// Generous on purpose: sessions in normal use hand work to each other a few
// times an hour. Anything sustaining one a minute is not collaborating.
const (
	sendRateWindow = time.Hour
	sendRateLimit  = 60
)

var sendLimits = newRateLimiter(sendRateWindow, sendRateLimit)

// guardSendRate reports whether this sender may send, refusing loudly and
// audibly when it may not.
//
// Audited rather than only refused, because a session that has been throttled
// looks exactly like a session that went quiet — and the difference matters at
// the moment somebody is asking why a handoff never arrived. It is the same
// argument that put `message.bounce` in the audit log.
func (h *Hub) guardSendRate(w http.ResponseWriter, r *http.Request, tenant, from string) bool {
	if from == "" {
		from = "unattributed"
	}
	now := h.now()
	if sendLimits.allow(from, now) {
		return true
	}
	h.store.Tenant(tenant).Audit(r.Context(), AuditEntry{
		Actor: "session:" + from, Action: "message.throttled",
		Detail: fmt.Sprintf("more than %d messages in an hour", sendRateLimit),
	}, now)
	http.Error(w, fmt.Sprintf(
		"this session has sent more than %d messages in the last hour", sendRateLimit),
		http.StatusTooManyRequests)
	return false
}

func (h *Hub) agentSend(w http.ResponseWriter, r *http.Request) {
	c, ok := h.authed(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var env Envelope
	if !readJSON(w, r, &env) {
		return
	}
	if !h.guardSendRate(w, r, c.tenant, env.FromSession) {
		return
	}
	now := h.now()
	tn := h.store.Tenant(c.tenant)

	// Resolve the recipient BEFORE storing anything. "homelab" is what an agent
	// would naturally write, and until this existed it was accepted, stored,
	// reported as sent, and drained by nobody.
	to, err := tn.ResolveSession(r.Context(), env.ToSession, now)
	if err != nil {
		// Before bouncing: is this a real project that simply is not running?
		// Closing a session to save resources must not make its owner
		// unreachable, which is what bouncing here amounted to.
		if p, found := h.findStoppedProject(r.Context(), c.tenant, env.ToSession); found {
			env.ToSession = p.SessionID
			id, serr := tn.Send(r.Context(), env, now)
			if serr != nil {
				http.Error(w, serr.Error(), http.StatusBadRequest)
				return
			}
			// Stored first, asked second. The message is safe whatever the core
			// session decides or how long it takes; a failure to ask is a
			// latency problem, never a lost handoff.
			askErr := h.askCoreToStart(r.Context(), c.tenant, p, env.FromSession)
			tn.Audit(r.Context(), AuditEntry{
				Actor: "session:" + env.FromSession, Action: "message.deferred",
				Target: p.Project,
				Detail: fmt.Sprintf("not running on %s; %s", p.Node,
					map[bool]string{true: "core session asked to start it",
						false: "no core session to ask"}[askErr == nil]),
			}, now)
			writeJSON(w, map[string]any{
				"id": id, "to_session": p.SessionID, "deferred": true, "node": p.Node,
			})
			return
		}

		// A bounce is recorded, because until it was, a failed handoff existed
		// only in the sender's own context: the recipient never learned anyone
		// had tried to reach it, and nothing an operator can read said so
		// either. Diagnosing one afterwards meant asking the sender what it
		// remembered — which is exactly as reliable as it sounds.
		//
		// It is audited rather than logged so it lands beside the sends that
		// worked: "did that reach homelab?" is answered by one list either way.
		tn.Audit(r.Context(), AuditEntry{
			Actor: "session:" + env.FromSession, Action: "message.bounce",
			Target: env.ToSession, Detail: err.Error(),
		}, now)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	env.ToSession = to

	id, err := tn.Send(r.Context(), env, now)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Wake the recipient if its session is on a connected agent. This is the
	// nudge, and it lands immediately rather than up to 15 minutes later like
	// the cron that preceded it.
	h.nudge(r.Context(), c.tenant, env.ToSession)

	// Report what it resolved to. A sender that wrote "homelab" should be able
	// to see which session actually received it, and to notice if that was not
	// the one it meant.
	writeJSON(w, map[string]any{"id": id, "to_session": to})
}

func (h *Hub) agentBroadcast(w http.ResponseWriter, r *http.Request) {
	c, ok := h.authed(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var env Envelope
	if !readJSON(w, r, &env) {
		return
	}
	// A broadcast is one send however many subscribers it reaches: the limit
	// bounds how often a session speaks, not how many hear it.
	if !h.guardSendRate(w, r, c.tenant, env.FromSession) {
		return
	}
	id, n, err := h.store.Tenant(c.tenant).Broadcast(r.Context(), env, h.now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"id": id, "recipients": n})
}

// agentDrain is the durable-consumer pull: it returns a session's undelivered
// mail and marks it delivered, in one transaction.
func (h *Hub) agentDrain(w http.ResponseWriter, r *http.Request) {
	c, ok := h.authed(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		Session string `json:"session"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Session == "" {
		http.Error(w, "session required", http.StatusBadRequest)
		return
	}
	msgs, err := h.store.Tenant(c.tenant).Drain(r.Context(), req.Session, h.now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if msgs == nil {
		msgs = []Envelope{}
	}
	writeJSON(w, map[string]any{"messages": msgs})
}

func (h *Hub) agentSubscribe(w http.ResponseWriter, r *http.Request) {
	h.subscription(w, r, true)
}

func (h *Hub) agentUnsubscribe(w http.ResponseWriter, r *http.Request) {
	h.subscription(w, r, false)
}

func (h *Hub) subscription(w http.ResponseWriter, r *http.Request, add bool) {
	c, ok := h.authed(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		Session string `json:"session"`
		Topic   string `json:"topic"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Session == "" || req.Topic == "" {
		http.Error(w, "session and topic required", http.StatusBadRequest)
		return
	}
	tn := h.store.Tenant(c.tenant)
	var err error
	if add {
		err = tn.Subscribe(r.Context(), req.Session, req.Topic)
	} else {
		err = tn.Unsubscribe(r.Context(), req.Session, req.Topic)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// agentStatus records what a session says it is doing.
//
// On the agent plane and not the human one because the author is a SESSION,
// not a person — the same distinction as message/send. It is the piece that
// makes several sessions legible as one system: "online" is the agent's
// answer, and it cannot tell you that homelab is waiting on iptv.
//
// This existed in mcp-natsbridge as session_status_set and was dropped in the
// migration. Nothing noticed, because nothing rendered it either.
func (h *Hub) agentStatus(w http.ResponseWriter, r *http.Request) {
	c, ok := h.authed(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		Session string `json:"session"`
		Status  string `json:"status"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Session == "" {
		http.Error(w, "session required", http.StatusBadRequest)
		return
	}
	if err := h.store.Tenant(c.tenant).SetSessionStatus(
		r.Context(), req.Session, req.Status, h.now()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Watching browsers should see it now, not on the next agent report: the
	// point of a status is that a peer reads it while it is true.
	h.SessionsChanged(c.tenant)
	w.WriteHeader(http.StatusNoContent)
}

// agentPeers replaces the presence KV: every session in this tenant, with its
// undrained mail count and whether its agent is currently connected.
func (h *Hub) agentPeers(w http.ResponseWriter, r *http.Request) {
	c, ok := h.authed(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	sessions, err := h.store.Tenant(c.tenant).ListSessions(r.Context(), h.now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	online := map[string]bool{}
	for _, n := range h.Online(c.tenant) {
		online[n] = true
	}
	type peerView struct {
		Session
		Online bool `json:"online"`
	}
	out := make([]peerView, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, peerView{Session: s, Online: online[s.Agent]})
	}
	writeJSON(w, map[string]any{"peers": out})
}

// nudge wakes a session so its inbox-drain hook fires on the next turn.
//
// Best effort by design: an offline session's mail waits in the store, and a
// busy one is left alone rather than having a prompt injected mid-thought.
// This replaces the 15-minute cron that read the presence KV.
func (h *Hub) nudge(ctx context.Context, tenant, sessionID string) {
	if sessionID == "" {
		return
	}
	sessions, err := h.store.Tenant(tenant).ListSessions(ctx, h.now())
	if err != nil {
		return
	}
	for _, s := range sessions {
		if s.SessionID != sessionID {
			continue
		}
		if !h.IsOnline(tenant, s.Agent) {
			return // offline: the delivery row is the wait
		}
		go func(agent, tmuxSession string, window int) {
			// Detached from the request: the sender must not block on the
			// recipient's tmux server.
			c, cancel := context.WithTimeout(context.WithoutCancel(ctx), callTimeout)
			defer cancel()
			h.Call(c, tenant, agent, "deliver", map[string]any{
				"session": tmuxSession,
				"window":  window,
			})
		}(s.Agent, s.TmuxSession, s.Index)
		return
	}
}

// ---------------------------------------------------------------------------
// notify: the outbound human-notification relay
// ---------------------------------------------------------------------------

// AppriseURL is where notifications are POSTed, e.g.
// http://apprise:8000/notify/homelab. Empty disables the endpoint entirely
// rather than failing per-call, so a deployment without a notifier says so once
// at startup instead of surprising a session mid-task.
//
// It lives on the COORDINATOR, not on each agent: the credentials and the
// routing config are one thing in one place, and a node that could notify
// directly would need the URL — and therefore the ability to spam it — on every
// host.
var AppriseURL string

// appriseTimeout bounds the outbound call. A notifier that hangs must not hold
// a session's tool call open.
const appriseTimeout = 10 * time.Second

// notifyRoutes registers the relay when a notifier is configured.
func (h *Hub) notifyRoutes(mux *http.ServeMux) {
	if AppriseURL == "" {
		return
	}
	mux.HandleFunc("POST /agent/notify", func(w http.ResponseWriter, r *http.Request) {
		c, ok := h.authed(r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req struct {
			Title string `json:"title"`
			Body  string `json:"body"`
			Tag   string `json:"tag"`
			Type  string `json:"type"`
		}
		if !readJSON(w, r, &req) {
			return
		}
		if strings.TrimSpace(req.Body) == "" {
			http.Error(w, "body is required", http.StatusBadRequest)
			return
		}
		if req.Tag == "" {
			req.Tag = "all"
		}
		if req.Type == "" {
			req.Type = "info"
		}

		// Say which session asked. A notification that arrives on a phone with
		// no idea which machine or project produced it is the kind that gets
		// muted, and then the next real one is missed.
		title := req.Title
		if title == "" {
			title = "shabadoo"
		}
		body := req.Body + "\n\n— " + c.node

		if err := postApprise(r.Context(), title, body, req.Tag, req.Type); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		h.store.Tenant(c.tenant).Audit(r.Context(), AuditEntry{
			Actor: "agent:" + c.node, Action: "notify", Target: req.Tag, Detail: title,
		}, h.now())

		writeJSON(w, map[string]any{"sent": true, "tag": req.Tag})
	})
}

// postApprise delivers one notification.
//
// Shared by the session-facing relay above and the blocked-session watcher, so
// both reach a phone by the same route and a deployment configures one URL
// rather than one per producer.
// appriseAllTag reaches every configured destination. Apprise treats tags as a
// filter, so this is both the default and the fallback when a caller's tag
// matches nothing.
const appriseAllTag = "all"

func postApprise(ctx context.Context, title, body, tag, typ string) error {
	if AppriseURL == "" {
		return fmt.Errorf("no notifier is configured")
	}
	if tag == "" {
		tag = appriseAllTag
	}
	if typ == "" {
		typ = "info"
	}
	payload, _ := json.Marshal(map[string]string{
		"title": title, "body": body, "tag": tag, "type": typ,
	})

	ctx, cancel := context.WithTimeout(ctx, appriseTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, AppriseURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: appriseTimeout}).Do(req)
	if err != nil {
		return fmt.Errorf("notifier unreachable: %w", err)
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))

	if resp.StatusCode == http.StatusFailedDependency && tag != appriseAllTag {
		// 424 is Apprise for "no destination matched that tag". The tag is a
		// ROUTING filter, not a label — an unrecognised one delivers to nobody.
		//
		// Callers reasonably read it as a label and pass something descriptive,
		// and the result was a notification silently reaching no one. Losing the
		// message over a naming mistake is the wrong trade for something whose
		// entire job is reaching a human, so it is retried unfiltered and the
		// mistake is logged rather than charged to the reader.
		log.Printf("hub: notification tag %q matched no destination; resending to all", tag)
		return postApprise(ctx, title, body, appriseAllTag, typ)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("notifier returned %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

// Delegated work, on the session plane because the party handing something over
// is a session rather than a person.
//
// Creating a task also SENDS the brief. Two calls for one act would let them
// drift — a task nobody was told about, or a message with no record — and the
// pair that drifts is the one that matters: work handed over with nothing
// tracking it is exactly the situation this exists to remove.
func (h *Hub) agentTaskCreate(w http.ResponseWriter, r *http.Request) {
	c, ok := h.authed(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		From   string `json:"from"`
		To     string `json:"to"`
		Brief  string `json:"brief"`
		Thread string `json:"thread"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if !h.guardSendRate(w, r, c.tenant, req.From) {
		return
	}
	now := h.now()
	tn := h.store.Tenant(c.tenant)

	to, err := tn.ResolveSession(r.Context(), req.To, now)
	if err != nil {
		tn.Audit(r.Context(), AuditEntry{
			Actor: "session:" + req.From, Action: "task.bounce",
			Target: req.To, Detail: err.Error(),
		}, now)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	task, err := tn.CreateTask(r.Context(), Task{
		SessionID: to, RequestedBy: req.From, Thread: req.Thread, Brief: req.Brief,
	}, now)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// The brief travels as mail so it lands in the assignee's context the same
	// way everything else does. The task id is in the body because the assignee
	// needs it to report back, and asking it to go and look one up would be a
	// step it can skip.
	if _, err := tn.Send(r.Context(), Envelope{
		FromSession: req.From, ToSession: to,
		Title: "Task: " + firstLineOf(req.Brief),
		Body: req.Brief + "\n\n---\nTask " + task.ID + ". Report progress with " +
			"task_update — active when you pick it up, blocked with a reason if you " +
			"stall, done or dropped when it ends. Something is chasing this, so a " +
			"task left silent will be asked about.",
		Type: "info",
	}, now); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.nudge(r.Context(), c.tenant, to)

	tn.Audit(r.Context(), AuditEntry{
		Actor: "session:" + req.From, Action: "task.create", Target: to,
		Detail: firstLineOf(req.Brief),
	}, now)
	writeJSON(w, task)
}

func (h *Hub) agentTaskUpdate(w http.ResponseWriter, r *http.Request) {
	c, ok := h.authed(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		ID    string `json:"id"`
		State string `json:"state"`
		Note  string `json:"note"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	now := h.now()
	tn := h.store.Tenant(c.tenant)

	before, _ := tn.Task(r.Context(), req.ID)
	task, err := tn.UpdateTask(r.Context(), req.ID, req.State, req.Note, now)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Tell whoever asked, when it ends. They delegated and moved on; without
	// this they would have to poll, which is the habit the task list exists to
	// make unnecessary.
	if terminalTask(task.State) && !terminalTask(before.State) && task.RequestedBy != "" {
		body := "Task " + task.ID + " is " + task.State + ".\n\n" + task.Brief
		if task.Note != "" {
			body += "\n\n" + task.Note
		}
		if _, err := tn.Send(r.Context(), Envelope{
			FromSession: task.SessionID, ToSession: task.RequestedBy,
			Title: "Task " + task.State + ": " + firstLineOf(task.Brief),
			Body:  body, Type: map[bool]string{true: "success", false: "warning"}[task.State == TaskDone],
		}, now); err == nil {
			h.nudge(r.Context(), c.tenant, task.RequestedBy)
		}
	}
	writeJSON(w, task)
}

func (h *Hub) agentTaskList(w http.ResponseWriter, r *http.Request) {
	c, ok := h.authed(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		Session     string `json:"session"`
		RequestedBy string `json:"requested_by"`
		IncludeDone bool   `json:"include_done"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	tasks, err := h.store.Tenant(c.tenant).Tasks(r.Context(), req.Session, req.RequestedBy, req.IncludeDone, 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"tasks": tasks})
}

// firstLineOf is a brief's headline, for titles and audit lines.
func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return truncate(strings.TrimSpace(s), 80)
}
