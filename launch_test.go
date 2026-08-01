package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The window name is a compatibility contract, not an implementation detail:
// it is how a folder finds its existing window. Change the formula and every
// running session is orphaned, silently, with a duplicate opened beside it.
// Most of these vectors were taken from live windows created by the shell
// launcher this code replaced, which is what makes them a compatibility check
// rather than a restatement of the implementation.
//
// The last two were substituted before this repo was made public — the
// originals named a private machine's user account and a client project. Their
// expected values were computed with `sha1sum`, an independent implementation,
// so they still check this code against something other than itself.
func TestWindowNameMatchesShellLauncher(t *testing.T) {
	c := launchConfig{HostLabel: "wsl"}
	for _, tc := range []struct{ cwd, want string }{
		{"/c/projects/homelab", "homelab-wsl-4b602ded"},
		{"/c/projects/iptv", "iptv-wsl-10cac2b9"},
		{"/c/projects/homelife", "homelife-wsl-1a170f99"},
		{"/c/projects/homelife-mcp", "homelife-mcp-wsl-60740c4b"},
		{"/c/projects/mcp-natsbridge", "mcp-natsbridge-wsl-1ee932cb"},
		{"/home/operator/bin", "bin-wsl-0964a8d7"},
		{"/home/user/src/example-service", "example-service-wsl-7ff56c0d"},
	} {
		if got := c.windowName(tc.cwd); got != tc.want {
			t.Errorf("windowName(%q) = %q, want %q", tc.cwd, got, tc.want)
		}
	}
}

// A folder whose basename needs sanitizing must still round-trip, since the
// hash is over the raw path but the label is over the cleaned basename.
func TestWindowNameSanitizesBasename(t *testing.T) {
	c := launchConfig{HostLabel: "wsl"}
	got := c.windowName("/tmp/we ird.dir")
	if want := "we_ird_dir-wsl-"; got[:len(want)] != want {
		t.Errorf("windowName = %q, want prefix %q", got, want)
	}
}

// An empty pattern is a substring of every window name, so without a guard it
// resolves "uniquely" against a single window and closes it. This bit once.
func TestResolveWindowRejectsEmptyPattern(t *testing.T) {
	c := launchConfig{SessionName: "irrelevant"}
	if _, err := c.resolveWindow(t.Context(), ""); err == nil {
		t.Fatal("empty pattern resolved; want error")
	}
	if _, err := c.resolveWindow(t.Context(), "   "); err == nil {
		t.Fatal("whitespace pattern resolved; want error")
	}
}

// claude.sh `source`d the env file, so a plain `export X=...` in it overwrote
// whatever the process had inherited. Reversing that order would rename every
// window on a host whose service environment disagrees with its env file.
func TestEnvFileBeatsProcessEnv(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "env")
	if err := os.WriteFile(envFile, []byte(
		"# comment\nexport CLAUDE_HOST_LABEL=wsl\nexport CLAUDE_BIN=\"claude\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	t.Setenv("CLAUDE_HOST_LABEL", "should-lose")

	if c := loadLaunchConfig(); c.HostLabel != "wsl" {
		t.Errorf("HostLabel = %q, want %q (env file must win)", c.HostLabel, "wsl")
	}
}

// An empty CLAUDE_RESUME disables resuming, which is different from unset.
// Treating empty as "fall back to the default" would force --continue on a
// host that deliberately turned it off.
func TestEmptyResumeIsHonoured(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "env"),
		[]byte("export CLAUDE_RESUME=\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	if c := loadLaunchConfig(); c.Resume != "" {
		t.Errorf("Resume = %q, want empty (explicitly disabled)", c.Resume)
	}
}

func TestEnvFileMapSkipsCommentsAndBlanks(t *testing.T) {
	dir := t.TempDir()
	body := "\n# a comment\n\nexport A=1\nB='two'\n  export C=\"three\"\nnot an assignment\n"
	if err := os.WriteFile(filepath.Join(dir, "env"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	m := envFileMap(filepath.Join(dir, "env"))
	for k, want := range map[string]string{"A": "1", "B": "two", "C": "three"} {
		if m[k] != want {
			t.Errorf("envFileMap[%q] = %q, want %q", k, m[k], want)
		}
	}
	if _, ok := m["not an assignment"]; ok {
		t.Error("non-assignment line parsed as a key")
	}
}

// The launch command carries the display name and remote-control alias. The
// script this replaced omitted both when opening from the dashboard, so a
// remotely-opened session showed up under a server-generated name.
func TestClaudeCommandCarriesAlias(t *testing.T) {
	c := launchConfig{Bin: "claude", Args: "--dangerously-skip-permissions", HostLabel: "wsl"}
	got := c.claudeCommand("/c/projects/homelab")
	want := "claude --dangerously-skip-permissions -n 'homelab-wsl' --remote-control 'homelab-wsl'"
	if got != want {
		t.Errorf("claudeCommand =\n  %q\nwant\n  %q", got, want)
	}
}
