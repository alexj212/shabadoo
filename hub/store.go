package hub

// Durable state: agents, sessions, tasks, the message inbox, and the audit log.
//
// This replaces the NATS JetStream stream (CLAUDE_MSGS), its per-session
// durable consumers, and the CLAUDE_PRESENCE KV bucket. The four inbox
// properties that made the bridge trustworthy are preserved here, and each is
// pinned by a test:
//
//  1. A message for an offline session waits and is delivered on reconnect.
//  2. Delivery is idempotent — draining twice does not duplicate.
//  3. A drained message is never redelivered.
//  4. Retention is bounded — a session that never drains cannot grow the DB
//     without limit.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	// messageTTL matches the JetStream stream's 24h limits retention.
	messageTTL = 24 * time.Hour

	// maxPerRecipient matches the stream's 100-messages-per-subject cap. A
	// session that never drains sheds its oldest mail rather than growing
	// unbounded.
	maxPerRecipient = 100
)

// AuditRetention is how long audit and retrieval rows are kept.
//
// A var, and a flag on `hub`, because this is policy rather than mechanism:
// how long you can answer "who drove that pane" is an operator's decision, and
// it is not something anyone should have to rebuild a binary to change.
//
// The default is deliberately the same as a device token's life. Anything
// shorter and the log stops covering the credential that did the thing.
var AuditRetention = 90 * 24 * time.Hour

// Store is the coordinator's database.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the coordinator database.
func Open(path string) (*Store, error) {
	// WAL so a long read (the timeline view) doesn't block an agent's write;
	// busy_timeout so concurrent writers wait rather than erroring immediately;
	// foreign_keys because deliveries reference messages.
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Ping checks the database is actually usable, for the health endpoint. A
// coordinator whose process is up but whose SQLite file has gone read-only or
// vanished underneath it still answers HTTP — and would report itself healthy
// on any check that only proves the port is open.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

const schema = `
CREATE TABLE IF NOT EXISTS tenants (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS agents (
  tenant      TEXT NOT NULL DEFAULT 'default',
  name        TEXT NOT NULL,
  fingerprint TEXT NOT NULL,
  version     TEXT NOT NULL DEFAULT '',
  last_seen   INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (tenant, name)
);

-- What a session says it is DOING, in its own words.
--
-- A separate table because the sessions table is replaced wholesale on every
-- agent report (DropAgentSessions deletes, then rows are re-upserted), so a
-- column there would be erased every five seconds. This is written by the
-- session itself through the MCP bridge, not by the agent, and the two must
-- not race.
--
-- Distinct from sessions.status, which is tmux's idea of the window (active or
-- idle). That is what the window is; this is what the work is.
CREATE TABLE IF NOT EXISTS session_status (
  tenant     TEXT NOT NULL DEFAULT 'default',
  session_id TEXT NOT NULL,
  note       TEXT NOT NULL DEFAULT '',
  at         INTEGER NOT NULL,
  PRIMARY KEY (tenant, session_id)
);

CREATE TABLE IF NOT EXISTS sessions (
  tenant     TEXT NOT NULL DEFAULT 'default',
  session_id TEXT NOT NULL,
  agent      TEXT NOT NULL,
  project    TEXT NOT NULL DEFAULT '',
  cwd        TEXT NOT NULL DEFAULT '',
  alias      TEXT NOT NULL DEFAULT '',
  window     TEXT NOT NULL DEFAULT '',
  status     TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL,
  tmux_session TEXT NOT NULL DEFAULT '',
  win_index    INTEGER NOT NULL DEFAULT 0,
  win_name     TEXT NOT NULL DEFAULT '',
  command      TEXT NOT NULL DEFAULT '',
  activity     INTEGER NOT NULL DEFAULT 0,
  panes        INTEGER NOT NULL DEFAULT 0,
  -- What owns the pane's keyboard: '' (unknown), 'composer', or 'dialog'.
  -- Reported by the agent so the dashboard can flag a session that is blocked
  -- waiting on a human without polling every pane from the browser.
  input_state  TEXT NOT NULL DEFAULT '',
  -- The question a modal is asking, when input_state is 'dialog'. Reported by
  -- the agent from the same capture that classified the state.
  asking       TEXT NOT NULL DEFAULT '',
  -- What is running here: 'claude', 'worker', or 'core'. Empty on rows written
  -- by an agent that predates the field.
  kind         TEXT NOT NULL DEFAULT '',
  -- The project's one-line self-description, read by the agent from the
  -- frontmatter of the CLAUDE.md that marks the project. The routing card.
  description  TEXT NOT NULL DEFAULT '',
  -- Keyed by (tenant, session_id), not session_id alone: session ids are derived
  -- from project path and host label, so two tenants can legitimately produce
  -- the same one. With a bare session_id key, one tenant's report silently
  -- overwrites the other's rows.
  PRIMARY KEY (tenant, session_id)
);
CREATE INDEX IF NOT EXISTS sessions_agent ON sessions(tenant, agent);

CREATE TABLE IF NOT EXISTS tasks (
  tenant     TEXT NOT NULL DEFAULT 'default',
  id         TEXT NOT NULL,
  session_id TEXT NOT NULL,
  thread     TEXT NOT NULL DEFAULT '',
  state      TEXT NOT NULL,
  brief      TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (tenant, id)
);
CREATE INDEX IF NOT EXISTS tasks_session ON tasks(tenant, session_id, state);

CREATE TABLE IF NOT EXISTS messages (
  tenant     TEXT NOT NULL DEFAULT 'default',
  id           TEXT PRIMARY KEY,
  from_session TEXT NOT NULL DEFAULT '',
  to_session   TEXT NOT NULL DEFAULT '',
  topic        TEXT NOT NULL DEFAULT '',
  title        TEXT NOT NULL DEFAULT '',
  body         TEXT NOT NULL,
  type         TEXT NOT NULL DEFAULT '',
  tag          TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS messages_expiry ON messages(expires_at);

CREATE TABLE IF NOT EXISTS deliveries (
  tenant       TEXT NOT NULL DEFAULT 'default',
  message_id   TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  to_session   TEXT NOT NULL,
  delivered_at INTEGER,
  acked_at     INTEGER,
  PRIMARY KEY (message_id, to_session)
);
CREATE INDEX IF NOT EXISTS deliveries_pending ON deliveries(tenant, to_session, acked_at);

CREATE TABLE IF NOT EXISTS subscriptions (
  tenant     TEXT NOT NULL DEFAULT 'default',
  session_id TEXT NOT NULL,
  topic      TEXT NOT NULL,
  PRIMARY KEY (tenant, session_id, topic)
);

CREATE TABLE IF NOT EXISTS audit (
  id     INTEGER PRIMARY KEY AUTOINCREMENT,
  tenant TEXT NOT NULL DEFAULT 'default',
  at     INTEGER NOT NULL,
  actor  TEXT NOT NULL,
  action TEXT NOT NULL,
  target TEXT NOT NULL DEFAULT '',
  detail TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS audit_at ON audit(at);

CREATE TABLE IF NOT EXISTS retrievals (
  id     INTEGER PRIMARY KEY AUTOINCREMENT,
  tenant TEXT NOT NULL DEFAULT 'default',
  at     INTEGER NOT NULL,
  asker  TEXT NOT NULL,
  scope  TEXT NOT NULL,
  query  TEXT NOT NULL,
  hits   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS retrievals_at ON retrievals(at);

-- Enrolled clients (phones). The primary key is the token's SHA-256, never the
-- token: a dump of this table yields hashes, not usable credentials. Pairing
-- codes are deliberately absent — they live five minutes, are single-use, and
-- losing them on restart is the correct behaviour.
CREATE TABLE IF NOT EXISTS devices (
  token_hash TEXT PRIMARY KEY,
  id         TEXT NOT NULL,
  tenant     TEXT NOT NULL DEFAULT 'default',
  label      TEXT NOT NULL DEFAULT '',
  email      TEXT NOT NULL DEFAULT '',
  -- '' = full access, 'read' = may not drive a pane. Persisted because a
  -- read-only phone that silently becomes read-write at the next restart is
  -- worse than never having offered the scope.
  scope      TEXT NOT NULL DEFAULT '',
  -- APNs token, empty until the app registers one. Updatable: iOS reissues it
  -- on reinstall, restore, and at the system's discretion.
  push_token TEXT NOT NULL DEFAULT '',
  -- Which APNs gateway the token belongs to: 'development' or 'production'.
  -- Apple runs two and their tokens are not interchangeable.
  push_env   TEXT NOT NULL DEFAULT '',
  enrolled   INTEGER NOT NULL,
  expires    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS devices_tenant ON devices(tenant);
`

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return err
	}
	// CREATE TABLE IF NOT EXISTS leaves an existing table alone, so columns
	// added after a database was created need an explicit ALTER. Adding a
	// column that is already there is the normal case on every start but the
	// first, so that specific failure is success.
	for _, alter := range []string{
		`ALTER TABLE sessions ADD COLUMN input_state TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE sessions ADD COLUMN asking TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE sessions ADD COLUMN kind TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE sessions ADD COLUMN description TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE devices ADD COLUMN scope TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE devices ADD COLUMN push_token TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE devices ADD COLUMN push_env TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := s.db.ExecContext(ctx, alter); err != nil &&
			!strings.Contains(err.Error(), "duplicate column name") {
			return err
		}
	}
	return nil
}

// newID returns a random opaque id. Message ids are the dedupe key clients use,
// so they must be unguessable enough not to collide, not cryptographically
// meaningful.
func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is not a condition this process can sensibly
		// continue through.
		panic("hub: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------------------
// tenancy
// ---------------------------------------------------------------------------

// DefaultTenant is the single tenant a self-hosted coordinator uses. Running
// self-hosted is exactly running hosted with one tenant, which is what keeps
// the two deployment modes on one code path instead of two forks.
const DefaultTenant = "default"

// Tenant scopes every operation to one owner's data.
//
// Operations hang off this handle rather than off Store so that forgetting the
// tenant filter is not expressible: there is no way to reach the message table
// without having named a tenant first.
type Tenant struct {
	s  *Store
	id string
}

// Tenant returns a handle scoped to one tenant, creating the row if needed.
func (s *Store) Tenant(id string) *Tenant {
	if id == "" {
		id = DefaultTenant
	}
	return &Tenant{s: s, id: id}
}

// ID reports which tenant this handle is scoped to.
func (t *Tenant) ID() string { return t.id }

// EnsureTenant records a tenant so the hosted manager can list them.
func (s *Store) EnsureTenant(ctx context.Context, id, name string, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO tenants (id, name, created_at) VALUES (?, ?, ?)`,
		id, name, now.Unix())
	return err
}

// Tenants lists every known tenant. Self-hosted has exactly one.
func (s *Store) Tenants(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM tenants ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// messages
// ---------------------------------------------------------------------------

// Envelope is one message on the bridge. Field names mirror the natsbridge
// envelope so keryx can carry the same payloads across the transport swap.
type Envelope struct {
	ID          string `json:"id"`
	FromSession string `json:"from_session,omitempty"`
	ToSession   string `json:"to_session,omitempty"`
	Topic       string `json:"topic,omitempty"`
	Title       string `json:"title,omitempty"`
	Body        string `json:"body"`
	Type        string `json:"type,omitempty"`
	Tag         string `json:"tag,omitempty"`
	CreatedAt   int64  `json:"created_at"`

	// Delivery state, filled in by the READ paths only (Replay, Conversation)
	// and absent everywhere else — Send ignores them, so a client cannot claim
	// its own message was read.
	//
	// "Acknowledged" here means drained: the recipient session pulled the
	// message into its context. That is the only acknowledgement this system
	// has, and it is the one worth showing — a handoff that was stored, and
	// reported as sent, and never picked up is indistinguishable from a
	// delivered one until you can see this.
	Recipients int   `json:"recipients,omitempty"` // delivery rows; >1 for a broadcast
	Acked      int   `json:"acked,omitempty"`      // how many have drained it
	AckedAt    int64 `json:"acked_at,omitempty"`   // most recent drain, unix seconds
}

// What a session is. A pane holds a Claude session, some other program a
// worker registered, or the node's own core session.
const (
	KindClaude = "claude"
	KindWorker = "worker"
	KindCore   = "core"
)

var ErrNoRecipient = errors.New("message has no recipient")

// ErrNoSuchSession is returned when a recipient cannot be resolved to a session
// this tenant knows about.
//
// It exists because the alternative was silence: Send inserted a delivery row
// for whatever string it was handed, so a typo — or a project name, which is
// what anyone would try first — produced a message that was stored, reported as
// sent, and drained by nobody. A misaddressed letter has to bounce.
var ErrNoSuchSession = errors.New("no session matches that recipient")

// ResolveSession turns what one session called another into a session id.
//
// This is the routing primitive for the whole premise: sessions are experts in
// domains, and a domain is a project, so an agent must be able to say "ask
// homelab" without first learning a hash-suffixed id it can only get by listing
// and string-matching itself.
//
// The rule is the one used everywhere else in this project — exact, then
// substring, and an ambiguous match is an error rather than a guess. Matching
// covers the session id, the alias and the project, and deliberately NOT the
// cwd: every session on a Linux host lives under /home/<user>, so matching the
// path would make common words resolve to everything.
//
// A `claude-`-prefixed string that matches nothing is passed through unchanged.
// That preserves the deliberate property that mail for an offline session waits
// for it — those sessions are absent from this table while their host is gone —
// without also swallowing every typo.
func (t *Tenant) ResolveSession(ctx context.Context, want string, now time.Time) (string, error) {
	want = strings.TrimSpace(want)
	if want == "" {
		return "", ErrNoRecipient
	}
	sessions, err := t.ListSessions(ctx, now)
	if err != nil {
		return "", err
	}

	var exact, partial []Session
	lower := strings.ToLower(want)
	for _, s2 := range sessions {
		switch {
		case s2.SessionID == want:
			return s2.SessionID, nil // an exact id always wins outright
		case strings.EqualFold(s2.Alias, want), strings.EqualFold(s2.Project, want):
			exact = append(exact, s2)
		case strings.Contains(strings.ToLower(s2.Alias), lower),
			strings.Contains(strings.ToLower(s2.Project), lower):
			partial = append(partial, s2)
		}
	}
	matches := exact
	if len(matches) == 0 {
		matches = partial
	}

	switch len(matches) {
	case 1:
		return matches[0].SessionID, nil
	case 0:
		if strings.HasPrefix(want, "claude-") {
			return want, nil // an offline session, addressed precisely
		}
		return "", fmt.Errorf("%w: %q (known: %s)", ErrNoSuchSession, want, sessionNames(sessions))
	default:
		return "", fmt.Errorf("%q is ambiguous: %s", want, sessionNames(matches))
	}
}

// ResolvePane turns a name into the pane coordinates a write needs.
//
// ResolveSession answers "which session" for mail; this answers "which pane"
// for a keystroke, which needs the node, the tmux session and the window index
// as well.
//
// It exists so no CLIENT has to invent the rule. A voice agent, the phone, the
// CLI and the dashboard resolving "homelife" three different ways is how the
// same phrase types into the wrong project — and this is the one place in the
// design where being wrong means text landing in a live session someone else
// is using.
func (t *Tenant) ResolvePane(ctx context.Context, want string, now time.Time) (Session, error) {
	id, err := t.ResolveSession(ctx, want, now)
	if err != nil {
		return Session{}, err
	}
	sessions, err := t.ListSessions(ctx, now)
	if err != nil {
		return Session{}, err
	}
	for _, s2 := range sessions {
		if s2.SessionID == id {
			return s2, nil
		}
	}
	// ResolveSession passes a claude-prefixed id through unresolved so mail can
	// wait for an offline session. A keystroke cannot wait for anything.
	return Session{}, fmt.Errorf("%w: %q is not a live pane", ErrNoSuchSession, want)
}

// sessionNames renders candidates for an error message. A resolution failure is
// most often a near miss, and the list is what turns it into a one-line fix.
func sessionNames(sessions []Session) string {
	seen := map[string]bool{}
	var names []string
	for _, s2 := range sessions {
		n := s2.Alias
		if n == "" {
			n = s2.Project
		}
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// Send queues a direct message for one session. The session need not exist or
// be connected: the delivery row is what makes an offline session's mail wait
// for it.
func (t *Tenant) Send(ctx context.Context, env Envelope, now time.Time) (string, error) {
	if env.ToSession == "" {
		return "", ErrNoRecipient
	}
	id, err := t.insertMessage(ctx, env, now)
	if err != nil {
		return "", err
	}
	if _, err := t.s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO deliveries (tenant, message_id, to_session) VALUES (?, ?, ?)`,
		t.id, id, env.ToSession); err != nil {
		return "", err
	}
	return id, t.trim(ctx, env.ToSession, now)
}

// Broadcast queues a message for every session subscribed to a topic.
//
// Fan-out happens at publish time, to subscribers as they are *now* — a session
// that subscribes later does not receive earlier broadcasts. That matches the
// core-NATS semantics the bridge had, and keeps a new session from waking up to
// a day of backlog.
func (t *Tenant) Broadcast(ctx context.Context, env Envelope, now time.Time) (string, int, error) {
	if env.Topic == "" {
		return "", 0, ErrNoRecipient
	}
	env.ToSession = ""
	id, err := t.insertMessage(ctx, env, now)
	if err != nil {
		return "", 0, err
	}

	rows, err := t.s.db.QueryContext(ctx,
		`SELECT session_id FROM subscriptions WHERE tenant = ? AND topic = ?`, t.id, env.Topic)
	if err != nil {
		return "", 0, err
	}
	var subs []string
	for rows.Next() {
		var sid string
		if err := rows.Scan(&sid); err != nil {
			rows.Close()
			return "", 0, err
		}
		subs = append(subs, sid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", 0, err
	}

	for _, sid := range subs {
		if _, err := t.s.db.ExecContext(ctx,
			`INSERT OR IGNORE INTO deliveries (tenant, message_id, to_session) VALUES (?, ?, ?)`,
			t.id, id, sid); err != nil {
			return "", 0, err
		}
		if err := t.trim(ctx, sid, now); err != nil {
			return "", 0, err
		}
	}
	return id, len(subs), nil
}

func (t *Tenant) insertMessage(ctx context.Context, env Envelope, now time.Time) (string, error) {
	id := env.ID
	if id == "" {
		id = newID()
	}
	_, err := t.s.db.ExecContext(ctx,
		`INSERT INTO messages (tenant, id, from_session, to_session, topic, title, body, type, tag, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.id, id, env.FromSession, env.ToSession, env.Topic, env.Title, env.Body, env.Type, env.Tag,
		now.Unix(), now.Add(messageTTL).Unix())
	if err != nil {
		return "", err
	}
	return id, nil
}

// Drain returns a session's undelivered mail and marks it delivered, in one
// transaction.
//
// Acknowledgement happens on read rather than after the caller has processed
// the batch. That makes this at-most-once, and it is the deliberate choice: a
// redelivered message is re-injected into a live Claude conversation as
// duplicate work, which is worse than a message lost to a crash in the
// milliseconds between the read and the hook printing it. The JetStream
// consumer this replaces behaved the same way — `inbox-drain` fetched and
// acked before emitting.
func (t *Tenant) Drain(ctx context.Context, sessionID string, now time.Time) ([]Envelope, error) {
	tx, err := t.s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
		SELECT m.id, m.from_session, m.to_session, m.topic, m.title, m.body, m.type, m.tag, m.created_at
		  FROM deliveries d
		  JOIN messages m ON m.id = d.message_id
		 WHERE d.tenant = ? AND d.to_session = ? AND d.acked_at IS NULL AND m.expires_at > ?
		 ORDER BY m.created_at ASC`, t.id, sessionID, now.Unix())
	if err != nil {
		return nil, err
	}
	var out []Envelope
	for rows.Next() {
		var e Envelope
		if err := rows.Scan(&e.ID, &e.FromSession, &e.ToSession, &e.Topic,
			&e.Title, &e.Body, &e.Type, &e.Tag, &e.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE deliveries SET delivered_at = ?, acked_at = ?
		  WHERE tenant = ? AND to_session = ? AND acked_at IS NULL`,
		now.Unix(), now.Unix(), t.id, sessionID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// Pending counts a session's undrained mail. This is what the nudge path reads
// to decide whether waking a session is worth a turn.
func (t *Tenant) Pending(ctx context.Context, sessionID string, now time.Time) (int, error) {
	var n int
	err := t.s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM deliveries d
		  JOIN messages m ON m.id = d.message_id
		 WHERE d.tenant = ? AND d.to_session = ? AND d.acked_at IS NULL AND m.expires_at > ?`,
		t.id, sessionID, now.Unix()).Scan(&n)
	return n, err
}

// PendingBySession returns undrained counts for every session with mail.
func (t *Tenant) PendingBySession(ctx context.Context, now time.Time) (map[string]int, error) {
	rows, err := t.s.db.QueryContext(ctx, `
		SELECT d.to_session, COUNT(*) FROM deliveries d
		  JOIN messages m ON m.id = d.message_id
		 WHERE d.tenant = ? AND d.acked_at IS NULL AND m.expires_at > ?
		 GROUP BY d.to_session`, t.id, now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var sid string
		var n int
		if err := rows.Scan(&sid, &n); err != nil {
			return nil, err
		}
		out[sid] = n
	}
	return out, rows.Err()
}

// trim enforces the per-recipient cap by dropping the oldest deliveries beyond
// maxPerRecipient. Only undrained mail is counted — drained rows are already
// spent and get cleaned up by Vacuum.
func (t *Tenant) trim(ctx context.Context, sessionID string, now time.Time) error {
	_, err := t.s.db.ExecContext(ctx, `
		DELETE FROM deliveries
		 WHERE tenant = ? AND to_session = ? AND acked_at IS NULL AND message_id IN (
		   SELECT d.message_id FROM deliveries d
		     JOIN messages m ON m.id = d.message_id
		    WHERE d.tenant = ? AND d.to_session = ? AND d.acked_at IS NULL
		    ORDER BY m.created_at DESC
		    LIMIT -1 OFFSET ?
		 )`, t.id, sessionID, t.id, sessionID, maxPerRecipient)
	return err
}

// Vacuum drops expired messages and the deliveries that cascade from them.
// Call on a timer; it is the only thing that bounds the database's growth over
// the long run.
func (s *Store) Vacuum(ctx context.Context, now time.Time) (VacuumResult, error) {
	var out VacuumResult

	del := func(dst *int64, query string, args ...any) error {
		res, err := s.db.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		*dst = n
		return err
	}

	if err := del(&out.Messages, `DELETE FROM messages WHERE expires_at <= ?`, now.Unix()); err != nil {
		return out, fmt.Errorf("messages: %w", err)
	}

	// Audit and retrievals grow with USE, not with time — every pane write,
	// every login, every failed enrolment attempt is a row. Only messages were
	// ever expired, so on a busy coordinator these two were the tables that
	// grew without bound. A rate-limited endpoint that audits each rejection
	// makes that worse, not better: a grinder writes rows as fast as it is
	// refused.
	cutoff := now.Add(-AuditRetention).Unix()
	if err := del(&out.Audit, `DELETE FROM audit WHERE at < ?`, cutoff); err != nil {
		return out, fmt.Errorf("audit: %w", err)
	}
	if err := del(&out.Retrievals, `DELETE FROM retrievals WHERE at < ?`, cutoff); err != nil {
		return out, fmt.Errorf("retrievals: %w", err)
	}

	// Deleting rows does not shrink the file; SQLite reuses the freed pages, so
	// the database plateaus rather than growing forever. A real VACUUM would
	// reclaim the space but needs to rewrite the whole file with exclusive
	// access, which is not worth taking a live coordinator offline for.
	return out, nil
}

// VacuumResult is what one maintenance pass removed, per table, so the log can
// say which thing was actually growing.
type VacuumResult struct {
	Messages   int64
	Audit      int64
	Retrievals int64
}

// Any reports whether anything was removed, so a quiet pass stays quiet.
func (v VacuumResult) Any() bool { return v.Messages+v.Audit+v.Retrievals > 0 }

func (v VacuumResult) String() string {
	return fmt.Sprintf("%d message(s), %d audit row(s), %d retrieval(s)",
		v.Messages, v.Audit, v.Retrievals)
}

// messageCols is the SELECT list both read paths use. Delivery state is an
// aggregate over `deliveries`: a direct message has one row, a broadcast has
// one per subscriber, and a LEFT JOIN keeps a message with no rows at all
// (which should not happen, and must not silently vanish if it does).
const messageCols = `
	SELECT m.id, m.from_session, m.to_session, m.topic, m.title, m.body,
	       m.type, m.tag, m.created_at,
	       COUNT(d.message_id), COUNT(d.acked_at), COALESCE(MAX(d.acked_at), 0)
	  FROM messages m
	  LEFT JOIN deliveries d ON d.message_id = m.id AND d.tenant = m.tenant`

func scanMessages(rows *sql.Rows) ([]Envelope, error) {
	defer rows.Close()
	var out []Envelope
	for rows.Next() {
		var e Envelope
		if err := rows.Scan(&e.ID, &e.FromSession, &e.ToSession, &e.Topic,
			&e.Title, &e.Body, &e.Type, &e.Tag, &e.CreatedAt,
			&e.Recipients, &e.Acked, &e.AckedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Replay returns recent messages regardless of delivery state — the timeline
// view. Unlike Drain it changes nothing.
func (t *Tenant) Replay(ctx context.Context, limit int, since time.Time) ([]Envelope, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := t.s.db.QueryContext(ctx, messageCols+`
		 WHERE m.tenant = ? AND m.created_at >= ?
		 GROUP BY m.id
		 ORDER BY m.created_at DESC LIMIT ?`, t.id, since.Unix(), limit)
	if err != nil {
		return nil, err
	}
	return scanMessages(rows)
}

// Conversation returns the thread involving one session, both directions.
func (t *Tenant) Conversation(ctx context.Context, sessionID string, limit int) ([]Envelope, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := t.s.db.QueryContext(ctx, messageCols+`
		 WHERE m.tenant = ? AND (m.from_session = ? OR m.to_session = ?)
		 GROUP BY m.id
		 ORDER BY m.created_at DESC LIMIT ?`, t.id, sessionID, sessionID, limit)
	if err != nil {
		return nil, err
	}
	return scanMessages(rows)
}

// ---------------------------------------------------------------------------
// subscriptions
// ---------------------------------------------------------------------------

func (t *Tenant) Subscribe(ctx context.Context, sessionID, topic string) error {
	_, err := t.s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO subscriptions (tenant, session_id, topic) VALUES (?, ?, ?)`, t.id, sessionID, topic)
	return err
}

func (t *Tenant) Unsubscribe(ctx context.Context, sessionID, topic string) error {
	_, err := t.s.db.ExecContext(ctx,
		`DELETE FROM subscriptions WHERE tenant = ? AND session_id = ? AND topic = ?`, t.id, sessionID, topic)
	return err
}

func (t *Tenant) Topics(ctx context.Context) ([]string, error) {
	rows, err := t.s.db.QueryContext(ctx,
		`SELECT DISTINCT topic FROM subscriptions WHERE tenant = ? ORDER BY topic`, t.id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// sessions and agents
// ---------------------------------------------------------------------------

// Session is a Claude session known to the coordinator.
//
// The tmux-shaped fields (Index, Name, Command, Activity, Panes) are carried
// through from the agent's report so the dashboard can render exactly what the
// flock's per-window view did. They are display data, not identity — the
// coordinator keys on (tenant, SessionID).
type Session struct {
	SessionID string `json:"session_id"`
	Agent     string `json:"agent"`
	Project   string `json:"project"`
	CWD       string `json:"cwd"`
	Alias     string `json:"alias"`
	Window    string `json:"window"` // "<tmux session>:<index>"
	Status    string `json:"status"`
	UpdatedAt int64  `json:"updated_at"`
	Pending   int    `json:"pending"`

	TmuxSession string `json:"tmux_session"`
	Index       int    `json:"index"`
	Name        string `json:"name"` // raw tmux window name, for reopen
	Command     string `json:"command"`
	Activity    int64  `json:"activity"`
	Panes       int    `json:"panes"`

	// InputState is "composer", "dialog", or "" when the agent did not say.
	// A "dialog" session is blocked on a human answering a prompt.
	// Kind is what is running in this pane. Until this existed the session table
	// silently claimed every tmux window was a Claude session — `top` in a window
	// reported itself as a project — and ResolveSession only passed through
	// `claude-`-prefixed ids, so a non-Claude tool could not be addressed at all.
	// A worker cannot be first-class until it can register as itself.
	Kind string `json:"kind,omitempty"`

	// Description is the project's one-line self-description, read from the
	// frontmatter of the CLAUDE.md that marks it. It is the routing card: what a
	// session consults to decide where work belongs, which is why it is bounded
	// and why it lives apart from the body of the file it is written in.
	Description string `json:"description,omitempty"`

	InputState string `json:"input_state,omitempty"`

	// Asking is the question a modal is waiting on, when InputState is
	// "dialog". Empty when there is no dialog or none could be recognised.
	//
	// It closes the last hop in the loop: without it the dashboard could say a
	// session was blocked and never what it was blocked ON, so answering meant
	// selecting the pane and reading a transcript first. Approving something
	// you have not read is the interaction this tool should be most careful
	// about, because these panes run with permissions disabled.
	Asking string `json:"asking,omitempty"`

	// Note is what this session says it is doing, in its own words, set through
	// the MCP bridge. Empty when it has said nothing recently.
	//
	// Deliberately not the same thing as Status: tmux knows whether a window is
	// active or idle, and knows nothing about whether the work in it is
	// "waiting on the homelab peer" or "rebuilding the index". Only the session
	// knows that, and under this project's premise — separate sessions, each in
	// its own domain, working as one — it is the thing a peer most needs.
	Note string `json:"note,omitempty"`
}

// statusTTL is how long a self-declared note stays visible.
//
// A note is a statement about NOW, and a session that set one and then died
// would otherwise claim to be mid-task forever. Long enough to survive a slow
// turn, short enough that a stale claim ages out on its own.
const statusTTL = 30 * time.Minute

// SetSessionStatus records what a session says it is doing. An empty note
// clears it, which is how a session says it has finished rather than stopped.
func (t *Tenant) SetSessionStatus(ctx context.Context, sessionID, note string, now time.Time) error {
	if sessionID == "" {
		return errors.New("session required")
	}
	note = strings.TrimSpace(note)
	if len(note) > 200 {
		// A status line, not a report. Truncated rather than refused: losing the
		// tail of an over-long note is better than losing the note.
		note = note[:200]
	}
	if note == "" {
		_, err := t.s.db.ExecContext(ctx,
			`DELETE FROM session_status WHERE tenant = ? AND session_id = ?`, t.id, sessionID)
		return err
	}
	_, err := t.s.db.ExecContext(ctx, `
		INSERT INTO session_status (tenant, session_id, note, at) VALUES (?, ?, ?, ?)
		ON CONFLICT(tenant, session_id) DO UPDATE SET note = excluded.note, at = excluded.at`,
		t.id, sessionID, note, now.Unix())
	return err
}

// UpsertSession records what an agent reports about one of its windows.
func (t *Tenant) UpsertSession(ctx context.Context, sess Session, now time.Time) error {
	_, err := t.s.db.ExecContext(ctx, `
		INSERT INTO sessions (tenant, session_id, agent, project, cwd, alias, window, status,
		                      updated_at, tmux_session, win_index, win_name, command, activity, panes,
		                      input_state, asking, kind, description)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(tenant, session_id) DO UPDATE SET
		  agent=excluded.agent, project=excluded.project, cwd=excluded.cwd,
		  alias=excluded.alias, window=excluded.window, status=excluded.status,
		  updated_at=excluded.updated_at, tmux_session=excluded.tmux_session,
		  win_index=excluded.win_index, win_name=excluded.win_name,
		  command=excluded.command, activity=excluded.activity, panes=excluded.panes,
		  input_state=excluded.input_state, asking=excluded.asking,
		  kind=excluded.kind, description=excluded.description`,
		t.id, sess.SessionID, sess.Agent, sess.Project, sess.CWD, sess.Alias,
		sess.Window, sess.Status, now.Unix(), sess.TmuxSession, sess.Index,
		sess.Name, sess.Command, sess.Activity, sess.Panes, sess.InputState, sess.Asking,
		sess.Kind, sess.Description)
	return err
}

// ListSessions returns every known session with its undrained mail count.
func (t *Tenant) ListSessions(ctx context.Context, now time.Time) ([]Session, error) {
	rows, err := t.s.db.QueryContext(ctx, `
		SELECT s.session_id, s.agent, s.project, s.cwd, s.alias, s.window, s.status,
		       s.updated_at, s.tmux_session, s.win_index, s.win_name, s.command,
		       s.activity, s.panes, s.input_state, s.asking, s.kind, s.description,
		       COALESCE(n.note, '')
		  FROM sessions s
		  LEFT JOIN session_status n
		    ON n.tenant = s.tenant AND n.session_id = s.session_id AND n.at >= ?
		 WHERE s.tenant = ? ORDER BY s.agent, s.win_index`,
		now.Add(-statusTTL).Unix(), t.id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var s2 Session
		if err := rows.Scan(&s2.SessionID, &s2.Agent, &s2.Project, &s2.CWD,
			&s2.Alias, &s2.Window, &s2.Status, &s2.UpdatedAt,
			&s2.TmuxSession, &s2.Index, &s2.Name, &s2.Command,
			&s2.Activity, &s2.Panes, &s2.InputState, &s2.Asking, &s2.Kind, &s2.Description,
			&s2.Note); err != nil {
			return nil, err
		}
		out = append(out, s2)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	pending, err := t.PendingBySession(ctx, now)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Pending = pending[out[i].SessionID]
	}
	return out, nil
}

// DropAgentSessions removes an agent's sessions. Called when it disconnects:
// presence is connection liveness, so a session whose agent is gone must not
// linger in the dashboard looking alive.
func (t *Tenant) DropAgentSessions(ctx context.Context, agent string) error {
	_, err := t.s.db.ExecContext(ctx, `DELETE FROM sessions WHERE tenant = ? AND agent = ?`, t.id, agent)
	return err
}

// SeenAgent records an agent's login.
func (t *Tenant) SeenAgent(ctx context.Context, name, fingerprint, version string, now time.Time) error {
	_, err := t.s.db.ExecContext(ctx, `
		INSERT INTO agents (tenant, name, fingerprint, version, last_seen) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(tenant, name) DO UPDATE SET
		  fingerprint=excluded.fingerprint, version=excluded.version, last_seen=excluded.last_seen`,
		t.id, name, fingerprint, version, now.Unix())
	return err
}

// AgentVersions maps a tenant's agent names to the build each reported at its
// last login.
//
// The version has been collected since the first login and read by nothing,
// which made it useless for the one job it exists for: `setup --service`
// installs whatever binary is running, so a stale checkout silently downgrades
// a node, and without this the dashboard shows a downgraded host as perfectly
// healthy. A value here is only as fresh as the last login — an agent holding a
// token across an upgrade still reports the build it authenticated with.
func (t *Tenant) AgentVersions(ctx context.Context) (map[string]string, error) {
	rows, err := t.s.db.QueryContext(ctx,
		`SELECT name, version FROM agents WHERE tenant = ?`, t.id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var name, version string
		if err := rows.Scan(&name, &version); err != nil {
			return nil, err
		}
		out[name] = version
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// audit
// ---------------------------------------------------------------------------

// AuditEntry is one recorded action.
type AuditEntry struct {
	At     int64  `json:"at"`
	Actor  string `json:"actor"`
	Action string `json:"action"`
	Target string `json:"target,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Audit records an action. Every write that reaches a pane goes through here —
// this is capability the flock never had: today nothing records who sent
// keystrokes into which window.
func (t *Tenant) Audit(ctx context.Context, e AuditEntry, now time.Time) error {
	_, err := t.s.db.ExecContext(ctx,
		`INSERT INTO audit (tenant, at, actor, action, target, detail) VALUES (?, ?, ?, ?, ?, ?)`,
		t.id, now.Unix(), e.Actor, e.Action, e.Target, e.Detail)
	return err
}

// AuditTail returns the most recent audit entries, newest first.
func (t *Tenant) AuditTail(ctx context.Context, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := t.s.db.QueryContext(ctx,
		`SELECT at, actor, action, target, detail FROM audit WHERE tenant = ? ORDER BY id DESC LIMIT ?`, t.id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.At, &e.Actor, &e.Action, &e.Target, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// RecordRetrieval logs an Aspis lookup. Promotion-by-demand needs a demand
// history, and that history is the one part of the knowledge design that
// cannot be reconstructed after the fact — so it is recorded from the first
// commit, before anything reads it.
func (t *Tenant) RecordRetrieval(ctx context.Context, asker, scope, query, hits string, now time.Time) error {
	_, err := t.s.db.ExecContext(ctx,
		`INSERT INTO retrievals (tenant, at, asker, scope, query, hits) VALUES (?, ?, ?, ?, ?, ?)`,
		t.id, now.Unix(), asker, scope, query, hits)
	return err
}

// Stats is a cross-table aggregate for one tenant.
//
// It had an HTTP endpoint (`GET /api/stats`) that nothing ever rendered, and it
// was removed rather than given a reader: every number here is derivable from
// `/api/sessions`, which the dashboard polls anyway, so the endpoint was a
// second request for data already on the wire.
//
// The method stays because tenant isolation is checked through it — one call
// that touches sessions, agents, messages, deliveries and the audit log is a
// better leak test than four separate assertions.
type Stats struct {
	Sessions   int `json:"sessions"`
	Agents     int `json:"agents"`
	Messages   int `json:"messages"`
	Pending    int `json:"pending"`
	AuditCount int `json:"audit_count"`
}

func (t *Tenant) Stats(ctx context.Context, now time.Time) (Stats, error) {
	var st Stats
	q := func(dest *int, query string, args ...any) error {
		return t.s.db.QueryRowContext(ctx, query, args...).Scan(dest)
	}
	if err := q(&st.Sessions, `SELECT COUNT(*) FROM sessions WHERE tenant = ?`, t.id); err != nil {
		return st, err
	}
	if err := q(&st.Agents, `SELECT COUNT(*) FROM agents WHERE tenant = ?`, t.id); err != nil {
		return st, err
	}
	if err := q(&st.Messages, `SELECT COUNT(*) FROM messages WHERE tenant = ? AND expires_at > ?`, t.id, now.Unix()); err != nil {
		return st, err
	}
	if err := q(&st.Pending, `
		SELECT COUNT(*) FROM deliveries d JOIN messages m ON m.id = d.message_id
		 WHERE d.tenant = ? AND d.acked_at IS NULL AND m.expires_at > ?`, t.id, now.Unix()); err != nil {
		return st, err
	}
	if err := q(&st.AuditCount, `SELECT COUNT(*) FROM audit WHERE tenant = ?`, t.id); err != nil {
		return st, err
	}
	return st, nil
}

// ---------------------------------------------------------------------------
// devices
//
// An enrolled phone must survive a coordinator restart. Holding devices only in
// memory made the 90-day token TTL fiction: every deploy silently logged out
// every client, which reads as "the app is broken" rather than "the server
// restarted".
// ---------------------------------------------------------------------------

// LoadDevices returns every unexpired device, keyed by token hash. Expired rows
// are dropped on the way past — a device nobody can authenticate with is not
// worth carrying in memory or in the table.
func (s *Store) LoadDevices(ctx context.Context, now time.Time) (map[string]Device, error) {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM devices WHERE expires <= ?`, now.Unix()); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT token_hash, id, tenant, label, email, scope, push_token, push_env, enrolled, expires FROM devices`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]Device{}
	for rows.Next() {
		var (
			hash              string
			d                 Device
			enrolled, expires int64
		)
		if err := rows.Scan(&hash, &d.ID, &d.Tenant, &d.Label, &d.Email, &d.Scope,
			&d.PushToken, &d.PushEnv, &enrolled, &expires); err != nil {
			return nil, err
		}
		d.Enrolled = time.Unix(enrolled, 0)
		d.Expires = time.Unix(expires, 0)
		out[hash] = d
	}
	return out, rows.Err()
}

// SaveDevice persists one enrolment.
func (s *Store) SaveDevice(ctx context.Context, hash string, d Device) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO devices (token_hash, id, tenant, label, email, scope, push_token, push_env, enrolled, expires)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(token_hash) DO UPDATE SET
		  label = excluded.label, expires = excluded.expires,
		  push_token = excluded.push_token, push_env = excluded.push_env`,
		hash, d.ID, d.Tenant, d.Label, d.Email, d.Scope, d.PushToken, d.PushEnv,
		d.Enrolled.Unix(), d.Expires.Unix())
	return err
}

// DeleteDevice removes an enrolment by device ID (what revocation names).
func (s *Store) DeleteDevice(ctx context.Context, deviceID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM devices WHERE id = ?`, deviceID)
	return err
}
