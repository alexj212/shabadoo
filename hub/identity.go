package hub

// How a human proves who they are.
//
// The agent plane is identical in every deployment — an SSH key is an SSH key
// whether the coordinator runs on dm or in a datacentre. Only the human plane
// differs, so that is the one thing behind an interface:
//
//	self-hosted browser  → Cloudflare Access (access.go)
//	iOS app, either mode → device token (this file)
//	hosted browser       → whatever the hosted manager uses, same interface
//	local development    → insecure, refuses to run on a routable address
//
// Everything downstream reads Identity.Tenant and nothing else, so adding a
// provider never touches the store, the hub, or the API.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// IdentityProvider authenticates a browser or app request.
type IdentityProvider interface {
	// Identify returns the verified human behind a request, or an error.
	// Implementations must fail closed.
	Identify(r *http.Request) (Identity, error)

	// Name is for logs and the startup banner.
	Name() string
}

// Middleware wraps a handler so only identified requests reach it.
func Middleware(p IdentityProvider, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := p.Identify(r)
		if err != nil {
			// A browser asking for a page gets sent somewhere it can act. Plain
			// error text is the right answer for an API client and a dead end
			// for a person: the dashboard is the URL they have bookmarked, and
			// until this redirect existed an unpaired browser landed on an
			// error with no route to pairing.
			if wantsHTML(r) {
				http.Redirect(w, r, "/pair", http.StatusSeeOther)
				return
			}

			// 401, not 403. This is an AUTHENTICATION failure — "I do not know
			// who you are" — and it must not be confused with the
			// authorization failure a read-only credential gets when it
			// attempts a write. Both were 403 before, so a client following the
			// obvious rule ("403 means signed out") would wipe a perfectly good
			// token the first time it hit a scope limit.
			//
			// WWW-Authenticate carries the reason in the form RFC 6750 defines,
			// so a client branches on a code rather than matching prose.
			code, reason, _ := authErrorCode(err)
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(
				`Bearer error=%q, error_description=%q, reason=%q`, code, err.Error(), reason))
			http.Error(w, "unauthenticated: "+err.Error(), http.StatusUnauthorized)
			return
		}
		// Tell the client how long its credential has left.
		//
		// Renewal existed but nothing called it, so every enrolled device was
		// quietly counting down to a lockout whose only recovery is restarting
		// the coordinator with --bootstrap — a trip to a terminal, impossible
		// from the phone that just expired. A client cannot renew in good time
		// without knowing when "good time" is, and it has no way to ask: the
		// token is opaque and /api/devices lists everyone's. So it rides on
		// every authenticated response and costs one header.
		if !id.Expiry.IsZero() {
			w.Header().Set("X-Shabadoo-Token-Expires", strconv.FormatInt(id.Expiry.Unix(), 10))
		}

		if id.Tenant == "" {
			// A provider that authenticates but names no tenant would read the
			// default tenant's data. Refuse rather than guess.
			http.Error(w, "forbidden: identity carries no tenant", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, id)))
	})
}

// Identify satisfies IdentityProvider for Cloudflare Access.
func (v *AccessVerifier) Identify(r *http.Request) (Identity, error) {
	token := r.Header.Get(accessHeader)
	if token == "" {
		return Identity{}, ErrNoAssertion
	}
	return v.Verify(r.Context(), token, time.Now())
}

func (v *AccessVerifier) Name() string { return "cloudflare-access" }

// ---------------------------------------------------------------------------
// device tokens — the iOS path
// ---------------------------------------------------------------------------

// A browser can complete an SSO redirect; a native app cannot do so cleanly on
// every launch, and it has somewhere safe to keep a long-lived secret (the
// Keychain). So the app enrols once and then presents a device token.
//
// Enrolment is deliberately human-gated: the coordinator mints a short code
// that someone already authenticated must read out to the app. There is no
// self-service path from "unauthenticated" to "enrolled device", because that
// path would be the whole security model's back door.

const (
	// enrolCodeTTL is how long a pairing code stays usable. Short, because it is
	// low-entropy enough to be read aloud.
	enrolCodeTTL = 5 * time.Minute

	// deviceTokenTTL bounds a device's credential. Long, because re-enrolling is
	// a manual act; revocation is the primary control, not expiry.
	deviceTokenTTL = 90 * 24 * time.Hour
)

var (
	ErrNoDeviceToken = errors.New("no device token")

	// Distinguished because a client acts differently on each: an EXPIRED token
	// means "this ran its course, pair again", while an invalid one means "this
	// credential is not known here" — a coordinator that was rebuilt, or a
	// device someone revoked. Telling a user the wrong one of those is the
	// difference between a routine re-pair and thinking they have been locked
	// out deliberately.
	//
	// Revocation deletes the row, so a revoked token is indistinguishable from
	// an unknown one and reports as invalid. Keeping tombstones purely to
	// separate them would mean retaining hashes of credentials that no longer
	// exist, which is a worse trade than the vaguer message.
	ErrBadDeviceToken     = errors.New("unknown or revoked device token")
	ErrExpiredDeviceToken = errors.New("device token has expired")

	ErrBadEnrolCode = errors.New("enrolment code is unknown or expired")
)

// authErrorCode maps an identity failure to the RFC 6750 error code a client
// should branch on, plus a short reason. Returns ok=false for anything that is
// not an authentication failure, so callers can tell 401 from 403.
func authErrorCode(err error) (code, reason string, ok bool) {
	switch {
	case errors.Is(err, ErrExpiredDeviceToken):
		return "invalid_token", "expired", true
	case errors.Is(err, ErrBadDeviceToken):
		return "invalid_token", "unknown", true
	case errors.Is(err, ErrNoDeviceToken), errors.Is(err, ErrNoAssertion):
		return "invalid_request", "missing", true
	case errors.Is(err, ErrBadAssertion):
		return "invalid_token", "assertion", true
	}
	return "", "", false
}

// Device is one enrolled client (a phone, usually).
type Device struct {
	ID       string
	Tenant   string
	Label    string
	Email    string
	Scope    string // "" = full, "read" = may not write
	Enrolled time.Time
	Expires  time.Time

	// PushToken is the APNs device token, empty until the app registers one.
	//
	// Updatable rather than set-once, because iOS reissues it on reinstall, on
	// restore from backup, and occasionally at the system's discretion. A
	// set-once field would mean a phone silently stopping receiving
	// notifications with nothing to indicate why.
	//
	// Never serialized: the device list is readable by every enrolled client,
	// and one phone has no business learning another's notification address.
	// Handlers expose whether a token is registered, not the token.
	PushToken string `json:"-"`

	// PushEnv is which APNs gateway this token belongs to: "development" or
	// "production". Empty means the client did not say.
	//
	// Apple runs two, and a token from one is meaningless to the other — a
	// production-only sender silently never delivers to a device that
	// registered from a debug build, with no error to notice. TestFlight and
	// App Store builds are production; Xcode builds are development, which is
	// exactly what the app is right now. Recorded at registration because that
	// is the only moment anyone knows, and a token stored without it is
	// ambiguous forever.
	PushEnv string `json:"push_env,omitempty"`
}

// DeviceStore issues and verifies device tokens.
//
// Tokens are held as SHA-256 hashes, so a dump of this map yields hashes rather
// than usable credentials. Lookup is by hash rather than a scan-and-compare:
// there is no per-byte comparison of the secret to leak timing, and the hash
// itself is not secret.
// The map is a cache in front of the devices table, not the record of truth:
// enrolments outlive the process, because a phone that has to re-pair after
// every `make deploy` is indistinguishable from a broken app.
type DeviceStore struct {
	mu      sync.Mutex
	byHash  map[string]Device
	codes   map[string]enrolment
	nowFunc func() time.Time
	store   *Store // nil = ephemeral (tests)

	// failures counts recent bad codes per caller, so /api/devices/redeem —
	// the only endpoint outside the identity middleware — cannot be ground
	// through at network speed.
	failures map[string]*attemptWindow
}

// attemptWindow is one caller's recent failed redemptions.
type attemptWindow struct {
	n     int
	until time.Time
}

const (
	// redeemFailureLimit is how many bad codes one caller may present before
	// being refused for the rest of the window. Generous enough that a person
	// fat-fingering an 8-character code is never locked out, small enough that
	// grinding is hopeless: at this rate the 32-bit space takes longer than the
	// five-minute life of any code by many orders of magnitude.
	redeemFailureLimit = 10

	// redeemFailureWindow is both the counting window and the cool-off.
	redeemFailureWindow = 15 * time.Minute
)

type enrolment struct {
	tenant  string
	email   string
	scope   string // carried from mint time onto the device that redeems it
	label   string // ditto: the OPERATOR names the device, not the device
	expires time.Time
}

// NewDeviceStore returns an in-memory store. Enrolments are lost on restart,
// which is fine for tests and for `shabadoo serve`, but not for a coordinator
// any app has paired with — use OpenDeviceStore for that.
func NewDeviceStore() *DeviceStore {
	return &DeviceStore{
		byHash:   map[string]Device{},
		codes:    map[string]enrolment{},
		failures: map[string]*attemptWindow{},
		nowFunc:  time.Now,
	}
}

// clientKey identifies the caller for throttling.
//
// Behind a reverse proxy every request arrives from the proxy's address, so
// keying on RemoteAddr alone would throttle all clients as one. Traefik sets
// X-Real-Ip from the connection it accepted and strips client-supplied
// X-Forwarded-* by default, which is what makes trusting it here reasonable.
//
// If this is ever fronted by a proxy that passes those headers through
// unsanitised, the key becomes attacker-controlled and the limiter degrades to
// per-proxy — annoying, not dangerous, since a code is still single-use and
// five-minute-lived. Worth knowing before changing what sits in front.
func clientKey(r *http.Request) string {
	if ip := strings.TrimSpace(r.Header.Get("X-Real-Ip")); ip != "" {
		return ip
	}
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		if i := strings.IndexByte(fwd, ','); i > 0 {
			return strings.TrimSpace(fwd[:i])
		}
		return strings.TrimSpace(fwd)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// throttled reports whether this caller has spent its failure budget, and
// records nothing — checking must not itself count as an attempt.
func (d *DeviceStore) throttled(key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	w, ok := d.failures[key]
	if !ok {
		return false
	}
	if d.now().After(w.until) {
		delete(d.failures, key)
		return false
	}
	return w.n >= redeemFailureLimit
}

// noteRedeemFailure counts a bad code against the caller.
func (d *DeviceStore) noteRedeemFailure(key string) int {
	d.mu.Lock()
	defer d.mu.Unlock()

	w, ok := d.failures[key]
	if !ok || d.now().After(w.until) {
		w = &attemptWindow{}
		d.failures[key] = w
	}
	w.n++
	// Each failure extends the window, so a slow grinder cannot wait out the
	// counter while still making steady progress.
	w.until = d.now().Add(redeemFailureWindow)
	return w.n
}

// clearRedeemFailures forgets a caller's failures after they succeed — someone
// who mistyped a code twice and then got it right is not a threat.
func (d *DeviceStore) clearRedeemFailures(key string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.failures, key)
}

// audit records an enrolment event when there is a database to record it in.
// Best effort by design: failing to write an audit row must not turn a working
// enrolment into a failed one, and the store is nil in tests.
func (d *DeviceStore) audit(ctx context.Context, tenant string, e AuditEntry) {
	if d.store == nil {
		return
	}
	if err := d.store.Tenant(tenant).Audit(ctx, e, d.now()); err != nil {
		log.Printf("hub: could not audit %s: %v", e.Action, err)
	}
}

// OpenDeviceStore loads persisted enrolments and writes new ones through to the
// database. Expired devices are dropped during the load.
func OpenDeviceStore(ctx context.Context, s *Store) (*DeviceStore, error) {
	d := NewDeviceStore()
	d.store = s
	loaded, err := s.LoadDevices(ctx, d.now())
	if err != nil {
		return nil, err
	}
	d.byHash = loaded
	return d, nil
}

// Count reports how many enrolments are live, for the startup log.
func (d *DeviceStore) Count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.byHash)
}

func (d *DeviceStore) now() time.Time { return d.nowFunc() }

// Bootstrap mints the first pairing code without a prior identity.
//
// Self-hosted has a chicken-and-egg problem: enrolling a device requires an
// authenticated human, and on a fresh install there is no way to become one.
// This breaks it exactly once, at startup, by printing a code to the service
// log — which is readable only by someone who can already read the host's
// journal, i.e. someone who could read the database anyway.
//
// It is not a back door: the code is single-use and expires like any other, and
// nothing re-mints it while the process runs.
func (d *DeviceStore) Bootstrap(tenant string) string {
	return d.NewEnrolCode(Identity{Tenant: tenant, Email: "bootstrap@" + tenant})
}

// NewEnrolCode mints a pairing code on behalf of an already-authenticated
// human. The code inherits that human's tenant — an app can never enrol into a
// tenant its operator does not already belong to.
func (d *DeviceStore) NewEnrolCode(id Identity) string {
	return d.NewScopedEnrolCode(id, ScopeFull)
}

// NewNamedEnrolCode mints a code that also fixes the device's NAME.
//
// Without this the device names itself at redeem time, so the operator's device
// list is populated by whatever each client chose to call itself — which is the
// list they later have to revoke from. Naming at mint time means the person
// granting access decides what the thing is called, and a client cannot
// overwrite it.
func (d *DeviceStore) NewNamedEnrolCode(id Identity, scope, label string) string {
	code := d.NewScopedEnrolCode(id, scope)
	d.mu.Lock()
	defer d.mu.Unlock()
	if e, ok := d.codes[code]; ok {
		e.label = strings.TrimSpace(label)
		d.codes[code] = e
	}
	return code
}

// NewScopedEnrolCode mints a code that will produce a device with the given
// scope. The scope is fixed at MINT time, by the already-authenticated human
// doing the enrolling — not requested by the device redeeming it, which would
// let a client choose its own permissions.
//
// A read-only credential cannot be escalated later either: changing scope means
// revoking and re-pairing, which is a visible act in the device list.
func (d *DeviceStore) NewScopedEnrolCode(id Identity, scope string) string {
	code := strings.ToUpper(newID()[:8])
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pruneLocked()
	d.codes[code] = enrolment{
		tenant:  id.Tenant,
		email:   id.Email,
		scope:   scope,
		expires: d.now().Add(enrolCodeTTL),
	}
	return code
}

// Redeem exchanges a pairing code for a device token, returned exactly once.
func (d *DeviceStore) Redeem(code, label string) (string, Device, error) {
	code = strings.ToUpper(strings.TrimSpace(code))

	d.mu.Lock()
	defer d.mu.Unlock()
	d.pruneLocked()

	e, ok := d.codes[code]
	if !ok || d.now().After(e.expires) {
		return "", Device{}, ErrBadEnrolCode
	}
	delete(d.codes, code) // single use

	token := newToken()
	// The label fixed at mint time wins. A client may suggest one — useful when
	// nobody bothered to name it — but it can never rename a device the operator
	// already labelled, or the device list stops meaning what the operator
	// thought it meant.
	if e.label != "" {
		label = e.label
	}
	if strings.TrimSpace(label) == "" {
		label = "unnamed device"
	}

	dev := Device{
		ID:       newID(),
		Tenant:   e.tenant,
		Label:    label,
		Email:    e.email,
		Scope:    e.scope,
		Enrolled: d.now(),
		Expires:  d.now().Add(deviceTokenTTL),
	}
	hash := hashToken(token)
	d.byHash[hash] = dev
	if d.store != nil {
		// A token handed to a phone that was never persisted would work until
		// the next restart and then fail mysteriously — better to refuse the
		// enrolment outright than to issue a credential with a hidden expiry.
		if err := d.store.SaveDevice(context.Background(), hash, dev); err != nil {
			delete(d.byHash, hash)
			return "", Device{}, fmt.Errorf("persist device: %w", err)
		}
	}
	return token, dev, nil
}

// Renew slides a live device's expiry forward by a full TTL.
//
// The credential is EXTENDED, not rotated. Rotation would bound the damage from
// a leaked token more tightly, but its failure mode is lockout: if the response
// carrying the new token is lost, the client is left holding one that has just
// been invalidated. Extending in place fails safe — a lost response means the
// client simply renews again later with a credential that still works. Given
// this exists specifically to stop people being locked out, that trade is the
// whole point.
//
// An ALREADY-EXPIRED token cannot be renewed: Identify rejects it before any
// handler runs, and resurrecting a dead credential is re-enrolment, not
// renewal. That is why the TTL is long and why clients should renew on launch
// rather than on expiry.
func (d *DeviceStore) Renew(deviceID string) (Device, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	for hash, dev := range d.byHash {
		if dev.ID != deviceID {
			continue
		}
		if d.now().After(dev.Expires) {
			return Device{}, ErrBadDeviceToken
		}

		renewed := dev
		renewed.Expires = d.now().Add(deviceTokenTTL)

		if d.store != nil {
			// Same rule Redeem follows: never report success for a credential
			// change that did not reach the database, or the client believes it
			// has months left and loses access at the next restart.
			if err := d.store.SaveDevice(context.Background(), hash, renewed); err != nil {
				return Device{}, fmt.Errorf("persist renewal: %w", err)
			}
		}
		d.byHash[hash] = renewed
		return renewed, nil
	}
	return Device{}, ErrBadDeviceToken
}

// SetPushToken records where to send this device's notifications.
//
// Keyed by device id and callable only by the device itself (see the handler),
// so one enrolled client cannot redirect another's notifications to a token it
// controls — which would turn a read-only credential into a way to intercept
// alerts.
func (d *DeviceStore) SetPushToken(deviceID, pushToken, env string) (Device, error) {
	switch env {
	case "development", "production":
	default:
		// Unknown or absent. Stored empty rather than guessed: a wrong gateway
		// fails silently, so "we do not know" has to stay distinguishable from
		// "we know it is production".
		env = ""
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	for hash, dev := range d.byHash {
		if dev.ID != deviceID {
			continue
		}
		updated := dev
		updated.PushToken = strings.TrimSpace(pushToken)
		updated.PushEnv = env

		if d.store != nil {
			// Same rule as Redeem and Renew: never report success for a
			// credential change that did not reach the database. A device that
			// believes it is registered for push and is not would simply stop
			// being notified, silently.
			if err := d.store.SaveDevice(context.Background(), hash, updated); err != nil {
				return Device{}, fmt.Errorf("persist push token: %w", err)
			}
		}
		d.byHash[hash] = updated
		return updated, nil
	}
	return Device{}, ErrBadDeviceToken
}

// PushTargets returns every device in a tenant that has registered for push.
// Used by the notifier; devices without a token are simply not reachable.
func (d *DeviceStore) PushTargets(tenant string) []Device {
	d.mu.Lock()
	defer d.mu.Unlock()

	var out []Device
	now := d.now()
	for _, dev := range d.byHash {
		if dev.Tenant == tenant && dev.PushToken != "" && now.Before(dev.Expires) {
			out = append(out, dev)
		}
	}
	return out
}

// Identify satisfies IdentityProvider for app clients.
func (d *DeviceStore) Identify(r *http.Request) (Identity, error) {
	tok := bearer(r)
	if tok == "" {
		return Identity{}, ErrNoDeviceToken
	}
	d.mu.Lock()
	dev, ok := d.byHash[hashToken(tok)]
	d.mu.Unlock()
	if !ok {
		return Identity{}, ErrBadDeviceToken
	}
	if d.now().After(dev.Expires) {
		return Identity{}, ErrExpiredDeviceToken
	}
	return Identity{
		Tenant: dev.Tenant,
		Email:  dev.Email,
		Label:  dev.Label,
		Sub:    "device:" + dev.ID,
		Expiry: dev.Expires,
		Scope:  dev.Scope,
	}, nil
}

func (d *DeviceStore) Name() string { return "device-token" }

// Revoke drops a device. This is the primary control on a lost phone, which is
// why the token TTL can afford to be long.
func (d *DeviceStore) Revoke(deviceID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for hash, dev := range d.byHash {
		if dev.ID == deviceID {
			delete(d.byHash, hash)
			if d.store != nil {
				// Revocation is the primary control on a lost phone, so a failed
				// delete must be loud: the in-memory drop already took effect,
				// but the row would resurrect the device on the next restart.
				if err := d.store.DeleteDevice(context.Background(), deviceID); err != nil {
					log.Printf("hub: device %s revoked in memory but not in the "+
						"database (%v) — it will return on restart; retry the revoke", deviceID, err)
				}
			}
			return true
		}
	}
	return false
}

// List returns a tenant's enrolled devices, newest first.
func (d *DeviceStore) List(tenant string) []Device {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := []Device{}
	for _, dev := range d.byHash {
		if dev.Tenant == tenant {
			out = append(out, dev)
		}
	}
	return out
}

func (d *DeviceStore) pruneLocked() {
	now := d.now()
	for code, e := range d.codes {
		if now.After(e.expires) {
			delete(d.codes, code)
		}
	}
	// Expired failure windows too: one entry per distinct caller address would
	// otherwise accumulate for the life of the process.
	for key, w := range d.failures {
		if now.After(w.until) {
			delete(d.failures, key)
		}
	}
}

func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// bearer pulls a token from the Authorization header.
// wantsHTML reports whether this looks like a person navigating rather than a
// client calling the API. Deliberately narrow: a GET that asks for HTML. The
// dashboard's own fetches send no Accept: text/html, so they still receive a
// 403 they can render, rather than a redirect body they would try to parse.
func wantsHTML(r *http.Request) bool {
	return r.Method == http.MethodGet &&
		strings.Contains(r.Header.Get("Accept"), "text/html")
}

// TokenCookie carries a device token for browsers.
//
// An app or the CLI sends `Authorization: Bearer`, because it has somewhere to
// keep a secret and code to attach it. A browser has neither: the dashboard is
// a static page, so before this the only way it could have authenticated was to
// read a token out of localStorage and add a header to every fetch — which puts
// the credential where any script on the page can read it. A cookie the
// JavaScript cannot touch is strictly better, and it survives a reload, which
// is the property that was actually missing.
const TokenCookie = "shabadoo_token"

// bearer extracts the caller's device token: the Authorization header first,
// then the cookie. Header first so an app or CLI that sends both is never
// surprised by a stale cookie from the same browser profile.
func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return h[7:]
	}
	if c, err := r.Cookie(TokenCookie); err == nil {
		return c.Value
	}
	return ""
}

// setTokenCookie hands a browser its credential.
//
// HttpOnly: script cannot read it, so an injection on the dashboard cannot
// exfiltrate the token. SameSite=Lax: sent on top-level navigation but NOT on
// cross-site POSTs, which is the CSRF guard for every write endpoint — and it
// pairs with the API's insistence on Content-Type: application/json, which a
// cross-origin form post cannot set without a preflight this server never
// answers.
//
// Secure is set only when the request actually arrived over TLS. The deployment
// is HTTPS behind Traefik, but `serve`/`hub` on loopback during development is
// not, and a Secure cookie there would simply never come back — an
// authentication failure with no visible cause.
func setTokenCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	https := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	http.SetCookie(w, &http.Cookie{
		Name:     TokenCookie,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   https,
		SameSite: http.SameSiteLaxMode,
	})
}

// ---------------------------------------------------------------------------
// composition and development
// ---------------------------------------------------------------------------

// AnyOf tries each provider in order and returns the first identity. This is
// how one coordinator serves both a browser (Access) and the iOS app (device
// token) on the same endpoints.
//
// ⚠️ A chain is exactly as strong as its weakest member. A request rejected by
// one provider falls through to the next, so adding a permissive provider does
// not narrow access — it widens it, for every request. In particular
// AnyOf{DeviceStore, InsecureProvider} admits any loopback caller whose device
// token was *invalid*, which is intended for development and catastrophic in
// production. Order does not save you here; membership is what matters.
type AnyOf []IdentityProvider

func (a AnyOf) Identify(r *http.Request) (Identity, error) {
	var lastErr error = errors.New("no identity provider matched")
	for _, p := range a {
		id, err := p.Identify(r)
		if err == nil {
			return id, nil
		}
		lastErr = err
	}
	return Identity{}, lastErr
}

func (a AnyOf) Name() string {
	names := make([]string, len(a))
	for i, p := range a {
		names[i] = p.Name()
	}
	return strings.Join(names, "+")
}

// InsecureProvider authenticates nobody and admits everybody as the default
// tenant. It exists so the coordinator can be run locally without Cloudflare.
//
// It refuses any request that did not arrive over loopback. That is not real
// security — a proxy can forge a source address — but it converts the most
// likely accident, starting a dev coordinator on a routable address and
// forgetting, from a silent compromise into an obvious failure.
type InsecureProvider struct{}

func (InsecureProvider) Identify(r *http.Request) (Identity, error) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return Identity{}, errors.New(
			"insecure mode serves loopback only; this request came from " + host)
	}
	return Identity{Tenant: DefaultTenant, Email: "insecure@localhost"}, nil
}

func (InsecureProvider) Name() string { return "insecure" }

// PublicRoutes registers the endpoints that must be reachable without an
// existing identity. There is exactly one: redeeming a pairing code, because
// an enrolling app has no credential yet by definition.
//
// It is safe only because the code is short-lived, single-use, and could only
// have been minted by an already-authenticated human.
// QREncoder renders a payload as an SVG QR. Injected by main so the hub package
// does not depend on the encoder — it is a rendering detail, and the coordinator
// should not fail to build because a QR library does not compile.
var QREncoder func(string) ([]byte, error)

// PairPage is the enrolment page's HTML, injected by the caller (it lives in
// the embedded static tree, which this package cannot see). Empty disables the
// route.
var PairPage []byte

func (d *DeviceStore) PublicRoutes(mux *http.ServeMux) {
	// Served outside the identity middleware for the same reason redeem is: a
	// device that is enrolling has no credential yet. The page itself carries
	// no data — the pairing code is the secret, and it does not come from here.
	if len(PairPage) > 0 {
		mux.HandleFunc("GET /pair", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.Write(PairPage)
		})
	}

	// A QR of the pairing URL, so a code minted at a terminal can be scanned off
	// a screen. Unauthenticated for the same reason the page is: whoever holds
	// the code already holds the only secret, and encoding it reveals nothing
	// they did not type in. The payload is supplied by the caller and never
	// stored, so this cannot leak a code it was not given.
	if QREncoder != nil {
		mux.HandleFunc("GET /pair/qr.svg", func(w http.ResponseWriter, r *http.Request) {
			data := r.URL.Query().Get("d")
			if data == "" || len(data) > 512 {
				http.Error(w, "d= is required and must be short", http.StatusBadRequest)
				return
			}
			svg, err := QREncoder(data)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "image/svg+xml")
			w.Header().Set("Cache-Control", "no-store")
			w.Write(svg)
		})
	}

	mux.HandleFunc("POST /api/devices/redeem", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Code  string `json:"code"`
			Label string `json:"label"`
		}
		if !readJSON(w, r, &req) {
			return
		}
		// This is the only endpoint outside the identity middleware, so it is
		// the only one an unauthenticated caller can reach. Until this existed
		// it had no rate limit and left no trace: an attempt to grind pairing
		// codes was both unbounded and invisible.
		key := clientKey(r)
		if d.throttled(key) {
			w.Header().Set("Retry-After", strconv.Itoa(int(redeemFailureWindow.Seconds())))
			http.Error(w, "too many failed attempts", http.StatusTooManyRequests)
			return
		}

		token, dev, err := d.Redeem(req.Code, req.Label)
		if err != nil {
			n := d.noteRedeemFailure(key)
			// Audited to the default tenant: a bad code names no tenant, so
			// there is no other place to put it. In a single-tenant deployment
			// that is simply "the" audit log.
			d.audit(r.Context(), DefaultTenant, AuditEntry{
				Actor:  "redeem:" + key,
				Action: "device.redeem.failed",
				Detail: fmt.Sprintf("attempt %d of %d before lockout", n, redeemFailureLimit),
			})
			if n >= redeemFailureLimit {
				log.Printf("hub: %s locked out of device enrolment after %d failed codes", key, n)
			}
			// Uniform error: distinguishing "unknown" from "expired" would help
			// someone grinding codes.
			http.Error(w, "invalid or expired code", http.StatusUnauthorized)
			return
		}
		d.clearRedeemFailures(key)
		d.audit(r.Context(), dev.Tenant, AuditEntry{
			Actor:  "redeem:" + key,
			Action: "device.redeem",
			Target: dev.ID,
			Detail: dev.Label,
		})
		// Also hand it back as a cookie, so a browser that just paired is
		// authenticated for the dashboard without the page having to store the
		// token itself. An app ignores this and keeps the token below.
		setTokenCookie(w, r, token, dev.Expires)

		writeJSON(w, map[string]any{
			"token":     token, // returned exactly once; the app stores it in the Keychain
			"device_id": dev.ID,
			"tenant":    dev.Tenant,
			"expires":   dev.Expires.Unix(),
			// What this credential actually turned out to be. A client should not
			// have to infer its own permissions by attempting a write and reading
			// the 403 — and the label is what the OPERATOR named it, which may
			// differ from whatever the client suggested.
			"scope": dev.Scope,
			"label": dev.Label,
		})
	})
}
