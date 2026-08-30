package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A project is a directory that SAYS it is one.
//
// `projectRoot` falls back to the directory itself when nothing above is
// marked, which is correct for naming a session and wrong for this command: it
// made `rules /tmp` announce a project called "tmp", inventing a layer that does
// not exist. Found by running it against a directory that is obviously not a
// project — the case a fixture built from real projects never covers.
//
// Pinned as the pair: a marked directory IS a project, an unmarked one is not,
// and a check that answers the same for both is answering nothing.
func TestOnlyAMarkedDirectoryIsAProject(t *testing.T) {
	base := t.TempDir()

	marked := filepath.Join(base, "real")
	if err := os.MkdirAll(marked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(marked, "CLAUDE.md"), []byte("# a project\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	plain := filepath.Join(base, "justadir")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}

	if !isProjectFor(marked) {
		t.Fatal("a directory holding CLAUDE.md is a project")
	}
	if isProjectFor(plain) {
		t.Fatal("an unmarked directory must not be reported as a project — " +
			"projectRoot falls back to the directory itself, which invents a layer")
	}
}

// isProjectFor mirrors what runRules decides, so the rule is testable without
// running a command that prints to stdout.
func isProjectFor(dir string) bool {
	root := projectRoot(dir)
	return root != "" && hasFile(filepath.Join(root, "CLAUDE.md"))
}
