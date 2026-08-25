package main

import (
	"os"
	"path/filepath"
	"testing"
)

// installFile is the primitive every step writes through, so its contract —
// never clobber differing content without a backup, never rewrite identical
// content — is what these tests pin down.
func TestInstallFile(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "nested", "claude.sh")
	s := &setup{}

	if err := s.installFile(dst, []byte("v1"), 0o755); err != nil {
		t.Fatalf("install: %v", err)
	}
	if got := readFile(t, dst); got != "v1" {
		t.Errorf("content = %q, want v1", got)
	}
	if st, _ := os.Stat(dst); st.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v, want 0755", st.Mode().Perm())
	}

	// Re-installing identical content must not produce a backup.
	if err := s.installFile(dst, []byte("v1"), 0o755); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	if n := len(backups(t, dst)); n != 0 {
		t.Errorf("identical reinstall made %d backups, want 0", n)
	}

	// Changed content must back up the old version before replacing it.
	if err := s.installFile(dst, []byte("v2"), 0o755); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := readFile(t, dst); got != "v2" {
		t.Errorf("content = %q, want v2", got)
	}
	baks := backups(t, dst)
	if len(baks) != 1 {
		t.Fatalf("update made %d backups, want 1", len(baks))
	}
	if got := readFile(t, baks[0]); got != "v1" {
		t.Errorf("backup holds %q, want the replaced v1", got)
	}
}

func TestInstallFileDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "claude.sh")
	s := &setup{dryRun: true}

	if err := s.installFile(dst, []byte("v1"), 0o755); err != nil {
		t.Fatalf("dry-run install: %v", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("dry run created %s", dst)
	}
}

func TestMentionsDir(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	binDir := filepath.Join(home, "bin")

	// The tilde and $HOME forms are the ones a literal-path match misses.
	for _, body := range []string{
		`export PATH=` + binDir + `:$PATH`,
		`[[ -d ~/bin ]] && export PATH=~/bin:${PATH}`,
		`export PATH="$HOME/bin:$PATH"`,
		`export PATH="${HOME}/bin:$PATH"`,
	} {
		if !mentionsDir(body, binDir) {
			t.Errorf("mentionsDir(%q) = false, want true", body)
		}
	}
	if mentionsDir("export PATH=/usr/local/bin:$PATH", binDir) {
		t.Error("mentionsDir matched an unrelated PATH entry")
	}
}

func TestEnvFileValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "env")
	body := `# a comment
export CLAUDE_HOST_LABEL=wsl
# export CLAUDE_BIN=commented-out
export CLAUDE_ARGS="--dangerously-skip-permissions"
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := envFileValue(path, "CLAUDE_HOST_LABEL"); got != "wsl" {
		t.Errorf("host label = %q, want wsl", got)
	}
	if got := envFileValue(path, "CLAUDE_ARGS"); got != "--dangerously-skip-permissions" {
		t.Errorf("args = %q, quotes should be stripped", got)
	}
	if got := envFileValue(path, "CLAUDE_SESSION_NAME"); got != "" {
		t.Errorf("absent key = %q, want empty", got)
	}
	if got := envFileValue(filepath.Join(dir, "nope"), "X"); got != "" {
		t.Errorf("missing file = %q, want empty", got)
	}
}

func TestSanitizeLabel(t *testing.T) {
	// Must match claude.sh's `tr -c 'A-Za-z0-9_-' '_'` so the window names
	// this binary predicts equal the ones the script creates.
	cases := map[string]string{
		"wsl":             "wsl",
		"DESKTOP-HVF1AKM": "DESKTOP-HVF1AKM",
		"alexs.macbook":   "alexs_macbook",
		"a b/c":           "a_b_c",
	}
	for in, want := range cases {
		if got := sanitizeLabel(in); got != want {
			t.Errorf("sanitizeLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func backups(t *testing.T, dst string) []string {
	t.Helper()
	m, err := filepath.Glob(dst + ".bak.*")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// `setup --service` installs THE BINARY THAT IS RUNNING IT, so running it from
// a stale checkout silently downgrades a host: the old binary is backed up, the
// units restart happily, and the only symptom is endpoints quietly going
// missing. These pin the guard that stops it.
//
// The comparison is on the build TIMESTAMP because `git describe` output cannot
// be ordered — given "a376549" and "1c8fb18" there is no way to say which came
// first.
func TestGuardDowngrade(t *testing.T) {
	const (
		newer = "2026-07-31T01:00:00Z"
		older = "2025-07-31T01:00:00Z"
	)

	// Stand in for an installed binary by building a tiny script that answers
	// `version --json` the way the real one does.
	install := func(t *testing.T, dir, ver, built string) string {
		t.Helper()
		path := filepath.Join(dir, "shabadoo")
		body := "#!/bin/sh\n"
		if built != "" {
			body += `printf '{"version":"` + ver + `","built":"` + built + `"}\n'` + "\n"
		} else {
			body += "printf 'not-this-program\\n'\n" // predates version --json
		}
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}

	cases := []struct {
		name       string
		ourBuild   string // buildTime of the binary doing the installing
		installed  string // build stamp already on disk ("" = unreadable)
		force      bool
		wantRefuse bool
	}{
		{"upgrade is allowed", newer, older, false, false},
		{"same build is allowed", newer, newer, false, false},
		{"downgrade is refused", older, newer, false, true},
		{"downgrade with --force is allowed", older, newer, true, false},
		{"unstamped over stamped is refused", "", newer, false, true},
		{"unstamped with --force is allowed", "", newer, true, false},
		{"unreadable stamp cannot be compared, so proceed", newer, "", false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			install(t, dir, "INSTALLED", tc.installed)

			saved := buildTime
			buildTime = tc.ourBuild
			defer func() { buildTime = saved }()

			s := &setup{binDir: dir, force: tc.force}
			err := s.guardDowngrade(filepath.Join(dir, "shabadoo"))

			if tc.wantRefuse && err == nil {
				t.Errorf("expected a refusal, got nil — a stale binary would install silently")
			}
			if !tc.wantRefuse && err != nil {
				t.Errorf("expected no refusal, got: %v", err)
			}
		})
	}
}

// Nothing installed yet is the fresh-machine case and must never be blocked.
func TestGuardDowngradeAllowsFreshTarget(t *testing.T) {
	s := &setup{binDir: t.TempDir()}
	if err := s.guardDowngrade(filepath.Join(s.binDir, "shabadoo")); err != nil {
		t.Errorf("guard blocked a fresh install: %v", err)
	}
}

// Uninstall's one safety property: --dry-run writes nothing. The command exists
// to be run speculatively on a host someone is unsure about, and a dry run that
// removed anything would be worse than having no uninstaller at all.
func TestUninstallDryRunRemovesNothing(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	state := filepath.Join(dir, "state")
	for _, d := range []string{bin, state} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	binPath := filepath.Join(bin, binName)
	statePath := filepath.Join(state, "agent_key")
	for _, f := range []string{binPath, statePath} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	u := &uninstall{binDir: bin, stateDir: state, dryRun: true, all: true, purge: true}
	u.removeFile(binPath)
	u.removeAll(state)

	for _, f := range []string{binPath, statePath} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("dry run removed %s", f)
		}
	}
	if u.removed != 2 {
		t.Errorf("removed count = %d, want 2 (counted, not performed)", u.removed)
	}
}

// Without --purge the state directory is kept, because agent_key surviving is
// what makes a reinstall come back under its existing authorization instead of
// needing a new key appended on the coordinator.
//
// NOTE: this calls stepState, NOT run. run() disables and removes real system
// services at hardcoded paths — an earlier draft of this test called it and
// uninstalled the developer's own shabadoo-node unit, which is the systemd
// version of the tmux hazard already documented in CLAUDE.md. Nothing in this
// package's tests may invoke a code path that names a real system location.
func TestUninstallKeepsStateWithoutPurge(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(state, "agent_key")
	if err := os.WriteFile(key, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	u := &uninstall{binDir: filepath.Join(dir, "bin"), stateDir: state}
	u.stepState()

	if _, err := os.Stat(key); err != nil {
		t.Fatalf("uninstall removed the agent key without --purge: %v", err)
	}
}

// --purge removes the state directory, and the node's own project lives inside
// it. That project is hand-written knowledge about a machine — the same class
// as the env file, which setup scaffolds and never overwrites because it does
// not own it. Not owning something on the way in means not deleting it on the
// way out.
//
// This calls stepState, never run(): run() disables real system services, and
// an earlier draft of a test in this file uninstalled the developer's own node.
func TestPurgeKeepsTheNodeProject(t *testing.T) {
	state := t.TempDir()
	label := hostLabel()
	if label == "" {
		t.Skip("no host label on this machine")
	}

	project := filepath.Join(state, label)
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	claudeMD := filepath.Join(project, "CLAUDE.md")
	for _, f := range []string{claudeMD, filepath.Join(state, "hub.db"), filepath.Join(state, "agent_key")} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	u := &uninstall{stateDir: state, purge: true}
	u.stepState()

	if _, err := os.Stat(claudeMD); err != nil {
		t.Errorf("purge destroyed the node's own project: %v", err)
	}
	// ...and it really did purge the rest, or the test would pass by doing
	// nothing at all.
	for _, gone := range []string{filepath.Join(state, "hub.db"), filepath.Join(state, "agent_key")} {
		if _, err := os.Stat(gone); err == nil {
			t.Errorf("%s survived --purge", filepath.Base(gone))
		}
	}
}
