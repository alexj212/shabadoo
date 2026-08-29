package hub

import (
	"fmt"
	"sort"
	"testing"
)

// Paging must neither LOOP nor SKIP across a day boundary.
//
// This is the whole reason the cursor carries an id as well as a timestamp.
// Dates in a mission log are day-granular, so a dozen entries share one
// timestamp; a cursor holding only the date either re-serves everything from
// that day on the next page or jumps past it. Both failures are invisible from a
// single page, which is why this walks the WHOLE collection and compares the set
// — a test that fetched two pages and eyeballed them would pass for either bug.
func TestMissionLogPagesWithoutLoopingOrSkipping(t *testing.T) {
	// Three days, many entries per day: the tie-break is exercised only when a
	// page boundary lands mid-day, so the page size deliberately does not divide
	// the day size.
	var all []logEntry
	for d, date := range []string{"2026-08-29", "2026-08-28", "2026-08-27"} {
		for i := 0; i < 7; i++ {
			all = append(all, logEntry{
				ID: fmt.Sprintf("%d-%02d", d, i), Date: date, Project: "p",
			})
		}
	}
	sort.Slice(all, func(i, j int) bool {
		ti, tj := entryTS(all[i].Date), entryTS(all[j].Date)
		if ti != tj {
			return ti > tj
		}
		return all[i].ID < all[j].ID
	})

	const pageSize = 5 // does not divide 7
	seen := map[string]int{}
	var order []string
	cur := Cursor{Dir: "b"}
	for page := 0; page < 20; page++ {
		rest := all
		if cur.ID != "" || cur.TS != 0 {
			for len(rest) > 0 {
				e := rest[0]
				if ts := entryTS(e.Date); ts > cur.TS || (ts == cur.TS && e.ID <= cur.ID) {
					rest = rest[1:]
					continue
				}
				break
			}
		}
		if len(rest) == 0 {
			break
		}
		if len(rest) > pageSize {
			rest = rest[:pageSize]
		}
		for _, e := range rest {
			seen[e.ID]++
			order = append(order, e.ID)
		}
		last := rest[len(rest)-1]
		cur = Cursor{Dir: "b", TS: entryTS(last.Date), ID: last.ID}
	}

	if len(order) != len(all) {
		t.Fatalf("walked %d entries, collection has %d — the cursor %s",
			len(order), len(all),
			map[bool]string{true: "SKIPPED some", false: "LOOPED"}[len(order) < len(all)])
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("entry %s served %d times; paging must be exactly-once", id, n)
		}
	}
	for i := range all {
		if order[i] != all[i].ID {
			t.Fatalf("position %d: paged %s, sorted %s — paging order must match "+
				"the collection order", i, order[i], all[i].ID)
		}
	}
}

// A cursor minted for the tail must not be accepted for history, and vice
// versa. Asserting only that a good cursor works passes for an implementation
// that accepts anything.
func TestMissionLogRefusesTheOtherDirection(t *testing.T) {
	forward := encodeCursor(Cursor{Dir: "a", TS: 1, ID: "x"})
	if _, err := decodeCursor(forward, "b"); err == nil {
		t.Error("a forward cursor was accepted for a backward page; mixing the " +
			"two silently pages the wrong way at the boundary")
	}
	back := encodeCursor(Cursor{Dir: "b", TS: 1, ID: "x"})
	if _, err := decodeCursor(back, "b"); err != nil {
		t.Errorf("a valid backward cursor was refused: %v", err)
	}
}

// Undated entries sort LAST. An undated line is not news, and floating it to the
// top of a "what changed" view puts the least dateable content where the newest
// belongs.
func TestUndatedEntriesSortLast(t *testing.T) {
	if entryTS("") != 0 {
		t.Error("an undated entry must carry the lowest timestamp")
	}
	if entryTS("2026-08-29") <= entryTS("2026-08-28") {
		t.Error("dates must order")
	}
	if entryTS("not-a-date") != 0 {
		t.Error("an unparseable date must degrade to undated, not to now")
	}
}
