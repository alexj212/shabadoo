package hub

// Letting the tailnet say who you are.
//
// Every other human provider here has to establish identity from scratch —
// Cloudflare Access with a JWT, device tokens with a pairing code read out
// loud. But when the coordinator is reachable *only* over a tailnet, the
// network already knows: WireGuard has authenticated the peer before the first
// byte of HTTP arrives, and Tailscale can name the user behind it.
//
// That removes the worst part of onboarding. Device enrolment has a bootstrap
// paradox — minting a pairing code requires an enrolled credential, broken once
// by `--bootstrap` printing a code into the service log — and someone standing
// up their own coordinator meets that on day one. With tailnet identity the
// first user is simply identified.
//
// **Membership is not authorization.** A tailnet usually contains phones,
// TVs and family devices, and reaching this dashboard means driving every pane
// on every connected host — panes running `claude
// --dangerously-skip-permissions`. So this provider is DEFAULT-DENY: it admits
// only logins on an explicit allowlist. "Is on the tailnet" is the wrong gate,
// and it is the gate someone would reach for first.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// TailscaleIdentity is what a whois lookup resolves to.
type TailscaleIdentity struct {
	Login    string // "alex@example.com"
	Node     string // "wsl.tailnet-example.ts.net"
	Tailnet  string // "tailnet-example.ts.net" — the natural tenant key
	Tags     []string
	IsTagged bool
}

// WhoisFunc resolves a remote address to a tailnet identity.
//
// An indirection with exactly one purpose: the coordinator may reach Tailscale
// two ways, and which one is a deployment choice rather than a code change.
// Shelling out to `tailscale whois` costs nothing and works wherever the daemon
// is reachable (a host with Tailscale, or a container sharing the sidecar's
// socket). Embedding `tsnet` — so the binary IS a tailnet node — would swap in
// LocalClient.WhoIs here and touch nothing else, at the price of taking the
// module count from 30 to 547.
type WhoisFunc func(ctx context.Context, remoteAddr string) (TailscaleIdentity, error)

var (
	ErrNotTailscale = fmt.Errorf("caller is not a tailnet peer")
	ErrNotAllowed   = fmt.Errorf("tailnet identity is not on the allowlist")
)

// TailscaleProvider authenticates a request by asking the tailnet who is
// calling, then checking that login against an allowlist.
type TailscaleProvider struct {
	// Allow lists the logins that may use this coordinator. Empty admits
	// nobody: a provider that defaults to open would hand the dashboard to
	// every device on the tailnet the moment someone enabled it.
	Allow []string

	// Tenant for admitted users. Empty means the tailnet name, which is the
	// mapping the hosted model wants — one tailnet, one tenant.
	Tenant string

	// Whois defaults to the `tailscale` CLI.
	Whois WhoisFunc

	// TTL bounds the identity cache. Short: a login removed from the allowlist
	// should stop working promptly, and whois is a local call, not a network
	// round trip.
	TTL time.Duration

	mu       sync.Mutex
	cache    map[string]cachedWhois
	warnOnce sync.Once
}

type cachedWhois struct {
	id TailscaleIdentity
	at time.Time
}

const defaultWhoisTTL = 30 * time.Second

func (t *TailscaleProvider) Name() string { return "tailscale" }

// Identify satisfies IdentityProvider.
func (t *TailscaleProvider) Identify(r *http.Request) (Identity, error) {
	who, err := t.lookup(r.Context(), r.RemoteAddr)
	if err != nil {
		return Identity{}, err
	}

	// A tagged node is a service, not a person — an agent container, a CI
	// runner. It has no login to attribute an action to, and the audit log's
	// whole job is naming who did something.
	if who.IsTagged || who.Login == "" {
		return Identity{}, ErrNotAllowed
	}
	if !t.allowed(who.Login) {
		return Identity{}, ErrNotAllowed
	}

	tenant := t.Tenant
	if tenant == "" {
		tenant = who.Tailnet
	}
	return Identity{
		Tenant: tenant,
		Email:  who.Login,
		Sub:    "tailscale:" + who.Login,
		Label:  who.Node,
		// No Expiry: the tailnet re-authenticates continuously, so there is no
		// credential of ours counting down. Leaving it zero also keeps the
		// renewal header off responses that have nothing to renew.
	}, nil
}

func (t *TailscaleProvider) allowed(login string) bool {
	for _, a := range t.Allow {
		if strings.EqualFold(strings.TrimSpace(a), login) {
			return true
		}
	}
	return false
}

// proxyWarning fires once when requests are arriving from something that is not
// a tailnet address.
//
// This provider reads r.RemoteAddr and DELIBERATELY NOT X-Forwarded-For, which
// the rest of this package uses via clientKey. A forwarded header is a claim by
// whoever sent it: behind a shared reverse proxy — dm's Traefik sits on a
// Docker network with a dozen other containers — any of them could assert
// someone else's tailnet address and be authenticated as them, with full
// access to every pane on every host. Identity has to come from the peer this
// process is actually talking to.
//
// The consequence is that the provider is a no-op behind a proxy, and the
// symptom is "the flag is set and nothing happens". So say so, once.
func (t *TailscaleProvider) warnIfProxied(remoteAddr string) {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	// 100.64.0.0/10 is the CGNAT range Tailscale allocates from.
	if ip == nil || (ip.To4() != nil && ip.To4()[0] == 100 && ip.To4()[1] >= 64 && ip.To4()[1] <= 127) {
		return
	}
	t.warnOnce.Do(func() {
		log.Printf("WARNING: --tailscale-allow is set, but requests are arriving from %s, "+
			"which is not a tailnet address. This coordinator is almost certainly behind a "+
			"reverse proxy, and tailnet identity cannot work there: the proxy's address is "+
			"what this process sees, and trusting a forwarded header instead would let "+
			"anything that can reach the origin claim any identity. Bind a tailnet address "+
			"directly, or drop the flag.", host)
	})
}

func (t *TailscaleProvider) lookup(ctx context.Context, remoteAddr string) (TailscaleIdentity, error) {
	// Cache on the ADDRESS, not the identity: the expensive part is the lookup,
	// and an address changing hands within the TTL is a tailnet reassigning an
	// IP, which does not happen at that timescale.
	key, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		key = remoteAddr
	}
	ttl := t.TTL
	if ttl == 0 {
		ttl = defaultWhoisTTL
	}

	t.mu.Lock()
	if c, ok := t.cache[key]; ok && time.Since(c.at) < ttl {
		t.mu.Unlock()
		return c.id, nil
	}
	t.mu.Unlock()

	whois := t.Whois
	if whois == nil {
		whois = CLIWhois
	}
	id, err := whois(ctx, remoteAddr)
	if err != nil {
		t.warnIfProxied(remoteAddr)
		return TailscaleIdentity{}, err
	}

	t.mu.Lock()
	if t.cache == nil {
		t.cache = map[string]cachedWhois{}
	}
	t.cache[key] = cachedWhois{id: id, at: time.Now()}
	t.mu.Unlock()
	return id, nil
}

// CLIWhois asks the local tailscaled via the `tailscale` binary.
//
// A subprocess rather than a library, which is this codebase's existing answer
// to the same question — it shells out to `tmux` for every pane operation and
// to `tailscale ip -4` during setup. It costs nothing in go.mod, and go.mod is
// the thing worth protecting on a binary that drives shells.
func CLIWhois(ctx context.Context, remoteAddr string) (TailscaleIdentity, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "tailscale", "whois", "--json", remoteAddr).Output()
	if err != nil {
		// Covers both "not a tailnet address" and "tailscaled is not running".
		// Deliberately one error: a provider that distinguished them would be
		// telling an unauthenticated caller about the server's configuration.
		return TailscaleIdentity{}, ErrNotTailscale
	}

	var resp struct {
		Node struct {
			Name string   `json:"Name"`
			Tags []string `json:"Tags"`
		} `json:"Node"`
		UserProfile struct {
			LoginName string `json:"LoginName"`
		} `json:"UserProfile"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return TailscaleIdentity{}, ErrNotTailscale
	}

	return TailscaleIdentity{
		Login:    resp.UserProfile.LoginName,
		Node:     resp.Node.Name,
		Tailnet:  tailnetOf(resp.Node.Name),
		Tags:     resp.Node.Tags,
		IsTagged: len(resp.Node.Tags) > 0,
	}, nil
}

// tailnetOf strips the host label from a MagicDNS name: "wsl.tailnet-example.ts.net"
// is the node, "tailnet-example.ts.net" is the tailnet — and the tailnet is what a
// tenant is.
func tailnetOf(nodeName string) string {
	if i := strings.Index(nodeName, "."); i >= 0 {
		return strings.TrimSuffix(nodeName[i+1:], ".")
	}
	return ""
}
