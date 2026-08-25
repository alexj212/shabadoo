package main

// Project identity: which directory a session belongs to, what that project is
// called, and the one line it uses to say what it is for.
//
// A project is a directory — that is the whole model, and it is why there is no
// registry here to keep in sync. What this file adds is the ability to say
// *which* directory, once a session can be scoped into a subfolder: a session in
// `shabadoo/hub` belongs to shabadoo, and saying so is what lets mail addressed
// to `shabadoo` find it.
//
// See docs/direction.md.

import (
	"os"
	"path/filepath"
	"strings"
)

// descriptionMax bounds a project's self-description.
//
// Brevity is a constraint here rather than a preference: a router holds every
// project's description at once in order to decide where work goes, which is
// the entire reason this is separate from the body of the file it lives in.
const descriptionMax = 200

// projectRoot reports the directory that owns dir.
//
// The rule is the **nearest ancestor with a CLAUDE.md that is also a git root**,
// falling back to the nearest CLAUDE.md, and then to dir itself.
//
// The git qualifier is not tidiness. Without it the rule collapses on a real
// machine: a shared workspace directory and a home directory can each hold a
// CLAUDE.md while being nobody's project, and a plain nearest-CLAUDE.md rule
// would then re-root every sibling beneath them — renaming projects that work
// today, and orphaning any mail addressed to the old names.
//
// Verified against this machine's layout before it was written: every existing
// project keeps the name it already has.
func projectRoot(dir string) string {
	dir = filepath.Clean(dir)
	firstMarked := ""

	for p := dir; ; p = filepath.Dir(p) {
		if hasFile(filepath.Join(p, "CLAUDE.md")) {
			if firstMarked == "" {
				firstMarked = p
			}
			if isDir(filepath.Join(p, ".git")) || hasFile(filepath.Join(p, ".git")) {
				return p // a git root that also marks itself: the strong signal
			}
		}
		parent := filepath.Dir(p)
		if parent == p { // reached the filesystem root
			break
		}
	}

	if firstMarked != "" {
		return firstMarked
	}
	return dir
}

// projectName is how a session's project is addressed.
//
// The root's base name, plus the path from the root when a session is scoped
// into a subfolder — `shabadoo/hub` rather than a bare `hub`, which would be
// indistinguishable from any other project's `hub` and unreachable by
// addressing the project that owns it.
func projectName(dir string) string {
	root := projectRoot(dir)
	name := filepath.Base(root)

	rel, err := filepath.Rel(root, filepath.Clean(dir))
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return name
	}
	return name + "/" + filepath.ToSlash(rel)
}

// projectDescription reads a project's one-line self-description.
//
// It lives as frontmatter on the CLAUDE.md that already marks the project,
// which is the point: "is a project" and "can be routed to" become the same
// fact rather than two facts that can disagree. Nothing new to create, and
// nothing to keep in sync with the marker.
//
// Deliberately a strict, tiny parser rather than a YAML dependency. It reads a
// leading `---` block and one key, and anything it does not understand leaves
// the project undescribed — which is the state every project is in today, and
// therefore safe. A malformed file must never break enumeration for the folders
// around it.
func projectDescription(root string) string {
	body, err := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "" // no frontmatter: the common case, and not an error
	}

	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			break // end of frontmatter; the key was not here
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != "description" {
			continue
		}
		return clampDescription(val)
	}
	return ""
}

// clampDescription normalises and bounds a description value.
//
// Bounded because it is read by something holding many of them at once, and
// flattened because a value that arrives with a newline in it would break the
// one-line contract every reader is written against.
func clampDescription(v string) string {
	v = strings.TrimSpace(v)
	v = strings.Trim(v, `"'`)
	v = strings.Join(strings.Fields(v), " ")
	if len(v) > descriptionMax {
		v = strings.TrimSpace(v[:descriptionMax]) + "…"
	}
	return v
}

func hasFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}
