package main

// The launcher core, ported from the `claude.sh` / `claude-sessions` scripts
// this binary used to shell out to.
//
// It exists as one type rather than two entry points because the scripts had
// drifted apart in two ways that both produced wrong behaviour:
//
//   - `claude-sessions` never read ~/.config/claude/env, so it resolved the
//     host label from `hostname -s` while `claude.sh` resolved it from the
//     file. Same folder, two window names, so the "already open" check missed
//     and you got a duplicate window. It was masked only because the systemd
//     unit sets CLAUDE_HOST_LABEL explicitly.
//   - `claude-sessions` launched claude without `-n` or `--remote-control
//     <alias>`, and without forwarding SSH_AUTH_SOCK, so a window opened from
//     the dashboard behaved differently from one opened by hand.
//
// Everything that starts a window now goes through launchConfig, so the two
// cannot diverge again.

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// launchConfig is the per-host launcher configuration — the same knobs
// claude.sh read from ~/.config/claude/env.
type launchConfig struct {
	Bin         string // the claude CLI
	Args        string // flags appended to every launch
	Resume      string // resume flag, empty to disable
	SessionName string // shared tmux session holding one window per folder
	HostLabel   string // short host segment in window names
}

// loadLaunchConfig resolves the launcher knobs the way claude.sh did.
//
// Order matters and is deliberately file-first: claude.sh `source`d the env
// file, and a plain `export X=...` overwrites whatever the process inherited.
// Reversing that would silently rename every window on a host whose service
// environment disagrees with its env file.
func loadLaunchConfig() launchConfig {
	file := envFileMap(claudeEnvPath())
	pick := func(key, def string) string {
		if v, ok := file[key]; ok && v != "" {
			return v
		}
		if v := os.Getenv(key); v != "" {
			return v
		}
		return def
	}

	c := launchConfig{
		Bin:         pick("CLAUDE_BIN", "claude"),
		Args:        pick("CLAUDE_ARGS", "--dangerously-skip-permissions"),
		SessionName: pick("CLAUDE_SESSION_NAME", "claude"),
	}
	// Resume is special: an empty value is a meaningful choice (disable), so
	// it cannot use the same "empty means unset" fallback as the others.
	if v, ok := file["CLAUDE_RESUME"]; ok {
		c.Resume = v
	} else if v, ok := os.LookupEnv("CLAUDE_RESUME"); ok {
		c.Resume = v
	} else {
		c.Resume = "--continue"
	}

	if v := pick("CLAUDE_HOST_LABEL", ""); v != "" {
		c.HostLabel = sanitizeLabel(v)
	} else {
		h, _, _ := strings.Cut(rawHostname(), ".")
		c.HostLabel = sanitizeLabel(strings.ToLower(h))
	}
	return c
}

func claudeEnvPath() string {
	if v := os.Getenv("CLAUDE_CONFIG_DIR"); v != "" {
		return filepath.Join(v, "env")
	}
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "claude", "env")
	}
	return filepath.Join(home(), ".config", "claude", "env")
}

// envFileMap reads every `export KEY=value` out of a shell env file. It does
// not evaluate the file: these hold simple assignments, and running arbitrary
// shell to learn a window name would be a poor trade.
func envFileMap(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = unquoteEnvValue(strings.TrimSpace(v))
	}
	return out
}

// unquoteEnvValue undoes shell quoting on a value from the env file.
//
// This used to be `strings.Trim(v, "\"'")` — trimming quote CHARACTERS from
// both ends, which is not the same as unquoting. It got the common cases right
// and mangled anything containing a quote: `--foo 'bar'` came back as
// `--foo 'bar`, silently, because the trailing quote was eaten as a delimiter.
// It also could not read back what shellQuote writes, which is how the bug
// surfaced — a value written by `config set` did not survive being read.
//
// Not a shell parser, deliberately. It handles the two forms this file
// actually contains: a single-quoted string with the '\'' idiom for an
// embedded quote, and a double-quoted string with backslash escapes.
func unquoteEnvValue(v string) string {
	if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
		return strings.ReplaceAll(v[1:len(v)-1], `'\''`, "'")
	}
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		r := strings.NewReplacer(`\"`, `"`, `\\`, `\`, "\\$", "$")
		return r.Replace(v[1 : len(v)-1])
	}
	return v
}

// windowName is the stable per-folder identifier: <project>-<host>-<8 hex>.
// The hash disambiguates two folders with the same basename on one host; the
// host segment disambiguates the same project across hosts in the session list.
func (c launchConfig) windowName(cwd string) string {
	sum := sha1.Sum([]byte(cwd))
	return fmt.Sprintf("%s-%s", c.labelFor(cwd), hex.EncodeToString(sum[:])[:8])
}

// labelFor is the readable half of a window's name: "<project>-<host>", except
// for the node's own main project, which is NAMED for the host and would
// otherwise render as "wsl-wsl". Saying it twice is noise in every list the
// operator reads.
//
// Safe to special-case despite windowName being a compatibility contract: it
// only differs for a folder whose base name already is the host label, and no
// such window exists on any node — checked before it was written, because
// changing this formula orphans every window that used the old one.
func (c launchConfig) labelFor(cwd string) string {
	base := sanitizeLabel(filepath.Base(cwd))
	if base == c.HostLabel {
		return base
	}
	return base + "-" + c.HostLabel
}

// sessionAlias is the human-readable half of the window name, used for the
// display name and the remote-control alias shown in the iOS / web app.
// Collisions are possible across two folders sharing a basename; that is
// accepted for readability, as it was in the script.
func (c launchConfig) sessionAlias(cwd string) string {
	return c.labelFor(cwd)
}

// hasTranscripts reports whether Claude has prior history for this folder,
// which is what makes --continue meaningful. Claude encodes the cwd by
// replacing every '/' with '-'.
func hasTranscripts(cwd string) bool {
	dir := filepath.Join(home(), ".claude", "projects", strings.ReplaceAll(cwd, "/", "-"))
	matches, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	return err == nil && len(matches) > 0
}

// shellQuote wraps a value for the single command string tmux hands to sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// claudeCommand builds the shell command tmux runs in the new window.
func (c launchConfig) claudeCommand(cwd string) string {
	alias := c.sessionAlias(cwd)
	parts := []string{c.Bin}
	if c.Args != "" {
		parts = append(parts, c.Args)
	}
	parts = append(parts, "-n", shellQuote(alias), "--remote-control", shellQuote(alias))
	if c.Resume != "" && hasTranscripts(cwd) {
		parts = append(parts, c.Resume)
	}
	return strings.Join(parts, " ")
}

func (c launchConfig) sessionExists(ctx context.Context) bool {
	return exec.CommandContext(ctx, "tmux", "has-session", "-t="+c.SessionName).Run() == nil
}

// windowNames lists the raw window names in the shared session.
func (c launchConfig) windowNames(ctx context.Context) ([]string, error) {
	out, err := exec.CommandContext(ctx, "tmux",
		"list-windows", "-t", c.SessionName, "-F", "#{window_name}").Output()
	if err != nil {
		return nil, fmt.Errorf("list windows in %q: %w", c.SessionName, err)
	}
	var names []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			names = append(names, l)
		}
	}
	return names, nil
}

// envArgs are the per-window variables tmux must be told explicitly. The tmux
// server captures its environment once at first-session creation, so a window
// created later would otherwise inherit a stale snapshot — including a dead
// ssh-agent socket.
func (c launchConfig) envArgs(cwd string) []string {
	name := c.windowName(cwd)
	args := []string{
		"-e", "CLAUDE_SESSION_ID=claude-" + name,
		"-e", "CLAUDE_SESSION_PROJECT=" + filepath.Base(cwd),
		"-e", "CLAUDE_SESSION_ALIAS=" + c.sessionAlias(cwd),
	}
	for _, k := range []string{"SSH_AUTH_SOCK", "SSH_AGENT_PID"} {
		if v := os.Getenv(k); v != "" {
			args = append(args, "-e", k+"="+v)
		}
	}
	return args
}

// launch creates the window for cwd, creating the shared session if needed.
// background leaves the session's current window selected (tmux new-window -d),
// which is what opening a folder remotely should do — it must not yank the
// window out from under whoever is attached.
func (c launchConfig) launch(ctx context.Context, cwd string, background bool) (string, error) {
	name := c.windowName(cwd)
	cmd := c.claudeCommand(cwd)

	var args []string
	if !c.sessionExists(ctx) {
		// new-session is always detached; background is irrelevant here.
		args = append([]string{"new-session", "-d", "-s", c.SessionName, "-n", name, "-c", cwd},
			c.envArgs(cwd)...)
	} else {
		args = append(args, "new-window")
		if background {
			args = append(args, "-d")
		}
		args = append(args, "-t", c.SessionName, "-n", name, "-c", cwd)
		args = append(args, c.envArgs(cwd)...)
	}
	args = append(args, cmd)

	if out, err := exec.CommandContext(ctx, "tmux", args...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("tmux: %s", strings.TrimSpace(string(out)))
	}
	c.refreshSessionEnv(ctx)
	c.applyDisplay(ctx)
	return name, nil
}

// refreshSessionEnv updates the session-level environment so windows created
// later — by the dashboard, or a manual new-window — inherit the *current*
// ssh-agent rather than whatever the tmux server started with.
func (c launchConfig) refreshSessionEnv(ctx context.Context) {
	for _, k := range []string{"SSH_AUTH_SOCK", "SSH_AGENT_PID"} {
		if v := os.Getenv(k); v != "" {
			// Best-effort: a missing session here is not worth failing a launch.
			exec.CommandContext(ctx, "tmux", "set-environment", "-t", c.SessionName, k, v).Run()
		}
	}
}

// applyDisplay strips the trailing -<hash> from the rendered window name in
// the status bar and terminal title. The real window name keeps the hash for
// tmux's own bookkeeping; only the rendering changes.
//
// The regex is `[a-f0-9]+$` and not `[0-9a-f]{8}$` because tmux's format
// parser treats `{...}` as nesting and chokes on the repetition count.
func (c launchConfig) applyDisplay(ctx context.Context) {
	friendly := `#{s/-[a-f0-9]+$//:#{window_name}}`
	exec.CommandContext(ctx, "tmux",
		"set-option", "-t", c.SessionName, "set-titles", "on", ";",
		"set-option", "-t", c.SessionName, "set-titles-string", "claude: "+friendly, ";",
		"set-window-option", "-t", c.SessionName, "-g", "window-status-format", "#I:"+friendly, ";",
		"set-window-option", "-t", c.SessionName, "-g", "window-status-current-format", "#I:"+friendly+"*",
	).Run()
}

// resolveWindow maps a pattern to exactly one window. An exact name wins
// outright; otherwise a substring must match uniquely. An ambiguous pattern is
// an error listing the candidates, never a guess — picking one eventually
// kills the wrong session.
func (c launchConfig) resolveWindow(ctx context.Context, pattern string) (string, error) {
	// Defence in depth for the caller-side guard: "" matches every name, so an
	// empty pattern would resolve uniquely against a single window and kill it.
	if strings.TrimSpace(pattern) == "" {
		return "", fmt.Errorf("empty window pattern")
	}
	names, err := c.windowNames(ctx)
	if err != nil {
		return "", err
	}
	if len(names) == 0 {
		return "", fmt.Errorf("session %q has no windows", c.SessionName)
	}
	for _, n := range names {
		if n == pattern {
			return n, nil
		}
	}
	var matches []string
	for _, n := range names {
		if strings.Contains(n, pattern) {
			matches = append(matches, n)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no window matches %q", pattern)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("multiple windows match %q:\n  %s\nrefine the pattern",
			pattern, strings.Join(matches, "\n  "))
	}
}

// windowCWD reports the working directory of a named window, which is how
// reopen relaunches in the same place after killing it.
func (c launchConfig) windowCWD(ctx context.Context, name string) (string, error) {
	out, err := exec.CommandContext(ctx, "tmux", "display-message", "-p",
		"-t", c.SessionName+":"+name, "#{pane_current_path}").Output()
	if err != nil {
		return "", fmt.Errorf("cwd for %q: %w", name, err)
	}
	cwd := strings.TrimSpace(string(out))
	if cwd == "" {
		return "", fmt.Errorf("could not determine cwd for %q", name)
	}
	if fi, err := os.Stat(cwd); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("cwd for %q is not a directory: %s", name, cwd)
	}
	return cwd, nil
}
