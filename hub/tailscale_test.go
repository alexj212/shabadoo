package hub

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
)

func fakeWhois(id TailscaleIdentity, err error) WhoisFunc {
	return func(context.Context, string) (TailscaleIdentity, error) { return id, err }
}

func tsReq() *httptest.ResponseRecorder { return httptest.NewRecorder() }

// Membership is not authorization. A tailnet holds phones, TVs and family
// devices, and reaching this dashboard means driving panes running
// --dangerously-skip-permissions. Default-deny is the whole point, and
// "everyone on the tailnet" is the gate someone would reach for first.
func TestTailscaleIsDefaultDeny(t *testing.T) {
	alex := TailscaleIdentity{Login: "alex@example.com", Node: "wsl.tailnet.example.ts.net", Tailnet: "tailnet.example.ts.net"}

	t.Run("empty allowlist admits nobody", func(t *testing.T) {
		p := &TailscaleProvider{Whois: fakeWhois(alex, nil)}
		if _, err := p.Identify(httptest.NewRequest("GET", "/api/sessions", nil)); !errors.Is(err, ErrNotAllowed) {
			t.Fatalf("err = %v, want ErrNotAllowed", err)
		}
	})

	t.Run("a login not listed is refused", func(t *testing.T) {
		p := &TailscaleProvider{Allow: []string{"someone@example.com"}, Whois: fakeWhois(alex, nil)}
		if _, err := p.Identify(httptest.NewRequest("GET", "/", nil)); !errors.Is(err, ErrNotAllowed) {
			t.Fatalf("err = %v, want ErrNotAllowed", err)
		}
	})

	t.Run("a listed login is admitted, tenant from the tailnet", func(t *testing.T) {
		p := &TailscaleProvider{Allow: []string{" Alex@Example.com "}, Whois: fakeWhois(alex, nil)}
		id, err := p.Identify(httptest.NewRequest("GET", "/", nil))
		if err != nil {
			t.Fatalf("listed login refused: %v", err)
		}
		if id.Tenant != "tailnet.example.ts.net" {
			t.Errorf("tenant = %q, want the tailnet", id.Tenant)
		}
		if id.Sub != "tailscale:alex@example.com" || id.Label != "wsl.tailnet.example.ts.net" {
			t.Errorf("identity = %+v", id)
		}
		// Nothing of ours is expiring, so nothing should advertise a renewal.
		if !id.Expiry.IsZero() {
			t.Errorf("expiry set on a tailnet identity: %v", id.Expiry)
		}
	})

	t.Run("a tagged node is a service, not a person", func(t *testing.T) {
		// An agent container or CI runner has no login to attribute an action
		// to, and naming who did something is the audit log's whole job.
		svc := TailscaleIdentity{Login: "", Node: "ci.tailnet.example.ts.net",
			Tags: []string{"tag:ci"}, IsTagged: true}
		p := &TailscaleProvider{Allow: []string{"alex@example.com"}, Whois: fakeWhois(svc, nil)}
		if _, err := p.Identify(httptest.NewRequest("GET", "/", nil)); !errors.Is(err, ErrNotAllowed) {
			t.Fatalf("err = %v, want ErrNotAllowed", err)
		}
	})

	t.Run("a non-tailnet caller is refused", func(t *testing.T) {
		p := &TailscaleProvider{Allow: []string{"alex@example.com"},
			Whois: fakeWhois(TailscaleIdentity{}, ErrNotTailscale)}
		if _, err := p.Identify(httptest.NewRequest("GET", "/", nil)); !errors.Is(err, ErrNotTailscale) {
			t.Fatalf("err = %v, want ErrNotTailscale", err)
		}
	})
}

// The cache must not outlive an allowlist change by long, and must key on the
// address rather than caching a decision.
func TestTailscaleCacheExpires(t *testing.T) {
	calls := 0
	p := &TailscaleProvider{
		Allow: []string{"alex@example.com"},
		Whois: func(context.Context, string) (TailscaleIdentity, error) {
			calls++
			return TailscaleIdentity{Login: "alex@example.com", Node: "a.tailnet.ts.net"}, nil
		},
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "100.100.0.10:5555"

	for i := 0; i < 5; i++ {
		if _, err := p.Identify(r); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Errorf("whois called %d times for one address inside the TTL, want 1", calls)
	}

	// A different peer must not be served the first one's identity.
	other := httptest.NewRequest("GET", "/", nil)
	other.RemoteAddr = "100.99.1.2:5555"
	if _, err := p.Identify(other); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("a second address reused the first's cache entry (calls=%d)", calls)
	}
}

func TestTailnetOf(t *testing.T) {
	for in, want := range map[string]string{
		"wsl.tailnet.example.ts.net": "tailnet.example.ts.net",
		"a.b.c.ts.net":          "b.c.ts.net",
		"nodot":                 "",
		"":                      "",
	} {
		if got := tailnetOf(in); got != want {
			t.Errorf("tailnetOf(%q) = %q, want %q", in, got, want)
		}
	}
}
