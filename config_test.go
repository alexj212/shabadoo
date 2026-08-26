package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The env file is full of comments explaining why each knob is set the way it
// is. Regenerating it from a parsed map would delete every one of them, and a
// config tool that eats your notes is one you stop using.
func TestConfigSetPreservesComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "env")
	original := `# Per-host overrides read by the launcher.
# Anything set here wins over the process environment.

# Keep it short — this shows in tmux window names.
export CLAUDE_HOST_LABEL=wsl

# Uncomment to customize:
# export CLAUDE_ARGS="--dangerously-skip-permissions"
export CLAUDE_BIN=/usr/local/bin/claude
`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := setEnvFileValue(path, "CLAUDE_HOST_LABEL", "laptop"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	s := string(got)

	for _, comment := range []string{
		"# Per-host overrides read by the launcher.",
		"# Keep it short — this shows in tmux window names.",
		"# Uncomment to customize:",
		`# export CLAUDE_ARGS="--dangerously-skip-permissions"`,
	} {
		if !strings.Contains(s, comment) {
			t.Errorf("lost a comment: %q\n---\n%s", comment, s)
		}
	}
	// A COMMENTED assignment is documentation of an option. Replacing it would
	// delete the hint, so it must survive even though it names the same key.
	if strings.Count(s, "CLAUDE_ARGS") != 1 {
		t.Errorf("the commented CLAUDE_ARGS hint was touched:\n%s", s)
	}
	if !strings.Contains(s, "CLAUDE_HOST_LABEL='laptop'") {
		t.Errorf("value not set:\n%s", s)
	}
	if strings.Contains(s, "CLAUDE_HOST_LABEL=wsl") {
		t.Errorf("old value still present:\n%s", s)
	}
	// An untouched key stays exactly as written.
	if !strings.Contains(s, "export CLAUDE_BIN=/usr/local/bin/claude") {
		t.Errorf("an unrelated assignment was rewritten:\n%s", s)
	}
}

// Two assignments of one key would make the file disagree with itself — the
// last wins when sourced, so the earlier one is a trap for whoever reads it.
func TestConfigSetCollapsesDuplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "env")
	os.WriteFile(path, []byte("export CLAUDE_BIN=a\nexport CLAUDE_BIN=b\n"), 0o644)

	if err := setEnvFileValue(path, "CLAUDE_BIN", "c"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if n := strings.Count(string(got), "CLAUDE_BIN"); n != 1 {
		t.Errorf("%d assignments left, want 1:\n%s", n, got)
	}
}

// A value with a space in it has to survive being sourced by a shell.
func TestConfigSetQuotesValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "env")
	if err := setEnvFileValue(path, "CLAUDE_ARGS", "--a --b 'c'"); err != nil {
		t.Fatal(err)
	}
	if got := envFileMap(path)["CLAUDE_ARGS"]; got != "--a --b 'c'" {
		t.Errorf("round trip = %q", got)
	}
}

// Empty is a meaningful value for CLAUDE_RESUME — it disables resuming — so it
// must be distinguishable from unset.
func TestConfigEmptyIsNotUnset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "env")
	setEnvFileValue(path, "CLAUDE_RESUME", "")

	m := envFileMap(path)
	if v, ok := m["CLAUDE_RESUME"]; !ok || v != "" {
		t.Errorf("empty value did not survive: %q present=%v", v, ok)
	}
	removed, err := removeEnvFileKey(path, "CLAUDE_RESUME")
	if err != nil || !removed {
		t.Fatalf("unset failed: removed=%v err=%v", removed, err)
	}
	if _, ok := envFileMap(path)["CLAUDE_RESUME"]; ok {
		t.Error("key still present after unset")
	}
}

// The boot list is matched through symlinks, because the list may hold a link
// while tmux reports the resolved path — the same reason /api/folders does it.
func TestBootListAddRemove(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "folders")
	real := filepath.Join(dir, "project")
	link := filepath.Join(dir, "link-to-project")
	os.MkdirAll(real, 0o755)
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks unavailable")
	}

	if err := appendBootList(path, real); err != nil {
		t.Fatal(err)
	}
	folders, _ := readBootList(path)
	if len(folders) != 1 || folders[0] != real {
		t.Fatalf("folders = %v", folders)
	}

	// Removing by the symlink must find the entry stored by its real path.
	removed, err := removeFromBootList(path, link)
	if err != nil {
		t.Fatal(err)
	}
	if removed != real {
		t.Errorf("removed %q, want %q — symlink did not resolve", removed, real)
	}
	if folders, _ := readBootList(path); len(folders) != 0 {
		t.Errorf("still listed: %v", folders)
	}

	// Removing something absent reports nothing rather than erroring.
	if removed, err := removeFromBootList(path, real); err != nil || removed != "" {
		t.Errorf("removing an absent entry: %q %v", removed, err)
	}
}

// Comments and blanks are not folders, and a missing file is an empty list
// rather than an error — nothing has been configured yet.
func TestReadBootListIgnoresCommentsAndMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "folders")

	if folders, err := readBootList(path); err != nil || folders != nil {
		t.Errorf("missing file: %v %v", folders, err)
	}
	os.WriteFile(path, []byte("# a header\n\n/one\n/two   # trailing note\n\n"), 0o644)
	folders, err := readBootList(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(folders, ",") != "/one,/two" {
		t.Errorf("folders = %v", folders)
	}
}

// The reader used to trim quote CHARACTERS from both ends, which is not the
// same as unquoting: `--foo 'bar'` came back as `--foo 'bar`, silently. The
// bug predates the config command and would have mangled a hand-written file
// too; it surfaced because a value written by `config set` could not be read
// back.
func TestUnquoteEnvValue(t *testing.T) {
	for in, want := range map[string]string{
		`plain`:              `plain`,
		`'quoted'`:           `quoted`,
		`"quoted"`:           `quoted`,
		`''`:                 ``,
		`""`:                 ``,
		`'--a --b'`:          `--a --b`,
		`'it'\''s'`:          `it's`,
		`"say \"hi\""`:       `say "hi"`,
		`--unquoted --flags`: `--unquoted --flags`,
		// A lone quote is not a delimiter pair and must be left alone rather
		// than half-eaten.
		`don't`: `don't`,
	} {
		if got := unquoteEnvValue(in); got != want {
			t.Errorf("unquoteEnvValue(%s) = %q, want %q", in, got, want)
		}
	}
}

// A bare invocation must not start a server.
//
// It used to, so that `ExecStart=... --addr ...` kept working when subcommands
// were introduced. Every unit this installs now names hub, node or boot
// explicitly, so that compatibility protected nothing — while a downloaded
// binary run once to see what it is silently bound port 8787 and served a
// dashboard.
//
// Exercised as a subprocess because the alternative is a test that starts a
// listener and hopes.
func TestBareInvocationPrintsUsage(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "shabadoo")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, out)
	}

	for _, tc := range []struct {
		args     []string
		wantExit int
		wantText string
	}{
		{nil, 0, "usage:"},                                      // asking is not a failure
		{[]string{"--help"}, 0, "usage:"},                       //
		{[]string{"--addr", "127.0.0.1:1"}, 2, "not a command"}, // a flag is not a command
		{[]string{"nonsense"}, 2, "unknown command"},
	} {
		cmd := exec.Command(bin, tc.args...)
		out, _ := cmd.CombinedOutput()
		code := cmd.ProcessState.ExitCode()

		if code != tc.wantExit {
			t.Errorf("%v exited %d, want %d\n%s", tc.args, code, tc.wantExit, out)
		}
		if !strings.Contains(string(out), tc.wantText) {
			t.Errorf("%v output missing %q:\n%s", tc.args, tc.wantText, out)
		}
		// The specific regression: none of these may bind a port.
		if strings.Contains(string(out), "listening on") {
			t.Errorf("%v started a server:\n%s", tc.args, out)
		}
	}
}

// A secret on the command line is world-readable: /proc/<pid>/cmdline is mode
// 444, so any user on the host reads it with ps. Found on a live deployment
// where the key was passed as a compose argument.
func TestKeyOnCommandLineDetection(t *testing.T) {
	base := []string{"shabadoo", "hub", "--addr=0.0.0.0:8787"}

	if keyOnCommandLine(base) {
		t.Error("warned with no key flag at all")
	}
	if !keyOnCommandLine(append(base, "--elevenlabs-key=sk_real_value")) {
		t.Error("a key in argv was not detected")
	}
	if !keyOnCommandLine(append(base, "--elevenlabs-key", "sk_real_value")) {
		t.Error("a space-separated key was not detected")
	}
	// Compose expands an unset variable to an empty flag. Warning on that would
	// be noise on every deployment that does not use voice.
	if keyOnCommandLine(append(base, "--elevenlabs-key=")) {
		t.Error("an empty flag was reported as an exposed key")
	}
	if keyOnCommandLine(append(base, "--elevenlabs-key", "")) {
		t.Error("an empty space-separated flag was reported as exposed")
	}
}
