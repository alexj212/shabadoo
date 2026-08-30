package main

// `shabadoo who` — the directory, as a peer reads it.
//
// The framework already carries what a session needs to know about the others:
// project, node, what it is for (`description`, from that project's CLAUDE.md
// frontmatter) and what it is doing right now (`note`, set by the session
// itself). Nothing displayed either, so nobody could see that most of it was
// empty — measured when this was written: 3 of 14 sessions had a description
// and 0 had a status.
//
// That is a content gap, not a mechanism gap, and the fix for a content gap is
// to show it. A blank line here is a session a peer can address by name and
// knows nothing else about.

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

func runWho(args []string) {
	fs := flag.NewFlagSet("who", flag.ExitOnError)
	coord := fs.String("coord", "", "coordinator base URL")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: shabadoo who

Who is out there, what each one is for, and what it says it is doing.

A session's purpose comes from the description: line in its project's CLAUDE.md
frontmatter; its status from session_status_set. Both are how a peer decides
whether to hand it work, so a blank is worth filling.
`)
	}
	fs.Parse(args)

	c, err := newClient(*coord)
	if err != nil {
		fatalf("%v", err)
	}
	nodes, err := fetchSessions(c)
	if err != nil {
		fatalf("%v", err)
	}

	// A project on more than one node is not a duplicate — it is the same
	// project where the hosts differ in what they can DO. `minutes` lives on
	// both because one can drive Windows audio over interop and the other holds
	// the Apple toolchain. Until now `who` printed the two identically, with the
	// same description and the same mission text, so the listing could not
	// answer the question somebody actually has: which of these do I want.
	onNodes := map[string]int{}
	for _, n := range nodes {
		for _, s := range n.Sessions {
			name := s.Project
			if name == "" {
				name = s.Alias
			}
			onNodes[name]++
		}
	}
	caps := map[string][]string{}
	for _, n := range nodes {
		caps[n.Node] = distinctCaps(n, nodes)
	}

	total, described := 0, 0
	for _, n := range nodes {
		for _, s := range n.Sessions {
			total++
			name := s.Project
			if name == "" {
				name = s.Alias
			}
			what := s.Description
			if what != "" {
				described++
			} else {
				what = "— no description; a peer sees this name and nothing else"
			}
			fmt.Printf("%-28s %-5s %s\n", truncate(name, 28), n.Node, truncate(what, 88))
			// The mission, where the project has said. Indented under the
			// project it belongs to rather than in a column, because most
			// projects will not have one and an empty column reads as an
			// answer.
			if s.MissionStatus != "" || s.MissionNow != "" {
				st := s.MissionStatus
				if st == "" {
					st = "?"
				}
				fmt.Printf("%-28s %-5s   [%s] %s\n", "", "", st, truncate(s.MissionNow, 78))
			}
			if s.MissionBlocked != "" {
				fmt.Printf("%-28s %-5s   blocked on: %s\n", "", "", truncate(s.MissionBlocked, 70))
			}
			if s.Note != "" {
				fmt.Printf("%-28s %-5s   doing: %s\n", "", "", truncate(s.Note, 80))
			}
			// Shown only where it DISCRIMINATES: on a project that exists on
			// more than one node. Everywhere else it is a fact about the host
			// that has nothing to do with choosing between two of them, and
			// printing it on every row is how the rows that matter stop being
			// read.
			if onNodes[name] > 1 {
				if c := caps[n.Node]; len(c) > 0 {
					fmt.Printf("%-28s %-5s   this host, not the other%s: %s\n",
						"", "", plural(len(nodes)-1), truncate(strings.Join(c, " "), 70))
				} else if !n.CapsKnown {
					// Said, because absent and unestablished are different
					// answers and this is exactly where somebody is choosing.
					fmt.Printf("%-28s %-5s   this host's capabilities could not be established\n", "", "")
				}
			}
		}
	}
	if total == 0 {
		fmt.Println("no sessions")
		return
	}
	fmt.Printf("\n%d of %d have a description.", described, total)
	if described < total {
		fmt.Printf(" Add a description: line to that project's CLAUDE.md\n" +
			"frontmatter — it is the routing card, and it is trigger text (when should\n" +
			"this be reached for) rather than a summary.")
	}
	fmt.Println()
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
