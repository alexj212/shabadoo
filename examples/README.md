# Examples

Working starting points, in the order you are likely to need them.

| File | What it gets you |
|---|---|
| [`docker-compose.yml`](docker-compose.yml) | A coordinator on loopback, in about a minute. Start here. |
| [`docker-compose.traefik.yml`](docker-compose.traefik.yml) | The same coordinator behind TLS, with the one proxy setting that otherwise breaks it silently. |
| [`node.service`](node.service) | A per-host agent as a systemd unit, when you would rather not run `setup --service`. |

Every one of these needs an **auth posture** — the coordinator refuses to start
without `--device-tokens`, `--access-team`/`--access-aud`, or
`--insecure-no-access`. There is no default, deliberately: the posture that
would "just work" is the absence of authentication, and this is a service whose
whole job is typing into panes that run with permissions disabled.

## The first device

Enrolment is circular by design — minting a pairing code requires an already
enrolled credential — so a fresh database cannot bootstrap itself. `--bootstrap`
breaks the loop exactly once by printing a single-use code to the log:

```bash
docker compose logs | grep 'pairing code'
shabadoo pair --code <CODE> --coord http://<host>:8787
```

Then **remove `--bootstrap` and restart.** It mints a fresh code on every start,
and a pairing code sitting in a container log is a credential sitting in a
container log.

## Before you put a proxy in front

Agents hold a long-lived `text/event-stream` open to receive commands, and so
does the dashboard (`/api/events`). Any buffering or compressing middleware on
those routes presents as **"agents connected, commands never arrive"** — no
error, no log line, just a dashboard that renders and never updates.

It is the single most common way a working deployment is broken by a proxy, so
verify it rather than assuming:

```bash
# Should print frames as they happen and never exit. If it hangs with no
# output, something between you and the hub is buffering.
curl -N -H "Authorization: Bearer $TOKEN" https://coordinator.example/api/events
```

`docker-compose.traefik.yml` is annotated with what to exclude and why.

## Not containerised: the node

Only the **hub** ships as an image. A node drives the host's tmux, so a
containerised node would be an agent with no sessions to manage — install it on
the host with `shabadoo setup --service --coord <URL>`, or use
[`node.service`](node.service) if you want the unit written by hand.
