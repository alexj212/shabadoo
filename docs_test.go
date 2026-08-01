package main

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The client spec is a contract with someone building against this coordinator
// from another machine, and a stale contract is worse than a missing one — they
// design around an absence that is no longer real.
//
// It went stale within hours of being written: `docs/mobile-client.md` still
// said push tokens and SSE "do not exist" after both had shipped that
// afternoon. A promise to keep it updated is exactly what failed, so this is a
// build failure instead — the same shape as serve_test.go pinning the fallback
// against the dashboard, and for the same reason: drift nobody can see until it
// costs someone a day.
func TestMobileSpecCoversHumanAPI(t *testing.T) {
	routes, err := os.ReadFile("hub/human.go")
	if err != nil {
		t.Fatalf("read routes: %v", err)
	}
	spec, err := os.ReadFile("docs/mobile-client.md")
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}

	// Only the human plane. The agent plane is spoken by this binary to itself
	// and is not something a client implements.
	re := regexp.MustCompile(`"(?:GET|POST|PUT|DELETE) (/api/[a-zA-Z0-9/{}_-]+)"`)
	seen := map[string]bool{}
	var missing []string
	for _, m := range re.FindAllStringSubmatch(string(routes), -1) {
		path := m[1]
		if seen[path] {
			continue
		}
		seen[path] = true
		if !strings.Contains(string(spec), path) {
			missing = append(missing, path)
		}
	}
	sort.Strings(missing)

	// A test that quietly extracts nothing passes forever. serve_test.go has
	// the same guard for the same reason.
	if len(seen) < 15 {
		t.Fatalf("only found %d endpoints in hub/human.go — the route style changed "+
			"and this test is no longer reading it", len(seen))
	}

	if len(missing) > 0 {
		t.Errorf("these human-plane endpoints are not mentioned in docs/mobile-client.md:\n  %s\n\n"+
			"Document them, or list them under \"Deliberately out of scope\" — but do not "+
			"leave a client author to find them by reading Go.", strings.Join(missing, "\n  "))
	}

	// The spec must not claim something is unbuilt when it is built. This is
	// the specific way it went stale, so it is the specific thing pinned.
	for _, claim := range []struct{ phrase, nowExists string }{
		{"`push_token` on a device | **not built", "PUT /api/devices/self/push"},
		{"SSE / websocket for the human plane | **not built", "GET /api/events"},
	} {
		if strings.Contains(string(spec), claim.phrase) {
			t.Errorf("the spec still says %q is unbuilt, but %s exists", claim.phrase, claim.nowExists)
		}
	}
}
