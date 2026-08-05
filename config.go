package main

// Editing the two files the launcher reads, without destroying what is in them.
//
//	~/.config/claude/env                 the knobs — host label, claude flags
//	~/.config/claude-sessions/folders    which folders open at boot
//
// Both hold DECISIONS rather than generated content, which is why `setup`
// scaffolds them once and never overwrites them. That same rule applies here:
// every edit below is surgical. A `config set` rewrites one line and leaves the
// explanatory comments around it alone, because a file that loses its comments
// the first time it is edited by a tool teaches you not to use the tool.

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// configKeys are the knobs the launcher reads, with what they do and what they
// fall back to. Listed rather than discovered so `config` can show a knob that
// is not set yet — the useful case is finding out what you COULD set.
var configKeys = []struct{ key, def, what string }{
	{"CLAUDE_HOST_LABEL", "hostname -s", "short host label; names tmux windows and the remote-control alias"},
	{"CLAUDE_BIN", "claude", "path or name of the claude CLI"},
	{"CLAUDE_ARGS", "--dangerously-skip-permissions", "args appended to every launch"},
	{"CLAUDE_RESUME", "--continue", "resume flag; empty disables resuming"},
	{"CLAUDE_SESSION_NAME", "claude", "tmux session all windows live in"},
}

func runConfig(args []string) {
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "", "list", "show":
		configShow()
	case "set":
		configSet(args)
	case "unset":
		configUnset(args)
	case "edit":
		configEdit()
	case "path":
		fmt.Println(claudeEnvPath())
	default:
		fmt.Fprintf(os.Stderr, `usage: shabadoo config [list|set|unset|edit|path]

  shabadoo config                        what each knob is, and where it came from
  shabadoo config set CLAUDE_HOST_LABEL wsl
  shabadoo config unset CLAUDE_ARGS      fall back to the default
  shabadoo config edit                   open the file in $EDITOR
  shabadoo config path                   print the file's location

Changes take effect for windows opened AFTER them; a running window keeps the
settings it was launched with.
`)
		os.Exit(2)
	}
}

// configShow prints each knob with its effective value and where that came from.
//
// The provenance is the point. The env file deliberately WINS over the process
// environment — the shell script this replaced sourced the file, and matching
// that order is what keeps window names stable — which is exactly backwards
// from what most people assume, and invisible until it surprises them.
func configShow() {
	path := claudeEnvPath()
	file := envFileMap(path)

	if _, err := os.Stat(path); err != nil {
		fmt.Printf("%s does not exist yet — every knob is at its default.\n", path)
		fmt.Printf("`shabadoo config set KEY VALUE` creates it.\n\n")
	} else {
		fmt.Printf("%s\n\n", path)
	}

	for _, k := range configKeys {
		value, source := k.def, "default"
		if v, ok := os.LookupEnv(k.key); ok && v != "" {
			value, source = v, "environment"
		}
		// Checked last because it wins last.
		if v, ok := file[k.key]; ok {
			value, source = v, "file"
			if v == "" {
				value = "(empty)"
			}
		}
		fmt.Printf("  %-22s %-34s %s\n", k.key, value, source)
		fmt.Printf("  %-22s %s\n\n", "", k.what)
	}
}

func configSet(args []string) {
	if len(args) < 1 {
		fatalf("usage: shabadoo config set KEY [VALUE]   (omit VALUE to set it empty)")
	}
	key := args[0]
	value := strings.Join(args[1:], " ")
	if !knownConfigKey(key) {
		// A warning rather than a refusal: the file is the operator's, and
		// nothing stops them keeping their own variables in it. But a typo'd
		// CLAUDE_HOST_LABLE would otherwise sit there doing nothing forever.
		warnf("%s is not a knob the launcher reads — it will be kept, but ignored", key)
	}
	if err := setEnvFileValue(claudeEnvPath(), key, value); err != nil {
		fatalf("%v", err)
	}
	shown := value
	if shown == "" {
		shown = "(empty)"
	}
	fmt.Printf("%s = %s\n", key, shown)
	fmt.Println("takes effect for windows opened from now on; running windows keep what they started with")
}

func configUnset(args []string) {
	if len(args) != 1 {
		fatalf("usage: shabadoo config unset KEY")
	}
	removed, err := removeEnvFileKey(claudeEnvPath(), args[0])
	if err != nil {
		fatalf("%v", err)
	}
	if !removed {
		fmt.Printf("%s was not set in %s\n", args[0], claudeEnvPath())
		return
	}
	fmt.Printf("%s unset — back to its default\n", args[0])
}

func configEdit() {
	path := claudeEnvPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fatalf("%v", err)
	}
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		fatalf("set $EDITOR (or $VISUAL) — or edit %s directly", path)
	}
	cmd := exec.Command("sh", "-c", editor+" "+path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fatalf("%s: %v", editor, err)
	}
}

func knownConfigKey(k string) bool {
	for _, c := range configKeys {
		if c.key == k {
			return true
		}
	}
	return false
}

// setEnvFileValue rewrites one assignment in place, or appends it.
//
// Line-by-line rather than parse-and-serialise: the file is full of comments
// explaining why each knob is set the way it is, and regenerating it from a map
// would silently delete all of them. A config tool that eats your notes is one
// you stop using.
func setEnvFileValue(path, key, value string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	line := fmt.Sprintf("export %s=%s", key, shellQuote(value))
	var out []string
	replaced := false
	for _, raw := range strings.Split(string(body), "\n") {
		if assignsKey(raw, key) && !replaced {
			out = append(out, line)
			replaced = true
			continue
		}
		// A second assignment of the same key is dropped: the last one wins at
		// source time, so leaving it would make the file disagree with itself.
		if assignsKey(raw, key) {
			continue
		}
		out = append(out, raw)
	}
	if !replaced {
		for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
			out = out[:len(out)-1]
		}
		if len(out) == 0 {
			out = append(out, "# Per-host launcher settings, read by shabadoo attach / win / boot.",
				"# Anything set here wins over the process environment.", "")
		}
		out = append(out, line)
	}
	return writeAtomic(path, []byte(strings.Join(out, "\n")+"\n"), 0o644)
}

func removeEnvFileKey(path, key string) (bool, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	var out []string
	removed := false
	for _, raw := range strings.Split(string(body), "\n") {
		if assignsKey(raw, key) {
			removed = true
			continue
		}
		out = append(out, raw)
	}
	if !removed {
		return false, nil
	}
	return true, writeAtomic(path, []byte(strings.Join(out, "\n")), 0o644)
}

// assignsKey reports whether a line assigns key, with or without `export`, and
// ignoring a commented-out one — a commented assignment is documentation of an
// option, and replacing it would delete the hint.
func assignsKey(line, key string) bool {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") {
		return false
	}
	t = strings.TrimPrefix(t, "export ")
	name, _, ok := strings.Cut(t, "=")
	return ok && strings.TrimSpace(name) == key
}

// ---------------------------------------------------------------------------
// the boot list
// ---------------------------------------------------------------------------

// runBootList prints what will open at boot, and what state each folder is in.
func runBootList(args []string) {
	fset := flag.NewFlagSet("boot list", flag.ExitOnError)
	path := fset.String("list", bootListPath(), "folder list")
	fset.Parse(args)

	folders, err := readBootList(*path)
	if err != nil {
		fatalf("%v", err)
	}
	if len(folders) == 0 {
		fmt.Printf("%s lists no folders.\n", *path)
		fmt.Println("add one with: shabadoo boot add <dir>")
		return
	}

	open := openFolders()
	fmt.Printf("%s\n\n", *path)
	for _, f := range folders {
		mark, note := " ", ""
		switch {
		case !dirExists(f):
			// A folder that has been moved or deleted starts nothing, silently,
			// which is exactly the kind of rot a list nobody reads accumulates.
			mark, note = "!", "  (missing — boot will skip it)"
		case open[resolve(f)]:
			mark, note = "*", "  (already open)"
		}
		fmt.Printf("  %s %s%s\n", mark, f, note)
	}
	fmt.Println("\n  * already open    ! missing")
}

func runBootAdd(args []string) {
	fset := flag.NewFlagSet("boot add", flag.ExitOnError)
	path := fset.String("list", bootListPath(), "folder list")
	dirs := argsAndFlags(fset, args)
	if len(dirs) == 0 {
		fatalf("usage: shabadoo boot add <dir>...   (default: this folder)")
	}

	for _, d := range dirs {
		abs, err := filepath.Abs(d)
		if err != nil {
			warnf("%s: %v", d, err)
			continue
		}
		// Refused rather than warned about: a boot list entry that does not
		// exist opens nothing and says nothing, so it would sit there looking
		// configured. Better to fail now, while somebody is watching.
		if !dirExists(abs) {
			warnf("%s does not exist — not adding it", abs)
			continue
		}
		folders, err := readBootList(*path)
		if err != nil {
			fatalf("%v", err)
		}
		// Compared through symlinks, because the list may hold a link while
		// tmux reports the resolved path — the same reason /api/folders does it.
		for _, f := range folders {
			if resolve(f) == resolve(abs) {
				fmt.Printf("already listed: %s\n", f)
				goto next
			}
		}
		if err := appendBootList(*path, abs); err != nil {
			fatalf("%v", err)
		}
		fmt.Printf("added %s\n", abs)
	next:
	}
}

func runBootRemove(args []string) {
	fset := flag.NewFlagSet("boot remove", flag.ExitOnError)
	path := fset.String("list", bootListPath(), "folder list")
	dirs := argsAndFlags(fset, args)
	if len(dirs) == 0 {
		fatalf("usage: shabadoo boot remove <dir>...")
	}

	for _, d := range dirs {
		abs, _ := filepath.Abs(d)
		removed, err := removeFromBootList(*path, abs)
		if err != nil {
			fatalf("%v", err)
		}
		if removed == "" {
			warnf("%s is not in the list — `shabadoo boot list` shows what is", abs)
			continue
		}
		fmt.Printf("removed %s\n", removed)
		fmt.Println("  (any window already open stays open; this only affects the next boot)")
	}
}

// readBootList returns the folders, without comments or blanks. A missing file
// is an empty list rather than an error: nothing has been configured yet.
func readBootList(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.Index(line, "#"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return out, sc.Err()
}

func appendBootList(path, dir string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if len(body) == 0 {
		body = []byte("# Folders to auto-launch Claude Code sessions for at startup.\n" +
			"# One absolute path per line. '#' comments and blank lines are ignored.\n")
	}
	if !strings.HasSuffix(string(body), "\n") {
		body = append(body, '\n')
	}
	return writeAtomic(path, append(body, []byte(dir+"\n")...), 0o644)
}

// removeFromBootList drops a folder and returns the line it removed, so the
// caller can report what was actually in the file rather than what was typed.
func removeFromBootList(path, dir string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	want := resolve(dir)
	var out []string
	removed := ""
	for _, raw := range strings.Split(string(body), "\n") {
		line := strings.TrimSpace(raw)
		if i := strings.Index(line, "#"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		// Matched through symlinks so either spelling of the same folder works.
		if line != "" && resolve(line) == want {
			removed = line
			continue
		}
		out = append(out, raw)
	}
	if removed == "" {
		return "", nil
	}
	return removed, writeAtomic(path, []byte(strings.Join(out, "\n")), 0o644)
}

// openFolders is the set of folders that already have a window, resolved.
func openFolders() map[string]bool {
	open := map[string]bool{}
	sessions, err := reportSessions(context.Background())
	if err != nil {
		return open
	}
	for _, s := range sessions {
		if s.CWD != "" {
			open[resolve(s.CWD)] = true
		}
	}
	return open
}

func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
