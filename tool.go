package main

// Publishing a tool that is not shabadoo.
//
// The existing path publishes one binary per platform, which is true of this
// program and false of the tools it distributes. The meeting recorder is two
// files that must land together — a linux orchestrator and a Windows capture
// helper reached over interop — and installing one without the other leaves a
// tool that refuses to run and blames the missing half.
//
// It also cannot be cross-built. The helper needs MSVC on Windows or swiftc and
// a signing identity on macOS, so NO host can produce every set: this machine
// builds the Windows set, the Mac builds the darwin one. Publishing is
// therefore partial by design and merged over time, rather than one command run
// in one place.
//
// The manifest is the tool's own `version --json`, which it already emits into
// its dist directory. Reading what the tool says about itself beats inventing a
// second format that has to be kept in step with it.

import (
	"encoding/json"
	"net/url"
	"strings"
	"fmt"
	"os"
	"path/filepath"
)

// toolManifest is the shape `<tool> version --json` returns.
type toolManifest struct {
	Version    string `json:"version"`
	Built      string `json:"built"`
	Platform   string `json:"platform"`
	Components []struct {
		Name     string `json:"name"`
		Platform string `json:"platform"`
		Present  bool   `json:"present"`
	} `json:"components"`
}

// readToolManifest loads the set description from a dist directory.
func readToolManifest(dir string) (toolManifest, error) {
	var m toolManifest
	raw, err := os.ReadFile(filepath.Join(dir, "version.json"))
	if err != nil {
		return m, fmt.Errorf("no version.json in %s: a tool release is a SET, and "+
			"this file is what says which files belong to it. Produce it with "+
			"`<tool> version --json > %s/version.json`", dir, dir)
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, fmt.Errorf("version.json in %s is not readable: %w", dir, err)
	}
	if m.Version == "" || m.Platform == "" {
		return m, fmt.Errorf("version.json in %s has no version or platform", dir)
	}
	if len(m.Components) == 0 {
		return m, fmt.Errorf("version.json in %s lists no components; nothing to publish", dir)
	}
	// A component the tool itself says is absent must not be published as
	// though it were there. Half a set installs a tool that cannot run.
	for _, c := range m.Components {
		if !c.Present {
			return m, fmt.Errorf("component %q is marked not present in %s — the "+
				"set is incomplete and publishing it would put a broken install "+
				"on every node that took it", c.Name, dir)
		}
		if _, err := os.Stat(filepath.Join(dir, c.Name)); err != nil {
			return m, fmt.Errorf("version.json lists %q but %s does not contain it", c.Name, dir)
		}
	}
	return m, nil
}

// publishToolSet uploads every component of one tool's release, for one node
// platform.
//
// Partial by design. No host can build every set — the capture helpers need
// MSVC or swiftc and a signing identity — so this publishes what THIS machine
// could produce, and another machine publishes its own. A node whose platform
// has no set gets told exactly that, which is a different answer from being
// behind.
func publishToolSet(c *client, tool, dir string) {
	m, err := readToolManifest(dir)
	if err != nil {
		fatalf("%v", err)
	}
	fmt.Printf("%s %s — set for %s, %d component(s)\n", tool, m.Version, m.Platform, len(m.Components))

	for _, comp := range m.Components {
		f, err := os.Open(filepath.Join(dir, comp.Name))
		if err != nil {
			fatalf("reading %s: %v", comp.Name, err)
		}
		info, _ := f.Stat()
		// The component's OWN platform is recorded for the operator to read;
		// what it is published AGAINST is the node platform the set serves. A
		// linux node needs the windows helper, so both are filed under
		// linux/amd64 — the question a node asks is "what do I need".
		q := url.Values{
			"tool": {tool}, "component": {comp.Name},
			"version": {m.Version}, "platform": {m.Platform},
		}
		_, err = c.doStream("POST", "/api/releases?"+q.Encode(), f)
		f.Close()
		if err != nil {
			fatalf("publishing %s: %v", comp.Name, err)
		}
		size := int64(0)
		if info != nil {
			size = info.Size()
		}
		fmt.Printf("  published %-24s %-14s %d bytes\n", comp.Name, comp.Platform, size)
	}
}

// installToolOnNodes installs one tool's set on each node, one at a time.
//
// Serial for the same reason upgrading this binary is: a bad set reaching every
// machine simultaneously is the failure that costs an SSH to each of them, and
// dial-out agents exist so that is never necessary.
//
// A node with no set for its platform is reported and SKIPPED rather than
// failing the run. Not every host can build every set — a capture helper needs
// MSVC on Windows or swiftc and a signing identity on macOS — so a missing
// platform is an ordinary state that somebody with that machine has to fix,
// and it must read differently from an install that went wrong.
func installToolOnNodes(c *client, tool, version string, nodes []string) {
	failed, skipped := 0, 0
	for _, node := range nodes {
		fmt.Printf("%-8s %s", node, tool)
		body := map[string]any{"node": node, "tool": tool}
		if version != "" {
			body["version"] = version
		}
		raw, err := c.do("POST", "/api/upgrade", body)
		if err != nil {
			if strings.Contains(err.Error(), "no "+tool+" set published") {
				fmt.Printf("  — no set for that platform yet; somebody on one has to publish\n")
				skipped++
				continue
			}
			fmt.Printf("  FAILED: %v\n", err)
			failed++
			continue
		}
		var out struct {
			Version    string `json:"version"`
			Components int    `json:"components"`
		}
		_ = json.Unmarshal(raw, &out)
		fmt.Printf("  installed %s (%d component(s))\n", out.Version, out.Components)
	}
	if skipped > 0 {
		fmt.Printf("\n%d node(s) have no published set. That is not the same as being "+
			"behind: build one on a machine of that platform and publish it there.\n", skipped)
	}
	if failed > 0 {
		os.Exit(1)
	}
}
