package main

// `shaba todo` — every open item on the fleet, grouped by who is blocked.
//
// The dashboard has rendered this for a while and the terminal had no answer to
// it. `blockers` looked like one and was not: it reads four MECHANICAL states —
// a pane at a dialog, undrained mail, a blocked task, an offline node — and
// never the `## Waiting on` rows that people actually write. With forty
// human-owned rows standing across fifteen projects it printed "nothing is
// stuck", which is the confidently-wrong answer this project keeps finding: a
// component reporting its partial view AS the whole.
//
// Grouped by owner rather than by project, which is the whole design and not
// formatting. A list sorted by subject makes every reader scan all of it to
// find their own rows; grouped by blocker, a person reads the first table and
// stops.

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

type todoRow struct {
	project string
	item    string
	since   int64
	cut     bool
	kind    string // "" mission row, "task", "dialog"
	extra   string // second line: what to run, who asked
}

type resolvedRow struct {
	Project  string `json:"project"`
	Owner    string `json:"owner"`
	Item     string `json:"item"`
	Stood    int64  `json:"stood"`
	Resolved int64  `json:"resolved_at"`
}

func runTodo(args []string) {
	fs := flag.NewFlagSet("todo", flag.ExitOnError)
	coord := fs.String("coord", "", "coordinator base URL")
	mine := fs.Bool("mine", false, "only rows owned by you")
	closed := fs.Bool("closed", false, "also show what has closed recently")
	days := fs.Int("days", 7, "window for --closed, in days")
	project := fs.String("project", "", "only this project (substring)")
	width := fs.Int("width", 0, "wrap width; 0 reads $COLUMNS, else 100")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: shaba todo [--mine] [--closed [--days N]] [--project X]

Every open item on the fleet, grouped by who is blocked: the `+"`## Waiting on`"+`
rows projects write in their MISSION.md, plus delegated tasks that are blocked
and panes sitting at a prompt. --closed adds what stopped being listed, and how
long each stood.
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
	now := time.Now().Unix()
	w := termWidth(*width)
	// An age here is bounded by how long the coordinator has been watching.
	// After a restart every row is first seen at once, so they all read as
	// minutes old regardless of how long they have actually stood — which is
	// the "unknown rendered as measured" failure this project keeps finding.
	// Cheap to bound, so it is bounded and said.
	uptime := coordUptime(c)

	// Keyed by owner. "" is a row whose author named nobody, which is NOT the
	// same as "nobody" — one is a blocker with no assignee, the other is a
	// decision that it needs no one. Rendering them alike is how the first
	// quietly becomes the second.
	groups := map[string][]todoRow{}
	add := func(owner string, r todoRow) {
		owner = strings.ToLower(strings.TrimSpace(owner))
		if *mine && owner != "you" {
			return
		}
		groups[owner] = append(groups[owner], r)
	}

	// Projects reporting nothing are named, not counted: "has not said" is a
	// different answer from "nothing waiting", and a count invites a threshold
	// judgment the reader can defer forever.
	var silent, quiet []string
	seenProj := map[string]bool{}
	dropped := 0
	offline := []string{}

	for _, n := range nodes {
		if !n.Online {
			offline = append(offline, fmt.Sprintf("%s (%d session(s) frozen)", n.Node, len(n.Sessions)))
			continue
		}
		for _, s := range n.Sessions {
			name := s.Project
			if name == "" {
				name = s.Alias
			}
			if *project != "" && !strings.Contains(strings.ToLower(name), strings.ToLower(*project)) {
				continue
			}
			// A pane at a dialog is an open item with the shortest fuse on the
			// page: a machine is halted until somebody answers. It belongs in
			// `you` rather than in a section of its own, because the reader's
			// question is "what needs me", not "by what mechanism".
			if s.InputState == "dialog" {
				q := s.Asking
				if q == "" {
					q = "question could not be read — open the pane"
				}
				add("you", todoRow{project: name, item: q, kind: "dialog",
					extra: "shaba tail " + nameOf(s)})
			}
			if s.Pending > 0 && s.InputState != "dialog" {
				add("you", todoRow{project: name, item: fmt.Sprintf(
					"%d message(s) delivered and never picked up — the nudge fails silently",
					s.Pending), kind: "unread", extra: "shaba tail " + nameOf(s)})
			}
			if seenProj[n.Node+"\x00"+name] {
				continue
			}
			seenProj[n.Node+"\x00"+name] = true
			if s.MissionStatus == "" && s.MissionNow == "" {
				silent = append(silent, name)
				continue
			}
			dropped += s.MissionDropped
			if len(s.MissionWaiting) == 0 {
				quiet = append(quiet, name)
				continue
			}
			for _, it := range s.MissionWaiting {
				add(it.Owner, todoRow{project: name, item: it.Item,
					since: it.Since, cut: it.Truncated})
			}
		}
	}

	// Delegated tasks join the same grouping: an assignee is an owner like any
	// other, and a second list below would leave the reader doing the grouping
	// again by hand.
	inFlight := 0
	if raw, err := c.do("GET", "/api/tasks", nil); err == nil {
		var out struct{ Tasks []blockerTask `json:"tasks"` }
		if json.Unmarshal(raw, &out) == nil {
			for _, t := range out.Tasks {
				if t.State == "done" || t.State == "dropped" {
					continue
				}
				if t.State != "blocked" {
					inFlight++
					continue
				}
				why := t.Note
				if why == "" {
					why = "no reason given — which is itself a question for whoever asked"
				}
				add(shortID(t.SessionID), todoRow{project: shortID(t.SessionID),
					item: why, kind: "task", extra: "asked by " + t.RequestedBy})
			}
		}
	}

	total := 0
	for _, rows := range groups {
		total += len(rows)
	}
	proj := len(seenProj)
	fmt.Printf("\n📋 Open items — %d across %d project%s\n", total, proj, plural(proj))

	// you, then rows nobody was named for, then peers alphabetically, then work
	// that needs no one. `nobody` last because it needs no action; putting it
	// anywhere else makes the reader scroll past it to reach what does.
	rank := func(o string) int {
		switch o {
		case "you":
			return 0
		case "":
			return 1
		case "nobody":
			return 3
		}
		return 2
	}
	owners := make([]string, 0, len(groups))
	for o := range groups {
		owners = append(owners, o)
	}
	sort.Slice(owners, func(i, j int) bool {
		if rank(owners[i]) != rank(owners[j]) {
			return rank(owners[i]) < rank(owners[j])
		}
		return owners[i] < owners[j]
	})

	for _, o := range owners {
		rows := groups[o]
		// Oldest first. This is what turns a list into a priority: a row
		// standing three days and one that appeared two minutes ago are not the
		// same request. Rows with no age sort LAST — unknown is not new, and
		// floating an unmeasured row above measured ones would out-rank them.
		sort.SliceStable(rows, func(i, j int) bool {
			a, b := rows[i].since, rows[j].since
			if a == 0 {
				return false
			}
			if b == 0 {
				return true
			}
			return a < b
		})
		if o == "nobody" {
			// Collapsed to prose, deliberately: it needs no action, so it gets
			// no table. Rendering everything at equal weight is how the rows
			// that matter stop being noticed.
			fmt.Printf("\n⚪ Open, nobody blocked — %d\n", len(rows))
			for _, r := range rows {
				fmt.Printf("   %s: %s\n", r.project, truncate(r.item, w-6-len(r.project)))
			}
			continue
		}
		fmt.Printf("\n%s %s — %d\n", markFor(o), labelFor(o), len(rows))
		printTodoTable(rows, now, w)
	}

	if *closed {
		printClosed(c, *days, w)
	}

	// Tails. Both are stated: "nothing waiting" and "has not said" are
	// different answers and neither may be rendered as the other.
	fmt.Println()
	if inFlight > 0 {
		fmt.Printf("   %d delegated task%s in flight — not blocked, so not listed\n",
			inFlight, plural(inFlight))
	}
	if dropped > 0 {
		fmt.Printf("   ⚠ %d waiting row%s over the six-row cap and NOT shown — "+
			"run `shabadoo mission` in that project to see them\n", dropped, plural(dropped))
	}
	if len(quiet) > 0 {
		sort.Strings(quiet)
		printWrapped("   %d reporting, nothing waiting: ", len(quiet), quiet, w)
	}
	if len(silent) > 0 {
		sort.Strings(silent)
		// Wrapped, never truncated. Naming these is the entire point — a count
		// invites a threshold judgment the reader can defer forever, and a list
		// cut off at "ad…" is a count wearing a list's clothes.
		printWrapped("   ⚠ %d with no MISSION.md — has not said, which is not idle: ",
			len(silent), silent, w)
		fmt.Printf("     each can write one with `shabadoo mission init`\n")
	}
	oldest := int64(0)
	for _, rows := range groups {
		for _, r := range rows {
			if r.since > 0 && now-r.since > oldest {
				oldest = now - r.since
			}
		}
	}
	if uptime > 0 && oldest > 0 && oldest >= uptime-60 {
		fmt.Printf("   ⓘ ages are bounded by the coordinator's uptime (%s) — "+
			"a row may have stood far longer than it reads\n", shortAge(uptime))
	}
	for _, off := range offline {
		fmt.Printf("   ⚠ node offline: %s — anything waiting there is waiting on nothing\n", off)
	}
	if total == 0 && len(silent) == 0 {
		fmt.Println("   nothing open")
	}
	fmt.Println()
}

func markFor(o string) string {
	if o == "you" || o == "" {
		return "🔴"
	}
	return "🔵"
}

func labelFor(o string) string {
	switch o {
	case "you":
		return "Waiting on you"
	case "":
		return "Waiting on — nobody named"
	}
	return "Waiting on " + o
}

// printTodoTable draws the box the wrap-up format uses. Widths are computed
// from the terminal rather than fixed: a table wider than the window wraps into
// nonsense, and a table narrower than it wastes the column that carries the
// risk and the cost — which is the half that makes a row decidable.
func printTodoTable(rows []todoRow, now int64, w int) {
	projW := 10
	for _, r := range rows {
		if n := len([]rune(r.project)); n > projW {
			projW = n
		}
	}
	if projW > 26 {
		projW = 26
	}
	ageW := 5
	itemW := w - projW - ageW - 10
	if itemW < 24 {
		itemW = 24
	}

	bar := func(l, m, r string) string {
		return l + strings.Repeat("─", projW+2) + m + strings.Repeat("─", itemW+2) +
			m + strings.Repeat("─", ageW+2) + r
	}
	fmt.Println(bar("┌", "┬", "┐"))
	for _, r := range rows {
		item := r.item
		if r.cut {
			item = strings.TrimRight(item, "…") + "…"
		}
		age := ""
		if r.since > 0 {
			age = shortAge(now - r.since)
		}
		tag := ""
		switch r.kind {
		case "task":
			tag = "[task] "
		case "dialog":
			tag = "[prompt] "
		case "unread":
			tag = "[mail] "
		}
		for i, line := range wrapRunes(tag+item, itemW) {
			p, a := "", ""
			if i == 0 {
				p, a = truncate(r.project, projW), age
			}
			fmt.Printf("│ %-*s │ %-*s │ %*s │\n", projW, p, itemW, line, ageW, a)
		}
		if r.extra != "" {
			fmt.Printf("│ %-*s │ %-*s │ %*s │\n", projW, "", itemW,
				truncate("→ "+r.extra, itemW), ageW, "")
		}
	}
	fmt.Println(bar("└", "┴", "┘"))
}

func printClosed(c *client, days, w int) {
	raw, err := c.do("GET", fmt.Sprintf("/api/missions/resolved?days=%d", days), nil)
	if err != nil {
		// Said, never swallowed. An unreachable history is not an empty one,
		// and printing nothing here would read as a fleet that closed nothing.
		fmt.Printf("\n✅ Closed — could not be read: %v\n", err)
		return
	}
	var out struct {
		Resolved []resolvedRow `json:"resolved"`
		Summary  struct {
			Window int   `json:"window_days"`
			Count  int   `json:"count"`
			Median int64 `json:"median_stood"`
		} `json:"summary"`
	}
	if json.Unmarshal(raw, &out) != nil {
		fmt.Printf("\n✅ Closed — the coordinator's answer could not be read\n")
		return
	}
	fmt.Printf("\n✅ Closed in the last %d day%s — %d", days, plural(days), out.Summary.Count)
	if out.Summary.Median > 0 {
		fmt.Printf(" · typically stood %s", shortAge(out.Summary.Median))
	}
	fmt.Println()
	if out.Summary.Count == 0 {
		// A window with nothing in it and a window that was never measured are
		// different answers. A coordinator restarted an hour ago has the second.
		fmt.Println("   nothing recorded closed in this window — note that tracking " +
			"starts when the coordinator does")
		return
	}
	rows := make([]todoRow, 0, len(out.Resolved))
	for _, r := range out.Resolved {
		who := r.Owner
		if who == "" {
			who = "—"
		}
		rows = append(rows, todoRow{project: r.Project,
			item: fmt.Sprintf("(%s) %s", who, r.Item), since: r.Stood})
	}
	printClosedTable(rows, w)
}

func printClosedTable(rows []todoRow, w int) {
	projW := 10
	for _, r := range rows {
		if n := len([]rune(r.project)); n > projW {
			projW = n
		}
	}
	if projW > 26 {
		projW = 26
	}
	stoodW := 6
	itemW := w - projW - stoodW - 10
	if itemW < 24 {
		itemW = 24
	}
	bar := func(l, m, r string) string {
		return l + strings.Repeat("─", projW+2) + m + strings.Repeat("─", itemW+2) +
			m + strings.Repeat("─", stoodW+2) + r
	}
	fmt.Println(bar("┌", "┬", "┐"))
	for _, r := range rows {
		for i, line := range wrapRunes(r.item, itemW) {
			p, st := "", ""
			if i == 0 {
				p, st = truncate(r.project, projW), shortAge(r.since)
			}
			fmt.Printf("│ %-*s │ %-*s │ %*s │\n", projW, p, itemW, line, stoodW, st)
		}
	}
	fmt.Println(bar("└", "┴", "┘"))
}

// wrapRunes breaks on runes, not bytes. A mission is prose — an em dash or an
// accented name is enough to make a byte split emit invalid UTF-8, and these
// rows are full of both.
func wrapRunes(s string, w int) []string {
	if w < 8 {
		w = 8
	}
	var out []string
	for _, word := range strings.Fields(s) {
		r := []rune(word)
		if len(out) == 0 {
			out = append(out, "")
		}
		last := []rune(out[len(out)-1])
		switch {
		case len(last) == 0 && len(r) <= w:
			out[len(out)-1] = word
		case len(last)+1+len(r) <= w:
			out[len(out)-1] = string(last) + " " + word
		case len(r) <= w:
			out = append(out, word)
		default:
			// A single word longer than the column: hard-split it rather than
			// let the table blow out. URLs and paths reach this.
			if len(last) > 0 {
				out = append(out, "")
			}
			for len(r) > w {
				out[len(out)-1] = string(r[:w])
				r = r[w:]
				out = append(out, "")
			}
			out[len(out)-1] = string(r)
		}
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

// shortAge is a duration a person reads at a glance: 4m, 3h, 2d.
func shortAge(secs int64) string {
	switch {
	case secs <= 0:
		return ""
	case secs < 90:
		return fmt.Sprintf("%ds", secs)
	case secs < 90*60:
		return fmt.Sprintf("%dm", secs/60)
	case secs < 48*3600:
		return fmt.Sprintf("%dh", secs/3600)
	}
	return fmt.Sprintf("%dd", secs/86400)
}

func termWidth(flagW int) int {
	if flagW > 0 {
		return flagW
	}
	if v := os.Getenv("COLUMNS"); v != "" {
		n := 0
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n >= 60 {
			return n
		}
	}
	return 100
}

// printWrapped renders a labelled list across as many lines as it needs.
func printWrapped(format string, n int, items []string, w int) {
	head := fmt.Sprintf(format, n)
	lines := wrapRunes(strings.Join(items, " · "), w-len([]rune(head))-2)
	for i, l := range lines {
		if i == 0 {
			fmt.Printf("%s%s\n", head, l)
			continue
		}
		fmt.Printf("%*s%s\n", len([]rune(head)), "", l)
	}
}

// coordUptime asks the unauthenticated health endpoint how long this
// coordinator has been up. Zero means it could not be established — never
// treated as "up forever", which would suppress the very warning it gates.
func coordUptime(c *client) int64 {
	raw, err := c.do("GET", "/healthz", nil)
	if err != nil {
		return 0
	}
	var h struct {
		Uptime int64 `json:"uptime"`
	}
	if json.Unmarshal(raw, &h) != nil {
		return 0
	}
	return h.Uptime
}
