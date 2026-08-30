package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The checksum must come from the RELEASE and must name this exact asset.
//
// Pinned as a pair: the right asset resolves, and an asset the release does not
// carry is refused with a message that distinguishes "this platform is not in
// that release" from "the release is broken". Those are different answers and a
// person acts differently on each — one waits for a build, the other files a bug.
func TestPublishedSumNamesTheAssetOrSaysWhyNot(t *testing.T) {
	body := "aaaa  shabadoo-linux-amd64\nbbbb  shabadoo-darwin-arm64\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()

	got, err := publishedSum(context.Background(), srv.URL, "shabadoo-linux-amd64")
	if err != nil {
		t.Fatalf("a published asset must resolve: %v", err)
	}
	if got != "aaaa" {
		t.Fatalf("wrong checksum: %q", got)
	}
	// The one that must NOT silently succeed — and must not read as a broken
	// release either.
	_, err = publishedSum(context.Background(), srv.URL, "shabadoo-plan9-arm")
	if err == nil {
		t.Fatal("an asset absent from SHA256SUMS must be refused, not defaulted")
	}
	if !strings.Contains(err.Error(), "may not be in that release") {
		t.Fatalf("the refusal must separate 'no build for this platform' from "+
			"'the release is broken': %v", err)
	}
}

// A rate-limited GitHub must not read as a permissions problem. 403 is the
// status for both, and somebody told only "403" goes looking for an access
// problem they do not have — which costs an hour and finds nothing.
func TestRateLimitIsNotReportedAsPermissionDenied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	// latestTag builds its own URL, so the parsing is exercised through the
	// same shape rather than the real host.
	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("fixture wrong: %s", resp.Status)
	}
	// The message the user would see, asserted as text because that IS the
	// feature: the status code alone is the misleading part.
	msg := rateLimitHint(resp.Status)
	if !strings.Contains(msg, "rate limit") || !strings.Contains(msg, "not a permissions problem") {
		t.Fatalf("a 403 must be explained as a rate limit: %q", msg)
	}
}
