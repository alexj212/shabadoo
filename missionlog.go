package main

import (
	"context"

	"shabadoo/tmux"
)

// projectLog is one project's log, as the agent reports it.
//
// Owner and Updated travel with the entries at the mobile client's request: a
// log whose newest entry is eleven days old is a project nobody is writing down,
// and that is worth showing NEXT to the entries rather than leaving a reader to
// infer it from dates. Stale-and-honest beats fresh-looking.
type projectLog struct {
	Project string       `json:"project"`
	Path    string       `json:"path"`
	Status  string       `json:"status,omitempty"`
	Owner   string       `json:"owner,omitempty"`
	Updated string       `json:"updated,omitempty"`
	Log     []MissionLog `json:"log,omitempty"`
}

// missionLogs reads every distinct mission on this host.
//
// Keyed by the directory that OWNS the file, not by the project root: several
// panes share one mission — a split window — and reading it once per pane would
// render duplicates rather than one project. But a session scoped into a
// subfolder with its own MISSION.md is a DIFFERENT mission, and keying on the
// root collapsed seven of them into their parent's card.
func missionLogs(ctx context.Context) ([]projectLog, error) {
	// Panes, not sessions: each pane has its own directory and therefore its own
	// project, which is the whole reason reporting moved from windows to panes.
	// A window's cwd is the ACTIVE pane's, which is right for one pane and a
	// silent lie for two.
	panes, err := tmux.Panes(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := []projectLog{}
	for _, p := range panes {
		if p.Path == "" {
			continue
		}
		root := projectRoot(p.Path)
		if root == "" {
			continue
		}
		dir := missionDirFor(p.Path, root)
		if dir == "" || seen[dir] {
			continue // no MISSION.md above this pane — absent, not empty
		}
		seen[dir] = true
		m := readMission(dir)
		if m == nil {
			continue
		}
		out = append(out, projectLog{
			Project: projectName(dir), Path: dir, Status: m.Status,
			Owner: m.Owner, Updated: m.Updated, Log: m.Log,
		})
	}
	return out, nil
}
