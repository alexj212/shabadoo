package hub

// The human plane: Cloudflare Access authenticates the browser, and this file
// verifies its assertion.
//
// Read the warning on Middleware before changing anything here. Verifying the
// JWT is only half the control — the other half is that the origin must be
// unreachable except through the tunnel that sets it.

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// accessHeader is set by Cloudflare Access on every proxied request. It is
	// trustworthy only because the origin refuses connections that did not come
	// through the tunnel — on its own the header is attacker-controlled.
	accessHeader = "Cf-Access-Jwt-Assertion"

	// jwksTTL bounds how long a fetched key set is reused. Cloudflare rotates
	// signing keys; too long a TTL means a rotated-out key keeps validating.
	jwksTTL = 30 * time.Minute

	// clockSkew tolerates small disagreement between Cloudflare's clock and ours
	// on exp/nbf/iat.
	clockSkew = 60 * time.Second
)

var (
	ErrNoAssertion  = errors.New("no Cloudflare Access assertion on request")
	ErrBadAssertion = errors.New("Access assertion failed verification")
)

// Identity is the human behind a verified request.
type Identity struct {
	Tenant string // which tenant's data this human may see
	Email  string
	Sub    string
	Expiry time.Time

	// Label names this specific credential for the audit log — a device's
	// label, empty for identity providers where the email already identifies
	// the actor.
	//
	// It exists because Email does NOT distinguish devices: a device inherits
	// the email of whoever minted its pairing code, so every device enrolled
	// from the same chain shares one. Attributing the audit log to Email alone
	// meant three enrolled devices all appeared as `bootstrap@default`, and the
	// log could not answer the only question it is for: which of them did this.
	Label string

	// Scope limits what this credential may do. Empty means full access, so a
	// provider that knows nothing about scopes (Access, network trust) keeps
	// working unchanged and nothing has to opt in to being powerful.
	//
	// ScopeRead is the interesting one: a phone that can only read cannot type
	// into a live Claude session, which makes developing a client against the
	// real coordinator safe rather than nerve-wracking.
	Scope string
}

// Credential scopes. Deliberately two values, not a permission system: the
// distinction that matters is "can this thing drive a pane".
const (
	ScopeFull = ""     // everything, the default
	ScopeRead = "read" // GETs, plus renewing its own token
)

// AccessVerifier validates Cloudflare Access JWTs against the team's published
// signing keys.
type AccessVerifier struct {
	teamDomain string // e.g. "example.cloudflareaccess.com"
	audience   string // the Access application's AUD tag
	tenant     string // tenant every admitted human belongs to
	client     *http.Client

	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
}

// NewAccessVerifier builds a verifier for one Access application.
//
// audience is the application's AUD tag, and it is not optional: the team's
// signing key is shared by every Access app on the account, so a JWT minted for
// a *different* app on the same team verifies cryptographically. Without the
// audience check, anyone who can reach any of the team's Access apps can mint a
// token this service accepts.
func NewAccessVerifier(teamDomain, audience string) (*AccessVerifier, error) {
	teamDomain = strings.TrimSpace(teamDomain)
	audience = strings.TrimSpace(audience)
	if teamDomain == "" {
		return nil, errors.New("access: team domain required")
	}
	if audience == "" {
		return nil, errors.New("access: application audience (AUD) required")
	}
	return &AccessVerifier{
		teamDomain: teamDomain,
		audience:   audience,
		tenant:     DefaultTenant,
		client:     &http.Client{Timeout: 10 * time.Second},
		keys:       map[string]*rsa.PublicKey{},
	}, nil
}

// jwks is the subset of Cloudflare's key document we read.
type jwks struct {
	Keys []struct {
		Kid string `json:"kid"`
		Kty string `json:"kty"`
		Alg string `json:"alg"`
		N   string `json:"n"`
		E   string `json:"e"`
	} `json:"keys"`
}

func (v *AccessVerifier) certsURL() string {
	return "https://" + v.teamDomain + "/cdn-cgi/access/certs"
}

func (v *AccessVerifier) issuer() string {
	return "https://" + v.teamDomain
}

// refresh fetches the key set if the cache is cold or stale.
func (v *AccessVerifier) refresh(ctx context.Context, now time.Time) error {
	v.mu.Lock()
	fresh := now.Sub(v.fetched) < jwksTTL && len(v.keys) > 0
	v.mu.Unlock()
	if fresh {
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.certsURL(), nil)
	if err != nil {
		return err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("access: fetch certs: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("access: certs endpoint returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}

	keys, err := parseJWKS(body)
	if err != nil {
		return err
	}

	v.mu.Lock()
	v.keys = keys
	v.fetched = now
	v.mu.Unlock()
	return nil
}

func parseJWKS(body []byte) (map[string]*rsa.PublicKey, error) {
	var doc jwks
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("access: parse certs: %w", err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range doc.Keys {
		// Only RSA keys are usable for the RS256 assertions Access issues.
		// Anything else in the document is skipped rather than erroring, so a
		// future key type Cloudflare adds does not break verification.
		if k.Kty != "RSA" || k.Kid == "" {
			continue
		}
		nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			continue
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			continue
		}
		keys[k.Kid] = &rsa.PublicKey{
			N: new(big.Int).SetBytes(nBytes),
			E: int(bigEndianUint(eBytes)),
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("access: certs document contained no usable RSA keys")
	}
	return keys, nil
}

// bigEndianUint reads a short big-endian byte slice (the JWK exponent) as a
// uint64. Exponents are 1-4 bytes in practice.
func bigEndianUint(b []byte) uint64 {
	var padded [8]byte
	if len(b) > 8 {
		b = b[len(b)-8:]
	}
	copy(padded[8-len(b):], b)
	return binary.BigEndian.Uint64(padded[:])
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

type jwtClaims struct {
	Iss   string   `json:"iss"`
	Sub   string   `json:"sub"`
	Email string   `json:"email"`
	Exp   int64    `json:"exp"`
	Nbf   int64    `json:"nbf"`
	Iat   int64    `json:"iat"`
	Aud   audience `json:"aud"`
}

// audience decodes the `aud` claim, which JWT allows to be either a string or
// an array of strings.
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*a = []string{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*a = many
	return nil
}

// Verify checks a raw assertion and returns the identity it carries.
func (v *AccessVerifier) Verify(ctx context.Context, token string, now time.Time) (Identity, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Identity{}, fmt.Errorf("%w: not a three-part JWT", ErrBadAssertion)
	}

	headerRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Identity{}, fmt.Errorf("%w: header: %v", ErrBadAssertion, err)
	}
	var hdr jwtHeader
	if err := json.Unmarshal(headerRaw, &hdr); err != nil {
		return Identity{}, fmt.Errorf("%w: header: %v", ErrBadAssertion, err)
	}

	// Pin the algorithm before touching the key. Accepting whatever `alg` the
	// token names is the classic JWT forgery: "none" skips verification outright,
	// and an HMAC alg would have us verify with the RSA public key as the shared
	// secret — which the attacker also has, since it is public.
	if hdr.Alg != "RS256" {
		return Identity{}, fmt.Errorf("%w: unexpected alg %q", ErrBadAssertion, hdr.Alg)
	}
	if hdr.Kid == "" {
		return Identity{}, fmt.Errorf("%w: no kid", ErrBadAssertion)
	}

	if err := v.refresh(ctx, now); err != nil {
		return Identity{}, err
	}
	v.mu.Lock()
	key, known := v.keys[hdr.Kid]
	v.mu.Unlock()
	if !known {
		// A kid we have never seen may be a rotation we have not picked up. Force
		// one refresh, then give up — an attacker must not be able to trigger a
		// fetch per request.
		v.mu.Lock()
		v.fetched = time.Time{}
		v.mu.Unlock()
		if err := v.refresh(ctx, now); err != nil {
			return Identity{}, err
		}
		v.mu.Lock()
		key, known = v.keys[hdr.Kid]
		v.mu.Unlock()
		if !known {
			return Identity{}, fmt.Errorf("%w: unknown signing key %q", ErrBadAssertion, hdr.Kid)
		}
	}

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Identity{}, fmt.Errorf("%w: signature: %v", ErrBadAssertion, err)
	}
	signed := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, signed[:], sig); err != nil {
		return Identity{}, fmt.Errorf("%w: %v", ErrBadAssertion, err)
	}

	// Only now are the claims trustworthy.
	claimsRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Identity{}, fmt.Errorf("%w: claims: %v", ErrBadAssertion, err)
	}
	var c jwtClaims
	if err := json.Unmarshal(claimsRaw, &c); err != nil {
		return Identity{}, fmt.Errorf("%w: claims: %v", ErrBadAssertion, err)
	}

	if c.Iss != v.issuer() {
		return Identity{}, fmt.Errorf("%w: issuer %q", ErrBadAssertion, c.Iss)
	}
	if !c.Aud.has(v.audience) {
		// Every Access app on the team shares a signing key, so this check is
		// what stops a token minted for another app being replayed here.
		return Identity{}, fmt.Errorf("%w: audience mismatch", ErrBadAssertion)
	}
	if c.Exp == 0 {
		return Identity{}, fmt.Errorf("%w: no expiry", ErrBadAssertion)
	}
	if now.After(time.Unix(c.Exp, 0).Add(clockSkew)) {
		return Identity{}, fmt.Errorf("%w: expired", ErrBadAssertion)
	}
	if c.Nbf != 0 && now.Before(time.Unix(c.Nbf, 0).Add(-clockSkew)) {
		return Identity{}, fmt.Errorf("%w: not yet valid", ErrBadAssertion)
	}

	// A Cloudflare Access application fronts exactly one coordinator, so every
	// human it admits belongs to that deployment's tenant. Hosted multi-tenancy
	// uses a different provider (see identity.go), not a different audience.
	return Identity{
		Tenant: v.tenant, Email: c.Email, Sub: c.Sub, Expiry: time.Unix(c.Exp, 0),
	}, nil
}

func (a audience) has(want string) bool {
	for _, v := range a {
		if v == want {
			return true
		}
	}
	return false
}

// identityKey is the context key carrying the verified human.
type identityKey struct{}

// IdentityFrom returns the verified identity attached by Middleware.
func IdentityFrom(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(Identity)
	return id, ok
}

// Middleware rejects any request without a valid Access assertion.
//
// ⚠️ This is only half of the control, and the weaker half.
//
// Verifying the header proves a request *came through* Cloudflare Access. It
// does nothing about a request that reaches the origin another way. Every other
// a typical self-hosted service sits behind a reverse proxy on a private
// directly from the tailnet — published that way, hub would be drivable
// by anyone on the tailnet with no assertion at all, and this middleware would
// never see those requests to reject them.
//
// So the origin must be bound to the cloudflared network only: no Traefik
// router, no host port, no public DNS entry. There is deliberately no
// "trusted source IP" bypass in this function — a private RFC1918 source proves
// nothing here, because the tailnet is exactly where an attacker would be.
//
// Verify the property directly before every cutover: from a tailnet host, a
// request to the origin must fail to connect. If it connects, this middleware
// is decoration.
func (v *AccessVerifier) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get(accessHeader)
		if token == "" {
			http.Error(w, "forbidden: no Access assertion", http.StatusForbidden)
			return
		}
		id, err := v.Verify(r.Context(), token, time.Now())
		if err != nil {
			http.Error(w, "forbidden: "+err.Error(), http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, id)))
	})
}
