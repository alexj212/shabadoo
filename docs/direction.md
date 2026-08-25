# Direction: an operating system for agent work

> A **direction document, not a spec.** Nothing here is built. Every design
> question raised while writing it was answered, and the answers are recorded as
> decisions with their reasons and their accepted costs — so a later reader can
> tell a considered trade-off from an accident.
>
> One decision was **reconciled rather than instructed** — see *Waking a stopped
> session*. It is flagged there and in *Still open*.

## The premise

shabadoo began as a way to see and drive tmux sessions from a browser. What it
became is the substrate for something else: **an operating system whose
processes are reasoning agents.**

Each node is a brain with its own abilities — one has a microphone and an iOS
toolchain, one has the work source trees, one is always on and runs containers,
one has a local model. Together they form a hive that is addressable from
anywhere. The primary client is Claude, but nothing in the design requires the
thing on the end of a session to be Claude.

## Taking the OS metaphor literally

Useful precisely because it shows where the holes are.

| OS concept | shabadoo today |
|---|---|
| processes | sessions ✓ |
| IPC | the messaging plane ✓ |
| syscalls | the MCP bridge (`shabadoo mcp`) ✓ |
| users, permissions, audit | device tokens, scopes, the audit log ✓ |
| init | `boot` + the cron watchdog ✓ — opens a fixed list, knows nothing |
| **device drivers** | **missing.** A node reports `name`, `fingerprint`, `version`, `last_seen`. Nothing describes what it can *do*. |
| **scheduler** | **missing.** Work is routed by a human typing. |
| **process lifecycle** | **missing.** A session is running or it does not exist. |
| namespace | projects — below |

One dormant asset: the **`tasks` table already exists in the schema**
(`id, session_id, thread, state, brief, created_at, updated_at`, indexed on
`(tenant, session_id, state)`) and **nothing reads or writes it.** That is the
scheduler, declared and unwired.

## A project is a directory

A project's identity is its path. Sessions derive their project from `cwd`, mail
is addressed to a project, and expertise lives in that directory's `CLAUDE.md`
and memory. No registry to keep in sync, no second source of truth.

- **Things without a directory are not projects.** A meeting recorder is not a
  project; it is a *worker*. That is what `kind` is for, and it is why `top` in a
  tmux window currently misreports itself as project `buildwatch`.
- **Anything routed is routed to a real directory**, where the session owning
  that domain will read it.

### The project root is the nearest CLAUDE.md that is a git root

Walk up from `cwd` to the nearest ancestor holding a `CLAUDE.md` **that is also a
git repository root**. Fall back to the nearest `CLAUDE.md`, then to the
directory itself.

The git qualifier is not tidiness; without it the rule breaks on a real machine.
On the machine this was designed against, a shared workspace directory and the
home directory each hold a `CLAUDE.md` and neither is a git root. A pure
nearest-CLAUDE.md rule would therefore re-root about ten sibling projects under
the workspace, and everything beneath the home directory under the home
directory's own name. With the qualifier, `shabadoo` is a git root so
`shabadoo/hub` works, while those siblings keep the names they have today.

### Each node has a main project, and its core session always runs

One directory per node holds that node's configuration and knows its
environment: what is installed, what the machine can do, where things live, what
is peculiar about it. Its session is the node's **core session** — the
addressable "you" of that machine, always running, the supervisor of what runs
there, and the only actor permitted to start sessions on it.

No new concept is needed: the thing that knows the machine is a project like any
other, with its own `CLAUDE.md` and memory, loaded only by the session that needs
it.

**The discipline that makes it work:** an always-on session is the one that must
stay cheap. The core session routes, decides and starts; it should not do the
work itself, or a context that never ends fills with the details of finished
jobs. *Mechanical facts are data, judgment is a session.*

### A project narrows into its subfolders, and spawns into panes

A session in `shabadoo/hub` sees a smaller world than one in `shabadoo`. The
parent coordinates and holds the wide view; children do narrow work. The same
routing primitive at every level.

**This already happens.** On a real machine, several session histories are
already subfolders of other projects — `<project>/examples/<sample>`,
`<workspace>/<project>/apps/<component>`. It works because `windowName`
(`launch.go`) hashes the **full path**. What is missing is that the system does not know the relationship:
`filepath.Base(cwd)` turns `/c/projects/shabadoo/hub` into project `hub`,
indistinguishable from any other `hub` and unreachable by addressing `shabadoo`.

**A spawned session is a tmux pane, and panes are addressed.**
`{node, session, window}` becomes `{node, session, window, pane}` across the wire
protocol, every op in `handleOp`, `reportSessions`, `input_state` and the
dashboard's row model. This is the largest single piece of work the direction
implies, accepted deliberately: without it a spawned child cannot receive mail or
hold a task, which makes spawning a layout trick rather than a delegation
primitive. It also fixes a live hazard — `target()` is `session:window`, which
tmux resolves to whichever pane is *active*, so a multi-pane window today
silently accepts writes aimed at the wrong pane.

**Pane 0 keeps the session id it has now.** `sessionID` is
`"claude-" + window name`; that stays for a window's first pane, and only extra
panes take a suffix. Session ids are how mail is addressed, so renaming them all
at once would orphan every undrained handoff. Nothing changes until a window is
split.

**The trap: narrowing the directory narrows the file tree, not the
instructions.** Claude Code loads `CLAUDE.md` from the working directory upward,
so a session in `shabadoo/hub` still loads shabadoo's ~1,200-line `CLAUDE.md`. A
subfolder `CLAUDE.md` is *additive*. Sub-scoping saves no context by itself; the
parent document has to be tiered deliberately.

And what this is not: an ephemeral subagent. A spawned session persists, is
addressable, keeps its own transcript, and can be re-entered.

## Sessions have a lifecycle

Today a session is running or it does not exist. When a window closes it vanishes
from the report, and with it every trace that the project was ever there. That is
the missing OS concept, and it blocks the thing it was raised for: **closing
sessions to preserve resources.**

The pieces are half-present. `/api/folders` already merges the boot list,
transcript history and live windows, flagging each folder `open` or not — that is
a deactivated state in all but name. But `ResolveSession` lists **live sessions
only**, so mail addressed to a project whose session is closed bounces — even
though it is a real project with history that could be started. Several projects
on a working machine are in exactly that position at any time.

**A project is known because it carries the self-description file.** The one-line
file described below for routing also marks a directory as a project. A folder
carrying it is addressable whether or not it has ever run — so a brand-new
project is routable before its first session, and the agent already reads it
while enumerating folders.

**Exiting deactivates until something re-activates.** The cron watchdog runs
`shabadoo boot` every ten minutes, so today a boot-listed session you exit comes
straight back within ten minutes — which defeats closing it to save resources. An
exit records intent, and the watchdog honours it.

*Accepted cost, worth stating plainly:* a vanished window and a crashed window
look identical, and this design does not distinguish them (the alternative,
tmux's `remain-on-exit`, was considered and not taken). So **the watchdog stops
healing crashes too** — a session that dies stays down until something needs it.
That is coherent with preserving resources rather than a hole: nothing nobody
needs gets restarted, and work arriving is what brings it back.

### Waking a stopped session

Mail for a stopped project **starts it** rather than waiting for someone to
notice. That is what the premise promises: hand work to the domain expert and it
gets picked up.

> **This is a reconciliation, not an instruction.** Two decisions conflict read
> literally — mail auto-starts a stopped session, *and* only humans and the core
> session may start anything. The only way both hold is that the **core session
> is the actor**: mail for a stopped project reaches that node's core session,
> which starts the target and lets the message deliver. Automatic from the
> sender's point of view, with exactly one permitted actor on each machine, and
> the judgment about whether a wake is warranted lands where judgment belongs.
> If auto-start was meant to bypass the core session, this needs revisiting.

**Only humans and the node's core session start sessions.** A peer may ask; it
cannot spawn. One confused or looping session must not be able to start work
across every machine, and each start costs real resources.

## Token efficiency is an architectural constraint

In a hive, **context is the scarce resource.** Efficiency means not loading
context into the wrong brain.

- **Two tiers, not one document.** A routing card is a different artifact from
  the working document needed to do the job.
- **The live registry beats a written one.** `/agent/peers` already returns every
  session with project, self-declared status and pending mail. It cannot go stale.
- **The anti-pattern to resist:** every project's `CLAUDE.md` growing to know
  about every other project.

**Each project describes itself in one line, in a small file the agent reads.**
The agent already enumerates folders for `/api/folders` and reads the description
in the same pass. Two properties a central list cannot have: the description is
owned by the project it describes, so it cannot drift out of sync; and a project
with no running session is still routable — which is exactly when routing
matters. The node's main project becomes a *router over live data* rather than a
maintained catalogue.

## The three primitives that are missing

**1. `kind` on a session — `claude` | `worker` | `core`.**
The session table silently claims every tmux window is a Claude session, and
`ResolveSession` only passes through `claude-`-prefixed ids, so a non-Claude tool
cannot be addressed at all. It is also the honest fix for a real inaccuracy: the
blocked-session watcher applies Claude-shaped heuristics to whatever is on screen.

**2. A capability manifest per node, reported by the core session.**
`audio.capture`, `gpu`, `ios.build`, `llm.local`, `always-on`. The core session
reads its own `CLAUDE.md` and reports upward, the way `session_status_set`
reports status — so the agent never parses prose.

**Capabilities persist while the agent is connected and clear when it
disconnects.** They are facts about a machine, not claims about work: a node does
not lose its microphone because its core session got busy. Deliberately unlike
`session_status`, which ages out after 30 minutes because it *is* a claim about
work in progress. Accepted cost: a node reports nothing until its core session
starts, so the hub must distinguish "not reported yet" from "none".

**3. Wire the `tasks` table.**
Delegation today is mail: acknowledged when drained, and then nothing — no
accepted / in progress / blocked / done, no way to ask what was handed off and
never came back. A hive with only messages is a group chat. It is also what makes
parent→child spawning legible rather than a pane appearing from nowhere.

**Workers authenticate by file permissions.** The local socket is 0600 in the
operator's own directory: "can open this socket" means "is already this user",
who could read the agent key anyway. No new credential, no enrolment. A decision,
not an accident.

## Worked example: the meeting recorder

The example that produced this document, instructive because the first design was
wrong. The instinct was a tray app for three operating systems talking to
shabadoo. In this model it is not an app: it is a **worker registering
`audio.capture`** on the nodes that have it. The core session drives it; the
judgment call — which project do these notes belong to — is made by a session;
delivery uses the messaging plane, which already allowlists `/message/send` and
`/notify` on the local socket.

Platform facts, hard-won and worth keeping regardless of design:

| Platform | Microphone | System audio | Status |
|---|---|---|---|
| Windows | WASAPI capture | **WASAPI loopback** on the default render endpoint | needs a Windows-side process — a WSL node cannot see Windows audio |
| macOS | avfoundation | none without a helper | ScreenCaptureKit or CoreAudio process taps; a virtual device (BlackHole/Loopback) avoids native code at the cost of a prerequisite |
| Linux (real desktop) | pulse source | `<sink>.monitor` | works with ffmpeg alone |
| WSL | `RDPSource` works | **trap** — `RDPSink.monitor` carries only audio from Linux apps inside WSL | a meeting in Teams/Zoom/a browser never touches it; must refuse rather than record silence |

**Record two tracks, never one mix.** Separate mic and system tracks make speaker
attribution free — your track is you, the other is everyone else — and that is
worth more than any diarization model. Mixing is irreversible.

### The shape: a native capture helper per OS, one orchestrator

Each platform contributes a small helper that does nothing but capture and write
**raw PCM to stdout**. One cross-platform orchestrator runs the helper, segments
the stream, transcribes, summarises and delivers. Linux's "helper" is just
ffmpeg; Windows and macOS get native ones.

**Windows uses system-wide WASAPI loopback**, not a virtual cable. Routing audio
through VB-Cable or Voicemeeter works, but it changes the machine's audio setup
and risks the failure where the recording succeeds and the human stops hearing
the meeting. Loopback on the default render endpoint captures what is playing
while leaving playback untouched. Accepted cost: it captures *everything* —
notification sounds and music land on the meeting track. Process-specific
loopback (Windows 10 2004+, `ActivateAudioInterfaceAsync` with
`AUDIOCLIENT_ACTIVATION_PARAMS`) records only the meeting application and is the
obvious refinement.

It is deferred for sequencing, not capability: every Windows SDK on the target
machine ships `audioclientactivationparams.h`, so nothing blocks it. The reason
to start system-wide is that it always works and needs no process discovery,
and mis-targeting a process records silence — a failure you discover after the
meeting.

**The helper is C++ built with MSVC, driven from the Linux side through
interop.** The alternative was mingw-w64, cross-compiling from Linux with no
Microsoft toolchain at all — attractive until you notice what it risks: mingw's
Windows SDK headers are community-maintained and lag, and the process-loopback
declarations are exactly the kind of newer API that goes missing. The official
SDK removes that whole class of question.

It costs nothing in build ergonomics, which was mingw's real appeal. A WSL
process can execute `cl.exe` directly, and the drive mount means a Linux path
and a Windows path name the same file, so the build stays one command from the
Linux side rather than a trip to another machine. Measured, not assumed: the
compiler was invoked through interop and reported its version.

**The transport is WSL–Windows interop, and it was measured rather than
assumed.** A WSL process execs the Windows `.exe` directly and reads its stdout;
all 256 byte values survive the pipe intact, with no CRLF translation. That
matters more than it looks: it means no TCP listener, no named pipe, no shared
filesystem, and — critically — **no new credential**. The orchestrator stays a
Linux process, so it keeps authenticating to the agent socket by file
permissions, exactly as decided above. A Windows-side orchestrator would have
needed a device token and broken that rule.

**Take both tracks from the same side.** Capturing the microphone through
Windows as well as the loopback keeps both streams in one clock domain. Two
independently started capture streams drift, and drift is invisible until you try
to align a ninety-minute transcript — so a shared time reference across the two
tracks is a requirement, not a refinement.

## What this does not change

- Projects stay directories. No registry, no new identity system.
- The messaging plane, the auth model, the audit log and the agent protocol keep
  their shape. Pane addressing extends the addressing tuple; it does not alter
  how agents connect or how humans authenticate.
- Nothing loosens the safety properties: reaching the coordinator still means
  driving every pane, and the answer is still scoped credentials rather than
  trusting the network.

## Still open

- **Whether auto-start should route through the core session** — the
  reconciliation above, flagged for confirmation.
- **Where deactivation state lives.** The pattern for per-host state is a small
  file outside the repo, edited surgically (`~/.config/claude/env`, the boot
  folder list). Deactivation belongs there, and every path that opens a window
  must honour it or the state is decorative.
- **What each node's main project is, concretely.** Every node needs one; naming
  them consistently matters more than the names.
- **Keeping the core session cheap.** Always-on plus unbounded context is a
  problem no mechanism here solves; it needs a working discipline.
- **Clock drift between the two tracks.** Two capture streams started
  independently drift apart; aligning a long transcript needs a shared time
  reference. Named here because it is invisible until the first long meeting.
- **The macOS helper is undesigned.** Native helpers being acceptable means
  ScreenCaptureKit or CoreAudio taps can remove the virtual-device prerequisite,
  but that is a Swift component and nothing has been decided about it.
- **Detection could complement declaration.** Capabilities are declared; some are
  also detectable (ffmpeg present, audio devices, GPU), and detected facts cannot
  go stale.
