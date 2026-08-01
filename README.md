# shabadoo

A coordinator (**hub**), a per-host agent (**node**), and the installer
for the launcher toolchain they drive. One binary, three roles.

Agents dial the coordinator and hold a command stream open; the coordinator
merges every host's Claude sessions into one dashboard, routes writes to the
agent that owns the pane, and records every action in an audit log. Because the
agents dial out, only the coordinator needs to be reachable.

The dashboard lists every tmux session and window with live status (active pane,
running command, cwd, idle time, auto-refreshing), and lets you **click a pane to
select it**, **send text or slash-commands to it** (one-tap chips like
`/remote-control`, plus a free-form box), **view its live transcript** (colored,
`tail -f` style), **see what the Claude session in it is doing** (model, turns,
tokens, tools — read from Claude's own record, not the screen), and **reopen /
kill / open** windows. It is mobile-responsive; the chips make `/remote-control`
a one-tap send from a phone.

It is also **the installer and source of truth for that toolchain**: the
launcher is built in (`attach` / `win` / `boot`, once three shell scripts), and
the portable `~/.claude` config (skills, settings) is compiled into the binary,
so one copied file can set up a new machine offline.

> **Security:** the deployment runs `--device-tokens` — every client (browser,
> CLI, phone) presents a credential it was enrolled with, and there is no
> anonymous access. That matters because a credential holder can read every
> project path, **read any session's transcript** (including file contents and
> anything ever pasted into a prompt) and **type keystrokes into any pane**,
> which run `claude --dangerously-skip-permissions`. Enrol anything that only
> needs to watch with `pair --scope read`, which cannot write at all. The
> coordinator is also tailnet-only. See [CLAUDE.md](CLAUDE.md) and
> [docs/shabadoo.md](docs/shabadoo.md).

## Install

Prebuilt binaries and a container image are published for every tagged release.
Neither needs a Go toolchain or a clone.

### Download a binary

One static file, no runtime, no install step — it is the same artefact
`make dist` produces, built by CI from the tag.

```bash
OS=$(uname -s | tr '[:upper:]' '[:lower:]')          # linux | darwin
ARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
BASE=https://github.com/alexj212/shabadoo/releases/latest/download

curl -fsSL -o shabadoo "$BASE/shabadoo-$OS-$ARCH"
curl -fsSL -o SHA256SUMS "$BASE/SHA256SUMS"

# Verify before running it. The whole point of a published checksum is that
# somebody checks it, and this is a binary you are about to give a terminal.
grep " shabadoo-$OS-$ARCH\$" SHA256SUMS | sed "s/shabadoo-$OS-$ARCH/shabadoo/" | sha256sum -c -

chmod +x shabadoo && ./shabadoo version
```

`sha256sum` is `shasum -a 256` on macOS. Platforms: `linux-amd64`,
`linux-arm64`, `darwin-amd64`, `darwin-arm64`.

Deliberately **not** a `curl … | sh` installer. This binary installs a
toolchain, and a project whose own rule is "never fetch from the network during
install" should not open by asking you to pipe an unread script into a shell.
Two commands and a checksum is the honest version of the same convenience.

> The macOS builds are **unsigned**. `curl` does not set the quarantine
> attribute so the commands above just work, but a binary downloaded through a
> **browser** is quarantined and Gatekeeper will refuse it — clear it with
> `xattr -d com.apple.quarantine shabadoo`.

Then install the toolchain, or just run it — see [Run locally](#run-locally).

```bash
./shabadoo setup      # ~/bin, PATH, deps report, portable ~/.claude
```

### Run the coordinator as a container

Multi-arch (`linux/amd64`, `linux/arm64`), ~27 MB, static binary on Alpine,
runs as UID 1000.

```bash
docker pull ghcr.io/alexj212/shabadoo:0.1.1
docker run --rm ghcr.io/alexj212/shabadoo:0.1.1 version
```

A coordinator you can actually reach is a few more lines — a data directory, an
auth posture, and one pairing code. [`examples/docker-compose.yml`](examples/docker-compose.yml)
is that, commented, and it is about a minute end to end.

```bash
curl -fsSLO https://raw.githubusercontent.com/alexj212/shabadoo/main/examples/docker-compose.yml
mkdir -p data && touch data/authorized_agents
docker compose up -d
docker compose logs | grep 'pairing code'
```

**Pin a version tag rather than `latest`.** The image is what drives every pane
on every connected machine, so "whatever was pushed most recently" is not a
property you want a restart to pick up on its own.

The image contains the **hub only**. A node is not containerised on purpose: it
drives the host's tmux, so a node in a container would be a node with nothing to
manage.

### Build from source

```bash
git clone https://github.com/alexj212/shabadoo && cd shabadoo
make build      # stamps the version from `git describe`
make dist       # all four platforms, into dist/
```

Stdlib-only, so there is nothing to fetch beyond the Go toolchain itself.

## Run locally

```bash
make build                                              # stamps the version from git describe
./shabadoo                                               # standalone fallback, http://127.0.0.1:8787
./shabadoo hub --insecure-no-access --bootstrap    # coordinator, local dev
./shabadoo node --coord http://127.0.0.1:8788         # agent
./shabadoo version                                      # which build is this?
```

`shabadoo` with no subcommand serves this host alone, on loopback — the same
dashboard the coordinator serves, driving only this machine's panes.

Builds stamp their version from `git describe`; every node reports it at login
and the dashboard flags a node whose build differs from the coordinator's, since
`setup --service` installs whichever binary invoked it.

## Install the toolchain

```bash
shabadoo doctor                    # report what would change; writes nothing
shabadoo setup                     # apply
```

`setup` installs this binary into `~/bin`, ensures that dir is on `PATH`,
reports missing dependencies (`tmux`, `claude`), scaffolds
`~/.config/claude/env`, and syncs the portable `~/.claude` config. Every step is
idempotent and backs up anything it replaces to `<path>.bak.<epoch>`; a second
run reports `unchanged` throughout.

The launcher used to be three generated shell scripts in `~/bin`
(`claude.sh`, `claude-sessions`, `claude-startup.sh`); they are subcommands now
— `shabadoo attach`, `shabadoo win`, `shabadoo boot` — so there is one binary to
install and one implementation of window naming to keep correct.

`--service` runs a node as services (systemd on Linux, launchd agents on
macOS); `--boot` installs the login launcher that opens a window per configured
folder; `--caddy` is Linux-only and currently unused.

`--service` **requires an auth posture** and writes nothing without one:

```bash
shabadoo setup --service --addr tailscale:8787 --device-tokens
shabadoo setup --service --addr tailscale:8787 --access-team T --access-aud A
```

It installs both halves of a node — `shabadoo-hub.service` and `shabadoo-node.service`.
The agent key and the coordinator's authorized-agents list are **reported,
never generated**: one is a credential, the other a trust decision.

### Operator CLI

```bash
shabadoo pair --self                      # enrol this CLI (token in ~/.config/shabadoo)
shabadoo sessions                         # every node; `!` marks one waiting on a prompt
shabadoo open techtalks                   # start a session by folder name
shabadoo keys --window 7 Enter            # answer a prompt
```

`shabadoo pair` (without `--self`) prints a pairing URL to open on a phone. It
authenticates with a device token — the same credential the iOS app will use.

### Bootstrap another machine

```bash
make dist                                          # linux + darwin, amd64 + arm64
scp dist/shabadoo-darwin-arm64 mac:bin/shabadoo
ssh mac 'chmod +x bin/shabadoo && bin/shabadoo setup'
```

No SSH-back, rsync, or reachable source host needed — this replaces the retired
`claude-install.sh`. To make that machine another node on the existing
dashboard, install the **agent only** and point it at the coordinator:

```bash
ssh-keygen -t ed25519 -N "" -C mac -f ~/.config/shabadoo/agent_key
shabadoo setup --service --boot --coord https://coordinator.example
```

then append its `agent_key.pub` (with the node name as the comment) to
`/srv/shabadoo/data/authorized_agents` **on the coordinator**. No restart:
the file is re-read when it changes, because restarting to admit one agent
would disconnect every other.

Useful flags: `--dry-run`, `--bin-dir`, `--skip=deps,config`, `--force`.
Full reference in [CLAUDE.md](CLAUDE.md).

`config/` is the embedded payload — the **portable** half of `~/.claude`,
snapshotted at build time and safe to publish: behavioural guidance, a generic
`settings.json`, and skills that name nobody's infrastructure. A fresh clone
builds and installs that tree alone.

The operator's own `~/.claude` — hostnames, addresses, infrastructure skills —
lives in `config.local/`, which git never sees. `make vendor` fills it from this
machine's live config; `make vendor-diff` shows drift first. Vendoring is
deliberate, never automatic, and `make vendor-check` fails the build if anything
under the personal overlay is tracked. See
[CLAUDE.md](CLAUDE.md) for why the split is where it is.

## Deployed

The coordinator is a container on **dm** behind Traefik; agents run on the
machines that have tmux and dial out to it.

- **URL:** https://coordinator.example/ (tailnet only, TLS via the
  `*.apps.example.com` wildcard — no DNS work, the wildcard already points at dm)
- **Hub:** `shabadoo-hub` container, stack at `/srv/shabadoo/` on dm,
  `--device-tokens`, SQLite bind-mounted at `/srv/shabadoo/data/hub.db`
  (so dm's nightly borg run covers it — a named volume would not be)
- **Agents:** `shabadoo-node.service` (wsl) and
  `dev.shabadoo.node.plist` (mac), each authenticating with
  `~/.config/shabadoo/agent_key`
- **Image:** `ghcr.io/alexj212/shabadoo`, built and pushed by CI on every `v*`
  tag; tag pinned in `.env`. Compose source of truth is
  `deploy/docker-compose.yml` here

Since the image is published, upgrading is a pull rather than a build:

```bash
ssh user@coordinator "cd /srv/shabadoo && sed -i 's/^SHABADOO_IMAGE_TAG=.*/SHABADOO_IMAGE_TAG=0.1.1/' .env && docker compose pull && docker compose up -d"

make install                   # rebuild the local binary + ~/bin (agents, CLI)
ssh user@coordinator 'docker logs shabadoo-hub -f'
```

The build-and-ship path still works and is what you want for an **untagged**
build — a fix you have not cut a release for, or a coordinator with no route to
ghcr.io:

```bash
V=$(git describe --tags --always --dirty)
docker build --load --build-arg VERSION=$V -t shabadoo:$V .
docker save shabadoo:$V | gzip -1 | ssh user@coordinator 'gunzip | docker load'
ssh user@coordinator "cd /srv/shabadoo && sed -i 's/^SHABADOO_IMAGE_TAG=.*/SHABADOO_IMAGE_TAG=$V/' .env && docker compose up -d"
```

**Moved off the WSL workstation 2026-07-30**, along with `make deploy` and
`shabadoo-hub.service`, which no longer exist. The hub is a single point of
failure for every node's dashboard and there it inherited a workstation's
uptime. The auth posture changed with the move: `--trust-network` (no
authentication at all) would have been materially worse on an always-on host
behind a wildcard cert than on a port that is asleep half the day. That flag
was removed entirely on 2026-07-31, once enrolment became a closed loop.

**Retired 2026-07-29:** `tmuxbridge.service` (replaced by these two on the same
URL, taking the peer-to-peer "flock" with it), `ttyd-claude`, and **Caddy**,
which existed only to terminate TLS. The trade: no HTTPS (browsers say "Not
secure"), no gzip, no access log — but no loss of confidentiality, since
Tailscale encrypts the transport.

## Layout

| File                 | Role                                                            |
|----------------------|-----------------------------------------------------------------|
| `main.go`            | Subcommand dispatch (serve / node / hub / pair / sessions / folders / open / send / keys / attach / win / boot / setup / doctor / version). |
| `cmd.go`             | `node` and `hub` wiring: flags, units' entry points.    |
| `cli.go`             | Operator CLI over the human API, authenticating with a device token. |
| `ops.go`             | The agent's command handlers — the seam to tmux/claudelog.       |
| `hub/`               | Coordinator: agent auth (SSHSIG), human auth, SQLite store, connection hub, human API. |
| `node/`              | Per-host agent: dial-out, SSE command stream, reconnect.         |
| `serve.go`           | Standalone fallback server (`shabadoo serve`) — this host only. Delegates to `ops.go`, so it cannot drift from the agent. |
| `setup.go`           | Installer steps + the backup-and-replace file primitive.         |
| `service.go`         | systemd/launchd units + boot launcher, and the Caddy vhost.      |
| `assets.go`          | The two `go:embed` trees (`static/`, `config/`).                 |
| `tmux/tmux.go`       | Shells out to `tmux` (list / select / send-keys / kill / capture).|
| `claudelog/`         | Reads Claude's own session transcripts (`~/.claude/projects`).    |
| `launch.go`          | Launcher core: env file, window naming, launch, window resolution. Every path that starts a window goes through it. |
| `win.go`             | Local commands: `attach`, `win list/open/close/reopen/clear`, `boot`. |
| `static/index.html`  | Embedded single-page dashboard (no build step, no deps).         |
| `static/pair.html`   | Device enrolment page, served outside the auth middleware.       |
| `config/`            | Embedded install payload: the portable half of `~/.claude`.      |

## API

Two authenticated planes on one origin. Full detail in
[docs/shabadoo.md](docs/shabadoo.md); the table in [CLAUDE.md](CLAUDE.md) lists
every endpoint.

**Agent plane** — SSH-key auth, verified per request: `/agent/hello`,
`/agent/login`, `/agent/stream` (long-lived SSE), `/agent/result`,
`/agent/report`.

Auth postures, exactly one required: `--device-tokens` (enrolled clients only,
pair the first with `--bootstrap`), `--access-team`/`--access-aud` (Cloudflare
Access), `--insecure-no-access` (loopback, dev).

**Human plane** — behind the identity middleware: `/api/sessions`,
`/api/capture`, `/api/claude/session`, `/api/audit`, `/api/messages`,
`/api/input-state`, the writes (`select`, `send`, `command`,
`keys`, `kill`, `reopen`, `open` — all audited), durable messaging, and device
enrolment.

When Claude Code has a **dialog** open in a pane (permission prompt, `/status`,
plan approval), that modal owns the keyboard: sent text is discarded and Enter
is consumed by the dialog. `/api/send` refuses in that state rather than
reporting a success that never happened, and `/api/keys` sends the keypress the
prompt is waiting for — which is also what makes a permission prompt
answerable from a phone.

Every write carries `node`; the coordinator resolves the agent within the
caller's tenant, which comes from the verified identity and never from the
request. Writes require `Content-Type: application/json` — a baseline CSRF
guard.

**"Session" means two things** in this app, deliberately kept apart:
`/api/sessions` is *tmux* sessions, `/api/claude/session` is a *Claude* session.
The viewer's two tabs are the same split — **Pane** scrapes the terminal,
**Session** reads Claude's own transcript.

## Not yet done (future phases)

**Session detail is phase 1 of 3** — the stats header ships. Phase 2 renders the
conversation itself (`/api/claude/events`, cursor-paginated); phase 3 browses
past sessions per project, with resume. See CLAUDE.md for the constraints each
one inherits.

Also open: the cross-host NATS `CLAUDE_PRESENCE` view that `claude-sessions
list` shows, and — before the write surface grows further — moving off
onto one of the two real auth planes.

## License

[MIT](LICENSE).
