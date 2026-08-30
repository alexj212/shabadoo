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
	"os"
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
	//
	// AND THE ENTITLEMENTS ARE NOT OPTIONAL ONCE `--options runtime` IS SET.
	//
	// Hardened runtime is what makes macOS REQUIRE an entitlement before it will
	// even ask for a TCC-gated device. Without one the log reads "Prompting
	// policy for hardened runtime; service: kTCCServiceMicrophone requires
	// entitlement com.apple.security.device.audio-input but it is missing", and
	// then "Policy disallows prompt … access denied".
	//
	// That is the sharp end and it is worse than a denial: macOS does not refuse
	// after asking, it refuses to ASK. No dialog appears, nothing shows up to
	// toggle in System Settings, and every API on the path still returns
	// success while delivering silence. Reported by the meeting recorder, whose
	// helper measured 96,256 samples with one distinct value — all zero — where
	// a working run the night before had captured a mean of -42.6 dB.
	//
	// Anything shabadoo launches inherits this as the RESPONSIBLE process, so a
	// missing entitlement here removes microphone capture from every tool on the
	// machine, not just from this one. The reporter proved that by signing their
	// own helper three ways — with the entitlement, without hardened runtime,
	// and as shipped — and getting the identical dead result from all three: the
	// accessing process's signature is not the lever, the responsible process's
	// is.
	ent, cleanup, err := entitlementsFile()
	if err != nil {
		return "not signed: could not write entitlements: " + err.Error()
	}
	defer cleanup()

	cmd := exec.Command("codesign", "--force", "--sign", id,
		"--identifier", "com.github.alexj212.shabadoo",
		"--options", "runtime", "--entitlements", ent, path)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Sprintf("not signed: codesign failed (%v): %s",
			err, strings.TrimSpace(string(out)))
	}

	// READ THE ENTITLEMENTS BACK OFF THE BINARY. codesign exiting 0 says the
	// command ran, not that the blob attached — and the two are separable,
	// which is not a hypothetical: a Mac measured this exact binary at
	// v0.4.72 with a zero-byte entitlement blob while the signing step
	// reported success in words byte-identical to every signing before the
	// entitlements existed at all.
	//
	// There was no input that would have made that line red. It said the same
	// thing when it worked and when it did nothing, which is this project's
	// own named failure — nothing verifies itself — sitting one layer above
	// the fix rather than inside it.
	//
	// So the outcome NAMES what attached, and an empty blob is reported as a
	// failure even though codesign succeeded.
	got := readEntitlements(path)
	switch {
	case got == "":
		return "signed as com.github.alexj212.shabadoo with " + id +
			" BUT NO ENTITLEMENTS ATTACHED — TCC cannot raise a microphone " +
			"dialog for anything this binary launches; capture will fail silently"
	case !strings.Contains(got, "audio-input"):
		return "signed as com.github.alexj212.shabadoo with " + id +
			" but the audio-input entitlement is missing from the blob: " + got
	}
	return "signed as com.github.alexj212.shabadoo with " + id +
		"; entitlements attached: " + got
}

// readEntitlements returns the entitlement keys actually on the binary, or ""
// when there are none.
//
// The keys rather than the raw plist: the question a reader has is "did the one
// I need attach", and a wall of XML in a service log answers it less well than
// a list does.
func readEntitlements(path string) string {
	out, err := exec.Command("codesign", "-d", "--entitlements", "-", path).CombinedOutput()
	if err != nil {
		return ""
	}
	var keys []string
	for _, line := range strings.Split(string(out), "\n") {
		i := strings.Index(line, "com.apple.security.")
		if i < 0 {
			continue
		}
		k := line[i:]
		if j := strings.IndexAny(k, "<\" \t"); j > 0 {
			k = k[:j]
		}
		k = strings.TrimSuffix(k, "</key>")
		if k != "" && !contains(keys, k) {
			keys = append(keys, k)
		}
	}
	return strings.Join(keys, ", ")
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// entitlementsFile writes the plist codesign needs and returns a cleanup.
//
// Only the device entitlements, and only the two that a tool launched from here
// plausibly needs. An entitlement is a claim about what this program may do, so
// the list is short on purpose: adding one that is never used widens what a
// compromised binary could reach, for nothing.
//
// The camera is included alongside the microphone deliberately. It costs the
// same nothing today, and the failure it prevents is the one that just
// happened — a capability that cannot even ASK for consent, discovered only
// because somebody happened to be measuring samples rather than return codes.
func entitlementsFile() (string, func(), error) {
	const plist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>com.apple.security.device.audio-input</key>
	<true/>
	<key>com.apple.security.device.camera</key>
	<true/>
</dict>
</plist>
`
	f, err := os.CreateTemp("", "shabadoo-entitlements-*.plist")
	if err != nil {
		return "", func() {}, err
	}
	name := f.Name()
	cleanup := func() { os.Remove(name) }
	if _, err := f.WriteString(plist); err != nil {
		f.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return name, cleanup, nil
}
