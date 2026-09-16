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
			switch e, pa := stoppedMatch(f, want); {
			case e:
				exact = append(exact, p)
			case pa:
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
// It carries the message's SUBJECT, not just its sender. The core session is
// being asked to make a judgment — is this worth waking a machine for — and the
// warning used to ship only who sent it. A core session that cannot read
// another session's inbox then has two options, guess or wake it to find out.
// Measured: one guessed from board state, guessed wrong, and spent three of its
// own turns and a peer's turn asking the sender what it had queued — to
// establish a fact the coordinator was holding at the moment it warned.
//
// Withholding the subject protects nothing, which is the part worth checking
// rather than assuming: `Replay` selects `m.title` across the whole tenant and
// the dashboard's Mail panel renders it, so a subject line already crosses
// project lines by design. This is the machine's own core session, on the node
// the mail is addressed to.
//
// The BODY is still withheld. A subject is what a decision needs; a body is the
// work itself, and inlining it would deliver the handoff to a session that has
// not agreed to take it.
func (h *Hub) askCoreToStart(ctx context.Context, tenant string, p stoppedProject, env Envelope) error {
	core, ok := h.coreSessionOf(ctx, tenant, p.Node)
	if !ok {
		return fmt.Errorf("no core session on %s to ask", p.Node)
	}
	// An untitled message SAYS it is untitled. Rendering a blank line there
	// would read as "the coordinator did not tell me", which is the empty-versus-
	// unknown failure this codebase keeps paying for, arriving in a warning whose
	// whole purpose is to inform a decision.
	subject := env.Title
	if strings.TrimSpace(subject) == "" {
		subject = "(the sender set no title)"
	}
	body := fmt.Sprintf(
		"Mail has arrived for %s on this node, which is not running.\n\n"+
			"Subject: %s\nPath: %s\nFrom: %s\n",
		p.Project, subject, p.Path, env.FromSession)
	// Type and tag only when set: absence genuinely means the sender chose none,
	// so there is no unknown to distinguish here.
	if env.Type != "" {
		body += "Type: " + env.Type + "\n"
	}
	if env.Tag != "" {
		body += "Tag: " + env.Tag + "\n"
	}
	// THREE moves, not two. This warning used to end at "start it, or leave it",
	// and a core session reading it reasonably concluded those were the only
	// options — to the point of writing itself a rule that asking the SENDER was
	// the only way to learn what was queued without starting a machine.
	//
	// It is not: the queue is readable, drains nothing and wakes nothing, and
	// the coordinator is holding the session id at the very moment it warns. So
	// the id is substituted here rather than described, because a reader who has
	// to go and find it will decide on the subject line instead.
	//
	// Reading and asking are still different jobs and both are offered. Twice in
	// twelve hours, asking a sender made the sender change their mind — one split
	// a message and re-routed the urgent half, another withdrew theirs and filed
	// a mission row instead. Neither outcome exists anywhere in the queue's text.
	body += "\n" + h.queuedLine(ctx, tenant, p) +
		"\n\nThe message is already stored and will be delivered when that session starts; " +
		"nothing is lost if you decide it can wait.\n\n" +
		"Read the whole queue WITHOUT starting it:\n" +
		"  shabadoo mail --session " + p.SessionID + "\n" +
		"That drains nothing and wakes nothing. You can also ask the sender what they " +
		"need — senders often revise or withdraw once asked.\n\n" +
		"Start it if the work is worth waking, using the open command for that folder."
	if p.Deactivated {
		body += "\n\nNote: this project was closed deliberately, so somebody chose to stop it. " +
			"Weigh that before restarting it."
	}

	_, err := h.store.Tenant(tenant).Send(ctx, Envelope{
		FromSession: "coordinator",
		ToSession:   core,
		Title:       h.warnTitle(ctx, tenant, p),
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

// queuedLine says how much is waiting, so a second warning is distinguishable
// from a repeat of the first.
//
// Two warnings for one project arrived ninety seconds apart and were byte
// identical, so the reader could not tell a retry from a follow-up and reported
// that to a human as an unknown. A count separates them.
//
// A failed count is reported as UNKNOWN rather than omitted or rendered as
// zero: "nothing else is waiting" and "I could not look" lead to opposite
// decisions, and the whole point of this message is to inform one.
func (h *Hub) queuedLine(ctx context.Context, tenant string, p stoppedProject) string {
	n, err := h.store.Tenant(tenant).Pending(ctx, p.SessionID, h.now())
	switch {
	case err != nil:
		return "Queued for it: could not be counted — treat this as unknown rather than as one."
	case n <= 1:
		return "Queued for it: this message, and nothing else."
	default:
		return fmt.Sprintf("Queued for it: %d messages including this one.", n)
	}
}

// warnTitle puts the count in the subject line too, because that is the part a
// reader sees before deciding whether to open anything.
func (h *Hub) warnTitle(ctx context.Context, tenant string, p stoppedProject) string {
	base := "Work waiting for " + p.Project
	if n, err := h.store.Tenant(tenant).Pending(ctx, p.SessionID, h.now()); err == nil && n > 1 {
		return fmt.Sprintf("%s (%d queued)", base, n)
	}
	return base
}

// stoppedMatch decides whether a folder answers to a recipient name.
//
// Split out because the defect it fixes is a DIRECTION error, invisible at a
// glance and silent in effect. The substring arm asked whether the PROJECT NAME
// contains the thing being looked up — right for a human typing `dev`, and
// backwards for a session id, because "devops" does not contain
// "claude-devops-wsl-0bcb99a1".
//
// A task's requester is always a concrete session id, so every task-completion
// notice addressed to a stopped project matched nothing here. The completion was
// stored correctly against the id it would have, and nobody was ever told. Found
// by a core session doing arithmetic on queue counts it could not reconcile: two
// messages arrived, one warning appeared, and the missing one was a task notice.
func stoppedMatch(f folderView, want string) (exact, partial bool) {
	if want == "" {
		return false, false
	}
	// A session id is the most precise thing a caller can hand over, so it wins
	// outright rather than competing with names that merely resemble it.
	if f.SessionID != "" && strings.EqualFold(f.SessionID, want) {
		return true, false
	}
	if f.Project == "" {
		return false, false
	}
	if strings.EqualFold(f.Project, want) {
		return true, false
	}
	return false, strings.Contains(strings.ToLower(f.Project), strings.ToLower(want))
}
