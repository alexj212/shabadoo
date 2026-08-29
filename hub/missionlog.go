package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
)

// The merged mission log: every project's `## Log` across every connected node,
// newest first.
//
// MERGED rather than per-project, at the mobile client's insistence and against
// my own sketch. Per-project needs the reader to know which card to tap, which
// is a desktop's model — you can see seventeen cards at once there. The
// interesting entry is the one from the project you were not thinking about, so
// making somebody choose a machine before asking a question is the wrong first
// screen.
//
// It carries no `waiting`. That is already on /api/sessions, and the client
// declined a second copy: two sources for one fact is where a phone shows a
// blocker that was resolved an hour ago. The same defect as tools_stale without
// tools_known, one layer up.

// logEntry is one line, flattened with the project it came from.
type logEntry struct {
	ID        string `json:"id"`
	Date      string `json:"date,omitempty"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated,omitempty"`
	Length    int    `json:"length,omitempty"`

	Project string `json:"project"`
	Node    string `json:"node"`
	Status  string `json:"status,omitempty"`
	Owner   string `json:"owner,omitempty"`

	// Updated is the project's own `updated:` line, not this entry's date. A log
	// whose newest entry is eleven days old is a project nobody is writing down,
	// and that belongs beside the entries rather than being inferred from them.
	Updated string `json:"mission_updated,omitempty"`
}

type agentProjectLog struct {
	Project string `json:"project"`
	Status  string `json:"status"`
	Owner   string `json:"owner"`
	Updated string `json:"updated"`
	Log     []struct {
		ID        string `json:"id"`
		Date      string `json:"date"`
		Text      string `json:"text"`
		Truncated bool   `json:"truncated"`
		Length    int    `json:"length"`
	} `json:"log"`
}

type missionLogPage struct {
	Page
	Entries []logEntry `json:"entries"`

	// Nodes that did not answer. A merged view assembled from a subset must say
	// so: this file's own rule is that a component which cannot see the whole
	// fleet must never present its partial view AS the fleet, and a reader who
	// cannot find an entry would otherwise conclude it does not exist.
	Missing []string `json:"missing,omitempty"`
}

// entryTS orders by date. Undated entries sort last rather than first — an
// undated line is not news, and floating them to the top of a "what changed"
// view would put the least dateable content where the newest belongs.
func entryTS(date string) int64 {
	if date == "" {
		return 0
	}
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return 0
	}
	return t.Unix()
}

func (h *humanAPI) missionLog(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenant := tenantOf(ctx)

	limit := 50
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		limit = min(n, 200)
	}
	var cur Cursor
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		c, err := decodeCursor(raw, "b")
		if err != nil {
			http.Error(w, err.Error(), http.StatusGone)
			return
		}
		cur = c
	}

	// Fanned out in PARALLEL, with a bound. Serially, one unreachable node adds
	// its whole timeout to every other node's latency — and this is the endpoint
	// a phone opens first.
	nodes := h.hub.Online(tenant)
	var (
		mu      sync.Mutex
		all     []logEntry
		missing []string
		wg      sync.WaitGroup
	)
	fanCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for _, node := range nodes {
		wg.Add(1)
		go func(node string) {
			defer wg.Done()
			raw, err := h.hub.Call(fanCtx, tenant, node, "mission_logs", map[string]any{})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				missing = append(missing, node)
				return
			}
			var projects []agentProjectLog
			if err := json.Unmarshal(raw, &projects); err != nil {
				missing = append(missing, node)
				return
			}
			for _, p := range projects {
				for _, e := range p.Log {
					all = append(all, logEntry{
						ID: e.ID, Date: e.Date, Text: e.Text,
						Truncated: e.Truncated, Length: e.Length,
						Project: p.Project, Node: node, Status: p.Status,
						Owner: p.Owner, Updated: p.Updated,
					})
				}
			}
		}(node)
	}
	wg.Wait()

	// Sorted by (date desc, id asc). The id tie-break is what makes paging
	// correct rather than merely ordered: dates here are day-granular, so many
	// entries share one timestamp, and without a total order a cursor at a day
	// boundary either loops over entries it has shown or skips ones it has not.
	sort.Slice(all, func(i, j int) bool {
		ti, tj := entryTS(all[i].Date), entryTS(all[j].Date)
		if ti != tj {
			return ti > tj
		}
		return all[i].ID < all[j].ID
	})
	sort.Strings(missing)

	if cur.ID != "" || cur.TS != 0 {
		for len(all) > 0 {
			e := all[0]
			if ts := entryTS(e.Date); ts > cur.TS || (ts == cur.TS && e.ID <= cur.ID) {
				all = all[1:]
				continue
			}
			break
		}
	}

	page := missionLogPage{Missing: missing, Entries: []logEntry{}}
	clamped := ""
	if len(all) > limit {
		all = all[:limit]
		clamped = "count"
	}
	page.Entries = all
	page.Clamped = clamped

	// Next is always present, including on an empty page: "you are current" must
	// be a stated answer rather than inferred from a zero-length array.
	last := Cursor{Dir: "b"}
	if n := len(page.Entries); n > 0 {
		last.TS = entryTS(page.Entries[n-1].Date)
		last.ID = page.Entries[n-1].ID
	} else {
		last = Cursor{Dir: "b", TS: cur.TS, ID: cur.ID}
	}
	page.Next = encodeCursor(last)
	if r.URL.Query().Get("cursor") == "" && len(page.Entries) > 0 {
		page.Tail = encodeCursor(Cursor{Dir: "a",
			TS: entryTS(page.Entries[0].Date), ID: page.Entries[0].ID})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(page)
}
