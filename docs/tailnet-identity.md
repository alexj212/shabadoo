# Running a coordinator on someone else's tailnet

The design question: if someone runs a shabadoo hub in a container and is a
Tailscale user, can the app put *itself* on the network rather than depending on
the host?

**Decision: yes, and it joins the CLIENT's tailnet, not ours.**

That single choice settles most of the rest, so it is worth being explicit about
what it buys and what it costs.

---

## What the decision means

| | Consequence |
|---|---|
| **The operator owns the tailnet** | They generate the auth key, set the ACLs, add and remove users. None of it is in our infrastructure |
| **We cannot reach their coordinator** | Not a limitation to work around — it is the product being self-hosted. Support is diagnostics they can run and paste, never a connection we open |
| **We never hold their credentials** | No auth keys, no tokens, no database. There is nothing of theirs to leak |
| **Their nodes dial their hub over their tailnet** | No inbound ports anywhere in that setup, same as ours |
| **One tailnet = one tenant** | The multi-tenant model the architecture anticipated, arrived at naturally rather than invented |

This makes shabadoo a genuinely self-hosted product rather than a service with a
self-hosted mode, and it matches the positioning exactly: private by
construction, no cloud, no account.

---

## The part that is already built

`hub.TailscaleProvider` — a fourth `IdentityProvider`, enabled with
`--tailscale-allow alice@example.com,bob@example.com`.

```
$ curl https://coordinator/api/sessions     # no token, no pairing, no QR
HTTP 200
$ shabadoo audit
alex@example.com | select        # the person, not a device id
```

Verified against a real tailnet: an allowed login gets 200 with no credential of
any kind, a tailnet member *not* on the allowlist gets 401, and a loopback
caller gets 401.

**It dissolves the bootstrap paradox.** Enrolling the first device requires an
enrolled credential to mint a pairing code — broken once by `--bootstrap`
printing a code into the service log. Someone standing up their own coordinator
meets that on day one, and it is the single worst moment in onboarding. With
tailnet identity the first user is simply identified.

### Membership is not authorization

The mistake available here is gating on "is on the tailnet". A tailnet holds
phones, TVs and family devices, and reaching this dashboard means driving panes
running `claude --dangerously-skip-permissions`.

So the provider is **default-deny**: an empty allowlist admits nobody, and only
exact logins get in. A **tagged** node — an agent container, a CI runner — is
refused outright: it is a service with no login, and attributing an audit entry
to it would name nobody.

### Why a subprocess, not a library

Identity comes from `tailscale whois --json`, shelled out. That is this
codebase's existing answer to the same question — it shells out to `tmux` for
every pane operation and to `tailscale ip -4` during setup — and it adds
**nothing** to `go.mod`.

---

## The part that is not built: `tsnet`

`tsnet` makes the binary *itself* a tailnet node: its own address, its own
MagicDNS name, and `ListenTLS` for automatic HTTPS from Tailscale's certs. No
host Tailscale, no TUN device, no `NET_ADMIN`, no sidecar. It would delete
Caddy, the Cloudflare resolver and the whole TLS story.

It is also the best possible onboarding story: *download one file, run it with
an auth key, reach it from your phone over HTTPS.*

**The cost, measured rather than guessed** (a `tsnet` hello-world, built):

| | today | with `tsnet` |
|---|---|---|
| modules | **30** | **547** |
| binary | 18 MB | ~22 MB stripped, hello-world alone |

Binary size is a non-issue. The dependency tree is the issue: it pulls AWS SDK
fragments, a TOML parser and gVisor's netstack into a binary whose job is
driving shells with permissions disabled. For a project whose stated convention
is stdlib-only, that is a real change.

`WhoisFunc` exists so this stays a swap rather than a rewrite: the `tsnet` path
would replace one function and touch nothing else. If it is adopted, put it
behind a **build tag** so the default binary stays lean.

---

## Operational footguns, before anyone hits them

1. **Use a tagged, reusable auth key.** An ordinary key expires — typically 90
   days — and the coordinator silently drops off the tailnet when it does.
   Tagged nodes do not expire. This is the one that will bite.
2. **Persist the tsnet state directory.** It holds the node key. If it is not on
   a volume, every container restart creates a *new* tailnet node, and the
   operator's device list fills with `shabadoo-1`, `shabadoo-2`, … while the old entries
   linger. Put it beside `hub.db`.
3. **Never enable Funnel.** It publishes the coordinator to the open internet,
   which is the exact opposite of every other decision here.
4. **Support is diagnostics, not access.** We cannot reach their host. `healthz`,
   `shabadoo doctor` and the version stamps have to be enough to debug from a
   paste. Worth testing that assumption before they need it.
5. **They are the first real second tenant.** Multi-tenancy is built and has never
   been exercised. Expect to find things.

---

## What is still open

- Whether to adopt `tsnet` at all, or stay with a Tailscale **sidecar container**
  (`network_mode: service:ts-shabadoo`), which costs zero dependencies and still
  supports `whois` if the daemon socket is shared into the hub container.
- Whether `--tailscale-allow` should support a **tag or group** (`tag:ops`)
  rather than only literal logins, so the operator manages membership in the
  Tailscale admin rather than in a flag. Probably yes, and it is a small change.
- Scopes: a tailnet identity is currently full-access. Read-only enrolment
  exists for device tokens and has no equivalent here yet.
