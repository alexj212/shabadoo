package hub

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// accessFixture stands in for a Cloudflare Access team: an RSA signing key and
// a certs endpoint that publishes it.
type accessFixture struct {
	key      *rsa.PrivateKey
	kid      string
	server   *httptest.Server
	verifier *AccessVerifier
	fetches  int
}

func newAccessFixture(t *testing.T) *accessFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &accessFixture{key: key, kid: "test-kid-1"}

	mux := http.NewServeMux()
	mux.HandleFunc("/cdn-cgi/access/certs", func(w http.ResponseWriter, r *http.Request) {
		f.fetches++
		json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{{
				"kid": f.kid,
				"kty": "RSA",
				"alg": "RS256",
				"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
			}},
		})
	})
	// TLS, because the verifier builds its certs URL and issuer as https://<team>.
	f.server = httptest.NewTLSServer(mux)
	t.Cleanup(f.server.Close)

	u, _ := url.Parse(f.server.URL)
	v, err := NewAccessVerifier(u.Host, "test-aud")
	if err != nil {
		t.Fatal(err)
	}
	// Point the verifier at the test server instead of https://<team>/.
	v.client = f.server.Client()
	f.verifier = v
	return f
}

// mint builds a signed JWT. Callers mutate header/claims to test each check.
func (f *accessFixture) mint(t *testing.T, hdr map[string]any, claims map[string]any) string {
	t.Helper()
	enc := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	signing := enc(hdr) + "." + enc(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (f *accessFixture) goodHeader() map[string]any {
	return map[string]any{"alg": "RS256", "kid": f.kid, "typ": "JWT"}
}

func (f *accessFixture) goodClaims(now time.Time) map[string]any {
	return map[string]any{
		"iss":   f.verifier.issuer(),
		"aud":   []string{"test-aud"},
		"email": "alex@example.com",
		"sub":   "user-1",
		"iat":   now.Add(-time.Minute).Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}
}

func TestAccessVerifyAcceptsValidToken(t *testing.T) {
	f := newAccessFixture(t)
	now := time.Now()
	tok := f.mint(t, f.goodHeader(), f.goodClaims(now))

	id, err := f.verifier.Verify(context.Background(), tok, now)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.Email != "alex@example.com" {
		t.Errorf("email = %q", id.Email)
	}
}

// alg:none is the canonical JWT forgery — the signature is empty and a naive
// verifier skips the check entirely.
func TestAccessRejectsAlgNone(t *testing.T) {
	f := newAccessFixture(t)
	now := time.Now()

	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	tok := enc(map[string]any{"alg": "none", "kid": f.kid}) + "." + enc(f.goodClaims(now)) + "."

	if _, err := f.verifier.Verify(context.Background(), tok, now); !errors.Is(err, ErrBadAssertion) {
		t.Fatalf("err = %v, want ErrBadAssertion", err)
	}
}

// Algorithm confusion: an attacker signs with HMAC using the *public* key as
// the shared secret. Pinning alg to RS256 is what stops it.
func TestAccessRejectsHMACConfusion(t *testing.T) {
	f := newAccessFixture(t)
	now := time.Now()
	tok := f.mint(t, map[string]any{"alg": "HS256", "kid": f.kid}, f.goodClaims(now))

	if _, err := f.verifier.Verify(context.Background(), tok, now); !errors.Is(err, ErrBadAssertion) {
		t.Fatalf("err = %v, want ErrBadAssertion", err)
	}
}

// Every Access app on a team shares one signing key, so a token for a
// *different* app verifies cryptographically. The audience check is the only
// thing separating them.
func TestAccessRejectsWrongAudience(t *testing.T) {
	f := newAccessFixture(t)
	now := time.Now()
	claims := f.goodClaims(now)
	claims["aud"] = []string{"some-other-app-on-the-same-team"}
	tok := f.mint(t, f.goodHeader(), claims)

	_, err := f.verifier.Verify(context.Background(), tok, now)
	if !errors.Is(err, ErrBadAssertion) || !strings.Contains(err.Error(), "audience") {
		t.Fatalf("err = %v, want audience mismatch", err)
	}
}

func TestAccessRejectsWrongIssuer(t *testing.T) {
	f := newAccessFixture(t)
	now := time.Now()
	claims := f.goodClaims(now)
	claims["iss"] = "https://evil.cloudflareaccess.com"
	tok := f.mint(t, f.goodHeader(), claims)

	if _, err := f.verifier.Verify(context.Background(), tok, now); !errors.Is(err, ErrBadAssertion) {
		t.Fatalf("err = %v, want ErrBadAssertion", err)
	}
}

func TestAccessRejectsExpired(t *testing.T) {
	f := newAccessFixture(t)
	now := time.Now()
	claims := f.goodClaims(now)
	claims["exp"] = now.Add(-2 * time.Hour).Unix()
	tok := f.mint(t, f.goodHeader(), claims)

	if _, err := f.verifier.Verify(context.Background(), tok, now); !errors.Is(err, ErrBadAssertion) {
		t.Fatalf("err = %v, want ErrBadAssertion", err)
	}
}

// A token with no expiry must not be treated as valid forever.
func TestAccessRejectsMissingExpiry(t *testing.T) {
	f := newAccessFixture(t)
	now := time.Now()
	claims := f.goodClaims(now)
	delete(claims, "exp")
	tok := f.mint(t, f.goodHeader(), claims)

	if _, err := f.verifier.Verify(context.Background(), tok, now); !errors.Is(err, ErrBadAssertion) {
		t.Fatalf("err = %v, want ErrBadAssertion", err)
	}
}

// A token signed by a key that isn't the team's must fail even if every claim
// looks right.
func TestAccessRejectsForeignSigningKey(t *testing.T) {
	f := newAccessFixture(t)
	now := time.Now()

	attacker, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	signing := enc(f.goodHeader()) + "." + enc(f.goodClaims(now))
	sum := sha256.Sum256([]byte(signing))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, attacker, crypto.SHA256, sum[:])
	tok := signing + "." + base64.RawURLEncoding.EncodeToString(sig)

	if _, err := f.verifier.Verify(context.Background(), tok, now); !errors.Is(err, ErrBadAssertion) {
		t.Fatalf("err = %v, want ErrBadAssertion", err)
	}
}

// Tampering with claims after signing must invalidate the token.
func TestAccessRejectsTamperedClaims(t *testing.T) {
	f := newAccessFixture(t)
	now := time.Now()
	tok := f.mint(t, f.goodHeader(), f.goodClaims(now))

	parts := strings.Split(tok, ".")
	var claims map[string]any
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	json.Unmarshal(raw, &claims)
	claims["email"] = "attacker@example.com"
	b, _ := json.Marshal(claims)
	parts[1] = base64.RawURLEncoding.EncodeToString(b)

	if _, err := f.verifier.Verify(context.Background(), strings.Join(parts, "."), now); !errors.Is(err, ErrBadAssertion) {
		t.Fatalf("err = %v, want ErrBadAssertion", err)
	}
}

// An unknown kid triggers at most one extra refresh — otherwise an attacker
// could drive a certs fetch per request.
func TestAccessUnknownKidRefreshesOnce(t *testing.T) {
	f := newAccessFixture(t)
	now := time.Now()
	tok := f.mint(t, map[string]any{"alg": "RS256", "kid": "nope"}, f.goodClaims(now))

	if _, err := f.verifier.Verify(context.Background(), tok, now); err == nil {
		t.Fatal("unknown kid accepted")
	}
	before := f.fetches
	if _, err := f.verifier.Verify(context.Background(), tok, now); err == nil {
		t.Fatal("unknown kid accepted")
	}
	if got := f.fetches - before; got > 2 {
		t.Errorf("certs fetched %d times for one bad request", got)
	}
}

// The middleware must fail closed: no header, no access. There is deliberately
// no private-source-IP bypass.
func TestMiddlewareRejectsMissingHeader(t *testing.T) {
	f := newAccessFixture(t)
	h := f.verifier.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler ran without an assertion")
	}))

	for _, remote := range []string{"10.0.0.50:1234", "10.0.0.10:1234", "127.0.0.1:1234"} {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = remote
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("remote %s: status = %d, want 403 — private source addresses get no bypass",
				remote, rec.Code)
		}
	}
}

func TestMiddlewarePassesIdentity(t *testing.T) {
	f := newAccessFixture(t)
	now := time.Now()
	tok := f.mint(t, f.goodHeader(), f.goodClaims(now))

	var seen Identity
	h := f.verifier.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := IdentityFrom(r.Context())
		if !ok {
			t.Error("no identity in context")
		}
		seen = id
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set(accessHeader, tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if seen.Email != "alex@example.com" {
		t.Errorf("identity = %+v", seen)
	}
}

func TestNewAccessVerifierRequiresAudience(t *testing.T) {
	if _, err := NewAccessVerifier("team.cloudflareaccess.com", ""); err == nil {
		t.Fatal("empty audience accepted — every app on the team would validate")
	}
	if _, err := NewAccessVerifier("", "aud"); err == nil {
		t.Fatal("empty team domain accepted")
	}
}

func TestParseJWKSRejectsEmpty(t *testing.T) {
	if _, err := parseJWKS([]byte(`{"keys":[]}`)); err == nil {
		t.Fatal("empty key set accepted")
	}
	// A non-RSA key alone is unusable, but must not crash the parser.
	if _, err := parseJWKS([]byte(`{"keys":[{"kty":"EC","kid":"x"}]}`)); err == nil {
		t.Fatal("EC-only key set accepted")
	}
}
