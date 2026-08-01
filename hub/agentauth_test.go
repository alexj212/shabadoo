package hub

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// testKey makes an ed25519 SSH keypair and the signer for it, standing in for
// the key an agent would hold in ssh-agent.
func testKey(t *testing.T) (ssh.PublicKey, ssh.Signer) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer.PublicKey(), signer
}

// authorizedLine renders a key the way authorized_agents stores it.
func authorizedLine(pub ssh.PublicKey, node string) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))) + " " + node + "\n"
}

func signChallenge(t *testing.T, signer ssh.Signer, c Challenge) []byte {
	t.Helper()
	sig, err := signer.Sign(rand.Reader, c.blob())
	if err != nil {
		t.Fatal(err)
	}
	return ssh.Marshal(sig)
}

func TestVerifyAcceptsAuthorizedAgent(t *testing.T) {
	pub, signer := testKey(t)
	agents, err := ParseAuthorizedAgents([]byte(authorizedLine(pub, "wsl")))
	if err != nil {
		t.Fatal(err)
	}
	auth := NewAuthorizer(agents)

	now := time.Now()
	c, err := auth.Issue(now)
	if err != nil {
		t.Fatal(err)
	}
	got, err := auth.Verify(c, pub.Marshal(), signChallenge(t, signer, c), now)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.Name != "wsl" {
		t.Errorf("agent = %q, want wsl", got.Name)
	}
}

// A key that verifies perfectly but isn't listed must not authenticate — this
// is the whole trust boundary for the agent plane.
func TestVerifyRejectsUnlistedKey(t *testing.T) {
	listed, _ := testKey(t)
	stranger, strangerSigner := testKey(t)

	agents, err := ParseAuthorizedAgents([]byte(authorizedLine(listed, "wsl")))
	if err != nil {
		t.Fatal(err)
	}
	auth := NewAuthorizer(agents)

	now := time.Now()
	c, _ := auth.Issue(now)
	_, err = auth.Verify(c, stranger.Marshal(), signChallenge(t, strangerSigner, c), now)
	if !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("err = %v, want ErrUnknownKey", err)
	}
}

// A listed agent signing with the wrong key, or tampering with the signed
// bytes, must fail.
func TestVerifyRejectsBadSignature(t *testing.T) {
	pub, _ := testKey(t)
	_, otherSigner := testKey(t)

	agents, _ := ParseAuthorizedAgents([]byte(authorizedLine(pub, "wsl")))
	auth := NewAuthorizer(agents)

	now := time.Now()
	c, _ := auth.Issue(now)
	// Signature made by a different key, presented as the listed key's.
	_, err := auth.Verify(c, pub.Marshal(), signChallenge(t, otherSigner, c), now)
	if !errors.Is(err, ErrBadSignature) {
		t.Fatalf("err = %v, want ErrBadSignature", err)
	}
}

// The signed blob is built from the challenge fields, so altering any of them
// after signing invalidates the signature rather than being ignored.
func TestVerifyRejectsAlteredChallenge(t *testing.T) {
	pub, signer := testKey(t)
	agents, _ := ParseAuthorizedAgents([]byte(authorizedLine(pub, "wsl")))
	auth := NewAuthorizer(agents)

	now := time.Now()
	c, _ := auth.Issue(now)
	sig := signChallenge(t, signer, c)

	altered := c
	altered.Timestamp = c.Timestamp + 1 // still inside the window, but not what was signed

	if _, err := auth.Verify(altered, pub.Marshal(), sig, now); err == nil {
		t.Fatal("altered challenge verified; the timestamp is not covered by the signature")
	}
}

// A captured challenge+signature must not work twice.
func TestVerifyRejectsReplay(t *testing.T) {
	pub, signer := testKey(t)
	agents, _ := ParseAuthorizedAgents([]byte(authorizedLine(pub, "wsl")))
	auth := NewAuthorizer(agents)

	now := time.Now()
	c, _ := auth.Issue(now)
	sig := signChallenge(t, signer, c)

	if _, err := auth.Verify(c, pub.Marshal(), sig, now); err != nil {
		t.Fatalf("first Verify: %v", err)
	}
	if _, err := auth.Verify(c, pub.Marshal(), sig, now); !errors.Is(err, ErrStaleNonce) {
		t.Fatalf("replay err = %v, want ErrStaleNonce", err)
	}
}

// A failed signature still consumes the nonce, so an attacker cannot hold a
// challenge open by guessing against it.
func TestFailedVerifyConsumesNonce(t *testing.T) {
	pub, signer := testKey(t)
	_, otherSigner := testKey(t)
	agents, _ := ParseAuthorizedAgents([]byte(authorizedLine(pub, "wsl")))
	auth := NewAuthorizer(agents)

	now := time.Now()
	c, _ := auth.Issue(now)

	if _, err := auth.Verify(c, pub.Marshal(), signChallenge(t, otherSigner, c), now); err == nil {
		t.Fatal("bad signature accepted")
	}
	// Now present the correct signature for the same challenge.
	if _, err := auth.Verify(c, pub.Marshal(), signChallenge(t, signer, c), now); !errors.Is(err, ErrStaleNonce) {
		t.Fatalf("err = %v, want ErrStaleNonce — a burned nonce must stay burned", err)
	}
}

func TestVerifyRejectsExpiredChallenge(t *testing.T) {
	pub, signer := testKey(t)
	agents, _ := ParseAuthorizedAgents([]byte(authorizedLine(pub, "wsl")))
	auth := NewAuthorizer(agents)

	issued := time.Now()
	c, _ := auth.Issue(issued)
	sig := signChallenge(t, signer, c)

	late := issued.Add(challengeTTL + time.Second)
	if _, err := auth.Verify(c, pub.Marshal(), sig, late); !errors.Is(err, ErrStaleNonce) {
		t.Fatalf("err = %v, want ErrStaleNonce", err)
	}
}

// A signature produced for another SSHSIG namespace (git signing, say) must not
// authenticate here even though the same key made it.
func TestVerifyRejectsWrongNamespace(t *testing.T) {
	pub, signer := testKey(t)
	agents, _ := ParseAuthorizedAgents([]byte(authorizedLine(pub, "wsl")))
	auth := NewAuthorizer(agents)

	now := time.Now()
	c, _ := auth.Issue(now)
	c.Namespace = "git"
	sig := signChallenge(t, signer, c)

	if _, err := auth.Verify(c, pub.Marshal(), sig, now); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("err = %v, want ErrBadSignature", err)
	}
}

func TestParseAuthorizedAgents(t *testing.T) {
	a, _ := testKey(t)
	b, _ := testKey(t)

	src := "# the flock\n\n" + authorizedLine(a, "wsl") + authorizedLine(b, "mac")
	agents, err := ParseAuthorizedAgents([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 2 || agents[0].Name != "wsl" || agents[1].Name != "mac" {
		t.Fatalf("agents = %+v", agents)
	}
}

// A key with no comment has no node name. Defaulting it would let that key
// claim another node's sessions, so it is a hard error.
func TestParseRejectsKeyWithoutNodeName(t *testing.T) {
	pub, _ := testKey(t)
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))) + "\n"
	if _, err := ParseAuthorizedAgents([]byte(line)); err == nil {
		t.Fatal("key with no node name was accepted")
	}
}

func TestParseRejectsDuplicateNode(t *testing.T) {
	a, _ := testKey(t)
	b, _ := testKey(t)
	src := authorizedLine(a, "wsl") + authorizedLine(b, "wsl")
	if _, err := ParseAuthorizedAgents([]byte(src)); err == nil {
		t.Fatal("duplicate node name was accepted; two keys could claim the same node")
	}
}

func TestParseRejectsEmpty(t *testing.T) {
	if _, err := ParseAuthorizedAgents([]byte("# nothing here\n")); err == nil {
		t.Fatal("empty key set accepted")
	}
}

// Nonces must not accumulate for the process lifetime.
func TestIssuePrunesExpiredNonces(t *testing.T) {
	pub, _ := testKey(t)
	agents, _ := ParseAuthorizedAgents([]byte(authorizedLine(pub, "wsl")))
	auth := NewAuthorizer(agents)

	start := time.Now()
	for range 50 {
		auth.Issue(start)
	}
	if got := len(auth.issued); got != 50 {
		t.Fatalf("issued = %d, want 50", got)
	}
	auth.Issue(start.Add(challengeTTL + time.Second))
	if got := len(auth.issued); got != 1 {
		t.Fatalf("issued = %d after prune, want 1", got)
	}
}

// Adding a node should be an edit to authorized_agents, not a coordinator
// restart — a restart disconnects every agent already connected, so making
// people pay it to add one machine is how a two-node setup stays a one-node
// setup.
func TestAuthorizerReloadsChangedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_agents")

	keyA, signerA := testKey(t)
	keyB, signerB := testKey(t)
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		// Stat granularity is coarse enough that two writes in the same tick can
		// look identical; make the change unambiguous.
		old := time.Now().Add(-2 * time.Second)
		os.Chtimes(path, old, time.Now().Add(time.Duration(len(body))*time.Millisecond))
	}

	write(authorizedLine(keyA, "wsl"))
	a, err := NewAuthorizerFromFile(path)
	if err != nil {
		t.Fatalf("NewAuthorizerFromFile: %v", err)
	}

	login := func(signer ssh.Signer) error {
		t.Helper()
		c, err := a.Issue(time.Now())
		if err != nil {
			return err
		}
		_, err = a.Verify(c, signer.PublicKey().Marshal(), signChallenge(t, signer, c), time.Now())
		return err
	}

	if err := login(signerA); err != nil {
		t.Fatalf("key A should authenticate: %v", err)
	}
	if err := login(signerB); err == nil {
		t.Fatal("key B authenticated before being added")
	}

	// Add the second machine.
	write(authorizedLine(keyA, "wsl") + authorizedLine(keyB, "mac"))
	if err := login(signerB); err != nil {
		t.Fatalf("key B should authenticate after being added: %v", err)
	}
	if a.Count() != 2 {
		t.Errorf("Count() = %d, want 2", a.Count())
	}

	// A half-written edit must not lock everyone out: the previous set stands.
	write("ssh-ed25519 this-is-not-a-key wsl\n")
	if err := login(signerA); err != nil {
		t.Errorf("unparseable file dropped a working key: %v", err)
	}

	// Removing a key takes effect at the next login.
	write(authorizedLine(keyA, "wsl"))
	if err := login(signerB); err == nil {
		t.Error("revoked key B still authenticates")
	}
	if err := login(signerA); err != nil {
		t.Errorf("key A should still authenticate: %v", err)
	}
}
