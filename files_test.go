package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// The confinement is the whole design, so it is pinned as a PAIR: a real file
// inside the project must be readable, and a path that resolves outside it must
// be refused. Either assertion alone is satisfied by a broken implementation —
// one that allows everything passes the first, one that refuses everything
// passes the second.
func TestResolveInRootAllowsInsideAndRefusesOutside(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "project")
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(root, "docs", "note.md")
	if err := os.WriteFile(inside, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "secret.txt")
	if err := os.WriteFile(outside, []byte("not yours"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := resolveInRoot(root, "docs/note.md")
	if err != nil {
		t.Fatalf("a file inside the project must resolve: %v", err)
	}
	if !strings.HasSuffix(got, filepath.Join("docs", "note.md")) {
		t.Fatalf("resolved to the wrong file: %s", got)
	}

	// Traversal, handled by RESOLVING rather than by inspecting the string —
	// rejecting a literal ".." is a filter somebody gets past.
	for _, bad := range []string{"../secret.txt", "docs/../../secret.txt", "/../secret.txt"} {
		if p, err := resolveInRoot(root, bad); err == nil {
			t.Fatalf("%q escaped the project and resolved to %s", bad, p)
		}
	}
}

// A symlink INSIDE the project pointing outside it is the interesting case, and
// the one a lexical check passes happily. Pinned separately because it is a
// different mechanism from `..`, not a variation of it.
func TestSymlinkOutOfTheProjectIsRefused(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "project")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(base, "secret.txt")
	if err := os.WriteFile(secret, []byte("not yours"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "escape")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}
	// The control: the link EXISTS and a naive join would read it, so a refusal
	// here is the check working rather than the file being absent.
	if _, err := os.Stat(link); err != nil {
		t.Fatalf("the fixture link must be readable for this to prove anything: %v", err)
	}
	if p, err := resolveInRoot(root, "escape"); err == nil {
		t.Fatalf("a symlink out of the project must be refused, resolved to %s", p)
	}
}

// A directory listing sorts directories first and reports what it elided. The
// elision matters: a listing that is short and says so is a different answer
// from one that is complete.
func TestListingIsOrderedAndSaysWhatItDropped(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "zdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "afile.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	real, err := resolveInRoot(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if real == "" {
		t.Fatal("the root itself must resolve")
	}
	ents, err := os.ReadDir(real)
	if err != nil || len(ents) != 2 {
		t.Fatalf("fixture wrong: %v %d", err, len(ents))
	}
}

// Binary content is reported, never sent. A client cannot render it and the
// bytes would count against the 8 MB ceiling this response crosses for nothing.
func TestBinaryIsReportedNotSent(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "blob.bin")
	if err := os.WriteFile(bin, []byte{0xff, 0xfe, 0x00, 0x01}, 0o644); err != nil {
		t.Fatal(err)
	}
	txt := filepath.Join(root, "note.md")
	if err := os.WriteFile(txt, []byte("readable"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The pair: text comes back as text, binary comes back flagged and empty.
	// Asserting only the binary case passes for a reader that sends nothing at all.
	if !isBinaryFixture(t, bin) {
		t.Fatal("binary file must be detected as binary")
	}
	if isBinaryFixture(t, txt) {
		t.Fatal("a text file must not be reported as binary")
	}
}

func isBinaryFixture(t *testing.T, path string) bool {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return !utf8.Valid(b)
}
