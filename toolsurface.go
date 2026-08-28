package main

// What tool surface a running `shabadoo mcp` child is actually serving.
//
// The old answer was "was it started before this binary was built", which is a
// clock standing in for a question the child can answer directly. It marked
// every session stale after ANY upgrade — measured at 11 of 11 and then 12 of
// 12 — including three releases in a row that did not touch `mcp.go` at all.
// A flag that is true of everything is not actionable, and advising a restart
// on it costs a session its context for nothing.
//
// So the child states its surface at startup and the detector compares strings.
// No clock anywhere in the comparison.
//
// The key is (pid, start time), not pid. Pids recycle: a record left by a dead
// child and inherited by an unrelated one with the same pid would report
// "current" for a process serving something else entirely — a manufactured
// clean tick, strictly worse than the "unknown" it should have been, because
// nothing downstream can tell it from a real one. Raised by the node that
// recycles pids across sleep/wake and would have produced it first.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrIndeterminate means a process's identity could not be established.
//
// Callers MUST map this to "unknown" — never to stale, never to current. A key
// derived from a wrong start time does not fail loudly; it manufactures a
// confident wrong answer, which is the failure this exists to prevent.
var ErrIndeterminate = errors.New("process start time indeterminate")

// toolSurfaceHash fingerprints what this build's MCP server advertises.
//
// Names, descriptions AND input schemas — not the name list. Names are the
// coarsest part of the surface: adding a parameter, widening an enum, or
// rewriting a description the model reads to decide WHEN to call leaves the
// names identical while genuinely changing what a session is serving. Changing
// one is also far more frequent than adding a tool. Raised in review, and the
// hash is named after what it covers because of it.
func toolSurfaceHash() string {
	tools := mcpTools()
	// json.Marshal of a struct slice is already deterministic in field order;
	// the slice order is fixed by the source. Sorting names would hide a
	// reordering that changes nothing, and cost the ability to notice one.
	raw, err := json.Marshal(tools)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// surfaceRecordDir is where a child leaves its note, beside the agent key.
func surfaceRecordDir(stateDir string) string {
	return filepath.Join(stateDir, "mcp")
}

func surfaceRecordName(pid int, token string) string {
	return fmt.Sprintf("%d.%s", pid, token)
}

// recordToolSurface is called by `shabadoo mcp` at startup.
//
// Best effort in every direction: a child that cannot write its record leaves
// itself unknown, which is the honest answer and strictly better than the
// alternative of assuming it is current.
func recordToolSurface(stateDir string) {
	pid := os.Getpid()
	token, err := processStartToken(pid)
	if err != nil {
		return
	}
	dir := surfaceRecordDir(stateDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, surfaceRecordName(pid, token)),
		[]byte(toolSurfaceHash()), 0o600)
	reapSurfaceRecords(dir)
}

// reapSurfaceRecords drops notes whose process is gone.
//
// Unambiguous only because the key carries the start time: a bare pid cannot
// distinguish a dead record from a live one without guessing.
func reapSurfaceRecords(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		pid, token, ok := parseSurfaceRecord(e.Name())
		if !ok {
			continue
		}
		if got, err := processStartToken(pid); err != nil || got != token {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func parseSurfaceRecord(name string) (int, string, bool) {
	i := strings.Index(name, ".")
	if i <= 0 || i == len(name)-1 {
		return 0, "", false
	}
	var pid int
	if _, err := fmt.Sscanf(name[:i], "%d", &pid); err != nil || pid <= 0 {
		return 0, "", false
	}
	return pid, name[i+1:], true
}

// toolState is what can be said about one MCP child.
type toolState int

const (
	// toolUnknown: no record, or its identity could not be established. It must
	// render distinctly from current — a check that cannot look is not a check
	// that found nothing wrong, and nobody investigates behind a clean answer.
	toolUnknown toolState = iota
	toolCurrent
	toolStale
)

// surfaceOf reports what a running child is serving, by reading its own note.
func surfaceOf(stateDir string, pid int, want string) toolState {
	token, err := processStartToken(pid)
	if err != nil {
		return toolUnknown
	}
	raw, err := os.ReadFile(filepath.Join(surfaceRecordDir(stateDir),
		surfaceRecordName(pid, token)))
	if err != nil {
		return toolUnknown // predates this mechanism, or could not be written
	}
	if strings.TrimSpace(string(raw)) == want {
		return toolCurrent
	}
	return toolStale
}

// defaultStateDir is where the agent key and socket live — the same directory,
// because a child's note about itself is the same trust boundary as the key:
// anyone who can read one can read the other.
func defaultStateDir() string {
	if v := os.Getenv("SHABADOO_SOCKET"); v != "" {
		return filepath.Dir(v)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return os.TempDir()
	}
	return filepath.Join(home, ".config", "shabadoo")
}
