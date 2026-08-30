package main

// `shabadoo update` — replace this binary with the newest published release.
//
// Distinct from `upgrade`, and the distinction matters. `upgrade` is an
// OPERATOR telling their own coordinator to push a build they published to
// their own nodes; it needs a coordinator, an enrolled token and a fleet. This
// needs none of that: it is one person with one binary asking GitHub whether
// there is a newer one. Somebody handed a copy of this tool has no coordinator
// yet, and telling them to stand one up before they can update is the wrong
// order.
//
// It does NOT violate the project's no-network-install rule. That rule is about
// `setup` fetching a payload it should already carry — a fresh machine must
// bootstrap from one copied file. This is an existing, verified binary checking
// for its own successor, with the checksum published beside it and the staged
// build run before it is trusted. The same four checks `upgrade` makes, minus
// the ones that only make sense with a coordinator.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// releaseRepo is where the published binaries live. A constant rather than a
// flag: an update that can be pointed at an arbitrary host is a way to install
// somebody else's binary over this one, and nothing here needs that.
const releaseRepo = "alexj212/shabadoo"

func runUpdate(args []string) {
	fset := flag.NewFlagSet("update", flag.ExitOnError)
	check := fset.Bool("check", false, "report whether a newer release exists; change nothing")
	force := fset.Bool("force", false, "install even when the tag matches what is running")
	fset.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: shabadoo update [--check]

Replace this binary with the newest release published on GitHub. The download is
checksum-verified and the staged binary is run before the swap; the previous one
is kept alongside as .prev.

flags:
`)
		fset.PrintDefaults()
	}
	fset.Parse(args)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	latest, err := latestTag(ctx)
	if err != nil {
		fatalf("%v", err)
	}
	fmt.Printf("running %s%s\n", version, builtSuffix())
	fmt.Printf("latest  %s\n", latest)

	// EQUALITY, never ordering. `git describe` strings cannot be ordered — given
	// two of them there is no way to tell which came first — and this file is
	// not going to pretend otherwise. What GitHub's "latest release" means is
	// unambiguous, so the only question asked is "am I that one".
	if latest == version && !*force {
		fmt.Println("\nalready on the latest release")
		return
	}
	if *check {
		fmt.Printf("\na different release is published: %s\n", latest)
		fmt.Println("install it with: shabadoo update")
		return
	}

	self, err := os.Executable()
	if err != nil {
		fatalf("cannot locate this binary: %v", err)
	}
	if p, err := filepath.EvalSymlinks(self); err == nil {
		self = p // replace what the symlink points AT, not the link
	}

	asset := fmt.Sprintf("shabadoo-%s-%s", runtime.GOOS, runtime.GOARCH)
	base := fmt.Sprintf("https://github.com/%s/releases/download/%s", releaseRepo, latest)

	want, err := publishedSum(ctx, base+"/SHA256SUMS", asset)
	if err != nil {
		fatalf("%v", err)
	}

	// Staged in the SAME directory as the target: rename(2) is atomic only
	// within a filesystem, and copying instead would be the non-atomic write
	// this avoids.
	tmp, err := os.CreateTemp(filepath.Dir(self), ".shabadoo-update-*")
	if err != nil {
		fatalf("%v", err)
	}
	staged := tmp.Name()
	defer os.Remove(staged)

	fmt.Printf("\ndownloading %s %s…\n", asset, latest)
	sum, err := fetchTo(ctx, base+"/"+asset, tmp)
	tmp.Close()
	if err != nil {
		fatalf("download: %v", err)
	}
	if sum != want {
		fatalf("checksum mismatch: got %s, published %s — refusing to install",
			sum[:12], want[:12])
	}
	if err := os.Chmod(staged, 0o755); err != nil {
		fatalf("%v", err)
	}
	// Run it before trusting it. A checksum proves the bytes arrived intact; it
	// says nothing about whether they execute here. This is what catches a
	// wrong architecture and a not-this-program.
	if err := runsAndReports(ctx, staged, latest); err != nil {
		fatalf("the downloaded binary was rejected: %v", err)
	}

	prev := self + ".prev"
	_ = os.Remove(prev)
	if err := os.Link(self, prev); err != nil {
		_ = copyFileTo(self, prev)
	}
	if err := os.Rename(staged, self); err != nil {
		fatalf("install %s: %v", self, err)
	}
	fmt.Printf("installed %s at %s\n", latest, self)
	fmt.Printf("the previous build is kept at %s\n", prev)
	// Named because it is the one thing this cannot do for them: a running
	// service is still executing the old inode.
	fmt.Println("\nrestart anything long-running — an agent installed as a service is\n" +
		"still running the previous binary until it restarts.")
}

// latestTag asks GitHub which release is newest.
//
// Unauthenticated, because the repository is public and a token would be one
// more thing to rotate for a fact anybody can read — the same reasoning the CI
// watcher uses. The rate limit is 60/hour per address, which is generous for a
// command a person types and is worth naming when it bites, since "403" alone
// sends somebody looking for a permissions problem they do not have.
func latestTag(ctx context.Context) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", releaseRepo)
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("asking GitHub for the latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return "", fmt.Errorf("%s", rateLimitHint(resp.Status))
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub answered %s", resp.Status)
	}
	var out struct {
		Tag string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return "", fmt.Errorf("could not read GitHub's answer: %w", err)
	}
	if out.Tag == "" {
		return "", fmt.Errorf("GitHub named no release tag")
	}
	return out.Tag, nil
}

// publishedSum reads SHA256SUMS and returns the digest for one asset.
//
// The checksum comes from the RELEASE, not from the download: a digest computed
// over the bytes that just arrived proves only that they arrived intact, which
// is what a corrupted transfer already fails. It has to be published separately
// to mean anything.
func publishedSum(ctx context.Context, url, asset string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching SHA256SUMS: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("SHA256SUMS: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(body), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == asset {
			return f[0], nil
		}
	}
	return "", fmt.Errorf("no checksum published for %s — this platform may not be "+
		"in that release, which is different from the release being broken", asset)
}

func fetchTo(ctx context.Context, url string, w io.Writer) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: %s", url, resp.Status)
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(w, h), resp.Body); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// runsAndReports executes the staged binary and requires it to name the version
// that was asked for.
func runsAndReports(ctx context.Context, path, want string) error {
	cmd := exec.CommandContext(ctx, path, "version", "--json")
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("it did not run: %w", err)
	}
	var v struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return fmt.Errorf("it did not report a version: %w", err)
	}
	if v.Version != want {
		return fmt.Errorf("it reports %s, not %s", v.Version, want)
	}
	return nil
}

func copyFileTo(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// rateLimitHint explains a 403 from GitHub.
//
// The status is the same for "you are rate limited" and "you may not do that",
// and an unauthenticated caller on a public repository is never the second. A
// person shown only "403" goes looking for an access problem they do not have,
// which costs an hour and finds nothing.
func rateLimitHint(status string) string {
	return fmt.Sprintf("GitHub refused the request (%s) — on a public repository "+
		"this is the unauthenticated rate limit of 60/hour and not a permissions "+
		"problem; try again later", status)
}
