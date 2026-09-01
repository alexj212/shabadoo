package hub

// Reaching a project that is not running.
//
// `ResolveSession` lists live sessions, so mail to a project whose session is
// closed bounces — even though it is a real project with history that could be
// started. That is the wrong answer for a system whose premise is handing work
// to whoever owns a domain: closing a session to save resources should not make
// its owner unreachable.
//
// The agent already enumerates startable folders, and each now carries the
// session id it WOULD have. That is what makes this possible without a new
// table or a periodic report: the mail is stored against the prospective id and
// drained when the session starts, which is the same durable-inbox behaviour
// that already lets mail wait for an offline host.
//
// See docs/build-plan.md (Phase 3).

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// stoppedProject is a project that exists on a node but has no session.
type stoppedProject struct {
	Node        string
	Path        string
	Project     string
	SessionID   string
	Deactivated bool
}

// folderView mirrors the agent's Folder, which lives in package main. Only the
// fields routing needs — a fuller mirror would be a second definition to keep
// in step for no benefit.
type folderView struct {
	Path        string `json:"path"`
	Project     string `json:"project"`
	SessionID   string `json:"session_id"`
	Open        bool   `json:"open"`
	Deactivated bool   `json:"deactivated"`
}

// findStoppedProject asks every connected node whether it owns a project by
// this name that is not currently running.
//
// Asked on demand rather than reported periodically. A resolution failure is
// rare — it happens when someone addresses a project whose session is closed —
// and paying for it then is far cheaper than every agent shipping its whole
// folder list every few seconds forever.
//
// The matching rule is the one used everywhere else: exact first, then
// substring, and **ambiguity is refused rather than guessed**. Waking the wrong
// project is worse than waking none, because it also delivers somebody's work
// to the wrong expert.
func (h *Hub) findStoppedProject(ctx context.Context, tenant, want string) (stoppedProject, bool) {
	want = strings.TrimSpace(want)
	if want == "" {
		return stoppedProject{}, false
	}
	lower := strings.ToLower(want)

	var exact, partial []stoppedProject
	for _, node := range h.Online(tenant) {
		// Bounded: this runs inside somebody's send, and a node that has
		// stopped answering must not hold the sender open.
		callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		raw, err := h.Call(callCtx, tenant, node, "folders", nil)
		cancel()
		if err != nil {
			continue // an unreachable node simply has nothing to offer here
		}
		var list []folderView
		if json.Unmarshal(raw, &list) != nil {
			continue
		}
		for _, f := range list {
			if f.Open || f.SessionID == "" || f.Project == "" {
				continue // running projects are ResolveSession's business
			}
			p := stoppedProject{
				Node: node, Path: f.Path, Project: f.Project,
				SessionID: f.SessionID, Deactivated: f.Deactivated,
			}
			switch {
			case strings.EqualFold(f.Project, want):
				exact = append(exact, p)
			case strings.Contains(strings.ToLower(f.Project), lower):
				partial = append(partial, p)
			}
		}
	}

	matches := exact
	if len(matches) == 0 {
		matches = partial
	}
	if len(matches) != 1 {
		return stoppedProject{}, false // none, or ambiguous — both are "no"
	}
	return matches[0], true
}

// askCoreToStart tells a node's core session that work has arrived for a
// project that is not running.
//
// The coordinator does not start it directly, and that is the whole design
// rather than an implementation detail. Only a human or a node's core session
// starts sessions there; a peer may ask. If the coordinator spawned on any
// inbound message, every peer would be able to spend a machine's resources by
// writing to it, with nothing exercising judgment in between.
//
// So the mail is already stored — nothing is lost or waiting on this — and the
// core session decides whether waking is warranted. Slowness here costs
// latency, never a message.
func (h *Hub) askCoreToStart(ctx context.Context, tenant string, p stoppedProject, from string) error {
	core, ok := h.coreSessionOf(ctx, tenant, p.Node)
	if !ok {
		return fmt.Errorf("no core session on %s to ask", p.Node)
	}
	body := fmt.Sprintf(
		"Mail has arrived for %s on this node, which is not running.\n\n"+
			"Path: %s\nFrom: %s\n\n"+
			"The message is already stored and will be delivered when that session starts; "+
			"nothing is lost if you decide it can wait. Start it if the work is worth waking, "+
			"using the open command for that folder.",
		p.Project, p.Path, from)
	if p.Deactivated {
		body += "\n\nNote: this project was closed deliberately, so somebody chose to stop it. " +
			"Weigh that before restarting it."
	}

	_, err := h.store.Tenant(tenant).Send(ctx, Envelope{
		FromSession: "coordinator",
		ToSession:   core,
		Title:       "Work waiting for " + p.Project,
		Body:        body,
		Type:        "warning",
	}, h.now())
	if err == nil {
		h.nudge(ctx, tenant, core, wakeStoppedCore)
	}
	return err
}

// coreSessionOf finds a node's core session id.
func (h *Hub) coreSessionOf(ctx context.Context, tenant, node string) (string, bool) {
	sessions, err := h.store.Tenant(tenant).ListSessions(ctx, h.now())
	if err != nil {
		return "", false
	}
	for _, s := range sessions {
		if s.Agent == node && s.Kind == KindCore {
			return s.SessionID, true
		}
	}
	return "", false
}
