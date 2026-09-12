package hub

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A refusal must not read as authoritative about what exists.
//
// The index is a snapshot: DropAgentSessions deletes a node's rows the moment
// its agent disconnects, and a coordinator upgrade restarts every agent by
// design. So "known aliases: mac, minutes-mac" was true and complete about the
// index while being wrong about the fleet, and the reader nearly recorded a live
// peer as gone.
//
// Pinned as a pair: the refusal must carry the count AND must still resolve a
// name that IS present, or a message that simply always refuses passes the first
// assertion.
func TestRefusalStatesHowMuchItCouldSee(t *testing.T) {
	ctx, tn, now := context.Background(), testStore(t), time.Now()
	if err := tn.ReplaceAgentSessions(ctx, "mac", []Session{
		{SessionID: "claude-mac-1", Agent: "mac", Project: "mac", Alias: "mac"},
		{SessionID: "claude-minutes-mac-1", Agent: "mac", Project: "minutes", Alias: "minutes-mac"},
	}, now); err != nil {
		t.Fatal(err)
	}

	t.Run("an absent name is refused WITH the index size", func(t *testing.T) {
		_, err := tn.ResolveSession(ctx, "devops/missions/access-model", now)
		if err == nil {
			t.Fatal("a name that is not there must be refused")
		}
		msg := err.Error()
		if !strings.Contains(msg, "2 session(s)") {
			t.Errorf("refusal does not say how much it could see, so the alias "+
				"list reads as the fleet:\n%s", msg)
		}
		if !strings.Contains(msg, "incomplete") {
			t.Errorf("refusal does not warn it may be a partial snapshot:\n%s", msg)
		}
	})

	// The control. Without it, a resolver that refuses everything satisfies the
	// assertion above while being far worse than the bug.
	t.Run("a present name still resolves", func(t *testing.T) {
		got, err := tn.ResolveSession(ctx, "minutes-mac", now)
		if err != nil || got != "claude-minutes-mac-1" {
			t.Fatalf("a live alias must still resolve, got %q err=%v", got, err)
		}
	})
}
