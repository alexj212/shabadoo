package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// How long a blocker has been standing.
//
// This is the dimension the mission view was missing, and without it a list is
// not a priority: a row that appeared two minutes ago and one stuck for three
// days render identically, so a reader ranks by reading rather than by looking.
// The dialog queue has always had it — longest wait first — and nothing else did.
//
// Nothing in MISSION.md carries it and nothing should: asking a session to date
// each line would put the burden in the wrong place and it would rot. The
// coordinator already sees every project's rows every five seconds, so it can
// simply remember when each first appeared. Derived from a stream that already
// exists, requiring no file change and no agent change.

const (
	// resolvedRetention bounds the history. It is kept for trend — how long
	// things actually take — and a year of it would answer no question anyone
	// asks while growing the file every backup touches.
	resolvedRetention = 60 * 24 * time.Hour
)

// waitKey identifies one row within a project. Content-addressed for the same
// reason log ids are: rows are unordered and get rewritten, so position is not
// identity. Owner is included because reassigning a blocker IS a new fact — the
// clock should restart when work moves to somebody else.
func waitKey(owner, item string) string {
	sum := sha256.Sum256([]byte(owner + "\x00" + item))
	return hex.EncodeToString(sum[:8])
}

// reconcileWaiting records first-sightings and closes out rows that are gone.
//
// THE LOAD-BEARING RULE: a row is only resolved if its PROJECT still reported.
// A row also disappears when a node drops, a window closes, or a session is
// killed — none of which resolved anything. Recording those as resolutions would
// fill the trend history with fictional completions, and a duration statistic
// built on them is worse than none because it looks authoritative.
//
// So this reconciles ONLY within projects present in this report. A project that
// stopped reporting keeps its rows untouched, with last_seen where it was: still
// outstanding, and visibly not fresh.
func (t *Tenant) reconcileWaiting(ctx context.Context, tx execer, agent string, sessions []Session, now time.Time) error {
	ts := now.Unix()

	// Present rows, keyed by project. A project may appear on several panes; the
	// rows are the same file, so union rather than last-wins.
	present := map[string]map[string]MissionWait{}
	for _, s := range sessions {
		if s.Project == "" || len(s.MissionWaiting) == 0 {
			continue
		}
		byKey := present[s.Project]
		if byKey == nil {
			byKey = map[string]MissionWait{}
			present[s.Project] = byKey
		}
		for _, w := range s.MissionWaiting {
			byKey[waitKey(w.Owner, w.Item)] = w
		}
	}

	// A project that reported a mission but no waiting rows still reconciles —
	// that is precisely the "everything got resolved" case, and skipping it
	// would leave the last row of every project outstanding forever.
	for _, s := range sessions {
		if s.Project != "" && s.MissionStatus != "" {
			if _, ok := present[s.Project]; !ok {
				present[s.Project] = map[string]MissionWait{}
			}
		}
	}

	for project, rows := range present {
		for key, w := range rows {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO mission_waiting_seen
				       (tenant, agent, project, item_key, owner, item, first_seen, last_seen)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(tenant, agent, project, item_key) DO UPDATE SET
				  last_seen = excluded.last_seen, item = excluded.item`,
				t.id, agent, project, key, w.Owner, w.Item, ts, ts); err != nil {
				return err
			}
		}

		// Close out what this project no longer lists, as INSERT…SELECT then
		// DELETE rather than read-modify-write: no round trip, and the two
		// statements cannot disagree about which rows they moved.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO mission_resolved
			       (tenant, agent, project, owner, item, first_seen, resolved_at)
			SELECT tenant, agent, project, owner, item, first_seen, ?
			  FROM mission_waiting_seen
			 WHERE tenant = ? AND agent = ? AND project = ? AND last_seen < ?`,
			ts, t.id, agent, project, ts); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM mission_waiting_seen
			 WHERE tenant = ? AND agent = ? AND project = ? AND last_seen < ?`,
			t.id, agent, project, ts); err != nil {
			return err
		}
	}

	_, err := tx.ExecContext(ctx,
		`DELETE FROM mission_resolved WHERE tenant = ? AND resolved_at < ?`,
		t.id, now.Add(-resolvedRetention).Unix())
	return err
}

// waitingAges returns first_seen per (project, item key) for this tenant.
func (t *Tenant) waitingAges(ctx context.Context) (map[string]int64, error) {
	rows, err := t.s.db.QueryContext(ctx,
		`SELECT project, item_key, first_seen FROM mission_waiting_seen WHERE tenant = ?`, t.id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var project, key string
		var first int64
		if err := rows.Scan(&project, &key, &first); err != nil {
			return nil, err
		}
		out[project+"\x00"+key] = first
	}
	return out, rows.Err()
}
