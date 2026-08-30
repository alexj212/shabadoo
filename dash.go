package main

// `shaba dash` — open the dashboard in this machine's browser.
//
// The coordinator address is already known: it is in ~/.config/shabadoo/coord
// after the first `pair`, and every listing now prints it. What was missing is
// the step between reading it and looking at it, which on WSL is not obvious —
// the browser is on the Windows side and `xdg-open` there does the wrong thing.

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

func runDash(args []string) {
	fset := flag.NewFlagSet("dash", flag.ExitOnError)
	coord := fset.String("coord", "", "coordinator base URL")
	print := fset.Bool("print", false, "print the URL and open nothing")
	fset.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: shaba dash [--print] [path]

Open the dashboard in this machine's browser. With a path or session name, opens
it focused on that pane.

flags:
`)
		fset.PrintDefaults()
	}
	rest := argsAndFlags(fset, args)

	c, err := newClient(*coord)
	if err != nil {
		fatalf("%v", err)
	}
	url := strings.TrimRight(c.coord, "/")
	if len(rest) > 0 && rest[0] != "" {
		// A fragment, not a query: it never reaches the server, so a session
		// name does not land in an access log. The page reads it on load.
		url += "#pane=" + urlFragmentEscape(rest[0])
	}

	// Printed FIRST and always. An opener that silently fails — no display, a
	// headless box, an ssh session — would otherwise leave nothing to copy, and
	// the URL is the whole point of the command.
	fmt.Println(url)
	if *print {
		return
	}
	if err := openBrowser(url); err != nil {
		// Not fatal. The address is already on screen, and refusing to exit 0
		// because a helper is missing would make this useless over ssh.
		fmt.Fprintf(os.Stderr, "could not open a browser (%v) — the URL is above\n", err)
	}
}

// openBrowser hands a URL to whatever this machine uses.
//
// WSL is the case worth naming, and it is why this is not one line: the browser
// lives on the Windows side, and `xdg-open` is present but wrong there — it
// resolves to a Linux handler that either does nothing or starts a text browser
// in the terminal. `wslview` (from wslu) is the correct front door and is what
// Windows' own default-browser registration is reachable through; the
// `explorer.exe` fallback exists for a WSL install without wslu.
//
// Tried in order, first success wins, and the failure is reported rather than
// swallowed — a command whose whole job is to open something must not exit 0
// having opened nothing.
func openBrowser(url string) error {
	var candidates [][]string
	switch {
	case runtime.GOOS == "darwin":
		candidates = [][]string{{"open", url}}
	case runtime.GOOS == "windows":
		candidates = [][]string{{"rundll32", "url.dll,FileProtocolHandler", url}}
	case isWSL():
		candidates = [][]string{
			{"wslview", url},
			{"explorer.exe", url},
			{"powershell.exe", "-NoProfile", "Start-Process", url},
			{"xdg-open", url},
		}
	default:
		candidates = [][]string{{"xdg-open", url}, {"sensible-browser", url}}
	}

	var tried []string
	for _, c := range candidates {
		if _, err := exec.LookPath(c[0]); err != nil {
			continue
		}
		tried = append(tried, c[0])
		cmd := exec.Command(c[0], c[1:]...)
		// A browser launcher outlives this process and its diagnostics are not
		// ours; letting it inherit stdout would print Windows chatter into a
		// terminal that just asked for a URL.
		cmd.Stdout, cmd.Stderr = nil, nil
		if err := cmd.Start(); err == nil {
			go cmd.Wait() // reap it; nothing here waits for a browser to close
			return nil
		}
	}
	if len(tried) == 0 {
		return fmt.Errorf("no opener found (looked for %s)", strings.Join(openerNames(candidates), ", "))
	}
	return fmt.Errorf("tried %s", strings.Join(tried, ", "))
}

func openerNames(c [][]string) []string {
	out := make([]string, 0, len(c))
	for _, x := range c {
		out = append(out, x[0])
	}
	return out
}

// isWSL reads the kernel version string, which carries "microsoft" on both WSL1
// and WSL2. Checked rather than assumed from GOOS: WSL reports linux, and the
// browser question is the one place that distinction changes the answer.
func isWSL() bool {
	b, err := os.ReadFile("/proc/version")
	return err == nil && strings.Contains(strings.ToLower(string(b)), "microsoft")
}

// urlFragmentEscape keeps a session name usable in a fragment without dragging
// in net/url for one call that must not escape the characters names actually
// contain (`-`, `/`).
func urlFragmentEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.', r == '/':
			b.WriteRune(r)
		default:
			b.WriteString(fmt.Sprintf("%%%02X", r))
		}
	}
	return b.String()
}
