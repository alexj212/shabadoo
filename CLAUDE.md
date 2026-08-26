# shabadoo

**A framework for many Claude sessions to work as one system** — each in its own
domain, on its own machine, monitored and driven securely from anywhere.

That is the premise, and it is worth stating before the architecture, because
every part of the design falls out of it:

- **Each in its own domain.** A session per project, per machine, holding
  context nobody else has. The point is not to merge them; it is that a
  specialist stays a specialist.
- **Working as one.** They message each other directly (`shabadoo mcp` →
  `session_send`, `broadcast`, `inbox_drain`), see each other (`session_list`,
  `/agent/peers`), and say what they are doing (`session_status_set`). A
  session that cannot see what its peers are waiting on is a silo, not a
  collaborator.
- **Monitored.** One dashboard over every session on every host, a queue of
  the ones blocked on a human, and a notification when one has been stuck.
- **Controlled securely, remotely.** Agents dial out, so no host needs an
  inbound port; every human client presents an enrolled credential; every
  action is attributed and audited.

A coordinator (**hub**), a per-host agent (**node**), and the installer for the
launcher toolchain they drive. One binary, three roles.

Agents dial the coordinator and hold a command stream open; the coordinator
merges every host's Claude sessions into one dashboard, routes writes to the
agent that owns the pane, and records every action in an audit log. It runs
**self-hosted or hosted** from the same binary — self-hosted is hosted with one
tenant — and serves a browser and (by device token) an iOS app.

> **Architecture, decisions, and cutover: `docs/shabadoo.md`.** Read it before
> changing the auth planes, the tenancy model, or the agent protocol.
>
> **Build order: `docs/build-plan.md`.** Which phase before which, and why —
> chiefly what it costs to find something out late. Goes stale as work ships;
> `direction.md` should not.
>
> **Where this is going: `docs/direction.md`.** What the project is becoming —
> an OS whose processes are agents — and the decisions already taken about node
> capabilities, session lifecycle, pane addressing and project naming. Direction,
> not spec: nothing in it is built. Read it before designing anything new, so a
> settled question is not reopened by accident.

## Deployed

**https://coordinator.example** — the coordinator is a container on **dm**,
behind Traefik, tailnet-only. Agents run on the machines that have tmux and
dial out to it.

| Where | What |
|---|---|
| dm, `/srv/shabadoo/` | `shabadoo-hub` container. State bind-mounted at `/srv/shabadoo/data` (`hub.db`, `authorized_agents`), image pinned in `.env` |
| wsl, mac | `shabadoo-node` agent only (`shabadoo-node.service` / `dev.shabadoo.node.plist`), key at `~/.config/shabadoo/agent_key` |

Moved off the WSL workstation 2026-07-30. The hub is a single point of failure
for every node's dashboard, and there it inherited a workstation's uptime —
during one wsl wobble the mac agent flapped for an hour. dm is always on. TLS,
compression on the capture endpoint, a browser secure context (what Web Push
needs) and nightly borg coverage came along with it.

```bash
# upgrade: build here, ship the image, bump the pin (see deploy/docker-compose.yml)
V=$(git describe --tags --always --dirty)
docker build --load --build-arg VERSION=$V -t shabadoo:$V .
docker save shabadoo:$V | gzip -1 | ssh user@coordinator 'gunzip | docker load'
ssh user@coordinator "cd /srv/shabadoo && sed -i 's/^SHABADOO_IMAGE_TAG=.*/SHABADOO_IMAGE_TAG=$V/' .env && docker compose up -d"

make install                   # rebuild the local binary + ~/bin (agents, CLI)
ssh user@coordinator 'docker logs shabadoo-hub -f'
```

`make deploy` is gone: it restarted a local hub unit that no longer exists.
Deployment ops for dm live in the homelab repo (`docs/shabadoo.md`).

> ### Auth postures
>
> `hub` requires exactly one, and refuses to start without it:
> `--device-tokens` (every human client presents an enrolled token; pair the
> first with `--bootstrap`), `--access-team`/`--access-aud` (Cloudflare Access),
> or `--insecure-no-access` (loopback only, development — the provider itself
> refuses any routable address).
>
> **`--trust-network` was removed 2026-07-31.** It admitted every request from
> the bound network, which was the honest migration step while there was no way
> to enrol a client. Enrolment is a closed loop now — bootstrap, pair, QR,
> read-only scopes — so the flag's only remaining effect would have been to
> silently disable all of it: scopes stop applying, the audit log stops naming a
> device, revocation stops meaning anything. One flag that turns off the whole
> security model is a trapdoor, not an option. `shabadoo serve` is still
> unauthenticated, deliberately and locally — see The fallback.
>
> ### `--tailscale-allow` — the tailnet says who you are
>
> When the coordinator is reachable only over a tailnet, the network has
> already authenticated the peer before the first byte of HTTP: WireGuard did
> it, and `tailscale whois` names the user behind the address. So a listed
> login needs **no pairing code, no token, no QR** — it just works, and the
> audit log names the person rather than a device id.
>
> This dissolves the bootstrap paradox below. Minting the first pairing code
> requires an already-enrolled credential, broken once by `--bootstrap` printing
> a code into the service log; someone standing up their own coordinator meets
> that on day one and it is the worst part of onboarding.
>
> **Membership is not authorization, and this is the mistake to avoid.** A
> tailnet holds phones, TVs and family devices — `tag:family` already holds
> `dm:*` here — and reaching this dashboard means driving panes running
> `claude --dangerously-skip-permissions`. So the provider is **default-deny**:
> an empty allowlist admits nobody, and only the exact logins named in
> `--tailscale-allow` get in. A **tagged** node (an agent container, a CI
> runner) is refused outright — it is a service, with no login to attribute an
> action to, and naming who did something is the audit log's whole job.
>
> Tenant defaults to the **tailnet**, which is the mapping the hosted model
> wants: one tailnet, one tenant. Device tokens stay enabled alongside it for
> everything the tailnet cannot speak for — the iOS app, a CLI on a host
> outside it.
>
> Identity comes from `tailscale whois` as a **subprocess**, which is this
> codebase's existing answer to the same question (it already shells out to
> `tmux`, and to `tailscale ip -4` during setup). It adds nothing to `go.mod`.
> Embedding `tsnet` — so the binary *is* a tailnet node, with its own address
> and automatic HTTPS — would swap `WhoisFunc` and touch nothing else, at the
> price of taking the module count from **30 to 547**. Measured, not guessed.
> See `docs/tailnet-identity.md`.
>
> ### Current auth posture: `--device-tokens`
>
> Every human client — browser, CLI, phone — presents a token it was enrolled
> with. No anonymous access, no login form, no Cloudflare in front.
>
> This replaced `--trust-network` **at the moment of the move**, not after it.
> Under network trust anyone who could reach the port drove every pane; that was
> tolerable on a workstation port that is asleep half the day, and not on an
> always-on host behind a wildcard cert that every tailnet device can reach —
> including `tag:family` devices, which already hold `dm:*`. **TLS is not
> authentication**, and a move that added encryption while keeping open access
> would have felt like a security improvement while being the opposite.
>
> Every action is still written to the audit log and readable in the dashboard's
> Audit panel — but now attributed to an enrolled identity rather than a source
> address.
>
> **Enrolling the first device** is circular: minting a pairing code requires an
> enrolled credential. `--bootstrap` breaks it once by printing a single-use code
> to the service log (readable only by someone who could read the database
> anyway), redeemed with `shabadoo pair --code <CODE> --coord <URL>`. Remove the
> flag afterwards — it mints a fresh code on every start.
>
> **Staying enrolled** is `POST /api/devices/renew` (`shabadoo renew`), which
> slides the calling device's own expiry forward by a full 90-day term without
> changing the token. Renew while it still works: an expired credential cannot
> renew itself, and recovering from that is the `--bootstrap` path above.
>
> Cloudflare Access remains built but unused; it needs the origin reachable only
> through the tunnel, which this deployment is not — see the bypass warning in
> `docs/shabadoo.md`.

## Operator CLI

The same human API the dashboard uses, from a terminal — and authenticating the
way the iOS app will, so the device-token path is exercised daily rather than
first tried on the day the app ships.

```bash
shabadoo pair --coord http://host:8787    # prints a pairing URL + code for a phone
shabadoo pair --self                      # enrol this CLI; token -> ~/.config/shabadoo/cli_token
shabadoo sessions                         # every node, `!` marks one waiting on a prompt
shabadoo tail homelab                     # what is on that pane right now
shabadoo folders                          # startable folders, `*` marks already open
shabadoo open techtalks                   # start a session; name, substring, or absolute path
shabadoo send --window 7 "run the tests"  # type into a pane and submit
shabadoo keys --window 7 Enter            # answer a dialog
shabadoo command --pane homelab /clear    # run a slash command
shabadoo kill homelab                     # close a window (asks first)
shabadoo audit                            # who drove which pane
shabadoo mail                             # session-to-session messages
shabadoo devices                          # enrolled clients: scope, days left, push
shabadoo disconnect wsl                   # cut a node's live session now
shabadoo revoke "Alex FireFox"            # sign out an enrolled client, permanently
shabadoo publish dist/                    # upload binaries the coordinator can push
shabadoo releases                         # what is published, and each node's platform
shabadoo upgrade --all                    # replace each node's binary, one at a time
```

**Panes are addressed by name**, not just by index: `tail`, `kill`, `command`,
`send` and `keys` all take a session name and resolve it exactly as `open` resolves a
folder — exact match first, then substring, and an ambiguous match is an error
rather than a guess. Window indices shift as sessions come and go, so a number
in a script is a stale pointer waiting to type into the wrong project.
Substring matching covers the alias and the **folder name**, never the whole
path: matching the full path makes `home` hit every session on a Linux host
through `/home/<user>/…`.

`tail` means what it means in `tail(1)` — the last N lines. The endpoint's own
`lines` parameter is scrollback *depth*, which is right for the dashboard's
viewer and wrong for a command called tail, so depth is `--scrollback` and the
trim happens client-side.

`sessions` points at `tail` before `keys` when something is waiting, for the
same reason the dashboard's queue has no answer button: answering a prompt you
have not read is how "yes" reaches a question about deleting something.

The coordinator URL is remembered in `~/.config/shabadoo/coord` after the first
`pair`, and `--node` is inferred when exactly one agent is connected — with
several it refuses rather than picking, because guessing eventually types into
the wrong machine's pane. Folder names resolve by exact match first, then
substring; an ambiguous match is an error, never a guess.

The QR encoder is stdlib-only (`qr.go`) and pinned against `qrencode` in
`qr_ref_test.go`, mask by mask, for the payload shapes `pair --qr` actually
emits. Two defects it did not originally catch, both of which produced a symbol
that rendered convincingly and scanned as nothing:

- **No version-information block.** ISO/IEC 18004 requires 18 bits, in two
  places, on version 7 and up — and forbids it below. Versions 1-6 were
  therefore correct and everything larger was malformed. The pairing URL crosses
  into version 7 at **107 bytes**, which a MagicDNS coordinator host plus a
  device label reaches easily, so this was reachable in normal use.
- **The terminal renderer set only a background colour.** `▄` paints its lower
  half in the FOREGROUND, which was never set — so half of every mixed cell took
  the terminal's default text colour. On a dark theme the light-over-dark cells
  inverted; on a light theme the dark-over-light ones did. Roughly a quarter of
  the modules were wrong either way. Both colours are now stated on every cell:
  never rely on a terminal's defaults for something a camera has to read.

`pair` prints a URL whose fragment carries the code (`/pair#code=…`). A fragment
is not sent to the server, so the code stays out of access logs and Referer
headers, and the page strips it from the address bar after reading it. **There
is no QR code**: encoding one needs a dependency or a few hundred lines of
encoder, and this project is stdlib-only — the decision is a `--qr` flag away if
typing a code becomes annoying.

## Layout

| File                 | Role                                                            |
|----------------------|-----------------------------------------------------------------|
| `main.go`            | Subcommand dispatch (serve / node / hub / pair / sessions / folders / open / send / keys / setup / doctor). |
| `cmd.go`             | `node` and `hub` wiring: flags, units' entry points.   |
| `cli.go`             | Operator CLI over the human API, authenticating with a device token. |
| `project.go`         | Project identity: which directory owns a session, its path-derived name, and the one-line description read from that directory's `CLAUDE.md` frontmatter. |
| `watch.go`           | The agent's window diff — a window that was present and is now gone is an event. Foundation under deactivation and core-session recovery. |
| `core.go`            | Keeps this node's core session running, with backoff; merges detected and declared capabilities. |
| `deactivate.go`      | A session closed on purpose stays closed. Per-host state beside the boot list. |
| `inbox.go`           | `shabadoo inbox` — drains this session's mail for a prompt hook. |
| `node/capabilities.go` | What this host can do, detected from a curated toolchain vocabulary. |
| `hub/ratelimit.go`   | A windowed per-key limiter, shared by the voice mint and the message loop guard. |
| `hub/stopped.go`     | Reaching a project that exists but is not running: find it, store the mail against the id it will have, ask that node's core session. |
| `ops.go`             | The agent's command handlers — the seam to tmux/claudelog.      |
| `launch.go`          | Launcher core: env file, window naming, launch, window resolution. Every path that starts a window goes through it. |
| `win.go`             | Local commands: `attach`, `win list/open/close/reopen/clear`, `boot`. |
| `mcp.go`             | `shabadoo mcp` — the MCP server each Claude session launches. JSON-RPC 2.0 over stdio; reaches the coordinator through this host's agent socket, so a session needs no credential of its own. |
| `node/socket.go`     | The agent's local unix socket (0600, beside the agent key). Relays an **allowlist** of messaging endpoints to the coordinator with the agent's token. |
| `hub/`               | Coordinator: agent auth (SSHSIG), human auth (Access/device/network-trust), SQLite store, connection hub, human API. |
| `node/`              | Per-host agent: dial-out, SSE command stream, reconnect.        |
| `serve.go`           | Standalone fallback server (`shabadoo serve`) — this host only. Every handler delegates to `handleOp`, the same dispatch the node runs; see The fallback. |
| `setup.go`           | Installer: the `binary`/`path`/`deps`/`env`/`config` steps, plus the `installFile` backup-and-replace primitive everything writes through. |
| `service.go`         | Platform-specific steps: systemd (linux) / launchd (darwin) service + boot launcher, and the linux-only Caddy vhost. |
| `assets.go`          | The two `go:embed` trees (`static/`, `config/`).                |
| `tmux/tmux.go`       | Shells out to `tmux` (list / select-window / send-keys / kill-window / capture-pane), parses it. |
| `claudelog/`         | Reads Claude Code's own session transcripts (`~/.claude/projects/*/*.jsonl`) and summarizes them. Pure file I/O — no tmux, no HTTP. |
| `static/index.html`  | Embedded single-page dashboard (`go:embed`, no build step).     |
| `static/pair.html`   | Device enrolment page, served **outside** the auth middleware.  |
| `config/`            | **Embedded payload:** portable `~/.claude` (CLAUDE.md, settings.json, `skills/`). Work-specific config is excluded — see Vendoring. |
| `launch_test.go`     | Window-name vectors taken from live windows: the naming formula is a compatibility contract, not an implementation detail. |
| `serve_test.go`      | Reads the endpoint list **out of `static/index.html`** and asserts `serve` routes every one. The fallback drifting behind the dashboard is invisible until an outage, so it is pinned rather than reviewed. |
| `agent_e2e_test.go`  | A real agent against a real coordinator in one process: SSHSIG login, SSE stream, result correlation, periodic report, and rejection of an unauthorized key. Only the two host-touching seams (op handler, session reporter) are stubbed — tmux is not what it tests. |
| `setup_test.go`      | `installFile`'s contract (idempotence, backup-on-change, dry-run writes nothing) plus the small pure helpers: `mentionsDir`, `envFileValue`, `sanitizeLabel`. |
| `Makefile`           | `build` / `vet` / `test` / `install` / `deploy` / `vendor` / `vendor-check` / `version`. Builds stamp `main.version` from `git describe`. |

## The launcher (`launch.go`, `win.go`)

Starting and managing Claude windows is built into the binary. It was three
shell scripts (`claude.sh`, `claude-sessions`, `claude-startup.sh`); they are
subcommands now.

| Command | Role |
|---------|------|
| `shabadoo attach` | The daily driver. Start or attach this folder's window — one shared tmux session (default `claude`), one window per project dir. Re-running from the same folder re-attaches; from a new folder, adds a window. Execs into tmux so the terminal is handed over cleanly. |
| `shabadoo win list\|open\|close\|reopen\|clear` | Inspect and control those windows locally. Also what the global `claude-sessions` skill drives. |
| `shabadoo boot` | Opens one window per folder in `~/.config/claude-sessions/folders`. Driven by a cron watchdog (`*/10`) and the `claude-sessions.service` user unit. |
| `shabadoo boot list\|add\|remove` | Edit that list. A **bare `boot` still opens the windows** — the watchdog runs it every ten minutes, so turning this into a noun-only namespace would stop autostart on every host, silently. That makes it the one command whose bare form acts, so it announces what it is about to open first, and **`--dry-run`** answers "what would this do" without doing it — `doctor` to `setup`, applied to the same tension. |
| `shabadoo config [set\|unset\|edit]` | The launcher knobs in `~/.config/claude/env`, with **where each value came from**. The file wins over the process environment, which is the opposite of what most people assume and invisible until it surprises them. |

**Everything that starts a window goes through `launchConfig` in `launch.go`.**
That is the whole point of the port: as two scripts they had drifted, and both
divergences were silent.

- `claude-sessions` never read `~/.config/claude/env`, so it resolved the host
  label from `hostname -s` while `claude.sh` read it from the file. Same folder,
  two window names — so the "already open" check missed and you got a duplicate
  window. It was masked only because the systemd unit sets `CLAUDE_HOST_LABEL`
  explicitly.
- `claude-sessions` launched without `-n` or `--remote-control <alias>` and
  without forwarding `SSH_AUTH_SOCK`, so a window opened from the dashboard had
  no ssh-agent and showed up under a server-generated name.

The window name — `<project>-<host>-<8 hex of sha1(path)>` — is a
**compatibility contract**: it is how a folder finds its existing window.
Change the formula and every running session is orphaned, silently, with a
duplicate opened beside it. `launch_test.go` pins it with vectors taken from
live windows.

`loadLaunchConfig` reads `~/.config/claude/env`; anything set there overrides
the defaults:

| Variable              | Default                                         | Effect |
|-----------------------|-------------------------------------------------|--------|
| `CLAUDE_BIN`          | `claude`                                        | Path/name of the `claude` CLI |
| `CLAUDE_ARGS`         | `--dangerously-skip-permissions`                | Args appended to every launch |
| `CLAUDE_RESUME`       | `--continue`                                    | Resume flag (empty disables — empty is honoured, not treated as unset) |
| `CLAUDE_SESSION_NAME` | `claude`                                        | tmux session name |
| `CLAUDE_HOST_LABEL`   | `hostname -s` (sanitized)                       | Short host label in window names + remote-control alias |
| `CLAUDE_CONFIG_DIR`   | `$XDG_CONFIG_HOME/claude` or `~/.config/claude` | Where the `env` file is read from |

The env file **wins over the process environment**, because the script it
replaced `source`d the file and a plain `export X=...` overwrites what was
inherited. Reversing that order would rename every window on a host whose
service environment disagrees with its env file.

`CLAUDE_HOST_LABEL` is the one that matters — it shows in tmux window names
**and in the iOS / web Code app session list**. Keep it short and
machine-distinctive (`wsl`, `mac`, `dm`).

Per-host config deliberately lives **outside** this repo and is never vendored:
`~/.config/claude/env` (knobs) and `~/.config/claude-sessions/folders` (boot
folder list). `shabadoo config` and `shabadoo boot add/remove` edit them
**surgically** — one line at a time, comments untouched. Both files hold
decisions with reasons written beside them, and a tool that regenerated either
from a parsed map would delete every one of those reasons the first time it
was used.

`boot add` refuses a folder that does not exist, because an entry that cannot
open starts nothing and says nothing — it would sit there looking configured.
Folders are compared **through symlinks**, so removing by either spelling of
the same path works; that is the same reason `/api/folders` resolves them.

## The fallback (`shabadoo serve`)

The standalone server is what drives this host's panes when the coordinator is
unreachable — the mitigation `docs/shabadoo.md` names for the hub being a single
point of failure. It serves the **same embedded dashboard** the hub does.

**Every handler delegates to `handleOp`**, the same dispatch table the node runs
coordinator commands against, so the two modes execute identical code for
identical requests. That is not tidiness; it is the only arrangement that keeps
the fallback working, and it was established by finding out what happens without
it. `serve` had its own copy of the handlers, and it had drifted into being
completely unusable:

- `/api/sessions` still returned the flock's flat `{now, node, sessions}` while
  the page reads `data.nodes` — the fallback rendered as **"No agents
  connected"**, i.e. indistinguishable from a total outage;
- `/api/keys`, `/api/input-state` and `/api/folders` were routed nowhere, so a
  dialog could not be answered and the folder picker was empty;
- every write returned **400**, because the dashboard posts `{node, …}` on all
  of them and the per-op structs never declared `node` while using
  `DisallowUnknownFields`.

All of it silent, because nothing exercises this mode until the coordinator is
already down — which is the worst moment to discover the escape hatch is welded
shut. `serve_test.go` now extracts the endpoint list from `static/index.html`
itself and fails when the page calls something this mode does not route; a
hardcoded list would have been written from the same stale understanding that
caused the drift.

Endpoints the hub serves that this mode structurally cannot — `/api/audit`,
which reads a database `serve` does not have — are routed to an explicit **501**
rather than left to fall through. `GET /` is a file server that answers any
unmatched path with `index.html` and a 200, so an unrouted API path returns a
web page where the caller expects JSON.

`serve` is drive-only: there is no audit log, because there is no database. A
fallback that refused to work without one would defeat its own purpose.

## Versions

Builds stamp `main.version` from `git describe --tags --always --dirty`
(`make build` / `install` / `dist`); an unstamped build reports
`dev (unstamped)`, which is deliberately not mistakable for a release.

- `shabadoo version` (or `--version`) prints it.
- Each node reports it at login; the hub stores it and returns it per node in
  `/api/sessions`, alongside its own as the top-level `version`.
- The dashboard shows the hub's build in the header and each node's as a badge,
  **amber when the two differ**.

This exists for one hazard: `setup --service` installs *the binary that is
running it*, so a stale checkout silently downgrades a node, and before this the
downgraded host looked perfectly healthy. A hardcoded version constant could not
do the job — a stale binary carries a stale constant that still looks current.
A node's version is only as fresh as its last login: an agent holding a token
across an upgrade still reports the build it authenticated with.

### The downgrade guard

Builds also stamp `main.buildTime` with the **commit date**, and
`setup` refuses to replace a newer installed binary with an older one.

`git describe` strings cannot be ordered — given `a376549` and `1c8fb18` there
is no way to tell which came first — so the comparison needs a timestamp. The
commit date rather than the build date keeps it reproducible: the same source
builds to the same stamp, so Docker layers still cache.

`setup` asks the installed binary via `shabadoo version --json`, which exists
precisely so two copies of this program have a contract better than a
human-readable line. What it does:

| Situation | Result |
|---|---|
| installed build is older, or the same | installs |
| installed build is **newer** | **refuses** — "you are probably running setup from a stale checkout"; `--force` overrides, loudly |
| this binary is **unstamped** (plain `go build`) over a stamped one | **refuses** — same hazard from the other side |
| installed binary predates `version --json`, or is not this program | installs; "cannot tell" must not become "refuse" |
| nothing installed yet | installs — the fresh-machine case is never blocked |

## Nodes, projects and sessions (phases 0-3 of `docs/direction.md`)

The system used to see one kind of thing: a tmux window. These four changes make
it see what is actually there. **`docs/direction.md`** is why; this is what runs.

**A session has a `kind`** — `claude`, `worker`, or `core`. The table used to
claim every window held a Claude session, so `top` in one reported itself as a
project, and `ResolveSession` only passed `claude-`-prefixed ids so a non-Claude
tool could not be addressed at all. Classification keys on the launcher's 8-hex
window suffix, **not** the pane command: tmux misreports that on macOS, where a
real session reads as `2_1_220`.

**A project is a directory, named by path.** Its root is the nearest ancestor
`CLAUDE.md` **that is also a git root** — the git qualifier is load-bearing, or a
`CLAUDE.md` in a workspace or home directory swallows every project beneath it.
A session scoped into a subfolder reports `shabadoo/hub` rather than a bare `hub`
belonging to nobody. Checked before shipping that no existing project was
renamed, because project names are how mail is addressed.

**A project describes itself** in one line of its `CLAUDE.md` frontmatter
(`description:`). It is the routing card, and it is trigger text rather than a
summary — *when should this be reached for* — because if sessions route work to
each other then the quality of the description IS the quality of the routing. A
vague line does not fail loudly; it delivers work to the wrong expert.

**A node reports what it can do.** The agent detects a curated toolchain
vocabulary (`go`, `ffmpeg`, `gpu.nvidia`, `ios.build`, …) — presence only, no
versions, since "can this node build Go" is the routing question and "which Go"
is answerable by whoever gets the work. The node's own project declares what no
probe can establish (`always-on`, `apple-toolchain`). **Detection wins on
conflict**: a declared capability that is checkable and absent is dropped, not
believed, because the cost of believing it lands after a handoff.

### Each node has a core session

`<shabadoo-dir>/<host label>/` — a project like any other, holding what that
machine knows about itself. The agent derives the path from state directory plus
host label, so there is nothing to configure. `setup` scaffolds it when absent
and never again; **`uninstall --purge` skips it**, because hand-written knowledge
about a machine is the same class as the env file.

It is the addressable "you" of that machine and the only thing permitted to start
sessions there. **The agent restarts it** — one report cycle rather than the
ten-minute watchdog — with backoff, or a core session that fails instantly turns
its supervisor into the outage. It is exempt from deactivation: marking the only
thing that may start sessions as deliberately not running is a deadlock.

Keep it cheap. Always-on plus unbounded context is the one problem no mechanism
here solves; it routes and decides, and delegates the doing.

### A closed session stays closed, and mail still reaches it

An exit records intent in a file beside the boot list, and `boot` honours it —
otherwise the watchdog reopens within ten minutes, which defeats closing one to
free resources. **Opening clears it**: the file says "do not start this on your
own", never "refuse to start this".

Mail to a closed project no longer bounces. Every startable folder carries the
session id it *would* have, so the message is stored against it and drains when
that session starts. **Stored first, core session asked second** — the message is
safe whatever that session decides, so a slow decision costs latency rather than
a handoff. The coordinator never starts it directly: otherwise any peer could
spend a machine's resources by writing to it.

An unknown name still bounces and an ambiguous one is still refused. Waking the
wrong project also delivers somebody's work to the wrong expert.

### Two guards on messaging

**A send rate limit** — 60/hour per sending session, audited as
`message.throttled`. This is the loop guard, in place *before* mail could start
sessions rather than alongside it. A hop chain was planned and abandoned on
contact: there is no mechanical causal link between a message received and one
later sent, and a guard relying on the sender to declare itself is not a guard.

**An empty message is refused.** Reported from the field: three were accepted,
stored, acknowledged with an id and delivered, so a sender believed it had handed
off work while the recipient got a notification containing nothing. It succeeded
at every layer, which is what made it invisible.

## Delegated work (`tasks`)

Handing work to a peer was mail: acknowledged when drained, and after that the
system knew nothing. There was no way to ask what had been handed off and never
came back — the sender remembered, or nobody did. The `tasks` table sat in the
schema from the beginning with nothing reading it; this is that, wired.

`task_create` hands work over **and sends the brief in one call**. Two calls for
one act would drift, and the drift that matters is work handed over with nothing
tracking it. The task id travels in the message body, because the assignee needs
it to report back and making it look one up is a step it will skip.

States are `open`, `active`, `blocked`, `done`, `dropped` — five and no more.
Every state a session must choose between is a decision it has to get right, and
a vocabulary nobody can remember gets used vaguely rather than precisely.
**`dropped` exists because deciding not to do something is an answer**, and
without it that outcome has nowhere to go but silence.

Whoever asked is **told automatically when a task ends**. They delegated and
moved on; without that they would have to poll, which is the habit a task list
exists to remove.

An empty brief is refused, for the reason an empty message is. An unknown state
is refused with the valid set named. An update with no note leaves the previous
one alone rather than erasing the assignee's last word.

**A task nobody chases is a row in a table**, so `taskwatch.go` chases them —
`blocked.go`'s shape reused in full, because every mistake in edge detection has
the same form and it is not one anyone spots from a single observation.
Untouched for **6 hours** raises it once, then **daily** while it stands, and
touching it resets the state so a task that stalls twice is two events. The
thresholds are far longer than the blocked-session grace deliberately: a dialog
blocks a machine and is answered in seconds, while chasing somebody else's work
after an hour is nagging — and a notifier that mostly cries wolf gets muted,
which costs the one that mattered.

The sweep rides the coordinator's existing hourly maintenance tick. Tasks do not
arrive on a timer the way agent reports do, so without it nothing would ever
look.

## Panes, protocol, and what a session costs

**A session is a pane, not a window.** `session:window` is resolved by tmux to
whichever pane is ACTIVE, so a split window already accepted writes aimed at a
different one — a keystroke in the wrong pane looks identical to one in the
right pane, from every side. Every read and write now takes a pane; a negative
pane keeps the old meaning, because that is what a caller which never heard of
panes means and the only safe reading of silence.

Reporting moved from windows to panes for the same reason: a window's report
carries the active pane's command, path and pid, which is right for one pane and
a silent lie for two. Each pane has its own directory and therefore its own
project — that is what makes a split window legible rather than a place two
projects hide behind one name.

**Pane 0 keeps the session id its window has always had.** Ids are how mail is
addressed, so renaming them together would orphan every undrained handoff, and
nothing changes at all until somebody splits a window.

**The protocol is negotiated, and mismatches are refused rather than degraded.**
Only a build stamp was exchanged before — a fact about a binary, not a contract
about behaviour. `upgrade --all` is deliberately serial, so a mixed fleet happens
during *every* upgrade; an agent predating pane addressing ignores the field and
writes to the active pane, which is the failure the addressing removes. One
guard, at the single point every operation passes through. Pane 0 and an absent
pane do not trip it, or this would fail every write to a node in the window
before it is upgraded.

**Sessions report what they have spent.** Context is the scarce resource this
design is arranged around, and it was measured nowhere at fleet level: the
numbers were already parsed and served one session at a time, so nothing
aggregated them and a router could not weigh cost. Affordable on a five-second
report only because `claudelog` caches incrementally — an unchanged transcript
costs a stat, a grown one costs its new lines. Measured across eleven live
sessions: 1.57s cold, 122ms warm.

## A session cannot see tools added after it started

Each Claude session launches `shabadoo mcp` as a child **at start**, and that
child advertises its tool list once. Upgrading the binary does nothing for it:
the session keeps the surface it was born with until the window is restarted.

So a release that adds a tool reaches nobody already running, and the failure is
invisible from exactly where it matters — inside the affected session, which has
no way to know its own surface is behind. Found by a session being told about
three new tools, trying one, and not finding it.

`stale.go` reports it as `tools_stale`, rendered as a quiet badge on the
dashboard row and a count under `shabadoo sessions`. Measured on this fleet the
day it shipped: **11 of 11** sessions on the node that could measure. It is the
default outcome of an upgrade, not a corner case.

**The remedy restarts the process.** `/clear` does not work and saying so
matters, because it runs cleanly and fixes nothing — the MCP child is launched
by the session and outlives a context clear. Verified rather than reasoned:
every MCP child on this host is within seconds of its `claude` parent's age,
several of them 12 days old across many clears.

**It reads the process table two ways, and that is the whole point.** The first
version was `/proc`-only with no build tag, so on macOS `os.ReadDir("/proc")`
simply failed and the empty result became `tools_stale: false` on *every*
session — a node reporting "all clean" when it had not looked. It surfaced as
mac reporting 0 of 5 while wsl reported 11 of 11, minutes after both were
upgraded, which is not a plausible difference between two machines. **A detector
that answers "fine" when it means "I cannot tell" is worse than an absent one**,
because nobody checks behind a clean answer. `ps -Ao pid=,ppid=,etime=,command=`
is the portable reader, and it is the fallback on Linux too rather than only the
macOS path.

Elapsed time rather than start time: `ps -o lstart=` emits a locale-formatted
date that has to be parsed back, and this only feeds a comparison against a build
stamp hours or days away. `stale_test.go` runs **both readers over the same live
process table** and requires them to agree, because a fixture only ever agrees
with whatever its author assumed — which is precisely how the `/proc`-only
version shipped looking correct.

## An agent's report is one transaction

A node's session list is replaced wholesale on every report — the agent is the
authority on its own tmux server, so a window that vanished must vanish here.
That was a `DELETE` followed by N separate upserts, each in its own implicit
transaction, which meant **every reader during a report saw an arbitrary prefix
of that node's sessions, or none of them.** Agents report every 5 seconds and a
busy node has eleven windows, so the exposed window was a real fraction of the
time.

The dashboard raced identically and merely flickered, which is why it was never
caught there. What made it visible was the **recipient resolver**, the one
reader that turns an incomplete view into a hard error: `session_send
to="minutes"` was refused as unknown while `minutes` was live, and the refusal
listed the 8 sessions it could see out of 16 as though that were the fleet.

That is the part worth remembering. **A refusal that enumerates what exists
reads as authoritative**, so an incomplete index becomes a claim about the world
— "that project is not there" rather than "I could not see it". The peer that
hit it nearly concluded the session had exited and started it again, which would
have spent a machine's resources on a lie. Same shape as the broadcast that
reached zero recipients: the mechanism was confidently silent about its own
incompleteness.

`ReplaceAgentSessions` does the swap in one transaction. WAL means readers take
the pre-transaction snapshot until it commits, so a reader sees the old list or
the new one, never a prefix of either. `store_test.go` hammers the report path
while reading and pins both layers — the store never half-visible, and the
resolver still finding a project by name while its node reports. Both were
confirmed to **fail against the previous implementation** before being kept; a
race test that has not been seen to fail is decoration.

## Blocked-session notifications

When a session sits at a prompt for **90 seconds**, the coordinator sends a
notification (`hub/blocked.go`). It rides the same `--apprise-url` relay as
`notify_send`, so a deployment configures one notifier rather than one per
producer, and it is enabled only when that URL is set — with nowhere to send,
the watcher would be bookkeeping for messages that go nowhere.

The policy is the whole design; the mechanism is a map:

- **Edge-triggered, not level-triggered.** Agents report every 5 s and a blocked
  window reports `dialog` every time. Notifying on the state would be a
  notification every five seconds, forever.
- **90 seconds of grace.** Most prompts are answered in seconds by whoever is
  already at the keyboard. Notifying immediately would buzz a phone for every
  permission dialog, and a notification stream that is mostly noise gets muted —
  which costs the one that mattered.
- **One reminder an hour** while it still stands (`blockedRepeat`), so a session
  blocked overnight is not represented by a single alert from twelve hours ago.
- **Answering resets it.** A window that leaves `dialog` — or disappears —
  loses its state, so blocking twice in a morning is two events, not one.
- Per (tenant, node, window): one agent's report never clears another's state.

`blocked_test.go` pins all of it, because every mistake in edge detection has
the same shape and it is not a shape you notice from one report.

An APNs sender is **not** built. Devices can register a push token
(`PUT /api/devices/self/push`) and `DeviceStore.PushTargets` returns them, so
the sender is a drop-in — but it needs an Apple team id, key id and `.p8` from a
developer account that does not exist yet, and writing an untestable APNs client
against an unknown bundle id would be worse than the Apprise path, which reaches
the same phone today through Telegram/Pushover.

## Upgrading a node (`publish` / `upgrade`)

Upgrading a node was scp plus a service restart, per host, per platform, by
hand — the ritual that produces the version skew the build stamps exist to make
visible. The transport to fix it was already there: every agent holds an
authenticated stream open.

```bash
make dist && shabadoo publish dist/   # upload every platform to the coordinator
shabadoo upgrade --all                # one node at a time, each confirmed back
```

**Operator-triggered, never automatic.** Trust was never the obstacle — the
coordinator can already send keystrokes into panes running
`claude --dangerously-skip-permissions`, so "the hub can make a node run code"
was always true. What is missing is a good answer to *"the new build is broken
on every host at once"*: a push fans out, and recovery is SSH to each machine,
which is precisely what dial-out agents exist to avoid needing. So a human
decides when, and `upgrade` does one node at a time, waiting for each to come
back before touching the next.

**The hub serves bytes an operator gave it; it never fetches a binary.** That
keeps the "self-contained, no network install path" convention intact. It also
*cannot* build what it ships — it runs linux/amd64 in a container and the Mac
needs darwin/arm64 — which is why `publish` exists at all. Releases live in
`--releases DIR` on disk, not in `hub.db`: a 15 MB blob per platform per version
would bloat the file every backup touches.

The directory keeps the **3 newest versions per platform**, pruned on publish.
A publish is ~70 MB across four platforms and the directory sits inside the
bind mount the nightly borg run covers, so unbounded growth is not just disk —
it is disk in every backup, forever. Three is the running build, the one before
it (what `<path>.prev` on each node corresponds to), and one more for when the
answer to a bad deploy is "go back further".

Four checks stand between a published file and a node running it, and each
exists for a failure that is otherwise unrecoverable — a node that overwrites
itself with a binary it cannot execute cannot be told anything again:

| Check | Where | Catches |
|---|---|---|
| platform read by **running** the file (`version --json`) | `publish` | a mislabelled upload; falls back to the `make dist` filename when cross-compiled |
| release platform must equal the node's reported platform | hub | sending a Mac a Linux build |
| sha256 | node | a truncated or corrupted download |
| **run the staged binary and check it reports the expected version** | node | wrong architecture, not-this-program, won't execute here |

Only then is the swap done, `rename(2)` onto the running executable from a temp
file **in the same directory** — a cross-device rename fails, and copying
instead would be the non-atomic write this avoids. The outgoing binary is kept
at `<path>.prev`, which is not an automatic rollback (nothing is left running to
perform one) but turns recovery into one `mv` over SSH.

The **first** upgrade on any host is still manual: a node cannot report a
platform until it runs a build that knows to. `upgrade` says exactly that,
distinguishing "connected but predates upgrade support" from "not connected" —
the same empty string, and the difference between one `scp` and going to check
the network.

Restart is by exiting **non-zero** (code 70): the units already supervise the
process (`Restart=on-failure` / launchd `KeepAlive`), and a clean exit would
satisfy `on-failure` and leave the node simply gone.

## Credential lifetimes

Two different tokens, both of which used to expire with nothing renewing them.

**Agent tokens** (node → coordinator, from the SSHSIG login) last 24h. The
stream is authenticated **once, at connect**, so a node whose token expired
underneath an open stream kept *looking* connected while every `/agent/report`
and `/agent/result` silently 401'd — the dashboard showed a healthy node with a
session list frozen at the moment the token died. A 401 on either now tears the
stream down (`credentialRejected`), and the existing reconnect loop performs a
fresh login. **Only 401**: a 5xx is the coordinator having a bad moment, and
re-authenticating on one would turn a blip into a reconnect storm across every
node simultaneously.

**Device tokens** (browser, CLI, phone) last 90 days. `POST /api/devices/renew`
existed from the start and *nothing called it*, so every enrolled client was
counting down to a lockout whose only recovery is restarting the coordinator
with `--bootstrap` — a trip to a terminal, impossible from the phone that just
expired. A client cannot renew in good time without knowing when "good time" is,
and it has no way to ask: the token is opaque and `/api/devices` lists everyone's.
So the coordinator reports it, on every authenticated response:

```
X-Shabadoo-Token-Expires: 1801180800
```

Both clients act on it below 30 days — the dashboard after a *successful* poll
(renewing against a coordinator you just failed to reach is a write in the dark)
and at most once a day via `localStorage`; the CLI opportunistically, once per
process, so any command the operator runs keeps the credential alive. 30 days
leaves two months of chances to be used once, and once is a full fresh term.

## Install (`shabadoo setup`)

The binary is self-contained — copy it to a fresh machine and it can install
the whole toolchain with no network and no source tree:

```bash
shabadoo doctor         # report what would change; writes nothing
shabadoo setup          # apply
```

Default steps (skip any with `--skip=deps,config`):

| Step      | Effect                                                                 |
|-----------|------------------------------------------------------------------------|
| `binary`  | This binary → `--bin-dir` (default `~/bin`), mode 0755. `shabadoo attach` is the daily driver, so the binary has to resolve by name from a fresh shell. |
| `path`    | Appends a PATH export to the shell rc **only if** the bin dir isn't already on PATH — `~/bin`, `$HOME/bin` and `${HOME}/bin` spellings all count, so it won't duplicate an existing entry. |
| `deps`    | Reports missing `tmux` / `claude` (required) and `nats` (optional) with per-OS install hints. Never installs anything itself. |
| `env`     | Scaffolds `~/.config/claude/env` **only if absent** — it holds decisions (host label, claude flags), not content this binary owns. `--force` overrides. |
| `config`  | Portable `~/.claude`: `CLAUDE.md`, `settings.json`, `claude-powerline.json`, `statusline-powerline.sh`, `session-bridge-prompts.md`, `skills/`. Additive — target-only skills are never deleted. The installed `CLAUDE.md` imports `CLAUDE.local.md`, which this step never writes. |

Three opt-in steps. Only the Linux system-level writes escalate (via `sudo`);
everything else runs as the invoking user, so **never run `sudo shabadoo
setup`** — that would install the user-level files into root's home.

| Flag        | Effect                                                                |
|-------------|-----------------------------------------------------------------------|
| `--service` | Runs **both** halves of a node as services — the coordinator and this host's agent, because a coordinator with no agent has nothing to show. Copies the running binary to `<bin-dir>/shabadoo` first, so `ExecStart` resolves. **linux:** `/etc/systemd/system/{hub,node}.service` + `daemon-reload`/`enable --now`/`restart` (sudo). **darwin:** `~/Library/LaunchAgents/dev.shabadoo.{hub,node}.plist` + `launchctl bootstrap` (no sudo). State (db, agent key, authorized list) lives in `--shabadoo-dir`, default `~/.config/shabadoo`. |
| `--boot`    | Login launcher opening one window per folder in `~/.config/claude-sessions/folders`. User-scoped on both platforms, no sudo. **linux:** `~/.config/systemd/user/claude-sessions.service`. **darwin:** `~/Library/LaunchAgents/dev.shabadoo.boot.plist` (`RunAtLoad`, no `KeepAlive` — it is a one-shot, not a daemon). |
| `--caddy`   | Appends a vhost block to `/etc/caddy/Caddyfile`. Host defaults to `tmux.<host-label>.example.com`; bind IP defaults to this host's `tailscale ip -4`. **Linux only** — on macOS bind the tailnet directly with `--addr tailscale:PORT`. |

`--service` **requires an auth posture** and refuses to write anything without
one — `--device-tokens`, or `--access-team X --access-aud Y`. There is no
default on purpose: `hub` exits without one, and the posture that would
"just work" would be the absence of authentication, which is not something an
installer should choose quietly. The two files the units need at runtime —
`agent_key` and `authorized_agents` — are **reported, never generated**: one is
a credential, the other a trust decision. Setup prints the `ssh-keygen` line
instead.

> `--service` installs **the binary that is running it** (`installSelf`). Run a
> stale `./shabadoo setup --service` and it silently downgrades the deployed
> coordinator — the old one is backed up, but the endpoints are gone until you
> notice. `make install` now builds the repo binary and `~/bin` together so the
> two cannot drift.

`--service --coord URL` installs the **agent alone** and joins an existing
coordinator — no posture needed, since no coordinator is installed here. That
is what every machine after the first wants: a second coordinator would be a
second dashboard showing one host, not another node on the first.

`--addr` is baked into the coordinator unit **unresolved**, so `tailscale:8787`
stays a token that `hub` expands at startup and a re-assigned tailnet
address still binds. The agent's `--coord` cannot work that way — it needs a
concrete URL — so it is resolved once at install time; if the tailnet address
changes, re-run setup.

Safety properties worth preserving:

- **Idempotent.** A second run reports `unchanged` for everything; on a
  correctly-installed host `shabadoo doctor` reports zero changes.
- **Never clobbers silently.** Any file whose content differs is copied to
  `<path>.bak.<epoch>` before being replaced. Identical content is not
  rewritten, so no backup churn.
- **Atomic writes.** `writeAtomic` uses temp-file + rename — overwriting
  a file in place while a process is reading it would corrupt that read.
- **Caddy is validated before reload**, with `/etc/caddy/caddy.env` sourced so
  `{env.CF_API_TOKEN}` resolves (a bare `caddy validate` fails on the empty
  token). A rejected config is rolled back and Caddy is never reloaded — a bad
  block would take down every other vhost this Caddy fronts, not just this one.
- The Caddy step is a no-op if a block for that host already exists; it never
  appends a duplicate site address, which Caddy rejects outright.

## Uninstall (`shabadoo uninstall`)

The counterpart to `setup`, and it exists for the same reason `setup` is
idempotent: this binary gets installed on machines to try it, and the only way
off used to be remembering every path it wrote. Nobody does, so what actually
happened was a dead unit left enabled on a host, failing at every boot forever.

```bash
shabadoo uninstall --dry-run   # what would go
shabadoo uninstall             # services
shabadoo uninstall --all       # ...and the binary
```

The governing rule mirrors setup's "never clobbers silently": **it removes what
setup generated and nothing else.** Setup deliberately scaffolds two things it
never overwrites — the env file (decisions) and `~/.claude` (config with hand
edits) — because they are not content this binary owns. Not owning them on the
way in means not deleting them on the way out.

| Path | Uninstall |
|---|---|
| `/etc/systemd/system/shabadoo-{hub,node}.service` | disabled, removed, `daemon-reload` (sudo) |
| `~/.config/systemd/user/claude-sessions.service` | disabled, removed |
| `~/Library/LaunchAgents/dev.shabadoo.{hub,node,boot}.plist` | `bootout`, then removed — with their `.bak.*` |
| `<bin-dir>/shabadoo` | only with `--all` |
| `~/.config/shabadoo` (agent key, `hub.db`, `authorized_agents`) | **kept** unless `--purge` |
| `~/.claude`, `~/.config/claude/env`, `~/.config/claude-sessions/folders` | **never** |
| `/etc/caddy/Caddyfile` | **reported, never edited** — a bad edit takes down every other vhost this Caddy fronts |

`--purge` warns before it runs: `hub.db` holds every enrolled device token, so
removing it signs out every paired phone and browser and takes the audit log
with it. That is a decision, not a cleanup.

Keeping the state directory by default is what makes a reinstall trivial —
`agent_key` survives, so the node comes back under its existing authorization
rather than needing a new key appended on the coordinator. Verified as a real
round-trip on the Mac, not reasoned about: uninstall → no jobs, no plists →
`setup --service --coord URL` → online again in seconds.

`launchctl bootout` runs **before** the plist is removed: launchd holds a job by
label, not by file, so deleting the plist first leaves the agent running with
nothing on disk to explain it.

## Vendoring the embedded payload (`config/` + `config.local/`)

The binary ships **two** payload trees, merged at install time:

| Tree | Committed? | Contents |
|---|---|---|
| `config/` | **yes** | the *portable* half — behavioural guidance, a generic `settings.json`, and skills that name nobody's infrastructure. Safe to publish |
| `config.local/` | **no** (gitignored but for `.gitkeep`) | one operator's real `~/.claude`, filled by `make vendor` |

`make vendor` refreshes **`config.local/`** from this machine's live `~/.claude`;
`make vendor-diff` shows drift without changing anything. Vendoring is
deliberate, never automatic.

**Why two trees.** There used to be one, and it was one person's `~/.claude`:
tailnet hostnames, LAN addresses, Cloudflare zone IDs and six infrastructure
skills, embedded in every binary and committed to the repo. That is a *feature*
for its operator — one file carries their whole setup to a new machine — and the
single reason this repo could not be published. Scrubbing `config/` would not
have held: `make vendor` is a straight copy and would have undone the scrub on
the next run. Writing the personal half somewhere **git never sees** is what
makes it stick, and nothing is lost — `make vendor && make build` still produces
a binary carrying everything it carried before.

A fresh clone has only `.gitkeep` in `config.local/`, so it builds and installs
the portable payload alone. Verified both ways, not reasoned about.

**Install merges, then writes once.** `mergePayloads` flattens both trees —
overlay wins a collision, everything else is added — and each path is written a
single time. The obvious alternative (install base, then install overlay on top)
is wrong in a way that only appears on the *second* run: the base sees the
overlay's file, calls it a difference, backs it up and replaces it, and the
overlay puts it back. Every run would churn two files and leave two backups, and
`doctor` would never report a clean host again.

**The step is additive.** It writes into a directory the operator also edits by
hand and runs repeatedly on machines that already work, so it never deletes: a
skill present on the target and absent from the payload stays.

`make vendor-check` enforces both halves of publishability — a denylist of
client and product names (`VENDOR_DENY`) grepped across both trees, **and** a
check that nothing under `config.local/` is tracked by git. It runs at the end of
every `make vendor` and fails the build rather than stripping silently, because
the fix belongs in the live config, not in a filter here.

> **`go:embed` skips symlinks silently** — no error, no warning, the files just
> aren't in the binary. Several skills are symlinks into `~/.agents/skills`, so
> `make vendor` uses `rsync -aL` to dereference them into real files. Keep the
> `-L`. After changing anything about vendoring, verify with a fresh-target
> install (`HOME=$(mktemp -d) ./shabadoo setup`) and diff the result against the
> payload — comparing file *counts* is what catches a silently-dropped tree.

`skills/watch` is its own git checkout; vendoring excludes `.git` so it doesn't
end up nested in this repo or embedded in the binary.

Deliberately **not** vendored at all (per-machine or private): `settings.local.json`,
`mcp_settings.json`, `projects/`, `stats-cache.json`, `.credentials.json`,
`history.jsonl`, `todos/`, `shell-snapshots/`, `CLAUDE.local.md`, `commands/`.

### The portable/local split

`~/.claude/CLAUDE.md` is **portable** — how to work — and a generic version of it
is what `config/` ships. Everything specific to one machine or one employer (the
project registry, host names, private Go modules, work toolchains) lives in
`~/.claude/CLAUDE.local.md`, which the shipped `CLAUDE.md` imports with
`@CLAUDE.local.md` and which vendoring never touches. `commands/` is excluded for
the same reason: those slash commands are work tooling, not toolchain.

The same split now runs one level up, between the two payload trees — and that
is the one that matters, because `config/` is what a stranger receives.

## Bootstrapping another machine

```bash
make dist                                          # linux + darwin, amd64 + arm64
scp dist/shabadoo-darwin-arm64 mac:bin/shabadoo
ssh mac 'chmod +x bin/shabadoo && bin/shabadoo setup'
```

The payload is whatever was vendored at build time, so a darwin binary built
here carries this host's config — the same direction the old installer synced.

To make that machine another node on this dashboard:

```bash
# on the mac — agent only, joining the coordinator, plus the login launcher
ssh mac 'ssh-keygen -t ed25519 -N "" -C mac -f ~/.config/shabadoo/agent_key'
shabadoo setup --service --boot --coord https://coordinator.example
```

Then authorize it **on the coordinator** — append the Mac's
`agent_key.pub` (keytype + key + the node name as the comment) to
`/srv/shabadoo/data/authorized_agents` on dm. **No restart**: the file is
re-read when it changes, and restarting to admit one agent would disconnect
every other. The agent dials out, so the Mac needs no inbound reachability;
only the coordinator does.

Set `CLAUDE_HOST_LABEL=mac` in the Mac's `~/.config/claude/env` first — it
names the node, the tmux windows, and the remote-control alias, and the
default (short hostname) is usually something like `alexs-macbook-pro`.

This replaced `~/bin/claude-install.sh`, retired 2026-07-29 to
`~/bin/archives/claude-legacy/` along with `claude.sh.README.md` (whose
env-knobs table is now the one above). That script bootstrapped a machine by
rsyncing the toolchain over SSH, which required the target to reach this host,
have `rsync`, and hold a trusted SSH key. Copying one binary needs none of
that. Don't reintroduce a network-fetching install path — see Conventions.

## API

Two authenticated planes on one origin. Full detail in `docs/shabadoo.md`.

**Agent plane** — SSH-key auth, verified per request. Not behind the human
middleware; agents are not browsers.

| Endpoint | Body / effect |
|---|---|
| `POST /agent/hello` | issue a signing challenge (unauthenticated by necessity) |
| `POST /agent/login` | `{challenge, pubkey, signature, version}` → bearer token |
| `GET /agent/stream` | long-lived SSE stream of commands |
| `POST /agent/result` | one command's reply, correlated by `id` |
| `POST /agent/report` | this agent's window list (replaces its view wholesale) |
| `GET /agent/release/{version}/{platform}` | the binary for `shabadoo upgrade`. On this plane because the downloader is an agent holding an agent's token; the operator who *publishes* is a human on the human plane |

**Session messaging**, on the same plane and the same bearer token
(`hub/agentapi.go`). These act as a *session* where the human plane's
`/api/message/*` act as a person, and they are what the MCP bridge calls — one
for one, the NATS subjects they replace. A session cannot address another
tenant's inbox because it cannot name one.

| Endpoint | Replaces |
|---|---|
| `POST /agent/message/send` | `claude.inbox.<session>` — also nudges the recipient if its agent is connected |
| `POST /agent/message/broadcast` | `claude.broadcast.<topic>` |
| `POST /agent/message/drain` | the durable-consumer pull; returns and marks delivered in one transaction |
| `POST /agent/subscribe\|unsubscribe` | topic subscriptions |
| `GET /agent/peers` | the `CLAUDE_PRESENCE` KV: every session in the tenant, with undrained mail count and whether its agent is online |
| `POST /agent/status` | what this session says it is DOING, in its own words. On this plane because the author is a session, not a person |

**Unauthenticated** — deliberately outside every middleware.

| Endpoint | Why it has to be |
|---|---|
| `GET /healthz` | `{status, version, uptime, agents}`. A monitor that needs a credential is a monitor nobody configures, and a container healthcheck has nowhere to keep a token. Pings the **database**, not just the port — a process serving HTTP with its SQLite gone is the failure a port check calls healthy. Reports counts and a build stamp only: no node names, project paths or session names. `serve` has the same endpoint, checking tmux instead |
| `GET /pair`, `POST /api/devices/redeem` | enrolment; see the device-token row below |

**Human plane** — behind `IdentityProvider` middleware (Access / device token /
network-trust). Shapes are close to the flock's so the dashboard was a
re-point, not a rewrite. An unauthenticated **navigation** (a GET that accepts
HTML) is redirected to `/pair`; API calls get 403, since the dashboard's own
fetches must not be handed a redirect body to parse.

| Endpoint | Body / effect |
|---|---|
| `GET /api/sessions` | `{now, version, nodes:[{node, online, version, sessions[]}]}`; each session carries `input_state`. Both `version` fields are build stamps — see Versions |
| `GET /api/events` | the **same payload, pushed** — SSE, one frame per change plus a `: ping` keepalive. From the same builder as `/api/sessions`, because two renderings of one view drift and the drift is invisible until you need the one you were not looking at |
| `GET /api/capture` | `?node=&session=&window=&lines=&color=1` → pane text |
| `GET /api/claude/session` | `?node=&path=` → Claude session summary |
| `GET /api/audit` | `?limit=` → recent actions, newest first. Rendered by the dashboard's Audit panel (collapsed by default, polled only while open) |
| `GET /api/messages` | `?limit=&session=` → the durable inbox: the last 24h across the tenant, or one session's thread. Read-only — looking at mail never consumes it. Each message carries `recipients`/`acked`/`acked_at`, so a handoff that was stored and never picked up is distinguishable from one that landed. Rendered by the dashboard's Mail panel, same collapse-and-poll-while-open shape as Audit |
| `GET /api/input-state` | `?node=&session=&window=` → `composer` or `dialog` |
| `GET /api/folders` | `?node=` → startable folders: boot list + every folder with a transcript, each flagged `open` |
| `POST /api/select\|send\|command\|kill\|reopen\|open` | `{node, ...}` → proxied to that agent, **audited** |
| `POST /api/keys` | `{node, session, window, keys:[…]}` → raw keypresses, no C-u. Answers a dialog; **audited** |
| `POST /api/message/send\|broadcast` | durable inbox (replaces NATS) |
| `POST /api/devices/code\|revoke`, `GET /api/devices` | iOS enrolment |
| `PUT /api/devices/self/push` | register where to send this device's notifications (APNs token). Self-service, repeatable, and allowed under read scope — a read-only phone is precisely the one that needs telling. `""` deregisters; the token is never readable back |
| `POST /api/devices/renew` | extend the **calling** device's own token by a full term; no device id in the body, so one token cannot keep another alive. Extends rather than rotates — see Auth postures |
| `GET /api/releases` | what is published, plus each connected node's platform — "who needs upgrading" answered from one response rather than two |
| `POST /api/releases` | `?version=&platform=` + raw binary body → publish. **Audited** |
| `POST /api/upgrade` | `{node, version?}` → tell one node to replace itself. Audited on success *and* failure, since a node left mid-swap is what someone reading this log later is reconstructing |
| `POST /api/voice/session` | mint a short-lived signed URL for a voice agent. **Not** behind `requireWrite` — a read-only phone is exactly the one that benefits from asking out loud — and rate limited per device because it is the only endpoint that spends money |
| `POST /api/devices/redeem` | **the only endpoint outside the middleware** — an enrolling app has no credential yet. Rate limited per caller (10 bad codes / 15 min → 429) and audited either way; it is the one thing an unauthenticated caller can reach |

Every write carries `node`; the coordinator resolves the agent within the
caller's tenant. **Tenant comes from the verified identity, never from the
request.**

## Live updates (`/api/events`)

The dashboard prefers a pushed stream and **falls back to polling**. Agents
already report on a timer, so the coordinator knows the moment the view
changes; `SessionsChanged` fires on an agent report and on an agent login (a
node appearing is a change, and it is not a report — without it a reconnected
agent reads as offline until its next one).

The fallback is not belt-and-braces, it is the safety property. A proxy that
buffers `text/event-stream` turns SSE into a page that connects and never
updates — silent, and indistinguishable from every agent having gone away. It
is the same hazard already documented for the agent stream, and the reason
dm's Traefik was verified end-to-end before the move. So:

- polling starts first and covers the first paint;
- the poll is cancelled only once a frame has actually **arrived**, never on
  `onopen` — an open connection that never speaks is precisely the failure;
- a stream open but silent past 45 s (server keepalive is 25 s) is treated as
  buffered and polling resumes;
- `onerror` resumes polling too, since `EventSource` reconnects on its own but
  cannot tell a coordinator restart from a proxy that will never deliver.

Worst case is therefore the 3 s poll this replaced. `serve` answers **501** —
it reads its own tmux and has nothing to be pushed by — so the fallback mode
polls, which is what it wanted anyway.

Server side: frames coalesce over 400 ms (several agents reporting at once is
one change to a person looking at the page), the per-subscriber queue is depth
1 because these are full-state snapshots and a backed-up client wants the
newest rather than a queue of stale ones, and `notify` never blocks — it is
called inline from the report handler, so a blocking send would stall an
agent's report behind a slow browser.

## Voice (`--elevenlabs-key`)

The coordinator mints short-lived signed URLs for a conversational voice agent;
the phone opens the socket directly and the audio never passes through here.

Same arrangement as `--apprise-url`, for the same reason: the credential and the
routing config are one thing in one place. The key is account-wide and **billed
per minute**, so a copy inside a shipped app is a copy on every phone that
installs it. Requires both `--elevenlabs-key` and `--elevenlabs-agent`; half a
configuration leaves the endpoint disabled and says so at startup.

**Configure it through the environment, never the command line.** A compose
file's `--elevenlabs-key=${VAR}` expands at compose time and bakes the secret
into the process argv, where `/proc/<pid>/cmdline` is mode **444** — every user
on the host can read an account-wide, per-minute-billed key with `ps`. The
environment is mode 400, owner only, and `hub` reads `SHABADOO_ELEVENLABS_KEY`
as the flag's own default, so nothing about the configuration changes except
who can see it. The flag stays, because removing a documented interface is a
worse surprise than a warning — `hub` says once at startup when a non-empty key
arrived in argv. Found on the live deployment, whose compose comment already
said the values were "read from the environment below" while passing them as
flags: the intent was right and the expansion published the value anyway.

**Nothing here grants a permission.** The agent's tools run on the CLIENT,
against this same API, with the device's own token — so a read-scoped phone's
attempt to dictate into a pane is refused by `requireWrite` without the voice
layer knowing scopes exist. The voice agent cannot exceed the device holding
it, because it is not a separate identity. The `scope` in the response is for
greying out a button, not for enforcement.

**It cannot answer a dialog, and that is enforced by not shipping the tool.**
An agent told "never approve a prompt" can be argued into approving one; an
agent with no keypress tool cannot, whatever it decides. This is the fourth
place the same line is drawn — no answer button on a queue row, selecting from
the queue opens the transcript, the shipped question still says to read the
pane — and voice is the strongest possible version of answering without
reading, on panes running with permissions disabled.

The agent's own definition — prompt, tools, voice — lives in the provider's
dashboard rather than here, so what it believes it can call and what this API
serves can drift with nothing to catch it. `docs/voice-agent.md` checks in the
intended configuration so the drift is at least **visible**; it is
documentation of intent, not the source of truth, and a disagreement between
the two is a bug in one of them.

Rate limited to 30 mints per device per hour. That limit is about **spend**,
not about guessing, which is what the redeem throttle is for — this is the
first credential the coordinator hands out that arrives with a bill.

**Only successful mints count.** A call the provider refused spent nothing, so
charging for it is wrong in the way that bites hardest: when the key is broken,
the retries that diagnose it eat the budget for the retries that fix it. Found
by configuring voice for the first time and burning four slots on 401s. The
reservation is taken before the call and *refunded* on failure rather than
recorded after success — check-then-record would let concurrent callers all
pass the check before any of them records, which is a hole in the one guarantee
the limiter exists to give.

**A refused mint is explained in the hub log, never to the client.** The client
gets the status alone, because an authenticated upstream's error bodies name
accounts and keys. But that first 401 was a bare 502 on the phone while the
provider was saying exactly what was wrong — `missing the permission
convai_write` — and reading it took a hand-run `curl` on the coordinator host.
The provider's own `status`/`code`/`message` fields now go to the log, which is
on the machine that already holds the key; never the raw body, since an
unbounded echo of an authenticated response into a log is the same leak
somewhere quieter.

## The MCP bridge (`shabadoo mcp`)

Each Claude session launches `shabadoo mcp`, which speaks MCP over stdio and
reaches the coordinator through **this host's agent**, over a unix socket at
`~/.config/shabadoo/agent.sock`.

```bash
claude mcp add shabadoo -- shabadoo mcp
```

It replaces `mcp-natsbridge` and folds it into the binary rather than renaming
it, for three reasons in the order they bite: `setup` already ships this binary
everywhere so there is no second artefact to distribute; hub/node/bridge as
three separately-versioned programs speaking one protocol is the exact hazard
the build stamps exist for; and the session inherits its agent's authenticated
session rather than holding a credential of its own.

**The socket is the security boundary, and it is filesystem permissions.** 0600
in the operator's own directory, so "can open this socket" means "is already
this user" — who could read the agent key anyway. What it relays is an
**allowlist** of messaging endpoints, not a prefix: the agent's token can drive
every pane on the host, so a session able to name its own upstream path would
inherit the whole agent plane.

Two behaviours that are load-bearing rather than incidental:

- **A failed tool call is content with `isError`, not a JSON-RPC error.** A
  transport error says the server is broken; "the coordinator is unreachable" is
  an answer the model should reason about. Conflating them makes an offline
  coordinator look like a crash.
- **`session_list` degrades to this host's tmux** when the coordinator cannot be
  reached. Tooling that dies inside every running session whenever the hub blips
  would be a worse blast radius than the one it replaces — that was the explicit
  condition on folding this in at all.

`stdout` carries the protocol. Anything else written there surfaces to the
client as a parse error rather than as the problem; diagnostics go to stderr.

**Switched over 2026-07-31 — but only half of it, and the other half went
unnoticed for a week.** The MCP tool moved; the two *hooks* that surface mail
without anyone asking (`SessionStart`, `UserPromptSubmit`) were left pointing at
`mcp-natsbridge -mode=inbox-drain`, against a stream nothing writes to any more.
On wsl that drained an empty subject forever; on the mac the bridge cannot start
at all (see below). Both failed into `2>/dev/null`, so a week of silence looked
exactly like a week with no mail — the visible symptom was a human typing "check
inbox" by hand. **`shabadoo inbox` is the hook-shaped counterpart** of
`session_inbox_drain`: same socket, no credential, but a shell command rather
than an MCP client. Hooks call it as `shabadoo inbox 2>/dev/null || true`,
which is the form to keep — an unknown subcommand exits 2, and a
`UserPromptSubmit` hook exiting 2 blocks the prompt. It prints nothing on an
empty inbox and always exits 0, for the same reason.
`mcp-natsbridge` is now removed from both hosts: unconfigured everywhere, the
15-minute `-mode=nudge` cron on wsl retired (the coordinator nudges instantly),
and the binary moved to `~/bin/archives/claude-legacy/`.
`shabadoo` is the only session-messaging MCP server configured on wsl and mac.
A wsl session was canaried first — it reported its own id, listed all 9 sessions
across both hosts, and a send/drain round trip confirmed drained mail never
redelivers.

`notify_send` moved with it, so nothing was lost in the swap: the Apprise →
Telegram/Pushover relay now lives on the coordinator (`--apprise-url`), verified
delivering to both, and every send is audited with the node that asked.

**The NATS cluster keeps running on dm** — other things use it (the Telegram
inbound relay, homelife-mcp's live-activity console). What changed is that
shabadoo no longer depends on it.

> **`mcp-natsbridge` is already broken on the Mac**, which is what makes this
> more than tidying. It builds a JetStream consumer name from the hostname, and
> `Alexs-MacBook-Air.local` contains dots, which NATS rejects — so a new bridge
> process cannot start there at all. An already-running one keeps working, so
> the failure only appears on restart. `shabadoo mcp` has no equivalent: it
> reaches the coordinator through the local socket and never names a consumer.

## Publishing

This repo is published with **no history**. The 69 commits that preceded it
carried a vendored personal `~/.claude` — hostnames, addresses, infrastructure
skills — and scrubbing that from history across every commit that also mentions
one deployment by name is far more error-prone than starting clean. The private
history stays local; public history starts at the first public commit.

Two guards, and they cover different things:

| Guard | Scope |
|---|---|
| `make vendor-check` | the embedded payload — denied tokens in `config/`, and nothing under `config.local/` tracked |
| `./scripts_publish_check.sh` | **every tracked file** — private hostnames, addresses, people, client and product names |

`scripts_publish_check.sh` runs in CI. Three things it found that review had
not, which is the argument for having it:

- `config/skills/next-bus` was a personal skill (a specific city's bus route)
  sitting in the payload that installs onto *other people's* machines;
- `launch_test.go` pinned window-name hashes computed from real paths naming a
  private user account and a **client project**. The replacements' expected
  values were computed with `sha1sum`, an independent implementation, so the
  test still checks this code against something other than itself;
- `VENDOR_DENY` in the Makefile was *itself* a list of client names — a
  denylist committed to a public repo publishes exactly what it exists to
  withhold. It reads from a gitignored `.vendor-deny` now.

Tailnet addresses are denied **individually**, not as `100.64.0.0/10`. The
range is Tailscale's documented allocation, so the code names it in a comment
and the tests need examples from it; scanning for the range flags its own
documentation, and a scanner that cries wolf gets ignored.

## Conventions

- **A change to the human API is a change to `docs/mobile-client.md`.** That
  document is a contract with someone building a client from another machine,
  and a stale contract is worse than a missing one: they design around an
  absence that is no longer real. It went stale within hours of being written —
  still saying push tokens and SSE "do not exist" the afternoon both shipped —
  so it is **pinned by `docs_test.go`**, which fails when a human-plane
  endpoint is not mentioned there, and when the spec still claims something
  unbuilt that is built. Document a new endpoint, or list it under
  *Deliberately out of scope*; never leave a client author to find it by
  reading Go. Ship the spec change **in the same commit** as the code.

- **Stdlib only** — no third-party deps. Keep it that way unless there's a
  strong reason.
- **Self-contained binary.** Everything `setup` installs is embedded, so a
  single copied file bootstraps a machine with no network and no source tree.
  Don't add an install step that fetches from the network.
- **`core.fileMode=false` on a Windows-mounted worktree.** This checkout lives
  on `/c/...` (drvfs), which reports *every* file as executable, so git keeps
  committing markdown and YAML as `100755`. Fixing it with
  `git update-index --chmod=-x` is index-only and survives exactly one commit by
  anyone else — the bit returns the moment another session stages the file. Set
  the config instead; git then honours the index and ignores what the filesystem
  claims. It is local config, deliberately not committable, because it describes
  where this worktree lives rather than anything about the project — a clone on
  a real filesystem neither needs nor wants it.
- **Setup steps must stay idempotent and non-destructive** — re-runnable,
  reporting `unchanged`, backing up anything they replace. `shabadoo doctor`
  on a correctly-installed host must report zero changes; that property is the
  reason `doctor` is trustworthy.
- **Loopback by default** — the binary binds `127.0.0.1:8787` unless told
  otherwise. Prefer fronting it with Caddy (as on WSL) over binding a routable
  address; bind the tailnet only where there is no proxy, and never `0.0.0.0`.
- **Session detail widens the read surface, knowingly.** `/api/capture` is
  bounded by what is still in tmux scrollback; `/api/claude/session` is not. It
  reads the transcript store, which holds file contents, memory dirs, and
  anything ever pasted into a prompt — for every session ever run in that
  folder, indefinitely. Accepted on the same tailnet-only basis as the write
  endpoints; revisit if auth is ever added. Phase 2 (rendering the messages
  themselves) puts that content on the wire, not just its totals.
- **Reaching the dashboard is driving every pane.** Authentication is real now
  (`--device-tokens`), but a credential is all it takes: whoever holds one can
  read every project path/window name, **read the full buffer of any pane**, **read
  any Claude session's transcript**, **send keystrokes into any pane**, and
  **kill / reopen / spawn** claude windows — and those panes run
  `claude --dangerously-skip-permissions`, so a keystroke can do anything Claude
  can. Read-only enrolment (`pair --scope read`) is how a client gets to watch
  without that power, and it is the right default for anything that only needs
  to look.
- **The coordinator concentrates that blast radius, deliberately.** Reaching the
  hub means driving *every connected node*, not just one machine. Under the
  flock this was transitive through a peer list; with dial-out agents it is
  simply central, which is easier to reason about and no smaller. Authorizing an
  agent is the same decision as handing that machine's Claude panes to anyone
  who can reach this dashboard — which is why `authorized_agents` is a trust
  decision `setup` reports rather than generates. The chokepoint for tightening
  it is the `IdentityProvider` middleware in `hub/identity.go`, not the
  transport.
- **`friendly_name`** strips the trailing `-<8hex>` hash that `windowName`
  (`launch.go`) appends, so `homelab-wsl-4b602ded` renders as `homelab-wsl`.
  The raw name is preserved (shown on hover).
- **A dialog in the pane owns the keyboard.** When Claude Code has a modal up
  (permission prompt, `/status`, plan approval, the trust dialog), text sent to
  that pane is discarded and Enter is consumed by the dialog — the send
  "succeeds" and nothing is submitted. `tmux.InputState` classifies the pane
  from its visible tail, `guardDialog` turns a send in that state into an
  error, and `/api/keys` sends the keypress the dialog is waiting for. The
  guard **fails open**: an unrecognised screen is treated as a composer,
  because a false "dialog" would block real messages while a false "composer"
  only restores the old behaviour. It deliberately never presses Escape
  itself — Escape during a running turn interrupts Claude's work, and
  discarding someone's in-flight turn is the operator's call.
  **Select-list modals were invisible to it.** The markers were written from
  confirm-style prompts, and `/remote-control` on an already-connected session
  raises a three-item menu footed `Enter to select · Esc to continue` — nothing
  the list matched. So the pane read `composer` while a modal owned the
  keyboard, with `guardDialog` waving sends into the exact silent no-op it
  exists to prevent. Found from a screenshot; there is no other way this
  surfaces.
  A **visible composer row beats any marker above it**, because footer text is
  also just words: the session that widened this list was discussing the
  markers on screen while its own agent classified the pane. The input row
  (boxed ASCII `>`) is the discriminator rather than the box outline, since
  permission modals are boxed too and menus select with `❯`.
- **One key is pressed automatically, and only one.** Remote control drops on
  its own: the `--remote-control` flag is set at launch and stays set, but the
  CLI's link to claude.ai does not survive indefinitely, and a session whose
  link dropped **vanishes from the mobile app** — so the tap that restores it
  can only come from here. `/remote-control` on a still-connected session opens
  a menu (Disconnect / Show QR / Continue) that then owns the keyboard, making
  a one-tap chip a three-step chore. `dismissRemoteControl` closes it.
  It presses **Escape, never Enter**, and that is the safety argument rather
  than a detail: Enter acts on whichever line the cursor is on, and one of them
  is *Disconnect this session* — the precise opposite of the request. The
  cursor defaults to Continue, but a default is something that changes in
  another program's UI without telling us; Escape means "continue"
  unconditionally, per the modal's own footer. It fires only after the pane is
  confirmed to hold **that** modal (classified as a dialog *and* carrying both
  of its markers), which is also why Escape is safe here — a modal owning the
  keyboard means no turn is running. Dismissing a receipt this program just
  caused is not the same decision as answering a question nobody has read.
- **Sessions are addressed by DOMAIN, not by id.** `session_send to="homelab"`
  resolves to whichever session owns that project — exact match on session id,
  alias or project first, then substring, and an ambiguous name is refused
  rather than guessed. This is the routing primitive the whole premise rests
  on: an expert-per-project arrangement is useless if reaching an expert
  requires already knowing its hash-suffixed id. Matching deliberately excludes
  the **cwd** — every session on a Linux host lives under `/home/<user>`, so
  matching the path would make common words resolve to everything (the same
  lesson the CLI's pane resolver learned).
  **An unknown recipient now bounces**, with the list of what exists. Before
  this, `Send` inserted a delivery row for whatever string it was handed, so a
  project name — the first thing anyone would try — produced a message that was
  stored, reported as sent, and drained by nobody. A `claude-`-prefixed id that
  matches nothing is still passed through, because mail for an **offline**
  session is meant to wait for it and those sessions are absent from the table
  while their host is gone.
  **A bounce is audited** (`message.bounce`), because it used to exist only in
  the sender's own context: the recipient never learned anyone had tried to
  reach it, and nothing an operator could read said so either — so diagnosing
  one meant asking the sender what it remembered. Found the hard way, trying to
  investigate a real bounce with nothing to read.
- **Mail shows whether it was picked up.** `deliveries` has carried
  `acked_at` since the start and nothing read it, so the Mail panel rendered a
  message nobody ever drained identically to one that was acted on — which is
  precisely the state you are in when asking "did that reach homelab?". The
  read paths now aggregate it: `recipients` (one row for a direct message, one
  per subscriber for a broadcast), `acked`, and `acked_at`. Direct mail renders
  a green dot plus the drain time, or an amber dot plus **waiting**; a
  broadcast renders `n/m`. The dot is the `.led` node rows already use, so one
  shape means one thing across the page, and the word beside it carries the
  same state for anyone who cannot separate green from amber.
  **Every row states which it is, including the ones that landed.** Marking
  only the exceptions looked tidier and was worse: an unmarked line is then
  ambiguous between "picked up" and "this build does not report it", which is
  exactly what the first person to read the panel asked.
  Acknowledged means **drained** — the recipient session pulled it into
  context. Deliberately not called "read": nothing here can know whether anyone
  acted on it, and a word implying that would claim more than the data
  supports. The fields are filled by the read paths only, so a client cannot
  assert its own message was received. `Replay` and `Conversation` share one
  SELECT because two renderings of one fact drift, and the drift is invisible
  until you rely on the one you were not looking at.
- **A session says what it is doing; tmux cannot.** `sessions.status` is tmux's
  view (active / idle) and `Session.Note` is the session's own
  (`session_status_set` through the MCP bridge, rendered in the dashboard row
  and under the CLI listing). Only the session knows it is "waiting on the
  homelab peer" rather than merely idle, and under this project's premise that
  is the thing a peer most needs. It lives in its own **`session_status`
  table**, because `sessions` is deleted and re-inserted wholesale on every
  agent report — a column there would be erased every five seconds. It ages out
  after 30 minutes: a session that set a status and then died would otherwise
  claim to be mid-task forever, and a peer would act on it. Empty clears it,
  which is how a session says it finished rather than stopped.
- **A blocked session is visible everywhere, and reaches you when it isn't.**
  The agent classifies each window in its periodic report (`input_state`:
  `composer` / `dialog`), so the dashboard flags every session waiting on a
  prompt — a **Waiting on you** queue at the top of the page (longest wait
  first, rendered only when non-empty), a badge on the row, a count in the
  header, and `(n) shabadoo` in the tab title. All four require the page to be
  open, which is the problem: a session blocks precisely when the human is doing
  something else. So the coordinator also **notifies** through the same Apprise
  relay as `notify_send` — see Blocked-session notifications. The browser does
  not poll panes for any of this; `/api/input-state` remains for the two moments
  that cannot wait for the next report (selecting a pane, and the instant after
  answering one).
- **The queue says what it is asking.** `tmux.DialogPrompt` pulls the question
  out of the same capture that classified the pane as a dialog, and it appears
  on the queue row, under `shabadoo sessions`, and — most usefully — as the
  first line of the notification. Without it the loop this project sells had an
  unadvertised hop in the middle: you were told a session was blocked and had
  to open the pane to discover what you were agreeing to, which is the one
  interaction to be most careful about when the pane runs with permissions
  disabled.
  It is a heuristic over another program's UI and fails the same way
  `InputState` does — unrecognised returns empty and every surface simply shows
  nothing, which is the behaviour that existed before. A line qualifies only if
  it is **framed** by the modal's box and ends in a question mark, or opens
  with Claude Code's own phrasing. A trailing `?` alone is not enough:
  "Done. Anything else?" is something the assistant *says*, and showing it as a
  pending question would be worse than showing none.
- **Selecting from the queue opens the pane.** There is deliberately no
  answer-button on a queue row. Answering without reading the question is how
  "yes" gets sent to a prompt that was asking about deleting something, so a tap
  selects the pane and opens the transcript; the answer keys are one more tap
  away, with the dialog on screen.
- **An empty `authorized_agents` is fine; a missing one is not.** Empty is what
  every coordinator looks like before its first machine is added, and refusing
  to start blocked only that case — which is every new install. The documented
  first run (create the file, start the hub, add agents afterwards) was
  impossible until someone actually followed it. A missing file is still an
  error: that is almost always a wrong `--agents` path, and starting anyway
  hands the operator a coordinator that silently trusts nobody. Starting with
  none says so loudly in the log.
- **`authorized_agents` is re-read when it changes**, checked at login — adding
  a node is an edit, not a restart, and a restart would disconnect every agent
  already connected to admit one. A file that fails to parse keeps the previous
  key set (a half-written edit must not lock everyone out). Removing a key
  stops the *next* login; an agent already holding a token keeps its stream
  until it reconnects or the coordinator restarts.
- **Revoking a node takes two steps, and that is deliberate.** `shabadoo
  disconnect <node>` drops the live session — stream closed, token dead, now —
  but the agent dials back in within seconds, because the file remains the
  single source of truth for who may connect. Two places to look is how a host
  nobody meant to authorize stays authorized. So: remove the key from
  `authorized_agents`, **then** `disconnect`. Either alone is incomplete — the
  file edit is not immediate, and the disconnect is not permanent.
- **There is no remote stop, on purpose.** The coordinator can replace a node's
  binary but cannot stop its agent, because the services are supervised
  (`Restart=on-failure`, launchd `KeepAlive`) and would bring it straight back —
  and if they did not, the only way to restore a stopped node is SSH to that
  machine, which is the dependency dial-out agents exist to remove. Cutting
  access is `disconnect` plus the key removal above; stopping the process is a
  decision for whoever is at that host.
- **Revoking a human credential** is `shabadoo revoke <label|id-prefix>`,
  which is immediate and permanent: the row is deleted, so the client is signed
  out on its next request and cannot renew. Re-pairing is the only way back —
  which is also why a revoked token reports as `unknown` rather than `revoked`
  (see Auth postures).
- **Enrolled devices are durable.** Device tokens live in the `devices` table
  (hash as primary key — a table dump yields hashes, not credentials), loaded
  at startup and written through on enrol/revoke. They were once in-memory
  only, which made the 90-day token TTL fiction: every `make deploy` logged out
  every paired client. Pairing *codes* stay in memory deliberately — five
  minutes, single-use, and losing them on restart is correct. A `Redeem` whose
  write fails returns an error rather than handing out a token that would stop
  working at the next restart.
- **Folder discovery** (`/api/folders`) merges the boot list, every folder with
  a transcript, and the live windows. Paths are compared through
  `filepath.EvalSymlinks` — the boot list holds symlinks while tmux reports
  resolved paths, so a raw string match shows an open folder as closed and
  invites a duplicate window. Folders that no longer exist are dropped:
  transcripts outlive the directories they describe.
- **Entry history** lives in `localStorage` (`shabadoo.sendHistory`, 50 entries,
  deduped most-recent-first), recalled with ↑/↓ and exposed as a `datalist` so
  phones — which have no arrow keys — still get the dropdown.
- **Mobile-first control.** The dashboard is responsive (`@media (max-width:640px)`
  in `static/index.html`): larger touch targets, stacked control deck, and the
  `panes`/`cwd` columns (`.hide-sm`) collapse on phones. The quick-command chips
  exist mainly so a phone can fire `/remote-control` and friends in one tap
  without typing — this is the primary mobile workflow, keep it fast.

## Build & run

```bash
make build                        # stamps main.version from `git describe`
./shabadoo                         # standalone fallback, http://127.0.0.1:8787
./shabadoo hub --insecure-no-access --bootstrap   # coordinator, local dev
./shabadoo node --coord http://127.0.0.1:8788        # agent
./shabadoo version                # which build is this?
make vet test                     # before committing
```

A plain `go build -o shabadoo .` still works but leaves the version unstamped
(`dev (unstamped)`), which is why `make build` is the documented path.

**A bare `shabadoo` prints usage and exits 0.** It used to start the fallback
server, so that `ExecStart=... --addr ...` kept working when subcommands were
introduced — but every unit `setup` writes now names `hub`, `node` or `boot`
explicitly, so that compatibility was protecting nothing. Meanwhile a
downloaded binary run once to see what it is silently bound port 8787 and
served a dashboard, which is a startling first impression and the opposite of
what typing a program's name suggests.

Flags with no subcommand (`shabadoo --addr X`) are an **error with a hint**,
not an assumption: guessing `serve` is how someone meaning to reach their
coordinator quietly starts a second, local one instead. `--version` and
`--help` are still answered first. `bare_invocation_test` runs the real binary
and asserts none of these paths ever prints `listening on`.

> **Tests run against the developer's real machine.** There is no sandbox
> between this code and the host, so any fixture that *resolves* is a live
> target. This has now happened twice:
>
> - an early draft of `serve_test.go` used session `claude` window 1 and path
>   `/tmp`, and running it killed a real Claude window and spawned a stray
>   session;
> - an early draft of `TestUninstallKeepsStateWithoutPurge` called
>   `(*uninstall).run()`, which reads `hubUnit`/`nodeUnit` — hardcoded paths in
>   `/etc/systemd/system` — and uninstalled the developer's own
>   `shabadoo-node.service`.
>
> The rule that covers both: **a test may not call a function that names a real
> system location.** tmux fixtures must name a session tmux cannot have and a
> path that does not exist; anything that touches units, plists or `/etc` must
> be split so the decision is testable without the teardown (`stepState` is that
> split for uninstall). The assertions are about routing, decoding and policy —
> all of which happen before dispatch.

## Retired

**The names `phalanx`, `polemarch` and `hoplite` — retired 2026-07-30.** The
project is `shabadoo`; the roles are `hub` (coordinator) and `node` (per-host
agent). `node` was chosen because the wire protocol already called a host a
node (`{node, session, window}`, `--node`), so the package name and the API
vocabulary now agree instead of needing a translation.

Renaming moved the units and the state directory, and neither migrates itself:

```bash
sudo systemctl disable --now polemarch hoplite
sudo rm /etc/systemd/system/{polemarch,hoplite}.service
mv ~/.config/phalanx/polemarch.db ~/.config/shabadoo/hub.db
mv ~/.config/phalanx/{agent_key,agent_key.pub,authorized_agents,coord,cli_token} ~/.config/shabadoo/
shabadoo setup --service --device-tokens --addr tailscale:8787
```

The database matters: enrolled device tokens live in it, so starting the new
coordinator against an empty one logs out every paired client. `setup --service`
warns when it finds either leftover (`checkLegacyInstall` in `service.go`) but
never moves them — stopping services and relocating a database while the
operator watches is their call, not the installer's.

The dashboard's send history is the one thing that does migrate itself:
`static/index.html` reads the old `phalanx.sendHistory` localStorage key once
when the new one is absent, because a browser's storage cannot be moved by hand.

**`tmuxbridge.service` — retired 2026-07-29.** Replaced by `shabadoo-hub.service` +
`shabadoo-node.service` on the same address and URL, so nothing downstream changed.
Removed with it: the binary (`~/bin/tmuxbridge`), the unit, the peer list, and
`peers.go` — the flock's fan-out, node routing and recursion guard all became
dead code the moment agents started dialling out.

**Caddy and ttyd — retired 2026-07-29** (earlier the same day). Both packages
are still installed and both units disabled; `/etc/caddy/` keeps the previous
config as `Caddyfile.retired.<epoch>`. `claude.laptop.example.com` still resolves
but points at nothing.

What the Caddy removal traded away: HTTPS (browsers mark the page "Not secure"),
`encode zstd gzip` (~5x on the capture endpoint), and an access log the binary
does not produce. It did **not** trade away confidentiality — Tailscale is
WireGuard-encrypted. The `--caddy` setup step still exists and still works; it
is simply unused.


## Future phases (not yet built)

Select, send-text, `/clear`, `/remote-control`, kill, reopen and open-new-window
are all done (see API).

**Session detail is phase 1 of 3.** The summary (stats header) ships; the reader
underneath it already returns a byte cursor, which is what the rest builds on:

- **Phase 2 — rendered conversation.** `GET /api/claude/events` returning
  cursor-paginated user/assistant turns with tool calls collapsed, below the
  stat header. Two constraints already discovered: `proxyGet` buffers a peer
  response through `io.ReadAll` with an **8 MB cap**, so responses must paginate
  hard and truncate individual tool results on the wire; and the poll must
  *append* DOM nodes using the cursor rather than rewriting `innerHTML`, or it
  destroys scroll position every 2 s.
- **Phase 3 — session browser.** Every past session per project, not just the
  live one, with resume. `transcripts()` already lists them newest-first, so
  this is mostly UI.

Also still open: the cross-host NATS `CLAUDE_PRESENCE` view that the retired
`claude-sessions` script showed (peers on other hosts) — `shabadoo win list` is
local-only, and the coordinator's own `/agent/peers` covers the same ground for
connected nodes and is what `session_list` reads. And, before the write surface grows
further, real auth: everything currently leans on the network-trust posture
above.

The reviewed gap list (health endpoint, redeem throttling, audit retention,
downgrade guard, uninstall) and the feature backlog (blocked-session push,
waiting queue, SSE for the human plane, CLI read commands, transcript search,
capability negotiation, folding the MCP bridge in as `shabadoo mcp`) live in
the task list. Closed since: `hub.db` backup (the move to dm put it under
borg) and device-token renewal.
