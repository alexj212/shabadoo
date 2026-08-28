package main

// `shaba blockers` — what is stuck, and nothing else.
//
// The dashboard answers "what is happening"; `who` answers "who is out there".
// Neither answers the question actually asked in passing, which is **are we
// good** — and answering it today means reading three screens and knowing which
// three.
//
// Everything here is already reported and none of it was collected: a session
// waiting on a dialog, mail that arrived and was never picked up, work handed
// over and blocked, a node that has gone. Four states with one thing in common
// — each is somebody or something waiting, and none of them resolves itself.

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

type blockerTask struct {
	SessionID   string `json:"session_id"`
	RequestedBy string `json:"requested_by"`
	State       string `json:"state"`
	Brief       string `json:"brief"`
	Note        string `json:"note"`
}

func runBlockers(args []string) {
	fs := flag.NewFlagSet("blockers", flag.ExitOnError)
	coord := fs.String("coord", "", "coordinator base URL")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: shaba blockers

What is stuck: sessions waiting on a prompt, mail nobody picked up, work handed
over and blocked, and nodes that have gone. Silence means nothing is stuck.
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

	found := 0

	// 1. Waiting on a human. Offline nodes are excluded: their sessions are a
	// frozen last-reported view, so one that was at a dialog when its agent
	// dropped still reads `dialog` and would sit here forever.
	for _, n := range nodes {
		if !n.Online {
			continue
		}
		for _, s := range n.Sessions {
			if s.InputState != "dialog" {
				continue
			}
			found++
			q := s.Asking
			if q == "" {
				q = "question could not be read — open the pane"
			}
			fmt.Printf("WAITING  %-22s %s\n", nameOf(s), truncate(q, 76))
			fmt.Printf("         %-22s shaba tail %s\n", "", nameOf(s))
		}
	}

	// 2. Mail that arrived and was never picked up. The nudge that should have
	// closed this loop fails silently, which is the whole reason to look.
	for _, n := range nodes {
		if !n.Online {
			continue
		}
		for _, s := range n.Sessions {
			if s.Pending == 0 || s.InputState == "dialog" {
				continue
			}
			found++
			fmt.Printf("UNREAD   %-22s %d message(s) not picked up\n", nameOf(s), s.Pending)
		}
	}

	// 3. Work handed over and blocked on something.
	if raw, err := c.do("GET", "/api/tasks", nil); err == nil {
		var out struct {
			Tasks []blockerTask `json:"tasks"`
		}
		if json.Unmarshal(raw, &out) == nil {
			for _, t := range out.Tasks {
				if t.State != "blocked" {
					continue
				}
				found++
				why := t.Note
				if why == "" {
					why = "no reason given — which is itself a question for whoever asked"
				}
				fmt.Printf("BLOCKED  %-22s %s\n", shortID(t.SessionID), truncate(why, 76))
			}
		}
	}

	// 4. A node that has gone. Its sessions cannot be driven and its panes
	// cannot be read, so anything waiting on that machine is waiting on nothing.
	for _, n := range nodes {
		if n.Online {
			continue
		}
		found++
		fmt.Printf("OFFLINE  %-22s %d session(s) frozen at their last report\n",
			n.Node, len(n.Sessions))
	}

	if found == 0 {
		fmt.Println("nothing is stuck")
	}
}

func nameOf(s cliSession) string {
	if s.Alias != "" {
		return s.Alias
	}
	return s.Project
}
