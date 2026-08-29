// Package tmux shells out to the tmux CLI to read session/window state and
// control it (select-window, send-keys, kill-window, capture-pane).
package tmux

import (
	"unicode"
	"unicode/utf8"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// FieldSep delimits the fields of a tmux -F format.
//
// It used to be the ASCII unit separator (0x1F), on the reasoning that tmux
// never emits it inside a name or path. True, and irrelevant: **tmux rewrites
// every non-printable byte in -F output to "_" when it is not in UTF-8 mode.**
// A service manager supplies no locale, so under systemd or launchd the
// separator silently became an underscore, every row failed its field count,
// and the agent reported an empty window list while looking perfectly healthy.
// A Mac node sat on the dashboard as "online, zero sessions" for half an hour
// because of it.
//
// Setting LANG in the generated units fixes that instance. This fixes the
// class: a printable delimiter survives any locale, so nothing has to be true
// about the environment for parsing to work.
//
// Three characters that are individually plausible in a path but absurd
// together. Two further defences make a collision harmless rather than
// corrupting: every parser uses SplitN with a known field count, and the field
// most likely to contain surprising text — pane_current_path — is always LAST,
// so a stray separator lands inside it instead of shifting every column.
const FieldSep = "|@|"

// Session is one tmux session.
type Session struct {
	Name     string   `json:"name"`
	Windows  int      `json:"windows"`
	Attached bool     `json:"attached"`
	Created  int64    `json:"created"` // unix seconds
	WindowsL []Window `json:"windows_list"`
}

// Window is one window inside a session.
type Window struct {
	Session      string `json:"session"`
	Index        int    `json:"index"`
	Name         string `json:"name"`          // raw tmux window name
	FriendlyName string `json:"friendly_name"` // name with the claude.sh -<hash> suffix stripped
	Active       bool   `json:"active"`
	Panes        int    `json:"panes"`
	Activity     int64  `json:"activity"` // unix seconds of last activity
	Command      string `json:"command"`  // pane_current_command of the active pane
	Path         string `json:"path"`     // pane_current_path of the active pane
	PID          int    `json:"pid"`      // pane_pid of the active pane
}

// hashSuffix matches the trailing "-<8 hex>" that claude.sh appends to window
// names (e.g. "homelab-wsl-4b602ded"). Stripping it yields the friendly alias.
var hashSuffix = regexp.MustCompile(`-[0-9a-f]{8}$`)

func friendly(name string) string {
	return hashSuffix.ReplaceAllString(name, "")
}

// noServer reports whether err is tmux's "no server running" condition, which
// we treat as "zero sessions" rather than a hard error.
func noServer(err error, out string) bool {
	if err == nil {
		return false
	}
	return strings.Contains(out, "no server running") ||
		strings.Contains(out, "no current session") ||
		strings.Contains(out, "error connecting")
}

func run(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "tmux", args...).CombinedOutput()
	return string(out), err
}

// Sessions returns every tmux session with its windows attached. If no tmux
// server is running it returns an empty slice and nil error.
func Sessions(ctx context.Context) ([]Session, error) {
	// session_name last: it is the only free-text field here, so SplitN below
	// lets a separator inside it stay inside it.
	sOut, err := run(ctx, "list-sessions", "-F",
		strings.Join([]string{
			"#{session_windows}", "#{session_attached}",
			"#{session_created}", "#{session_name}",
		}, FieldSep))
	if err != nil {
		if noServer(err, sOut) {
			return []Session{}, nil
		}
		return nil, err
	}

	windows, err := Windows(ctx)
	if err != nil {
		return nil, err
	}
	byName := map[string][]Window{}
	for _, w := range windows {
		byName[w.Session] = append(byName[w.Session], w)
	}

	var sessions []Session
	unparsed := 0
	for _, line := range splitLines(sOut) {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.SplitN(line, FieldSep, 4)
		if len(f) < 4 {
			unparsed++
			continue
		}
		sessions = append(sessions, Session{
			Windows:  atoi(f[0]),
			Attached: atoi(f[1]) > 0,
			Created:  atoi64(f[2]),
			Name:     f[3],
			WindowsL: byName[f[3]],
		})
	}
	if len(sessions) == 0 && unparsed > 0 {
		return nil, unparsedError("session", unparsed, sOut)
	}
	return sessions, nil
}

// Windows returns every window across every session.
func Windows(ctx context.Context) ([]Window, error) {
	// pane_current_path LAST: a directory name is the one field here that can
	// contain genuinely arbitrary text, so SplitN keeps any separator inside it
	// from shifting every other column.
	out, err := run(ctx, "list-windows", "-a", "-F",
		strings.Join([]string{
			"#{session_name}", "#{window_index}", "#{window_name}",
			"#{window_active}", "#{window_panes}", "#{window_activity}",
			"#{pane_current_command}", "#{pane_pid}", "#{pane_current_path}",
		}, FieldSep))
	if err != nil {
		if noServer(err, out) {
			return []Window{}, nil
		}
		return nil, err
	}

	return parseWindows(out)
}

// parseWindows splits `list-windows` output. Separated from the tmux call so
// the locale-mangling case can be tested without a tmux server.
func parseWindows(out string) ([]Window, error) {
	var windows []Window
	unparsed := 0
	for _, line := range splitLines(out) {
		// A blank line is not malformed output — counting it would raise the
		// alarm below on a host that simply has no windows.
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.SplitN(line, FieldSep, 9)
		if len(f) < 9 {
			unparsed++
			continue
		}
		windows = append(windows, Window{
			Session:      f[0],
			Index:        atoi(f[1]),
			Name:         f[2],
			FriendlyName: friendly(f[2]),
			Active:       f[3] == "1",
			Panes:        atoi(f[4]),
			Activity:     atoi64(f[5]),
			Command:      f[6],
			PID:          atoi(f[7]),
			Path:         f[8],
		})
	}
	if len(windows) == 0 && unparsed > 0 {
		return nil, unparsedError("window", unparsed, out)
	}
	return windows, nil
}

// Pane is one pane inside a window.
type Pane struct {
	Session string `json:"session"`
	Window  int    `json:"window"`
	Index   int    `json:"index"`
	Active  bool   `json:"active"`
	Command string `json:"command"`
	PID     int    `json:"pid"`
	Path    string `json:"path"`
}

// Panes returns every pane across every session.
//
// Windows() reports the ACTIVE pane's command, path and pid, which is the right
// answer for a single-pane window and a silent lie for any other. This is what
// makes a split window legible: each pane has its own working directory, and
// therefore its own project.
func Panes(ctx context.Context) ([]Pane, error) {
	out, err := run(ctx, "list-panes", "-a", "-F",
		strings.Join([]string{
			"#{session_name}", "#{window_index}", "#{pane_index}",
			"#{pane_active}", "#{pane_current_command}", "#{pane_pid}",
			"#{pane_current_path}",
		}, FieldSep))
	if err != nil {
		if noServer(err, out) {
			return []Pane{}, nil
		}
		return nil, err
	}
	return parsePanes(out)
}

// parsePanes splits `list-panes` output, separated from the call for the same
// reason parseWindows is: the locale-mangling failure is testable without tmux.
func parsePanes(out string) ([]Pane, error) {
	var panes []Pane
	unparsed := 0
	for _, line := range splitLines(out) {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.SplitN(line, FieldSep, 7)
		if len(f) < 7 {
			unparsed++
			continue
		}
		panes = append(panes, Pane{
			Session: f[0], Window: atoi(f[1]), Index: atoi(f[2]),
			Active: f[3] == "1", Command: f[4], PID: atoi(f[5]), Path: f[6],
		})
	}
	if len(panes) == 0 && unparsed > 0 {
		return nil, unparsedError("pane", unparsed, out)
	}
	return panes, nil
}

// unparsedError explains output that tmux produced but this parser could not
// split. It exists because the alternative — dropping the rows and returning an
// empty list — is indistinguishable from a host with nothing running, and that
// is exactly how it presented: an agent that logged in, streamed, and answered
// commands, while the dashboard showed it owning zero sessions.
//
// The cause is worth naming in the message, because it is not guessable: tmux
// replaces every non-printable byte in -F output with "_" when it is not in
// UTF-8 mode, which silently destroys the 0x1F separator these formats are
// built on. A login shell has a locale so this never appears by hand; a service
// manager supplies none, so it appears only once the agent runs as a service.
func unparsedError(kind string, n int, out string) error {
	return fmt.Errorf(
		"tmux returned %d %s line(s) that did not parse: the 0x1f field separator is gone, "+
			"which is what tmux does to non-printable bytes when it is not in UTF-8 mode — "+
			"set a UTF-8 locale (LANG=en_US.UTF-8) in this process's environment; first line: %q",
		n, kind, firstLine(out))
}

// target builds a "session:window" address for tmux's -t flag.
func target(session string, window int) string {
	return session + ":" + strconv.Itoa(window)
}

// paneTarget addresses a specific pane.
//
// `session:window` is resolved by tmux to whichever pane is ACTIVE, so a
// multi-pane window silently accepts writes aimed at a different one. That is
// the bug this exists to remove: a keystroke landing in the wrong pane looks
// exactly like one landing in the right pane, from every side.
//
// A negative pane keeps the old meaning — the active pane — which is what a
// caller that has never heard of panes wants and is the only safe answer when
// nobody said which.
func paneTarget(session string, window, pane int) string {
	if pane < 0 {
		return target(session, window)
	}
	return target(session, window) + "." + strconv.Itoa(pane)
}

// Select makes the given window the active one in its session, switching the
// attached client's view to it.
func Select(ctx context.Context, session string, window, pane int) error {
	if session == "" {
		return fmt.Errorf("session required")
	}
	if out, err := run(ctx, "select-window", "-t", target(session, window)); err != nil {
		return fmt.Errorf("select-window: %s", firstLine(out))
	}
	// Selecting the window is not enough once a window holds more than one
	// pane: the keyboard follows the ACTIVE pane, so landing on the window and
	// leaving the wrong pane focused is the same class of miss this addressing
	// exists to remove.
	if pane >= 0 {
		if out, err := run(ctx, "select-pane", "-t", paneTarget(session, window, pane)); err != nil {
			return fmt.Errorf("select-pane: %s", firstLine(out))
		}
	}
	return nil
}

// Input states a pane can be in, as far as a remote sender needs to care.
const (
	InputComposer = "composer" // normal prompt line; text + Enter submits
	InputDialog   = "dialog"   // a modal owns the keyboard; text is swallowed
)

// dialogMarkers are strings Claude Code's own modals print in their footer.
// A modal (permission prompt, /status, plan approval, the trust dialog) takes
// over the keyboard: text sent to the pane vanishes and Enter is consumed by
// the dialog rather than submitting a message. Detecting that is the
// difference between "your message was sent" and a silent no-op.
//
// This is a heuristic over another program's UI, so it is deliberately
// one-sided: only strong, footer-level markers count, and anything unrecognised
// is treated as a normal composer. A false "composer" sends into a dialog (the
// status quo); a false "dialog" would block legitimate messages, which is
// worse.
var dialogMarkers = []string{
	"Esc to cancel",
	"Enter to confirm",
	"Do you want to proceed",
	"Do you want to make this edit",
	"esc to reject",

	// Claude Code's SELECT-LIST modals use a different footer, and missing it
	// was a false negative rather than a missed nicety: `/remote-control` on an
	// already-connected session puts up a three-item menu whose footer reads
	// "Enter to select · Esc to continue", and none of the markers above match
	// it. So the pane reported `composer` while a modal owned the keyboard —
	// the precise silent no-op guardDialog exists to prevent, with the guard
	// itself waved through. Found from a screenshot of the real dialog, which
	// is the only way this class of bug ever surfaces.
	"Enter to select",
	"Esc to continue",
}

// InputState classifies what owns the keyboard in a captured pane, given the
// pane's visible text. Only the tail matters — a dialog's footer is the last
// thing drawn — and scanning further back would match a modal that has since
// been dismissed and scrolled up.
func InputState(pane string) string {
	lines := strings.Split(strings.TrimRight(pane, "\n"), "\n")
	if n := len(lines); n > 20 {
		lines = lines[n-20:]
	}
	// A visible composer box wins over any marker above it. The markers are
	// footer text, and footer text is also just words — a pane whose TRANSCRIPT
	// quotes one is not a modal. That is not hypothetical: the session that
	// added "Enter to select" to this list was discussing it on screen while
	// its own agent classified the pane, and a false dialog is the expensive
	// direction (it refuses real messages and raises a blocked-session alert
	// about nothing).
	//
	// The input row is the discriminator rather than the box outline, because
	// permission modals are boxed too. Claude Code draws the composer's prompt
	// with an ASCII ">", while menu selection uses "❯".
	if hasComposerRow(lines) {
		return InputComposer
	}

	tail := strings.Join(lines, "\n")
	for _, m := range dialogMarkers {
		if strings.Contains(tail, m) {
			return InputDialog
		}
	}
	return InputComposer
}

// hasComposerRow reports whether the pane's last few lines hold Claude Code's
// input row. Only the last few, because it is drawn at the very bottom when the
// composer has the keyboard; finding one higher up means it has since been
// replaced by something else.
//
// It shares composerDraft with ComposerBusy deliberately. This used to require
// a "│" and an ASCII "> ", which darwin draws neither of — so the discriminator
// that exists to stop footer text being read as a dialog was inoperative on that
// platform, and InputState there was returning "composer" only because that is
// what it falls through to. Two readers of the same row must agree on what a
// row is.
func hasComposerRow(lines []string) bool {
	if n := len(lines); n > 6 {
		lines = lines[n-6:]
	}
	for _, l := range lines {
		if _, ok := composerDraft(l); ok {
			return true
		}
	}
	return false
}

// ComposerBusy reports whether the pane's input row already holds text.
//
// It exists because a nudge types into somebody else's terminal. The wire for
// that is SendCommand, which begins with C-u — it CLEARS the input line before
// typing, which is right for an operator who chose to send a command and wrong
// for an automatic nudge triggered by a peer's mail: a human half-way through
// composing a prompt loses it, silently, because somebody else sent a message.
//
// So the nudge asks first, and skips when the answer is yes. Nothing is lost by
// skipping: the mail is already stored, and the drain hook fires on that
// session's next prompt regardless. A nudge is an optimisation on WHEN mail is
// noticed, never the thing that delivers it.
//
// Unrecognised means busy. This is the opposite default from InputState, on
// purpose: there a false dialog refuses real messages, while here a false busy
// costs only the promptness of a nudge whose mail is waiting anyway.
func ComposerBusy(pane string) bool {
	lines := strings.Split(strings.TrimRight(pane, "\n"), "\n")
	if n := len(lines); n > 6 {
		lines = lines[n-6:]
	}
	for _, l := range lines {
		draft, ok := composerDraft(l)
		if !ok {
			continue
		}
		return draft != ""
	}
	return true // no input row found — do not type here
}

// composerDraft splits an input row into the prompt marker and whatever has
// been typed after it, or reports that this line is not an input row.
//
// It matches on STRUCTURE rather than on either machine's rendering, because
// the two disagree completely and a third will disagree again:
//
//	linux   │ > half a question            │      boxed, ASCII ">"
//	darwin  ❯ half a question                     no box at all, U+276F
//
// The first version encoded the linux form — a line containing "│" and "> ",
// with the draft bounded by the closing "│". On darwin there is no "│" anywhere
// in the pane (grep counts zero), the glyph is "❯", and the row has no closing
// delimiter at all. So it matched nothing, fell through to "cannot tell", and
// that node stopped being nudged entirely while looking perfectly healthy. A
// check that never fires is indistinguishable from one that finds nothing
// wrong.
//
// The invariant that holds on both: after any box edge, the first glyph on the
// input row is a prompt marker, and the draft is the rest of the line. A
// trailing box edge is stripped when present and simply absent when not.
func composerDraft(line string) (string, bool) {
	boxed := false
	rest := line
	if t := strings.TrimLeft(rest, " \t"); strings.HasPrefix(t, "│") {
		boxed = true
		rest = strings.TrimLeft(strings.TrimPrefix(t, "│"), " \t")
	}
	// Unboxed, the input row starts at column 0. A menu's selection cursor is
	// indented among its options — that is what separates darwin's composer
	// "❯ text" from the "  > Continue" of a select list, now that the box can
	// no longer do it.
	if !boxed && rest != strings.TrimLeft(rest, " \t") {
		return "", false
	}

	var marker string
	switch {
	case strings.HasPrefix(rest, "❯"):
		marker = "❯"
	case strings.HasPrefix(rest, ">"):
		marker = ">"
	default:
		return "", false
	}
	rest = rest[len(marker):]
	// The marker must be followed by WHITESPACE — and not necessarily a plain
	// space. Claude Code separates the prompt from the draft with U+00A0, a
	// NON-BREAKING space, which `strings.HasPrefix(rest, " ")` rejects.
	//
	// That single byte pair disabled every nudge on the fleet for ten hours.
	// No input row matched, ComposerBusy fell through to "cannot tell", the
	// nudge skipped every time, and mail sat undrained until a human asked a
	// session how it was doing — which is what surfaced it. The unit tests
	// passed throughout, because the fixtures were written by hand from what I
	// believed a pane looked like rather than captured from one.
	if rest != "" {
		r, _ := utf8.DecodeRuneInString(rest)
		if !unicode.IsSpace(r) && r != '\u00a0' {
			return "", false
		}
	}
	// A trailing box edge, where one is drawn.
	if i := strings.LastIndex(rest, "│"); i >= 0 {
		rest = rest[:i]
	}
	rest = strings.TrimSpace(rest)

	// "❯ 1. Resume from summary" is a menu, not a draft — and it is the exact
	// row of the resume prompt, whose default action spends usage limits. A
	// numbered first token means somebody is choosing, not typing.
	if isMenuItem(rest) {
		return "", false
	}
	return rest, true
}

// isMenuItem reports whether the text after a marker reads as "1. Something".
func isMenuItem(s string) bool {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return i > 0 && strings.HasPrefix(s[i:], ". ")
}

// DialogPrompt extracts the question a modal is asking, from the same captured
// pane InputState classified.
//
// This closes the last hop in the loop this project exists for. The dashboard
// could say a session was blocked and never what it was blocked ON, so
// answering meant selecting the pane and reading a transcript — and "approve
// something you have not read" is the one interaction this tool should be most
// careful about, given the panes run with permissions disabled.
//
// A heuristic over another program's UI, so it fails the same way InputState
// does: unrecognised returns "" and every caller simply shows nothing, which is
// the behaviour that existed before this. A wrong question is far worse than no
// question — somebody would answer it.
func DialogPrompt(pane string) string {
	lines := strings.Split(strings.TrimRight(pane, "\n"), "\n")
	if n := len(lines); n > 30 {
		lines = lines[n-30:]
	}

	// Search backwards: a pane can hold an earlier answered prompt above the
	// live one, and the live one is always nearer the bottom.
	for i := len(lines) - 1; i >= 0; i-- {
		raw := lines[i]
		q := strings.TrimSpace(stripBox(raw))
		if q == "" || len(q) < 8 {
			continue
		}
		// Two ways to be confident this is the modal's question rather than
		// prose that happens to end in a question mark:
		//
		//   - it opens with the phrasing Claude Code's prompts use, or
		//   - it sits INSIDE the modal's box frame and ends in a question mark.
		//
		// A trailing "?" alone is not enough. "Done. Anything else?" is
		// something the assistant says, and extracting it would put a question
		// nobody is being asked onto a dashboard and into a notification.
		framed := strings.ContainsAny(raw, "│┃|") && strings.HasSuffix(q, "?")
		phrased := strings.HasPrefix(strings.ToLower(q), "do you want")
		if !framed && !phrased {
			continue
		}
		if len(q) > dialogPromptMax {
			q = strings.TrimSpace(q[:dialogPromptMax]) + "…"
		}
		return q
	}
	return ""
}

// dialogPromptMax bounds the extracted question. It rides in every agent report
// and into a phone notification; a wrapped paragraph helps nobody on a lock
// screen.
const dialogPromptMax = 120

// stripBox removes the box-drawing characters Claude Code's modals are framed
// in, so the question comes out as a sentence rather than as a row of a table.
func stripBox(line string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		switch {
		case r >= 0x2500 && r <= 0x257F: // box drawing
			return -1
		case r == '❯' || r == '›' || r == '*':
			return -1
		}
		return r
	}, line))
}

// SendRawKeys sends key names (Enter, Escape, Up, "1", "y") to a pane without
// clearing the input line first. This is how a dialog gets answered: the
// keystroke is the whole message, and a C-u would be one more key the modal
// has to interpret.
//
// Key names are passed to tmux verbatim, so this can send any key the terminal
// can — including C-c. That is the point: answering a prompt from a phone
// needs the same keys the keyboard has.
func SendRawKeys(ctx context.Context, session string, window, pane int, keys []string) error {
	if session == "" {
		return fmt.Errorf("session required")
	}
	if len(keys) == 0 {
		return fmt.Errorf("keys required")
	}
	args := append([]string{"send-keys", "-t", paneTarget(session, window, pane)}, keys...)
	if out, err := run(ctx, args...); err != nil {
		return fmt.Errorf("send-keys %v: %s", keys, firstLine(out))
	}
	return nil
}

// SendText clears the pane's input line (C-u), then types literal text into
// it. When enter is true it follows with an Enter keypress (submitting the
// line). The leading C-u means the sent text replaces whatever was already
// half-typed in the pane rather than appending to it.
//
// The text is sent with send-keys -l (literal): tmux does not interpret key
// names, so a message containing "Enter" or "C-c" is typed verbatim rather
// than acted on. Args are passed via exec without a shell, so there is no
// shell-injection surface; "--" guards against text that starts with "-".
func SendText(ctx context.Context, session string, window, pane int, text string, enter bool) error {
	if session == "" {
		return fmt.Errorf("session required")
	}
	t := paneTarget(session, window, pane)
	// Clear anything already on the pane's input line first, so the sent text
	// replaces it rather than appending to whatever was half-typed there.
	if out, err := run(ctx, "send-keys", "-t", t, "C-u"); err != nil {
		return fmt.Errorf("send-keys C-u: %s", firstLine(out))
	}
	if text != "" {
		if out, err := run(ctx, "send-keys", "-t", t, "-l", "--", text); err != nil {
			return fmt.Errorf("send-keys: %s", firstLine(out))
		}
	}
	if enter {
		if out, err := run(ctx, "send-keys", "-t", t, "Enter"); err != nil {
			return fmt.Errorf("send-keys Enter: %s", firstLine(out))
		}
	}
	return nil
}

// SendCommand submits a command line (e.g. a slash command like "/clear" or
// "/remote-control") to the pane. It first sends C-u to clear anything the
// pane already has on its input line, types the command literally, then Enter.
func SendCommand(ctx context.Context, session string, window, pane int, cmd string) error {
	if session == "" {
		return fmt.Errorf("session required")
	}
	if cmd == "" {
		return fmt.Errorf("command required")
	}
	t := paneTarget(session, window, pane)
	if out, err := run(ctx, "send-keys", "-t", t, "C-u"); err != nil {
		return fmt.Errorf("send-keys C-u: %s", firstLine(out))
	}
	if out, err := run(ctx, "send-keys", "-t", t, "-l", "--", cmd); err != nil {
		return fmt.Errorf("send-keys: %s", firstLine(out))
	}
	if out, err := run(ctx, "send-keys", "-t", t, "Enter"); err != nil {
		return fmt.Errorf("send-keys Enter: %s", firstLine(out))
	}
	return nil
}

// Capture returns the pane's buffer: the visible screen plus up to `history`
// lines of scrollback above it. history <= 0 captures the visible screen only.
// When color is true it includes ANSI SGR escape sequences (capture-pane -e)
// so the caller can render the terminal's colors. Read-only — capture-pane
// does not disturb the pane.
func Capture(ctx context.Context, session string, window, pane, history int, color bool) (string, error) {
	if session == "" {
		return "", fmt.Errorf("session required")
	}
	args := []string{"capture-pane", "-p", "-t", paneTarget(session, window, pane)}
	if color {
		args = append(args, "-e")
	}
	if history > 0 {
		args = append(args, "-S", "-"+strconv.Itoa(history))
	}
	out, err := run(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("capture-pane: %s", firstLine(out))
	}
	return out, nil
}

// KillWindow closes a window.
func KillWindow(ctx context.Context, session string, window int) error {
	if session == "" {
		return fmt.Errorf("session required")
	}
	if out, err := run(ctx, "kill-window", "-t", target(session, window)); err != nil {
		return fmt.Errorf("kill-window: %s", firstLine(out))
	}
	return nil
}

// KillWindowByName closes a window addressed by name rather than index.
// Reopen needs this: it resolves a window by name, and an index can shift
// under it when another window closes in between.
func KillWindowByName(ctx context.Context, session, name string) error {
	if session == "" || name == "" {
		return fmt.Errorf("session and window name required")
	}
	if out, err := run(ctx, "kill-window", "-t", session+":"+name); err != nil {
		return fmt.Errorf("kill-window: %s", firstLine(out))
	}
	return nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func splitLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

// SendCommandPreservingDraft submits cmd, then puts back whatever was in the
// composer first.
//
// The nudge used to refuse a pane holding an unsent draft, because clearing the
// line to type into it destroys somebody's half-written message. That refusal
// was right about the danger and wrong about the choice — it treated "type over
// it" and "do nothing" as the only options, and the cost of doing nothing
// finally landed on a person: a spoken instruction to turn on a light sat
// undelivered for fourteen minutes behind a draft that had been sitting in that
// composer for hours. Every component worked; the session was never told.
//
// So the draft is read, the command is submitted, and the draft is typed back —
// unsent, exactly as it was found. The draft is also returned so a caller can
// record it, because the one unacceptable outcome here is losing text a person
// wrote and having no copy of it anywhere.
//
// Restoration is NOT verified by re-reading the pane. It would race the
// application redrawing, and a check that reports failure on a timing artifact
// would be worse than none — the caller logs what it held, which is the
// recovery that actually matters.
func SendCommandPreservingDraft(ctx context.Context, session string, window, pane int, cmd, draft string) error {
	if err := SendCommand(ctx, session, window, pane, cmd); err != nil {
		return err
	}
	if draft == "" {
		return nil
	}
	// Literal, and no Enter: it goes back as text somebody is still writing,
	// not as a message they are now deemed to have sent.
	t := paneTarget(session, window, pane)
	if out, err := run(ctx, "send-keys", "-t", t, "-l", "--", draft); err != nil {
		return fmt.Errorf("restoring draft: %s", firstLine(out))
	}
	return nil
}

// ComposerDraft returns the text currently in a pane's input row, and whether an
// input row was found at all. Exported so a caller can preserve it.
func ComposerDraft(pane string) (string, bool) {
	lines := strings.Split(strings.TrimRight(pane, "\n"), "\n")
	if n := len(lines); n > 6 {
		lines = lines[n-6:]
	}
	for _, l := range lines {
		if d, ok := composerDraft(l); ok {
			return d, true
		}
	}
	return "", false
}
