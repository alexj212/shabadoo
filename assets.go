package main

import "embed"

// The binary carries everything `shabadoo setup` installs, so a single
// copied file can bootstrap a machine with no network and no source tree.

// Listed explicitly rather than embedding the directory: a file that is not
// named here is silently absent from the binary, and finding that out at
// runtime (a 404 on a page that exists on disk) is worse than editing this
// line when a page is added.
//
//go:embed static/index.html static/pair.html
var staticFS embed.FS

// configFS is the PORTABLE payload: the half of ~/.claude that is the same on
// every machine — behavioural guidance, a generic settings.json, and skills
// that do not name anyone's infrastructure. It is committed, and it is safe to
// publish.
//
// `all:` so dot-prefixed files inside skills are kept; the default embed
// pattern silently drops them.
//
//go:embed all:config
var configFS embed.FS

// localFS is the PERSONAL overlay: one operator's real ~/.claude, populated by
// `make vendor` and **gitignored**.
//
// The split exists because the payload was one person's config with their
// tailnet hostnames, LAN addresses and six infrastructure skills in it — a
// feature for them (one binary carries their whole setup to a new machine) and
// a non-starter for anyone else, and the reason this repo could not be
// published. Scrubbing would not have held: `make vendor` is a straight copy
// and would undo it on the next run. Keeping the personal half in a tree git
// never sees is what makes it stick.
//
// A fresh clone has only `.gitkeep` here, so it builds and ships the portable
// payload alone. Nothing is lost for the operator: `make vendor` refills it and
// the binary carries everything it did before.
//
//go:embed all:config.local
var localFS embed.FS
