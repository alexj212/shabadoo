package tmux

import "strings"

// Plain and Typed turn a colour capture (capture-pane -e) back into text.
//
// Typed also DROPS every run drawn faint (SGR 2). Claude Code draws its prompt
// suggestion, the grey "what you might say next", inside the empty input row
// in faint. A plain capture loses the attribute, so the suggestion read as a
// draft: restart refused idle panes as "not empty", and the nudge, which puts a
// draft back after typing `check inbox`, typed the suggestion in as real text.
// Measured on wsl 2026-10-23: the audit logged "restored draft: retry
// adept-service-core-mac" at 04:01:07, and that pane then held the text at
// normal intensity, cursor at its end, while two other panes showed their
// suggestions faint with the cursor at column 2.
//
// So a decision about what somebody TYPED reads Typed; anything else
// (InputState, dialog questions) reads Plain, since a dialog's footer may well
// be faint and must not vanish. Measured on linux and darwin (2026-10-23); a build that
// draws suggestions some other way would still read them as drafts, the busy
// direction, which delays rather than destroys.
func Plain(colour string) string { return sgrText(colour, false) }

// Typed is Plain without the faint runs. See Plain.
func Typed(colour string) string { return sgrText(colour, true) }

func sgrText(s string, dropFaint bool) string {
	var b strings.Builder
	faint := false
	for i := 0; i < len(s); {
		c := s[i]
		if c != 0x1b {
			if c == '\n' || !(dropFaint && faint) {
				b.WriteByte(c)
			}
			i++
			continue
		}
		if i+1 >= len(s) {
			break
		}
		if s[i+1] != '[' {
			i += 2 // a two-byte escape; tmux -e emits none, but never print one
			continue
		}
		j := i + 2
		for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
			j++
		}
		if j >= len(s) {
			break
		}
		if s[j] == 'm' {
			faint = applySGR(s[i+2:j], faint)
		}
		i = j + 1
	}
	return b.String()
}

// applySGR returns the faint state after one SGR sequence's parameters. The
// colour forms are skipped whole, because their arguments are numbers too:
// 38;5;2 is colour 2, not faint.
func applySGR(params string, faint bool) bool {
	ps := strings.Split(params, ";")
	for k := 0; k < len(ps); k++ {
		switch ps[k] {
		case "", "0", "22":
			faint = false
		case "2":
			faint = true
		case "38", "48", "58":
			if k+1 < len(ps) && ps[k+1] == "5" {
				k += 2
			} else if k+1 < len(ps) && ps[k+1] == "2" {
				k += 4
			}
		}
	}
	return faint
}
