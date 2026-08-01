package tmux

import "testing"

// The question a modal is asking is the last hop in the loop this project
// sells: the dashboard could say a session was blocked and never what it was
// blocked ON. Extracting it wrong is worse than not extracting it — somebody
// would answer a question they had not read — so unrecognised must stay "".
func TestDialogPrompt(t *testing.T) {
	permission := `
● I'll create the file now.

╭──────────────────────────────────────────────────╮
│ Do you want to create foo.go?                    │
│                                                  │
│ ❯ 1. Yes                                         │
│   2. Yes, and don't ask again this session       │
│   3. No, and tell Claude what to do differently  │
╰──────────────────────────────────────────────────╯
  Esc to cancel
`
	if got := DialogPrompt(permission); got != "Do you want to create foo.go?" {
		t.Errorf("permission prompt = %q", got)
	}

	// A pane can hold an ANSWERED prompt above the live one. The live one is
	// nearer the bottom, so the search runs backwards.
	stacked := permission + `
╭──────────────────────────────────────────────────╮
│ Do you want to make this edit to server.go?      │
╰──────────────────────────────────────────────────╯
  Esc to cancel
`
	if got := DialogPrompt(stacked); got != "Do you want to make this edit to server.go?" {
		t.Errorf("stacked prompts picked the wrong one: %q", got)
	}

	// Nothing that is not a modal question. A trailing "?" alone must not
	// qualify: "Done. Anything else?" is something the assistant SAYS, and
	// extracting it would put a question nobody is being asked onto a dashboard
	// and into somebody's notification.
	for _, pane := range []string{
		"● Done. Anything else?\n\n> \n",
		"● Should I continue?\n",
		"",
		"$ ls\nfoo.go  bar.go\n",
		"  Esc to cancel\n",
	} {
		if got := DialogPrompt(pane); got != "" {
			t.Errorf("DialogPrompt(%q) = %q, want empty", pane, got)
		}
	}

	// Long questions are truncated rather than shipped whole: this rides in
	// every agent report and onto a lock screen.
	long := "│ Do you want to " + string(make([]byte, 0)) + repeat("run this very long command ", 12) + "?│"
	got := DialogPrompt(long)
	if len(got) > dialogPromptMax+4 {
		t.Errorf("prompt not truncated: %d chars", len(got))
	}
}

func repeat(s string, n int) string {
	out := ""
	for range n {
		out += s
	}
	return out
}

// The box-drawing frame must not survive into a notification.
func TestStripBox(t *testing.T) {
	for in, want := range map[string]string{
		"│ Do you want to proceed? │": "Do you want to proceed?",
		"╭──────────╮":                "",
		"❯ 1. Yes":                    "1. Yes",
		"  plain text  ":              "plain text",
	} {
		if got := stripBox(in); got != want {
			t.Errorf("stripBox(%q) = %q, want %q", in, got, want)
		}
	}
}
