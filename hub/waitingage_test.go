package hub

import (
	"context"
	"testing"
	"time"
)

func sessWith(project string, w ...MissionWait) Session {
	return Session{SessionID: "s-" + project, Agent: "n1", Project: project,
		Kind: "claude", MissionStatus: "active", MissionWaiting: w}
}

func resolvedCount(t *testing.T, ten *Tenant) int {
	t.Helper()
	var n int
	if err := ten.s.db.QueryRow(
		`SELECT COUNT(*) FROM mission_resolved WHERE tenant = ?`, ten.id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A blocker keeps its FIRST sighting across reports. Without this the age resets
// every five seconds and the whole dimension is worthless — which is a state
// that looks identical to "this just appeared".
func TestWaitingKeepsItsFirstSighting(t *testing.T) {
	ten := testStore(t)
	ctx := context.Background()
	t0 := time.Unix(1_700_000_000, 0)
	row := MissionWait{Owner: "you", Item: "decide the thing"}

	for i := 0; i < 5; i++ {
		if err := ten.ReplaceAgentSessions(ctx, "n1",
			[]Session{sessWith("p", row)}, t0.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	ages, err := ten.waitingAges(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := ages["p\x00"+waitKey(row.Owner, row.Item)]
	if got != t0.Unix() {
		t.Errorf("first_seen = %d, want %d — the age reset on a later report, "+
			"which makes a three-day blocker indistinguishable from a new one", got, t0.Unix())
	}
}

// THE LOAD-BEARING DISTINCTION. A row vanishing because it was resolved and a
// row vanishing because its node went away are different facts, and recording
// the second as the first fills the trend history with fictional completions —
// worse than no history, because a duration built on it looks authoritative.
//
// Asserted as a PAIR: the resolved case must record, the silent case must not.
// Either alone passes for an implementation that always records, or never does.
func TestOnlyAReportingProjectCanResolveAnything(t *testing.T) {
	ctx := context.Background()
	t0 := time.Unix(1_700_000_000, 0)
	row := MissionWait{Owner: "you", Item: "decide the thing"}

	t.Run("project reports without the row: RESOLVED", func(t *testing.T) {
		ten := testStore(t)
		if err := ten.ReplaceAgentSessions(ctx, "n1", []Session{sessWith("p", row)}, t0); err != nil {
			t.Fatal(err)
		}
		if err := ten.ReplaceAgentSessions(ctx, "n1",
			[]Session{sessWith("p")}, t0.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		if n := resolvedCount(t, ten); n != 1 {
			t.Errorf("resolved = %d, want 1 — the project reported and no longer "+
				"lists the row, which is the only thing that means done", n)
		}
	})

	t.Run("project stops reporting entirely: NOT resolved", func(t *testing.T) {
		ten := testStore(t)
		if err := ten.ReplaceAgentSessions(ctx, "n1", []Session{sessWith("p", row)}, t0); err != nil {
			t.Fatal(err)
		}
		// The window closed, or the node dropped. Nothing was completed.
		if err := ten.ReplaceAgentSessions(ctx, "n1",
			[]Session{sessWith("other")}, t0.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		if n := resolvedCount(t, ten); n != 0 {
			t.Errorf("resolved = %d, want 0 — a project that stopped reporting "+
				"resolved nothing; counting it invents a completion that never "+
				"happened", n)
		}
		ages, err := ten.waitingAges(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := ages["p\x00"+waitKey(row.Owner, row.Item)]; !ok {
			t.Error("the row was dropped from the index; it is still outstanding, " +
				"just not currently visible")
		}
	})
}

// Reassigning a blocker restarts its clock, because it IS a new fact: the
// question "how long has mac had this" is not answered by when Alex had it.
func TestReassigningRestartsTheClock(t *testing.T) {
	ten := testStore(t)
	ctx := context.Background()
	t0 := time.Unix(1_700_000_000, 0)
	if err := ten.ReplaceAgentSessions(ctx, "n1",
		[]Session{sessWith("p", MissionWait{Owner: "you", Item: "same item"})}, t0); err != nil {
		t.Fatal(err)
	}
	later := t0.Add(time.Hour)
	if err := ten.ReplaceAgentSessions(ctx, "n1",
		[]Session{sessWith("p", MissionWait{Owner: "mac", Item: "same item"})}, later); err != nil {
		t.Fatal(err)
	}
	ages, err := ten.waitingAges(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := ages["p\x00"+waitKey("mac", "same item")]; got != later.Unix() {
		t.Errorf("reassigned row first_seen = %d, want %d", got, later.Unix())
	}
	if _, stale := ages["p\x00"+waitKey("you", "same item")]; stale {
		t.Error("the old assignment is still outstanding after being reassigned")
	}
}

// The age must reach the wire, which is the step mission_waiting itself skipped
// when it shipped parsed, documented, rendered and carried by nothing.
func TestSinceReachesListSessions(t *testing.T) {
	ten := testStore(t)
	ctx := context.Background()
	t0 := time.Unix(1_700_000_000, 0)
	row := MissionWait{Owner: "you", Item: "decide the thing"}
	if err := ten.ReplaceAgentSessions(ctx, "n1", []Session{sessWith("p", row)}, t0); err != nil {
		t.Fatal(err)
	}
	// A second report an hour later must NOT move it.
	later := t0.Add(time.Hour)
	if err := ten.ReplaceAgentSessions(ctx, "n1", []Session{sessWith("p", row)}, later); err != nil {
		t.Fatal(err)
	}
	out, err := ten.ListSessions(ctx, later)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || len(out[0].MissionWaiting) != 1 {
		t.Fatalf("unexpected sessions: %+v", out)
	}
	if got := out[0].MissionWaiting[0].Since; got != t0.Unix() {
		t.Errorf("Since = %d, want %d — an age that does not reach the wire is a "+
			"dimension the dashboard cannot rank by", got, t0.Unix())
	}
}
