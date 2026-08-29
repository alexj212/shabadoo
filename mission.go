package main

// What a project says it is doing, read from its MISSION.md.
//
// `CLAUDE.md` says what a project IS and is stable for months. This says what
// it is DOING and changes weekly, which is why they are separate files: mixing
// them means every status edit touches the file whose stability is the point.
//
// Read here rather than by a session, for the same reason `description` is: a
// peer deciding whether to hand work over cannot open somebody else's repo, and
// asking a session to report its own status is the arrangement that produced
// `session_status_set` being set by nobody.

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Mission is the reportable part of a MISSION.md.
//
// Every field is omitempty and the whole struct is absent for a project with no
// MISSION.md — which is the honest answer and must not read as "nothing is
// happening here". A project without one has not said, and a reader has to be
// able to tell that from a project that said it is idle.
type Mission struct {
	Headline string `json:"mission_headline,omitempty"`
	Status   string `json:"mission_status,omitempty"` // active | blocked | paused | done
	Now      string `json:"mission_now,omitempty"`
	Blocked  string `json:"mission_blocked,omitempty"` // absent when not blocked
	Updated  string `json:"mission_updated,omitempty"`

	// Waiting is `## Waiting on`, one entry per line, each naming WHO is
	// blocked. That owner is the whole value: it is what lets a dashboard group
	// by blocker rather than by project, so a person reads their own rows and
	// stops. "you" is the human, "nobody" is open-but-unblocked, anything else
	// is a session name.
	Waiting []MissionWait `json:"mission_waiting,omitempty"`

	// Dropped counts `Waiting on` lines the cap discarded ENTIRELY. It exists
	// because the alternative is this feature failing at its own first job: a
	// blocker present in the file, absent from the dashboard, with nobody able
	// to find out — not the author, who watched it parse, and not the reader,
	// who cannot miss what was never shown.
	//
	// Reported by two sessions who each blew the limit twice within an hour of
	// reading the rule and while specifically watching for it. A limit enforced
	// by silent truncation cannot be complied with by attention, and four
	// failures under concentration is not a discipline problem.
	Dropped int `json:"mission_dropped,omitempty"`
}

// MissionWait is one line of `## Waiting on`.
type MissionWait struct {
	// Owner is empty when the line did not name one — which is NOT the same as
	// "nobody", and the difference is the point. "nobody" is a decision that
	// this needs no one; empty is a line written before owners existed, or one
	// whose author did not say. Rendering them alike would turn an unattributed
	// blocker into a resolved one.
	Owner string `json:"owner,omitempty"`
	Item  string `json:"item"`

	// Truncated marks a row that was cut to fit. Kept separate from Dropped
	// because they are different answers: this row is present and shortened,
	// that one is not here at all. Collapsing them would tell a reader "some
	// rows are incomplete" when the truth is "one is missing".
	Truncated bool `json:"truncated,omitempty"`
}

// missionStates is the closed set. A status outside it is dropped rather than
// passed through: five states exist so a reader can hold them all, and a
// vocabulary that grows by typo stops being one.
var missionStates = map[string]bool{
	"active": true, "blocked": true, "paused": true, "done": true,
}

// readMission parses a project's MISSION.md, returning nil when there is none.
//
// A tiny hand parser rather than a markdown dependency, for the reason the
// frontmatter reader gives: it understands one shape, and anything it does not
// understand yields nothing — which leaves the project exactly where every
// project is today and is therefore safe. A malformed file must never break
// reporting for the projects around it.
func readMission(root string) *Mission {
	if root == "" {
		return nil
	}
	f, err := os.Open(filepath.Join(root, "MISSION.md"))
	if err != nil {
		return nil
	}
	defer f.Close()

	var m Mission
	section := ""
	sc := bufio.NewScanner(f)
	// A mission is prose, not data: cap the line length so a pathological file
	// cannot make an agent's report expensive.
	sc.Buffer(make([]byte, 0, 64<<10), 64<<10)

	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t")
		trimmed := strings.TrimSpace(line)

		switch {
		case strings.HasPrefix(line, "## "):
			section = strings.ToLower(strings.TrimSpace(line[3:]))
			continue
		case strings.HasPrefix(line, "# "):
			if m.Headline == "" {
				m.Headline = clampMission(strings.TrimSpace(line[2:]))
			}
			continue
		}

		if section == "" {
			// The header block: `key: value` before any section.
			if k, v, ok := strings.Cut(trimmed, ":"); ok {
				switch strings.ToLower(strings.TrimSpace(k)) {
				case "status":
					if s := strings.ToLower(strings.TrimSpace(v)); missionStates[s] {
						m.Status = s
					}
				case "updated":
					m.Updated = clampMission(strings.TrimSpace(v))
				}
			}
			continue
		}

		if trimmed == "" {
			continue
		}
		switch section {
		case "now":
			if m.Now == "" {
				m.Now = clampMission(trimmed)
			}
		case "waiting on", "waiting":
			m.addWaiting(trimmed)
		case "blocked on", "blocked":
			// The section `Waiting on` replaced. Read into the same list with
			// no owner, because that is what the line actually says — it is a
			// blocker nobody has been named for, and inventing "you" here would
			// put words in the file's mouth.
			m.addWaiting(trimmed)
		}
	}
	// Blocked is derived rather than stored, so the two can never disagree. It
	// is the first entry that is waiting on somebody — "nobody" is open work,
	// not a blocker.
	for _, w := range m.Waiting {
		if w.Owner != "nobody" {
			m.Blocked = w.Item
			if w.Owner != "" {
				m.Blocked = w.Owner + ": " + w.Item
			}
			break
		}
	}

	if m.Headline == "" && m.Status == "" && m.Now == "" && len(m.Waiting) == 0 {
		return nil // a file that says nothing is the same as no file
	}
	return &m
}

// addWaiting parses one `- owner: item` line.
//
// Bounded at six entries of 120 runes: these ride EVERY agent report, every five
// seconds, for every project on the node. A file that lists thirty open
// questions must not make the report expensive for the ten projects beside it —
// and a wrap-up that long is the sign the items are too small anyway.
func (m *Mission) addWaiting(line string) {
	const maxEntries = 6
	line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
	if line == "" {
		return
	}
	// Counted BEFORE the cap check, so a dropped row is reported rather than
	// forgotten. An empty line is not a dropped row and must not inflate this.
	if len(m.Waiting) >= maxEntries {
		m.Dropped++
		return
	}
	w := MissionWait{Item: line}
	// Only a SHORT leading token is an owner. Splitting on the first colon
	// anywhere would turn "shipped: the paging dialect, which fixes: nothing"
	// into an owner of "shipped", and prose contains colons far more often than
	// it contains owners.
	if i := strings.Index(line, ":"); i > 0 && i <= 32 && !strings.Contains(line[:i], " ") {
		w.Owner = strings.ToLower(line[:i])
		w.Item = strings.TrimSpace(line[i+1:])
	}
	full := w.Item
	if w.Item = clampMissionTo(w.Item, 120); w.Item == "" {
		return
	}
	w.Truncated = w.Item != full
	m.Waiting = append(m.Waiting, w)
}

// clampMission bounds one field. These ride every agent report, and a project
// that wrote an essay in `Now` must not make the report expensive for the ten
// projects beside it.
func clampMission(s string) string { return clampMissionTo(s, 240) }

func clampMissionTo(s string, max int) string { // max is RUNES, not bytes
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	// Cut on a RUNE boundary. A byte offset splits a multi-byte character and
	// emits invalid UTF-8, which is reachable here because a mission is prose —
	// an em dash or an accented name is enough. Caught by a test asserting a
	// byte bound, which is the sort of near-miss a fixture of ASCII would never
	// have produced.
	out := []rune(s)[:max-1]
	return string(out) + "…"
}
