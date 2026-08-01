package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func deviceReq(token string) *http.Request {
	r := httptest.NewRequest("GET", "/api/sessions", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

// The enrolment round trip an iOS app performs once, at install.
func TestDeviceEnrolment(t *testing.T) {
	d := NewDeviceStore()

	// A human already authenticated as tenant "alex" mints the code.
	code := d.NewEnrolCode(Identity{Tenant: "alex", Email: "alex@example.com"})
	if code == "" {
		t.Fatal("no code minted")
	}

	token, dev, err := d.Redeem(code, "Alex's iPhone")
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	if dev.Tenant != "alex" {
		t.Errorf("device tenant = %q, want alex", dev.Tenant)
	}

	id, err := d.Identify(deviceReq(token))
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if id.Tenant != "alex" || id.Email != "alex@example.com" {
		t.Errorf("identity = %+v", id)
	}
	if !strings.HasPrefix(id.Sub, "device:") {
		t.Errorf("sub = %q, want a device subject", id.Sub)
	}
}

// A pairing code is single use — otherwise an overheard code enrols any number
// of devices.
func TestEnrolCodeIsSingleUse(t *testing.T) {
	d := NewDeviceStore()
	code := d.NewEnrolCode(Identity{Tenant: "alex"})

	if _, _, err := d.Redeem(code, "first"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.Redeem(code, "second"); !errors.Is(err, ErrBadEnrolCode) {
		t.Fatalf("err = %v, want ErrBadEnrolCode — a code must not enrol twice", err)
	}
}

func TestEnrolCodeExpires(t *testing.T) {
	d := NewDeviceStore()
	base := time.Now()
	d.nowFunc = func() time.Time { return base }
	code := d.NewEnrolCode(Identity{Tenant: "alex"})

	d.nowFunc = func() time.Time { return base.Add(enrolCodeTTL + time.Second) }
	if _, _, err := d.Redeem(code, "late"); !errors.Is(err, ErrBadEnrolCode) {
		t.Fatalf("err = %v, want ErrBadEnrolCode", err)
	}
}

func TestUnknownCodeRejected(t *testing.T) {
	d := NewDeviceStore()
	if _, _, err := d.Redeem("NOTACODE", "x"); !errors.Is(err, ErrBadEnrolCode) {
		t.Fatalf("err = %v, want ErrBadEnrolCode", err)
	}
}

func TestDeviceTokenRejections(t *testing.T) {
	d := NewDeviceStore()
	code := d.NewEnrolCode(Identity{Tenant: "alex"})
	token, dev, _ := d.Redeem(code, "phone")

	t.Run("no token", func(t *testing.T) {
		if _, err := d.Identify(deviceReq("")); !errors.Is(err, ErrNoDeviceToken) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("wrong token", func(t *testing.T) {
		if _, err := d.Identify(deviceReq(newToken())); !errors.Is(err, ErrBadDeviceToken) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("revoked", func(t *testing.T) {
		if !d.Revoke(dev.ID) {
			t.Fatal("Revoke reported no such device")
		}
		if _, err := d.Identify(deviceReq(token)); !errors.Is(err, ErrBadDeviceToken) {
			t.Fatalf("revoked token still works: %v", err)
		}
	})
}

func TestDeviceTokenExpires(t *testing.T) {
	d := NewDeviceStore()
	base := time.Now()
	d.nowFunc = func() time.Time { return base }
	code := d.NewEnrolCode(Identity{Tenant: "alex"})
	token, _, _ := d.Redeem(code, "phone")

	d.nowFunc = func() time.Time { return base.Add(deviceTokenTTL + time.Hour) }
	// Specifically EXPIRED, not merely invalid: a client tells the user
	// "your access ran out, pair again" rather than "unknown credential",
	// and those read very differently to someone who has done nothing wrong.
	if _, err := d.Identify(deviceReq(token)); !errors.Is(err, ErrExpiredDeviceToken) {
		t.Fatalf("expired token: err = %v, want ErrExpiredDeviceToken", err)
	}
}

// A device only ever sees its own tenant's device list.
func TestDeviceListIsTenantScoped(t *testing.T) {
	d := NewDeviceStore()
	a := d.NewEnrolCode(Identity{Tenant: "alex"})
	b := d.NewEnrolCode(Identity{Tenant: "other"})
	d.Redeem(a, "alex phone")
	d.Redeem(b, "other phone")

	if got := d.List("alex"); len(got) != 1 || got[0].Label != "alex phone" {
		t.Fatalf("alex sees %+v", got)
	}
	if got := d.List("other"); len(got) != 1 || got[0].Label != "other phone" {
		t.Fatalf("other sees %+v", got)
	}
}

// The middleware must refuse an identity that names no tenant, rather than
// silently defaulting it into someone else's data.
func TestMiddlewareRejectsTenantlessIdentity(t *testing.T) {
	h := Middleware(tenantlessProvider{}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("handler ran for a tenantless identity")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

type tenantlessProvider struct{}

func (tenantlessProvider) Identify(*http.Request) (Identity, error) {
	return Identity{Email: "someone@example.com"}, nil // no Tenant
}
func (tenantlessProvider) Name() string { return "tenantless" }

// AnyOf lets one deployment serve a browser and the app on the same endpoints.
func TestAnyOfFallsThrough(t *testing.T) {
	d := NewDeviceStore()
	code := d.NewEnrolCode(Identity{Tenant: "alex"})
	token, _, _ := d.Redeem(code, "phone")

	providers := AnyOf{d, InsecureProvider{}}

	// App request: matched by the device store.
	id, err := providers.Identify(deviceReq(token))
	if err != nil || id.Tenant != "alex" {
		t.Fatalf("app request: id=%+v err=%v", id, err)
	}

	// Loopback browser request with no token: falls through to insecure.
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5000"
	id, err = providers.Identify(r)
	if err != nil || id.Tenant != DefaultTenant {
		t.Fatalf("loopback request: id=%+v err=%v", id, err)
	}
}

// The insecure provider is for local development only, and must not admit a
// request that arrived over the network — the likeliest accident is starting a
// dev coordinator on a routable address and forgetting.
func TestInsecureProviderIsLoopbackOnly(t *testing.T) {
	p := InsecureProvider{}

	for _, remote := range []string{"10.0.0.50:1234", "10.0.0.10:1234", "8.8.8.8:80"} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		if _, err := p.Identify(r); err == nil {
			t.Errorf("insecure provider admitted %s", remote)
		}
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	if _, err := p.Identify(r); err != nil {
		t.Errorf("insecure provider refused loopback: %v", err)
	}
}

// Tokens must not be recoverable from the store's contents.
func TestTokensAreStoredHashed(t *testing.T) {
	d := NewDeviceStore()
	code := d.NewEnrolCode(Identity{Tenant: "alex"})
	token, _, _ := d.Redeem(code, "phone")

	for key := range d.byHash {
		if key == token {
			t.Fatal("token stored verbatim; a database dump would yield live credentials")
		}
	}
	if _, ok := d.byHash[hashToken(token)]; !ok {
		t.Fatal("token not found under its hash")
	}
}

// Production chains have no permissive member: a bogus token must be refused
// outright, not fall through to something weaker. This is the assertion the
// live insecure-mode run cannot make, because InsecureProvider is in that
// chain by design.
func TestAnyOfWithoutInsecureRefusesBadToken(t *testing.T) {
	d := NewDeviceStore()
	providers := AnyOf{d} // the production shape, minus Access which needs a network

	if _, err := providers.Identify(deviceReq("deadbeef")); err == nil {
		t.Fatal("bogus device token accepted with no permissive provider in the chain")
	}

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5000"
	if _, err := providers.Identify(r); err == nil {
		t.Fatal("loopback request with no credential accepted")
	}
}

// The middleware rejects, rather than admits, when every provider fails.
func TestMiddlewareFailsClosed(t *testing.T) {
	h := Middleware(AnyOf{NewDeviceStore()}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("handler ran with no valid identity")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, deviceReq("nonsense"))

	// 401, and specifically NOT 403. An unknown credential is an
	// authentication failure; 403 is reserved for a valid credential that may
	// not do something. A client that cannot tell them apart discards a good
	// token the first time it hits a scope limit.
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); !strings.Contains(got, "invalid_token") {
		t.Errorf("WWW-Authenticate = %q, want an RFC 6750 error code", got)
	}
}

// An expired device should not be loaded back into memory.
func TestExpiredDeviceNotReloaded(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hub.db")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	d, err := OpenDeviceStore(ctx, s)
	if err != nil {
		t.Fatalf("OpenDeviceStore: %v", err)
	}
	// Enrol as if it happened just over a token lifetime ago.
	past := time.Now().Add(-deviceTokenTTL - time.Hour)
	d.nowFunc = func() time.Time { return past }
	token, _, err := d.Redeem(d.NewEnrolCode(Identity{Tenant: "alex"}), "old phone")
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	s.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	d2, err := OpenDeviceStore(ctx, s2)
	if err != nil {
		t.Fatalf("OpenDeviceStore: %v", err)
	}
	if n := d2.Count(); n != 0 {
		t.Errorf("expired device loaded: count = %d, want 0", n)
	}
	if _, err := d2.Identify(deviceReq(token)); !errors.Is(err, ErrBadDeviceToken) {
		t.Errorf("expired token accepted: err = %v", err)
	}
}

// Renewal exists so a 90-day TTL does not become a lockout. The token is
// EXTENDED rather than rotated, so these pin that the same credential keeps
// working — a rotation bug would show up as the old token dying here.
func TestDeviceRenewExtendsTheSameToken(t *testing.T) {
	d := NewDeviceStore()
	base := time.Now()
	d.nowFunc = func() time.Time { return base }

	code := d.NewEnrolCode(Identity{Tenant: "alex"})
	token, dev, err := d.Redeem(code, "phone")
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}

	// Most of the way through the token's life, as a client renewing on launch.
	d.nowFunc = func() time.Time { return base.Add(deviceTokenTTL - 24*time.Hour) }
	renewed, err := d.Renew(dev.ID)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if !renewed.Expires.After(dev.Expires) {
		t.Errorf("expiry did not move: was %v, now %v", dev.Expires, renewed.Expires)
	}

	// The original token must still authenticate — that is the whole design.
	if _, err := d.Identify(deviceReq(token)); err != nil {
		t.Fatalf("the renewed device's original token stopped working: %v", err)
	}

	// And it must now outlive the original expiry.
	d.nowFunc = func() time.Time { return base.Add(deviceTokenTTL + time.Hour) }
	if _, err := d.Identify(deviceReq(token)); err != nil {
		t.Fatalf("token expired despite renewal: %v", err)
	}
}

// An already-dead credential cannot renew itself. Identify rejects it before a
// handler ever runs, and resurrecting one would make revocation reversible by
// whoever holds the revoked token.
func TestDeviceRenewRefusesExpiredAndUnknown(t *testing.T) {
	d := NewDeviceStore()
	base := time.Now()
	d.nowFunc = func() time.Time { return base }

	code := d.NewEnrolCode(Identity{Tenant: "alex"})
	_, dev, _ := d.Redeem(code, "phone")

	d.nowFunc = func() time.Time { return base.Add(deviceTokenTTL + time.Hour) }
	if _, err := d.Renew(dev.ID); !errors.Is(err, ErrBadDeviceToken) {
		t.Errorf("expired device renewed itself: %v", err)
	}

	d.nowFunc = func() time.Time { return base }
	if _, err := d.Renew("no-such-device"); !errors.Is(err, ErrBadDeviceToken) {
		t.Errorf("unknown device id renewed: %v", err)
	}
}

// A revoked device must not be renewable — otherwise revocation is undone by
// the holder of the credential you just revoked.
func TestDeviceRenewRefusesRevoked(t *testing.T) {
	d := NewDeviceStore()
	code := d.NewEnrolCode(Identity{Tenant: "alex"})
	_, dev, _ := d.Redeem(code, "phone")

	if !d.Revoke(dev.ID) {
		t.Fatal("revoke reported no such device")
	}
	if _, err := d.Renew(dev.ID); !errors.Is(err, ErrBadDeviceToken) {
		t.Errorf("revoked device renewed itself: %v", err)
	}
}

// Renewal must reach the database. An extension that lives only in memory
// silently reverts at the next restart, which is the same class of bug that
// made the TTL fiction before devices were persisted at all.
func TestDeviceRenewSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hub.db")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	d, err := OpenDeviceStore(ctx, s)
	if err != nil {
		t.Fatalf("OpenDeviceStore: %v", err)
	}
	base := time.Now()
	d.nowFunc = func() time.Time { return base }

	code := d.NewEnrolCode(Identity{Tenant: "alex"})
	token, dev, _ := d.Redeem(code, "phone")

	d.nowFunc = func() time.Time { return base.Add(deviceTokenTTL - 24*time.Hour) }
	if _, err := d.Renew(dev.ID); err != nil {
		t.Fatalf("renew: %v", err)
	}
	s.Close()

	// Reopen, as a coordinator restart would.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	d2, err := OpenDeviceStore(ctx, s2)
	if err != nil {
		t.Fatalf("OpenDeviceStore after restart: %v", err)
	}
	d2.nowFunc = func() time.Time { return base.Add(deviceTokenTTL + time.Hour) }

	if _, err := d2.Identify(deviceReq(token)); err != nil {
		t.Fatalf("renewal did not survive the restart: %v", err)
	}
}

// A browser has nowhere to put a bearer token, so redeem hands it a cookie and
// the same credential must authenticate. Before this, pairing in a browser
// produced a token the page could only display: the dashboard sent no
// credential at all and every reload looked like a lost pairing.
func TestDeviceCookieAuthenticates(t *testing.T) {
	d := NewDeviceStore()
	code := d.NewEnrolCode(Identity{Tenant: "alex"})
	token, _, err := d.Redeem(code, "firefox")
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}

	r := httptest.NewRequest("GET", "/api/sessions", nil)
	r.AddCookie(&http.Cookie{Name: TokenCookie, Value: token})
	id, err := d.Identify(r)
	if err != nil {
		t.Fatalf("cookie did not authenticate: %v", err)
	}
	if id.Tenant != "alex" {
		t.Errorf("tenant = %q, want alex", id.Tenant)
	}
}

// The Authorization header wins, so a CLI or app that also carries a browser
// cookie for the same origin is never authenticated as the wrong device.
func TestBearerHeaderBeatsCookie(t *testing.T) {
	d := NewDeviceStore()
	good := d.NewEnrolCode(Identity{Tenant: "alex"})
	headerToken, _, _ := d.Redeem(good, "cli")

	r := httptest.NewRequest("GET", "/api/sessions", nil)
	r.Header.Set("Authorization", "Bearer "+headerToken)
	r.AddCookie(&http.Cookie{Name: TokenCookie, Value: "stale-cookie-value"})

	if _, err := d.Identify(r); err != nil {
		t.Fatalf("a valid header was overridden by a stale cookie: %v", err)
	}
}

// The redeem endpoint must actually set the cookie, with the flags that make it
// safe: HttpOnly (script cannot read it) and SameSite=Lax (not sent on
// cross-site POSTs, which is the CSRF guard for every write endpoint).
func TestRedeemSetsHardenedCookie(t *testing.T) {
	d := NewDeviceStore()
	code := d.NewEnrolCode(Identity{Tenant: "alex"})

	mux := http.NewServeMux()
	d.PublicRoutes(mux)

	body := strings.NewReader(`{"code":"` + code + `","label":"firefox"}`)
	req := httptest.NewRequest("POST", "/api/devices/redeem", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("redeem: %d %s", rec.Code, rec.Body.String())
	}
	var found *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == TokenCookie {
			found = c
		}
	}
	if found == nil {
		t.Fatal("redeem set no token cookie — a browser cannot authenticate")
	}
	if !found.HttpOnly {
		t.Error("cookie is readable by script; an injection could exfiltrate the token")
	}
	if found.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax (CSRF guard for the write endpoints)", found.SameSite)
	}
	if found.Value == "" {
		t.Error("cookie carries no token")
	}
}

// /healthz must answer without a credential — a monitor that needs one is a
// monitor nobody configures, and a container healthcheck has nowhere to keep a
// token. It must also stay boring: an unauthenticated caller learns whether the
// service is up, not what it is working on.
func TestHealthzIsUnauthenticatedAndDiscreet(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	h := New(&Authorizer{}, s)
	Version = "test-build"

	// A session that WOULD be disclosed if the endpoint were careless.
	tn := s.Tenant(DefaultTenant)
	if err := tn.UpsertSession(ctx, Session{
		SessionID: "claude-secret-project-abc",
		Agent:     "wsl",
		Project:   "secret-project",
		CWD:       "/c/projects/secret-project",
	}, time.Now()); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	mux := http.NewServeMux()
	h.HealthRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil)) // no Authorization
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz needs a credential or is broken: %d %s", rec.Code, rec.Body.String())
	}

	var out struct {
		Status  string `json:"status"`
		Version string `json:"version"`
		Agents  int    `json:"agents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Status != "ok" {
		t.Errorf("status = %q, want ok", out.Status)
	}
	if out.Version != "test-build" {
		t.Errorf("version = %q — a healthcheck that cannot tell you which build is running "+
			"cannot catch a bad deploy", out.Version)
	}

	body := rec.Body.String()
	for _, leak := range []string{"secret-project", "/c/projects", "wsl", "claude-secret"} {
		if strings.Contains(body, leak) {
			t.Errorf("healthz disclosed %q to an unauthenticated caller: %s", leak, body)
		}
	}
}

// /api/devices/redeem is the only endpoint outside the identity middleware, so
// it is the only one an unauthenticated caller can reach. It had no rate limit
// and left no trace, meaning an attempt to grind pairing codes was both
// unbounded and invisible.
func TestRedeemThrottlesAfterRepeatedFailures(t *testing.T) {
	d := NewDeviceStore()
	mux := http.NewServeMux()
	d.PublicRoutes(mux)

	post := func(code, ip string) int {
		req := httptest.NewRequest("POST", "/api/devices/redeem",
			strings.NewReader(`{"code":"`+code+`","label":"x"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Real-Ip", ip)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}

	for i := 0; i < redeemFailureLimit; i++ {
		if got := post("BADCODE1", "10.0.0.1"); got != http.StatusUnauthorized {
			t.Fatalf("attempt %d: got %d, want 401", i+1, got)
		}
	}
	if got := post("BADCODE1", "10.0.0.1"); got != http.StatusTooManyRequests {
		t.Errorf("after %d failures got %d, want 429 — grinding is unbounded",
			redeemFailureLimit, got)
	}

	// Throttling is per caller: one attacker must not lock out everybody else,
	// which would turn the rate limit into a denial of service on pairing.
	if got := post("BADCODE1", "10.0.0.2"); got != http.StatusUnauthorized {
		t.Errorf("a second address got %d, want 401 — the limiter is global, not per caller", got)
	}
}

// A person who mistypes a code and then gets it right must not carry a penalty,
// and a successful pairing clears the count.
func TestRedeemSuccessClearsFailures(t *testing.T) {
	d := NewDeviceStore()
	mux := http.NewServeMux()
	d.PublicRoutes(mux)
	code := d.NewEnrolCode(Identity{Tenant: "alex"})

	post := func(c string) int {
		req := httptest.NewRequest("POST", "/api/devices/redeem",
			strings.NewReader(`{"code":"`+c+`","label":"phone"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Real-Ip", "10.0.0.9")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}

	for i := 0; i < redeemFailureLimit-1; i++ {
		post("WRONG123")
	}
	if got := post(code); got != http.StatusOK {
		t.Fatalf("valid code after typos got %d, want 200", got)
	}
	if d.throttled("10.0.0.9") {
		t.Error("a successful pairing left the caller throttled")
	}
}

// The limiter must key on the caller, not the proxy — behind Traefik every
// request shares a RemoteAddr, so keying on that would throttle all clients as
// one the moment any of them fumbled a code.
func TestClientKeyPrefersForwardedAddress(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	r.RemoteAddr = "172.18.0.5:41234" // the proxy
	if got := clientKey(r); got != "172.18.0.5" {
		t.Errorf("bare request: clientKey = %q, want the remote host", got)
	}

	r.Header.Set("X-Forwarded-For", "100.64.0.7, 172.18.0.5")
	if got := clientKey(r); got != "100.64.0.7" {
		t.Errorf("with XFF: clientKey = %q, want the originating client", got)
	}

	r.Header.Set("X-Real-Ip", "100.64.0.9")
	if got := clientKey(r); got != "100.64.0.9" {
		t.Errorf("with X-Real-Ip: clientKey = %q, want the proxy's own view of the client", got)
	}
}

// A read-only credential must not be able to drive a pane. This is the whole
// point of the scope: with it, developing a client against the LIVE coordinator
// cannot type into a real Claude session mid-task.
func TestReadScopeCannotWrite(t *testing.T) {
	read := Identity{Tenant: "alex", Scope: ScopeRead}
	full := Identity{Tenant: "alex"}

	// Every mutating endpoint the human plane exposes.
	writes := []string{
		"/api/select", "/api/send", "/api/command", "/api/keys",
		"/api/kill", "/api/reopen", "/api/open",
		"/api/message/send", "/api/message/broadcast",
		"/api/devices/code", "/api/devices/revoke",
	}
	for _, path := range writes {
		r := httptest.NewRequest("POST", path, nil)
		r = r.WithContext(context.WithValue(r.Context(), identityKey{}, read))
		if writeAllowed(r) {
			t.Errorf("read-only credential may POST %s — it can drive a pane", path)
		}

		r = httptest.NewRequest("POST", path, nil)
		r = r.WithContext(context.WithValue(r.Context(), identityKey{}, full))
		if !writeAllowed(r) {
			t.Errorf("full credential refused POST %s", path)
		}
	}
}

// Reads stay open, and a read-only device must still be able to renew — one
// that cannot would silently expire in 90 days, which is the exact lockout the
// renew endpoint exists to prevent.
func TestReadScopeCanReadAndRenew(t *testing.T) {
	read := Identity{Tenant: "alex", Scope: ScopeRead}

	for _, path := range []string{"/api/sessions", "/api/capture", "/api/devices"} {
		r := httptest.NewRequest("GET", path, nil)
		r = r.WithContext(context.WithValue(r.Context(), identityKey{}, read))
		if !writeAllowed(r) {
			t.Errorf("read-only credential refused GET %s", path)
		}
	}

	r := httptest.NewRequest("POST", "/api/devices/renew", nil)
	r = r.WithContext(context.WithValue(r.Context(), identityKey{}, read))
	if !writeAllowed(r) {
		t.Error("read-only credential cannot renew — it will silently expire")
	}
}

// Default-deny on method: an endpoint added later without anyone thinking about
// scopes must be refused for read-only clients, not quietly permitted.
func TestReadScopeDeniesUnknownWriteByDefault(t *testing.T) {
	read := Identity{Tenant: "alex", Scope: ScopeRead}
	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		r := httptest.NewRequest(m, "/api/some-endpoint-invented-next-year", nil)
		r = r.WithContext(context.WithValue(r.Context(), identityKey{}, read))
		if writeAllowed(r) {
			t.Errorf("%s to a brand-new endpoint was allowed for a read-only credential", m)
		}
	}
}

// The scope is fixed when the code is minted and rides onto the device. A
// client cannot ask for its own permissions.
func TestScopeSurvivesRedeemAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hub.db")

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	d, err := OpenDeviceStore(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	code := d.NewScopedEnrolCode(Identity{Tenant: "alex"}, ScopeRead)
	token, dev, err := d.Redeem(code, "phone")
	if err != nil {
		t.Fatal(err)
	}
	if dev.Scope != ScopeRead {
		t.Fatalf("redeemed device scope = %q, want read", dev.Scope)
	}
	id, err := d.Identify(deviceReq(token))
	if err != nil {
		t.Fatal(err)
	}
	if id.Scope != ScopeRead {
		t.Errorf("identity scope = %q, want read", id.Scope)
	}
	s.Close()

	// A read-only phone that becomes read-write at the next restart is worse
	// than never having offered the scope.
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	d2, err := OpenDeviceStore(ctx, s2)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := d2.Identify(deviceReq(token))
	if err != nil {
		t.Fatal(err)
	}
	if id2.Scope != ScopeRead {
		t.Errorf("scope did not survive restart: %q — the device silently gained write access", id2.Scope)
	}
}

// The audit log's whole job is answering "which credential did this". Attributing
// by Email could not: a device inherits the email of whoever minted its pairing
// code, so every device enrolled from one chain shared an actor string and three
// enrolled devices all appeared as `bootstrap@default`.
func TestAuditActorNamesTheDevice(t *testing.T) {
	d := NewDeviceStore()

	// Two devices, same enrolling human — the case that used to collapse.
	code1 := d.NewEnrolCode(Identity{Tenant: "alex", Email: "bootstrap@default"})
	_, phone, _ := d.Redeem(code1, "Alex's iPhone")
	code2 := d.NewEnrolCode(Identity{Tenant: "alex", Email: "bootstrap@default"})
	_, laptop, _ := d.Redeem(code2, "Alex FireFox")

	actorFor := func(dev Device) string {
		id := Identity{Tenant: dev.Tenant, Email: dev.Email, Label: dev.Label,
			Sub: "device:" + dev.ID}
		return actor(context.WithValue(context.Background(), identityKey{}, id))
	}

	a, b := actorFor(phone), actorFor(laptop)
	if a == b {
		t.Fatalf("two devices share one audit actor (%q) — the log cannot say which acted", a)
	}
	if !strings.Contains(a, "Alex's iPhone") || !strings.Contains(b, "Alex FireFox") {
		t.Errorf("actor should name the device: got %q and %q", a, b)
	}
	if strings.Contains(a, "bootstrap@default") {
		t.Errorf("actor still attributes to the enrolling email: %q", a)
	}
	// The id ties a row to something revocable, and disambiguates two devices
	// a person gave the same name.
	if !strings.Contains(a, phone.ID[:8]) {
		t.Errorf("actor %q does not carry the device id", a)
	}
}

// A human identity (Access) is still named by email — devices are the special
// case, not the rule.
func TestAuditActorKeepsEmailForHumans(t *testing.T) {
	id := Identity{Tenant: "alex", Email: "alex@example.com", Sub: "sso|123"}
	got := actor(context.WithValue(context.Background(), identityKey{}, id))
	if got != "alex@example.com" {
		t.Errorf("actor = %q, want the email", got)
	}
}

// The OPERATOR names the device, at mint time. Without this the device names
// itself at redeem, so the list an operator later revokes from is populated by
// whatever each client chose to call itself.
func TestMintTimeLabelWinsOverClient(t *testing.T) {
	d := NewDeviceStore()

	code := d.NewNamedEnrolCode(Identity{Tenant: "alex"}, ScopeFull, "Alex's iPhone")
	_, dev, err := d.Redeem(code, "totally-legit-device")
	if err != nil {
		t.Fatal(err)
	}
	if dev.Label != "Alex's iPhone" {
		t.Errorf("label = %q — a client renamed a device the operator had named", dev.Label)
	}
}

// A client-supplied name is still used when the operator did not give one, so
// the bootstrap path (where the CLI names itself) keeps working.
func TestClientLabelUsedWhenOperatorGaveNone(t *testing.T) {
	d := NewDeviceStore()

	code := d.NewEnrolCode(Identity{Tenant: "alex"})
	_, dev, err := d.Redeem(code, "cli@wsl")
	if err != nil {
		t.Fatal(err)
	}
	if dev.Label != "cli@wsl" {
		t.Errorf("label = %q, want the client's own name", dev.Label)
	}
}

// Nothing may end up nameless — an unnamed row in the device list is one the
// operator cannot make a revocation decision about.
func TestDeviceIsNeverUnnamed(t *testing.T) {
	d := NewDeviceStore()
	code := d.NewEnrolCode(Identity{Tenant: "alex"})
	_, dev, err := d.Redeem(code, "   ")
	if err != nil {
		t.Fatal(err)
	}
	if dev.Label == "" || strings.TrimSpace(dev.Label) == "" {
		t.Errorf("device enrolled with a blank label: %q", dev.Label)
	}
}

// A client must be able to learn what it was granted from the enrolment
// response itself, rather than by attempting a write and reading the 403. This
// was claimed as shipped before it was, and caught by the client author.
func TestRedeemResponseReportsScopeAndLabel(t *testing.T) {
	d := NewDeviceStore()
	code := d.NewNamedEnrolCode(Identity{Tenant: "alex"}, ScopeRead, "Alex's iPhone")

	mux := http.NewServeMux()
	d.PublicRoutes(mux)

	req := httptest.NewRequest("POST", "/api/devices/redeem",
		strings.NewReader(`{"code":"`+code+`","label":"ignored"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("redeem: %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"token", "device_id", "tenant", "expires", "scope", "label"} {
		if _, ok := out[key]; !ok {
			t.Errorf("redeem response is missing %q — a client cannot tell what it was granted", key)
		}
	}
	if out["scope"] != ScopeRead {
		t.Errorf("scope = %v, want read", out["scope"])
	}
	if out["label"] != "Alex's iPhone" {
		t.Errorf("label = %v, want the operator's name", out["label"])
	}
}

// The distinction that matters most in practice: a read-only credential
// attempting a write must NOT look like a dead credential. Both were 403, so a
// client following the obvious rule ("403 means signed out") would throw away a
// perfectly good token the first time it hit a scope limit.
func TestScopeFailureIsNotAuthFailure(t *testing.T) {
	d := NewDeviceStore()
	code := d.NewNamedEnrolCode(Identity{Tenant: "alex"}, ScopeRead, "phone")
	token, _, err := d.Redeem(code, "phone")
	if err != nil {
		t.Fatal(err)
	}

	// A valid read-only token reaching a write endpoint: authorization failure.
	inner := requireWrite(func(w http.ResponseWriter, r *http.Request) {
		t.Error("a read-only credential reached a write handler")
	})
	h := Middleware(AnyOf{d}, inner)

	req := httptest.NewRequest("POST", "/api/send", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("read-only write: status = %d, want 403 (authorization)", rec.Code)
	}

	// The same endpoint with a credential that is not known: authentication.
	req2 := httptest.NewRequest("POST", "/api/send", nil)
	req2.Header.Set("Authorization", "Bearer not-a-real-token")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Errorf("unknown token: status = %d, want 401 (authentication)", rec2.Code)
	}

	if rec.Code == rec2.Code {
		t.Fatal("scope failure and auth failure return the same status — " +
			"a client cannot tell 'not allowed' from 'signed out'")
	}
}

// An expired token reports as expired all the way out to the header, so a
// client can say something true to the user without parsing prose.
func TestExpiredTokenReportsReasonInHeader(t *testing.T) {
	d := NewDeviceStore()
	base := time.Now()
	d.nowFunc = func() time.Time { return base }
	code := d.NewEnrolCode(Identity{Tenant: "alex"})
	token, _, _ := d.Redeem(code, "phone")
	d.nowFunc = func() time.Time { return base.Add(deviceTokenTTL + time.Hour) }

	h := Middleware(AnyOf{d}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, deviceReq(token))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); !strings.Contains(got, `reason="expired"`) {
		t.Errorf("WWW-Authenticate = %q, want reason=\"expired\"", got)
	}
}

// A push token must survive a restart, and a device must be able to replace it
// — iOS reissues one on reinstall and restore, so a set-once field would mean a
// phone that silently stops being notified.
func TestPushTokenIsUpdatableAndDurable(t *testing.T) {
	dir := t.TempDir()
	open := func() (*Store, *DeviceStore) {
		st, err := Open(filepath.Join(dir, "hub.db"))
		if err != nil {
			t.Fatal(err)
		}
		ds, err := OpenDeviceStore(context.Background(), st)
		if err != nil {
			t.Fatal(err)
		}
		return st, ds
	}

	st, d := open()
	code := d.NewEnrolCode(Identity{Tenant: "alex"})
	token, dev, _ := d.Redeem(code, "phone")

	if _, err := d.SetPushToken(dev.ID, "apns-first", "development"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SetPushToken(dev.ID, "apns-second", "development"); err != nil {
		t.Fatalf("second registration rejected: %v", err)
	}
	st.Close()

	// Reopened: the token is still there, and it is the second one.
	st, d = open()
	defer st.Close()
	id, err := d.Identify(deviceReq(token))
	if err != nil {
		t.Fatalf("device lost across restart: %v", err)
	}
	if id.Tenant != "alex" {
		t.Fatalf("tenant = %q", id.Tenant)
	}
	targets := d.PushTargets("alex")
	if len(targets) != 1 || targets[0].PushToken != "apns-second" {
		t.Fatalf("push targets = %+v, want one holding apns-second", targets)
	}

	// Clearing is how an app turns notifications off without giving up its
	// credential, so it must not leave a stale target behind.
	if _, err := d.SetPushToken(dev.ID, "", "development"); err != nil {
		t.Fatal(err)
	}
	if got := d.PushTargets("alex"); len(got) != 0 {
		t.Fatalf("cleared device still a push target: %+v", got)
	}

	// The APNs gateway is recorded, and an unknown one stays empty rather than
	// being guessed. Apple runs two gateways whose tokens are not
	// interchangeable, so a wrong guess fails silently for that device — "we do
	// not know" has to remain distinguishable from "we know it is production".
	st.Close()
	st, d = open()
	defer st.Close()
	if _, err := d.SetPushToken(dev.ID, "apns-third", "production"); err != nil {
		t.Fatal(err)
	}
	if got := d.PushTargets("alex"); len(got) != 1 || got[0].PushEnv != "production" {
		t.Fatalf("push env not recorded: %+v", got)
	}
	if _, err := d.SetPushToken(dev.ID, "apns-third", "sandbox-ish"); err != nil {
		t.Fatal(err)
	}
	if got := d.PushTargets("alex"); got[0].PushEnv != "" {
		t.Errorf("an unrecognised environment was stored as %q, want empty", got[0].PushEnv)
	}

	// A device id nobody enrolled must not create one.
	if _, err := d.SetPushToken("no-such-device", "apns", "development"); !errors.Is(err, ErrBadDeviceToken) {
		t.Fatalf("err = %v, want ErrBadDeviceToken", err)
	}
}

// A read-only credential must be able to register for push. A phone that can
// only read is exactly the one that needs telling when something blocks.
func TestReadScopeMayRegisterForPush(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"/api/devices/self/push", true},
		{"/api/devices/renew", true},
		{"/api/send", false},
		{"/api/devices/revoke", false},
	} {
		r := httptest.NewRequest("PUT", tc.path, nil)
		r = r.WithContext(context.WithValue(r.Context(), identityKey{},
			Identity{Tenant: "alex", Scope: ScopeRead}))
		if got := writeAllowed(r); got != tc.want {
			t.Errorf("writeAllowed(%s) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
