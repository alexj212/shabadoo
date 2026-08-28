# Building a shabadoo mobile client

Everything a client needs to pair by camera, authenticate, and drive Claude Code
sessions. Written for an agent building the app; the coordinator side is
maintained in `/c/projects/shabadoo` and this document is its contract.

**Read the API tables in `CLAUDE.md` alongside this.** Where the two disagree,
`CLAUDE.md` is right and this file is stale — say so rather than working around
it.

> ### Three things changed on 2026-07-30. Ignore any earlier instructions.
>
> 1. **The coordinator moved.** It is `https://coordinator.example` (dm). The
>    old `http://tmux.laptop.example.com:8787` was a systemd unit on a workstation
>    and is **gone** — it refuses connections, it is not slow or flaky. Anything
>    that still names it is stale.
> 2. **TLS is real** — a valid Let's Encrypt certificate, no `-k`, no pinning
>    needed. This matters more on iOS than it sounds: App Transport Security
>    blocks plaintext HTTP by default, so the old endpoint would have required a
>    documented `Info.plist` exemption and this one does not. **The mobile path
>    will not regress to plain HTTP without telling you first.**
> 3. **Authentication is ON.** The deployment runs `--device-tokens`; an
>    unauthenticated `GET /api/sessions` returns `403 forbidden: no device
>    token`. It previously ran `--trust-network`, i.e. no auth at all. This
>    inverts the obvious plan: **pairing is the first thing that must work, not
>    the last.** You cannot develop against an open hub and add tokens later.

---

## 1. What the app is for

Not a terminal. The dashboard already does terminals badly enough on a phone.

The app answers one question well — **"is anything waiting on me?"** — and lets
you act on the answer in a few taps:

1. Which sessions are running, across every machine.
2. Which are **blocked on a prompt** and cannot proceed without a human.
3. Answer that prompt (a keypress), or send a line of text.
4. Read a pane when you need context.

If a feature does not serve that loop, it belongs in the browser dashboard.

---

## 2. Pairing (the camera flow)

### What the QR contains

A single line of UTF-8 — the pairing URL, with the code in the **fragment**:

```
https://coordinator.example/pair#code=A1B2C3D4&label=Alex%27s+iPhone
```

- **Base URL** = the coordinator. Take it from the scan; do not hardcode it. It
  identifies which coordinator this token belongs to, and users will have
  exactly one until they don't.
- **`code`** = 8 uppercase hex characters, single use, **valid 5 minutes**.
- **`label`** = the name the OPERATOR gave this device when minting the code.
  Show it on the confirm screen — *"Pairing as: Alex's iPhone"* — so the person
  scanning knows what they are enrolling as before they commit.
- Both are in the fragment (`#`) deliberately: a fragment is never sent to the
  server, so the code stays out of access logs and `Referer` headers. Parse them
  client-side; never append them to a request URL.

> **`label` is display-only, and the server does not trust it.** The name is
> fixed when the code is minted and the coordinator uses the one it recorded, so
> a tampered fragment renames nothing. Do not send it back as your own label
> expecting it to stick — and do not offer the user a "name this device" field
> at pairing time. **The operator names the device**, because they are the one
> who later has to recognise it in the list they revoke from.
>
> `label` may be absent on a code minted by an older client or by
> `pair --self`. Fall back to showing just the coordinator host.

> ### Parse the RAW fragment. Do not use a pre-decoded accessor.
>
> This fails silently and passes every test you write by hand.
>
> Many URL libraries hand you an already-percent-decoded fragment — Swift's
> `URLComponents.fragment` does, and so do plenty of others. Split *that* on `&`
> and an operator label containing an encoded separator tears itself apart:
>
> ```
> label=Dev%26Ops   → decoded fragment "label=Dev&Ops"   → label="Dev", junk field
> label=A%3DB       → decoded fragment "label=A=B"       → label="A", junk field
> ```
>
> It looks correct in testing because a label like `Alex%27s%20iPhone` survives
> double-decoding by luck. It breaks on the first label with an `&` in it, which
> is exactly the one nobody tries until production.
>
> **Take the raw/percent-encoded fragment, split on `&` and `=`, then decode each
> value once.** Swift: `URLComponents.percentEncodedFragment`. JavaScript:
> `location.hash` is already raw, so `URLSearchParams` is safe. Python:
> `urlparse().fragment` is raw.
>
> Spaces are sent as `%20`, never `+`, so you do **not** need query-string
> plus-decoding — but handle `+` as a literal plus if you see one, because that
> is what it will be. *(Found by the iOS client author, who hit it.)*

The same QR works two ways, which is why it is a plain `https` URL rather than a
custom scheme:

| Scanned with | Result |
|---|---|
| The app's own scanner | app parses `coord` + `code`, redeems, stores the token |
| The system camera | opens the page in a browser, which pairs **that browser** |

If you register a Universal Link for `coordinator.example/pair`, the system
camera can hand off to the app instead. Treat that as an enhancement — the
in-app scanner is the path that must work.

### Where the QR comes from

The operator produces it in one of two ways:

```bash
shabadoo pair --qr          # prints a QR in the terminal
```

or the `/pair` page in a browser, which renders one for the code it just minted.
Either way the payload is the URL above.

### Redeeming

```http
POST {coord}/api/devices/redeem
Content-Type: application/json

{"code": "A1B2C3D4", "label": "Alex's iPhone"}
```

`label` here is only a **suggestion**, used if the operator minted the code
without a name. If they named it — which the CLI now requires for any code
minted for another device — theirs wins and yours is ignored. Send the label
from the QR if you have it; it will simply match.

**200:**

```json
{
  "token": "…64 hex chars…",
  "device_id": "…",
  "tenant": "default",
  "expires": 1793404800,
  "scope": "read",
  "label": "Alex's iPhone"
}
```

`scope` and `label` describe what you actually got: `scope` is `"read"` or
absent/empty for full access, and `label` is the name the **operator** gave this
device, which may differ from whatever you suggested. Read your permissions from
here rather than discovering them from a 403 later.

**`token` is returned exactly once.** There is no endpoint that will tell you it
again. Write it to the Keychain before you do anything else with the response;
if the write fails, discard it and make the user re-scan, because a token you
cannot store is worse than no token — it counts against the device list while
being unusable.

**401** — `invalid or expired code`. Uniform on purpose: unknown and expired are
not distinguished, so do not try to tell the user which it was. Offer a re-scan.

**429** — too many failed attempts from this address; honour `Retry-After`
(seconds). Ten bad codes in 15 minutes triggers it. If you hit this in normal
use, something is wrong with your scanner, not with the user.

### Read-only devices

A device can be enrolled so that it **cannot write at all**:

```bash
shabadoo pair --qr --scope read
```

The scope is fixed when the code is minted, by the enrolling human — a client
never requests its own permissions. A read-scoped token may do every `GET`, and
may `POST` only to `/api/devices/renew` and `PUT` only to
`/api/devices/self/push` — staying enrolled and being notified are not
"writes" in any sense a read-only device should be denied. Anything else
returns:

```
HTTP 403
forbidden: this credential is read-only
```

For a read-only v1 this is the scope to ask for. It makes developing against the
**live** coordinator risk-free: a bug in the client cannot type into a real
Claude session mid-task. The redeem response includes `scope`, so the app can
show what it is holding and grey out actions it cannot perform rather than
discovering the 403 at the worst moment.

Escalation is not possible: a read-only credential cannot mint a full-access
code, and changing scope means revoking and re-pairing — a visible act in the
device list.

### Storing the token

- iOS Keychain, `kSecAttrAccessibleAfterFirstUnlock` — background refresh needs
  it before the user unlocks.
- Store the **coordinator URL alongside it**. A token is meaningless without
  knowing which coordinator issued it.
- On sign-out, delete both and call `POST /api/devices/revoke` if you can.

---

## 3. Authenticating

> **A coordinator may not need you to pair at all.** If it runs with
> `--tailscale-allow` and the phone is on that tailnet as an allowed login, the
> API answers without any credential — Tailscale has already authenticated the
> peer. Try a plain `GET /api/sessions` first; a **200 means you are already
> authenticated** and the whole pairing flow can be skipped. Fall back to
> pairing on 401. Nothing below changes; this is a shortcut past it.


Every request:

```
Authorization: Bearer <token>
```

> **Error bodies are `text/plain`, not JSON.** Every success path is JSON, so
> the natural client decodes unconditionally — and on an error gets a parse
> failure that masks the real status. Check the status code first and only then
> decode. An error body is a human-readable sentence:
>
> ```
> HTTP 403
> forbidden: unknown, revoked, or expired device token
> ```

**401 and 403 mean different things. Do not treat them alike.**

| Status | Meaning | What the client does |
|---|---|---|
| **401** | authentication failed — the credential is missing, unknown, revoked or expired | clear the Keychain, send the user to the scanner |
| **403** | authorization failed — the credential is **valid** but not permitted (a read-only device attempting a write) | keep the token, disable the action |

Getting this backwards discards a working credential to fix a permission. They
used to be the same status, which is why this table exists.

A 401 carries the reason in `WWW-Authenticate`, RFC 6750 style, so branch on the
code rather than matching prose:

```
WWW-Authenticate: Bearer error="invalid_token",
                  error_description="device token has expired", reason="expired"
```

`reason` is one of `expired`, `unknown`, `missing`, `assertion`. **`expired` is
worth its own message** — "your access ran out, pair again" reads very
differently to a user than "unknown credential", and only one of them suggests
they did something wrong. A revoked token reports as `unknown`: revocation
deletes the record, and keeping hashes of dead credentials purely to sharpen a
message is a worse trade. Treat it as "signed out": clear the Keychain entry and send
the user back to the scanner. Do not retry with the same token.

### Staying enrolled — do not skip this

Tokens last **90 days**, and **an expired token cannot renew itself.** Recovery
from expiry requires the operator to restart the coordinator with `--bootstrap`
and produce a new code, which is not something a user can do from a phone.

The coordinator tells you how long you have, on **every** authenticated
response:

```
X-Shabadoo-Token-Expires: 1793233992     (unix seconds)
```

Read it off any response and renew when it drops below **30 days**. That is
what the web dashboard does, and it leaves ~60 days of chances to be opened
once — once being a full fresh term. Renew only after a *successful* call: a
renewal aimed at a coordinator you just failed to reach is a write in the dark.

Before this header existed a client had no way to know: the token is opaque and
`/api/devices` lists everyone's. So renewal was implemented, correct, and called
by nothing, while every enrolled device counted down to a lockout whose only
recovery is a terminal.

```http
POST {coord}/api/devices/renew     → {"expires": 1801180800, "label": "…"}
```

The token does **not** change — this extends its expiry. Nothing to re-store.
There is no device id in the request: a token can only renew itself.

### Registering for push

```http
PUT {coord}/api/devices/self/push
{"push_token": "<APNs device token, hex>", "platform": "ios",
 "environment": "development" | "production"}
   → {"push": true, "label": "alex-iphone", "environment": "development"}
```

**`environment` is not optional in practice.** Apple runs two APNs gateways and
a token from one is meaningless to the other — a sender pointed at production
silently never delivers to a device that registered from an Xcode build, with no
error anywhere. TestFlight and App Store builds are **production**; everything
built from Xcode is **development**. Send whichever your build actually is
(`aps-environment` in the entitlements), on every registration.

An unrecognised value is stored as empty rather than guessed, because "we do not
know" has to stay distinguishable from "we know it is production".

Send this **on every launch**, not once at first run. iOS reissues the APNs
token on reinstall, on restore from backup, and occasionally on its own; a
client that registers once will one day stop being notified with nothing on
screen to say so. Re-registering an unchanged token is a no-op server-side, so
there is no cost to being unconditional about it.

The device is identified by its own bearer token — there is no device id in the
body. That is deliberate: an id in the request would let any enrolled client
redirect another device's notifications to a token it controls.

`{"push_token": ""}` deregisters. That is how "turn notifications off" is
expressed; it is not the same as revoking the credential, and the app should
never reach for revoke to mean this.

A read-only device may call this. See Read-only devices.

Push tokens are never readable through the API — `GET /api/devices` reports
`"push": true|false` per device and never the token itself.

---

## 4. The endpoints you need

All JSON, all behind the bearer token unless noted.

### `GET /api/sessions` — the main screen

```json
{
  "now": 1785460000,
  "version": "734c6b4",
  "nodes": [
    {
      "node": "wsl",
      "online": true,
      "version": "734c6b4",
        "capabilities": ["docker", "ffmpeg", "go", "gpu.nvidia", "tmux"],
        "capabilities_known": true,
        "payload_known": true,
        "payload_pending": 0,
      "sessions": [ … ]
    }
  ]
}
```

**`node`** is that machine's name, and it is the value every write puts in its
`node` field — it is how the coordinator decides which agent to route to, so a
client should carry it around rather than reconstruct it.

**`online`** is whether that node's agent currently holds its command stream
open. It is the difference between a node whose panes can be driven and one that
can only be looked at: the sessions of an offline node are its last reported
view, and any write aimed at it will fail. Show it; a stale session list that
looks live is the thing this field exists to prevent.

**`capabilities` is what that machine can do**, and is **absent for an offline
node** — a host nobody can reach can do nothing, so an offline machine
advertising a microphone would invite work that cannot arrive.

Most of it is detected (a curated toolchain vocabulary — presence only, never
versions); the node's own project may also declare what no probe can establish,
such as `always-on` or `apple-toolchain`. Where the two disagree about something
checkable, detection wins.

**Check `capabilities_known` before concluding anything from an empty list.** An
absent `capabilities` means nothing on its own: it is equally a machine with
nothing to report and an agent whose build predates capability reporting. Only
when `capabilities_known` is `true` does an empty list mean "this host can do
none of these things". Treating the two as the same is how a router declines to
send work a machine could perfectly well have done — and `upgrade --all` is
deliberately serial, so a mixed fleet exists during every upgrade.

**`payload_pending` is how many of that node's `~/.claude` files differ from the
payload inside its own binary** — non-zero means somebody should run `shabadoo
setup` on that machine. It exists because `upgrade` replaces a binary and never
runs the config step, so a node can carry new guidance inside itself while the
old copy sits on disk indefinitely, looking entirely healthy.

**`payload_known` gates it, for the same reason `capabilities_known` gates the
list above.** A node that could not perform the check reports `payload_known:
false`, and `payload_pending` is then meaningless — absent rather than zero. A
client must not render "could not look" as "nothing to do"; that is the failure
this whole pair of fields exists to avoid, and it has been made three times in
this codebase already.

A client's only correct action on a non-zero count is to *show* it. There is
deliberately no endpoint to run `setup` remotely: it writes into a directory the
operator hand-edits, which `setup` is careful never to own.

Useful for shaping what a client offers: do not present "build the iOS app" on a
node without `ios.build`. It is **not** a permission — nothing here grants
anything, and the API refuses on its own terms whatever a client chose to show.

A session object:

| Field | Type | Meaning |
|---|---|---|
| `session_id` | string | stable id, unique per tenant — use as the list identity |
| `agent` | string | which node owns it |
| `project` | string | project name, **path-derived** — a session scoped into a subfolder reads as `shabadoo/hub`, not a bare `hub` |
| `kind` | string | `claude` \| `worker` \| `core` \| **absent** — see below |
| `description` | string | the project's one-line routing card; **absent** unless its `CLAUDE.md` declares one |
| `cwd` | string | absolute path |
| `alias` | string | friendly name; **display this** |
| `name` | string | raw tmux window name — what `/api/reopen` takes, not `alias` |
| `window` | string | `"<tmux session>:<index>"` |
| `tmux_session` | string | send this in write bodies |
| `index` | int | send this in write bodies |
| `status` | string | `active` \| `idle` |
| `pane` | int | which pane within the window; **0 unless a window has been split** |
| `tools_stale` | bool | **absent unless true** — this session's tool surface predates the running agent |
| `input_state` | string | `composer` \| `dialog` \| **absent** |
| `tokens_in`, `tokens_out`, `tokens_cache` | int64 | what this session has spent; **absent** for anything with no transcript |
| `note` | string | what the session says it is DOING, in its own words; **absent** unless set, and expires after 30 minutes |
| `activity` | int64 | unix secs, last pane activity |
| `pending` | int | undrained inbox count |
| `panes`, `command`, `updated_at` | | display detail |

**`tools_stale` means this session needs restarting to see new tools.**

A session's MCP tool surface is fixed at the moment it starts. Upgrading the
agent does nothing for anything already running, so a release that adds a tool
reaches only sessions started afterwards — and the session itself cannot tell,
which is the worst place for that fact to hide. It was found by a session being
told about three new tools and not finding any of them.

Worth surfacing: a badge on the row, and "restart to pick up new tools" rather
than anything alarming. It is not an error and nothing is broken — the session
works exactly as it did, it simply cannot reach anything added since it began.
Recycling the window is the whole fix.

**`pane` matters only on a window somebody has split.** It is 0 everywhere
else, which is every window until a session spawns narrower work — and pane 0
deliberately keeps the session id its window has always had, so nothing a client
already stores changes.

**Send it back on writes.** Omitting it means "whichever pane is active", which
is what every client written before panes existed meant and remains the safe
default. But a coordinator will REFUSE a write naming a pane above 0 when that
node's agent is too old to honour it, rather than quietly writing to the active
pane — so a 4xx there means upgrade the node, not retry without the field.

**Tokens are cumulative for the session's whole transcript**, not since the
client connected. Useful for showing what a session has cost; do not treat a
difference between two polls as a rate, since a session that was cleared starts
a new transcript and the number drops.

**`note` is the session's own account of what it is doing**, which is the thing
`status` cannot tell you — `active` and `idle` are tmux's view, and only the
session knows it is waiting on a peer rather than merely quiet. It expires after
thirty minutes, deliberately: a session that set one and then died would
otherwise claim to be mid-task forever.

**`kind` says what is running in the pane, and absence is meaningful.** A node
on a build older than this reports nothing, which is not the same as `worker`.

- `claude` — a Claude session started by this toolchain. What almost everything
  in this document assumes.
- `worker` — something else entirely in a tmux window: a build, a recorder, a
  shell. **Do not offer Claude-shaped actions on one.** `/api/keys` still works
  because tmux does not care, but slash commands, transcripts and dialog
  handling are all meaningless there.
- `core` — the node's own session. It speaks for the machine and is the only
  thing permitted to start sessions on it. Always running; killing it is not a
  useful thing to offer, since the agent restarts it within seconds.

**`description` is trigger text, not a summary.** It answers *when should this
be reached for*. Reasonable to show under a project name; it is what a router
consults to decide where work belongs, so it is short by construction.

> **`command` is whatever tmux says, and tmux can be unhelpful.** On the Mac it
> reads `2_1_220` rather than `claude`, because `~/.local/bin/claude` is a
> symlink to a versioned binary and macOS resolves it — so the process really is
> named after the version. It is not wrong, it is faithful. Do not key logic off
> this field; use `alias` or `project` to identify a session.

> **The response names and the write-body names differ, and nothing warns you.**
> A session reports `tmux_session` and `index`; a write body wants those same
> two values under `session` and `window`:
>
> ```
> session.tmux_session  ->  body.session
> session.index         ->  body.window
> session.agent         ->  body.node
> ```
>
> Same class of trap as `alias` vs `name`. Map it once in your client and never
> think about it again.

**`input_state` is tri-state and absence is meaningful.** `dialog` means the
session is blocked waiting on a human — that is the app's whole reason to exist.
Absent means the agent could not classify it; it is **not** the same as
`composer`. The classifier also **fails open**, so a false `composer` is
possible and a false `dialog` is not. Never infer "blocked" from anything else.

Poll every 3–5 seconds while foregrounded. **There is no SSE or websocket on the
human plane yet** — if you find yourself designing around a push stream, that
endpoint does not exist. Ask before building against it.

### `GET /api/capture` — read a pane

```
GET /api/capture?node=wsl&session=claude&window=7&lines=200&color=1
```

Returns **`text/plain`, not JSON**. `lines` is scrollback depth, clamped
0–5000; `0` is the visible screen only. `color=1` gives raw ANSI (SGR
sequences) — parse or strip it; omit the parameter for plain text. Bounded by
what is still in tmux's scrollback, so it is not history.

### `POST /api/keys` — answer a blocked session

```json
{"node": "wsl", "session": "claude", "window": 7, "keys": ["Enter"]}
```

Raw keypresses, tmux key names: `Enter`, `Escape`, `Up`, `Down`, `1`, `y`.
**This is how a dialog gets answered** — text typed into a modal is swallowed.

Never send `Escape` on the user's behalf without them choosing it. Escape during
a running turn interrupts Claude's work, and discarding someone's in-flight turn
is their call.

### `POST /api/send` — type a line

```json
{"node": "wsl", "session": "claude", "window": 7, "text": "run the tests", "enter": true}
```

Clears the input line, types the text, optionally submits.

**It is refused with an error when the pane has a dialog up**, because a modal
swallows text and the send would silently do nothing. Check `input_state` first
and offer keys instead.

**Multi-line text does not submit.** It arrives as a bracketed paste and the
trailing Enter inserts a newline. Send the text, then a separate
`POST /api/keys ["Enter"]`. This bit the desktop client; do not rediscover it.

### `POST /api/command` — a slash command

```json
{"node": "wsl", "session": "claude", "window": 7, "command": "/clear"}
```

### `GET /api/events` — the same view, pushed

```http
GET {coord}/api/events        text/event-stream
```

One `data:` frame per change, payload identical to `/api/sessions`, plus a
`: ping` keepalive every 25s.

**Keep your polling path.** That is the design, not caution — see *Things that
will bite you*. Start polling, switch to the stream only once a frame has
actually arrived, and fall back on error or on silence past ~45s.

Frames are **deduped server-side**: the coordinator skips a frame that would
render identically, so an idle screen receives nothing but keepalives.
Consequence — `now` stops advancing between frames, so **advance relative times
("idle 4m") locally** rather than waiting for a frame that is not coming.

### `GET /api/claude/session` — what the Claude session is doing

```http
GET {coord}/api/claude/session?node=wsl&path=/c/projects/homelab
```

Model, turn counts, token totals, tools used, last prompt. This reads Claude's
own transcript, not the terminal, so it is the real answer to "what is this
session up to" — but see the warning about its read surface in *Ground rules*.

### Session-to-session mail

| Endpoint | Use |
|---|---|
| `GET /api/messages?limit=&session=` | the durable inbox: last 24h across the tenant, or one session's thread. **Read-only — looking never consumes it** |
| `POST /api/message/send` | `{to_session, title, body}` |
| `POST /api/message/broadcast` | `{topic, title, body}` |

### Managing devices from the app

| Endpoint | Use |
|---|---|
| `GET /api/devices` | enrolled clients: label, scope, expiry, and `push` (bool — the token itself is never readable back) |
| `POST /api/devices/code` | mint a pairing code, so a paired phone can enrol the *next* device. `{label, scope}` |
| `POST /api/devices/revoke` | `{device_id}` — immediate and permanent; that client is signed out and cannot renew |

### Others

| Endpoint | Use |
|---|---|
| `GET /api/folders?node=` | startable folders, each flagged `open` |
| `POST /api/open` | `{node, path}` — start a session |
| `POST /api/reopen` | `{node, name}` — **raw** window name |
| `POST /api/kill` | `{node, session, window}` |
| `POST /api/select` | `{node, session, window}` — make a pane the active one in tmux |
| `GET /api/input-state` | one pane's state; for the instant after answering, when waiting 5s for the next report is too slow |
| `GET /api/audit` | `?limit=` — who drove which pane, newest first |
| `GET /healthz` | **no auth** — reachability check before showing a login error |

### `POST /api/voice/session` — talk to your sessions

```http
POST {coord}/api/voice/session
   → {"signed_url": "wss://…", "agent_id": "…", "scope": "full"}
```

Mints a **short-lived signed WebSocket URL** for an ElevenLabs conversational
agent. Open it directly from the app; the audio never touches the coordinator.

**The API key is never sent to you and must never be in the app.** It is
account-wide and billed per minute, so a copy in a shipped binary is a copy on
every phone that installs it. The coordinator holds it and hands out signed
URLs; that is the entire reason this endpoint exists.

Returns **404** when the coordinator has no voice configured — treat that as
"this deployment does not do voice" and hide the feature, not as an error.

**Rate limited to 30 sessions per device per hour**, returning **429**. This is
the only endpoint that costs money when you call it, so the limit is about
spend rather than abuse. Mint one when the user starts talking, not on launch.

#### What the agent may do, and why you do not enforce it

The agent's tools run **on your side**, calling this same API with **this
device's existing token**. So you do not implement permissions:

- a **read-scoped** device's `send` gets a 403 from the coordinator, exactly as
  it would from a button;
- the `scope` in the response is for **shaping the UI** — grey out dictation on
  a read-only device rather than letting someone talk into a refusal. It is not
  what enforces anything.

Suggested client tools:

| Tool | Calls | Notes |
|---|---|---|
| `list_sessions` | `GET /api/sessions` | who is running, who is blocked, what each is asking |
| `send_message` | `POST /api/send` | dictation into a pane; 403 on a read-only device |

**Deliberately not `read_pane`.** A pane is a screenful of box-drawing and
diffs; read aloud it is unusable, and the `asking` field is the useful sentence
out of it. Add a capture tool only if a real gap appears, not on principle.

#### Do not give it a tool that answers a dialog

There is deliberately **no keypress tool**. Not "the agent is told not to" —
the tool does not exist, because an agent instructed not to approve can be
argued into approving, and one with no such tool cannot regardless of what it
decides.

The reason is the same one behind there being no answer button on a queue row:
these panes run `claude --dangerously-skip-permissions`, and approving
something you have not read is the one interaction that can do real damage.
When the user asks to approve, the right behaviour is to **open that pane** so
they can read it — one more tap, with the dialog on screen.

If you do surface the question by voice, speak the **verbatim** `asking` string
from `/api/sessions`. Never let the model paraphrase it: rendering
`Do you want to delete /etc/foo?` as *"it wants to remove a config file, shall
I approve?"* is precisely the failure this is guarding against.

### Deliberately out of scope for a phone

These exist and your token can call them, but they are operator-grade and a
phone is the wrong place for them. Named here only so you do not wonder whether
you missed something:

`POST /api/releases`, `POST /api/upgrade`, `POST /api/nodes/disconnect` —
publishing binaries, replacing a node's binary, and cutting a node off. Use the
`shabadoo` CLI.

---

### `GET /api/tasks` — what did I hand off, and where did it get to

The waiting queue answers *who needs me*. This answers the other question, which
is the one an operator has away from their desk. Both have existed on the agent
plane since tasks shipped; only the first was reachable from a phone.

```json
{ "tasks": [ {
  "id": "cfbf26b53ac9…",
  "session_id":   "claude-mac-78709fb6",
  "requested_by": "claude-shabadoo-wsl-1ef3aefe",
  "thread": "nudge-guard",
  "state": "active",
  "brief": "Please independently verify the nudge guard shipped in v0.4.12…",
  "note":  "composer fixture does not match darwin — no box characters at all",
  "created_at": 1787901454, "updated_at": 1787901812
} ] }
```

`state` is one of **`open`, `active`, `blocked`, `done`, `dropped`** and never
anything else. `dropped` is an *answer* — deciding not to do something, which
without it has nowhere to go but silence — so render it as a resolution rather
than a failure.

`note` is the assignee's last word: the reason for a `blocked`, or the outcome
of a `done`. An update with no note leaves the previous one standing rather than
erasing it, so a `note` may be older than its `updated_at`.

**Finished work is hidden unless `include_done=1`.** `tasks` is `[]`, never
`null`.

Nothing needs chasing from a client: a task untouched for 6 hours raises once
then daily, and the requester is mailed automatically when one reaches `done` or
`dropped`.

**Creating and closing tasks is deliberately absent.** A task is a handoff
between sessions; a person driving one from outside would be recording work
nobody was asked to do.

### `GET /api/events` carries a frame sequence

Every `data:` frame includes a monotonic **`seq`**, and the keepalive is now a
named event carrying the coordinator's current value:

```
data: {"now":…,"version":"v0.4.21","nodes":[…],"seq":42}

event: ping
data: {"seq":42}
```

Compare them. **Equal** means the fleet is genuinely idle — frames that would
render identically are skipped deliberately, so an idle fleet must not cost more
than the poll this replaced. **Greater than yours** means you missed a frame and
are rendering stale state believing it is current: resync with `/api/sessions`.

This exists because silence was ambiguous and could not be resolved from the
client side. `: ping` is still emitted for anything written against the old
wire, but a `:` comment is never surfaced to `EventSource`, so it could not
carry state even in principle — which is why clients were resolving it with a
silence timer, a guess dressed as a policy.

Keep a silence timer anyway, for the case the sequence cannot cover: a buffering
proxy swallows the keepalive too, so nothing arrives to compare.

**`seq` is per-connection, not global.** It counts frames on *your* stream and
restarts at 1 when you reconnect; it is not a cursor and does not address
history. It is deliberately not SSE's own `id:` field, because that is
`Last-Event-ID`, which a browser replays on reconnect — and this server keeps no
history to honour it with. Claiming resumability it cannot provide would be
worse than not offering it.

`/api/sessions` carries no `seq`. It has no stream, so a number there would
always be the same and mean nothing.

## 5. Things that will bite you

**An offline node's sessions are frozen, including `input_state`.** They are its
last reported view, so a session that was at a dialog when its agent dropped
still reads `dialog` — and a naive waiting count says "1 waiting on you" forever
about a prompt nobody can answer through here. **Filter the queue by
`node.online`.** Reported by a client author who hit it first; the dashboard and
the CLI had the same bug and have been fixed.


- **Every write is audited** and attributed to your device. This is a feature;
  do not batch or retry writes blindly, because a retry storm is legible as one
  in someone's audit panel.
- **`alias` vs `name`.** Display `alias`; send `name` to `/api/reopen`. Getting
  them backwards fails confusingly.
- **The coordinator is tailnet-only.** `coordinator.example` resolves to a
  Tailscale address. The phone needs Tailscale connected and up — a "cannot
  reach coordinator" state is normal and needs its own screen, not an error
  toast. `GET /healthz` distinguishes "network is fine, you are signed out"
  from "cannot reach it at all".
- **Version skew.** `/api/sessions` returns the hub's `version` and each node's.
  If they differ, the deployment is mid-upgrade; surfacing it is genuinely
  useful.
- **Do not add a dependency to solve something small.** The coordinator is
  stdlib-only by policy. That does not bind the app, but it is the house
  style — prefer the boring solution.

---

## 6. What does not exist yet

Ask before designing against any of it:

| Wanted | State |
|---|---|
| **APNs push delivery** | **half built.** A device can register a token (`PUT /api/devices/self/push`) and the coordinator stores it — but there is **no APNs sender**, because that needs a team id, key id and `.p8` from a developer account that does not exist yet. Notifications reach a phone today via the coordinator's Apprise relay (Telegram/Pushover), not via APNs. Give me a bundle id and the sender is a drop-in |
| A rendered conversation view | **not built.** `/api/capture` scrapes the terminal; `/api/claude/session` gives totals, not turns. Rendering the messages themselves is the next phase |
| Session history / resume | **not built** |
| Transcript search | **not built**, and deliberately dropped — say so if you want it |
| OpenAPI spec | does not exist. This document and `CLAUDE.md`'s tables are the contract |

Shipped since the first draft of this document, in case you designed around
their absence:

| Was missing | Now |
|---|---|
| `push_token` on a device | `PUT /api/devices/self/push`, updatable, allowed under read scope |
| SSE for the human plane | `GET /api/events` — see §4 |
| Knowing when your token expires | `X-Shabadoo-Token-Expires` on every authenticated response |
| Telling 401 from 403 | 401 = re-pair, 403 = scope limit, **do not discard the token** |
| Revoking a client | `POST /api/devices/revoke`, and `shabadoo revoke` from a terminal |

**Phase 1 is a SwiftUI list bound to a poll, plus the scanner and Keychain.**
That is genuinely a day's work against this document, and it is worth having
before anything above. Add the stream in phase 2 — the poll is its fallback, so
it is never wasted work.

---

## 7. Ground rules

- **Do not write coordinator-side code.** If you need an endpoint or a field,
  say so and it will be added. Two people editing the same Go package from
  different machines is how the contract drifts.
- Build against the real coordinator, not a mock. It is reachable from any
  tailnet device, and a mock would encode today's misunderstandings.
- When something in this document turns out to be wrong, report it — a stale
  contract is worse than a missing one.
