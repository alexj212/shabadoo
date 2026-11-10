package machoplist

import (
	"bytes"
	"debug/macho"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var plist = mustRead("Info.plist")

func mustRead(p string) []byte {
	b, err := os.ReadFile(p)
	if err != nil {
		panic(err)
	}
	return b
}

var (
	buildOnce sync.Once
	built     = map[string][]byte{}
	buildErr  error
)

// darwinBinary links a real program for darwin with Go's internal linker, the
// same way `make dist` does, so the fixture is the linker's output rather than
// a Mach-O written by the author of the code under test.
func darwinBinary(t *testing.T, arch string) []byte {
	t.Helper()
	if testing.Short() {
		t.Skip("links darwin binaries")
	}
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "machoplist")
		if err != nil {
			buildErr = err
			return
		}
		defer os.RemoveAll(dir)
		src := filepath.Join(dir, "main.go")
		os.WriteFile(src, []byte("package main\nfunc main() { println(\"hi\") }\n"), 0o644)
		os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module probe\n"), 0o644)
		for _, a := range []string{"arm64", "amd64"} {
			out := filepath.Join(dir, "probe-"+a)
			cmd := exec.Command("go", "build", "-o", out, ".")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GOOS=darwin", "GOARCH="+a, "CGO_ENABLED=0")
			if msg, err := cmd.CombinedOutput(); err != nil {
				buildErr = &buildError{string(msg)}
				return
			}
			built[a] = mustRead(out)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return built[arch]
}

type buildError struct{ msg string }

func (e *buildError) Error() string { return "go build: " + e.msg }

// The verifier has to agree with the Go linker before it can vouch for this
// package: an untouched arm64 build must verify, and the same build with one
// header byte changed must not. Without the second half, a Verify that accepts
// everything would pass every test below.
func TestVerifyAgreesWithTheLinkersSignature(t *testing.T) {
	bin := darwinBinary(t, "arm64")
	cd, err := codeDirectory(bin)
	if err != nil || cd == nil {
		t.Fatalf("the Go linker's arm64 output should carry an ad hoc signature: %v", err)
	}
	if err := Verify(bin); err != nil {
		t.Fatalf("untouched linker output does not verify: %v", err)
	}
	tampered := bytes.Clone(bin)
	tampered[headerSize64+le.Uint32(bin[20:])+40] = 1 // one byte of header padding
	if Verify(tampered) == nil {
		t.Fatal("a changed page still verifies")
	}
}

func TestEmbedAddsTheSectionAndKeepsTheSignatureValid(t *testing.T) {
	for _, arch := range []string{"arm64", "amd64"} {
		t.Run(arch, func(t *testing.T) {
			bin := darwinBinary(t, arch)
			out, err := Embed(bin, plist)
			if err != nil {
				t.Fatal(err)
			}
			if len(out) != len(bin) {
				t.Fatalf("file size changed: %d -> %d", len(bin), len(out))
			}
			if err := Verify(out); err != nil {
				t.Fatalf("signature after embedding: %v", err)
			}

			before, _ := macho.NewFile(bytes.NewReader(bin))
			after, err := macho.NewFile(bytes.NewReader(out))
			if err != nil {
				t.Fatalf("result does not parse as Mach-O: %v", err)
			}
			sec := after.Section("__info_plist")
			if sec == nil || sec.Seg != "__TEXT" {
				t.Fatal("no __TEXT,__info_plist section")
			}
			got, _ := sec.Data()
			if !bytes.Equal(got, plist) {
				t.Fatalf("section holds %q", got)
			}
			// Everything the program runs from is where it was.
			// Names repeat across segments (__rodata), so match on both.
			key := func(s *macho.Section) string { return s.Seg + "," + s.Name }
			moved := map[string]*macho.Section{}
			for _, s := range after.Sections {
				moved[key(s)] = s
			}
			for _, s := range before.Sections {
				a := moved[key(s)]
				if a == nil || a.Addr != s.Addr || a.Offset != s.Offset || a.Size != s.Size {
					t.Fatalf("section %s moved or vanished", key(s))
				}
			}
			if len(after.Sections) != len(before.Sections)+1 {
				t.Fatalf("sections: %d -> %d, want one more", len(before.Sections), len(after.Sections))
			}
			if len(after.Loads) != len(before.Loads) {
				t.Fatalf("load commands: %d -> %d", len(before.Loads), len(after.Loads))
			}
			if !strings.Contains(string(got), "NSLocalNetworkUsageDescription") {
				t.Fatal("the plist lost the key it exists for")
			}
		})
	}
}

func TestEmbedRefusesWhatItCannotDoCleanly(t *testing.T) {
	bin := darwinBinary(t, "arm64")
	once, err := Embed(bin, plist)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Embed(once, plist); err == nil {
		t.Error("embedding twice should be refused")
	}
	if _, err := Embed(bin, bytes.Repeat([]byte("x"), 8192)); err == nil {
		t.Error("a plist larger than the header space should be refused")
	}
	if _, err := Embed([]byte("#!/bin/sh\n"), plist); err == nil {
		t.Error("a non-Mach-O file should be refused")
	}
}
