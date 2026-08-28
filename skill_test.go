package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// config/skills/claude-sessions/SKILL.md is what a session reads before driving
// another one, and it drifted for months with nothing to catch it — measured:
// invoked twice in fourteen days while the work it describes happened about 230
// times, and its trigger vocabulary did not contain the word people actually
// use. A peer's project pins its own skill against its CLI and that test caught
// two flags that had silently gone; this is the same idea pointed here.
//
// The command list is read out of main()'s dispatch rather than written beside
// it, because a hand-kept copy agrees with whatever its author last assumed.
const sessionSkill = "config/skills/claude-sessions/SKILL.md"

// operatorOnly are commands this skill deliberately does not document.
//
// It is about DRIVING other sessions. Running a coordinator, publishing builds,
// enrolling devices and installing the toolchain are a different job with a
// different reader, and documenting them here would make the skill longer for
// everyone in order to serve nobody who loaded it. Each is listed rather than
// pattern-matched so adding a command forces a decision about which it is.
var operatorOnly = map[string]string{
	"serve":      "runs the standalone fallback server",
	"node":       "runs the agent; a unit does this, not a session",
	"hub":        "runs the coordinator",
	"pair":       "enrols a human client",
	"renew":      "extends this client's own credential",
	"mcp":        "is what a session's MCP child runs, not something it calls",
	"inbox":      "is a shell hook's drain, not a session-facing verb",
	"devices":    "lists enrolled human clients",
	"publish":    "uploads release binaries",
	"upgrade":    "replaces a node's binary",
	"releases":   "lists what is published",
	"disconnect": "cuts a node's session",
	"revoke":     "signs out a human client",
	"attach":     "is how a person opens their own terminal",
	"boot":       "manages the autostart list",
	"config":     "edits the launcher's env file",
	"setup":      "installs the toolchain",
	"doctor":     "reports what setup would change",
	"uninstall":  "removes it",
	"version":    "prints the build",
	"help":       "is help",
	"audit":      "is an operator's record of who drove which pane",
	"mail":       "reads the traffic; documented in the skill by name already",
}

func dispatchedCommands(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("reading main.go: %v", err)
	}
	body := string(src)
	start := strings.Index(body, "switch")
	if start < 0 {
		t.Fatal("main.go no longer has a subcommand switch this test can read")
	}
	var out []string
	for _, m := range regexp.MustCompile(`(?m)^\tcase "([a-z-]+)"`).FindAllStringSubmatch(body[start:], -1) {
		out = append(out, m[1])
	}
	if len(out) < 10 {
		t.Fatalf("found only %d commands; the parse is wrong, not the skill", len(out))
	}
	return out
}

// Every session-facing command must appear in the skill. `who` was added to the
// CLI and not to the skill within the same hour this test was written, which is
// the drift in miniature.
func TestSessionSkillDocumentsEverySessionFacingCommand(t *testing.T) {
	raw, err := os.ReadFile(sessionSkill)
	if err != nil {
		t.Fatalf("reading %s: %v", sessionSkill, err)
	}
	text := string(raw)

	for _, cmd := range dispatchedCommands(t) {
		if _, skip := operatorOnly[cmd]; skip {
			continue
		}
		if !strings.Contains(text, "shabadoo "+cmd) && !strings.Contains(text, "`"+cmd+"`") {
			t.Errorf("`shabadoo %s` is dispatched and session-facing but absent from %s —\n"+
				"document it, or add it to operatorOnly with the reason it is not for sessions",
				cmd, sessionSkill)
		}
	}
}

// The other direction: a skill naming a command that no longer exists sends a
// session to run something that exits 2.
func TestSessionSkillNamesNoCommandThatIsGone(t *testing.T) {
	raw, err := os.ReadFile(sessionSkill)
	if err != nil {
		t.Fatalf("reading %s: %v", sessionSkill, err)
	}
	known := map[string]bool{}
	for _, c := range dispatchedCommands(t) {
		known[c] = true
	}
	// `win` has its own subcommands, and MCP tools are not CLI commands.
	for _, extra := range []string{"win", "list", "close", "reopen", "clear", "open", "start"} {
		known[extra] = true
	}
	for _, m := range regexp.MustCompile("`?shabadoo ([a-z-]+)").FindAllStringSubmatch(string(raw), -1) {
		name := m[1]
		if known[name] {
			continue
		}
		switch name { // prose, not a command claim
		case "reaches", "win", "is", "and", "mcp", "attach":
			continue
		}
		t.Errorf("%s names `shabadoo %s`, which this binary does not dispatch", sessionSkill, name)
	}
}
