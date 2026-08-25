package main

import (
	"os"
	"path/filepath"
	"testing"

	"shabadoo/hub"
	"shabadoo/tmux"
)

// mkProject builds a directory tree under a temp root. A path ending in
// "/CLAUDE.md" writes that file; a path ending in "/.git" makes it a git root.
func mkProject(t *testing.T, paths map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range paths {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if filepath.Base(rel) == ".git" {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// The git qualifier is the whole rule. Without it a CLAUDE.md high in the tree —
// a shared workspace directory, a home directory — swallows every project
// beneath it, renaming things that work today and orphaning their mail.
func TestProjectRootPrefersAGitRoot(t *testing.T) {
	root := mkProject(t, map[string]string{
		// A workspace that describes itself but is nobody's project.
		"work/CLAUDE.md":                "# workspace",
		"work/proj/CLAUDE.md":           "# proj",
		"work/proj/.git":                "",
		"work/proj/hub/main.go":         "package hub",
		"work/loose/CLAUDE.md":          "# loose, no git",
		"work/loose/sub/x.go":           "package sub",
		"bare/deep/nested/nothing_here": "",

		// The case the git qualifier exists for: a marked directory nested
		// inside a marked GIT ROOT. Without the qualifier the inner one wins
		// and the project loses its parent; with it, the repository owns it.
		"mono/CLAUDE.md":     "# monorepo",
		"mono/.git":          "",
		"mono/pkg/CLAUDE.md": "# a package inside it, not its own repo",
	})

	for _, tc := range []struct{ dir, wantRoot, wantName string }{
		// A git root that marks itself wins, even though an ancestor is marked.
		{"work/proj", "work/proj", "proj"},
		// ...and a subfolder of it belongs to it, named through it.
		{"work/proj/hub", "work/proj", "proj/hub"},
		// No git root anywhere: fall back to the nearest CLAUDE.md.
		{"work/loose/sub", "work/loose", "loose/sub"},
		// Nothing marked at all: the directory is its own project.
		{"bare/deep/nested", "bare/deep/nested", "nested"},
		// A marked subdirectory of a marked git root belongs to the repository,
		// not to itself. This case fails without the qualifier.
		{"mono/pkg", "mono", "mono/pkg"},
	} {
		dir := filepath.Join(root, tc.dir)
		if got := projectRoot(dir); got != filepath.Join(root, tc.wantRoot) {
			t.Errorf("projectRoot(%s) = %s, want %s", tc.dir, got, tc.wantRoot)
		}
		if got := projectName(dir); got != tc.wantName {
			t.Errorf("projectName(%s) = %q, want %q", tc.dir, got, tc.wantName)
		}
	}
}

// Descriptions are read by something holding many at once, so the parser has to
// be boring: understand one shape, and leave everything else undescribed rather
// than guessing. A project with no frontmatter is the common case today and
// must not read as an error.
func TestProjectDescription(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"frontmatter", "---\ndescription: Use for the IPTV proxy\n---\n# iptv\n",
			"Use for the IPTV proxy"},
		{"quoted", "---\ndescription: \"Use for billing\"\n---\n", "Use for billing"},
		{"other keys around it", "---\nname: x\ndescription: Use for X\nversion: 1\n---\n",
			"Use for X"},
		{"colons in the value", "---\ndescription: Use for X: the thing\n---\n",
			"Use for X: the thing"},

		// Everything below leaves the project undescribed, which is the state
		// every project is in today.
		{"no frontmatter", "# just a heading\n", ""},
		{"frontmatter without the key", "---\nname: x\n---\n", ""},
		{"key appears after the block closes", "---\nname: x\n---\ndescription: too late\n", ""},
		{"empty file", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := mkProject(t, map[string]string{"p/CLAUDE.md": tc.body})
			if got := projectDescription(filepath.Join(root, "p")); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}

	// A folder with no CLAUDE.md at all must not error — enumeration walks
	// folders that were never projects.
	if got := projectDescription(t.TempDir()); got != "" {
		t.Errorf("missing CLAUDE.md returned %q", got)
	}
}

// Bounded, because a router holds every description at once. A project that
// writes an essay must not be able to crowd out its neighbours.
func TestDescriptionIsBounded(t *testing.T) {
	long := ""
	for len(long) < descriptionMax*2 {
		long += "words and more words "
	}
	root := mkProject(t, map[string]string{"p/CLAUDE.md": "---\ndescription: " + long + "\n---\n"})

	got := projectDescription(filepath.Join(root, "p"))
	if len(got) > descriptionMax+4 { // +4 for the ellipsis rune
		t.Errorf("description is %d bytes, want <= %d", len(got), descriptionMax)
	}
	if got == "" {
		t.Error("a long description was dropped entirely rather than trimmed")
	}
}

// The launcher's 8-hex suffix is what distinguishes a window this toolchain
// created from anything else someone is running in tmux. Keyed on the name
// rather than the pane command, because tmux misreports the command on at least
// one platform — a real Claude session on macOS reads as `2_1_220`.
func TestKindOf(t *testing.T) {
	claude := tmux.Window{Name: "iptv-wsl-10cac2b9", FriendlyName: "iptv-wsl", Path: "/w/iptv"}
	worker := tmux.Window{Name: "buildwatch", FriendlyName: "buildwatch", Path: "/w/build"}

	if got := kindOf(claude, ""); got != hub.KindClaude {
		t.Errorf("launcher-created window = %q, want %q", got, hub.KindClaude)
	}
	// The bug this fixes: `top` in a tmux window used to report itself as a
	// project called `buildwatch`.
	if got := kindOf(worker, ""); got != hub.KindWorker {
		t.Errorf("hand-started window = %q, want %q", got, hub.KindWorker)
	}

	// The node's own main project is neither: it is the machine's supervisor.
	core := tmux.Window{Name: "wsl-wsl-deadbeef", FriendlyName: "wsl-wsl", Path: t.TempDir()}
	if got := kindOf(core, core.Path); got != hub.KindCore {
		t.Errorf("core project window = %q, want %q", got, hub.KindCore)
	}
	// An empty core path must not make everything core.
	if got := kindOf(claude, ""); got == hub.KindCore {
		t.Error("an unset core path matched a window")
	}
}
