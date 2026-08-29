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
	// Sign the NEW binary before restarting into it.
	//
	// On macOS a permission grant is recorded against the designated
	// requirement, which for an ad-hoc binary is a bare hash of the bytes — so
	// every upgrade silently revokes every grant a human has given. It cannot be
	// done at build time because darwin binaries are cross-compiled elsewhere
	// and `codesign` is macOS-only; this is the only moment the binary and an
	// identity are on the same machine.
	signed := ""
	if c.cfg.SignSelf != nil {
		signed = c.cfg.SignSelf(self)
		if signed != "" {
			log.Printf("node: %s", signed)
			// The changeover costs exactly one permission prompt per machine,
			// and saying so is the difference between a fix that looks like it
			// worked and one that looks like it did not. Reported from the
			// field: the binary on disk is signed immediately, but a
			// long-running process that has not restarted is still the
			// responsible identity, so macOS asks once at the moment it does.
			// Corrected by the node that measured it. "Grants persist
			// afterwards" is true of THIS process, which restarts here, and
			// optimistic for a session-hosted one: `shabadoo mcp` is a child of
			// a Claude session and nothing in an upgrade can restart it. So a
			// machine can be fully upgraded, correctly signed, and still be
			// running sessions whose consent identity is a binary no longer on
			// disk — for them the changeover has not begun.
			signed += ". macOS asks once more when each long-running process " +
				"restarts into this binary; for session-hosted processes " +
				"(shabadoo mcp) that is whenever the session restarts, which " +
				"may be much later — no upgrade can accelerate it"
		}
	}
	log.Printf("node: upgraded %s -> %s at %s; restarting", c.cfg.Version, req.Version, self)

	// Reply first, then exit. The operator is waiting on this result, and a
	// process that exits before answering looks to them exactly like a node
	// that died mid-upgrade.
	go func() {
		time.Sleep(500 * time.Millisecond)
		os.Exit(exitUpgraded)
	}()
	out := map[string]any{"upgraded": true, "version": req.Version, "restarting": true}
	if signed != "" {
		// Reported to the operator, not just logged. An unsigned binary on a Mac
		// revokes permissions somebody already granted, and the person running
		// the upgrade is the one who can do something about it.
		out["signing"] = signed
	}
	return out, nil
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

type installToolReq struct {
	Tool       string `json:"tool"`
	Version    string `json:"version"`
	Components []struct {
		Name   string `json:"name"`
		SHA256 string `json:"sha256"`
		Path   string `json:"path"`
	} `json:"components"`
}

// installTool downloads and installs another tool's release set.
//
// Simpler than upgrading this binary, and deliberately not sharing its path:
// there is no running process to replace and no restart to arrange. What it
// does share is the property that matters — nothing is moved into place until
// EVERY component has been fetched and checksummed.
//
// That staging is the whole design. A release set is a set precisely because
// half of it is useless: installing an orchestrator without its capture helper
// leaves a tool that starts, refuses to work, and blames a missing file. A
// download that fails on the second of two files must leave the first
// untouched rather than half-upgrading the host.
func (c *Client) installTool(ctx context.Context, payload json.RawMessage) (any, error) {
	var req installToolReq
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, err
	}
	if req.Tool == "" || len(req.Components) == 0 {
		return nil, fmt.Errorf("tool and components are required")
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("cannot locate my own binary, so I cannot tell where tools live: %w", err)
	}
	binDir := filepath.Dir(exe)

	// Stage everything first, beside the destination so the final move is a
	// rename within one filesystem — a cross-device rename fails, and copying
	// instead would be the non-atomic write this exists to avoid.
	staged := make([]string, 0, len(req.Components))
	defer func() {
		for _, p := range staged {
			os.Remove(p) // a no-op once renamed away
		}
	}()

	for _, comp := range req.Components {
		if comp.Name == "" || strings.ContainsAny(comp.Name, `/\`) {
			return nil, fmt.Errorf("refusing component name %q: a set member names a "+
				"file in the bin directory, never a path", comp.Name)
		}
		tmp := filepath.Join(binDir, "."+comp.Name+".incoming")
		f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return nil, err
		}
		staged = append(staged, tmp)
		sum, err := c.download(ctx, comp.Path, f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("downloading %s: %w", comp.Name, err)
		}
		if sum != comp.SHA256 {
			return nil, fmt.Errorf("%s checksum mismatch: got %s, expected %s",
				comp.Name, sum[:12], comp.SHA256[:12])
		}
	}

	// Every component is present and verified; now move them in. The previous
	// copy is kept as .prev for the same reason the binary's is — not an
	// automatic rollback, but one `mv` over SSH instead of a rebuild.
	installed := []string{}
	for i, comp := range req.Components {
		dst := filepath.Join(binDir, comp.Name)
		if _, err := os.Stat(dst); err == nil {
			_ = os.Rename(dst, dst+".prev")
		}
		if err := os.Rename(staged[i], dst); err != nil {
			return nil, fmt.Errorf("installing %s: %w", comp.Name, err)
		}
		installed = append(installed, comp.Name)
	}
	return map[string]any{
		"tool": req.Tool, "version": req.Version, "installed": installed, "dir": binDir,
	}, nil
}
