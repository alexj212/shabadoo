package main

// Taking shabadoo off a machine.
//
// The counterpart to `setup`, and it exists for the same reason `setup` is
// idempotent: this binary gets installed on machines to try it, and until now
// the only way off was to remember every path it wrote — two systemd units, a
// user unit, three launchd plists, a binary, a PATH line. Nobody remembers
// that, so what actually happened was a dead unit left enabled on a host,
// failing on every boot forever.
//
// The governing rule is the mirror of setup's "never clobbers silently":
// UNINSTALL REMOVES WHAT SETUP GENERATED AND NOTHING ELSE. Setup deliberately
// scaffolds two things it never overwrites — the env file (decisions) and
// ~/.claude (config with hand edits) — because they are not content this binary
// owns. Not owning them on the way in means not deleting them on the way out.

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type uninstall struct {
	binDir   string
	stateDir string

	dryRun bool
	all    bool // also remove the binary
	purge  bool // also remove the state directory — see the warning

	removed  int
	warnings []string
	failed   bool
}

func runUninstall(args []string) {
	h, err := os.UserHomeDir()
	if err != nil {
		fatalf("cannot resolve home directory: %v", err)
	}
	u := &uninstall{}
	fset := flag.NewFlagSet("uninstall", flag.ExitOnError)
	fset.StringVar(&u.binDir, "bin-dir", filepath.Join(h, "bin"), "where the binary was installed")
	fset.StringVar(&u.stateDir, "shabadoo-dir", filepath.Join(h, ".config", "shabadoo"), "state directory")
	fset.BoolVar(&u.dryRun, "dry-run", false, "report what would be removed without removing anything")
	fset.BoolVar(&u.all, "all", false, "also remove the binary from --bin-dir")
	fset.BoolVar(&u.purge, "purge", false, "also remove the state directory (agent key, hub.db — see warning)")
	fset.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: shabadoo uninstall [flags]

Removes the services this binary installed:
  linux   /etc/systemd/system/shabadoo-{hub,node}.service (sudo)
          ~/.config/systemd/user/claude-sessions.service
  darwin  ~/Library/LaunchAgents/dev.shabadoo.{hub,node,boot}.plist

Never removed, because setup never owned them:
  ~/.claude               your Claude config — setup merges into it, edits live there
  ~/.config/claude/env    per-host decisions (host label, claude flags)
  ~/.config/claude-sessions/folders   your boot folder list

Not removed, because it is shared:
  /etc/caddy/Caddyfile    the vhost block is reported, not deleted — a bad edit
                          takes down every other site this Caddy fronts

flags:
`)
		fset.PrintDefaults()
	}
	fset.Parse(args)

	u.run()
}

func (u *uninstall) run() {
	mode := "removing"
	if u.dryRun {
		mode = "dry run — nothing will be removed"
	}
	fmt.Printf("shabadoo uninstall (%s)\n\n", mode)

	fmt.Println("==> services")
	switch runtime.GOOS {
	case "linux":
		u.servicesSystemd()
	case "darwin":
		u.servicesLaunchd()
	default:
		u.warn("no service manager known for %s — remove any units by hand", runtime.GOOS)
	}

	if u.all {
		fmt.Println("\n==> binary")
		u.removeFile(filepath.Join(u.binDir, binName))
		// The shorthand symlink goes with the binary it points at — leaving it
		// behind would be a dangling link named after a program that is gone.
		u.removeFile(filepath.Join(u.binDir, "shaba"))
		// The PATH line is left alone on purpose. It is one line in a shell rc
		// the operator also edits by hand, it is harmless once the directory is
		// empty, and rewriting someone's .bashrc to remove it is a much worse
		// failure than leaving it.
		u.report("kept", "the PATH line in your shell rc (harmless; remove by hand if you want)")
	}

	fmt.Println("\n==> state")
	u.stepState()

	if runtime.GOOS == "linux" {
		if body, err := os.ReadFile(caddyFile); err == nil && strings.Contains(string(body), "# shabadoo") {
			u.warn("a shabadoo vhost remains in %s — remove the block by hand, "+
				"then `caddy validate` before reloading", caddyFile)
		}
	}

	fmt.Println()
	for _, w := range u.warnings {
		fmt.Printf("warning: %s\n", w)
	}
	switch {
	case u.failed:
		fmt.Println("\nuninstall finished with errors.")
		os.Exit(1)
	case u.removed == 0:
		// Distinct from success: "nothing to do" and "done" look identical in a
		// list of `unchanged` lines, and the difference matters when you are
		// checking whether you uninstalled from the host you meant to.
		fmt.Println("nothing installed by shabadoo was found on this host.")
	case u.dryRun:
		fmt.Printf("%d item(s) would be removed. Re-run without --dry-run to apply.\n", u.removed)
	default:
		fmt.Printf("removed %d item(s).\n", u.removed)
	}
}

// stepState decides what happens to the directories setup did not generate.
//
// purgeStateExceptNodeProject empties the state directory but leaves this
// node's own project behind, returning its path when one was found.
func (u *uninstall) purgeStateExceptNodeProject() string {
	keep := hostLabel()
	entries, err := os.ReadDir(u.stateDir)
	if err != nil {
		// Nothing readable there: fall back to the blunt removal so an
		// unreadable directory is still reported rather than silently skipped.
		u.removeAll(u.stateDir)
		return ""
	}
	kept := ""
	for _, e := range entries {
		if keep != "" && e.IsDir() && e.Name() == keep {
			kept = filepath.Join(u.stateDir, e.Name())
			continue
		}
		u.removeAll(filepath.Join(u.stateDir, e.Name()))
	}
	if kept == "" {
		u.removeAll(u.stateDir) // nothing to preserve: take the directory too
	}
	return kept
}

// Split out of run() so it is testable: run() disables and removes real
// system services, which is not something a test may do. See the note in
// setup_test.go — an earlier draft of that test called run() and uninstalled
// the developer's own node.
func (u *uninstall) stepState() {
	if u.purge {
		// Loud, and only on an explicit flag. hub.db holds every enrolled
		// device token: removing it signs out every paired phone and browser,
		// and takes the audit log — the entire record of who drove which pane —
		// with it. That is a decision, not a cleanup.
		u.warn("--purge removes enrolled device tokens and the audit log; they are not recoverable")

		// The node's own project sits inside the state directory, and purge
		// must not take it. Its CLAUDE.md and memory are hand-written knowledge
		// about a machine — the same class as the env file and ~/.claude, which
		// setup scaffolds and never overwrites precisely because it does not own
		// them. Not owning something on the way in means not deleting it on the
		// way out.
		//
		// So the state directory is emptied entry by entry rather than removed
		// whole. `removeAll(stateDir)` would be shorter and would silently
		// destroy the one thing in there nobody can regenerate.
		if kept := u.purgeStateExceptNodeProject(); kept != "" {
			u.report("kept", "%s (this node's own project — not installed by setup's payload)", kept)
		}
	} else {
		u.report("kept", "%s (agent key, hub.db, authorized_agents) — --purge to remove", u.stateDir)
	}
	u.report("kept", "~/.claude, ~/.config/claude/env, ~/.config/claude-sessions/folders")
}

// ---------------------------------------------------------------------------
// linux
// ---------------------------------------------------------------------------

func (u *uninstall) servicesSystemd() {
	if _, err := exec.LookPath("systemctl"); err != nil {
		u.report("skipped", "systemctl not found — no systemd units to remove")
		return
	}

	// System units first, and only if present: `systemctl disable` on a unit
	// that was never installed is an error, and reporting it would send someone
	// looking for a problem that is really "it was already not there".
	var system []string
	for _, unit := range []string{hubUnit, nodeUnit} {
		if _, err := os.Stat(unit); err == nil {
			system = append(system, filepath.Base(unit))
		}
	}
	if len(system) > 0 {
		u.runCmd("sudo", append([]string{"systemctl", "disable", "--now"}, system...)...)
		for _, unit := range []string{hubUnit, nodeUnit} {
			u.removeRoot(unit)
		}
		u.runCmd("sudo", "systemctl", "daemon-reload")
	}

	userUnit := filepath.Join(home(), ".config", "systemd", "user", "claude-sessions.service")
	if _, err := os.Stat(userUnit); err == nil {
		u.runCmd("systemctl", "--user", "disable", "--now", "claude-sessions.service")
		u.removeFile(userUnit)
		u.runCmd("systemctl", "--user", "daemon-reload")
	}

	if len(system) == 0 {
		if _, err := os.Stat(userUnit); err != nil {
			u.report("unchanged", "no shabadoo units installed")
		}
	}
}

// ---------------------------------------------------------------------------
// darwin
// ---------------------------------------------------------------------------

func (u *uninstall) servicesLaunchd() {
	target := fmt.Sprintf("gui/%d", os.Getuid())
	found := false
	// The io.dumpr.* names are what these agents were called before the labels  // publish-check:allow
	// were renamed off a personal domain. Removed too: an uninstall that leaves
	// a live job behind under a name the tool no longer knows is the exact
	// failure this command exists to prevent.
	labels := []string{hubLabel, nodeLabel, startupLabel,
		"io.dumpr.shabadoo-hub", "io.dumpr.shabadoo-node", // publish-check:allow
		"io.dumpr.shabadoo-boot", "io.dumpr.claude-startup"} // publish-check:allow
	for _, label := range labels {
		path := launchAgentPath(label)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		found = true
		// bootout before removing the plist: launchd holds the job by label,
		// not by file, so deleting the plist first leaves the agent running
		// with nothing on disk to explain it — and it comes back at next login
		// only if the plist is there, so the state is genuinely confusing.
		u.runCmd("launchctl", "bootout", target+"/"+label)
		u.removeFile(path)
	}
	// setup backs up a plist it replaces; those accumulate and are ours.
	for _, label := range labels {
		matches, _ := filepath.Glob(launchAgentPath(label) + ".bak.*")
		for _, m := range matches {
			found = true
			u.removeFile(m)
		}
	}
	if !found {
		u.report("unchanged", "no shabadoo launch agents installed")
	}
}

// ---------------------------------------------------------------------------
// primitives
// ---------------------------------------------------------------------------

func (u *uninstall) removeFile(path string) {
	if _, err := os.Stat(path); err != nil {
		u.report("unchanged", "%s (not present)", path)
		return
	}
	if u.dryRun {
		u.removed++
		u.report("would remove", "%s", path)
		return
	}
	if err := os.Remove(path); err != nil {
		u.fail("remove %s: %v", path, err)
		return
	}
	u.removed++
	u.report("removed", "%s", path)
}

func (u *uninstall) removeAll(path string) {
	if _, err := os.Stat(path); err != nil {
		u.report("unchanged", "%s (not present)", path)
		return
	}
	if u.dryRun {
		u.removed++
		u.report("would remove", "%s (recursively)", path)
		return
	}
	if err := os.RemoveAll(path); err != nil {
		u.fail("remove %s: %v", path, err)
		return
	}
	u.removed++
	u.report("removed", "%s (recursively)", path)
}

func (u *uninstall) removeRoot(path string) {
	if _, err := os.Stat(path); err != nil {
		return
	}
	if u.dryRun {
		u.removed++
		u.report("would remove", "%s (sudo)", path)
		return
	}
	if err := run("sudo", "rm", "-f", path); err != nil {
		u.fail("remove %s: %v", path, err)
		return
	}
	u.removed++
	u.report("removed", "%s", path)
}

func (u *uninstall) runCmd(name string, args ...string) {
	line := name + " " + strings.Join(args, " ")
	if u.dryRun {
		u.report("would run", "%s", line)
		return
	}
	if err := run(name, args...); err != nil {
		// Not fatal: a unit that is already stopped, or a launchd job already
		// booted out, fails here and means the goal is already met. The file
		// removal that follows is what actually has to succeed.
		u.report("note", "%s: %v", line, err)
		return
	}
	u.report("ran", "%s", line)
}

func (u *uninstall) report(status, format string, a ...any) {
	fmt.Printf("    %-14s %s\n", status, fmt.Sprintf(format, a...))
}

func (u *uninstall) warn(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	u.warnings = append(u.warnings, msg)
	u.report("warn", "%s", msg)
}

func (u *uninstall) fail(format string, a ...any) {
	u.failed = true
	u.report("FAILED", "%s", fmt.Sprintf(format, a...))
}
