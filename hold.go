package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
)

// `shabadoo hold` asks sessions to report rather than act.
//
// The operator's problem, in their words: sessions "running off on tasks" when
// they wanted "to be in loop of actions". The wake cap paces how many wake at
// once; this is about whether they act at all.
func runHold(args []string) {
	fset := flag.NewFlagSet("hold", flag.ExitOnError)
	fset.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: shabadoo hold [on [name...] | off]

  hold            what is held right now
  hold on         hold the whole fleet
  hold on NAME…   hold only those sessions (alias, project or session id)
  hold off        release everything

A held session is still delivered its mail. It is asked to reply with what it
WOULD do and then wait, rather than doing it. Core sessions, this project, and
your own messages are never held — a hold that silenced the sessions used to
lift it would be a lockout.

`)
		fset.PrintDefaults()
	}
	coord := fset.String("coord", "", "coordinator base URL")
	rest := argsAndFlags(fset, args)
	c, err := newClient(*coord)
	if err != nil {
		fatalf("%v", err)
	}

	show := func(raw []byte) {
		var st struct {
			Enabled bool     `json:"enabled"`
			Holding []string `json:"holding"`
			Held    int64    `json:"held"`
		}
		_ = json.Unmarshal(raw, &st)
		if !st.Enabled {
			fmt.Println("nothing is held — mail is delivered and acted on normally")
			return
		}
		fmt.Printf("HELD: %s\n", strings.Join(st.Holding, ", "))
		fmt.Printf("  sessions are delivered their mail and asked to report what they\n" +
			"  would do, then wait. `shaba hold off` releases.\n")
		if st.Held > 0 {
			// Named, because a hold nobody has met yet and a hold that is doing
			// nothing look identical otherwise.
			fmt.Printf("  applied %d time(s) since this coordinator started\n", st.Held)
		}
	}

	switch {
	case len(rest) == 0:
		raw, err := c.do("GET", "/api/hold", nil)
		if err != nil {
			fatalf("%v", err)
		}
		show(raw)
	case strings.EqualFold(rest[0], "off"):
		raw, err := c.do("POST", "/api/hold", map[string]any{"holding": []string{}})
		if err != nil {
			fatalf("%v", err)
		}
		show(raw)
	case strings.EqualFold(rest[0], "on"):
		names := rest[1:]
		if len(names) == 0 {
			names = []string{"all"}
		}
		raw, err := c.do("POST", "/api/hold", map[string]any{"holding": names})
		if err != nil {
			fatalf("%v", err)
		}
		show(raw)
	default:
		fset.Usage()
		os.Exit(2)
	}
}
