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
	"unicode/utf8"
	"os"
	"path/filepath"
	"strings"
)

// Mission is the reportable part of a MISSION.md.
//
// Every field is omitempty and the whole struct is absent for a project with no
// MISSION.md — which is the honest answer and must not read as "nothing is
// happening here". A project without one has not said, and a reader has to be
// able to tell that from a project that said it is idle.
type Mission struct {
	Headline string `json:"mission_headline,omitempty"`
	Status   string `json:"mission_status,omitempty"`  // active | blocked | paused | done
	Now      string `json:"mission_now,omitempty"`
	Blocked  string `json:"mission_blocked,omitempty"` // absent when not blocked
	Updated  string `json:"mission_updated,omitempty"`
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
		case "blocked on", "blocked":
			if m.Blocked == "" {
				m.Blocked = clampMission(strings.TrimPrefix(trimmed, "- "))
			}
		}
	}
	if m.Headline == "" && m.Status == "" && m.Now == "" {
		return nil // a file that says nothing is the same as no file
	}
	return &m
}

// clampMission bounds one field. These ride every agent report, and a project
// that wrote an essay in `Now` must not make the report expensive for the ten
// projects beside it.
func clampMission(s string) string {
	const max = 240 // runes, not bytes
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
