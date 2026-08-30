package main

// Reading a project's files, for a client that has no shell on that machine.
//
// Asked for to read the `docs/` libraries several projects now carry, which are
// reachable today only by being on the host. It rides the same read surface as
// the conversation reader.
//
// **The whole design is the confinement.** A file browser over an agent is
// otherwise arbitrary file read on somebody's machine, bounded by whatever a
// handler happens to check. So it is rooted at the PROJECTS THIS NODE ALREADY
// REPORTS: a project root is a first-class concept here, which turns an
// unbounded capability into an enumerable one. A path that does not resolve
// inside one of those roots is refused, and refusal is the default — every way
// out of the switch below that is not an explicit allow is a denial.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"shabadoo/tmux"
)

const (
	maxEntries  = 500       // per directory listing
	maxFileRead = 256 << 10 // bytes of a file returned in one response
)

// FileEntry is one directory member.
type FileEntry struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir,omitempty"`
	Size int64  `json:"size,omitempty"`
	Mod  int64  `json:"mod,omitempty"` // unix seconds
}

// FileListing is a directory, or a file's contents. One shape rather than two
// endpoints: a client walking a tree does not know which it is about to get
// until it asks, and making it guess produces a wrong guess.
type FileListing struct {
	Project string      `json:"project"`
	Root    string      `json:"root"`
	Path    string      `json:"path"` // relative to Root, "" for the root itself
	Dir     bool        `json:"dir"`
	Entries []FileEntry `json:"entries,omitempty"`

	// Text and its companions describe a FILE. Binary is reported rather than
	// sent: a client cannot render it and a response full of it would threaten
	// the 8 MB ceiling this crosses for nothing.
	Text      string `json:"text,omitempty"`
	Binary    bool   `json:"binary,omitempty"`
	Size      int64  `json:"size,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`

	// Elided counts directory members beyond the cap. Stated rather than
	// silently dropped: a listing that is short and says so is a different
	// answer from one that is complete.
	Elided int `json:"elided,omitempty"`
}

// projectRoots is every directory this node will serve, keyed by project name.
//
// Derived from the panes actually running here, which is what makes the set
// enumerable and self-limiting: a project nobody has open is not readable, and
// nothing here can name a directory the node is not already reporting.
func projectRoots(ctx context.Context) (map[string]string, error) {
	panes, err := tmux.Panes(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, p := range panes {
		if p.Path == "" {
			continue
		}
		root := projectRoot(p.Path)
		if root == "" {
			continue
		}
		out[projectName(root)] = root
	}
	return out, nil
}

// resolveInRoot joins a relative path to a root and proves the result is still
// inside it.
//
// Symlinks are resolved BEFORE the check, not after — a link inside the project
// pointing at /etc is the interesting case, and a purely lexical check passes it
// happily. `..` is handled by the same resolution rather than by string
// inspection, because rejecting the literal ".." is a filter somebody gets past
// and resolving the path is a fact.
func resolveInRoot(root, rel string) (string, error) {
	root = filepath.Clean(root)
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("project root is unreadable: %w", err)
	}
	joined := filepath.Join(realRoot, filepath.Clean("/"+rel))
	real, err := filepath.EvalSymlinks(joined)
	if err != nil {
		// A path that does not exist cannot be read, and saying which of "not
		// there" and "not allowed" applies would confirm the existence of files
		// outside the root. One answer for both.
		return "", fmt.Errorf("no such path in this project")
	}
	if real != realRoot && !strings.HasPrefix(real, realRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("path is outside the project")
	}
	return real, nil
}

// browse serves one directory or one file inside a named project.
func browse(ctx context.Context, project, rel string) (FileListing, error) {
	roots, err := projectRoots(ctx)
	if err != nil {
		return FileListing{}, err
	}
	root, ok := roots[project]
	if !ok {
		// Named rather than guessed, and the list is what this node actually
		// serves — so a caller can correct itself without a second round trip.
		names := make([]string, 0, len(roots))
		for n := range roots {
			names = append(names, n)
		}
		sort.Strings(names)
		return FileListing{}, fmt.Errorf("unknown project %q (this node serves: %s)",
			project, strings.Join(names, ", "))
	}
	real, err := resolveInRoot(root, rel)
	if err != nil {
		return FileListing{}, err
	}
	fi, err := os.Stat(real)
	if err != nil {
		return FileListing{}, fmt.Errorf("no such path in this project")
	}
	out := FileListing{Project: project, Root: root, Path: strings.TrimPrefix(rel, "/")}

	if fi.IsDir() {
		out.Dir = true
		ents, err := os.ReadDir(real)
		if err != nil {
			return FileListing{}, err
		}
		sort.Slice(ents, func(i, j int) bool {
			// Directories first, then by name — a tree is read top-down and a
			// mixed alphabetical list makes the reader do the sorting.
			if ents[i].IsDir() != ents[j].IsDir() {
				return ents[i].IsDir()
			}
			return ents[i].Name() < ents[j].Name()
		})
		for _, e := range ents {
			if len(out.Entries) >= maxEntries {
				out.Elided++
				continue
			}
			fe := FileEntry{Name: e.Name(), Dir: e.IsDir()}
			if info, err := e.Info(); err == nil {
				fe.Size, fe.Mod = info.Size(), info.ModTime().Unix()
			}
			out.Entries = append(out.Entries, fe)
		}
		return out, nil
	}

	out.Size = fi.Size()
	f, err := os.Open(real)
	if err != nil {
		return FileListing{}, err
	}
	defer f.Close()
	buf := make([]byte, maxFileRead)
	n, _ := f.Read(buf)
	buf = buf[:n]
	// Binary is REPORTED, never sent. A client cannot render it, and the bytes
	// would count against the ceiling this response crosses for nothing.
	if !utf8.Valid(buf) {
		out.Binary = true
		return out, nil
	}
	out.Text = string(buf)
	out.Truncated = fi.Size() > int64(n)
	return out, nil
}
