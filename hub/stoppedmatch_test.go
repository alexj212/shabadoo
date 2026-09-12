package hub

import "testing"

// A recipient can be named three ways, and a SESSION ID must win outright.
//
// Pinned because the defect was a direction error that produced no error: the
// substring arm asked whether the project name contained the lookup, which is
// backwards for an id. Every assertion about names passed throughout; only the
// id case failed, and it failed silently by matching nothing at all.
func TestStoppedMatchResolvesIdsAndNames(t *testing.T) {
	f := folderView{Project: "devops", SessionID: "claude-devops-wsl-0bcb99a1"}

	t.Run("a session id is an exact match", func(t *testing.T) {
		e, p := stoppedMatch(f, "claude-devops-wsl-0bcb99a1")
		if !e || p {
			t.Errorf("id must match exactly, got exact=%v partial=%v — this is the "+
				"case every task-completion notice takes", e, p)
		}
	})
	t.Run("the project name is an exact match", func(t *testing.T) {
		if e, _ := stoppedMatch(f, "devops"); !e {
			t.Error("project name must still match exactly")
		}
	})
	t.Run("a substring of the name is partial, not exact", func(t *testing.T) {
		e, p := stoppedMatch(f, "dev")
		if e || !p {
			t.Errorf("substring should be partial, got exact=%v partial=%v", e, p)
		}
	})

	// The control. Without it every assertion above passes for a matcher that
	// says yes to everything, which is the other half of the same failure.
	t.Run("an unrelated name matches nothing", func(t *testing.T) {
		e, p := stoppedMatch(f, "iptv")
		if e || p {
			t.Errorf("unrelated name matched: exact=%v partial=%v", e, p)
		}
	})
	t.Run("an empty want matches nothing", func(t *testing.T) {
		if e, p := stoppedMatch(f, ""); e || p {
			t.Error("empty recipient matched something")
		}
	})
}
