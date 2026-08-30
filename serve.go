package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"shabadoo/claudelog"
)

// bridge serves this host's own tmux server, with no coordinator involved.
//
// This is the standalone fallback: if hub is unreachable, `shabadoo serve`
// still drives the panes on this machine. It has no peers — cross-host merging
// is the coordinator's job now, reached by agents dialling out.
type bridge struct {
	self string // this node's name, matching the host label
}

// processStart backs the uptime reported by /healthz.
var processStart = time.Now()

func runServe(args []string) {
	fset := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fset.String("addr", "127.0.0.1:8787",
		"listen address (host:port). Host may be `tailscale` to bind this machine's Tailscale IP.")
	node := fset.String("node", "", "this node's name (default: the host label)")
	fset.Parse(args)

	b := &bridge{self: *node}
	if b.self == "" {
		b.self = hostLabel()
	}

	listen, err := resolveAddr(*addr)
	if err != nil {
		log.Fatalf("addr: %v", err)
	}

	mux, err := b.routes()
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("shabadoo serve (standalone) node %q listening on http://%s", b.self, listen)
	if !isLoopback(listen) {
		log.Printf("WARNING: %s is not loopback. There is no authentication — "+
			"anyone who can reach this address can drive every Claude pane on this host.", listen)
	}

	srv := &http.Server{
		Addr:              listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}

// routes builds the standalone server's mux.
//
// Split out from runServe so a test can exercise it without binding a port —
// specifically so `serve_test.go` can assert that every endpoint the embedded
// dashboard calls is actually registered here. That assertion is the guard
// against this file drifting behind the page again.
func (b *bridge) routes() (*http.ServeMux, error) {
	pages, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", b.handleHealth)
	mux.HandleFunc("GET /api/sessions", b.handleSessions)
	mux.HandleFunc("GET /api/capture", b.handleCapture)
	mux.HandleFunc("GET /api/claude/session", b.handleClaudeSession)
	mux.HandleFunc("GET /api/claude/events", b.handleClaudeEvents)
	mux.HandleFunc("GET /api/input-state", b.handleInputState)
	mux.HandleFunc("GET /api/folders", b.handleFolders)

	// Endpoints the coordinator serves that this mode structurally cannot: they
	// read the hub's database, and there is none here. Routed deliberately
	// rather than left to fall through — the static file server answers GET /
	// with index.html and a 200, so an unrouted API path returns a web page
	// where the caller expects JSON, which is a far more confusing failure than
	// an honest 501.
	mux.HandleFunc("GET /api/audit", unsupported(
		"the audit log lives in the coordinator's database; `serve` has none"))
	mux.HandleFunc("GET /api/messages", unsupported(
		"the durable inbox lives in the coordinator's database; `serve` has none"))
	// Resolutions are DERIVED from the report stream over time — the coordinator
	// watches rows appear and disappear across reports. This mode reads its own
	// tmux once per request and has no memory between them, so it cannot know
	// that a blocker used to be there. An honest 501, not an empty list: zero
	// closed and cannot-tell are different answers, and the page renders the
	// trend only when it has one.
	// Delegated work is a coordinator concept: a task is a promise between two
	// SESSIONS, recorded centrally so whoever asked is told when it ends. This
	// mode drives one host's panes and brokers nothing between them.
	mux.HandleFunc("GET /api/tasks", unsupported(
		"tasks live in the coordinator's database; `serve` has none"))
	mux.HandleFunc("GET /api/missions/resolved", unsupported(
		"resolutions are derived from stored history; `serve` keeps none"))
	// The dashboard renews its credential when the coordinator tells it one is
	// getting old. This mode has no credentials at all, so it never sends that
	// header and the page never asks — but the route exists so the answer is an
	// honest 501 rather than an HTML page where JSON was expected.
	mux.HandleFunc("POST /api/devices/renew", unsupported(
		"`serve` has no device enrolment: it is unauthenticated and local"))
	// The pushed session view needs agents reporting into a coordinator; this
	// mode reads its own tmux directly and has nothing to be notified BY. The
	// dashboard treats a failure here as "poll instead", which is what this
	// mode wants anyway — so the honest 501 is also the correct behaviour.
	mux.HandleFunc("GET /api/events", unsupported(
		"`serve` has no coordinator to push from; the dashboard polls here"))
	// Device enrolment is the coordinator's, and this mode has no identity of
	// any kind — it is unauthenticated and local by design. The dashboard hides
	// the Devices panel entirely on a non-2xx, which is the honest rendering:
	// not "nobody is paired", but "not a thing here".
	for _, p := range []string{"GET /api/devices", "POST /api/devices/code",
		"POST /api/devices/revoke"} {
		mux.HandleFunc(p, unsupported(
			"`serve` has no device enrolment: it is unauthenticated and local"))
	}

	mux.HandleFunc("POST /api/select", b.write("select"))
	mux.HandleFunc("POST /api/send", b.write("send"))
	mux.HandleFunc("POST /api/command", b.write("command"))
	mux.HandleFunc("POST /api/keys", b.write("keys"))
	mux.HandleFunc("POST /api/kill", b.write("kill"))
	mux.HandleFunc("POST /api/reopen", b.write("reopen"))
	mux.HandleFunc("POST /api/open", b.write("open"))
	mux.Handle("GET /", http.FileServerFS(pages))

	return mux, nil
}

// resolveAddr expands the `tailscale` host token to this machine's Tailscale
// IPv4. Nodes without a fronting proxy (a Mac, say) must bind a tailnet
// address to be reachable by peers, and that address is assigned rather than
// chosen — so naming it beats hardcoding it into a launchd plist.
func resolveAddr(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	if host != "tailscale" {
		return addr, nil
	}
	ip, err := tailscaleIPv4()
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(ip, port), nil
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "" {
		return false // empty host means all interfaces
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ---------------------------------------------------------------------------
// handlers
// ---------------------------------------------------------------------------
//
// Every handler below delegates to handleOp — the same dispatch table the node
// runs coordinator commands against. Standalone mode and agent mode therefore
// execute identical code for identical requests, which is the only arrangement
// that keeps this fallback honest.
//
// It did not used to. This file defined its own handlers, and they drifted the
// moment the dashboard grew /api/keys, /api/input-state and /api/folders: three
// endpoints that simply did not exist here, a sessions payload still in the
// flock's flat shape (which the page renders as "No agents connected"), and
// write bodies that 400'd because the dashboard sends `node` on every write and
// these structs did not declare it. All of it silent, because nothing exercises
// `serve` until the coordinator is already down — which is the worst possible
// moment to discover the escape hatch is welded shut.

// opTimeout bounds one local operation. It matches the node's dispatch budget,
// so an op that completes under the coordinator does not time out here.
const opTimeout = 25 * time.Second

// call runs one op on this host and returns its result payload.
func call(ctx context.Context, op string, args opArgs) (json.RawMessage, error) {
	payload, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	out, err := handleOp(ctx, op, payload)
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, nil
	}
	return json.Marshal(out)
}

// handleHealth is the liveness endpoint, matching the coordinator's shape so a
// monitor can point at either without special-casing.
//
// "Healthy" means something different here: there is no database, so the thing
// worth checking is that tmux still answers. A fallback that reports itself up
// while unable to reach the tmux server is useless in exactly the outage it
// exists for.
func (b *bridge) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	status, code := "ok", http.StatusOK
	sessions, err := reportSessions(ctx)
	if err != nil {
		status, code = "tmux unreachable: "+err.Error(), http.StatusServiceUnavailable
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{
		"status":   status,
		"version":  version,
		"uptime":   int(time.Since(processStart).Seconds()),
		"node":     b.self,
		"sessions": len(sessions),
	})
}

// handleSessions returns this host's windows in the coordinator's response
// shape. There is exactly one node — this one — and it is online by definition,
// since the process answering is the process that owns the tmux server.
func (b *bridge) handleSessions(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), opTimeout)
	defer cancel()

	sessions, err := reportSessions(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	// The agent path leaves this empty for the coordinator to fill from the
	// authenticated connection. Here it is simply us.
	for i := range sessions {
		sessions[i].Agent = b.self
	}

	writeJSONResp(w, map[string]any{
		"now": time.Now().Unix(),
		"nodes": []map[string]any{{
			"node":     b.self,
			"online":   true,
			"version":  version,
			"sessions": sessions,
		}},
	})
}

// handleCapture returns a pane's buffer as plain text: the visible screen plus
// `lines` of scrollback. Read-only, so no CSRF guard is needed.
func (b *bridge) handleCapture(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	session := q.Get("session")
	if session == "" {
		http.Error(w, "session required", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), opTimeout)
	defer cancel()

	window, _ := strconv.Atoi(q.Get("window"))
	// An absent `lines` means a page of scrollback rather than the visible
	// screen alone; the viewer always sends one, but a hand-written curl should
	// get something useful. handleOp clamps the upper bound.
	history := 1000
	if v := q.Get("lines"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			history = n
		}
	}

	raw, err := call(ctx, "capture", opArgs{
		Session: session,
		Window:  window,
		Lines:   history,
		Color:   q.Get("color") == "1",
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

// handleInputState reports whether a pane's keyboard belongs to the composer or
// to a modal dialog, so the dashboard can offer the answer keys.
func (b *bridge) handleInputState(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	window, _ := strconv.Atoi(q.Get("window"))

	ctx, cancel := context.WithTimeout(r.Context(), opTimeout)
	defer cancel()

	raw, err := call(ctx, "input_state", opArgs{Session: q.Get("session"), Window: window})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw)
}

// handleFolders lists startable folders on this host.
func (b *bridge) handleFolders(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), opTimeout)
	defer cancel()

	raw, err := call(ctx, "folders", opArgs{})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw)
}

// handleClaudeEvents serves a page of the conversation itself.
//
// This mode CAN answer it — the transcripts are on this host — unlike the
// endpoints routed to 501 above, which need the coordinator's database. The
// fallback exists to drive this machine's panes when the coordinator is gone,
// and reading what a session said is part of that.
func (b *bridge) handleClaudeEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("path") == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}
	num := func(k string) int64 {
		n, _ := strconv.ParseInt(q.Get(k), 10, 64)
		return n
	}
	file, err := claudelog.Resolve(q.Get("path"))
	if err != nil {
		if errors.Is(err, claudelog.ErrNotFound) {
			http.Error(w, "no claude session for "+q.Get("path"), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	page, err := claudelog.Events(file, claudelog.EventOpts{
		After: num("after"), Before: num("before"), Limit: int(num("limit")),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSONResp(w, page)
}

// handleClaudeSession summarizes the Claude session running in a window: the
// transcript Claude itself keeps under ~/.claude/projects, keyed by the
// window's cwd. Where /api/capture scrapes the terminal, this reads the
// session's own record — model, turns, tokens, tools — which outlives the
// pane's scrollback.
//
// Read-only, so no CSRF guard, and proxied to the owning node the same way
// /api/capture is.
func (b *bridge) handleClaudeSession(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	path := q.Get("path") // the window's cwd
	if path == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}

	// A cold scan of the largest transcript on record (72MB) takes ~0.5s and
	// only happens once per file; every later call folds in just the appended
	// bytes. No goroutine or warm-up path is needed to stay inside the poll
	// interval.
	sum, err := claudelog.Summarize(path)
	if err != nil {
		if errors.Is(err, claudelog.ErrNotFound) {
			http.Error(w, "no claude session for "+path, http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSONResp(w, sum)
}

// serveWriteReq is the body every pane-write endpoint accepts. It mirrors the
// coordinator's writeReq field for field — including `node`, which standalone
// mode has no use for but must still declare: the dashboard posts the same body
// to both servers, and decodeJSON rejects unknown fields. Dropping `node` here
// is what made every write in this mode fail with a 400.
type serveWriteReq struct {
	Node    string   `json:"node,omitempty"`
	Session string   `json:"session,omitempty"`
	Window  int      `json:"window,omitempty"`
	Text    string   `json:"text,omitempty"`
	Enter   bool     `json:"enter,omitempty"`
	Command string   `json:"command,omitempty"`
	Keys    []string `json:"keys,omitempty"`
	Name    string   `json:"name,omitempty"`
	Path    string   `json:"path,omitempty"`
}

// write returns a handler that runs one mutating op on this host.
//
// Unlike the coordinator's equivalent there is no audit log to write to: that
// lives in the hub's database, and this mode has no database. A fallback that
// refused to work without one would defeat its own purpose, so the trade is
// explicit — `serve` is drive-only, and what happened during an outage is
// recoverable from the panes themselves, not from here.
func (b *bridge) write(op string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req serveWriteReq
		if !decodeJSON(w, r, &req) {
			return
		}
		// Checked here rather than left to handleOp so a missing argument reads
		// as the client error it is, not as a failed operation.
		switch op {
		case "reopen":
			if req.Name == "" {
				http.Error(w, "name required", http.StatusBadRequest)
				return
			}
		case "open":
			if req.Path == "" {
				http.Error(w, "path required", http.StatusBadRequest)
				return
			}
		}

		ctx, cancel := context.WithTimeout(r.Context(), opTimeout)
		defer cancel()

		_, err := call(ctx, op, opArgs{
			Session: req.Session,
			Window:  req.Window,
			Text:    req.Text,
			Enter:   req.Enter,
			Command: req.Command,
			Keys:    req.Keys,
			Name:    req.Name,
			Path:    req.Path,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// unsupported answers an endpoint this mode cannot implement. The dashboard
// treats any non-OK response as "hide that panel", so a 501 renders as the
// feature being absent rather than as an error the operator must chase.
func unsupported(reason string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, reason, http.StatusNotImplemented)
	}
}

func writeJSONResp(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("encode error: %v", err)
	}
}

// decodeJSON reads a JSON body into v. It requires Content-Type
// application/json — which, for a browser, forces a CORS preflight on
// cross-origin requests the server never answers, giving baseline CSRF
// protection for these write endpoints. Returns false (and writes an error
// response) on any problem.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		http.Error(w, "expected Content-Type application/json", http.StatusUnsupportedMediaType)
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}
