package main

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"testing"
)

// Pane names are how the CLI is actually driven — window indices shift as
// sessions come and go and nobody remembers them. The matching rules are the
// folder rules, and the failure mode of getting them wrong is typing into the
// wrong project's Claude session.
func TestPaneMatching(t *testing.T) {
	nodes := []cliNode{{
		Node: "wsl",
		Sessions: []cliSession{
			{Alias: "homelab-wsl", TmuxSession: "claude", Index: 2, CWD: "/c/projects/homelab"},
			{Alias: "homelife-wsl", TmuxSession: "claude", Index: 4, CWD: "/c/projects/homelife"},
			{Alias: "iptv-wsl", TmuxSession: "claude", Index: 1, CWD: "/c/projects/iptv"},
			{Alias: "bin-wsl", TmuxSession: "claude", Index: 6, CWD: "/home/operator/bin"},
		},
	}, {
		Node:     "mac",
		Sessions: []cliSession{{Alias: "iptv-mac", TmuxSession: "claude", Index: 0, CWD: "/Users/a/iptv"}},
	}}

	for _, tc := range []struct {
		name, want, wantErr string
		node                string
	}{
		{name: "homelab-wsl", want: "wsl:2"},
		{name: "iptv", wantErr: "ambiguous"}, // two nodes have one
		{name: "iptv", node: "wsl", want: "wsl:1"},
		{name: "homel", wantErr: "ambiguous"},
		// The folder name matches, but the full path must not: every session on
		// a Linux host lives under /home/<user>, so matching the whole path
		// would make "home" permanently ambiguous.
		{name: "bin", want: "wsl:6"},
		{name: "operator", wantErr: "no session matching"},
		{name: "nope", wantErr: "no session matching"},
	} {
		got, err := matchPane(nodes, tc.node, tc.name)
		switch {
		case tc.wantErr != "":
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("match(%q, node=%q) err = %v, want %q", tc.name, tc.node, err, tc.wantErr)
			}
		case err != nil:
			t.Errorf("match(%q) unexpected error: %v", tc.name, err)
		default:
			if id := fmt.Sprintf("%s:%d", got.node, got.window); id != tc.want {
				t.Errorf("match(%q) = %s, want %s", tc.name, id, tc.want)
			}
		}
	}
}

// Go's flag package stops at the first non-flag argument, so a leading name
// silently swallowed every flag after it: `tail homelab --lines 4` looked for a
// session called "homelab --lines 4". Both orders have to work.
func TestNameAndFlags(t *testing.T) {
	for _, tc := range []struct {
		args      []string
		wantName  string
		wantLines int
	}{
		{[]string{"homelab", "--lines", "4"}, "homelab", 4},
		{[]string{"--lines", "4", "homelab"}, "homelab", 4},
		{[]string{"homelab"}, "homelab", 40},
		{[]string{}, "", 40},
	} {
		fset := flag.NewFlagSet("tail", flag.ContinueOnError)
		fset.SetOutput(io.Discard)
		lines := fset.Int("lines", 40, "")
		if name := nameAndFlags(fset, tc.args); name != tc.wantName {
			t.Errorf("nameAndFlags(%v) name = %q, want %q", tc.args, name, tc.wantName)
		} else if *lines != tc.wantLines {
			t.Errorf("nameAndFlags(%v) lines = %d, want %d", tc.args, *lines, tc.wantLines)
		}
	}
}

// A command taking a LIST must tolerate flags among the list. `upgrade mac
// --version probe1` parsed as three node names and reported
// `node "--version" is not connected`.
func TestArgsAndFlags(t *testing.T) {
	for _, tc := range []struct {
		args     []string
		wantPos  []string
		wantFlag string
	}{
		{[]string{"mac", "--version", "probe1"}, []string{"mac"}, "probe1"},
		{[]string{"--version", "probe1", "mac"}, []string{"mac"}, "probe1"},
		{[]string{"mac", "wsl"}, []string{"mac", "wsl"}, ""},
		{[]string{"mac", "--version", "v1", "wsl"}, []string{"mac", "wsl"}, "v1"},
		{[]string{}, nil, ""},
	} {
		fset := flag.NewFlagSet("upgrade", flag.ContinueOnError)
		fset.SetOutput(io.Discard)
		v := fset.String("version", "", "")
		pos := argsAndFlags(fset, tc.args)
		if strings.Join(pos, ",") != strings.Join(tc.wantPos, ",") {
			t.Errorf("argsAndFlags(%v) = %v, want %v", tc.args, pos, tc.wantPos)
		}
		if *v != tc.wantFlag {
			t.Errorf("argsAndFlags(%v) version = %q, want %q", tc.args, *v, tc.wantFlag)
		}
	}
}

func TestShortSession(t *testing.T) {
	for in, want := range map[string]string{
		"claude-shabadoo-wsl-1ef3aefe": "shabadoo-wsl",
		"human:cli@wsl (device abc)":   "cli@wsl (device abc)",
		"claude-x-mac-deadbeef":        "x-mac",
		"":                             "",
	} {
		if got := shortSession(in); got != want {
			t.Errorf("shortSession(%q) = %q, want %q", in, got, want)
		}
	}
}

// The version of a file about to be run on every node must come from the file,
// never from a guess. An earlier draft fell back to the filename plus this
// CLI's own version, which published a darwin build under whatever version
// happened to be in ~/bin — a binary labelled as something it is not.
func TestLdflagValue(t *testing.T) {
	for in, want := range map[string]string{
		`-ldflags="-X main.version=8205036 -X main.buildTime=2026-07-31T01:42:39-04:00"`: "8205036",
		`-ldflags="-X main.buildTime=2026-01-01T00:00:00Z -X main.version=abc123"`:       "abc123",
		`-ldflags="-X main.version=v1.2.3-dirty"`:                                        "v1.2.3-dirty",
		`-ldflags="-s -w"`:                                                               "",
		``:                                                                               "",
	} {
		if got := ldflagValue(in, "main.version"); got != want {
			t.Errorf("ldflagValue(%q) = %q, want %q", in, got, want)
		}
	}
}
