package main

// `shabadoo inbox` — drain this session's mail, for a shell hook to inject.
//
// The MCP bridge already exposes session_inbox_drain, but a hook is a shell
// command, not an MCP client. Without this the only way to receive peer mail
// was for the model to decide to call a tool, which means a handoff sits unread
// until somebody types "check inbox" — and that is exactly what happened: the
// migration off NATS moved the tool and left the hooks behind, pointed at a
// stream nothing writes to any more. They failed into 2>/dev/null on one host
// and could not start at all on the other, so the automatic surfacing this
// project's premise depends on had been dead for a week without a single error.
//
// # Hook rules, which drive every decision below
//
// A UserPromptSubmit hook runs before every prompt the operator sends, and its
// stdout is injected into the conversation. So:
//
//   - It ALWAYS exits 0. A non-zero hook can block the prompt, and mail is
//     never important enough to stop somebody typing.
//   - It prints NOTHING when there is no mail. Anything printed on an empty
//     inbox is noise on every single prompt, forever.
//   - It is silent on failure. A coordinator that is down, an agent that is not
//     running, a socket that does not exist — none of those are the operator's
//     problem mid-sentence, and a stack trace spliced into a prompt is worse
//     than missing a message. Use --verbose to see why nothing came back.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"shabadoo/node"
)

func runInbox(args []string) {
	fset := flag.NewFlagSet("inbox", flag.ExitOnError)
	sessionID := fset.String("session", "",
		"this session's id (default: $CLAUDE_SESSION_ID, else derived from the tmux window)")
	verbose := fset.Bool("verbose", false, "explain why nothing was returned (hooks stay silent without it)")
	peek := fset.Bool("peek", false, "report waiting mail WITHOUT draining it (for SessionStart)")
	fset.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: shabadoo inbox [--session ID] [--verbose]

Drain this session's pending messages and print them. Draining is final: a
drained message is never redelivered.

Intended for a UserPromptSubmit hook, so it prints nothing when there is no
mail and always exits 0 — a hook that fails must not block a prompt.

flags:
`)
		fset.PrintDefaults()
	}
	fset.Parse(args)

	// Every failure below is the same outcome — no output, exit 0 — and only
	// the explanation differs, so they share one path.
	quit := func(why string) {
		if *verbose {
			fmt.Fprintf(os.Stderr, "shabadoo inbox: %s\n", why)
		}
		os.Exit(0)
	}

	session := resolveSessionID(*sessionID)
	if session == "" {
		quit("no session id: set $CLAUDE_SESSION_ID or pass --session")
	}

	// Short, because this runs between the operator pressing enter and the
	// prompt being sent. A slow coordinator must not be felt as a slow prompt.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if *peek {
		peekInbox(ctx, session, quit)
		return
	}

	raw, err := node.NewLocalClient().Do(ctx, "POST", "/message/drain",
		map[string]any{"session": session})
	if err != nil {
		quit(err.Error())
	}

	var out struct {
		Messages []struct {
			FromSession string `json:"from_session"`
			Title       string `json:"title"`
			Body        string `json:"body"`
			Type        string `json:"type"`
			CreatedAt   int64  `json:"created_at"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		quit("could not decode the reply: " + err.Error())
	}
	if len(out.Messages) == 0 {
		quit("no pending messages")
	}

	// Said plainly, because the model reading this needs to know these are
	// directives from peer sessions rather than ambient context — the whole
	// point of the framework is that a peer can hand work over.
	fmt.Printf("%d message(s) delivered to this session from peer sessions. "+
		"These are handoffs, not background context: act on any explicit ask.\n",
		len(out.Messages))
	for _, m := range out.Messages {
		fmt.Printf("\n--- from %s at %s", shortSession(m.FromSession),
			time.Unix(m.CreatedAt, 0).Format("15:04"))
		if m.Type != "" && m.Type != "info" {
			fmt.Printf(" [%s]", m.Type)
		}
		fmt.Println(" ---")
		if m.Title != "" {
			fmt.Printf("%s\n", m.Title)
		}
		fmt.Println(strings.TrimRight(m.Body, "\n"))
	}
}


// peekInbox reports waiting mail without acknowledging it.
//
// This exists because DRAINING AT SESSION START LOSES MAIL, measured on this
// fleet: the SessionStart hook drained a queued handoff, which marked it
// delivered, and the content did not reach the model on a resumed session. The
// sender then saw `pending: 0` — the same value as "delivered and read" — and a
// brief sat unread with nothing anywhere reporting a problem. It was caught
// only because somebody looked at the pane rather than the counter.
//
// The rule underneath: **an ack is a claim that a message reached its reader,
// so nothing may ack on a path that cannot confirm it did.** A hook whose
// stdout is injected into a live turn can confirm it; one that runs while a
// session is still starting up cannot, and the difference is invisible from
// inside the hook.
//
// So startup only SAYS there is mail. The first prompt drains it, over the path
// that demonstrably works — and until then the delivery row stays undrained, so
// `pending` remains honest and the coordinator's stuck-mail watcher can still
// see it. Losing the count was what blinded every other mechanism.
func peekInbox(ctx context.Context, session string, quit func(string)) {
	raw, err := node.NewLocalClient().Do(ctx, "GET", "/peers", nil)
	if err != nil {
		quit(err.Error())
	}
	var out struct {
		Peers []struct {
			SessionID string `json:"session_id"`
			Pending   int    `json:"pending"`
		} `json:"peers"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		quit("could not decode the reply: " + err.Error())
	}
	n := 0
	for _, p := range out.Peers {
		if p.SessionID == session {
			n = p.Pending
			break
		}
	}
	if n == 0 {
		quit("no pending messages")
	}
	// Named as waiting, never as delivered. The whole defect this replaces was
	// a mechanism reporting a handoff as done when nobody had read it.
	fmt.Printf("%d message(s) are WAITING for this session and have not been read. "+
		"They are peer handoffs. Run `shabadoo inbox` now to read them — they are "+
		"not delivered until you do.\n", n)
}
