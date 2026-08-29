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

// missionLogs reads every distinct project on this host.
//
// Keyed by project ROOT rather than by pane: several panes share one project —
// a split window, or a session scoped into a subfolder — and reading the same
// file once per pane would return the same entries several times, which a merged
// fleet view would render as duplicates rather than as one project.
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
		if root == "" || seen[root] {
			continue
		}
		seen[root] = true
		m := readMission(root)
		if m == nil {
			continue // has no MISSION.md — absent, not empty
		}
		out = append(out, projectLog{
			Project: projectName(root), Path: root, Status: m.Status,
			Owner: m.Owner, Updated: m.Updated, Log: m.Log,
		})
	}
	return out, nil
}
