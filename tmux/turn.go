package tmux

import (
	"regexp"
	"strings"
)

// spinnerLine is Claude Code's in-progress status line: a glyph, a verb with an
// ellipsis, then the elapsed time in parentheses, e.g.
//
//	✢ Transfiguring… (4m 26s · ↓ 15.0k tokens · thought for 5s)
//
// A finished turn rewrites it in the past tense with no ellipsis
// ("✻ Churned for 14s · done 4:01 AM"), which is what separates the two.
var spinnerLine = regexp.MustCompile(`^\s*\S\s+\p{L}[\p{L} ]*…\s+\(\d`)

// TurnRunning reports whether the pane shows a turn in progress.
//
// The composer row is drawn and empty while a turn runs, so a composer check
// alone calls a working session idle. Found when `restart --all-idle` listed a
// session ten minutes into a turn with a tool running (wsl core, 2026-10-23);
// only an unrelated parsing bug stopped that restart.
//
// It fails toward RUNNING. A false "running" skips one restart, which can be
// retried; a false "idle" kills work in flight. "esc to interrupt" is the older
// builds' marker for the same state and is accepted too. A heuristic over
// another program's UI, measured on linux: a build that draws its spinner some
// other way reads as not running, which is the expensive direction, so a
// restart path must not rely on this alone where it can avoid it.
func TurnRunning(pane string) bool {
	lines := strings.Split(strings.TrimRight(pane, "\n"), "\n")
	if n := len(lines); n > 20 {
		lines = lines[n-20:]
	}
	for _, l := range lines {
		if spinnerLine.MatchString(l) || strings.Contains(l, "esc to interrupt") {
			return true
		}
	}
	return false
}
