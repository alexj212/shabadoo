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
}

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
	for rel, want := range payload {
		got, err := os.ReadFile(filepath.Join(claudeDir, rel))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			n++ // absent: setup would install it
		case err != nil:
			// A file we cannot read is a file we cannot compare. Unreadable is
			// not "the same", so this is the one case that gives up entirely
			// rather than guessing in either direction.
			return PayloadState{}
		case !bytes.Equal(got, want):
			n++
		}
	}
	return PayloadState{Known: true, Pending: n}
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

	n := 0
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
	payloadCache.mu.Lock()
	payloadCache.at = time.Time{} // force a rescan, so the report reflects this
	payloadCache.mu.Unlock()
}
