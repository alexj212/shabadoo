//go:build darwin

package main

// Signing this binary on the machine it lands on.
//
// macOS records a TCC permission grant against a binary's DESIGNATED
// REQUIREMENT. For an ad-hoc signed binary that requirement is a bare hash of
// the bytes, so **every release silently revokes every grant** — the human said
// yes once, and the next upgrade makes that yes apply to a binary that no
// longer exists.
//
// Measured, not theorised. A peer lost a twelve-minute recording run to it: the
// grant was given against v0.4.31 and the run happened on v0.4.39, eight
// releases later, so a permission dialog appeared with nobody at the screen and
// a capture helper was killed by a timeout. It failed as "the capture helper
// produced no report", which reads as that project's bug — they only knew
// otherwise from two days spent in the TCC log. The failure scales the wrong
// way: the more often this ships, the more often every TCC-gated capability on
// every Mac is revoked.
//
// It cannot be fixed at build time. Darwin binaries here are CROSS-COMPILED on
// linux and `codesign` is a macOS-only tool, so the only moment this can be
// signed is on the machine it arrives at — which is also the only machine that
// has an identity to sign it with.
//
// A real identity produces a requirement naming the identifier and the
// certificate, which survives every subsequent rebuild. That is the whole point:
// the grant stops being a fact about some bytes and becomes a fact about this
// program.

import (
	"fmt"
	"os/exec"
	"strings"
)

// signingIdentity is what `codesign` is asked to sign as.
//
// Discovered rather than hardcoded: a hardcoded one is right on the machine it
// was written on and wrong everywhere else, which is the shape this codebase
// keeps meeting.
func signingIdentity() (string, bool) {
	out, err := exec.Command("security", "find-identity", "-v", "-p", "codesigning").Output()
	if err != nil {
		return "", false
	}
	// Prefer a Developer ID (distributable) over an Apple Development cert, but
	// take either — the requirement is stable under rebuilds with both, which is
	// the property that matters here.
	var dev, appleDev string
	for _, line := range strings.Split(string(out), "\n") {
		i, j := strings.Index(line, `"`), strings.LastIndex(line, `"`)
		if i < 0 || j <= i {
			continue
		}
		name := line[i+1 : j]
		switch {
		case strings.HasPrefix(name, "Developer ID Application"):
			dev = name
		case strings.HasPrefix(name, "Apple Development"), strings.HasPrefix(name, "Mac Developer"):
			if appleDev == "" {
				appleDev = name
			}
		}
	}
	if dev != "" {
		return dev, true
	}
	return appleDev, appleDev != ""
}

// signSelf signs the binary at path so its TCC grants survive the next upgrade.
//
// Returns a human-readable outcome rather than an error: a binary that could not
// be signed still works, and failing an upgrade over it would trade a permission
// prompt for an unusable node. But it must SAY so, because the alternative is
// the silence that cost somebody a measurement run.
func signSelf(path string) string {
	id, ok := signingIdentity()
	if !ok {
		return "not signed: no codesigning identity on this machine, so macOS " +
			"permission grants will be revoked by the next upgrade"
	}
	// --identifier matters as much as the signature. Without it the identifier
	// is the linker default `a.out`, which is both what the requirement names
	// and what a person sees in System Settings.
	//
	// It must also never change. The designated requirement names it, so
	// renaming later revokes every grant exactly as ad-hoc signing does — which
	// is the bug this exists to fix. Derived from the public repository rather
	// than a private domain: a bundle identifier is visible in System Settings
	// and in any signature anyone inspects, and the publish guard refused the
	// first choice for precisely that reason.
	cmd := exec.Command("codesign", "--force", "--sign", id,
		"--identifier", "com.github.alexj212.shabadoo", "--options", "runtime", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Sprintf("not signed: codesign failed (%v): %s",
			err, strings.TrimSpace(string(out)))
	}
	return "signed as com.github.alexj212.shabadoo with " + id
}
