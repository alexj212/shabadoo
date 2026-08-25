package main

// Local window control: `shabadoo attach`, `shabadoo win …`, `shabadoo boot`.
//
// These are the *local* half of the CLI and need no coordinator — they drive
// this host's tmux directly, so they keep working when the hub is down. The
// coordinator-wide commands (`sessions`, `open`, `send`, `keys`) live in
// cli.go and go over the human API. Scope is visible in the command name
// because the alternative — one verb that silently picks a host — eventually
// kills a window on the wrong machine.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"shabadoo/tmux"
)

func winUsage() {
	fmt.Fprint(os.Stderr, `usage:
  shabadoo win list                 windows in the shared tmux session
  shabadoo win open <path>          start a session for a folder (no attach)
  shabadoo win close <window>       kill a window
  shabadoo win reopen <window>      kill it and relaunch in the same folder
  shabadoo win clear <window>       send /clear to that window

<window> is an exact name from 'list' or a unique substring.
Override the session with $CLAUDE_SESSION_NAME (default "claude").
`)
	os.Exit(2)
}

func runWin(args []string) {
	if len(args) == 0 {
		winUsage()
	}
	if err := exec.Command("tmux", "-V").Run(); err != nil {
		fatalf("tmux is not installed")
	}

	ctx := context.Background()
	c := loadLaunchConfig()
	sub, rest := args[0], args[1:]

	// An empty pattern must never reach resolveWindow: "" is a substring of
	// every name, so against a single window it resolves "uniquely" and closes
	// it. A shell expanding to nothing is the realistic way that happens.
	arg := func() string {
		if len(rest) == 0 || strings.TrimSpace(rest[0]) == "" {
			winUsage()
		}
		return rest[0]
	}

	switch sub {
	case "list", "ls":
		winList(ctx, c)
	case "open", "start":
		winOpen(ctx, c, arg())
	case "close", "kill":
		winClose(ctx, c, arg())
	case "reopen", "restart":
		winReopen(ctx, c, arg())
	case "clear":
		winClear(ctx, c, arg())
	case "-h", "--help", "help":
		winUsage()
	default:
		fatalf("unknown subcommand %q (try: list open close reopen clear)", sub)
	}
}

func winList(ctx context.Context, c launchConfig) {
	fmt.Printf("Local tmux windows (session %q):\n", c.SessionName)
	if !c.sessionExists(ctx) {
		fmt.Println("  (no tmux session — nothing running locally)")
		return
	}
	// Same separator and the same path-goes-last rule as tmux.Windows — this
	// list had its own hardcoded 0x1F copy, which meant it broke in exactly the
	// same way and had to be found twice.
	format := strings.Join([]string{
		"#{window_index}", "#{?window_active,yes,}",
		"#{window_name}", "#{pane_current_path}",
	}, tmux.FieldSep)

	out, err := exec.CommandContext(ctx, "tmux", "list-windows", "-t", c.SessionName,
		"-F", format).Output()
	if err != nil {
		fatalf("list windows: %v", err)
	}
	fmt.Printf("  %-4s  %-6s  %-36s  %s\n", "IDX", "ACTIVE", "NAME", "CWD")
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		f := strings.SplitN(line, tmux.FieldSep, 4)
		if len(f) < 4 {
			continue
		}
		fmt.Printf("  %-4s  %-6s  %-36s  %s\n", f[0], f[1], f[2], f[3])
	}
}

func winOpen(ctx context.Context, c launchConfig, path string) {
	cwd, err := resolveDir(path)
	if err != nil {
		fatalf("%v", err)
	}
	name := c.windowName(cwd)
	if c.sessionExists(ctx) {
		names, err := c.windowNames(ctx)
		if err != nil {
			fatalf("%v", err)
		}
		for _, n := range names {
			if n == name {
				fmt.Printf("already open: %q (in %s)\n", name, cwd)
				return
			}
		}
	}
	// Background: opening a folder must not yank the current window away from
	// whoever is attached to this session.
	if _, err := c.launch(ctx, cwd, true); err != nil {
		fatalf("%v", err)
	}
	fmt.Printf("opened %q in %s\n", name, cwd)
}

func winClose(ctx context.Context, c launchConfig, pattern string) {
	name := mustResolve(ctx, c, pattern)
	if err := tmux.KillWindowByName(ctx, c.SessionName, name); err != nil {
		fatalf("%v", err)
	}
	fmt.Printf("closed window %q\n", name)
}

func winReopen(ctx context.Context, c launchConfig, pattern string) {
	name := mustResolve(ctx, c, pattern)
	// Read the cwd before killing: afterwards there is nothing left to ask.
	cwd, err := c.windowCWD(ctx, name)
	if err != nil {
		fatalf("%v", err)
	}
	if err := tmux.KillWindowByName(ctx, c.SessionName, name); err != nil {
		fatalf("%v", err)
	}
	if _, err := c.launch(ctx, cwd, false); err != nil {
		fatalf("%v", err)
	}
	fmt.Printf("reopened %q in %s\n", name, cwd)
}

func winClear(ctx context.Context, c launchConfig, pattern string) {
	name := mustResolve(ctx, c, pattern)
	// C-u clears any half-typed input first so /clear lands on an empty line.
	if out, err := exec.CommandContext(ctx, "tmux", "send-keys",
		"-t", c.SessionName+":"+name, "C-u", "/clear", "Enter").CombinedOutput(); err != nil {
		fatalf("send-keys: %s", strings.TrimSpace(string(out)))
	}
	fmt.Printf("sent /clear to %q\n", name)
}

func mustResolve(ctx context.Context, c launchConfig, pattern string) string {
	if !c.sessionExists(ctx) {
		fatalf("no tmux session %q — nothing running", c.SessionName)
	}
	name, err := c.resolveWindow(ctx, pattern)
	if err != nil {
		fatalf("%v", err)
	}
	return name
}

func resolveDir(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("not a directory: %s", path)
	}
	fi, err := os.Stat(resolved)
	if err != nil || !fi.IsDir() {
		return "", fmt.Errorf("not a directory: %s", path)
	}
	return resolved, nil
}

// runAttach starts (or selects) the window for this folder and attaches to it.
// This is the daily driver — what `claude.sh` was.
func runAttach(args []string) {
	fset := flag.NewFlagSet("attach", flag.ExitOnError)
	dir := fset.String("dir", "", "folder to attach to (default: current directory)")
	fset.Parse(args)

	if err := exec.Command("tmux", "-V").Run(); err != nil {
		fatalf("tmux is not installed")
	}
	ctx := context.Background()
	c := loadLaunchConfig()

	if _, err := exec.LookPath(c.Bin); err != nil {
		fatalf("%q not found in PATH", c.Bin)
	}

	target := *dir
	if target == "" {
		wd, err := os.Getwd()
		if err != nil {
			fatalf("%v", err)
		}
		target = wd
	}
	cwd, err := resolveDir(target)
	if err != nil {
		fatalf("%v", err)
	}

	name := c.windowName(cwd)
	existing := false
	if c.sessionExists(ctx) {
		names, err := c.windowNames(ctx)
		if err != nil {
			fatalf("%v", err)
		}
		for _, n := range names {
			if n == name {
				existing = true
				break
			}
		}
	}

	if existing {
		fmt.Fprintf(os.Stderr, "attaching to existing window %q\n", name)
		c.refreshSessionEnv(ctx)
		c.applyDisplay(ctx)
	} else {
		fmt.Fprintf(os.Stderr, "creating window %q in %s\n", name, cwd)
		// Foreground: this is an interactive launch, so the new window should
		// be the one you land on.
		if _, err := c.launch(ctx, cwd, false); err != nil {
			fatalf("%v", err)
		}
	}

	// No controlling terminal means this is the boot path (systemd/launchd or
	// `shabadoo boot`): the window exists, and there is nothing to attach to.
	if !isTerminal(os.Stdout) {
		return
	}

	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		fatalf("tmux not found")
	}
	verb := "attach-session"
	if os.Getenv("TMUX") != "" {
		// Already inside tmux; attaching would nest a client inside itself.
		verb = "switch-client"
	}
	argv := []string{"tmux", verb, "-t", c.SessionName + ":" + name}
	// Exec rather than run: this process should *become* the tmux client, so
	// the terminal is handed over cleanly and exit status is tmux's own.
	if err := syscall.Exec(tmuxBin, argv, os.Environ()); err != nil {
		fatalf("exec tmux: %v", err)
	}
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// runBoot opens one window per folder in the boot list. It is a one-shot, run
// by the login launcher and the cron watchdog; already-open folders are left
// alone, so re-running is harmless.
func runBoot(args []string) {
	fset := flag.NewFlagSet("boot", flag.ExitOnError)
	list := fset.String("list", bootListPath(), "folder list to open")

	// A bare `boot` opens windows, and it has to: the cron watchdog runs it
	// every ten minutes, so making it noun-only would stop autostart on every
	// host, silently. But that leaves it the one command here where "let me see
	// what this does" has a side effect — `config` with no arguments prints,
	// and someone reasonably expects the same shape from `boot`.
	//
	// So it says what it is about to do before doing it, and --dry-run answers
	// the question without acting. That is `doctor` to `setup`, which is this
	// project's existing answer to the same tension.
	dry := fset.Bool("dry-run", false, "report what would open; start nothing")
	fset.Parse(args)

	body, err := os.ReadFile(*list)
	if err != nil {
		fatalf("folder list: %v", err)
	}
	if !*dry {
		if err := exec.Command("tmux", "-V").Run(); err != nil {
			fatalf("tmux is not installed")
		}
	}

	ctx := context.Background()
	c := loadLaunchConfig()
	failed := false

	verb := "opening"
	if *dry {
		verb = "would open"
	}
	fmt.Fprintf(os.Stderr, "boot: %s folders listed in %s\n", verb, *list)

	for _, raw := range strings.Split(string(body), "\n") {
		line := strings.TrimSpace(raw)
		if i := strings.Index(line, "#"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		cwd, err := resolveDir(expandHome(line))
		if err != nil {
			fmt.Fprintf(os.Stderr, "boot: skipping missing folder: %s\n", line)
			failed = true
			continue
		}
		// Closed on purpose stays closed. Without this the watchdog reopens a
		// session within ten minutes, which defeats closing one to free
		// resources — the thing deactivation exists for.
		if isDeactivated(cwd) {
			fmt.Fprintf(os.Stderr, "boot: deactivated, skipping: %s\n", cwd)
			continue
		}
		name := c.windowName(cwd)
		if c.sessionExists(ctx) {
			names, _ := c.windowNames(ctx)
			open := false
			for _, n := range names {
				if n == name {
					open = true
					break
				}
			}
			if open {
				fmt.Fprintf(os.Stderr, "boot: already open: %s\n", name)
				continue
			}
		}
		if *dry {
			fmt.Fprintf(os.Stderr, "boot: would launch in %s\n", cwd)
			continue
		}
		fmt.Fprintf(os.Stderr, "boot: launching in %s\n", cwd)
		if _, err := c.launch(ctx, cwd, true); err != nil {
			fmt.Fprintf(os.Stderr, "boot: failed for %s: %v\n", cwd, err)
			failed = true
		}
	}
	if failed {
		os.Exit(1)
	}
}

func bootListPath() string {
	if v := os.Getenv("CLAUDE_SESSIONS_LIST"); v != "" {
		return v
	}
	return filepath.Join(home(), ".config", "claude-sessions", "folders")
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		return filepath.Join(home(), strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
	}
	return p
}
