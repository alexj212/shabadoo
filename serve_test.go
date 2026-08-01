package main

// The standalone server exists to be there when the coordinator is not, which
// means it is exercised precisely when nobody is in a position to debug it.
// These tests stand in for that missing exercise.
//
// The parity test reads the endpoint list out of the dashboard itself rather
// than hardcoding one: a hardcoded list would have been written from the same
// stale understanding that let this file fall three endpoints behind the page.
// Deriving it from static/index.html means adding a fetch to the dashboard
// fails this test until `serve` can answer it.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// dashboardEndpoints returns the API paths static/index.html calls, split by
// method. Anything reached through the page's post() helper is a write;
// everything else is a read.
func dashboardEndpoints(t *testing.T) (gets, posts []string) {
	t.Helper()
	page, err := os.ReadFile("static/index.html")
	if err != nil {
		t.Fatalf("read dashboard: %v", err)
	}
	src := string(page)

	// Two ways the page writes: its own post() helper, and a bare fetch with
	// an explicit method. Recognising only the first classified a direct
	// `fetch("/api/devices/renew", {method:"POST"})` as a GET, and the test
	// then demanded a GET route that should not exist.
	postRe := regexp.MustCompile(`post\("(/api/[a-zA-Z0-9/_-]+)"`)
	fetchRe := regexp.MustCompile(
		`fetch\("(/api/[a-zA-Z0-9/_-]+)"\s*,\s*\{[^}]*method:\s*"(POST|PUT|DELETE)"`)
	anyRe := regexp.MustCompile(`/api/[a-zA-Z0-9/_-]+`)

	isPost := map[string]bool{}
	for _, m := range postRe.FindAllStringSubmatch(src, -1) {
		isPost[m[1]] = true
	}
	for _, m := range fetchRe.FindAllStringSubmatch(src, -1) {
		isPost[m[1]] = true
	}
	seen := map[string]bool{}
	for _, path := range anyRe.FindAllString(src, -1) {
		if seen[path] {
			continue
		}
		seen[path] = true
		if isPost[path] {
			posts = append(posts, path)
		} else {
			gets = append(gets, path)
		}
	}
	sort.Strings(gets)
	sort.Strings(posts)
	return gets, posts
}

// TestServeRoutesCoverDashboard is the regression guard for the fallback being
// silently unusable: /api/keys, /api/input-state and /api/folders were all
// called by the page and routed nowhere here, so a dialog could not be answered
// and the folder picker was empty in the one mode you reach for during an
// outage.
func TestServeRoutesCoverDashboard(t *testing.T) {
	gets, posts := dashboardEndpoints(t)
	if len(gets) == 0 || len(posts) == 0 {
		t.Fatalf("extracted no endpoints from the dashboard (gets=%v posts=%v) — "+
			"the page's call style changed and this test is no longer reading it", gets, posts)
	}

	b := &bridge{self: "test"}
	mux, err := b.routes()
	if err != nil {
		t.Fatalf("routes: %v", err)
	}

	for _, path := range gets {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		_, pattern := mux.Handler(req)
		// "GET /" is the file server catching everything unmatched; an API path
		// landing there means no handler is registered for it.
		if pattern == "" || pattern == "GET /" {
			t.Errorf("GET %s is not routed (falls through to the static file server)", path)
		}
	}
	for _, path := range posts {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		_, pattern := mux.Handler(req)
		if pattern == "" {
			t.Errorf("POST %s is not routed", path)
		}
	}
}

// TestServeWriteAcceptsDashboardBody pins the second half of that break: the
// page sends {node, ...} on every write, decodeJSON rejects unknown fields, and
// the old per-op structs did not declare `node` — so every write 400'd with a
// message about an unexpected field.
//
// # These fixtures must stay un-resolvable, and that is not a style preference
//
// This test runs against the developer's own machine, against the real tmux
// server, with no sandbox between them: `call` dispatches to handleOp, which
// shells out for real. An earlier draft used session "claude" window 1 and path
// "/tmp" — plausible-looking values that on this host named a live Claude
// pane. Running the test killed that window and spawned a stray session in
// /tmp.
//
// So: the session name must be one tmux cannot have, and the path must not
// exist. Every op then fails inside tmux, which is exactly what this test
// wants — the assertion is that the body was *decoded*, and decoding happens
// before dispatch. A fixture that resolves is a test that mutates the machine
// it is run on.
const (
	noSuchSession = "shabadoo-test-no-such-session"
	noSuchPath    = "/nonexistent/shabadoo-test-no-such-path"
	noSuchWindow  = `"name":"shabadoo-test-no-such-window"`
)

func TestServeWriteAcceptsDashboardBody(t *testing.T) {
	b := &bridge{self: "test"}
	mux, err := b.routes()
	if err != nil {
		t.Fatalf("routes: %v", err)
	}

	// The shapes static/index.html posts, with every target deliberately
	// un-resolvable — see the comment above.
	s := `"node":"test","session":"` + noSuchSession + `","window":0`
	bodies := map[string]string{
		"/api/select":  `{` + s + `}`,
		"/api/send":    `{` + s + `,"text":"x","enter":true}`,
		"/api/command": `{` + s + `,"command":"/status"}`,
		"/api/keys":    `{` + s + `,"keys":["Enter"]}`,
		"/api/kill":    `{` + s + `}`,
		"/api/reopen":  `{"node":"test",` + noSuchWindow + `}`,
		"/api/open":    `{"node":"test","path":"` + noSuchPath + `"}`,
	}

	for path, body := range bodies {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		// The op itself may well fail — there is no such tmux window, and CI may
		// have no tmux at all. What must not happen is the request being refused
		// before it ever reaches tmux.
		switch rec.Code {
		case http.StatusNotFound, http.StatusBadRequest, http.StatusUnsupportedMediaType:
			t.Errorf("POST %s rejected the dashboard's body: %d %s",
				path, rec.Code, strings.TrimSpace(rec.Body.String()))
		}
	}
}

// TestServeSessionsShape pins the response shape against the renderer. The page
// reads data.nodes; this endpoint used to return the flock's flat
// {now, node, sessions}, which renders as "No agents connected" — a fallback
// that looks like a total outage rather than a working local server.
func TestServeSessionsShape(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	b := &bridge{self: "test"}
	mux, err := b.routes()
	if err != nil {
		t.Fatalf("routes: %v", err)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sessions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/sessions: %d %s", rec.Code, strings.TrimSpace(rec.Body.String()))
	}

	var got struct {
		Now   int64 `json:"now"`
		Nodes []struct {
			Node     string            `json:"node"`
			Online   bool              `json:"online"`
			Version  string            `json:"version"`
			Sessions []json.RawMessage `json:"sessions"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Now == 0 {
		t.Error("now is missing")
	}
	if len(got.Nodes) != 1 {
		t.Fatalf("want exactly one node, got %d", len(got.Nodes))
	}
	n := got.Nodes[0]
	if n.Node != "test" {
		t.Errorf("node = %q, want %q", n.Node, "test")
	}
	if !n.Online {
		t.Error("the node answering the request reported itself offline")
	}
	if n.Version != version {
		t.Errorf("version = %q, want %q", n.Version, version)
	}
}
