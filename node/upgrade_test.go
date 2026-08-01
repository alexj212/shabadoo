package node

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A 401 on a report must tear the stream down so the outer loop re-logins.
//
// Without this the agent token's 24h expiry was invisible: the stream is
// authenticated once at connect, so a node whose token died kept looking
// online while every report silently 401'd and its session list froze.
func TestCredentialRejectionForcesReconnect(t *testing.T) {
	c := New(Config{Coord: "http://example.invalid", Node: "test"},
		func(context.Context, string, json.RawMessage) (any, error) { return nil, nil },
		func(context.Context) (any, error) { return nil, nil })

	select {
	case <-c.reauth:
		t.Fatal("reauth signalled before anything was rejected")
	default:
	}

	c.credentialRejected()
	select {
	case <-c.reauth:
	default:
		t.Fatal("credentialRejected did not signal")
	}

	// Repeated rejections must not block: many in-flight calls can notice the
	// same dead token at once, and none of them may stall on the channel.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			c.credentialRejected()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("credentialRejected blocked")
	}
}

// Only 401 triggers it. A 5xx is the coordinator having a bad moment, and
// re-authenticating on one would turn a blip into a reconnect storm across
// every node at once.
func TestOnlyUnauthorizedForcesReconnect(t *testing.T) {
	for _, code := range []int{http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusOK} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		}))

		c := New(Config{Coord: srv.URL, Node: "test"},
			func(context.Context, string, json.RawMessage) (any, error) { return nil, nil },
			func(context.Context) (any, error) { return map[string]any{"sessions": []any{}}, nil })
		c.token = "stale"
		c.report(context.Background())

		select {
		case <-c.reauth:
			t.Errorf("HTTP %d forced a reconnect; only 401 should", code)
		default:
		}
		srv.Close()
	}
}
