package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	hubUnit      = "/etc/systemd/system/shabadoo-hub.service"
	nodeUnit     = "/etc/systemd/system/shabadoo-node.service"
	caddyFile    = "/etc/caddy/Caddyfile"
	caddyEnv     = "/etc/caddy/caddy.env"
	hubLabel     = "dev.shabadoo.hub"
	nodeLabel    = "dev.shabadoo.node"
	startupLabel = "dev.shabadoo.boot"
	binName      = "shabadoo"
)

// Service management is the only genuinely platform-specific part of setup:
// everything else (scripts, PATH, deps, env, config) is the same on Linux and
// macOS. Each step dispatches on GOOS and fails clearly on anything else.

// stepService installs both halves of a shabadoo node: the coordinator
// (hub) and this host's agent (node). They are installed together
// because a coordinator with no agent has nothing to show, and the agent needs
// the coordinator's address — which this step is the one that decides.
//
// --coord names an existing coordinator instead, and installs the agent alone.
// That is what every machine after the first wants: a second coordinator would
// be a second dashboard showing one host, not another node on the first.
func (s *setup) stepService() error {
	var (
		auth  []string
		coord = s.coord
		err   error
	)
	if coord == "" {
		if auth, err = s.posture(); err != nil {
			return err
		}
		// The agent dials a concrete address. The coordinator may bind the
		// tailnet only, so loopback is not a safe assumption; resolve what it
		// will bind.
		if coord, err = coordURL(s.addr); err != nil {
			return fmt.Errorf("resolve coordinator URL from --addr %s: %w", s.addr, err)
		}
	} else {
		s.report("agent only", "joining coordinator %s", coord)
	}

	// Both platforms need the binary at the path the unit/plist will exec.
	if err := s.installSelf(); err != nil {
		return err
	}
	s.checkAgentCredentials()
	s.checkLegacyInstall()

	switch runtime.GOOS {
	case "linux":
		return s.serviceSystemd(auth, coord)
	case "darwin":
		return s.serviceLaunchd(auth, coord)
	default:
		return fmt.Errorf("--service is not supported on %s", runtime.GOOS)
	}
}

// posture returns the coordinator's human-plane auth flags.
//
// There is no default, because the posture that would "just work" is the
// absence of authentication, and that is not something an installer should
// choose quietly. hub exits without one of these too.
//
// --trust-network was removed 2026-07-31. It admitted every request from the
// bound network, which was the honest migration step while there was no way to
// enrol a client — but enrolment is a closed loop now (bootstrap -> pair -> QR
// -> read-only scopes), and the flag's only remaining effect would be to
// silently disable all of it: scopes stop applying, the audit log stops naming
// a device, and revocation stops meaning anything. One flag that turns off the
// whole security model is a trapdoor, not an option.
func (s *setup) posture() ([]string, error) {
	access := s.accessTeam != "" || s.accessAud != ""
	switch {
	case access:
		if s.accessTeam == "" || s.accessAud == "" {
			return nil, errors.New("Cloudflare Access needs both --access-team and --access-aud")
		}
		return []string{"--access-team", s.accessTeam, "--access-aud", s.accessAud}, nil
	case s.deviceTokens:
		return []string{"--device-tokens"}, nil
	default:
		return nil, errors.New(`--service needs an auth posture for the coordinator:
      --device-tokens                  every client presents an enrolled token
      --access-team X --access-aud Y   Cloudflare Access
    nothing written`)
	}
}

// checkAgentCredentials reports — but never creates — the two files the units
// need at runtime. The agent key is a credential and the authorized list is a
// trust decision; generating either silently would be setup making a security
// choice on the operator's behalf.
func (s *setup) checkAgentCredentials() {
	key := filepath.Join(s.stateDir, "agent_key")
	if _, err := os.Stat(key); err == nil {
		s.report("found", "agent key %s", key)
	} else {
		s.warn("%s does not exist — node cannot authenticate. Create it with:\n"+
			"      ssh-keygen -t ed25519 -N '' -C %s -f %s", key, hostLabel(), key)
	}

	if s.coord != "" {
		// The authorized list lives on the coordinator, not here.
		s.report("note", "add this host to the coordinator's authorized_agents:\n"+
			"      printf '%%s %%s\\n' \"$(cut -d' ' -f1,2 %s.pub)\" %s", key, hostLabel())
		return
	}
	agents := filepath.Join(s.stateDir, "authorized_agents")
	if _, err := os.Stat(agents); err == nil {
		s.report("found", "authorized agents %s", agents)
	} else {
		s.warn("%s does not exist — hub exits at startup without it. Seed it with "+
			"this host's key (the comment field is the node name):\n"+
			"      printf '%%s %%s\\n' \"$(cut -d' ' -f1,2 %s.pub)\" %s >> %s",
			agents, key, hostLabel(), agents)
	}
}

// checkLegacyInstall warns about a pre-rename install still on this host.
// The old units are named differently, so installing the new ones does not
// replace them: both would bind the same address, and the new coordinator
// would open an empty database while the enrolled devices stay in the old one.
// Reported rather than fixed — stopping services and moving a database is the
// operator's call, not something an installer should do while they watch.
func (s *setup) checkLegacyInstall() {
	legacyState := filepath.Join(home(), ".config", "phalanx")
	if _, err := os.Stat(filepath.Join(legacyState, "polemarch.db")); err == nil {
		s.warn("legacy state found at %s — the new units read %s and would start "+
			"with no enrolled devices. Move it across before starting them:\n"+
			"      mv %s/polemarch.db %s/hub.db\n"+
			"      mv %s/{agent_key,agent_key.pub,authorized_agents,coord,cli_token} %s/",
			legacyState, s.stateDir, legacyState, s.stateDir, legacyState, s.stateDir)
	}

	// The launchd labels used to be io.dumpr.* — a reverse-DNS prefix naming a  // publish-check:allow
	// personal domain, which had no business shipping to anyone else. launchd
	// holds a job by LABEL, so a renamed one does not replace the old: without
	// this, setup installs a second agent beside the first and both drive the
	// same tmux server.
	if runtime.GOOS == "darwin" {
		for _, old := range []string{
			"io.dumpr.shabadoo-hub", "io.dumpr.shabadoo-node", // publish-check:allow
			"io.dumpr.shabadoo-boot", "io.dumpr.claude-startup", // publish-check:allow
		} {
			if _, err := os.Stat(launchAgentPath(old)); err != nil {
				continue
			}
			s.warn("legacy launch agent %s is still installed — it would run ALONGSIDE "+
				"the renamed one, two agents driving the same tmux server. Retire it:\n"+
				"      launchctl bootout gui/$(id -u)/%s ; rm ~/Library/LaunchAgents/%s.plist",
				old, old, old)
		}
	}

	if runtime.GOOS != "linux" {
		return
	}
	for _, old := range []string{"polemarch", "hoplite"} {
		unit := "/etc/systemd/system/" + old + ".service"
		if _, err := os.Stat(unit); err != nil {
			continue
		}
		s.warn("legacy unit %s is still installed — it binds the same address as the "+
			"unit this step writes. Retire it:\n"+
			"      sudo systemctl disable --now %s && sudo rm %s",
			unit, old, unit)
	}
}

// coordURL is the base URL the local agent dials to reach the coordinator this
// step installs. Unlike the coordinator's own --addr, which hub resolves
// at startup, this is resolved once at install time — so a re-assigned
// Tailscale address means re-running setup.
func coordURL(addr string) (string, error) {
	resolved, err := resolveAddr(addr)
	if err != nil {
		return "", err
	}
	host, port, err := net.SplitHostPort(resolved)
	if err != nil {
		return "", err
	}
	// A wildcard bind is reachable on loopback; a specific one may not be.
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port), nil
}

// stepBoot installs the login-time launcher that opens a Claude window per
// folder in ~/.config/claude-sessions/folders. User-scoped on both platforms,
// so neither variant needs root.
func (s *setup) stepBoot() error {
	bin := filepath.Join(s.binDir, binName)
	if _, err := os.Stat(bin); err != nil {
		return fmt.Errorf("%s not installed — run the binary step first", bin)
	}
	// `shabadoo boot` reads this path directly (overridable via
	// $CLAUDE_SESSIONS_LIST), so mirror its default rather than joinConfig's.
	folders := filepath.Join(home(), ".config", "claude-sessions", "folders")
	if _, err := os.Stat(folders); errors.Is(err, fs.ErrNotExist) {
		s.warn("%s does not exist — the boot launcher will start nothing until you list folders in it", folders)
	}

	switch runtime.GOOS {
	case "linux":
		return s.bootSystemdUser(bin)
	case "darwin":
		return s.bootLaunchd(bin)
	default:
		return fmt.Errorf("--boot is not supported on %s", runtime.GOOS)
	}
}

// installSelf copies the running executable to <bin-dir>/shabadoo when that
// path is missing or stale, so the unit's ExecStart resolves.
func (s *setup) installSelf() error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate running binary: %w", err)
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return err
	}
	dst := filepath.Join(s.binDir, binName)
	if self == dst {
		s.report("ok", "running from %s", dst)
		return nil
	}
	if err := s.guardDowngrade(dst); err != nil {
		return err
	}
	data, err := os.ReadFile(self)
	if err != nil {
		return err
	}
	return s.installFile(dst, data, 0o755)
}

// buildStamp is what a binary reports about itself via `version --json`.
type buildStamp struct {
	Version string `json:"version"`
	Built   string `json:"built"`
}

// stampOf asks an installed binary how old it is. Anything unexpected — the
// file is missing, is not this program, or predates `version --json` — returns
// a zero stamp, because "cannot tell" must not become "refuse to install".
func stampOf(path string) (buildStamp, time.Time, bool) {
	var st buildStamp
	out, err := exec.Command(path, "version", "--json").Output()
	if err != nil {
		return st, time.Time{}, false
	}
	if err := json.Unmarshal(out, &st); err != nil || st.Built == "" {
		return st, time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, st.Built)
	if err != nil {
		return st, time.Time{}, false
	}
	return st, t, true
}

// guardDowngrade refuses to replace a newer installed binary with an older one.
//
// `setup --service` installs THE BINARY THAT IS RUNNING IT, so a stale checkout
// silently downgrades a host: the old binary is backed up, the units restart
// happily, and the only symptom is endpoints quietly going missing. Version
// strings from `git describe` cannot be ordered, which is why builds carry a
// commit timestamp.
func (s *setup) guardDowngrade(dst string) error {
	if _, err := os.Stat(dst); err != nil {
		return nil // nothing installed yet
	}
	theirs, theirTime, ok := stampOf(dst)
	if !ok {
		return nil // predates the stamp, or is not us — nothing to compare
	}

	// An unstamped build is a hand-rolled `go build`. Letting one silently
	// replace a stamped release is the same hazard from the other direction,
	// and the operator is right there to pass --force.
	if buildTime == "" {
		if s.force {
			s.warn("installing an unstamped build over %s (--force)", theirs.Version)
			return nil
		}
		return fmt.Errorf("refusing to install an unstamped build over %s (built %s)\n"+
			"      this binary carries no build stamp, so its age cannot be compared.\n"+
			"      usually a plain `go build` — use `make install` instead; a container\n"+
			"      image needs both --build-arg VERSION and --build-arg BUILT.\n"+
			"      or pass --force if you mean it",
			theirs.Version, theirs.Built)
	}

	ourTime, err := time.Parse(time.RFC3339, buildTime)
	if err != nil {
		return nil
	}
	if theirTime.After(ourTime) {
		if s.force {
			s.warn("downgrading %s (%s) to %s (%s) — --force",
				theirs.Version, theirs.Built, version, buildTime)
			return nil
		}
		return fmt.Errorf("refusing to downgrade %s: installed %s is NEWER than this build %s\n"+
			"      installed: %s (%s)\n"+
			"      this one:  %s (%s)\n"+
			"      you are probably running setup from a stale checkout — `git pull && make install`.\n"+
			"      pass --force to install it anyway",
			dst, theirs.Version, version,
			theirs.Version, theirs.Built, version, buildTime)
	}
	return nil
}

// servicePATH is the PATH a service needs for its shell-outs: claude-sessions
// lives in bin-dir and `claude` is usually under ~/.local/bin, neither of
// which is on the minimal PATH systemd/launchd hand a job.
func (s *setup) servicePATH() string {
	parts := []string{s.binDir}
	if p, err := exec.LookPath("claude"); err == nil {
		if dir := filepath.Dir(p); dir != s.binDir {
			parts = append(parts, dir)
		}
	}
	if runtime.GOOS == "darwin" {
		parts = append(parts, "/opt/homebrew/bin")
	}
	return strings.Join(append(parts, "/usr/local/bin", "/usr/bin", "/bin"), ":")
}

// ---------------------------------------------------------------------------
// linux: systemd
// ---------------------------------------------------------------------------

func (s *setup) serviceSystemd(auth []string, coord string) error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return errors.New("systemctl not found — this host does not use systemd")
	}

	// name is what systemctl is called with, so it must be the unit's name —
	// `shabadoo-hub`, not the role `hub`. They differ, which the role rename
	// made easy to conflate: the unit file is named for the product, the
	// subcommand for the role.
	type unit struct{ name, path, body string }
	var units []unit
	if s.coord == "" {
		units = append(units, unit{"shabadoo-hub", hubUnit, s.hubUnitFile(auth)})
	}
	units = append(units, unit{"shabadoo-node", nodeUnit, s.nodeUnitFile(coord)})

	anyChanged := false
	for _, u := range units {
		changed, err := s.rootInstall(u.path, []byte(u.body), "0644")
		if err != nil {
			return err
		}
		anyChanged = anyChanged || changed
		if s.dryRun && changed {
			s.report("would write", "%s:", u.path)
			s.printIndented(u.body)
		}
	}

	names := make([]string, len(units))
	for i, u := range units {
		names[i] = u.name
	}
	if s.dryRun {
		s.report("would run", "systemctl daemon-reload && systemctl enable --now %s",
			strings.Join(names, " "))
		return nil
	}

	if anyChanged {
		if err := s.sudo("systemctl", "daemon-reload"); err != nil {
			return err
		}
		s.report("ran", "systemctl daemon-reload")
	}
	// enable --now is a no-op when already running, so a changed unit or a
	// fresh binary only takes effect on an explicit restart. Order matters:
	// the agent's first dial should find a coordinator already listening.
	for _, u := range units {
		if err := s.sudo("systemctl", "enable", "--now", u.name); err != nil {
			return err
		}
		if err := s.sudo("systemctl", "restart", u.name); err != nil {
			return err
		}
		s.report("restarted", "%s.service", u.name)
	}

	for _, u := range units {
		out, _ := exec.Command("systemctl", "is-active", u.name).Output()
		if state := strings.TrimSpace(string(out)); state != "active" {
			s.warn("%s.service is %q after restart — check: journalctl -u %s -n 50", u.name, state, u.name)
		}
	}
	s.report("active", "%s", s.nodeSummary(coord))
	return nil
}

// nodeSummary describes what this host now runs, for the closing report line.
func (s *setup) nodeSummary(coord string) string {
	if s.coord != "" {
		return fmt.Sprintf("agent %q dialling %s", hostLabel(), coord)
	}
	return fmt.Sprintf("coordinator on %s, agent %q dialling %s", s.addr, hostLabel(), coord)
}

// unitOwner is the User/Group pair both units share.
func (s *setup) unitOwner() (uname, group string) {
	uname = os.Getenv("USER")
	if uname == "" {
		uname = filepath.Base(home())
	}
	group = uname
	if g, err := user.LookupGroupId(fmt.Sprint(os.Getgid())); err == nil {
		group = g.Name
	}
	return uname, group
}

func (s *setup) hubUnitFile(auth []string) string {
	uname, group := s.unitOwner()

	// The listen address is a per-host choice (loopback behind a proxy, or the
	// tailnet directly), so the description does not assert either. It is left
	// unresolved here on purpose: hub expands `tailscale:PORT` at
	// startup, so a re-assigned tailnet address still binds without a re-run.
	return fmt.Sprintf(`[Unit]
Description=shabadoo hub (coordinator)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=%s
Group=%s
Environment=HOME=%s
Environment=PATH=%s
WorkingDirectory=%s
ExecStart=%s hub --addr %s --db %s --agents %s %s
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target

# Managed by "shabadoo setup --service". Local edits are backed up, not kept.
`, uname, group, home(), s.servicePATH(), s.stateDir,
		filepath.Join(s.binDir, binName), s.addr,
		filepath.Join(s.stateDir, "hub.db"),
		filepath.Join(s.stateDir, "authorized_agents"),
		strings.Join(auth, " "))
}

func (s *setup) nodeUnitFile(coord string) string {
	uname, group := s.unitOwner()

	// Order after the coordinator only when it is this host's; against a remote
	// one there is no local unit to wait for, and the agent reconnects anyway.
	after := "After=shabadoo-hub.service\nWants=shabadoo-hub.service"
	if s.coord != "" {
		after = "After=network-online.target\nWants=network-online.target"
	}

	// Unlike the coordinator, the agent is the half that shells out: PATH
	// covers claude-sessions and claude, and CLAUDE_HOST_LABEL keeps
	// relaunched windows named the way claude.sh names them — the unit does
	// not source ~/.config/claude/env.
	return fmt.Sprintf(`[Unit]
Description=shabadoo node (per-host agent)
%s

[Service]
Type=simple
User=%s
Group=%s
Environment=HOME=%s
Environment=PATH=%s
Environment=CLAUDE_HOST_LABEL=%s
# A service manager supplies no locale at all. The parser no longer depends on
# one (the tmux field separator is printable now, deliberately), but pane text
# is UTF-8 and tmux mangles non-printable bytes without this — so it stays as
# defence in depth, not as the thing holding session reporting together.
Environment=LANG=en_US.UTF-8
WorkingDirectory=%s
ExecStart=%s node --coord %s --node %s --key %s
# The tmux server this agent starts for the core session lands in THIS unit's
# cgroup, so the default KillMode=control-group takes EVERY Claude session on
# the machine down with any restart of the agent — and upgrade restarts the
# agent by design, exiting non-zero for the supervisor to bring it back.
#
# Four upgrades in ninety minutes killed the tmux server four times, dropping
# the operator out of their terminal each time and taking twenty sessions with
# it. Every check in upgrade was about not bricking the NODE; the actual blast
# radius was every session on the host.
#
# KillMode=process stops only the agent. The sessions are deliberately
# longer-lived than the thing that reports on them — the agent is a supervisor,
# not an owner. Verified with a probe that confirms cgroup membership and goes
# red without this line.
KillMode=process
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target

# Managed by "shabadoo setup --service". Local edits are backed up, not kept.
`, after, uname, group, home(), s.servicePATH(), hostLabel(), home(),
		filepath.Join(s.binDir, binName), coord, hostLabel(),
		filepath.Join(s.stateDir, "agent_key"))
}

// bootSystemdUser installs the user-scoped unit that runs `shabadoo boot` at
// login. User units live under $HOME, so this needs no sudo.
func (s *setup) bootSystemdUser(bin string) error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return errors.New("systemctl not found — this host does not use systemd")
	}
	dst := filepath.Join(home(), ".config", "systemd", "user", "claude-sessions.service")
	unit := fmt.Sprintf(`[Unit]
Description=Pre-launch Claude Code tmux windows for configured project folders
Documentation=file:%%h/.config/claude-sessions/folders
After=default.target

[Service]
Type=oneshot
RemainAfterExit=yes
# User units start with a minimal PATH that excludes ~/.local/bin and ~/bin,
# so the launcher would not find `+"`claude`"+`. Set an explicit PATH.
Environment=PATH=%s
# Match the host label interactive shells use, so service-spawned windows
# dedupe with manually-opened ones instead of getting a separate suffix.
Environment=CLAUDE_HOST_LABEL=%s
ExecStart=%s boot
# "shabadoo boot" starts a tmux server and detaches; the unit completes once
# all configured windows have been created.
TimeoutStartSec=120

[Install]
WantedBy=default.target

# Managed by "shabadoo setup --boot". Local edits are backed up, not kept.
`, s.servicePATH(), hostLabel(), bin)

	if err := s.installFile(dst, []byte(unit), 0o644); err != nil {
		return err
	}
	if s.dryRun {
		// This replaces a unit that may already be enabled and running —
		// show exactly what would land before it does.
		s.printIndented(unit)
		s.report("would run", "systemctl --user daemon-reload && systemctl --user enable --now claude-sessions")
		return nil
	}
	if err := run("systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	if err := run("systemctl", "--user", "enable", "--now", "claude-sessions.service"); err != nil {
		return err
	}
	s.report("enabled", "claude-sessions.service (user)")
	return nil
}

// ---------------------------------------------------------------------------
// darwin: launchd
// ---------------------------------------------------------------------------

func launchAgentPath(label string) string {
	return filepath.Join(home(), "Library", "LaunchAgents", label+".plist")
}

func (s *setup) serviceLaunchd(auth []string, coord string) error {
	bin := filepath.Join(s.binDir, binName)
	type job struct {
		label string
		args  []string
		log   string
	}
	var agents []job
	if s.coord == "" {
		agents = append(agents, job{hubLabel, append([]string{bin, "hub",
			"--addr", s.addr,
			"--db", filepath.Join(s.stateDir, "hub.db"),
			"--agents", filepath.Join(s.stateDir, "authorized_agents"),
		}, auth...), "/tmp/hub.log"})
	}
	agents = append(agents, job{nodeLabel, []string{bin, "node",
		"--coord", coord,
		"--node", hostLabel(),
		"--key", filepath.Join(s.stateDir, "agent_key"),
	}, "/tmp/node.log"})

	for _, a := range agents {
		plist := s.plist(a.label, a.args, true, a.log)
		dst := launchAgentPath(a.label)
		if err := s.installFile(dst, []byte(plist), 0o644); err != nil {
			return err
		}
		if s.dryRun {
			s.printIndented(plist)
			s.report("would run", "launchctl bootstrap gui/$UID %s", dst)
			continue
		}
		if err := s.launchctlLoad(a.label, dst); err != nil {
			return err
		}
		s.report("loaded", "%s", a.label)
	}
	if !s.dryRun {
		s.report("active", "%s", s.nodeSummary(coord))
	}
	return nil
}

func (s *setup) bootLaunchd(bin string) error {
	// RunAtLoad without KeepAlive: this is a one-shot that opens the windows
	// and exits, not a daemon. KeepAlive would respawn it forever.
	plist := s.plist(startupLabel, []string{bin, "boot"}, false, "/tmp/shabadoo-boot.log")

	dst := launchAgentPath(startupLabel)
	if err := s.installFile(dst, []byte(plist), 0o644); err != nil {
		return err
	}
	if s.dryRun {
		s.printIndented(plist)
		s.report("would run", "launchctl bootstrap gui/$UID %s", dst)
		return nil
	}
	if err := s.launchctlLoad(startupLabel, dst); err != nil {
		return err
	}
	s.report("loaded", "%s (runs at login)", startupLabel)
	return nil
}

func (s *setup) plist(label string, args []string, keepAlive bool, logPath string) string {
	var argXML strings.Builder
	for _, a := range args {
		fmt.Fprintf(&argXML, "\n\t\t<string>%s</string>", xmlEscape(a))
	}
	keep := "<false/>"
	if keepAlive {
		keep = "<true/>"
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<!-- Managed by "shabadoo setup". Local edits are backed up, not kept. -->
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>%s
	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>HOME</key>
		<string>%s</string>
		<key>PATH</key>
		<string>%s</string>
		<key>CLAUDE_HOST_LABEL</key>
		<string>%s</string>
		<!-- launchd starts a job with no locale at all, and tmux mangles
		     non-printable bytes in its output when it is not in UTF-8 mode.
		     That once broke session reporting outright, because the field
		     separator was itself non-printable; it is printable now, so this is
		     defence in depth for pane CONTENT rather than the thing holding
		     reporting together. Interactive shells have a locale; a service
		     manager does not, which is why it has to be stated here. -->
		<key>LANG</key>
		<string>en_US.UTF-8</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<!-- The tmux server this agent starts for the core session is a child of
	     this job, so tearing the job down takes every Claude session on the
	     machine with it — and upgrade tears it down BY DESIGN, exiting
	     non-zero for launchd to bring it back. On the Linux side the same
	     defect dropped an operator out of their terminal four times in ninety
	     minutes and killed twenty sessions.

	     AbandonProcessGroup is launchd's counterpart to systemd's
	     KillMode=process: stop the job, leave what it spawned alone. The
	     sessions are deliberately longer-lived than the thing reporting on
	     them.

	     UNVERIFIED ON DARWIN at the time of writing — the systemd half was
	     proven with a probe that confirms cgroup membership and goes red
	     without the fix, and this machine cannot run the launchd equivalent.
	     It is written down as unverified rather than assumed correct. -->
	<key>AbandonProcessGroup</key>
	<true/>
	<key>KeepAlive</key>
	%s
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, xmlEscape(label), argXML.String(), xmlEscape(home()), xmlEscape(s.servicePATH()),
		xmlEscape(hostLabel()), keep, xmlEscape(logPath), xmlEscape(logPath))
}

// launchctlLoad re-bootstraps the agent so an updated plist takes effect.
//
// `bootout` returns before launchd has finished tearing the job down, and a
// `bootstrap` issued into that window fails with "Bootstrap failed: 5: Input/
// output error". The failure is worse than it looks: the plist on disk is
// already the new one, so a re-run reports "unchanged" and does nothing, while
// the *old* process keeps running with the old arguments. That is how a Mac
// agent kept talking to a decommissioned coordinator after being told to move.
//
// So: wait for the job to actually disappear before bootstrapping, and retry.
// bootout failing is still fine and ignored — on a first install there is
// nothing loaded to remove.
func (s *setup) launchctlLoad(label, path string) error {
	target := fmt.Sprintf("gui/%d", os.Getuid())
	service := target + "/" + label

	_ = run("launchctl", "bootout", service)

	// Poll rather than sleeping a fixed amount: teardown is usually instant,
	// and a wedged job should not be papered over with a longer sleep.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := run("launchctl", "print", service); err != nil {
			break // gone
		}
		time.Sleep(100 * time.Millisecond)
	}

	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if err = run("launchctl", "bootstrap", target, path); err == nil {
			_ = run("launchctl", "kickstart", "-k", service)
			return nil
		}
		time.Sleep(time.Duration(attempt+1) * 300 * time.Millisecond)
	}
	return fmt.Errorf("launchctl bootstrap: %w\n"+
		"      the plist was written but the service did not reload, so the "+
		"previously running agent is still up with its OLD configuration.\n"+
		"      recover with: launchctl bootout %s ; launchctl bootstrap %s %s",
		err, service, target, path)
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

// ---------------------------------------------------------------------------
// caddy (linux only)
// ---------------------------------------------------------------------------

func (s *setup) stepCaddy() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("--caddy is linux-only; on %s bind the tailnet directly with --addr tailscale:PORT", runtime.GOOS)
	}
	if _, err := exec.LookPath("caddy"); err != nil {
		return errors.New("caddy not found in PATH")
	}

	host := s.caddyHost
	if host == "" {
		// No built-in domain. A default that named someone's own zone would be
		// wrong for everybody else and quietly right for one person, which is
		// the worst of both — so this is required rather than guessed.
		return errors.New("--caddy-host is required: there is no default vhost " +
			"(try tmux.<host>.<your-domain>)")
	}

	bind := s.caddyBind
	if bind == "" {
		var err error
		if bind, err = tailscaleIPv4(); err != nil {
			return fmt.Errorf("could not determine Tailscale IP (%v) — pass --caddy-bind", err)
		}
		s.report("default", "caddy-bind %s (this host's Tailscale address)", bind)
	}

	current, err := s.rootRead(caddyFile)
	if err != nil {
		return fmt.Errorf("read %s: %w", caddyFile, err)
	}

	// Idempotency: leave any hand-tuned existing block alone rather than
	// appending a duplicate site address, which Caddy rejects outright.
	if strings.Contains(string(current), host) {
		s.report("unchanged", "%s already has a block for %s", caddyFile, host)
		return nil
	}

	block := fmt.Sprintf(`
%s {
	# Bind to the Tailscale interface only. Nothing on LAN or loopback.
	bind %s
	tls {
		dns cloudflare {env.CF_API_TOKEN}
		resolvers 1.1.1.1
	}
	encode zstd gzip
	reverse_proxy %s
	log {
		output file /var/log/caddy/tmux.access.log {
			roll_size 10mb
			roll_keep 5
		}
	}
}
`, host, bind, s.addr)

	if s.dryRun {
		s.report("would append", "this block to %s:", caddyFile)
		s.printIndented(block)
		s.report("would run", "caddy validate, then systemctl reload caddy")
		return nil
	}

	updated := append(bytes.TrimRight(current, "\n"), append([]byte("\n"), block...)...)
	if _, err := s.rootInstall(caddyFile, updated, "0644"); err != nil {
		return err
	}

	// Validate before reloading: a bad block would take down every other
	// vhost this Caddy fronts, not just the coordinator. Roll back if it fails.
	if err := s.caddyValidate(); err != nil {
		if _, rerr := s.rootInstall(caddyFile, current, "0644"); rerr != nil {
			return fmt.Errorf("caddy validate failed (%v) AND rollback failed (%v) — restore %s from the .bak file by hand", err, rerr, caddyFile)
		}
		s.report("rolled back", "%s restored — config unchanged", caddyFile)
		return fmt.Errorf("caddy validate rejected the new block: %w", err)
	}
	s.report("validated", "%s", caddyFile)

	if err := s.sudo("systemctl", "reload", "caddy"); err != nil {
		return fmt.Errorf("caddy reload failed: %w", err)
	}
	s.report("reloaded", "caddy — https://%s/ should now serve the coordinator", host)
	return nil
}

// caddyValidate runs the config through caddy's own parser with the Caddyfile
// env sourced. Without those vars the tls block's {env.CF_API_TOKEN} expands
// empty and validation fails on a perfectly good config.
func (s *setup) caddyValidate() error {
	script := fmt.Sprintf(`set -a; [ -f %s ] && . %s; set +a; exec caddy validate --adapter caddyfile --config %s`,
		caddyEnv, caddyEnv, caddyFile)
	out, err := exec.Command("sudo", "bash", "-c", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

// tailscaleIPv4 returns this machine's Tailscale IPv4. The macOS app ships the
// CLI inside the bundle rather than on PATH, so probe there too.
func tailscaleIPv4() (string, error) {
	candidates := []string{"tailscale"}
	if runtime.GOOS == "darwin" {
		candidates = append(candidates,
			"/Applications/Tailscale.app/Contents/MacOS/Tailscale",
			"/usr/local/bin/tailscale",
			"/opt/homebrew/bin/tailscale")
	}
	var lastErr error
	for _, bin := range candidates {
		if _, err := exec.LookPath(bin); err != nil {
			if _, serr := os.Stat(bin); serr != nil {
				continue
			}
		}
		out, err := exec.Command(bin, "ip", "-4").Output()
		if err != nil {
			lastErr = err
			continue
		}
		if ip := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0]); ip != "" {
			return ip, nil
		}
	}
	if lastErr == nil {
		lastErr = errors.New("tailscale CLI not found")
	}
	return "", lastErr
}

// ---------------------------------------------------------------------------
// privileged helpers
//
// Only the writes below escalate. The rest of setup runs as the invoking user
// so that running `sudo shabadoo setup` is never required — that would
// install the user-level files into root's home.
// ---------------------------------------------------------------------------

// rootRead reads a file, falling back to `sudo cat` when it is root-only.
func (s *setup) rootRead(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return data, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	out, serr := exec.Command("sudo", "cat", path).Output()
	if serr != nil {
		return nil, err // report the original, more meaningful error
	}
	return out, nil
}

// rootInstall writes data to a root-owned path via sudo, backing up any
// differing existing file first. Reports whether it changed anything.
func (s *setup) rootInstall(dst string, data []byte, mode string) (bool, error) {
	old, err := s.rootRead(dst)
	if err == nil && bytes.Equal(old, data) {
		s.report("unchanged", "%s", dst)
		return false, nil
	}
	exists := err == nil

	if s.dryRun {
		if exists {
			s.report("would update", "%s (as root)", dst)
		} else {
			s.report("would install", "%s (as root)", dst)
		}
		return true, nil
	}

	if exists {
		bak := fmt.Sprintf("%s.bak.%d", dst, time.Now().Unix())
		if err := s.sudo("cp", "-p", dst, bak); err != nil {
			return false, fmt.Errorf("backup %s: %w", dst, err)
		}
		s.report("backed up", "%s", bak)
	}

	tmp, err := os.CreateTemp("", "shabadoo-root-*")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}

	if err := s.sudo("install", "-m", mode, "-o", "root", "-g", "root", tmp.Name(), dst); err != nil {
		return false, err
	}
	if exists {
		s.report("updated", "%s", dst)
	} else {
		s.report("installed", "%s", dst)
	}
	return true, nil
}

func (s *setup) sudo(name string, args ...string) error {
	cmd := exec.Command("sudo", append([]string{name}, args...)...)
	cmd.Stdin = os.Stdin // allow the password prompt through
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("sudo %s %s: %s", name, strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}

func (s *setup) printIndented(text string) {
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		fmt.Printf("      | %s\n", line)
	}
}
