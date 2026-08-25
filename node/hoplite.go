// Package node is the per-host agent. It dials the coordinator, holds a
// command stream open, and executes tmux operations on the machine it runs on.
//
// It never listens. Everything is outbound, which is what lets a laptop on
// hotel wifi stay drivable and removes the peer list the flock needed.
package node

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

const (
	// reportInterval is how often the agent pushes its window list even when
	// nothing prompted it. Window changes are picked up by the coordinator's
	// own polling too; this bounds how stale the dashboard can get.
	reportInterval = 5 * time.Second

	// backoffCap bounds reconnect delay. The coordinator being down must not
	// turn into a retry storm, but a returning coordinator should be picked up
	// within a minute.
	backoffCap = 60 * time.Second

	// reportLogEvery re-logs a persisting report failure every N attempts. At
	// reportInterval that is roughly once a minute — often enough that a broken
	// agent is visible in the log, rare enough not to drown it.
	reportLogEvery = 12
)

// Config is what a node needs to reach its coordinator.
type Config struct {
	Coord   string // e.g. https://coordinator.example
	Node    string // SHABADOO_NODE; the coordinator overrides this from the key
	Version string
	// AuthSock is the ssh-agent socket. Empty means $SSH_AUTH_SOCK, which
	// claude.sh already forwards into every tmux window.
	AuthSock string

	// KeyFile is a private key to use instead of ssh-agent. A long-running
	// service has no interactive agent to borrow from, and spawning one from a
	// unit file is fragile — so a daemon points at a key it owns.
	KeyFile string
}

// Handler executes one command locally and returns its result payload.
type Handler func(ctx context.Context, op string, payload json.RawMessage) (any, error)

// Sessions reports this host's current windows.
type Sessions func(ctx context.Context) (any, error)

// Client is the agent's connection to the coordinator.
type Client struct {
	cfg      Config
	http     *http.Client
	handle   Handler
	sessions Sessions

	token string

	// Report failures are counted so a persistent one is logged periodically
	// rather than once or every time. Guarded because reportLoop runs
	// concurrently with the command stream.
	reportMu      sync.Mutex
	reportFails   int
	lastReportErr string

	// reauth is signalled when the coordinator rejects this session's token.
	//
	// The agent token expires after 24h, and nothing renewed it. A node whose
	// stream stayed open past that point kept LOOKING connected — the stream is
	// authenticated once, at connect — while every /agent/report and
	// /agent/result silently 401'd. The dashboard showed a healthy node with a
	// session list frozen at the moment the token died.
	//
	// Buffered and non-blocking: many callers may notice at once and none of
	// them may stall waiting for the stream to read it.
	reauth chan struct{}
}

func New(cfg Config, handle Handler, sessions Sessions) *Client {
	return &Client{
		cfg:    cfg,
		reauth: make(chan struct{}, 1),
		// No timeout: the stream is long-lived. Per-request timeouts are applied
		// with contexts on the short calls instead.
		http:     &http.Client{},
		handle:   handle,
		sessions: sessions,
	}
}

// Run connects and serves commands until ctx is cancelled, reconnecting with
// backoff. It returns only when ctx ends — an unreachable coordinator is an
// expected condition, not a fatal one. The tmux windows on this host keep
// working regardless; that degradation is the point.
func (c *Client) Run(ctx context.Context) error {
	var attempt int
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		err := c.session(ctx)
		if ctx.Err() != nil {
			return nil
		}
		attempt++
		delay := backoff(attempt)
		if err != nil {
			log.Printf("node: %v; retrying in %s", err, delay.Round(time.Second))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(delay):
		}
	}
}

// backoff grows exponentially with jitter, capped. Jitter matters when several
// agents lose the coordinator at once — without it they all retry in lockstep.
func backoff(attempt int) time.Duration {
	d := time.Duration(math.Min(
		float64(backoffCap),
		float64(time.Second)*math.Pow(2, float64(attempt-1)),
	))
	jitter := time.Duration(randInt63n(int64(d / 2)))
	return d/2 + jitter
}

func randInt63n(n int64) int64 {
	if n <= 0 {
		return 0
	}
	b := make([]byte, 8)
	rand.Read(b)
	v := int64(0)
	for _, x := range b {
		v = v<<8 | int64(x)
	}
	if v < 0 {
		v = -v
	}
	return v % n
}

// session performs one full connect: authenticate, open the stream, serve
// until it drops.
func (c *Client) session(ctx context.Context) error {
	if err := c.login(ctx); err != nil {
		return fmt.Errorf("login: %w", err)
	}
	log.Printf("node: connected to %s as %s", c.cfg.Coord, c.cfg.Node)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go c.reportLoop(ctx)

	return c.stream(ctx)
}

// login signs a coordinator challenge with a key from ssh-agent.
//
// Every key the agent holds is tried in turn, the way ssh itself does: the
// coordinator's authorized_agents decides which one counts, and the node
// does not need to be told which of the user's keys is the shabadoo key.
func (c *Client) login(ctx context.Context) error {
	signers, err := c.signers()
	if err != nil {
		return err
	}
	if len(signers) == 0 {
		return errors.New("ssh-agent holds no keys (is SSH_AUTH_SOCK forwarded?)")
	}

	var lastErr error
	for _, signer := range signers {
		ch, err := c.hello(ctx)
		if err != nil {
			return err
		}
		sig, err := signer.Sign(rand.Reader, challengeBlob(ch))
		if err != nil {
			lastErr = err
			continue
		}
		body, err := json.Marshal(map[string]any{
			"challenge": ch,
			"pubkey":    signer.PublicKey().Marshal(),
			"signature": ssh.Marshal(sig),
			"version":   c.cfg.Version,
			"platform":  Platform(),
			// What this machine can do. Sent at login rather than in the
			// periodic report: it is a fact about the host, not about the
			// moment, and it wants exactly the lifetime of the connection.
			"capabilities": Capabilities(),
		})
		if err != nil {
			return err
		}

		resp, err := c.post(ctx, "/agent/login", body, "")
		if err != nil {
			return err
		}
		if resp.StatusCode == http.StatusUnauthorized {
			resp.Body.Close()
			lastErr = errors.New("no key in ssh-agent is authorized by the coordinator")
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return fmt.Errorf("coordinator returned HTTP %d", resp.StatusCode)
		}
		var lr struct {
			Node  string `json:"node"`
			Token string `json:"token"`
		}
		err = json.NewDecoder(resp.Body).Decode(&lr)
		resp.Body.Close()
		if err != nil {
			return err
		}
		// The coordinator's name for us wins: it comes from which key verified,
		// not from this host's own config.
		c.token, c.cfg.Node = lr.Token, lr.Node
		return nil
	}
	if lastErr == nil {
		lastErr = errors.New("authentication failed")
	}
	return lastErr
}

func (c *Client) signers() ([]ssh.Signer, error) {
	if c.cfg.KeyFile != "" {
		raw, err := os.ReadFile(c.cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("key file: %w", err)
		}
		signer, err := ssh.ParsePrivateKey(raw)
		if err != nil {
			return nil, fmt.Errorf("key file %s: %w", c.cfg.KeyFile, err)
		}
		return []ssh.Signer{signer}, nil
	}

	sock := c.cfg.AuthSock
	if sock == "" {
		sock = os.Getenv("SSH_AUTH_SOCK")
	}
	if sock == "" {
		return nil, errors.New("SSH_AUTH_SOCK is not set")
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("ssh-agent: %w", err)
	}
	// Left open for the life of the login; closed when the process exits.
	return agent.NewClient(conn).Signers()
}

// challengeBlob mirrors the coordinator's Challenge.blob(). The two must stay
// byte-identical or nothing verifies.
func challengeBlob(ch map[string]any) []byte {
	return fmt.Appendf(nil, "%s\n%s\n%d\n",
		ch["namespace"], ch["nonce"], int64(ch["timestamp"].(float64)))
}

func (c *Client) hello(ctx context.Context) (map[string]any, error) {
	resp, err := c.post(ctx, "/agent/hello", []byte("{}"), "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hello: HTTP %d", resp.StatusCode)
	}
	var ch map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&ch); err != nil {
		return nil, err
	}
	return ch, nil
}

// credentialRejected reports that the coordinator refused this session's token,
// so the stream should be torn down and a fresh login performed.
//
// Only 401 counts. A 5xx is the coordinator having a bad moment and will pass;
// re-authenticating on one would turn a blip into a reconnect storm across
// every node at once.
func (c *Client) credentialRejected() {
	select {
	case c.reauth <- struct{}{}:
	default: // already signalled; one reconnect is enough
	}
}

// stream holds the command stream open and dispatches what arrives.
func (c *Client) stream(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.Coord+"/agent/stream", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("stream: HTTP %d", resp.StatusCode)
	}

	// A dead credential has to break the read loop, which is blocked in
	// Scan(). Closing the body is what unblocks it; the outer session loop then
	// re-logins with a fresh token.
	streamDone := make(chan struct{})
	defer close(streamDone)
	go func() {
		select {
		case <-c.reauth:
			log.Printf("node: coordinator rejected our token; reconnecting")
			resp.Body.Close()
		case <-streamDone:
		case <-ctx.Done():
		}
	}()

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		data, found := strings.CutPrefix(sc.Text(), "data: ")
		if !found {
			continue // ": ping" keepalives and blank separators
		}
		var cmd struct {
			ID      string          `json:"id"`
			Op      string          `json:"op"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal([]byte(data), &cmd); err != nil {
			continue
		}
		// Each command runs in its own goroutine so a slow capture does not
		// stall the stream behind it.
		go c.dispatch(ctx, cmd.ID, cmd.Op, cmd.Payload)
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return errors.New("stream closed by coordinator")
}

func (c *Client) dispatch(ctx context.Context, id, op string, payload json.RawMessage) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	res := map[string]any{"id": id}

	// `upgrade` is handled here rather than by the host op handler: it needs
	// the coordinator URL and this session's token to fetch the binary, and
	// both are the agent's, not the host's.
	var out any
	var err error
	if op == "upgrade" {
		out, err = c.upgrade(ctx, payload)
	} else {
		out, err = c.handle(ctx, op, payload)
	}
	if err != nil {
		res["ok"] = false
		res["error"] = err.Error()
	} else {
		res["ok"] = true
		if out != nil {
			b, mErr := json.Marshal(out)
			if mErr != nil {
				res["ok"] = false
				res["error"] = mErr.Error()
			} else {
				res["payload"] = json.RawMessage(b)
			}
		}
	}

	body, err := json.Marshal(res)
	if err != nil {
		return
	}
	resp, err := c.post(ctx, "/agent/result", body, c.token)
	if err == nil {
		// Same as the report path: a rejected result means this session's token
		// is gone, and every subsequent command would be answered into a void.
		if resp.StatusCode == http.StatusUnauthorized {
			c.credentialRejected()
		}
		resp.Body.Close()
	}
}

// reportLoop pushes this host's window list on a timer.
func (c *Client) reportLoop(ctx context.Context) {
	t := time.NewTicker(reportInterval)
	defer t.Stop()
	for {
		c.report(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (c *Client) report(ctx context.Context) {
	if c.sessions == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	list, err := c.sessions(ctx)
	if err != nil {
		c.reportProblem("collect sessions: %v", err)
		return
	}
	body, err := json.Marshal(map[string]any{"sessions": list})
	if err != nil {
		c.reportProblem("encode report: %v", err)
		return
	}
	resp, err := c.post(ctx, "/agent/report", body, c.token)
	if err != nil {
		c.reportProblem("post report: %v", err)
		return
	}
	defer resp.Body.Close()

	// A report that is *accepted* leaves no trace; one that is refused used to
	// leave none either, because this call discarded both the error and the
	// status. The agent then looked perfectly healthy — connected, streaming,
	// answering commands — while the dashboard showed it owning no sessions,
	// which is indistinguishable from a host with nothing running.
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		c.reportProblem("coordinator refused report: %s: %s",
			resp.Status, strings.TrimSpace(string(msg)))
		// 401 here means the 24h agent token expired underneath a stream that
		// is still open. Reconnecting is the fix and it costs one SSHSIG login.
		if resp.StatusCode == http.StatusUnauthorized {
			c.credentialRejected()
		}
		return
	}
	c.reportOK()
}

// reportProblem logs a failing report, but only when the failure is new or has
// persisted — reports run every few seconds, and logging each one would bury
// the connection messages that matter in an endless repeat of the same line.
func (c *Client) reportProblem(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	c.reportMu.Lock()
	first := msg != c.lastReportErr
	c.lastReportErr = msg
	c.reportFails++
	n := c.reportFails
	c.reportMu.Unlock()

	if first || n%reportLogEvery == 0 {
		log.Printf("node: report failed (%d in a row): %s", n, msg)
	}
}

// reportOK notes a recovery, so the log says when the sessions came back
// rather than just going quiet.
func (c *Client) reportOK() {
	c.reportMu.Lock()
	n := c.reportFails
	c.reportFails = 0
	c.lastReportErr = ""
	c.reportMu.Unlock()

	if n > 0 {
		log.Printf("node: reporting recovered after %d failure(s)", n)
	}
}

func (c *Client) post(ctx context.Context, path string, body []byte, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.Coord+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return c.http.Do(req)
}
