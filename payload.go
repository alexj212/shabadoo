package main

// Whether a node's installed ~/.claude matches the payload in its binary.
//
// `upgrade` replaces the binary and never runs the config step, so a node can
// carry a new skill INSIDE its binary while the old one sits on disk —
// indefinitely, with nothing anywhere reporting the difference. That is not
// theoretical: every payload release is followed by asking each machine to run
// `setup` by hand, and forgetting once leaves that node reading stale guidance
// with no signal, looking completely healthy.
//
// Same family as tools_stale, and reported the same way: detect here, let a
// human decide. The coordinator deliberately cannot run `setup` on a node —
// that writes into a directory the operator hand-edits and which setup is
// careful never to own.

import (
	"bytes"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// PayloadState is what this node can say about its own installed config.
//
// Known separates "nothing pending" from "could not tell", because a check that
// answers clean when it could not look is worse than no check: nobody looks
// behind a clean answer. Pending is meaningless unless Known.
type PayloadState struct {
	Known   bool `json:"payload_known"`
	Pending int  `json:"payload_pending"`

	// Drift NAMES the files rather than only counting them, because a count is
	// a question a reader can defer forever and a list is one they can act on —
	// the same reason the fleet view names projects with no MISSION.md instead
	// of tallying them.
	//
	// It is not decoration. A count of 1 stood on this node while the file
	// behind it was a skill whose vendored copy was three and a half months
	// stale, and nothing on any surface said WHICH file, so nobody looked.
	//
	// Capped, with the remainder counted: this rides every periodic report, and
	// a node whose whole ~/.claude differs must not make that report expensive
	// for the nodes beside it. Pending stays authoritative for the total.
	Drift []string `json:"payload_drift,omitempty"`
}

// maxDrift bounds the named list. Six is a wrap-up's worth: past that the answer
// is "your config is a long way behind" rather than a list anybody reads.
const maxDrift = 6

var payloadCache = struct {
	mu    sync.Mutex
	at    time.Time
	state PayloadState
}{}

// payloadScanEvery bounds the cost. This changes only when the binary is
// replaced or somebody edits ~/.claude, so re-reading a few dozen small files
// every five seconds to learn a fact that changes twice a week would be the
// same poor trade staleToolPanes avoids.
const payloadScanEvery = 5 * time.Minute

// payloadPending reports how many payload files differ from what is installed.
//
// It compares against the SAME merged payload `setup` would write — the merge
// is the contract, and computing it a second way here would let the report and
// the install drift apart, which is the failure this is meant to detect.
func payloadPending(claudeDir string) PayloadState {
	payloadCache.mu.Lock()
	defer payloadCache.mu.Unlock()
	if time.Since(payloadCache.at) < payloadScanEvery && payloadCache.at != (time.Time{}) {
		return payloadCache.state
	}
	st := scanPayload(claudeDir)
	payloadCache.at, payloadCache.state = time.Now(), st
	return st
}

func scanPayload(claudeDir string) PayloadState {
	if claudeDir == "" {
		return PayloadState{}
	}
	payload, err := mergePayloads()
	if err != nil {
		return PayloadState{} // cannot tell; must not read as clean
	}
	n := 0
	var drift []string
	for rel, want := range payload {
		got, err := os.ReadFile(filepath.Join(claudeDir, rel))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			n++ // absent: setup would install it
			drift = append(drift, rel)
		case err != nil:
			// A file we cannot read is a file we cannot compare. Unreadable is
			// not "the same", so this is the one case that gives up entirely
			// rather than guessing in either direction.
			return PayloadState{}
		case !bytes.Equal(got, want):
			n++
			drift = append(drift, rel)
		}
	}
	return PayloadState{Known: true, Pending: n, Drift: capDrift(drift, maxDrift)}
}

// defaultClaudeDir is where setup installs by default, resolved the same way.
// Duplicating the join rather than exporting setup's flag keeps the agent from
// depending on a flag set it never parses.
func defaultClaudeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// installPayload writes any payload file that differs, at agent startup.
//
// The node masters its own config. An `upgrade` already has this machine
// replace its own executable and exit non-zero so its supervisor restarts it —
// so the binary is new and the config beside it is not, purely because the two
// steps were split. Closing that here means an upgrade brings its guidance with
// it, and no coordinator has to reach into anybody's home directory.
//
// It reuses setup's own installFile, which is the whole point: same
// backup-before-replace, same skip-when-identical, same additive contract that
// never deletes a skill the operator added. Writing a second installer here
// would be a second set of rules for the same directory.
//
// Deliberately quiet when there is nothing to do — this runs on every agent
// start, and a line per file per restart would bury the one that matters.
// ensureShorthand keeps `shaba` pointing at the binary, on every agent start.
//
// `upgrade` replaces the binary and never runs setup, so a node upgraded rather
// than installed would never get the shorthand — the same split that left
// ~/.claude behind until the node started installing its own payload. Same
// place, same reason.
func ensureShorthand(binDir string) {
	if binDir == "" {
		return
	}
	link, target := filepath.Join(binDir, "shaba"), filepath.Join(binDir, "shabadoo")
	if got, err := os.Readlink(link); err == nil && got == target {
		return
	}
	if fi, err := os.Lstat(link); err == nil && fi.Mode()&fs.ModeSymlink == 0 {
		return // somebody's own file; not ours to replace
	}
	if _, err := os.Stat(target); err != nil {
		return // nothing to point at
	}
	_ = os.Remove(link)
	_ = os.Symlink(target, link)
}

func installPayload(claudeDir string) {
	if claudeDir == "" {
		return
	}
	if st := scanPayload(claudeDir); !st.Known || st.Pending == 0 {
		return
	}
	payload, err := mergePayloads()
	if err != nil {
		log.Printf("node: cannot read embedded config payload: %v", err)
		return
	}
	s := &setup{claudeDir: claudeDir, quiet: true}
	rels := make([]string, 0, len(payload))
	for rel := range payload {
		rels = append(rels, rel)
	}
	sort.Strings(rels)

	// The binary's payload is a SNAPSHOT, and it can be older than the disk.
	//
	// `make vendor` copies the live ~/.claude into the overlay by hand, so a
	// file edited after this binary was built is NEWER than the copy inside it —
	// and installing then reverts a real edit to a stale one, silently, on a
	// restart nobody connected to the change. Found with a skill corrected at
	// 12:32 today whose vendored copy was from 14 May: the next node restart
	// would have thrown away three and a half months of correction and reported
	// it as an install.
	//
	// So the direction is only assumed where it can be established. A file
	// modified after this build is left alone and counted.
	built := buildTimeOrZero()

	n, kept := 0, 0
	for _, rel := range rels {
		dst := filepath.Join(claudeDir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			log.Printf("node: config %s: %v", rel, err)
			continue
		}
		mode := fs.FileMode(0o644)
		if strings.HasSuffix(rel, ".sh") {
			mode = 0o755
		}
		before, _ := os.ReadFile(dst)
		if len(before) > 0 && !bytes.Equal(before, payload[rel]) && newerThanBuild(dst, built) {
			// Somebody edited this after this binary was built, so THIS COPY is
			// the stale one. An unstamped build cannot establish the order and
			// is treated as cannot-tell, which also skips: an automatic
			// overwrite of a hand-edited file is the wrong side to fail on.
			kept++
			continue
		}
		if err := s.installFile(dst, payload[rel], mode); err != nil {
			log.Printf("node: config %s: %v", rel, err)
			continue
		}
		if !bytes.Equal(before, payload[rel]) {
			n++
		}
	}
	if n > 0 {
		log.Printf("node: installed %d config file(s) from this build's payload "+
			"(replaced files were backed up alongside)", n)
	}
	if kept > 0 {
		// Said out loud. A skipped file and an installed one are both silent
		// otherwise, and the whole point is that somebody's edit survived —
		// which they should be able to see, and which tells them the binary's
		// copy is behind and wants `make vendor`.
		log.Printf("node: kept %d config file(s) that were edited after this build "+
			"(%s) — the payload copy is older; run `make vendor` to fold them in",
			kept, buildTime)
	}
	payloadCache.mu.Lock()
	payloadCache.at = time.Time{} // force a rescan, so the report reflects this
	payloadCache.mu.Unlock()
}

// buildTimeOrZero parses the build stamp, or reports zero when there is none.
//
// Zero means CANNOT TELL, never "the beginning of time" — a comparison against
// a zero time would make every file look newer and skip the whole payload,
// which is the same collapse of unknown into a value that this file exists to
// avoid elsewhere.
func buildTimeOrZero() time.Time {
	if buildTime == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, buildTime)
	if err != nil {
		return time.Time{}
	}
	return t
}

// newerThanBuild reports whether dst was modified after this binary was built.
//
// An unestablished build time answers TRUE — keep the file. The choice of
// default is the design: a false "newer" leaves a node running slightly stale
// guidance, which the payload_pending count already reports and a human can
// fix; a false "older" silently destroys an edit somebody made deliberately.
func newerThanBuild(dst string, built time.Time) bool {
	if built.IsZero() {
		return true
	}
	fi, err := os.Stat(dst)
	if err != nil {
		return false // no file: nothing to protect, install it
	}
	return fi.ModTime().After(built)
}

// capDrift sorts and bounds the named list.
//
// Its own function so the cap is testable without a payload large enough to
// trip it — this build embeds six files, so a test that waits for a seventh
// SKIPS, and a check that only ever skips is exactly as useless as one that
// only ever fails. Sorted because map iteration is random, and a list that
// reorders itself every five seconds reads as churn rather than as a fact.
//
// The COUNT is never capped: a reader told six when forty differ has been given
// a wrong number, not a short one.
func capDrift(list []string, max int) []string {
	sort.Strings(list)
	if len(list) > max {
		return list[:max]
	}
	return list
}
