package tmux

import (
	"os"
	"strings"
	"testing"
)

// waitExempt names the functions that submit an Enter WITHOUT waiting for the
// text first, and why. A written reason rather than a boolean, for the reason
// notCaptured is a string in composer_test.go: a flag is an opt-out somebody
// takes by omission, and a sentence cannot be added by accident.
var waitExempt = map[string]string{
	"SendRawKeys": "raw keypresses answering a dialog. There is no text to wait " +
		"for — the keys ARE the message — and a dialog has no composer row to " +
		"read, so a wait here would pay the full timeout on every dialog answer.",
}

// Every function that types text and then submits it must wait for the text to
// appear first, or the two arrive in one read() and the newline is taken as part
// of a paste: the line sits in the composer, typed and never sent.
//
// This exists because fixing it in ONE of the two functions looked exactly like
// fixing the mechanism. SendCommand got the wait; SendText — the path every
// operator send takes — did not, and an A/B through the deployed agent showed 6
// of 6 sends still coalescing afterwards. The change was real, the measurement
// was real, and they were about different functions. A negative that lands on
// the wrong path is indistinguishable from a fix that does not work.
//
// So the property is asserted over the SOURCE rather than over a list somebody
// maintains, for the same reason serve_test.go reads its endpoints out of
// index.html: a hand-kept list agrees with whatever its author last assumed,
// and what was assumed here was that there was one such function.
//
// It matches the quoted literal "Enter" — the code form — not the bare word,
// which appears in the prose explaining why the wait is there. Asserting a
// directive by substring is how a test passes against a file with the directive
// deleted and only its comment left.
func TestEverySubmitPathWaitsForItsText(t *testing.T) {
	src, err := os.ReadFile("tmux.go")
	if err != nil {
		t.Fatalf("reading own source: %v", err)
	}
	funcs := splitTopLevelFuncs(string(src))
	if len(funcs) < 10 {
		t.Fatalf("only parsed %d functions out of tmux.go — the splitter is broken, "+
			"and a guard that inspects nothing passes silently", len(funcs))
	}

	checked := 0
	for name, body := range funcs {
		if !strings.Contains(body, `"Enter"`) {
			continue
		}
		checked++
		if reason, ok := waitExempt[name]; ok {
			if reason == "" {
				t.Errorf("%s is exempt with no reason written", name)
			}
			continue
		}
		if !strings.Contains(body, "awaitComposer(") {
			t.Errorf("%s submits an Enter but never calls awaitComposer: the text and "+
				"the newline can arrive in one read() and the line will not send. "+
				"Add the wait, or add %s to waitExempt with a reason.", name, name)
		}
	}
	if checked == 0 {
		t.Fatal(`no function in tmux.go contains a "Enter" send-keys literal — either ` +
			`the submit paths were renamed or this guard has stopped looking, and the ` +
			`two are indistinguishable from a passing run`)
	}
}

// splitTopLevelFuncs maps each top-level func name to its body text. Brace
// counting is enough here: this file has no raw string literals containing
// braces, and the test above fails loudly if the count comes out implausible.
func splitTopLevelFuncs(src string) map[string]string {
	out := map[string]string{}
	lines := strings.Split(src, "\n")
	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(lines[i], "func ") {
			continue
		}
		name := funcName(lines[i])
		if name == "" {
			continue
		}
		depth, body := 0, []string{}
		for ; i < len(lines); i++ {
			body = append(body, lines[i])
			depth += strings.Count(lines[i], "{") - strings.Count(lines[i], "}")
			if depth == 0 && len(body) > 0 && strings.Contains(strings.Join(body, "\n"), "{") {
				break
			}
		}
		out[name] = strings.Join(body, "\n")
	}
	return out
}

// funcName pulls the identifier out of a func declaration line, skipping a
// receiver if there is one.
func funcName(line string) string {
	rest := strings.TrimPrefix(line, "func ")
	if strings.HasPrefix(rest, "(") {
		if k := strings.Index(rest, ")"); k >= 0 {
			rest = strings.TrimLeft(rest[k+1:], " ")
		}
	}
	if k := strings.IndexAny(rest, "([ "); k > 0 {
		return rest[:k]
	}
	return ""
}
