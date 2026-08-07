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
