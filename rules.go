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
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"shabadoo/claudelog"
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

	upstreamDrift()
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

// upstreamDrift reports payload skills that another REPOSITORY owns.
//
// `minutes` is the live case and so far the only one: the skill documenting a
// tool lives in that tool's own repo, pinned there by a test against the tool's
// command dispatch — a test this repo structurally cannot run. So the tool's
// repo is the source and what the payload carries is a vendored copy.
//
// A vendored copy is hand-editable, which is exactly how it went wrong: a rule
// about transcripts containing credentials, written after a root password
// nearly reached a work remote, was edited into the SNAPSHOT and shipped. The
// file the tool's own CLAUDE.md declares the interface of record never got it.
// Both copies were being edited as though each were authoritative, by different
// sessions, and nothing anywhere compared them. Found by a peer on another host
// answering a different question.
//
// Reported, never enforced. A gate in `make vendor` would have blocked the very
// state that produced the finding — the payload legitimately ahead while the fix
// was routed to the repo — and this project has already paid once for a guard
// that blocks the fix it exists to prompt.
func upstreamDrift() {
	payload, err := mergePayloads()
	if err != nil {
		fmt.Println("  upstream skills: CANNOT TELL — the payload could not be read")
		return
	}

	// Where this machine's projects are. The boot list is the only local answer:
	// `rules` runs from an arbitrary directory and the coordinator is not
	// necessarily reachable.
	roots := map[string]string{}
	note := func(dir string) {
		if dir == "" {
			return
		}
		if r, err := filepath.EvalSymlinks(dir); err == nil {
			dir = r
		}
		if _, ok := roots[filepath.Base(dir)]; !ok {
			roots[filepath.Base(dir)] = dir
		}
	}
	// The boot list alone is NOT enough and that is measured, not assumed: the
	// first version used it and reported "nothing checkable" in all three arms
	// of its own teeth-check, including the arm that had been deliberately
	// broken. `minutes` is a live project on this host and simply is not a boot
	// folder — the list holds what starts at boot, not what exists.
	for _, dir := range configuredFolders() {
		note(dir)
	}
	// Every folder that has ever run a Claude session. Same source /api/folders
	// merges, and the one that actually contains the projects a session works in.
	if projects, err := claudelog.Projects(); err == nil {
		for _, p := range projects {
			note(p.Path)
		}
	}

	same, differ := compareUpstream(payload, roots)

	// Nothing to compare is not the same as nothing wrong, and the difference is
	// invisible from the output unless it is stated. A stranger's machine has no
	// sibling repos at all and must not read as verified.
	if len(same)+len(differ) == 0 {
		fmt.Println("  upstream skills: none checkable here — no project on this host")
		fmt.Println("      owns a skill this payload ships, so nothing was compared")
		return
	}
	if len(differ) == 0 {
		fmt.Printf("  upstream skills: %d checked, all match (%s)\n",
			len(same), strings.Join(same, ", "))
		return
	}
	fmt.Printf("  upstream skills: %d of %d DIFFER from the repo that owns them\n",
		len(differ), len(same)+len(differ))
	for _, d := range differ {
		fmt.Printf("      %s\n", d)
	}
	fmt.Println("      that repo is the source; the payload carries a vendored copy.")
	fmt.Println("      edit it THERE, then vendor — a hand edit here ships a rule the")
	fmt.Println("      file its own project calls authoritative does not contain")
}

// compareUpstream is split out so the DISTINCTION can be tested: a comparator
// that has gone blind and reports everything as matching passes any single-sided
// fixture. The pair is what catches it.
func compareUpstream(payload map[string][]byte, roots map[string]string) (same, differ []string) {
	for rel := range payload {
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) != 3 || parts[0] != "skills" || parts[2] != "SKILL.md" {
			continue
		}
		name := parts[1]
		root, ok := roots[name]
		if !ok {
			continue // no project of that name here; not evidence of anything
		}
		up := filepath.Join(root, "skills", name, "SKILL.md")
		got, err := os.ReadFile(up)
		if err != nil {
			continue // the directory exists but ships no such skill
		}
		if bytes.Equal(got, payload[rel]) {
			same = append(same, name)
		} else {
			differ = append(differ, name+" ("+up+")")
		}
	}
	sort.Strings(same)
	sort.Strings(differ)
	return same, differ
}
