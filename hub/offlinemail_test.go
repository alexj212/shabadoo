package hub

import (
	"context"
	"testing"
	"time"
)

// A session whose agent is GONE must still be addressable by name, so its mail
// waits rather than being destroyed.
//
// This is the property the disconnect teardown used to break. It deleted a
// node's rows the moment its stream closed, so during a reconnect — which every
// coordinator upgrade causes, by design — a name-addressed send to a live peer
// resolved to nothing and was refused with the content DISCARDED. An
// id-addressed send survived, because an unmatched `claude-` id is passed
// through. The addressing form this project tells sessions to use was the lossy
// one.
//
// Pinned as a pair: a resolver that accepts anything would satisfy the first
// assertion while destroying the property that makes refusal safe, so a name
// that was never there must still bounce.
func TestOfflineSessionStaysAddressable(t *testing.T) {
	ctx, tn, now := context.Background(), testStore(t), time.Now()

	if err := tn.ReplaceAgentSessions(ctx, "wsl", []Session{
		{SessionID: "claude-devops-wsl-1", Agent: "wsl", Project: "devops", Alias: "devops-wsl"},
	}, now); err != nil {
		t.Fatal(err)
	}

	// No agent is connected in this fixture — exactly a node mid-reconnect.
	t.Run("a name on a disconnected node still resolves", func(t *testing.T) {
		got, err := tn.ResolveSession(ctx, "devops", now)
		if err != nil || got != "claude-devops-wsl-1" {
			t.Fatalf("name-addressed send to an offline session failed: got %q err=%v\n"+
				"this is the case that silently discarded mail during every upgrade", got, err)
		}
	})

	t.Run("and its mail is STORED, not refused", func(t *testing.T) {
		id, err := tn.Send(ctx, Envelope{
			FromSession: "claude-peer-1", ToSession: "claude-devops-wsl-1",
			Title: "handoff", Body: "work",
		}, now)
		if err != nil || id == "" {
			t.Fatalf("Send failed for an offline recipient: %v", err)
		}
		n, err := tn.Pending(ctx, "claude-devops-wsl-1", now)
		if err != nil || n != 1 {
			t.Fatalf("mail did not wait for the offline session: pending=%d err=%v", n, err)
		}
	})

	// The control: refusal still has teeth, or the fix above is just "accept
	// everything", which is the vanishing-mail failure it exists to prevent.
	t.Run("a name that never existed still bounces", func(t *testing.T) {
		if _, err := tn.ResolveSession(ctx, "no-such-project", now); err == nil {
			t.Error("an unknown name resolved — a typo must fail loudly")
		}
	})
}
