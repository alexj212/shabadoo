package main

// Sessions that are deliberately not running.
//
// A session used to be running or nonexistent. When a window closed it vanished
// from the report, and the ten-minute watchdog reopened it — which defeats
// closing one to save resources, the thing this exists for.
//
// So an exit records intent, and every path that opens a window honours it.
// The state is a per-host file beside the boot list, which is the established
// place for a decision about what this machine runs: the boot list says what
// should start, this says what should not.
//
// See docs/direction.md and docs/build-plan.md (Phase 3).

import "path/filepath"

// deactivatedPath is the sibling of the boot folder list.
func deactivatedPath() string {
	return filepath.Join(filepath.Dir(bootListPath()), "deactivated")
}

// isDeactivated reports whether this folder was deliberately closed.
//
// Compared through symlinks, for the same reason folder discovery is: the boot
// list holds the path someone typed while tmux reports a resolved one, so a
// string match would miss and the machine would reopen a session somebody just
// closed.
func isDeactivated(cwd string) bool {
	if cwd == "" {
		return false
	}
	want := resolve(cwd)
	list, err := readBootList(deactivatedPath())
	if err != nil {
		return false // unreadable state is not evidence of intent
	}
	for _, line := range list {
		if resolve(expandHome(line)) == want {
			return true
		}
	}
	return false
}

// markDeactivated records that a folder's session was closed on purpose.
//
// Best effort: failing to write this means the watchdog reopens a session,
// which is today's behaviour and an annoyance rather than damage. It is not
// worth failing an agent's report over.
func markDeactivated(cwd string) {
	if cwd == "" || isDeactivated(cwd) {
		return
	}
	_ = appendBootList(deactivatedPath(), cwd)
}

// clearDeactivated forgets that intent, because something has now asked for
// this folder to run.
//
// Called from the paths that START a session, and that direction matters: `open`
// is an explicit request, so honouring a stale deactivation there would refuse
// to do the thing it was just asked to do, for a reason nobody can see. The
// file says "do not start this on your own", never "refuse to start this".
func clearDeactivated(cwd string) {
	if cwd == "" {
		return
	}
	_, _ = removeFromBootList(deactivatedPath(), cwd)
}
