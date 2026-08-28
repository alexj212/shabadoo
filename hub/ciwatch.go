package hub

// Telling somebody the build is broken.
//
// This exists because CI was red on main for days and nothing said so. Every
// local `make test` passed — the failing test was flaky, so a single run was
// green — and a red badge nobody looks at is indistinguishable from a red badge
// that is the known red. Four releases shipped over the top of it.
//
// The coordinator already notifies when a SESSION is blocked. Saying nothing
// when its own build is broken was the gap: the same person is on the other end
// of both, and one of them was reaching them.
//
// It is polled from here rather than pushed from CI because **a GitHub runner
// cannot reach the notifier**. The relay is a container hostname on the
// coordinator's own docker network; there is no route to it from outside, and
// exposing one to carry build notifications would be a poor trade. Polling
// inverts the direction and needs nothing opened.
//
// No credential either: the repository is public, so the Actions API answers
// unauthenticated. A token would be one more thing to rotate for a fact anybody
// can read.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// CIRepo is "owner/name". Empty disables the watcher entirely — the same shape
// as AppriseURL, and for the same reason: half a configuration should leave a
// feature off rather than half-on.
var CIRepo string

const ciTimeout = 15 * time.Second

// ciState is the last conclusion seen, so this is edge-triggered.
//
// Level-triggered would notify every hour for as long as main is broken, which
// is the shape that gets a notifier muted — and a muted notifier costs the one
// that mattered. Same argument as blockedRepeat, and the same conclusion
// reached three times now in this codebase.
type ciTracker struct {
	mu   sync.Mutex
	seen string // head SHA of the last run examined
	last string // its conclusion
}

var ciState ciTracker

// shouldNotify decides, and is separated from the fetch so the edge logic can
// be tested without a network. Every mistake in edge detection has the same
// shape, and it is not one anybody spots from a single observation — the same
// reason blocked.go's is pinned.
func (t *ciTracker) shouldNotify(run *ciRun) bool {
	if run == nil || run.Status != "completed" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	first := t.seen == ""
	changed := run.HeadSHA != t.seen
	was := t.last
	t.seen, t.last = run.HeadSHA, run.Conclusion

	if run.Conclusion == "success" {
		return false
	}
	if !changed {
		return false
	}
	// On the first poll after a restart there is no previous state, so a
	// standing failure is announced once. A coordinator restarting into a broken
	// build should say so rather than wait for the next commit to make it new.
	if !first && was == run.Conclusion {
		return false
	}
	return true
}

type ciRun struct {
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
	Status     string `json:"status"`
	HeadSHA    string `json:"head_sha"`
	HTMLURL    string `json:"html_url"`
	Event      string `json:"event"`
}

// CheckCI looks at the newest completed run on the default branch and notifies
// on a transition INTO failure.
//
// Deliberately silent on recovery. A green build is the expected state, and a
// notification saying so trains the reader to skim them — which is what makes
// the next real one invisible.
func (h *Hub) CheckCI(ctx context.Context) {
	if CIRepo == "" || AppriseURL == "" {
		return
	}
	run, err := latestRun(ctx, CIRepo)
	if err != nil {
		// A failed poll is not a failed build, and must never be reported as
		// one. GitHub being unreachable is this watcher's problem, not the
		// repository's — the distinction this codebase keeps having to make.
		return
	}
	if !ciState.shouldNotify(run) {
		return
	}
	sha := run.HeadSHA
	if len(sha) > 7 {
		sha = sha[:7]
	}
	if err := postApprise(ctx,
		fmt.Sprintf("CI %s on %s", run.Conclusion, CIRepo),
		fmt.Sprintf("%s — %s\n%s", run.Name, sha, run.HTMLURL),
		appriseAllTag, "failure"); err != nil {
		log.Printf("hub: ci notification: %v", err)
	}
}

// latestRun returns the newest completed run on the repository's default
// branch, or nil if there is none.
func latestRun(ctx context.Context, repo string) (*ciRun, error) {
	if !strings.Contains(repo, "/") {
		return nil, fmt.Errorf("repo must be owner/name, got %q", repo)
	}
	url := fmt.Sprintf(
		"https://api.github.com/repos/%s/actions/runs?per_page=1&status=completed&exclude_pull_requests=true",
		repo)
	c, cancel := context.WithTimeout(ctx, ciTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(c, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github: %s", resp.Status)
	}
	var body struct {
		Runs []ciRun `json:"workflow_runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	if len(body.Runs) == 0 {
		return nil, nil
	}
	return &body.Runs[0], nil
}
