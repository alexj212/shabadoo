# Getting started

Someone handed you `shabadoo`. This gets it running.

You need **tmux** and **Claude Code** already installed. Everything else is one
file.

---

## The one line

Paste this into Claude Code, in any folder:

```
Install shabadoo for me: read https://raw.githubusercontent.com/alexj212/shabadoo/main/docs/getting-started.md
and follow the "By hand" section for my platform. Verify the checksum before
running anything, tell me what it verified, and stop if it does not match.
```

**That is deliberately a prompt and not `curl … | sh`.** This project's own rule
is that nothing fetches from the network during install, and a tool whose first
instruction is "pipe an unread script into your shell" has broken that rule in
its opening sentence. A prompt keeps the convenience and gives it back the
property that matters: **you can see every command before it runs, and something
that reads has checked the checksum rather than skipping it because the pipe made
it awkward.**

If you would rather not hand this to a model, the same four commands are below
and they take a minute.

---

## By hand

```bash
OS=$(uname -s | tr '[:upper:]' '[:lower:]')          # linux | darwin
ARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
BASE=https://github.com/alexj212/shabadoo/releases/latest/download

curl -fsSL -o shabadoo "$BASE/shabadoo-$OS-$ARCH"
curl -fsSL -o SHA256SUMS "$BASE/SHA256SUMS"

# Verify before running it. A published checksum only means something if
# somebody checks it, and this is a binary you are about to give a terminal.
grep " shabadoo-$OS-$ARCH$" SHA256SUMS \
  | sed "s/shabadoo-$OS-$ARCH/shabadoo/" | sha256sum -c -

chmod +x shabadoo && ./shabadoo version
```

`sha256sum` is `shasum -a 256` on macOS. Platforms: `linux-amd64`,
`linux-arm64`, `darwin-amd64`, `darwin-arm64`.

**macOS builds are unsigned.** `curl` does not set the quarantine attribute, so
the above works as written. A binary downloaded through a *browser* is
quarantined and Gatekeeper will refuse it — clear it with
`xattr -d com.apple.quarantine shabadoo`.

---

## Set it up

```bash
./shabadoo setup            # installs to ~/bin, writes the config, reports the rest
shabadoo doctor             # what a re-run would change; writes nothing
```

`setup` is idempotent and never clobbers silently: anything it replaces is copied
to `<path>.bak.<epoch>` first, and on a correctly-installed machine `doctor`
reports zero changes. It scaffolds `~/.config/claude/env` only if absent, because
that file holds decisions rather than content this binary owns.

Then, in any project folder:

```bash
shabadoo attach             # start or re-attach this folder's Claude session
```

That is the whole single-machine install. Everything below is optional.

---

## Staying current

```bash
shabadoo update --check     # is there a newer release?
shabadoo update             # replace this binary with it
```

It asks GitHub which release is newest, verifies the published checksum, **runs
the downloaded binary and requires it to report the expected version**, and only
then swaps — keeping the previous build alongside as `.prev`. A checksum proves
the bytes arrived intact; it says nothing about whether they execute on your
machine, which is what the extra step catches.

It compares tags for **equality**, never order. Two `git describe` strings cannot
be ordered, so this does not pretend to know which of two builds is newer — only
whether you are on the one GitHub calls latest.

**A service does not restart itself.** If you installed the agent as a service,
it keeps running the previous binary until it restarts.

---

## More than one machine

A **coordinator** merges every machine's sessions into one dashboard. One person
running one machine does not need it; the moment there are two, it is the point
of the tool.

```bash
# on something always-on
shabadoo hub --device-tokens --bootstrap --addr 0.0.0.0:8787

# on every machine you work on
shabadoo setup --service --coord https://your-coordinator:8787
```

Then pair a client with the code `--bootstrap` printed, and remove that flag —
it mints a fresh code on every start.

> **Run your own — this tool is single-operator.** Joining somebody else's
> coordinator is not like joining a chat. Authorising an agent hands *your*
> machine's Claude panes to anyone who can reach *their* dashboard, and those
> panes typically run `claude --dangerously-skip-permissions`. The reverse is
> equally true: their machines become drivable from your credential.
>
> Multi-tenancy is implemented and barely exercised, so treat it as one operator
> per coordinator. Two people who want to compare notes should run two
> coordinators, not share one. **Nothing you do is visible to whoever gave you
> this**, which is the property that makes it safe to hand out.

---

## Read this before you rely on it

- **Reaching the dashboard means driving every pane.** Whoever holds a credential
  can read any project path, any pane's full buffer, any Claude transcript, and
  send keystrokes into any pane. `pair --scope read` limits a client to watching
  and is the right default for anything that only needs to look.
- **Keep it on a private network.** A VPN or tailnet is the simplest way to mean
  that. It refuses to start without an auth posture, but that is a floor, not a
  substitute.
- **Everything is self-hosted.** No cloud service, no account, no telemetry. Your
  prompts, panes and transcripts stay on your machines.

## Other tools, distributed the same way

The coordinator can also install tools that are *not* shabadoo — an orchestrator
and its native helper, say — across your own machines:

```bash
shabadoo publish --tool minutes dist/release   # from a machine that can build it
shabadoo upgrade --tool minutes --all          # install it on your nodes
```

**Publishing is partial by design.** A release is a *set*, and no host can build
every platform's — a native audio helper needs MSVC on Windows or `swiftc` on
macOS. So sets are merged from several machines over time, and a node offered a
tool with no set for its platform is **skipped and told which**: *"not published
for your platform"* and *"published and you are behind"* are different answers,
and a node given the wrong one either installs nothing forever or chases a
version that cannot exist for it.

This runs against **your** coordinator. It is not a package repository and
nothing here reaches out to anybody else's.

## Where to look next

| | |
|---|---|
| `shabadoo --help` | every command, with the build stamp on the first line |
| `shabadoo sessions` | every session on every machine |
| `shabadoo rules` | which guidance is in effect here, and from where |
| `README.md` | what this is and why |
| `docs/shabadoo.md` | architecture, auth planes, the agent protocol |
