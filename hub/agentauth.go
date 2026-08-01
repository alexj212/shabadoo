// Package hub is the coordinator: it authenticates agents and humans,
// classifies inbound messages to the agent that owns them, and holds the
// durable state (sessions, tasks, messages, audit) that the flock used to keep
// scattered across NATS and per-host peer lists.
//
// This file is the agent plane: nodes prove who they are with an SSH key.
package hub

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// sigNamespace scopes every signature to this protocol. SSHSIG namespaces are
// what stop a signature produced for one purpose being replayed as another —
// without it, anything else that signs with the same agent key (git commit
// signing, `ssh-keygen -Y sign`) could be replayed as a shabadoo login.
const sigNamespace = "shabadoo-v1"

// challengeTTL bounds how long a signed challenge stays valid. Long enough to
// survive a slow ssh-agent (a hardware key may prompt for touch), short enough
// that a captured signature is useless by the time it could be replayed.
const challengeTTL = 60 * time.Second

var (
	ErrUnknownKey   = errors.New("public key is not in authorized_agents")
	ErrBadSignature = errors.New("signature does not verify")
	ErrStaleNonce   = errors.New("challenge expired or already used")
)

// Agent is one authorized node: a node name bound to a public key, within
// a tenant.
type Agent struct {
	Tenant  string // owner; DefaultTenant when the key names no tenant
	Name    string // node name, matching SHABADOO_NODE (wsl, mac, dm)
	Key     ssh.PublicKey
	Comment string
}

// Authorizer verifies agent logins against a fixed set of public keys.
//
// The key set is the entire trust root for the agent plane: a key in the file
// can drive every Claude pane on the node it names. Revocation is deleting a
// line — there is no other list to update, and no token to expire.
type Authorizer struct {
	mu     sync.Mutex
	agents []Agent
	issued map[string]challenge // nonce -> challenge, pruned on use and on issue

	// Source file, when the set was loaded from one. Re-read when it changes so
	// that adding a machine is editing a file, not restarting the coordinator —
	// a restart drops every connected agent and every human's in-flight request
	// to add one node.
	path    string
	modTime time.Time
	size    int64
}

type challenge struct {
	expires time.Time
}

// NewAuthorizer builds an Authorizer over a fixed key set. The set never
// changes — use NewAuthorizerFromFile to track a file.
func NewAuthorizer(agents []Agent) *Authorizer {
	return &Authorizer{agents: agents, issued: map[string]challenge{}}
}

// NewAuthorizerFromFile loads the key set from path and re-reads it whenever
// the file changes, checked at login. Logins are rare — an agent connects once
// and holds the stream — so this costs one stat per connection attempt.
func NewAuthorizerFromFile(path string) (*Authorizer, error) {
	agents, err := LoadAuthorizedAgents(path)
	if err != nil {
		return nil, err
	}
	a := &Authorizer{agents: agents, issued: map[string]challenge{}, path: path}
	if st, err := os.Stat(path); err == nil {
		a.modTime, a.size = st.ModTime(), st.Size()
	}
	return a, nil
}

// Count reports how many keys are currently trusted.
func (a *Authorizer) Count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.agents)
}

// refreshLocked re-reads the key file if it has changed since the last load.
//
// A file that fails to parse leaves the previous set in place: a half-written
// edit must not lock out every agent, and the failure is logged rather than
// returned so a bad edit degrades to "your change has not taken effect" instead
// of "nothing can connect".
func (a *Authorizer) refreshLocked() {
	if a.path == "" {
		return
	}
	st, err := os.Stat(a.path)
	if err != nil {
		return // the file being briefly absent is not a reason to forget the set
	}
	if st.ModTime().Equal(a.modTime) && st.Size() == a.size {
		return
	}
	a.modTime, a.size = st.ModTime(), st.Size()

	agents, err := LoadAuthorizedAgents(a.path)
	if err != nil {
		log.Printf("hub: %s changed but does not parse (%v) — keeping the previous %d key(s)",
			a.path, err, len(a.agents))
		return
	}
	if len(agents) != len(a.agents) {
		log.Printf("hub: reloaded %s: %d key(s), was %d", a.path, len(agents), len(a.agents))
	}
	a.agents = agents
}

// LoadAuthorizedAgents reads an authorized_agents file. The format is
// authorized_keys with the comment field carrying the node name, optionally
// prefixed by a tenant:
//
//	ssh-ed25519 AAAAC3Nz... wsl           # self-hosted: the default tenant
//	ssh-ed25519 AAAAC3Nz... alex/wsl      # hosted: tenant "alex", node "wsl"
//
// A key with no comment is rejected rather than defaulted: an agent whose node
// name we had to guess could impersonate another node's sessions.
func LoadAuthorizedAgents(path string) ([]Agent, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseAuthorizedAgents(raw)
}

func ParseAuthorizedAgents(raw []byte) ([]Agent, error) {
	var agents []Agent
	seen := map[string]string{} // node name -> key fingerprint

	rest := raw
	for len(rest) > 0 {
		// Skip blank lines and comments before handing the rest to the parser;
		// ParseAuthorizedKey stops at the first key it finds.
		trimmed := strings.TrimLeft(string(rest), " \t\r\n")
		if strings.HasPrefix(trimmed, "#") {
			_, after, found := strings.Cut(trimmed, "\n")
			if !found {
				break
			}
			rest = []byte(after)
			continue
		}
		if trimmed == "" {
			break
		}

		key, comment, _, next, err := ssh.ParseAuthorizedKey([]byte(trimmed))
		if err != nil {
			return nil, fmt.Errorf("authorized_agents: %w", err)
		}
		label := strings.TrimSpace(comment)
		if label == "" {
			return nil, fmt.Errorf("authorized_agents: key %s has no node name in its comment field",
				ssh.FingerprintSHA256(key))
		}
		tenant, name := DefaultTenant, label
		if before, after, found := strings.Cut(label, "/"); found {
			tenant, name = strings.TrimSpace(before), strings.TrimSpace(after)
			if tenant == "" || name == "" {
				return nil, fmt.Errorf("authorized_agents: key %s has a malformed tenant/node label %q",
					ssh.FingerprintSHA256(key), label)
			}
		}
		// Uniqueness is per tenant: two tenants may each have a node called
		// "wsl", and they must not collide.
		if prev, dup := seen[label]; dup {
			return nil, fmt.Errorf("authorized_agents: node %q listed twice (%s and %s)",
				label, prev, ssh.FingerprintSHA256(key))
		}
		seen[label] = ssh.FingerprintSHA256(key)
		agents = append(agents, Agent{Tenant: tenant, Name: name, Key: key, Comment: label})
		rest = next
	}
	if len(agents) == 0 {
		return nil, errors.New("authorized_agents: no keys found")
	}
	return agents, nil
}

// Challenge is what the coordinator sends an agent to sign.
type Challenge struct {
	Nonce     string `json:"nonce"`     // base64, 32 random bytes
	Timestamp int64  `json:"timestamp"` // unix seconds, coordinator clock
	Namespace string `json:"namespace"` // always sigNamespace
}

// blob is the exact byte sequence that gets signed. It is built from the
// challenge fields rather than transported as an opaque string so a malicious
// agent cannot get us to verify a signature over bytes of its choosing.
func (c Challenge) blob() []byte {
	return fmt.Appendf(nil, "%s\n%s\n%d\n", c.Namespace, c.Nonce, c.Timestamp)
}

// Issue mints a challenge and records its nonce as pending.
func (a *Authorizer) Issue(now time.Time) (Challenge, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return Challenge{}, err
	}
	c := Challenge{
		Nonce:     base64.StdEncoding.EncodeToString(buf),
		Timestamp: now.Unix(),
		Namespace: sigNamespace,
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	a.pruneLocked(now)
	a.issued[c.Nonce] = challenge{expires: now.Add(challengeTTL)}
	return c, nil
}

// pruneLocked drops expired nonces. Called on every issue, so the map stays
// bounded by the number of logins attempted within one TTL rather than growing
// for the process's lifetime.
func (a *Authorizer) pruneLocked(now time.Time) {
	for nonce, ch := range a.issued {
		if now.After(ch.expires) {
			delete(a.issued, nonce)
		}
	}
}

// Verify checks a signed challenge and returns the agent it authenticates.
//
// The order of checks matters: the nonce is consumed before the signature is
// verified, so a replayed challenge fails even if its signature is perfectly
// valid. `sig` is an SSH wire-format signature (ssh.Signature marshalled),
// which is what ssh-agent returns.
func (a *Authorizer) Verify(c Challenge, pubKey, sig []byte, now time.Time) (Agent, error) {
	if c.Namespace != sigNamespace {
		return Agent{}, fmt.Errorf("%w: wrong namespace %q", ErrBadSignature, c.Namespace)
	}

	// Reject a challenge whose timestamp is outside the window even if we still
	// hold the nonce — a coordinator restart must not widen the window.
	skew := now.Sub(time.Unix(c.Timestamp, 0))
	if skew < -challengeTTL || skew > challengeTTL {
		return Agent{}, ErrStaleNonce
	}

	a.mu.Lock()
	ch, pending := a.issued[c.Nonce]
	if pending {
		delete(a.issued, c.Nonce) // single use, consumed whether or not the signature holds
	}
	a.mu.Unlock()

	if !pending || now.After(ch.expires) {
		return Agent{}, ErrStaleNonce
	}

	key, err := ssh.ParsePublicKey(pubKey)
	if err != nil {
		return Agent{}, fmt.Errorf("%w: %v", ErrBadSignature, err)
	}

	agent, ok := a.lookup(key)
	if !ok {
		return Agent{}, ErrUnknownKey
	}

	var parsed ssh.Signature
	if err := ssh.Unmarshal(sig, &parsed); err != nil {
		return Agent{}, fmt.Errorf("%w: %v", ErrBadSignature, err)
	}
	if err := key.Verify(c.blob(), &parsed); err != nil {
		return Agent{}, fmt.Errorf("%w: %v", ErrBadSignature, err)
	}
	return agent, nil
}

// lookup finds the agent holding this key. Comparison is over the marshalled
// wire bytes — the same key presented in a different text encoding still
// matches, and a different key never does.
func (a *Authorizer) lookup(key ssh.PublicKey) (Agent, bool) {
	want := key.Marshal()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refreshLocked()
	for _, ag := range a.agents {
		if ag.Key.Type() == key.Type() && subtleEqual(ag.Key.Marshal(), want) {
			return ag, true
		}
	}
	return Agent{}, false
}

// subtleEqual compares in constant time. Public keys are not secret, but the
// habit is cheap and this function is one refactor away from comparing
// something that is.
func subtleEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := range a {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

// Agents returns the authorized set, for the doctor/status surfaces.
func (a *Authorizer) Agents() []Agent {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]Agent, len(a.agents))
	copy(out, a.agents)
	return out
}
