package main

import "testing"

// A snapshot adds what is open and NEVER removes what is not.
//
// The axis varied is whether a folder is currently open; the fixture is held
// constant across both halves. Naming it matters because the destructive
// reading — "not open means drop it" — produces a plan that looks correct on
// the `add` side while quietly discarding lines somebody wrote a reason beside.
//
// The pair is required: a planner that returned an empty `add` would satisfy
// "nothing was removed" perfectly while doing nothing at all.
func TestSnapshotAddsWhatIsOpenAndRemovesNothing(t *testing.T) {
	open := map[string]bool{
		"/c/projects/shabadoo": true,
		"/c/projects/homelab":  true,
	}
	listed := []string{"/c/projects/homelab", "/c/projects/iptv"}

	add, extra := snapshotPlan(open, listed)

	if len(add) != 1 || add[0] != "/c/projects/shabadoo" {
		t.Errorf("add = %v, want the one open folder that was not listed", add)
	}
	if len(extra) != 1 || extra[0] != "/c/projects/iptv" {
		t.Errorf("extra = %v, want the listed folder that is not open — reported, not removed", extra)
	}
}

// The control for the assertion above: when everything open is already listed,
// the plan writes nothing. Without this, a planner that added every open folder
// unconditionally would pass the first test and churn the file on every run.
func TestSnapshotAddsNothingWhenAlreadyListed(t *testing.T) {
	open := map[string]bool{"/c/projects/homelab": true}
	add, extra := snapshotPlan(open, []string{"/c/projects/homelab"})
	if len(add) != 0 {
		t.Errorf("add = %v, want nothing: the folder is already listed", add)
	}
	if len(extra) != 0 {
		t.Errorf("extra = %v, want nothing: the listed folder is open", extra)
	}
}

// An empty boot list is the fresh-machine case and must record everything.
func TestSnapshotOnAnEmptyListRecordsEverythingOpen(t *testing.T) {
	open := map[string]bool{"/a": true, "/b": true}
	add, extra := snapshotPlan(open, nil)
	if len(add) != 2 {
		t.Errorf("add = %v, want both open folders", add)
	}
	if len(extra) != 0 {
		t.Errorf("extra = %v, want nothing", extra)
	}
	// Sorted, so a run-to-run diff of the file is reviewable rather than
	// reordered by Go's map iteration.
	if add[0] != "/a" || add[1] != "/b" {
		t.Errorf("add = %v, want a stable sorted order", add)
	}
}
