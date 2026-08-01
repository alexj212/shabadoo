package tmux

// These pin the failure that made a Mac node show up on the dashboard as
// "online, zero sessions" for half an hour.
//
// tmux rewrites every non-printable byte in -F output to "_" when it is not in
// UTF-8 mode. The separator used to be 0x1F, which is non-printable, so under a
// service manager — which supplies no locale, unlike any interactive shell —
// every field collapsed into one, every row was skipped, and the agent reported
// an empty window list. Nothing errored anywhere: the agent logged in, held its
// stream, and answered commands, while appearing to own nothing.
//
// The separator is printable now, so the mangling cannot happen. The last test
// proves that against a real tmux with the locale stripped, rather than trusting
// the reasoning.

import (
	"os/exec"
	"strings"
	"testing"
)

func windowLine(fields ...string) string { return strings.Join(fields, FieldSep) }

// A well-formed line, in the order Windows() asks for: path LAST.
var goodWindow = windowLine("claude", "0", "proj-mac-85fb061a", "1", "1",
	"1785453214", "claude", "73362", "/Users/me/projects/proj")

func TestParseWindowsGood(t *testing.T) {
	got, err := parseWindows(goodWindow + "\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d windows, want 1", len(got))
	}
	w := got[0]
	if w.Session != "claude" || w.Index != 0 || w.Name != "proj-mac-85fb061a" {
		t.Errorf("bad parse: %+v", w)
	}
	if w.Path != "/Users/me/projects/proj" || w.PID != 73362 {
		t.Errorf("bad tail fields: path=%q pid=%d", w.Path, w.PID)
	}
	if !w.Active || w.Panes != 1 {
		t.Errorf("active/panes wrong: %+v", w)
	}
}

// Output that was produced but cannot be split must be an ERROR, never an empty
// list. An empty list is indistinguishable from a host with nothing running,
// which is precisely why the original bug went unnoticed.
func TestParseWindowsUnsplittableIsAnError(t *testing.T) {
	got, err := parseWindows("claude_0_proj_1_1_17854_claude_73362_/tmp\n")
	if err == nil {
		t.Fatalf("unsplittable output parsed without error, returning %d window(s) — "+
			"this is the silent-empty-list bug", len(got))
	}
	for _, want := range []string{"UTF-8", "LANG"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q so the fix is actionable; got: %v", want, err)
		}
	}
}

// Genuinely empty output is not an error: a host with no tmux windows is normal
// and must not be reported as a broken parse.
func TestParseWindowsEmptyIsNotAnError(t *testing.T) {
	for _, in := range []string{"", "\n", "  \n"} {
		got, err := parseWindows(in)
		if err != nil {
			t.Errorf("empty output %q should not error, got: %v", in, err)
		}
		if len(got) != 0 {
			t.Errorf("empty output %q gave %d windows", in, len(got))
		}
	}
}

// One bad row among good ones must not fail the batch — partial output beats
// none, and the loud error is reserved for "nothing parsed at all".
func TestParseWindowsPartialSurvives(t *testing.T) {
	got, err := parseWindows(goodWindow + "\nnot-a-row\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d windows, want the 1 parseable one", len(got))
	}
}

// The path is last and parsing uses SplitN, so even a directory containing the
// separator cannot shift the other columns. It corrupts only itself.
func TestParseWindowsSeparatorInsidePathDoesNotShiftColumns(t *testing.T) {
	nasty := "/tmp/weird" + FieldSep + "dir"
	line := windowLine("claude", "3", "proj-wsl-abc", "0", "2",
		"1785453214", "claude", "999", nasty)

	got, err := parseWindows(line + "\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d windows, want 1", len(got))
	}
	w := got[0]
	// Everything before the path must be intact — that is the property SplitN
	// and the field ordering exist for.
	if w.Session != "claude" || w.Index != 3 || w.Name != "proj-wsl-abc" || w.PID != 999 {
		t.Errorf("a separator inside the path shifted earlier columns: %+v", w)
	}
	if w.Path != nasty {
		t.Errorf("path = %q, want the whole remainder %q", w.Path, nasty)
	}
}

// The point of the whole change: the separator must survive tmux running
// WITHOUT a locale, which is what a service manager gives it. Run against a
// real tmux on a PRIVATE socket (-L) so it cannot touch the developer's own
// session — a test that resolves against the live server is a test that kills
// real windows.
func TestSeparatorSurvivesTmuxWithoutLocale(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	const sock = "shabadoo-parse-test"

	if out, err := exec.Command("tmux", "-L", sock, "new-session", "-d",
		"-s", "probe", "-n", "probe-window", "sh -c 'sleep 30'").CombinedOutput(); err != nil {
		t.Skipf("could not start a private tmux server: %v: %s", err, out)
	}
	t.Cleanup(func() { exec.Command("tmux", "-L", sock, "kill-server").Run() })

	format := strings.Join([]string{
		"#{session_name}", "#{window_index}", "#{window_name}", "#{window_active}",
		"#{window_panes}", "#{window_activity}", "#{pane_current_command}",
		"#{pane_pid}", "#{pane_current_path}",
	}, FieldSep)

	// env -i equivalent: no LANG, no LC_*, exactly what launchd/systemd supply.
	cmd := exec.Command("tmux", "-L", sock, "list-windows", "-a", "-F", format)
	cmd.Env = []string{"PATH=" + pathEnv()}

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("list-windows: %v", err)
	}
	if !strings.Contains(string(out), FieldSep) {
		t.Fatalf("tmux rewrote the separator with no locale set — output: %q", out)
	}

	windows, err := parseWindows(string(out))
	if err != nil {
		t.Fatalf("parse failed with no locale set: %v", err)
	}
	if len(windows) == 0 {
		t.Fatal("parsed zero windows from a server that has one — the silent-empty-list bug")
	}
	if windows[0].Name != "probe-window" {
		t.Errorf("window name = %q, want probe-window", windows[0].Name)
	}
}

func pathEnv() string {
	for _, kv := range []string{"/usr/local/bin:/usr/bin:/bin:/opt/homebrew/bin"} {
		return kv
	}
	return "/usr/bin:/bin"
}
