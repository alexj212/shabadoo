package main

// The node's command handlers: what a coordinator command actually does on
// this host. This is the seam between the transport (node) and the local
// machinery (tmux, claudelog, the launcher) — the same operations the flock's
// HTTP handlers performed, reached over the agent stream instead.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"shabadoo/claudelog"
	"shabadoo/hub"
	"shabadoo/node"
	"shabadoo/tmux"
)

// opArgs is the union of every command's arguments. One struct keeps the
// dispatch table flat; unused fields are simply absent from the JSON.
type opArgs struct {
	Session string   `json:"session,omitempty"`
	Window  int      `json:"window,omitempty"`
	Text    string   `json:"text,omitempty"`
	Enter   bool     `json:"enter,omitempty"`
	Command string   `json:"command,omitempty"`
	Keys    []string `json:"keys,omitempty"`
	Name    string   `json:"name,omitempty"`
	Path    string   `json:"path,omitempty"`
	Lines   int      `json:"lines,omitempty"`
	Color   bool     `json:"color,omitempty"`
}

// handleOp executes one coordinator command locally.
func handleOp(ctx context.Context, op string, payload json.RawMessage) (any, error) {
	var a opArgs
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &a); err != nil {
			return nil, fmt.Errorf("bad payload for %q: %w", op, err)
		}
	}

	switch op {
	case "list_sessions":
		return tmux.Sessions(ctx)

	case "capture":
		lines := min(max(a.Lines, 0), 5000)
		text, err := tmux.Capture(ctx, a.Session, a.Window, lines, a.Color)
		if err != nil {
			return nil, err
		}
		return map[string]string{"text": text}, nil

	case "select":
		return nil, tmux.Select(ctx, a.Session, a.Window)

	case "send":
		if err := guardDialog(ctx, a); err != nil {
			return nil, err
		}
		return nil, tmux.SendText(ctx, a.Session, a.Window, a.Text, a.Enter)

	case "command":
		if err := guardDialog(ctx, a); err != nil {
			return nil, err
		}
		if err := tmux.SendCommand(ctx, a.Session, a.Window, a.Command); err != nil {
			return nil, err
		}
		if isRemoteControl(a.Command) {
			dismissRemoteControl(ctx, a)
		}
		return nil, nil

	case "keys":
		return nil, tmux.SendRawKeys(ctx, a.Session, a.Window, a.Keys)

	case "input_state":
		state, err := inputState(ctx, a)
		if err != nil {
			return nil, err
		}
		return map[string]string{"state": state}, nil

	case "kill":
		return nil, tmux.KillWindow(ctx, a.Session, a.Window)

	case "reopen":
		if a.Name == "" {
			return nil, fmt.Errorf("reopen: name required")
		}
		return opReopen(ctx, a.Name)

	case "open":
		if a.Path == "" {
			return nil, fmt.Errorf("open: path required")
		}
		return opOpen(ctx, a.Path)

	case "folders":
		return folders(ctx)

	case "claude_session":
		if a.Path == "" {
			return nil, fmt.Errorf("claude_session: path required")
		}
		return claudelog.Summarize(a.Path)

	case "deliver":
		// A nudge: wake a session so its inbox-drain hook fires on the next
		// turn. Replaces the cron `nudge` mode, and lands instantly rather than
		// up to 15 minutes late.
		return nil, tmux.SendCommand(ctx, a.Session, a.Window, "check inbox")

	default:
		return nil, fmt.Errorf("unknown op %q", op)
	}
}

// reportSessions flattens this host's tmux windows into the shape the
// coordinator stores. The agent field is left empty deliberately — the
// coordinator fills it in from the authenticated connection, so a compromised
// agent cannot claim another node's windows. `serve` fills it in itself, being
// the only node it knows about.
func reportSessions(ctx context.Context) ([]hub.Session, error) {
	sessions, err := tmux.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	core := coreProjectPath()
	var out []hub.Session
	for _, s := range sessions {
		for _, w := range s.WindowsL {
			state, asking := windowInput(ctx, s.Name, w.Index)
			root := projectRoot(w.Path)
			out = append(out, hub.Session{
				InputState:  state,
				Asking:      asking,
				Kind:        kindOf(w, core),
				Description: projectDescription(root),
				SessionID:   sessionID(w),
				Project:     projectName(w.Path),
				CWD:         w.Path,
				Alias:       w.FriendlyName,
				Window:      fmt.Sprintf("%s:%d", s.Name, w.Index),
				Status:      statusOf(w),
				TmuxSession: s.Name,
				Index:       w.Index,
				Name:        w.Name,
				Command:     w.Command,
				Activity:    w.Activity,
				Panes:       w.Panes,
			})
		}
	}
	if out == nil {
		out = []hub.Session{}
	}
	return out, nil
}

// sessionID mirrors what claude.sh exports as CLAUDE_SESSION_ID, so a window
// and the MCP bridge subprocess inside it agree on the session's name.
func sessionID(w tmux.Window) string { return "claude-" + w.Name }

// kindOf classifies what is running in a window.
//
// The launcher appends an 8-hex suffix to every window it creates, which
// `FriendlyName` strips — so a name that differs from its friendly form was
// created by this toolchain and holds a Claude session. Anything else in tmux is
// some other program, and saying so is the whole point: until now the session
// table claimed `top` in a window was a project.
//
// Deliberately not keyed on the pane's current command. tmux reports that
// wrongly on at least one platform — a real Claude session on macOS reports a
// mangled value — so a classifier built on it would be wrong in exactly the
// place it matters.
func kindOf(w tmux.Window, corePath string) string {
	if corePath != "" && sameDir(w.Path, corePath) {
		return hub.KindCore
	}
	if w.FriendlyName != w.Name {
		return hub.KindClaude
	}
	return hub.KindWorker
}

// coreProjectPath is where this node's own main project lives: the agent's
// state directory plus the host label. Derived rather than configured — the
// agent is already holding both halves, so there is nothing to keep in sync.
func coreProjectPath() string {
	label := loadLaunchConfig().HostLabel
	if label == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(node.SocketPath()), label)
}

func sameDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if ra, err := filepath.EvalSymlinks(a); err == nil {
		a = ra
	}
	if rb, err := filepath.EvalSymlinks(b); err == nil {
		b = rb
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func statusOf(w tmux.Window) string {
	if w.Active {
		return "active"
	}
	return "idle"
}

// inputState reports what currently owns the pane's keyboard: the message
// composer, or a modal dialog. It reads the visible screen only — a dialog is
// drawn on screen, never in scrollback.
func inputState(ctx context.Context, a opArgs) (string, error) {
	pane, err := tmux.Capture(ctx, a.Session, a.Window, 0, false)
	if err != nil {
		return "", err
	}
	return tmux.InputState(pane), nil
}

// guardDialog refuses a text send when a modal owns the pane. Without it the
// send "succeeds" — tmux delivers the keys, the dialog eats them, and the
// caller is told their message went through when nothing was submitted. That
// silent no-op is the bug this guard exists to make loud.
//
// It deliberately does not dismiss the dialog itself: Escape is the obvious
// way to clear one, but Escape during a running turn interrupts Claude
// mid-work. Choosing to discard someone's in-flight turn is the operator's
// call, so the dashboard offers the key instead of pressing it.
//
// The single exception is dismissRemoteControl, and it is narrow on purpose:
// that modal is one this program just caused, identified by its own text
// before anything is pressed. "Dismiss a receipt we asked for" is not the same
// decision as "answer a question somebody has not read".
func guardDialog(ctx context.Context, a opArgs) error {
	state, err := inputState(ctx, a)
	if err != nil {
		// Capture failing is not evidence of a dialog; let the send proceed
		// rather than blocking on a broken check.
		return nil
	}
	if state == tmux.InputDialog {
		return fmt.Errorf("pane has a dialog open — it would swallow this text; " +
			"answer or dismiss it first (the answer keys are in the control deck)")
	}
	return nil
}

// Remote control drops on its own — the flag is set at launch and stays set,
// but the CLI's link to claude.ai does not survive indefinitely, and a session
// whose link has dropped VANISHES from the mobile app. So the tap that restores
// it has to come from here, and it should cost one interaction rather than
// three.
//
// `/remote-control` on a session that is already connected does not error; it
// opens a three-item menu — Disconnect this session / Show QR code / Continue —
// which then owns the keyboard until somebody dismisses it.
func isRemoteControl(cmd string) bool {
	return strings.EqualFold(strings.TrimSpace(cmd), "/remote-control")
}

// remoteControlMarkers identify that specific menu. Both must be present: the
// title alone appears in ordinary transcript text whenever anyone discusses
// this feature, and dismissing a modal because the word "Remote Control" is on
// screen would fire on a pane that is merely talking about it.
var remoteControlMarkers = []string{"Remote Control", "Disconnect this session"}

// dismissRemoteControl closes that menu, and closes ONLY that menu.
//
// It presses ESCAPE, never Enter, and the difference is the whole safety
// argument. Enter acts on whatever line the cursor is on, and one of the three
// lines is "Disconnect this session" — the exact opposite of what the operator
// asked for. The cursor defaults to Continue, but a default is a thing that
// changes in someone else's UI without telling us. Escape means "continue"
// unconditionally, which is what the modal's own footer says.
//
// Pressing Escape is otherwise something this project refuses to do, because
// Escape during a running turn discards work in flight. That does not apply
// here: it fires only after the pane is confirmed to hold this modal, and a
// modal owning the keyboard means no turn is running.
//
// Best effort throughout. A failure to dismiss leaves exactly today's
// behaviour — a menu waiting for a human — so nothing is worth reporting as an
// error, and the command itself already succeeded.
func dismissRemoteControl(ctx context.Context, a opArgs) {
	deadline := time.Now().Add(2 * time.Second)
	for {
		time.Sleep(250 * time.Millisecond)

		pane, err := tmux.Capture(ctx, a.Session, a.Window, 0, false)
		if err == nil && isRemoteControlDialog(pane) {
			tmux.SendRawKeys(ctx, a.Session, a.Window, []string{"Escape"})
			return
		}
		if time.Now().After(deadline) {
			return // it never appeared, or it is something else; leave it alone
		}
	}
}

// isRemoteControlDialog requires BOTH that the pane classifies as a modal and
// that it carries this menu's own text. Either test alone is too loose: the
// classifier does not say *which* modal, and the text can appear in a pane that
// is only discussing it.
func isRemoteControlDialog(pane string) bool {
	if tmux.InputState(pane) != tmux.InputDialog {
		return false
	}
	for _, m := range remoteControlMarkers {
		if !strings.Contains(pane, m) {
			return false
		}
	}
	return true
}

// Folder is a candidate place to start a session, for clients that cannot
// reasonably ask someone to type an absolute path — which is every phone.
type Folder struct {
	Path       string `json:"path"`
	Name       string `json:"name"`
	Source     string `json:"source"`                // "configured" or "recent"
	Open       bool   `json:"open"`                  // already has a window
	LastActive int64  `json:"last_active,omitempty"` // unix, recent folders only
}

// folders merges the three places a startable folder can come from: the boot
// list (a standing decision about what this host runs), the transcript store
// (anywhere a session has actually run), and the live windows (so the UI can
// say "already open" instead of spawning a duplicate).
//
// The boot list comes first regardless of recency: it is the curated set, and
// burying it under whatever folder was touched last would make the common
// case the hardest to reach.
func folders(ctx context.Context) ([]Folder, error) {
	open := map[string]bool{}
	if sessions, err := tmux.Sessions(ctx); err == nil {
		for _, s := range sessions {
			for _, w := range s.WindowsL {
				open[canonical(w.Path)] = true
			}
		}
	}

	var out []Folder
	seen := map[string]bool{}
	add := func(f Folder) {
		if f.Path == "" {
			return
		}
		// Match on the resolved path, display the configured spelling. The boot
		// list holds symlinks (~/some-project -> src.local/...) while tmux
		// reports where the shell actually is, so comparing raw strings shows a
		// folder as closed while its window is open — and invites a duplicate.
		key := canonical(f.Path)
		if seen[key] {
			return
		}
		// A folder that no longer exists is not startable. Transcripts outlive
		// the directories they describe, so "recent" accumulates deleted paths.
		if st, err := os.Stat(f.Path); err != nil || !st.IsDir() {
			return
		}
		seen[key] = true
		f.Name = filepath.Base(f.Path)
		f.Open = open[key]
		out = append(out, f)
	}

	for _, p := range configuredFolders() {
		add(Folder{Path: p, Source: "configured"})
	}
	projects, err := claudelog.Projects()
	if err != nil {
		return nil, err
	}
	for _, p := range projects {
		add(Folder{Path: p.Path, Source: "recent", LastActive: p.LastActive.Unix()})
	}
	return out, nil
}

// configuredFolders reads the boot list claude-startup.sh drives. Same path and
// same override the script uses, so the two never disagree about which file is
// authoritative.
func configuredFolders() []string {
	path := os.Getenv("CLAUDE_SESSIONS_LIST")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		path = filepath.Join(home, ".config", "claude-sessions", "folders")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// canonical resolves a path for comparison: symlinks followed, trailing
// separator dropped. Used only as a map key — the caller keeps the original
// spelling for display.
func canonical(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return strings.TrimSuffix(filepath.Clean(path), string(filepath.Separator))
}

// windowInputState classifies one window for the periodic report, so the
// dashboard can flag every blocked session at once instead of the browser
// probing panes one at a time.
//
// This is a capture-pane per window per report cycle. It is the visible screen
// only (no scrollback), which is the cheap form of the call, and it buys the
// only signal that a session is sitting on a prompt nobody has answered.
// A capture that fails reports "" rather than guessing.
func windowInput(ctx context.Context, session string, window int) (state, asking string) {
	pane, err := tmux.Capture(ctx, session, window, 0, false)
	if err != nil {
		return "", ""
	}
	state = tmux.InputState(pane)
	if state != tmux.InputDialog {
		return state, ""
	}
	// Only for a dialog, and from the SAME capture — a second call would be a
	// second screen, and the question could have been answered in between.
	return state, tmux.DialogPrompt(pane)
}

// opReopen and opOpen are the agent's window-lifecycle handlers. They call the
// launcher core directly; the previous implementation shelled out to a
// `claude-sessions` script, which is what let the two drift apart on window
// naming and launch flags.
func opReopen(ctx context.Context, pattern string) (any, error) {
	c := loadLaunchConfig()
	if !c.sessionExists(ctx) {
		return nil, fmt.Errorf("no tmux session %q — nothing running", c.SessionName)
	}
	name, err := c.resolveWindow(ctx, pattern)
	if err != nil {
		return nil, err
	}
	cwd, err := c.windowCWD(ctx, name)
	if err != nil {
		return nil, err
	}
	if err := tmux.KillWindowByName(ctx, c.SessionName, name); err != nil {
		return nil, err
	}
	if _, err := c.launch(ctx, cwd, false); err != nil {
		return nil, err
	}
	return map[string]string{"output": fmt.Sprintf("reopened %q in %s\n", name, cwd)}, nil
}

func opOpen(ctx context.Context, path string) (any, error) {
	c := loadLaunchConfig()
	cwd, err := resolveDir(path)
	if err != nil {
		return nil, err
	}
	name := c.windowName(cwd)
	if c.sessionExists(ctx) {
		names, err := c.windowNames(ctx)
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			if n == name {
				return map[string]string{
					"output": fmt.Sprintf("already open: %q (in %s)\n", name, cwd)}, nil
			}
		}
	}
	// Background: a remote open must not move the current window for whoever
	// is attached to this host's session.
	if _, err := c.launch(ctx, cwd, true); err != nil {
		return nil, err
	}
	return map[string]string{"output": fmt.Sprintf("opened %q in %s\n", name, cwd)}, nil
}
