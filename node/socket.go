package node

// The local socket: how a Claude session inside a tmux window on this host
// reaches the coordinator.
//
// A session's MCP subprocess talks to THIS process over a unix socket, and the
// node relays to the coordinator using the credential it already holds. That
// arrangement is the point:
//
//   - The subprocess needs no credential of its own. Today every session holds
//     NATS credentials; after this, a session's authority is "runs on a host
//     whose agent is authorised", which is a much smaller thing to leak.
//   - It works where the subprocess has no outbound network at all.
//   - Filesystem permissions are the access control. The socket is 0600 in the
//     operator's own directory, so "can open this socket" means "is already
//     this user", who could read the agent key anyway.
//
// It is deliberately a tiny HTTP server rather than a bespoke protocol: net/http
// over a unix listener is stdlib, debuggable with curl --unix-socket, and the
// request shapes are the coordinator's own.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SocketPath is where the agent listens for local session traffic. Alongside
// the agent key, because it is the same trust boundary: anyone who can open one
// can read the other.
func SocketPath() string {
	if v := os.Getenv("SHABADOO_SOCKET"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "shabadoo-agent.sock")
	}
	return filepath.Join(home, ".config", "shabadoo", "agent.sock")
}

// relayPaths is the exact set of coordinator endpoints a local session may
// reach, mapped from the local path it calls.
//
// An allowlist rather than a prefix match: the agent's bearer token can drive
// every pane on this host, so a session that could name its own upstream path
// would inherit the whole agent plane. These are the messaging endpoints and
// nothing else.
var relayPaths = map[string]string{
	"/message/send":      "/agent/message/send",
	"/message/broadcast": "/agent/message/broadcast",
	"/message/drain":     "/agent/message/drain",
	"/subscribe":         "/agent/subscribe",
	"/unsubscribe":       "/agent/unsubscribe",
	"/notify":            "/agent/notify",
	"/status":            "/agent/status",
	"/task/create":       "/agent/task/create",
	"/task/update":       "/agent/task/update",
	"/task/list":         "/agent/task/list",
}

// ErrNotConnected is returned to a local caller when the agent has no live
// coordinator session. Named so the MCP layer can say something useful rather
// than surfacing a transport error.
var ErrNotConnected = errors.New("agent is not connected to a coordinator")

// ServeLocal runs the local socket until ctx is done.
//
// It is started alongside the command stream and outlives individual
// connections to the coordinator: a session should get a clear "not connected"
// rather than a refused socket while the agent is reconnecting, because those
// two mean very different things to whoever is reading the error.
func (c *Client) ServeLocal(ctx context.Context) error {
	path := SocketPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("socket dir: %w", err)
	}
	// A socket left behind by a killed process makes Listen fail with "address
	// already in use" — for a file, not a port, which reads as nonsense the
	// first time. Remove a stale one, but never a live one: if something is
	// still listening, this node is a duplicate and should say so.
	if conn, err := net.DialTimeout("unix", path, 200*time.Millisecond); err == nil {
		conn.Close()
		return fmt.Errorf("another agent is already listening on %s", path)
	}
	_ = os.Remove(path)

	ln, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("listen %s: %w", path, err)
	}
	// 0600 before anything can connect. The default would be 0755, which on a
	// shared machine would hand every local user this host's Claude sessions.
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return fmt.Errorf("chmod %s: %w", path, err)
	}

	srv := &http.Server{
		Handler:           c.localRoutes(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
		os.Remove(path)
	}()

	log.Printf("node: local socket at %s", path)
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (c *Client) localRoutes() http.Handler {
	mux := http.NewServeMux()

	// Who am I, and is the agent usable right now? The first question any
	// client asks, and the one that distinguishes "nothing to report" from
	// "this is broken".
	mux.HandleFunc("GET /whoami", func(w http.ResponseWriter, r *http.Request) {
		writeLocalJSON(w, map[string]any{
			"node":      c.cfg.Node,
			"coord":     c.cfg.Coord,
			"version":   c.cfg.Version,
			"connected": c.tokenValue() != "",
		})
	})

	mux.HandleFunc("GET /peers", func(w http.ResponseWriter, r *http.Request) {
		c.relay(w, r, "GET", "/agent/peers", nil)
	})

	for local, upstream := range relayPaths {
		local, upstream := local, upstream
		mux.HandleFunc("POST "+local, func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			c.relay(w, r, "POST", upstream, body)
		})
	}
	return mux
}

// relay forwards a local call to the coordinator with the agent's credential.
func (c *Client) relay(w http.ResponseWriter, r *http.Request, method, path string, body []byte) {
	token := c.tokenValue()
	if token == "" {
		// Deliberately a specific status and message. A session that cannot
		// send mail because the coordinator is unreachable should say exactly
		// that; the alternative is a generic failure that reads like a bug in
		// whatever the session was doing.
		http.Error(w, ErrNotConnected.Error(), http.StatusServiceUnavailable)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	var resp *http.Response
	var err error
	if method == "GET" {
		req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.Coord+path, nil)
		if rerr != nil {
			http.Error(w, rerr.Error(), http.StatusInternalServerError)
			return
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err = c.http.Do(req)
	} else {
		resp, err = c.post(ctx, path, body, token)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	out, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	w.Write(out)
}

// tokenValue reads the current agent token. Empty means no live session with
// the coordinator — the agent may be mid-reconnect.
func (c *Client) tokenValue() string {
	c.reportMu.Lock()
	defer c.reportMu.Unlock()
	return c.token
}

func writeLocalJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// LocalClient talks to an agent's socket. Used by `shabadoo mcp`, and by
// anything else that wants to act as a session on this host.
type LocalClient struct {
	http *http.Client
}

// NewLocalClient dials the agent socket. It does not connect eagerly: a session
// starting before its agent is up should fail on the first call with a clear
// message, not fail to start.
func NewLocalClient() *LocalClient {
	path := SocketPath()
	return &LocalClient{
		http: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", path)
				},
			},
		},
	}
}

// Do calls the local agent. path is a local route ("/peers", "/message/send").
func (l *LocalClient) Do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = strings.NewReader(string(raw))
	}
	// The host is ignored — the transport dials the socket — but net/http
	// insists on a syntactically valid URL.
	req, err := http.NewRequestWithContext(ctx, method, "http://agent"+path, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := l.http.Do(req)
	if err != nil {
		// The common case by far: no agent running on this host. Say that,
		// rather than leaking a dial error about a path the reader did not know
		// existed.
		return nil, fmt.Errorf("no shabadoo agent on this host (%s): %w", SocketPath(), err)
	}
	defer resp.Body.Close()

	out, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%s %s: %s", method, path, strings.TrimSpace(string(out)))
	}
	return out, nil
}
