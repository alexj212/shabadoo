package hub

import "testing"

// An INITIAL page hands back both cursors; a cursored page hands back only the
// one that continues it.
//
// Pinned as a pair because the defect was an omission, and an omission passes
// every assertion that only checks what IS present. `TasksPage` set Tail
// correctly for twelve days while the handler dropped it, and nothing failed —
// the field was simply absent, which is indistinguishable from "this page has
// no tail" unless both cases are asserted.
func TestTaskPageCarriesTailOnlyOnAnInitialPage(t *testing.T) {
	t.Run("initial page carries the forward cursor", func(t *testing.T) {
		out := taskPageJSON([]Task{{ID: "a"}}, Page{Next: "n", Tail: "fwd"})
		if out["tail"] != "fwd" {
			t.Errorf("initial page dropped the forward cursor; a client cannot "+
				"mint one itself: %#v", out)
		}
		if out["next"] != "n" {
			t.Errorf("backward cursor missing: %#v", out)
		}
	})

	t.Run("a cursored page omits it rather than sending empty", func(t *testing.T) {
		out := taskPageJSON([]Task{{ID: "a"}}, Page{Next: "n"})
		if _, present := out["tail"]; present {
			t.Errorf("tail emitted on a continuation page; an empty string turns "+
				"'not applicable' into a value a client must interpret: %#v", out)
		}
	})

	// The same distinction for clamped, since it lives in the same map and was
	// the one conditional the original code did get right.
	t.Run("clamped behaves the same way", func(t *testing.T) {
		if _, present := taskPageJSON(nil, Page{Next: "n"})["clamped"]; present {
			t.Error("clamped emitted when the page was served whole")
		}
		if taskPageJSON(nil, Page{Next: "n", Clamped: "count"})["clamped"] != "count" {
			t.Error("clamped dropped when the page WAS cut")
		}
	})
}
