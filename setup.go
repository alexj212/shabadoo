package main

import (
	"bufio"
	"bytes"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// setup installs the launcher toolchain this binary embeds onto the local
// machine. Every step is idempotent: re-running reports "unchanged" for
// anything already correct, and any file it replaces is backed up first.
type setup struct {
	binDir    string // where claude.sh / claude-sessions go
	claudeDir string // ~/.claude — portable config target
	confDir   string // ~/.config/claude — per-host env file
	stateDir  string // ~/.config/shabadoo — coordinator db, agent key
	addr      string // listen address baked into the systemd unit
	caddyHost string
	caddyBind string

	dryRun bool
	force  bool // also overwrite files setup otherwise never touches
	// quiet suppresses the per-file report. Set when the agent installs its own
	// payload at startup: that runs on every restart, and a line per file would
	// bury the summary that matters.
	quiet bool
	boot   bool
	caddy  bool
	skip   map[string]bool

	// --service and the coordinator's auth posture. There is no default
	// posture: see (*setup).posture.
	service      bool
	coord        string // join this coordinator instead of installing one
	deviceTokens bool
	accessTeam   string
	accessAud    string

	warnings []string
	failed   bool
}

func runSetup(args []string) {
	home, err := os.UserHomeDir()
	if err != nil {
		fatalf("cannot resolve home directory: %v", err)
	}

	fs_ := flag.NewFlagSet("setup", flag.ExitOnError)
	s := &setup{}
	fs_.StringVar(&s.binDir, "bin-dir", filepath.Join(home, "bin"), "directory for claude.sh / claude-sessions")
	fs_.StringVar(&s.claudeDir, "claude-dir", filepath.Join(home, ".claude"), "Claude config directory to sync into")
	fs_.StringVar(&s.confDir, "config-dir", filepath.Join(home, ".config", "claude"), "directory for the per-host env file")
	fs_.StringVar(&s.stateDir, "shabadoo-dir", filepath.Join(home, ".config", "shabadoo"), "state directory for the coordinator db and agent key")
	fs_.StringVar(&s.addr, "addr", "127.0.0.1:8787", "listen address baked into the systemd unit")
	fs_.StringVar(&s.caddyHost, "caddy-host", "", "vhost for the Caddy block, e.g. tmux.laptop.example.com (required with --caddy)")
	fs_.StringVar(&s.caddyBind, "caddy-bind", "", "IP the Caddy block binds (default: this host's Tailscale v4 address)")
	fs_.BoolVar(&s.dryRun, "dry-run", false, "report what would change without writing anything")
	fs_.BoolVar(&s.force, "force", false, "also overwrite files setup normally preserves (the env file)")
	fs_.BoolVar(&s.service, "service", false, "install + start hub and node as services (systemd units / launchd agents)")
	fs_.StringVar(&s.coord, "coord", "", "--service: join this coordinator (agent only; no coordinator installed here)")
	fs_.BoolVar(&s.deviceTokens, "device-tokens", false, "--service: coordinator authenticates every client with an enrolled device token")
	fs_.StringVar(&s.accessTeam, "access-team", "", "--service: Cloudflare Access team domain, e.g. example.cloudflareaccess.com")
	fs_.StringVar(&s.accessAud, "access-aud", "", "--service: Cloudflare Access application AUD tag")
	fs_.BoolVar(&s.boot, "boot", false, "install the login launcher that opens a window per configured folder")
	fs_.BoolVar(&s.caddy, "caddy", false, "add the Caddy vhost fronting the coordinator (linux only, uses sudo)")
	skip := fs_.String("skip", "", "comma-separated steps to skip: scripts,path,deps,env,config")
	fs_.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: shabadoo setup [flags]

Installs the toolchain embedded in this binary:
  scripts  claude.sh + claude-sessions -> --bin-dir
  path     ensure --bin-dir is on PATH in your shell rc
  deps     report missing runtime dependencies (tmux, claude)
  env      scaffold <config-dir>/env if absent (never overwritten)
  binary   install this binary into --bin-dir so "shabadoo attach" resolves
  config   portable ~/.claude config: CLAUDE.md, settings.json, skills/

Opt-in steps:
  --service  run hub (coordinator) + node (this host's agent) as
             services. linux: /etc/systemd/system (sudo). darwin:
             ~/Library/LaunchAgents (no sudo). Requires an auth posture:
             --device-tokens, or --access-team X --access-aud Y.
             With --coord URL, installs the agent only and joins that
             coordinator — what an additional machine wants.
  --boot     login launcher opening a window per folder in
             ~/.config/claude-sessions/folders. User-scoped, no sudo.
  --caddy    a vhost block in /etc/caddy/Caddyfile, validated before reload.
             Linux only — on macOS bind the tailnet with --addr tailscale:PORT.

flags:
`)
		fs_.PrintDefaults()
	}
	fs_.Parse(args)

	// Validate the service posture before any step runs: an unusable --service
	// invocation must not leave half an install behind.
	if s.service && s.coord == "" {
		if _, err := s.posture(); err != nil {
			fatalf("%v", err)
		}
	}

	s.skip = map[string]bool{}
	for _, name := range strings.Split(*skip, ",") {
		if name = strings.TrimSpace(name); name != "" {
			s.skip[name] = true
		}
	}

	s.run()
}

func runDoctor(args []string) {
	// doctor is setup with the writes disabled — same checks, same report.
	runSetup(append([]string{"-dry-run"}, args...))
}

func (s *setup) run() {
	mode := "installing"
	if s.dryRun {
		mode = "dry run — nothing will be written"
	}
	fmt.Printf("shabadoo setup (%s)\n  bin-dir    %s\n  claude-dir %s\n  config-dir %s\n",
		mode, s.binDir, s.claudeDir, s.confDir)

	steps := []struct {
		name string
		run  func() error
		on   bool
	}{
		{"binary", s.stepBinary, true},
		{"path", s.stepPath, true},
		{"deps", s.stepDeps, true},
		{"env", s.stepEnv, true},
		{"config", s.stepConfig, true},
		{"node", s.stepNodeProject, true},
		{"service", s.stepService, s.service},
		{"boot", s.stepBoot, s.boot},
		{"caddy", s.stepCaddy, s.caddy},
	}

	for _, st := range steps {
		if !st.on || s.skip[st.name] {
			continue
		}
		fmt.Printf("\n==> %s\n", st.name)
		if err := st.run(); err != nil {
			s.failed = true
			s.report("FAILED", "%v", err)
		}
	}

	fmt.Println()
	for _, w := range s.warnings {
		fmt.Printf("warning: %s\n", w)
	}
	switch {
	case s.failed:
		fmt.Println("\nsetup finished with errors.")
		os.Exit(1)
	case s.dryRun:
		fmt.Println("\ndry run complete — re-run without --dry-run to apply.")
	default:
		fmt.Printf("\nsetup complete. Open a new shell, then: shabadoo win list\n")
	}
}

// ---------------------------------------------------------------------------
// steps
// ---------------------------------------------------------------------------

// stepBinary puts this binary on PATH. It replaces the step that installed
// the `claude.sh` / `claude-sessions` scripts, which are now subcommands —
// `shabadoo attach` is the daily driver, so the binary has to be reachable by
// name from a fresh shell, not just from wherever it was downloaded to.
func (s *setup) stepBinary() error {
	return s.installSelf()
}

func (s *setup) stepPath() error {
	// Already on PATH means the shell rc (or the login environment) is doing
	// its job, however it spells the directory — `~/bin`, `$HOME/bin`, a
	// pathadd helper. Appending another export would only duplicate it.
	if onPath(s.binDir) {
		s.report("ok", "%s is already on PATH", s.binDir)
		return nil
	}
	s.warn("%s is not on the current PATH — open a new shell after setup", s.binDir)

	line := fmt.Sprintf("export PATH=\"%s:$PATH\"", s.binDir)
	block := "\n# Added by shabadoo setup\n" + line + "\n"

	var rcs []string
	switch filepath.Base(os.Getenv("SHELL")) {
	case "zsh":
		rcs = []string{filepath.Join(home(), ".zshrc")}
	case "bash":
		rcs = []string{filepath.Join(home(), ".bashrc"), filepath.Join(home(), ".bash_profile")}
	case "fish":
		s.warn("fish detected — add %s to fish_user_paths yourself", s.binDir)
		return nil
	default:
		s.warn("unrecognized shell %q — add %s to PATH manually", os.Getenv("SHELL"), s.binDir)
		return nil
	}

	touched := false
	for _, rc := range rcs {
		body, err := os.ReadFile(rc)
		if errors.Is(err, fs.ErrNotExist) {
			continue // don't create rc files that aren't there
		} else if err != nil {
			return err
		}
		if mentionsDir(string(body), s.binDir) {
			s.report("unchanged", "%s already references %s", rc, s.binDir)
			touched = true
			continue
		}
		if s.dryRun {
			s.report("would append", "PATH export to %s", rc)
			touched = true
			continue
		}
		f, err := os.OpenFile(rc, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		_, werr := f.WriteString(block)
		cerr := f.Close()
		if werr != nil {
			return werr
		}
		if cerr != nil {
			return cerr
		}
		s.report("appended", "PATH export to %s", rc)
		touched = true
	}
	if !touched {
		s.warn("no shell rc file found to add %s to PATH", s.binDir)
	}
	return nil
}

type dep struct {
	bin      string
	required bool
	hints    map[string]string // GOOS -> install hint
	// elsewhere are paths to probe when the binary is not on PATH.
	//
	// "Not installed" and "installed where this shell cannot see it" are
	// different answers, and the remediation is the opposite: one is an install,
	// the other is a PATH entry. Reported by a node whose `claude` is the native
	// install at ~/.local/bin — the check said missing and advised
	// `npm install -g`, which would have put a SECOND Claude Code on the machine
	// on a different update channel, beside a working one.
	elsewhere []string
}

// findElsewhere looks for an executable the PATH lookup missed. Returns the
// path it found and the directory that would need to be on PATH.
func (d dep) findElsewhere() (string, string, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", false
	}
	for _, c := range d.elsewhere {
		full := filepath.Join(home, c)
		fi, err := os.Stat(full)
		if err != nil || fi.IsDir() || fi.Mode()&0o111 == 0 {
			continue
		}
		return full, filepath.Dir(full), true
	}
	return "", "", false
}

func (s *setup) stepDeps() error {
	deps := []dep{
		{bin: "tmux", required: true, hints: map[string]string{
			"darwin": "brew install tmux",
			"linux":  "sudo apt install tmux",
		}},
		{bin: "claude", required: true, hints: map[string]string{
			"darwin": "npm install -g @anthropic-ai/claude-code",
			"linux":  "npm install -g @anthropic-ai/claude-code",
		}, elsewhere: []string{".local/bin/claude"}},
		{bin: "nats", required: false, hints: map[string]string{
			"darwin": "brew install nats-io/nats-tools/nats  (optional: cross-host presence)",
			"linux":  "go install github.com/nats-io/natscli/nats@latest  (optional: cross-host presence)",
		}},
	}
	if s.service {
		deps = append(deps, dep{bin: "systemctl", required: true})
	}
	if s.caddy {
		deps = append(deps, dep{bin: "caddy", required: true})
	}

	for _, d := range deps {
		if path, err := exec.LookPath(d.bin); err == nil {
			s.report("found", "%-10s %s", d.bin, path)
			continue
		}
		// Installed, just unreachable. Say that instead of recommending an
		// install, which would add a second copy on another update channel.
		if full, dir, ok := d.findElsewhere(); ok {
			s.report("off-PATH", "%-10s %s", d.bin, full)
			s.warn("%q is installed at %s but %s is not on PATH — add that directory "+
				"rather than installing another copy", d.bin, full, dir)
			continue
		}
		if !d.required {
			s.report("missing", "%-10s (optional) %s", d.bin, d.hints[runtime.GOOS])
			continue
		}
		hint := d.hints[runtime.GOOS]
		if hint == "" {
			s.warn("required dependency %q not found in PATH", d.bin)
		} else {
			s.warn("required dependency %q not found in PATH — install with: %s", d.bin, hint)
		}
		s.report("MISSING", "%-10s required", d.bin)
	}
	return nil
}

// stepEnv scaffolds the per-host env file claude.sh sources. It is never
// overwritten without --force: it is the one file here that holds decisions
// (host label, claude flags) rather than content this binary owns.
func (s *setup) stepEnv() error {
	dst := filepath.Join(s.confDir, "env")
	if _, err := os.Stat(dst); err == nil && !s.force {
		s.report("preserved", "%s already exists (use --force to replace)", dst)
		return nil
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	body := fmt.Sprintf(`# Per-host overrides read by the launcher (shabadoo attach / win / boot).
# Anything set here wins over the built-in defaults AND over the process
# environment — the script this replaced sourced the file, and matching that
# order is what keeps window names stable on a host whose service environment
# disagrees with this file.

# Friendly host label used in tmux window names and the remote-control alias.
# Keep it short — this machine's hostname is %q.
export CLAUDE_HOST_LABEL=%s

# Uncomment to customize:
# export CLAUDE_BIN=claude
# export CLAUDE_ARGS="--dangerously-skip-permissions"
# export CLAUDE_SESSION_NAME=claude
# export CLAUDE_RESUME=--continue
`, rawHostname(), hostLabel())

	return s.installFile(dst, []byte(body), 0o644)
}

// stepNodeProject scaffolds this machine's own project — the directory whose
// session speaks for the node.
//
// It lives at <state dir>/<host label> because the agent already holds both
// halves and can derive the path with nothing to configure and nothing to keep
// in sync. It is named for the host because addressing the machine and
// addressing its supervisor should be the same act.
//
// Scaffolded only when absent, exactly like the env file: what goes in it is
// knowledge about a machine, written by whoever knows the machine. This
// installs a starting point and then never touches it again — not owning it is
// also why `uninstall --purge` leaves it behind.
func (s *setup) stepNodeProject() error {
	label := hostLabel()
	if label == "" {
		s.report("skipped", "no host label, so this node has no project name")
		return nil
	}
	dir := filepath.Join(s.stateDir, label)
	dst := filepath.Join(dir, "CLAUDE.md")

	if _, err := os.Stat(dst); err == nil {
		s.report("preserved", "%s already exists", dst)
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if s.dryRun {
		s.report("would create", "%s", dst)
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(dst, []byte(nodeProjectTemplate(label)), 0o644); err != nil {
		return err
	}
	s.report("created", "%s", dst)
	return nil
}

// nodeProjectTemplate is a starting point, not a format. The frontmatter
// description is the one machine-read part — it is what a router consults to
// decide whether work belongs to this machine — so it is filled in with
// something true rather than left as a placeholder nobody edits.
func nodeProjectTemplate(label string) string {
	return "---\n" +
		"description: Use for anything about the " + label + " machine itself — what is installed, what it can do, and starting or stopping sessions on it.\n" +
		"# Things no probe can establish. Toolchains, a GPU and the platform are\n" +
		"# detected automatically and do not belong here; a declared capability\n" +
		"# that is checkable and absent is ignored rather than believed.\n" +
		"capabilities:\n" +
		"---\n\n" +
		"# " + label + "\n\n" +
		"This machine's own project. Its session is " + label + "'s core session: it\n" +
		"knows the environment, and it is the only thing that starts sessions here.\n\n" +
		"Keep it cheap. It is always running, so a context that fills with the\n" +
		"details of finished work is the one problem no mechanism here solves.\n" +
		"Route and decide; delegate the doing.\n\n" +
		"## Environment\n\n" +
		"What is installed, where things live, what is peculiar about this host.\n\n" +
		"## Capabilities\n\n" +
		"What this machine can do that others cannot — an audio input, a GPU, a\n" +
		"platform toolchain, being always on. Declared here; some are also detected.\n\n" +
		"## Projects\n\n" +
		"Nothing to maintain by hand: every project describes itself in the\n" +
		"frontmatter of its own CLAUDE.md, and the live session list is the\n" +
		"registry. A written copy here would only go stale.\n"
}

func (s *setup) stepConfig() error {
	// MERGE first, install once.
	//
	// The obvious arrangement — install the portable payload, then install the
	// personal overlay over it — is wrong, and wrong in a way that only shows up
	// on the SECOND run: the base sees the overlay's file on disk, calls it a
	// difference, backs it up and replaces it, and the overlay puts it back.
	// Every run churns two files and leaves two backups, and `doctor` never
	// reports a clean host again. Idempotence is the property that makes
	// `doctor` trustworthy, so it is worth one map.
	payload, err := mergePayloads()
	if err != nil {
		return err
	}
	paths := make([]string, 0, len(payload))
	for rel := range payload {
		paths = append(paths, rel)
	}
	sort.Strings(paths) // stable output; a shuffled report reads like churn

	for _, rel := range paths {
		dst := filepath.Join(s.claudeDir, rel)
		if !s.dryRun {
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
		}
		mode := fs.FileMode(0o644)
		if strings.HasSuffix(rel, ".sh") {
			mode = 0o755
		}
		if err := s.installFile(dst, payload[rel], mode); err != nil {
			return err
		}
	}
	return nil
}

// mergePayloads flattens the portable payload and the personal overlay into the
// files that will actually be installed. The overlay wins on a collision;
// everything else is added.
//
// ADDITIVE is the contract of this step. It writes into a directory the
// operator also edits by hand and it runs repeatedly on machines that already
// work, so it never deletes: a skill present on the target and absent from the
// payload stays where it is.
func mergePayloads() (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, tree := range []struct {
		fsys embed.FS
		root string
	}{{configFS, "config"}, {localFS, "config.local"}} {
		err := fs.WalkDir(tree.fsys, tree.root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(tree.root, p)
			if err != nil {
				return err
			}
			// The placeholder that keeps an empty overlay embeddable. A fresh
			// clone has nothing else there, and installing a stray dotfile into
			// someone's ~/.claude would be a puzzle with no answer.
			if rel == ".gitkeep" {
				return nil
			}
			data, err := fs.ReadFile(tree.fsys, p)
			if err != nil {
				return err
			}
			out[rel] = data
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// file helpers
// ---------------------------------------------------------------------------

// installFile writes data to dst. An existing file with identical content is
// left alone; a differing one is copied to <dst>.bak.<epoch> before being
// replaced, so a bad vendor snapshot never destroys local edits silently.
func (s *setup) installFile(dst string, data []byte, mode fs.FileMode) error {
	// A payload-owned path that is a SYMLINK is refused, not followed.
	//
	// Reported from the field, and it is worse than it sounds. A project had
	// symlinked ~/.claude/skills/<name> at its own git checkout so a session
	// read the skill it was editing. Setup followed the link and wrote the
	// vendored copy straight into that working tree — replacing the source of
	// truth, not merely shadowing it. Had they edited afterwards they would
	// have been editing the payload's copy while believing it was the repo, and
	// the next commit would have quietly reverted their own fix. The `.bak` was
	// the only trace, sitting among untracked files.
	//
	// The general shape is what makes this a refusal rather than a warning:
	// following a symlink out of a payload-owned path makes setup a writer to
	// a repository it knows nothing about, and the blast radius is somebody
	// else's uncommitted work. Refusing costs one message; the alternative
	// costs a silent revert nobody attributes to an installer.
	if fi, lerr := os.Lstat(dst); lerr == nil && fi.Mode()&fs.ModeSymlink != 0 {
		target, _ := os.Readlink(dst)
		s.report("REFUSED", "%s is a symlink -> %s", dst, target)
		s.warn("refusing to write through the symlink at %s (-> %s).\n"+
			"         Following it would write this build's payload into whatever owns\n"+
			"         that path — a git working tree, in the case that produced this\n"+
			"         check. Replace the link with a real file or directory to let the\n"+
			"         payload own it, or move it aside to keep your own copy.", dst, target)
		return nil
	}

	old, err := os.ReadFile(dst)
	switch {
	case err == nil && bytes.Equal(old, data):
		s.report("unchanged", "%s", dst)
		return nil

	case err == nil:
		if s.dryRun {
			s.report("would update", "%s", dst)
			return nil
		}
		bak := fmt.Sprintf("%s.bak.%d", dst, time.Now().Unix())
		st, serr := os.Stat(dst)
		bakMode := fs.FileMode(0o600)
		if serr == nil {
			bakMode = st.Mode().Perm()
		}
		if err := os.WriteFile(bak, old, bakMode); err != nil {
			return fmt.Errorf("backup %s: %w", dst, err)
		}
		if err := writeAtomic(dst, data, mode); err != nil {
			return err
		}
		s.report("updated", "%s (backup: %s)", dst, filepath.Base(bak))
		return nil

	case errors.Is(err, fs.ErrNotExist):
		if s.dryRun {
			s.report("would install", "%s", dst)
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := writeAtomic(dst, data, mode); err != nil {
			return err
		}
		s.report("installed", "%s", dst)
		return nil

	default:
		return err
	}
}

// writeAtomic replaces dst via a temp file + rename. The rename matters for
// the scripts: overwriting claude.sh in place while a shell is executing it
// corrupts that running process, whereas rename leaves it on the old inode.
func writeAtomic(dst string, data []byte, mode fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".tmp*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op once the rename succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, mode); err != nil {
		return err
	}
	return os.Rename(name, dst)
}

// ---------------------------------------------------------------------------
// reporting + small utilities
// ---------------------------------------------------------------------------

func (s *setup) report(status, format string, a ...any) {
	if s.quiet {
		return
	}
	fmt.Printf("    %-14s %s\n", status, fmt.Sprintf(format, a...))
}

func (s *setup) warn(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	s.warnings = append(s.warnings, msg)
	s.report("warn", "%s", msg)
}

func fatalf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "shabadoo: "+format+"\n", a...)
	os.Exit(1)
}

func home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

// mentionsDir reports whether a shell rc already adds dir to PATH, allowing
// for the usual home-relative spellings — a literal-path match alone would
// miss `export PATH=~/bin:$PATH` and append a duplicate.
func mentionsDir(body, dir string) bool {
	candidates := []string{dir}
	if h := home(); h != "" && strings.HasPrefix(dir, h+string(filepath.Separator)) {
		rel := strings.TrimPrefix(dir, h)
		candidates = append(candidates, "~"+rel, "$HOME"+rel, "${HOME}"+rel)
	}
	for _, c := range candidates {
		if strings.Contains(body, c) {
			return true
		}
	}
	return false
}

// joinConfig resolves a path inside the per-host Claude config dir, mirroring
// claude.sh's own resolution ($CLAUDE_CONFIG_DIR, then XDG, then ~/.config).
func joinConfig(name string) string {
	base := os.Getenv("CLAUDE_CONFIG_DIR")
	if base == "" {
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			base = filepath.Join(xdg, "claude")
		} else {
			base = filepath.Join(home(), ".config", "claude")
		}
	}
	return filepath.Join(base, name)
}

func onPath(dir string) bool {
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p == dir {
			return true
		}
	}
	return false
}

func rawHostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "host"
	}
	return h
}

// hostLabel mirrors claude.sh's resolution order so the window names this
// installer predicts match the ones the script actually creates:
// $CLAUDE_HOST_LABEL, then the env file, then the short hostname.
func hostLabel() string {
	if v := os.Getenv("CLAUDE_HOST_LABEL"); v != "" {
		return sanitizeLabel(v)
	}
	if v := envFileValue(filepath.Join(home(), ".config", "claude", "env"), "CLAUDE_HOST_LABEL"); v != "" {
		return sanitizeLabel(v)
	}
	h, _, _ := strings.Cut(rawHostname(), ".")
	return sanitizeLabel(strings.ToLower(h))
}

// envFileValue pulls `export KEY=value` out of a shell env file without
// sourcing it. Good enough for the simple assignments claude.sh's env holds.
func envFileValue(path, key string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != key {
			continue
		}
		return strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return ""
}

// sanitizeLabel matches claude.sh's `tr -c 'A-Za-z0-9_-' '_'`.
func sanitizeLabel(s string) string {
	out := []rune(s)
	for i, r := range out {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			out[i] = '_'
		}
	}
	return string(out)
}
