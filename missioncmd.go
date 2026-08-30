package main

// `shabadoo mission` — read this project's MISSION.md, or scaffold one.
//
// The file is read by the agent and rendered on the dashboard, which means the
// two questions a session actually has about it — "does mine parse?" and "how
// do I start one?" — were answerable only by looking at a web page on another
// host, or by not knowing. Both are answered here, in the folder that owns the
// file.

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func runMission(args []string) {
	// Only a KNOWN word is a subcommand. Treating any bare first argument as
	// one turns `shabadoo mission /c/projects/iptv` — the obvious way to ask
	// about another folder — into a usage error naming a subcommand the caller
	// never typed. Found by running it.
	sub := ""
	if len(args) > 0 && (args[0] == "show" || args[0] == "init") {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "", "show":
		missionShow(args)
	case "init":
		missionInit(args)
	default:
		fatalf("usage: shabadoo mission [show|init] [dir]")
	}
}

// missionRoot resolves the project that owns a directory.
//
// The same rule the agent reports by (`projectRoot`), so what this prints is
// what the fleet sees. Deriving it a second way here is how a session ends up
// editing a file nothing reads.
func missionRoot(args []string, what string) string {
	dir := "."
	if len(args) > 0 {
		dir = args[0]
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		fatalf("%v", err)
	}
	root := projectRoot(abs)
	if root == "" {
		fatalf("%s is not inside a project (no CLAUDE.md at a git root above it), so there is\n"+
			"nowhere to %s a MISSION.md that the fleet would read", abs, what)
	}
	return root
}

func missionShow(args []string) {
	fset := flag.NewFlagSet("mission show", flag.ExitOnError)
	dirs := argsAndFlags(fset, args)
	root := missionRoot(dirs, "read")
	path := filepath.Join(root, "MISSION.md")

	m := readMission(root)
	if m == nil {
		// Two different absences, and they must not print alike: a file that is
		// not there, and one that is there and says nothing the parser can use.
		// The second looks finished to whoever wrote it, which is what makes it
		// worth naming.
		if !hasFile(path) {
			fmt.Printf("%s has no MISSION.md\n", projectName(root))
			fmt.Printf("  write one with: shabadoo mission init\n")
			return
		}
		fmt.Printf("%s: %s exists but nothing in it parsed\n", projectName(root), path)
		fmt.Printf("  a headline (`# ...`) or `status:` is the minimum the fleet can read\n")
		os.Exit(1)
	}

	fmt.Printf("%s  (%s)\n", projectName(root), path)
	if m.Headline != "" {
		fmt.Printf("  %s\n", m.Headline)
	}
	line := []string{}
	if m.Status != "" {
		line = append(line, "status: "+m.Status)
	}
	if m.Updated != "" {
		line = append(line, "updated: "+m.Updated)
	}
	if m.Owner != "" {
		line = append(line, "owner: "+m.Owner)
	}
	if len(line) > 0 {
		fmt.Printf("  %s\n", strings.Join(line, "   "))
	}
	if m.Now != "" {
		fmt.Printf("\nNow\n  %s\n", m.Now)
	}
	if len(m.Waiting) > 0 {
		fmt.Printf("\nWaiting on\n")
		for _, w := range m.Waiting {
			owner := w.Owner
			if owner == "" {
				owner = "(nobody named)"
			}
			cut := ""
			if w.Truncated {
				cut = "  …cut to fit"
			}
			fmt.Printf("  %-10s %s%s\n", owner, w.Item, cut)
		}
	}
	// A row the cap discarded is a blocker that exists and is not on the
	// dashboard. The author watched the file parse and cannot see what is
	// missing; this is the only place they can be told.
	if m.Dropped > 0 {
		fmt.Printf("\n  %d waiting row(s) over the six-row cap are NOT reported.\n", m.Dropped)
		fmt.Printf("  Merge or resolve some — a wrap-up that long means the items are too small.\n")
	}
	if n := len(m.Log); n > 0 {
		fmt.Printf("\nLog: %d entr%s, newest %s\n", n, map[bool]string{true: "y", false: "ies"}[n == 1], m.Log[0].Date)
	}
}

// missionInit scaffolds a MISSION.md, and refuses to touch one that exists.
//
// The scaffold states NOTHING on the project's behalf — no `status: active`, no
// invented `Now`. A generated claim is indistinguishable on the dashboard from
// one somebody made, and a fleet where half the statuses were written by a
// template is worse than one where half the projects are visibly silent. What
// it removes is the friction, not the thinking: the shape, the section names,
// and the rules that are easy to get wrong.
func missionInit(args []string) {
	fset := flag.NewFlagSet("mission init", flag.ExitOnError)
	force := fset.Bool("force", false, "overwrite an existing MISSION.md (it is backed up first)")
	dirs := argsAndFlags(fset, args)
	root := missionRoot(dirs, "write")
	path := filepath.Join(root, "MISSION.md")

	if hasFile(path) && !*force {
		fmt.Printf("%s already exists — left alone.\n", path)
		fmt.Printf("  read it with: shabadoo mission\n")
		return
	}

	name := projectName(root)
	headline := projectDescription(root)
	if headline == "" {
		headline = "What " + name + " is for — one line."
	}
	body := fmt.Sprintf(`# %s
status: active
updated: %s

## Now
<!-- One or two lines, present tense: what is being worked on right now.
     Rewrite this as the work moves; it is read by peers deciding whether to
     interrupt you. -->

## Waiting on
<!-- One line per item, and EVERY line names an owner:
       - you: <item> · <risk if not done> · <cost to do it>
       - <session>: <item> · ...
       - nobody: <item that needs no one>
     The owner is the whole value — it is what lets the dashboard group by who
     is blocked, so a person reads their own rows and stops. An item with no
     owner is a complaint nobody can answer.
     Six rows maximum: the seventh is counted and NOT shown. -->

## Log
<!-- Append-only, newest first, one line per thing that mattered:
       - %s <what changed, broke, or was decided>
     Not a commit log — git has that. Write entries as things land; a log
     reconstructed at the end is a summary, and it omits what you stopped
     noticing. -->
`, headline, time.Now().Format("2006-01-02"), time.Now().Format("2006-01-02"))

	// Backed up before replacing, the same contract every other write in this
	// binary keeps. --force is the only way here, and a MISSION.md is somebody's
	// hand-written record of what a project is doing.
	if old, err := os.ReadFile(path); err == nil {
		bak := fmt.Sprintf("%s.bak.%d", path, time.Now().Unix())
		if err := os.WriteFile(bak, old, 0o644); err != nil {
			fatalf("backup %s: %v", path, err)
		}
		fmt.Printf("backed up existing file to %s\n", filepath.Base(bak))
	}
	if err := writeAtomic(path, []byte(body), 0o644); err != nil {
		fatalf("%v", err)
	}
	fmt.Printf("wrote %s\n", path)
	fmt.Printf("  fill in Now and Waiting on — the scaffold states nothing on your behalf.\n")
	fmt.Printf("  check it parses with: shabadoo mission\n")
}
