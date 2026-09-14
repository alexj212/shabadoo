package hub

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Scoped broadcast: addressing a set of sessions computed at send time from the
// live session list, rather than from the subscriptions table.
//
// The subscriptions table is the older mechanism and it is EMPTY on every
// deployment measured — nothing has ever called session_subscribe — so a topic
// broadcast fans out to zero and reports success. That is this codebase's own
// named failure, a component presenting "reached nobody" as "delivered", and it
// is the first row in the table in CLAUDE.md. A selector fixes it by computing
// recipients from something that is never empty: the sessions the agents are
// reporting right now.
//
// The vocabulary is deliberately small. Each selector answers a question
// somebody actually has — everyone, this machine, this codebase, every core
// session — and an expression language would be a second thing to learn for
// cases nobody has met yet.
type broadcastScope struct {
	sel string
	arg string
}

const (
	scopeAll     = "all"
	scopeNode    = "node"
	scopeProject = "project"
	scopeKind    = "kind"
)

const scopeVocabulary = "want all, node:<name>, project:<prefix>, or kind:<claude|worker|core>"

func parseScope(s string) (broadcastScope, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return broadcastScope{}, fmt.Errorf("broadcast has no scope: %s", scopeVocabulary)
	}
	if strings.EqualFold(s, scopeAll) {
		return broadcastScope{sel: scopeAll}, nil
	}
	sel, arg, ok := strings.Cut(s, ":")
	if !ok {
		return broadcastScope{}, fmt.Errorf("scope %q is not understood: %s", s, scopeVocabulary)
	}
	sel, arg = strings.ToLower(strings.TrimSpace(sel)), strings.TrimSpace(arg)
	switch sel {
	case scopeNode, scopeProject, scopeKind:
	default:
		return broadcastScope{}, fmt.Errorf("scope %q is not understood: %s", s, scopeVocabulary)
	}
	if arg == "" {
		// `node:` names no node. Refused rather than widened to every node,
		// which is the reading a tired selector would take and the one that
		// turns a typo into a fleet fan-out.
		return broadcastScope{}, fmt.Errorf("scope %q names no %s: %s", s, sel, scopeVocabulary)
	}
	return broadcastScope{sel: sel, arg: arg}, nil
}

func (sc broadcastScope) String() string {
	if sc.sel == scopeAll {
		return scopeAll
	}
	return sc.sel + ":" + sc.arg
}

// match is the selector proper: pure, over the fields one session reports.
func (sc broadcastScope) match(s Session) bool {
	switch sc.sel {
	case scopeAll:
		return true
	case scopeNode:
		return strings.EqualFold(s.Agent, sc.arg)
	case scopeProject:
		// Exact, or a path prefix — so `project:shabadoo` reaches a session
		// scoped into `shabadoo/hub`, which is a real arrangement here rather
		// than a hypothetical one.
		//
		// It matches the path-derived project name and never the cwd, which is
		// the same choice ResolveSession makes and for the same reason: every
		// session on a Linux host lives under /home/<user>, so matching the cwd
		// would make common words resolve to everything.
		p, want := strings.ToLower(s.Project), strings.ToLower(sc.arg)
		return p == want || strings.HasPrefix(p, want+"/")
	case scopeKind:
		return strings.EqualFold(s.Kind, sc.arg)
	}
	return false
}

// mayAddressFleet answers who is allowed to reach beyond their own machine.
//
// A selector that reaches all two dozen sessions in one call is the same
// primitive that caused the incident the wake cap exists for: a check-in
// fan-out woke roughly two dozen idle sessions into simultaneous turns and
// consumed the account's usage window. The cap paces NUDGES — and a broadcast
// does not nudge, so the cap cannot help here at all. The bound has to live in
// the addressing or it does not exist.
//
// So a human's own send may address the fleet, and so may a core session: the
// addressable "you" of a machine, already the only thing permitted to start
// sessions there. Every other session is bounded to its own node.
//
// It FAILS CLOSED, which is the opposite of the wake cap and deserves its cost
// written down: a sender this cannot place in the session index is refused, so
// the cost of being wrong is a refusal naming exactly why, recoverable by
// asking a core session. Failing open would cost a fleet fan-out nobody
// authorised, which is the thing being prevented. The asymmetry runs the other
// way from the cap because the cap's failure holds sessions and this one's
// merely declines to widen one message.
func mayAddressFleet(from string, sessions []Session) bool {
	if strings.HasPrefix(from, "human:") {
		return true
	}
	for _, s := range sessions {
		if s.SessionID == from {
			return s.Kind == KindCore
		}
	}
	return false
}

// selectRecipients resolves a scope against the live session list.
//
// The sender is never a recipient: a session does not need its own message, and
// a delivery row for it is one more thing it must drain to reach zero.
//
// A refusal NAMES the sessions outside the sender's node that the scope would
// have reached, rather than quietly dropping them. That is the difference
// between a bound and a lie: a session on wsl naming `project:minutes`, where
// minutes also runs on mac, is told so and can hand the send to a core session.
// It is never told it reached the project when it reached half of it — which is
// the silent-narrowing failure this file exists to avoid reintroducing.
func selectRecipients(sessions []Session, sc broadcastScope, from string) ([]string, error) {
	fleet := mayAddressFleet(from, sessions)

	var home string
	for _, s := range sessions {
		if s.SessionID == from {
			home = s.Agent
			break
		}
	}
	if !fleet && home == "" {
		return nil, fmt.Errorf("sender %q is not in the session index, so a scoped broadcast cannot be bounded to its node; only a core session or a human may broadcast beyond one node", from)
	}

	var to, offNode []string
	for _, s := range sessions {
		if s.SessionID == "" || s.SessionID == from || !sc.match(s) {
			continue
		}
		if !fleet && !strings.EqualFold(s.Agent, home) {
			offNode = append(offNode, s.SessionID)
			continue
		}
		to = append(to, s.SessionID)
	}
	if len(offNode) > 0 {
		sort.Strings(offNode)
		return nil, fmt.Errorf("scope %s reaches %d session(s) on another node (%s); only a core session or a human may address beyond node %s",
			sc, len(offNode), strings.Join(offNode, ", "), home)
	}
	sort.Strings(to)
	return to, nil
}

// broadcastScoped resolves a scope and fans out.
//
// A package-level function rather than a method on either API, because both
// planes call it: who-may-address-whom is decided in exactly one place. Two
// copies of an authority rule drift, and the one that matters is whichever you
// were not reading.
func broadcastScoped(ctx context.Context, tn *Tenant, env Envelope, now time.Time) (string, int, error) {
	sc, err := parseScope(env.Scope)
	if err != nil {
		return "", 0, err
	}
	sessions, err := tn.ListSessions(ctx, now)
	if err != nil {
		return "", 0, err
	}
	to, err := selectRecipients(sessions, sc, env.FromSession)
	if err != nil {
		return "", 0, err
	}
	// The scope is stored where a topic would be: both answer "how was this
	// addressed", and it is what the Mail panel renders beside `acked n/m`.
	env.Topic = sc.String()
	return tn.BroadcastTo(ctx, env, to, now)
}
