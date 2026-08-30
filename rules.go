package main

// `shabadoo rules` — what guidance is actually in effect here, and from where.
//
// The layering exists in practice and is written down nowhere: the payload
// installs `~/.claude/CLAUDE.md`, the machine adds `CLAUDE.local.md` which is
// never vendored, a project adds its own `CLAUDE.md`, and a session's folder may
// add a `MISSION.md`. Four sources, one precedence order, and no way to ask what
// a session is reading.
//
// Built because I needed it twice in one day and answered it by hand both times:
// once diffing a 999-line live file against a 967-line payload to see whether an
// install would clobber it, and once discovering a skill whose vendored copy was
// three and a half months behind the live one. Both are one command now.
//
// It reports STATE, never opinion. A layer that is absent is normal for most
// layers and is said as absent rather than as missing.

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type ruleLayer struct {
	name  string
	path  string
	what  string // what this layer is for, in one line
	owned string // who writes it
}

func runRules(args []string) {
	fset := flag.NewFlagSet("rules", flag.ExitOnError)
	dirs := argsAndFlags(fset, args)
	dir := "."
	if len(dirs) > 0 {
		dir = dirs[0]
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		fatalf("%v", err)
	}
	claude := defaultClaudeDir()
	// projectRoot FALLS BACK to the directory itself when nothing above is
	// marked, which is right for naming a session and wrong here: it made
	// `rules /tmp` announce a project called "tmp". A project is a directory
	// that says it is one, so the marker is checked rather than assumed.
	root := projectRoot(abs)
	if root != "" && !hasFile(filepath.Join(root, "CLAUDE.md")) {
		root = ""
	}

	layers := []ruleLayer{
		{"payload", filepath.Join(claude, "CLAUDE.md"),
			"how to work — portable, installed by setup on every machine", "the shabadoo payload"},
		{"machine", filepath.Join(claude, "CLAUDE.local.md"),
			"this estate: project registry, hosts, toolchains", "you, never vendored"},
	}
	if root != "" {
		layers = append(layers,
			ruleLayer{"project", filepath.Join(root, "CLAUDE.md"),
				"what this project IS — stable for months", "whoever works this repo"})
		if d := missionDirFor(abs, root); d != "" {
			layers = append(layers, ruleLayer{"mission", filepath.Join(d, "MISSION.md"),
				"what it is DOING — changes weekly", "the session that owns it"})
		} else {
			layers = append(layers, ruleLayer{"mission", filepath.Join(abs, "MISSION.md"),
				"what it is DOING — changes weekly", "the session that owns it"})
		}
	}

	fmt.Printf("guidance in effect for %s\n", abs)
	if root != "" {
		fmt.Printf("project: %s  (%s)\n", projectName(root), root)
	} else {
		fmt.Printf("project: — this folder is not inside one, so only the first two layers apply\n")
	}
	fmt.Println()
	fmt.Println("  LAYER     STATE      SIZE    UPDATED       PATH")
	for _, l := range layers {
		state, size, when := "absent", "", ""
		if fi, err := os.Stat(l.path); err == nil {
			state = "present"
			size = fmt.Sprintf("%dL", countLines(l.path))
			when = fi.ModTime().Format("2006-01-02")
		}
		fmt.Printf("  %-9s %-10s %-7s %-13s %s\n", l.name, state, size, when, l.path)
	}
	fmt.Println()
	for _, l := range layers {
		fmt.Printf("  %-9s %s\n", l.name, l.what)
		fmt.Printf("  %-9s written by: %s\n", "", l.owned)
	}

	// The half nothing else answers: is the installed payload what this binary
	// would install? A count stood at 1 on this machine for months while the
	// file behind it was months stale, so the NAMES are what get printed.
	fmt.Println()
	st := scanPayload(claude)
	switch {
	case !st.Known:
		fmt.Println("  payload drift: CANNOT TELL — the payload could not be read or compared")
	case st.Pending == 0:
		fmt.Printf("  payload drift: none — %s matches what %s would install\n", claude, version)
	default:
		fmt.Printf("  payload drift: %d file(s) differ from what %s would install\n", st.Pending, version)
		for _, f := range st.Drift {
			fmt.Printf("      %s\n", f)
		}
		if st.Pending > len(st.Drift) {
			fmt.Printf("      … and %d more\n", st.Pending-len(st.Drift))
		}
		// Which direction matters, and it is not obvious: a file edited after
		// this build is the newer one, and a node restart will now LEAVE it
		// alone rather than reverting it.
		fmt.Println("      a file edited after this build is kept on restart; run `make vendor`")
		fmt.Println("      in the shabadoo repo to fold your edits into the payload")
	}
}

// countLines is a size a person can compare against another file at a glance.
// Bytes are precise and useless here — nobody knows whether 41,203 is a lot.
func countLines(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return strings.Count(string(b), "\n")
}
