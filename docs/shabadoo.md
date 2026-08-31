# Shabadoo — architecture

Status: **partly built** — see Build status below. Supersedes the flock
(peer-to-peer tmuxbridge nodes) and the NATS session bridge with a coordinator,
per-host agents that dial out to it, and a git-replicated shared knowledge
layer. Runs hosted or self-hosted from one binary, serving a browser and an iOS
app.

The coordinator and this host's agent **are** deployed (`shabadoo-hub.service` +
`shabadoo-node.service` on the WSL host); the flock and `tmuxbridge.service`
were retired 2026-07-29. The coordinator moved to a container on dm (2026-07-30) and the MCP bridge was
absorbed into the binary as `shabadoo mcp` (2026-07-31), so **shabadoo no longer
uses NATS at all**. The cluster keeps running on dm for consumers that were never
shabadoo's.

## Decisions

| Decision | Choice | Why |
|---|---|---|
| Connection direction | Agents dial the coordinator | Kills the peer list, gives presence for free, works off-tailnet, no inbound port anywhere |
| Agent auth | SSH key via ssh-agent (SSHSIG) | Keys already exist and `claude.sh` already forwards `SSH_AUTH_SOCK` into every window; no private key on disk, no new PKI |
| Human auth | Cloudflare Access | Zones + Tunnel already in use here; email-allowlist SSO with no auth code in the coordinator |
| Coordinator role | **Classifier/router**, not orchestrator | It needs to know *who knows*, never *how*. Context stays small and stable — it never compacts |
| Coordinator implementation | API call inside the service, not a Claude Code session | Must be stateless, always-on, and independent of any tmux window being alive |
| Knowledge substrate | Git repo, replicated to every node + coordinator search index | "Each node has the knowledge globally" = full clone = no single point of failure |
| Phase 1 scope | Control + messaging together | NATS retires at the end of phase 1 rather than running two systems for months |
| Agent transport | SSE downstream + POST upstream | Stdlib-only, passes Cloudflare Tunnel with no upgrade negotiation, reconnect is just re-issuing the GET. Costs one extra connection per agent |
| Deployment modes | Hosted **and** self-hosted, one binary | Self-hosted is hosted with one tenant. The agent plane is identical either way; only the human plane differs |
| Clients | Browser **and** iOS app | The app enrols once with a human-minted pairing code, then presents a device token |
| Vault | Deferred | Different blast radius: control leak = someone drives shells; secret leak = every lab credential at once |

## Names

| Piece | Name | Status | Notes |
|---|---|---|---|
| System / repo / binary | **shabadoo** | built | Domain is `shaba.do`, matching the `shaba` CLI alias — sayable and typeable from memory, which matters most on the pairing screen a new operator reaches before they have anything else. `shabadu.us` redirects to it **permanently**: it is the privacy-policy URL registered with Apple, which has no expiry and 404s during review rather than when it changes |
| Coordinator | **hub** | built | `shabadoo hub`, `shabadoo-hub.service`, `hub.db` |
| Per-host agent | **node** | built | `shabadoo node`, `shabadoo-node.service` |
| Shared knowledge | **atlas** | unbuilt | git repo `atlas.git`, full clone at `~/.shabadoo/atlas` |
| MCP bridge | — | **absorbed** | Not a separate piece: `shabadoo mcp` is a subcommand. Folding it in beat renaming it — one artefact to ship, no version skew between bridge and node, and the session inherits its agent's credential instead of holding one |
| Vault (later) | **vault** | unbuilt | separate service, separate blast radius |

**Plain English throughout.** The original scheme was Greek for things and
English for verbs (phalanx / polemarch / hoplite). That was internally coherent
but cost three definitions every time the project was explained, so the built
pieces are now plain words. `node` in particular was already the wire
vocabulary — `{node, session, window}`, `--node` — so the package name and the
API now agree.

The unbuilt pieces follow the same rule, so the whole vocabulary is one word
per thing with no glossary to learn.

The product name carries the personality; component names carry none.
"Shabadoo" is the word you say out loud. `hub`, `node`, `bridge` and `vault`
are words you type into a unit file at 2am while something is broken.

Commands, units, config paths and env vars: see **Renames** below.

## Topology

```
   browser            iOS app
     │ HTTPS             │ HTTPS + device token
     ▼                   ▼
   Cloudflare edge ── Access policy (email allowlist)
     │ cloudflared tunnel
     ▼
   hub  (dm, /docker/hub/)
     │   IdentityProvider: Access (browser) / device token (iOS app)
     │   classifies → routes · SQLite: agents, messages, deliveries, audit
     │
     │ SSE stream + POSTs, opened BY the agent, one per host
     ├──────────────┬──────────────┐
     ▼              ▼              ▼
   node(wsl)  node(mac)  node(dm)
     │              │              │
     │ unix socket  │              │   ← `shabadoo mcp` (MCP stdio) connects here
     │              │              │
   tmux windows  tmux windows  tmux windows
```

One binary, three roles: `shabadoo hub`, `shabadoo node`, and
`shabadoo serve` (retained as the local fallback — see Failure modes).

## Auth

Two planes, two mechanisms. They are not interchangeable: **SSH keys cannot
authenticate a browser**, which is the trap this design exists to avoid.

### Agents → coordinator

```
coordinator → {nonce, timestamp, namespace: "shabadoo-v1"}
agent       → SSHSIG over that blob, signed via $SSH_AUTH_SOCK
coordinator → verify against authorized_agents, pin pubkey → node name
```

`ssh-keygen -Y sign` / `-Y verify` semantics, via `golang.org/x/crypto/ssh` +
`ssh/agent`. Replay window 60s on the timestamp, nonce cached until it expires.
`/docker/hub/authorized_agents` maps pubkey → node name (`wsl`, `mac`,
`dm`), mirroring `SHABADOO_NODE`. Revocation = delete a line.

### Humans → coordinator

Cloudflare Access issues a JWT in `Cf-Access-Jwt-Assertion`. Hub verifies
it against `https://<team>.cloudflareaccess.com/cdn-cgi/access/certs` and checks
`aud` equals the application's AUD tag.

> ### The bypass — the most important constraint in this document
>
> Verifying that header is worthless on its own. Every other service in this lab
> sits behind Traefik at `*.apps.example.com` and is reachable **directly from the
> tailnet**. Published that way, anyone on the tailnet reaches the origin without
> passing Cloudflare — Access becomes decoration, and the UI drives shells
> running `claude --dangerously-skip-permissions`.
>
> The origin must be reachable *only* through the tunnel:
>
> - bind hub to the `cloudflared` docker network only — no Traefik router,
>   no host port, no `*.apps.example.com` entry;
> - reject any request without a valid Access JWT, with no bypass for private
>   source IPs;
> - if a tailnet path is ever wanted as a deliberate second door, it gets its own
>   auth, not an exemption.
>
> Test it explicitly before cutover: from a tailnet host, hitting the origin
> directly must fail closed. This is cutover gate 4.

## Agent protocol

JSON over one persistent SSE stream for commands, plain POSTs for results,
multiplexed by request `id`.

WebSocket was the original choice; SSE replaced it because it needs no
dependency, survives the tunnel without upgrade negotiation, and makes
reconnect trivial. The agent holds `GET /agent/stream` open and POSTs each
result back.

| Direction | Frames |
|---|---|
| hub → node | `list_sessions`, `capture`, `select`, `send`, `command`, `kill`, `reopen`, `open`, `claude_session`, `deliver` |
| node → hub | `hello` (node, version), `result`, `event` (window/presence/status), `publish` (outbound message from mcp-natsbridge) |

- **Agent-initiated, always.** The coordinator never dials an agent.
- **Reconnect with backoff + jitter.** An unreachable coordinator must never
  block a tmux window or the `claude` process; the agent degrades to local-only.
- **Presence is connection liveness.** No heartbeat KV, no TTL.
- **Idempotent writes.** Every mutating op carries a client-generated id so a
  reconnect mid-flight cannot double-send keystrokes into a pane.

### The MCP bridge uses a local unix socket

`mcp-natsbridge` (MCP stdio) runs as a subprocess of `claude`, not inside the node. It
connects to the node's unix socket rather than the network: one authenticated
uplink per host instead of one per Claude session, filesystem permissions as the
auth, and it works where the subprocess has no outbound network. The
`inbox-drain` and `nudge` CLI modes point at the same socket, so the
`UserPromptSubmit` / `SessionStart` hooks keep their current shape.

## Hub: the classifier

Its job is routing, not doing. It answers *which agent owns this* and hands off.
That is what keeps its context small and stable — it never accumulates, never
compacts, never drifts.

### What it classifies

Four decisions, with different failure costs:

| Decision | Notes |
|---|---|
| Which agent | The obvious one |
| New task or continuation | "that didn't work, try the other thing" must land in its existing thread. Highest-frequency case, easiest to get wrong |
| Wake or queue | Target may be offline: queue it, or nudge the session awake now |
| Is it for an agent at all | "What's my Strava mileage" is a direct tool call, not a project task. The classifier must be able to answer "none of the above" |

**A misroute is an edit in the wrong repo**, not an annoyance — "fix the auth
bug" landing on `service-a` instead of `service-b` starts an agent with
`--dangerously-skip-permissions` modifying the wrong codebase. So: low
confidence asks instead of guessing, the chosen route is visible before work
starts, and correcting a route is one tap.

Pure classification is 1→1. **Broadcast (1→N) and aggregate (N→1) are explicit
modes**, not emergent behaviour — merging is a different skill from routing.

### Model and request shape

Default to **`claude-opus-5`**. Do not downgrade for cost as a design decision —
that is the operator's call, and it is not free (see the caching trap below).

```go
// Go SDK — github.com/anthropics/anthropic-sdk-go
client := anthropic.NewClient()

resp, err := client.Messages.New(ctx, anthropic.MessageNewParams{
    Model:     "claude-opus-5",   // plain string; no typed constant yet
    MaxTokens: 1024,
    System: []anthropic.TextBlockParam{{
        Text:         registryPrompt,                     // frozen — see below
        CacheControl: anthropic.NewCacheControlEphemeralParam(),
    }},
    Tools:    routingTools,
    Messages: []anthropic.MessageParam{
        anthropic.NewUserMessage(anthropic.NewTextBlock(inbound)),
    },
})
```

Notes that are load-bearing:

- **`temperature`, `top_p`, `top_k` are removed on Opus 5** — sending any of them
  is a 400. Steer with the prompt.
- **Thinking is on by default on Opus 5.** Leave it on and use
  `output_config.effort: "low"` to control spend. Do **not** reach for
  `thinking: {type: "disabled"}` as a cheap-classifier trick: with thinking off,
  the model can write a tool call into its visible text instead of emitting a
  `tool_use` block — the turn succeeds, the call never runs, and no error is
  raised. For a router whose entire output is a tool call, that failure mode is
  silent misrouting. (Disabling is also a 400 at effort `xhigh`/`max`.)
- Verify the Go spelling of the effort field against the compiler; the skill
  documents `output_config: {effort: ...}` at the API level but does not show the
  Go struct field.

### Routing via strict tool use

The route is a tool call, not parsed prose. `strict: true` on the tool
definition (alongside `name`/`description`/`input_schema`, **not** on
`tool_choice`), with `additionalProperties: false` and `required` set,
guarantees the input validates exactly:

```
route_to_agent(agent, thread: "new"|"<id>", wake: bool, confidence: 0..1)
broadcast(topic, body)
aggregate(agents[], question)
none_of_the_above(reason)
```

Prescriptive descriptions matter more than usual here — state *when* to call
each, not just what it does.

### Prompt caching — and the trap in "use a cheap model"

The system prompt is the agent registry: who exists, what each owns, which host,
one line of domain. Cache it and the per-message cost is a cache read (~0.1×
input) instead of a fresh prefix every time.

**The minimum cacheable prefix is model-dependent, and it is not monotonic:**

| Model | Minimum |
|---|---:|
| Claude Opus 5 | 512 tokens |
| Opus 4.8, Sonnet 5, Sonnet 4.6 | 1024 tokens |
| **Haiku 4.5** | **4096 tokens** |

A registry of a dozen agents is a few hundred tokens. On Opus 5 that caches. On
Haiku 4.5 it **silently does not** — no error, `cache_creation_input_tokens: 0`,
full price on every classification. So "route on Haiku to make it cheap" can cost
*more* per call than Opus 5 with a warm cache, and the failure is invisible.
Measure before assuming; the check is `usage.cache_read_input_tokens > 0`.

Caching is a prefix match, so:

- **Freeze the registry prompt.** No timestamps, no "sessions currently online",
  no per-request IDs anywhere in it. Volatile state goes in the *user* turn,
  after the breakpoint. This is the single easiest way to destroy the cache, and
  a router is exactly the service tempted to inject live presence into its prompt.
- Serialize the registry deterministically (sort agents by name). A map iteration
  order change is a cache miss.
- Registry edits (adding a project) invalidate the cache once, by design.
- **Pre-warm at startup** with a `max_tokens: 0` request: it runs prefill, writes
  the cache, returns immediately, bills no output tokens. Hub boot is
  exactly the "moment before traffic" this is for. Re-warm only if idle gaps
  exceed the TTL, or use the 1h TTL (2× write, needs ≥3 reads to pay off).
- Under a burst, a cache entry is only readable once the first response *begins*
  streaming — N parallel classifications on a cold cache all pay full price.

## Tenancy: hosted and self-hosted

One binary serves both. **Self-hosted is hosted with exactly one tenant** —
there is no second code path, so the mode that gets less testing cannot rot.

The agent plane does not change between modes: an SSH key is an SSH key. Only
the human plane differs, so that is the one thing behind an interface
(`IdentityProvider`):

| Client | Self-hosted | Hosted |
|---|---|---|
| Browser | Cloudflare Access | the hosted manager's own login, same interface |
| iOS app | device token | device token |
| Local dev | insecure provider (loopback only) | — |

`AnyOf` composes them so one set of endpoints serves a browser and the app at
once. **A chain is exactly as strong as its weakest member** — a request
rejected by one provider falls through to the next, so adding a permissive
provider widens access for every request. That is why `InsecureProvider`
refuses anything that is not loopback, and why it is never in a production
chain.

Agent keys carry their tenant in the key comment, so a self-hosted
`authorized_agents` keeps working unchanged:

```
ssh-ed25519 AAAA... wsl          # self-hosted: the default tenant
ssh-ed25519 AAAA... alex/wsl     # hosted: tenant "alex", node "wsl"
```

**Tenant comes from the verified identity and nothing else** — never a query
parameter or header. In a hosted deployment that is the entire isolation
boundary between customers, and it is pinned by tests covering mail, sessions,
audit, timeline, subscriptions, stats, broadcast fan-out, and agent drop.

> ### Two collisions that are legitimate, not bugs
>
> Both were found by running two tenants against one coordinator, and neither
> is visible with a single tenant:
>
> - **Two tenants may each run a node called `wsl`.** Connections are keyed
>   `tenant\x00node`; keying by node alone would give one tenant's agent
>   another's commands.
> - **Two tenants may produce the same `session_id`.** It is derived from the
>   project path and host label, both of which repeat across customers. The
>   `sessions` table is keyed `(tenant, session_id)`; keying by `session_id`
>   alone let one tenant's report silently overwrite the other's rows.

## The iOS app

A browser can complete an SSO redirect; a native app cannot do so cleanly on
every launch, and it has somewhere safe to keep a long-lived secret. So the app
enrols once:

1. An already-authenticated human calls `POST /api/devices/code` → an 8-character
   pairing code, valid 5 minutes, single use.
2. The app calls `POST /api/devices/redeem` with that code → a device token,
   returned exactly once, stored in the Keychain.
3. Every later request carries `Authorization: Bearer <token>`.

The code inherits the minting human's tenant, so **an app can never enrol into a
tenant its operator does not already belong to**. There is deliberately no
self-service path from unauthenticated to enrolled — that path would be the
back door around everything else.

Tokens are stored as SHA-256 hashes, so a database dump yields hashes rather
than credentials. Revocation is the primary control (a lost phone), which is
why the 90-day TTL can afford to be long.

The app keeps itself enrolled with `POST /api/devices/renew`, which extends the
calling device's own expiry by a full term. It **extends rather than rotates**:
rotation would bound a leaked token more tightly, but losing the response that
carries the replacement locks the client out, and the whole reason this endpoint
exists is that lockout had no in-app recovery. Renew on launch — an expired
token cannot renew itself, and from there the only way back is a coordinator
restart with `--bootstrap`.

`POST /api/devices/redeem` is the **only** endpoint outside the identity
middleware, because an enrolling app has no credential by definition.

## Atlas: shared knowledge

Three layers, and the write rules that keep them from becoming landfill.

| Layer | Contents | Budget | Loaded by |
|---|---|---|---|
| Global | agent registry, host map, cross-cutting rules | small, hard-capped | every agent, every turn |
| Host | machine quirks, installed tooling, paths | modest | agents on that host |
| Project | everything else — history, decisions, gotchas | unbounded | its owner, plus anyone who queries |

**Global is paid N times; project is paid once.** Every token in global is loaded
by every agent in every session forever. Admission test: *would an agent working
in an unrelated project be wrong without this?* If not, it is project-level.

Three rules:

1. **Agents cannot write global.** They write their own project scope. The
   expensive shared resource simply isn't reachable by the thing that would fill
   it.
2. **Promotion is earned, not judged.** Cross-project lookups go through
   hub, so they can be counted. A fact pulled by three distinct projects
   has demonstrated it is global. Hub *proposes* promotions in a periodic
   digest; a human approves. It never promotes silently.
3. **Everything decays unless read.** Facts carry last-read and a source date.
   Unread for six months → flagged for review, never silently deleted. Facts
   naming a path, host, flag or file are machine-checkable — mark them stale when
   the thing they describe is gone.

**Retrieval, not broadcast.** An agent loads global + its host + its own project,
and *queries* for anything else. Scoping is a security boundary as much as a
quality one: project knowledge crossing into an unrelated session is how a
credential ends up in a context it shouldn't be in.

**Substrate is git.** Markdown with frontmatter, exactly like the memory dirs
today (one fact per file, `MEMORY.md` index, `[[wikilinks]]`). Every node holds a
complete clone — that is the no-single-point-of-failure property, literally.
Hub is a remote plus a search index over it; if the index is down, agents
fall back to local grep. Git gives offline work, real merge semantics instead of
last-write-wins, and history that answers "which agent claimed this, and when."

**Instrument retrieval from the first commit.** Who asked, what came back, which
scope it came from. Promotion-by-demand needs a demand history, and it is the one
part of this that cannot be reconstructed later.

Not solved, deliberately: duplicate facts learned independently by two agents
(needs dedupe at retrieval time), and *contradictory* facts, where the right move
is surfacing the conflict rather than picking a winner.

## Data model (SQLite, WAL)

Operational state lives here; knowledge lives in Atlas.

```sql
agents      (name PK, pubkey, last_seen, version)
sessions    (session_id PK, agent, project, cwd, alias, window, status, updated_at)
tasks       (id PK, agent, thread, state, brief, created_at, updated_at)
messages    (id PK, from_session, to_session, topic, title, body, type, tag,
             created_at, expires_at)
deliveries  (message_id, to_session, delivered_at, acked_at,
             PRIMARY KEY(message_id, to_session))
retrievals  (id PK, at, asker, scope, query, hit_ids)
audit       (id PK, at, actor, action, target, detail)
```

`tasks` is what lets hub answer "what is everyone doing" without asking
each session. `audit` and `retrievals` are new capability, not ports: today
nothing records who sent keystrokes into which pane, or who read what.

## Replacing NATS

| NATS function | Replacement | Difficulty |
|---|---|---|
| `CLAUDE_PRESENCE` KV (30s TTL, 10s heartbeat) | connection liveness | free, more accurate |
| `CLAUDE_MSGS` stream, 24h / 100-per-subject | `messages` + `expires_at` + per-recipient trim | easy |
| Durable consumer per session, explicit ack, 7d inactive | `deliveries` rows; drain returns unacked, acks on drain | **the hard part** |
| `claude.broadcast.<topic>` fan-out | coordinator loop over subscribers | easy |
| Apprise outbound + Telegram inbound relay | folds into hub | easy — already one binary |
| `nudge` cron (15 min, presence KV + tmux send-keys) | push down the live socket | easy, and instant instead of ≤15 min late |

Semantics that must survive:

1. A message for an offline session **waits** and is delivered on reconnect.
2. Delivery is **at-least-once**; drain is idempotent and dedupes by message id.
3. A drained message is **never redelivered**.
4. Retention is bounded — a session that never drains cannot grow the DB forever.

Net: a 3-node JetStream cluster with streams, consumers and a KV bucket collapses
to one binary and one file.

## Renames

**Proposed, not built.** Everything below is a design sketch for folding the
three launcher scripts into the binary. The rename that *did* happen —
phalanx/polemarch/hoplite → shabadoo/hub/node — is done and documented in the
repo `CLAUDE.md`; it did not touch any of the names in this table.

| Now | Proposed |
|---|---|
| `claude.sh` | `sbd` — thin shell script (sources env, execs `tmux attach`). Typed daily, so it stays short |
| `claude-sessions list\|open\|close\|reopen\|clear` | `shabadoo sessions …` — folds into the binary |
| `claude-startup.sh` | `shabadoo boot` |
| `tmuxbridge setup\|doctor` | `shabadoo setup\|doctor` |
| `~/.config/claude/env` | `~/.config/shabadoo/env` |
| `~/.config/claude/tmuxbridge-peers` | deleted — the flock dies with dial-out |
| `~/.config/claude-sessions/folders` | `~/.config/shabadoo/folders` |
| `CLAUDE_HOST_LABEL` | `SHABADOO_NODE` |
| `CLAUDE_SESSION_ID` / `_PROJECT` / `_ALIAS` | `SHABADOO_SESSION_ID` / `SHABADOO_PROJECT` / `SHABADOO_ALIAS` |
| `CLAUDE_SESSION_NAME` (default `claude`) | `SHABADOO_TMUX_SESSION`, default `shabadoo` |
| `CLAUDE_BIN`, `CLAUDE_ARGS`, `CLAUDE_RESUME` | unchanged — they name the `claude` CLI, not us |
| — | `SHABADOO_COORD` (coordinator URL), `SHABADOO_SOCKET` (local unix socket) |
| linux units | `shabadoo-node.service` (system), `shabadoo-boot.service` (user) |
| darwin | `dev.shabadoo.node.plist`, `dev.shabadoo.boot.plist` |
| `tmux.laptop.example.com` | kept, but only for the `shabadoo serve` fallback |
| — | `coordinator.example` — cloudflared + Access only, no Traefik router |

Three scripts collapse into one script plus subcommands, so `launcher.go` —
which exists only to shell out to `claude-sessions` — deletes.

**Migration notes.** `SHABADOO_SESSION_ID` is what mcp-natsbridge keys inboxes and presence
on, and `SHABADOO_NODE` feeds tmux window names *and* the `--remote-control` alias
visible in the iOS app — have `setup` honour the `CLAUDE_*` spellings as
fallbacks for one release. Changing the tmux session name from `claude` to
`shabadoo` orphans every running window, so do it during the full restart, not
live.

## Dependencies

The stdlib-only convention ends here. Be honest about the size of that change:
the module graph went from **0 to 30**.

| Direct | Why | Cost |
|---|---|---|
| `golang.org/x/crypto` | SSHSIG verification | small, quasi-stdlib |
| `modernc.org/sqlite` | durable inbox, audit, tenancy | pulls libc, memory, mathutil, bigfft — most of the 30 |
| `github.com/anthropics/anthropic-sdk-go` | the classifier (not yet wired) | moderate |

Pure-Go SQLite rather than cgo `mattn/go-sqlite3` is deliberate: `make dist`
cross-compiles darwin/arm64 to bootstrap the Mac, and cgo breaks that. The
alternative to SQLite entirely is hand-rolling the durable inbox on an
append-only file — that is precisely the part flagged as hard, and not worth
improvising to save a dependency.

## Failure modes

**Hub is a single point of failure.** Today dm dying costs nothing — every
node serves its own UI. After this, dm dying costs every UI at once. Mitigations:
keep `shabadoo serve` (loopback/tailnet) as a documented fallback; the node
degrades to local-only rather than blocking anything; SQLite and `atlas.git` back
up with dm's other `/docker` volumes.

> **A documented fallback is not a working one.** `serve` was both — documented,
> and completely unusable: the sessions payload was still the flock's flat
> shape (the page rendered "No agents connected"), three endpoints the dashboard
> calls were routed nowhere, and every write 400'd on an undeclared `node`
> field. Nothing catches that, because this mode is only reached when the
> coordinator is already down. It now delegates to the same `handleOp` dispatch
> the node uses, and `serve_test.go` derives its endpoint list from
> `static/index.html` so the page cannot outgrow it silently. Treat any future
> "fallback" the same way: if it is not exercised, assume it is broken.

**Cloudflare dependency.** Access down = no UI, even on the LAN. Accepted; the
`serve` fallback is the escape hatch.

**The classifier is the least reliable component.** It is an API call that can be
wrong, rate-limited, or slow. It must never sit in the path of manual control:
the UI drives sessions directly whether or not hub's brain is answering.

## Build status

Steps 1–4 are built, tested, and proven end-to-end against real tmux: SSH-key
login, session list, pane capture, session detail, pane writes, offline failure,
disconnect/reconnect presence, messaging, audit, device enrolment, and
two-tenant isolation. The dashboard re-point (step 3) shipped with the rename —
the page reads `{nodes:[…]}` from the coordinator, `peers.go` is deleted, and
the standalone `serve` fallback is back to parity with it (see Failure modes).

Also reading rather than merely recording: the audit log now has a panel in the
dashboard, and node/hub build stamps are surfaced per node so a host installed
from a stale checkout is visible as skew instead of as healthy.

Also reading rather than merely recording, since: the durable inbox has a Mail
panel, and a blocked session now reaches a phone instead of only a browser tab.
`/api/stats` was deleted rather than given a reader — every number it returned
is derivable from `/api/sessions`, which the dashboard polls anyway.

Not yet built: mcp-natsbridge's transport swap, the classifier, Atlas, and
everything under Cutover below.

## Build order and cutover

1. Hub skeleton: Access JWT verification, SSHSIG agent auth, SQLite, audit.
2. Node: dial-out, tmux ops over the socket, local-only degradation.
3. UI moves to hub. Flock and `peers.go` delete.
4. Messaging: inbox, presence-from-connection, broadcast, nudge, relay.
5. **Done 2026-07-31.** The bridge is not re-pointed but ABSORBED: `shabadoo
   mcp` is a subcommand, reaching the coordinator through the local agent
   socket. `mcp-natsbridge` is removed from both hosts' MCP config.
6. **Done for `notify_send`** — the Apprise relay lives on the coordinator
   (`--apprise-url`), so removing the bridge cost no capability. Still
   outstanding: homelife-mcp's live-activity console, which reads the presence
   KV that `/agent/peers` replaces.
7. Atlas: git repo, retrieval logging on from the first commit, index second.
8. Stop NATS — **not planned**. The cluster stays up for consumers that are not
   shabadoo's. The goal was for shabadoo to stop depending on it, which it has.

> ### The dual-run gate was dropped, deliberately (2026-07-30)
>
> This plan used to require a full week of dual-run parity at step 5 — both
> transports written, NATS authoritative, message counts and presence compared
> before flipping reads. That was written when the hub's inbox was theoretical.
>
> It is gone because its premise was wrong: **the bridge carries development and
> dogfooding traffic only.** Nothing production depends on a session message
> arriving, so a dropped one costs a retry, not an incident — and a week of
> running two messaging systems to protect traffic that is only us talking to
> ourselves is more risk than it removes, since the failure mode of "two systems,
> one authoritative" is confusion about which one just lost your message.
>
> The availability argument for keeping NATS never held either: the cluster is
> three containers **on dm**, and the hub is moving to dm. Same host, same
> failure domain — that is replication, not redundancy. Anything that takes dm
> down takes both, and if the tailnet is down no agent can reach either one.
>
> What replaces the gate: flip it, use it, and fix what breaks. `/api/messages`
> now has a reader — the dashboard's Mail panel — which is what makes "what
> breaks" visible while the swap is under way.

**The origin-bypass test still does not get skipped** — from a tailnet host, an
Access-protected hub origin must be unreachable without a valid JWT. That one
guards a real boundary, not a migration.

## Open questions

- **Agent identity vs session identity.** One node multiplexes many Claude
  sessions. `session_send` addresses sessions (`claude-<proj>-<hash>`), so
  hub must track session→agent and re-route when a session moves hosts.
- **Authorization granularity.** Access authenticates a human; it does not say
  *which* panes they may drive. Fine for a single operator; revisit before anyone
  else gets an Access grant.
- **UI ownership.** `homelife-mcp`'s natsbridge module (session list, traffic
  timeline, per-session threads, send/broadcast/notify) is roughly half this UI.
  Absorb it or retire it — do not maintain two dashboards over the same sessions.
- **Work is not portable, only knowledge is.** A project agent is bound to where
  its files are: if wsl is down, dm cannot pick up `/c/projects/iptv`. The hive
  survives any node's loss in the sense that nothing is *forgotten*, not that work
  continues. Making the second true means clonable project dirs and placeable
  sessions — a much larger change.
