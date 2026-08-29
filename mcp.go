package main

// `shabadoo mcp` — the MCP server each Claude session launches.
//
// This is the mcp-natsbridge replacement, folded into the binary rather than
// kept as a second program. Three reasons, in order of how often they bite:
//
//   - Distribution. `setup` already puts this binary on every machine, so the
//     MCP server exists wherever a node does. A separate artefact is a second
//     thing to ship, version and forget to update.
//   - Version skew. hub, node and bridge as three independently-versioned
//     programs speaking one protocol is exactly the hazard the build stamps
//     were added for. Folded in, node and bridge cannot disagree.
//   - Credentials. It talks to its local agent over a unix socket and inherits
//     that agent's authenticated session, so no session needs a credential of
//     its own — today every session holds NATS credentials.
//
// MCP over stdio is JSON-RPC 2.0, and only three methods are needed:
// initialize, tools/list, tools/call. That is small enough to hand-write, which
// keeps the stdlib-only rule intact.
//
// IMPORTANT: stdout carries the protocol. Anything printed there that is not a
// JSON-RPC message corrupts the stream and the client sees a parse error rather
// than whatever went wrong. All diagnostics go to stderr.

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"shabadoo/node"
	"shabadoo/tmux"
)

const mcpProtocolVersion = "2024-11-05"

// rpcRequest is one JSON-RPC 2.0 call. `id` is absent on a notification, which
// must not be answered — replying to one is a protocol error that some clients
// treat as fatal.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// mcpTool is one advertised tool.
type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func runMCP(args []string) {
	fset := flag.NewFlagSet("mcp", flag.ExitOnError)
	sessionID := fset.String("session", "", "this session's id (default: $CLAUDE_SESSION_ID, else derived from the tmux window)")
	fset.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: shabadoo mcp

Serves MCP over stdio, for a Claude session to talk to its peers. Launched by
the Claude client, not by hand:

  claude mcp add shabadoo -- shabadoo mcp

Reaches the coordinator through this host's agent over a unix socket, so the
session needs no credential of its own.

flags:
`)
		fset.PrintDefaults()
	}
	fset.Parse(args)

	// Leave a note saying what surface this child serves, so the agent can tell
	// a session holding a stale tool list from one that is current — without
	// inferring it from a clock. Best effort: a child that cannot write one
	// reports as unknown, which is the honest answer.
	recordToolSurface(defaultStateDir())

	s := &mcpServer{
		local:   node.NewLocalClient(),
		session: resolveSessionID(*sessionID),
	}
	if err := s.serve(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "shabadoo mcp: %v\n", err)
		os.Exit(1)
	}
}

// resolveSessionID works out which session this is.
//
// $CLAUDE_SESSION_ID is what the launcher exports, and is authoritative. The
// tmux fallback exists because a session started by hand still deserves to
// work, and matching what the agent reports keeps one session from appearing
// under two names.
func resolveSessionID(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if v := os.Getenv("CLAUDE_SESSION_ID"); v != "" {
		return v
	}
	if name := os.Getenv("TMUX_PANE"); name != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if windows, err := tmux.Windows(ctx); err == nil {
			for _, w := range windows {
				if fmt.Sprintf("%%%d", w.PID) == name {
					return "claude-" + w.Name
				}
			}
		}
	}
	return ""
}

type mcpServer struct {
	local   *node.LocalClient
	session string
}

func (s *mcpServer) serve(in *os.File, out *os.File) error {
	// A single message can carry a long body; the default 64KB scanner buffer
	// would silently truncate one and produce a parse error attributed to the
	// sender rather than to the reader.
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	enc := json.NewEncoder(out)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue // not addressed to anything we can answer
		}

		result, rpcErr := s.dispatch(req)

		// A notification has no id and must not be answered.
		if len(req.ID) == 0 {
			continue
		}
		resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
		if rpcErr != nil {
			resp.Error = rpcErr
		} else {
			resp.Result = result
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return sc.Err()
}

func (s *mcpServer) dispatch(req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "shabadoo", "version": version},
		}, nil

	case "tools/list":
		return map[string]any{"tools": mcpTools()}, nil

	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{Code: -32602, Message: err.Error()}
		}
		return s.callTool(p.Name, p.Arguments), nil

	case "ping":
		return map[string]any{}, nil
	}
	return nil, &rpcError{Code: -32601, Message: "unknown method: " + req.Method}
}

// toolResult renders a tool's outcome in MCP's shape.
//
// A FAILED tool call is reported as content with isError, not as a JSON-RPC
// error: a transport error means the server is broken, while "the coordinator
// is unreachable" is an answer the model should see and reason about. Conflating
// them makes an offline coordinator look like a crash.
func toolResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
}

func (s *mcpServer) callTool(name string, rawArgs json.RawMessage) map[string]any {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	args := map[string]any{}
	if len(rawArgs) > 0 {
		_ = json.Unmarshal(rawArgs, &args)
	}
	str := func(k string) string {
		v, _ := args[k].(string)
		return strings.TrimSpace(v)
	}

	switch name {
	case "session_whoami":
		out, err := s.local.Do(ctx, "GET", "/whoami", nil)
		if err != nil {
			return toolResult(err.Error(), true)
		}
		return toolResult(fmt.Sprintf("session %s\n%s", s.sessionOrUnknown(), out), false)

	case "session_list":
		out, err := s.local.Do(ctx, "GET", "/peers", nil)
		if err != nil {
			// Degrade rather than fail: the local tmux windows are still a
			// truthful answer to "what is running", and a session whose tooling
			// dies whenever the coordinator does is worse than one that says
			// less.
			return toolResult(s.localSessionsFallback(ctx, err), false)
		}
		return toolResult(string(out), false)

	case "session_send":
		to := str("to")
		if to == "" {
			return toolResult("`to` is required: the session id or alias to send to", true)
		}
		return s.relay(ctx, "/message/send", map[string]any{
			"to_session":   to,
			"title":        str("title"),
			"body":         str("body"),
			"type":         str("type"),
			"tag":          str("tag"),
			"from_session": s.session,
		})

	case "session_broadcast":
		topic := str("topic")
		if topic == "" {
			return toolResult("`topic` is required", true)
		}
		return s.relay(ctx, "/message/broadcast", map[string]any{
			"topic":        topic,
			"title":        str("title"),
			"body":         str("body"),
			"from_session": s.session,
		})

	case "session_inbox_drain":
		if s.session == "" {
			return toolResult("this session has no id, so it has no inbox: "+
				"set $CLAUDE_SESSION_ID or pass --session", true)
		}
		return s.relay(ctx, "/message/drain", map[string]any{"session": s.session})

	case "notify_send":
		body := str("body")
		if body == "" {
			return toolResult("`body` is required: what the human should be told", true)
		}
		return s.relay(ctx, "/notify", map[string]any{
			"title": str("title"),
			"body":  body,
			"tag":   str("tag"),
			"type":  str("type"),
		})

	case "task_create":
		if s.session == "" {
			return toolResult("this session has no id, so it cannot hand work over", true)
		}
		return s.relay(ctx, "/task/create", map[string]any{
			"from": s.session, "to": str("to"), "brief": str("brief"), "thread": str("thread"),
		})

	case "task_update":
		return s.relay(ctx, "/task/update", map[string]any{
			"id": str("id"), "state": str("state"), "note": str("note"),
		})

	case "task_list":
		// Default to this session's own outstanding work. "What is on my plate"
		// is the common question; "what did I hand out" needs asking for.
		q := map[string]any{"include_done": args["include_done"] == true}
		switch {
		case str("requested_by") != "":
			q["requested_by"] = str("requested_by")
		case str("session") != "":
			q["session"] = str("session")
		case args["all"] == true:
			// everything in the tenant
		default:
			q["session"] = s.session
		}
		return s.relay(ctx, "/task/list", q)

	case "session_status_set":
		// An empty status is a legitimate call, not a missing argument: it is
		// how a session says it has finished rather than stopped.
		return s.relay(ctx, "/status", map[string]any{
			"session": s.session, "status": str("status"),
		})

	case "session_subscribe", "session_unsubscribe":
		topic := str("topic")
		if topic == "" {
			return toolResult("`topic` is required", true)
		}
		path := "/subscribe"
		if name == "session_unsubscribe" {
			path = "/unsubscribe"
		}
		return s.relay(ctx, path, map[string]any{"session": s.session, "topic": topic})
	}
	return toolResult("unknown tool: "+name, true)
}

func (s *mcpServer) relay(ctx context.Context, path string, body map[string]any) map[string]any {
	out, err := s.local.Do(ctx, "POST", path, body)
	if err != nil {
		return toolResult(err.Error(), true)
	}
	return toolResult(string(out), false)
}

func (s *mcpServer) sessionOrUnknown() string {
	if s.session == "" {
		return "(unidentified — set $CLAUDE_SESSION_ID)"
	}
	return s.session
}

// localSessionsFallback answers session_list from this host's tmux when the
// coordinator cannot be reached, so the tool degrades instead of failing.
func (s *mcpServer) localSessionsFallback(ctx context.Context, cause error) string {
	var b strings.Builder
	fmt.Fprintf(&b, "coordinator unreachable (%v)\nshowing THIS HOST's sessions only:\n", cause)

	sessions, err := tmux.Sessions(ctx)
	if err != nil {
		fmt.Fprintf(&b, "  and tmux is not answering either: %v\n", err)
		return b.String()
	}
	n := 0
	for _, sess := range sessions {
		for _, w := range sess.WindowsL {
			fmt.Fprintf(&b, "  %s  (%s)\n", w.FriendlyName, w.Path)
			n++
		}
	}
	if n == 0 {
		b.WriteString("  (no windows)\n")
	}
	return b.String()
}

// mcpTools describes the tool surface. Descriptions are prescriptive on
// purpose: a model picks a tool from its description, so "when to use this"
// earns its place more than "what it does".
func mcpTools() []mcpTool {
	strProp := func(desc string) map[string]any {
		return map[string]any{"type": "string", "description": desc}
	}
	boolProp := func(desc string) map[string]any {
		return map[string]any{"type": "boolean", "description": desc}
	}
	obj := func(props map[string]any, required ...string) map[string]any {
		if required == nil {
			required = []string{}
		}
		return map[string]any{
			"type": "object", "properties": props, "required": required,
		}
	}

	return []mcpTool{
		{
			Name: "session_list",
			Description: "The directory of who is out there: every Claude session across all " +
				"hosts, the project each one owns, what it says it is currently doing, " +
				"whether it is online, and how much undrained mail it has. This is the " +
				"routing table — read it to find which domain expert to hand a task to, " +
				"then session_send to that project name. Degrades to this host's sessions " +
				"if the coordinator is down.",
			InputSchema: obj(map[string]any{}),
		},
		{
			Name: "session_send",
			Description: "Hand work to the session that owns a domain, or report a result " +
				"back. Address it BY PROJECT — \"homelab\", \"iptv\" — not by a session id: " +
				"each session is the expert in its own project, and the coordinator resolves " +
				"the name to whichever session that is. Exact match wins, then substring; an " +
				"ambiguous name is refused rather than guessed, and an unknown one is refused " +
				"with the list of what exists, so a typo bounces instead of vanishing. It " +
				"waits if that session is offline and is delivered when it returns. A " +
				"project that is not RUNNING also queues — but only if this coordinator " +
				"can already see it, meaning it is in its node's startable folder list. " +
				"A project it has never seen is REFUSED at send time and nothing is kept, " +
				"so check the reply: a refusal is an error, not a queue. The reply carries " +
				"`pending`: the recipient's undrained count AFTER this message. 1 means " +
				"yours is the only thing waiting; a larger number means they are behind and " +
				"may not read it soon. It is a receipt for STORAGE, never for reading — " +
				"nothing here can know whether anybody acted on it. " +
				"session_list shows who is out there.",
			InputSchema: obj(map[string]any{
				"to":    strProp("project or domain to route to, e.g. \"homelab\"; an alias or full session id also works"),
				"title": strProp("one-line subject"),
				"body":  strProp("the message"),
				"type":  strProp("info (default), success, warning, or failure"),
				"tag":   strProp("free-form tag carried to the recipient"),
			}, "to", "body"),
		},
		{
			Name: "session_broadcast",
			Description: "Send to every session subscribed to a topic. Use for announcements " +
				"that are not addressed to anyone in particular; prefer session_send when you " +
				"know who needs it.",
			InputSchema: obj(map[string]any{
				"topic": strProp("topic name"),
				"title": strProp("one-line subject"),
				"body":  strProp("the message"),
			}, "topic", "body"),
		},
		{
			Name: "session_inbox_drain",
			Description: "Fetch and acknowledge this session's pending messages. Draining is " +
				"final: a drained message is never redelivered, so act on what it returns.",
			InputSchema: obj(map[string]any{}),
		},
		{
			Name: "task_create",
			Description: "Hand a piece of work to another session AND record it, so it can " +
				"be asked about later. Use this instead of session_send whenever you are " +
				"asking someone to DO something rather than telling them something. The " +
				"brief is delivered as mail, and the task is what makes the difference " +
				"between delegating and hoping: an unanswered task is chased, an " +
				"unanswered message is forgotten.",
			InputSchema: obj(map[string]any{
				"to": strProp("the project or session to hand it to, e.g. \"homelab\""),
				"brief": strProp("what you are asking for, stated so somebody else could act " +
					"on it without asking you a question first"),
				"thread": strProp("optional label to group related tasks"),
			}, "to", "brief"),
		},
		{
			Name: "task_update",
			Description: "Report where a task has got to. Move it to `active` when you pick " +
				"it up, `blocked` with a note saying what you are stuck on, `done` when it " +
				"is finished, or `dropped` if it should not be done after all — dropping is " +
				"an answer, not a failure. Whoever asked is told automatically when a task " +
				"ends, so they do not have to poll. A task left silent will be chased.",
			InputSchema: obj(map[string]any{
				"id":    strProp("the task id, given to you when the work arrived"),
				"state": strProp("open, active, blocked, done, or dropped"),
				"note": strProp("what happened, in a sentence. Matters most for `blocked`: " +
					"a task stalled with no reason is a question for whoever asked."),
			}, "id", "state"),
		},
		{
			Name: "task_list",
			Description: "What work is outstanding. With no arguments, what has been handed " +
				"to THIS session. Pass `requested_by` with your own session id to answer the " +
				"question that used to be unanswerable: what did I hand off, and where did " +
				"it get to — `session_whoami` gives you that id. Finished work is hidden " +
				"unless you ask for it.",
			InputSchema: obj(map[string]any{
				"session":      strProp("whose plate to look at; defaults to yours"),
				"requested_by": strProp("who asked; use your own id to see what you delegated"),
				"include_done": boolProp("include done and dropped tasks"),
				"all":          boolProp("every task in the tenant, whoever it belongs to"),
			}),
		},
		{
			Name: "session_status_set",
			Description: "Say what this session is currently working on, in a few words " +
				"(\"rebuilding the index\", \"waiting on the homelab peer\"). Visible to " +
				"every other session and on the operator's dashboard. Set it when starting " +
				"work that will take a while, and pass an empty string when done — a stale " +
				"status is worse than none, because a peer will act on it. This is how " +
				"several sessions in different domains stay legible as one system: whether " +
				"a window is idle is something anyone can see; what the work is waiting on " +
				"is something only you know.",
			InputSchema: obj(map[string]any{
				"status": strProp("short description of current work; empty to clear"),
			}),
		},
		{
			Name:        "session_subscribe",
			Description: "Subscribe this session to a broadcast topic.",
			InputSchema: obj(map[string]any{"topic": strProp("topic name")}, "topic"),
		},
		{
			Name:        "session_unsubscribe",
			Description: "Stop receiving a broadcast topic.",
			InputSchema: obj(map[string]any{"topic": strProp("topic name")}, "topic"),
		},
		{
			Name: "notify_send",
			Description: "Notify the HUMAN operator on their phone (Telegram/Pushover). Use " +
				"only when something genuinely needs a person: a task finished that they are " +
				"waiting on, or work is blocked and cannot proceed. This interrupts someone — " +
				"it is not for progress updates, and a notification nobody needed is how the " +
				"next real one gets ignored.",
			InputSchema: obj(map[string]any{
				"body":  strProp("what happened, in one or two sentences"),
				"title": strProp("short subject line"),
				"tag":   strProp("all (default), telegram, or pushover"),
				"type":  strProp("info (default), success, warning, or failure"),
			}, "body"),
		},
		{
			Name: "session_whoami",
			Description: "This session's own id, its host's agent, and whether that agent is " +
				"connected to a coordinator. Use when a send fails and you need to tell " +
				"'offline' from 'misaddressed'.",
			InputSchema: obj(map[string]any{}),
		},
	}
}
