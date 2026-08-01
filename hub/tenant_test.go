package hub

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// twoTenants returns handles for two tenants sharing one database — the hosted
// arrangement.
func twoTenants(t *testing.T) (*Tenant, *Tenant) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s.Tenant("alex"), s.Tenant("someone-else")
}

// The isolation boundary. In a hosted deployment this single property is what
// separates customers; every assertion below is a way it could leak.
func TestTenantsAreIsolated(t *testing.T) {
	ctx, now := context.Background(), time.Now()
	alex, other := twoTenants(t)

	alex.Send(ctx, Envelope{ToSession: "sess", Body: "alex's secret"}, now)
	alex.UpsertSession(ctx, Session{SessionID: "sess", Agent: "wsl", Project: "iptv"}, now)
	alex.Audit(ctx, AuditEntry{Actor: "alex@example.com", Action: "send", Target: "wsl"}, now)
	alex.Subscribe(ctx, "sess", "deploys")

	t.Run("mail", func(t *testing.T) {
		// The other tenant has a session with the *same id* — the worst case,
		// since nothing but the tenant column distinguishes them.
		got, err := other.Drain(ctx, "sess", now)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("tenant read %d of another tenant's messages: %+v", len(got), got)
		}
		if n, _ := other.Pending(ctx, "sess", now); n != 0 {
			t.Errorf("pending = %d, want 0", n)
		}
	})

	t.Run("sessions", func(t *testing.T) {
		got, err := other.ListSessions(ctx, now)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("tenant saw %d of another tenant's sessions", len(got))
		}
	})

	t.Run("audit", func(t *testing.T) {
		got, err := other.AuditTail(ctx, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("tenant read %d of another tenant's audit entries", len(got))
		}
	})

	t.Run("timeline", func(t *testing.T) {
		got, err := other.Replay(ctx, 100, now.Add(-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("tenant replayed %d of another tenant's messages", len(got))
		}
		conv, _ := other.Conversation(ctx, "sess", 100)
		if len(conv) != 0 {
			t.Fatalf("tenant read %d messages of another tenant's thread", len(conv))
		}
	})

	t.Run("subscriptions", func(t *testing.T) {
		topics, err := other.Topics(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(topics) != 0 {
			t.Fatalf("tenant saw another tenant's topics: %v", topics)
		}
	})

	t.Run("stats", func(t *testing.T) {
		st, err := other.Stats(ctx, now)
		if err != nil {
			t.Fatal(err)
		}
		if st.Sessions != 0 || st.Messages != 0 || st.Pending != 0 || st.AuditCount != 0 {
			t.Fatalf("stats leaked across tenants: %+v", st)
		}
	})

	// And the owner still sees everything — isolation must not mean loss.
	t.Run("owner still sees its own", func(t *testing.T) {
		if n, _ := alex.Pending(ctx, "sess", now); n != 1 {
			t.Errorf("owner pending = %d, want 1", n)
		}
		sessions, _ := alex.ListSessions(ctx, now)
		if len(sessions) != 1 {
			t.Errorf("owner sessions = %d, want 1", len(sessions))
		}
		entries, _ := alex.AuditTail(ctx, 100)
		if len(entries) != 1 {
			t.Errorf("owner audit = %d, want 1", len(entries))
		}
	})
}

// A broadcast must never reach another tenant's subscriber, even when both
// tenants use the same topic name. Fresh handles so this test does not depend
// on what any other test wrote.
func TestBroadcastDoesNotCrossTenants(t *testing.T) {
	ctx, now := context.Background(), time.Now()
	alex, other := twoTenants(t)

	alex.Subscribe(ctx, "alex-session", "deploys")
	other.Subscribe(ctx, "other-session", "deploys")

	_, n, err := alex.Broadcast(ctx, Envelope{Topic: "deploys", Body: "alex only"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("fanned out to %d, want exactly this tenant's 1 subscriber", n)
	}
	got, _ := other.Drain(ctx, "other-session", now)
	if len(got) != 0 {
		t.Fatalf("other tenant received %d cross-tenant broadcasts", len(got))
	}
}

// Dropping one tenant's agent must not touch another tenant's identically
// named agent — both may legitimately run a node called "wsl".
func TestDropAgentIsTenantScoped(t *testing.T) {
	ctx, now := context.Background(), time.Now()
	alex, other := twoTenants(t)

	alex.UpsertSession(ctx, Session{SessionID: "a1", Agent: "wsl"}, now)
	other.UpsertSession(ctx, Session{SessionID: "b1", Agent: "wsl"}, now)

	if err := alex.DropAgentSessions(ctx, "wsl"); err != nil {
		t.Fatal(err)
	}
	got, err := other.ListSessions(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("dropping one tenant's agent removed another's: %d sessions left", len(got))
	}
}

// The same node name in two tenants must resolve to two distinct connections.
func TestNodeKeySeparatesTenants(t *testing.T) {
	if nodeKey("alex", "wsl") == nodeKey("someone-else", "wsl") {
		t.Fatal("node key collides across tenants — one tenant's agent would receive another's commands")
	}
}

// Agent keys carry their tenant, and a bare label means the default tenant so
// self-hosted files keep working unchanged.
func TestAuthorizedAgentsCarryTenant(t *testing.T) {
	a, _ := testKey(t)
	b, _ := testKey(t)

	agents, err := ParseAuthorizedAgents([]byte(
		authorizedLine(a, "wsl") + authorizedLine(b, "alex/mac")))
	if err != nil {
		t.Fatal(err)
	}
	if agents[0].Tenant != DefaultTenant || agents[0].Name != "wsl" {
		t.Errorf("bare label = %+v, want default tenant", agents[0])
	}
	if agents[1].Tenant != "alex" || agents[1].Name != "mac" {
		t.Errorf("tenant label = %+v, want alex/mac", agents[1])
	}
}

// Two tenants may each name a node "wsl" without colliding in the key file.
func TestSameNodeNameInTwoTenants(t *testing.T) {
	a, _ := testKey(t)
	b, _ := testKey(t)

	agents, err := ParseAuthorizedAgents([]byte(
		authorizedLine(a, "alex/wsl") + authorizedLine(b, "other/wsl")))
	if err != nil {
		t.Fatalf("same node name in two tenants rejected: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("agents = %d, want 2", len(agents))
	}
}

func TestMalformedTenantLabelRejected(t *testing.T) {
	pub, _ := testKey(t)
	for _, label := range []string{"/wsl", "alex/"} {
		if _, err := ParseAuthorizedAgents([]byte(authorizedLine(pub, label))); err == nil {
			t.Errorf("malformed label %q accepted", label)
		}
	}
}

// Two tenants can produce identical session ids — the id is derived from the
// project path and host label, both of which repeat across customers. Keying
// sessions by id alone let one tenant's report overwrite the other's, which a
// live two-tenant run caught and single-tenant tests did not.
func TestIdenticalSessionIDsAcrossTenants(t *testing.T) {
	ctx, now := context.Background(), time.Now()
	alex, other := twoTenants(t)

	// The same host, the same project, two customers.
	const shared = "claude-iptv-wsl-10cac2b9"
	if err := alex.UpsertSession(ctx, Session{SessionID: shared, Agent: "wsl", Project: "iptv"}, now); err != nil {
		t.Fatal(err)
	}
	if err := other.UpsertSession(ctx, Session{SessionID: shared, Agent: "wsl", Project: "iptv"}, now); err != nil {
		t.Fatal(err)
	}

	for name, tn := range map[string]*Tenant{"alex": alex, "other": other} {
		got, err := tn.ListSessions(ctx, now)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 {
			t.Errorf("%s sees %d sessions, want 1 — the other tenant's report clobbered it", name, len(got))
		}
	}
}
