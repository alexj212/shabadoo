package node

// Replacing this binary with one the coordinator holds.
//
// The agent already has everything needed: an authenticated session with the
// coordinator, and write access to its own executable. What it does not have is
// a supervisor of its own, so the ordering matters more than the mechanism —
// once the running binary is replaced there is no second chance to notice a
// mistake.
//
// The order is: download, verify the checksum, RUN THE NEW BINARY and check it
// reports the version it claims, and only then swap. That third step is the one
// worth arguing for: it costs a process spawn and it catches the whole class of
// failures that are otherwise unrecoverable — wrong architecture, truncated
// file, a binary that is not this program. A checksum only proves the bytes
// arrived intact, not that they can run here.
//
// Exiting is how the restart happens. The service definitions already supervise
// this process (systemd `Restart=on-failure`, launchd `KeepAlive`), so the
// agent exits non-zero and comes back three seconds later on the new build. A
// zero exit would satisfy `on-failure` and the node would simply be gone.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Platform is what this agent reports at login, and what the coordinator
// matches a release against.
func Platform() string { return runtime.GOOS + "/" + runtime.GOARCH }

// exitUpgraded is the code the process exits with after a successful swap.
// Non-zero so `Restart=on-failure` restarts it, and distinct from 1 so a log
// reader can tell a deliberate replacement from a crash.
const exitUpgraded = 70

type upgradeReq struct {
	Version  string `json:"version"`
	Platform string `json:"platform"`
	SHA256   string `json:"sha256"`
	Path     string `json:"path"`
}

// upgrade downloads, verifies and installs a new binary, then exits.
//
// It returns an error rather than exiting when anything goes wrong before the
// swap, so the operator sees the reason on the command that asked for it —
// which is the whole reason this is operator-triggered.
func (c *Client) upgrade(ctx context.Context, payload json.RawMessage) (any, error) {
	var req upgradeReq
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, err
	}
	if req.Platform != Platform() {
		// Belt and braces: the coordinator picks by platform, and this refuses
		// the one mistake it could not take back.
		return nil, fmt.Errorf("release is for %s, this host is %s", req.Platform, Platform())
	}
	if req.Version == c.cfg.Version {
		return map[string]any{"upgraded": false, "reason": "already running " + req.Version}, nil
	}

	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate this binary: %w", err)
	}
	self, _ = filepath.EvalSymlinks(self)

	// Beside the target, not in /tmp: the rename at the end must be atomic,
	// which means the same filesystem. A cross-device rename fails, and the
	// fallback of copying is exactly the non-atomic write this avoids.
	tmp, err := os.CreateTemp(filepath.Dir(self), ".shabadoo-upgrade-*")
	if err != nil {
		return nil, fmt.Errorf("staging file: %w", err)
	}
	staged := tmp.Name()
	defer os.Remove(staged) // no-op once renamed

	sum, err := c.download(ctx, req.Path, tmp)
	tmp.Close()
	if err != nil {
		return nil, err
	}
	if sum != req.SHA256 {
		return nil, fmt.Errorf("checksum mismatch: got %s, expected %s", sum[:12], req.SHA256[:12])
	}
	if err := os.Chmod(staged, 0o755); err != nil {
		return nil, err
	}

	// Run it before trusting it. This is the check that makes the swap safe to
	// perform unattended.
	if err := verifyBinary(ctx, staged, req.Version); err != nil {
		return nil, fmt.Errorf("staged binary rejected: %w", err)
	}

	// Keep the outgoing binary. It is not an automatic rollback — nothing is
	// left running to perform one — but it turns recovery from "find the old
	// build" into one `mv` over SSH.
	prev := self + ".prev"
	_ = os.Remove(prev)
	if err := os.Link(self, prev); err != nil {
		// A hard link is preferable (no copy, same inode) but fails across
		// filesystems and on some mounts; not fatal, the swap is still safe.
		log.Printf("node: could not keep a copy of the previous binary: %v", err)
	}

	if err := os.Rename(staged, self); err != nil {
		return nil, fmt.Errorf("install %s: %w", self, err)
	}
	log.Printf("node: upgraded %s -> %s at %s; restarting", c.cfg.Version, req.Version, self)

	// Reply first, then exit. The operator is waiting on this result, and a
	// process that exits before answering looks to them exactly like a node
	// that died mid-upgrade.
	go func() {
		time.Sleep(500 * time.Millisecond)
		os.Exit(exitUpgraded)
	}()
	return map[string]any{"upgraded": true, "version": req.Version, "restarting": true}, nil
}

// download streams a release to w and returns its hex sha256.
func (c *Client) download(ctx context.Context, path string, w io.Writer) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.Coord+path, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.tokenValue())

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return "", fmt.Errorf("download: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	sum := sha256.New()
	if _, err := io.Copy(io.MultiWriter(w, sum), resp.Body); err != nil {
		return "", fmt.Errorf("download: %w", err)
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// verifyBinary runs `<path> version --json` and checks it is this program
// reporting the expected build.
//
// `version --json` exists because two copies of this program need a contract
// better than a human-readable line; this is the second caller, after the
// downgrade guard in setup.
func verifyBinary(ctx context.Context, path, want string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, path, "version", "--json").Output()
	if err != nil {
		return fmt.Errorf("it does not run here: %w", err)
	}
	var got struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		return fmt.Errorf("not a shabadoo binary (unparseable version output)")
	}
	if got.Version != want {
		return fmt.Errorf("reports version %q, expected %q", got.Version, want)
	}
	return nil
}
