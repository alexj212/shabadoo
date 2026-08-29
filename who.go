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
		}
	}
	if total == 0 {
		fmt.Println("no sessions")
		return
	}
	fmt.Printf("\n%d of %d have a description.", described, total)
	if described < total {
		fmt.Printf(" Add a description: line to that project's CLAUDE.md\n"+
			"frontmatter — it is the routing card, and it is trigger text (when should\n"+
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
