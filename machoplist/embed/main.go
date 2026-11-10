// Command embed writes machoplist/Info.plist into a darwin build of shabadoo,
// in place. `make dist` runs it on each darwin binary; see package machoplist.
//
//	go run ./machoplist/embed machoplist/Info.plist dist/shabadoo-darwin-arm64
package main

import (
	"fmt"
	"os"

	"shabadoo/machoplist"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: embed Info.plist binary...")
		os.Exit(2)
	}
	plist, err := os.ReadFile(os.Args[1])
	if err != nil {
		fail(err)
	}
	for _, path := range os.Args[2:] {
		bin, err := os.ReadFile(path)
		if err != nil {
			fail(err)
		}
		out, err := machoplist.Embed(bin, plist)
		if err != nil {
			fail(fmt.Errorf("%s: %w", path, err))
		}
		// Check our own output before it can be published: a binary with a
		// broken signature cannot run on an arm64 Mac, and `upgrade` would
		// stop at its version check.
		if err := machoplist.Verify(out); err != nil {
			fail(fmt.Errorf("%s: signature does not verify after embedding: %w", path, err))
		}
		info, err := os.Stat(path)
		if err != nil {
			fail(err)
		}
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, out, info.Mode()); err != nil {
			fail(err)
		}
		if err := os.Rename(tmp, path); err != nil {
			fail(err)
		}
		fmt.Printf("  %s: embedded Info.plist (%d bytes)\n", path, len(plist))
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "embed:", err)
	os.Exit(1)
}
