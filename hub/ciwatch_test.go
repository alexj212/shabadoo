package hub

import "testing"

func run(sha, conclusion string) *ciRun {
	return &ciRun{Name: "ci", Status: "completed", HeadSHA: sha, Conclusion: conclusion}
}

// The whole value of this watcher is that it speaks rarely. A notifier that
// mostly cries wolf gets muted, which costs the one that mattered — the same
// argument blocked.go makes, reached independently for a third time here.
func TestCIWatcherIsEdgeTriggered(t *testing.T) {
	var tr ciTracker

	if !tr.shouldNotify(run("aaa1111", "failure")) {
		t.Fatal("a red build on the first poll must be announced: a coordinator " +
			"restarting into a broken build should say so, not wait for a commit")
	}
	if tr.shouldNotify(run("aaa1111", "failure")) {
		t.Error("the same failing run must not be announced twice — hourly polling " +
			"would otherwise notify forever while main is red")
	}
	if tr.shouldNotify(run("bbb2222", "failure")) {
		t.Error("a second consecutive failure is the same standing breakage, " +
			"not a new event")
	}
	if tr.shouldNotify(run("ccc3333", "success")) {
		t.Error("recovery must be silent: a green build is the expected state, and " +
			"saying so trains the reader to skim")
	}
	if !tr.shouldNotify(run("ddd4444", "failure")) {
		t.Error("breaking again after a green build is a NEW event and must be announced")
	}
}

// A run still in progress says nothing about the build yet, and reporting one
// as a conclusion would notify on every push.
func TestCIWatcherIgnoresIncompleteRuns(t *testing.T) {
	var tr ciTracker
	if tr.shouldNotify(&ciRun{Status: "in_progress", HeadSHA: "aaa1111"}) {
		t.Error("an in-progress run is not a result")
	}
	if tr.shouldNotify(nil) {
		t.Error("no runs at all is not a failure")
	}
	// Having ignored them, a real failure on the same commit must still land.
	if !tr.shouldNotify(run("aaa1111", "failure")) {
		t.Error("ignoring an incomplete run must not consume the edge")
	}
}

// `cancelled` and `timed_out` are not successes, and a build that never
// finished is worth the same message as one that failed.
func TestCIWatcherTreatsNonSuccessAsFailure(t *testing.T) {
	for _, c := range []string{"failure", "cancelled", "timed_out", "startup_failure"} {
		var tr ciTracker
		if !tr.shouldNotify(run("aaa1111", c)) {
			t.Errorf("conclusion %q must be reported", c)
		}
	}
}
