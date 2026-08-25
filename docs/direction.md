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
there, and the only actor permitted to start sessions on it. It runs in a tmux
window like every other session, and it can be given instructions.

No new concept is needed: the thing that knows the machine is a project like any
other, with its own `CLAUDE.md` and memory, loaded only by the session that needs
it.

**It is named for the host and lives in the agent's own state directory** —
`<shabadoo-dir>/<host label>/`. The name matters because addressing the machine
and addressing its supervisor should be the same act: mail to `wsl` reaches the
session that speaks for wsl. The location matters for a duller and better
reason — **the agent already knows that path**. State directory plus host label
derives it with no discovery, no configuration and nothing to keep in sync, and
both halves are things the agent is already holding.

Two consequences follow that would otherwise be found the hard way:

- **`uninstall --purge` must not remove it.** Purge deletes the state directory
  outright, warning about device tokens and the audit log. A node's `CLAUDE.md`
  and memory are neither: they are hand-written knowledge about a machine, in the
  same class as the env file and `~/.claude` — which `setup` scaffolds but never
  overwrites, precisely because it does not own them. Not owning something on the
  way in means not deleting it on the way out.
- **The core session is exempt from deactivation, and the agent restarts it.**
  Every other session may exit and stay down until something needs it. The core
  session may not: it is the only actor permitted to start sessions on its node,
  so one that exited and stayed down would leave that machine unable to start
  anything, recoverable only by walking to it. "Always running" is load-bearing,
  not a preference.

  **The agent restores it, not the ten-minute watchdog.** The agent is already
  running, already reports every five seconds, and already diffs its own view of
  the windows — which is how a deactivation is noticed in the first place. The
  same observation that would record "this session went away" instead means
  "start it again", and recovery is one report cycle rather than up to ten
  minutes of a node that cannot start anything. It has the machinery already; the
  `open` op launches a window today.

  Two things this must not become. It needs **backoff**: a core session that
  fails immediately — a missing binary, a broken config — would otherwise be
  relaunched every five seconds forever, and the supervisor becomes the outage.
  And it is **not** a hole in "only humans and the core session start sessions":
  that rule stops peers spawning work across the hive, whereas this is a machine
  restoring its own supervisor, which is the thing the rule presumes exists.

  The escape hatch is deliberate and coarse: to stop a node's core session, stop
  the node's agent. A machine you are not using should be off, not half-on.

One wrinkle to handle rather than inherit: window names and aliases are built
from a folder's base name plus the host label, which would render this project as
`wsl-wsl`. When a project's name already is the host label, it should not be said
twice.

**A node is a machine that runs sessions, and the coordinator is deliberately
not one.** It stays a single job: it is already the single point of failure for
every node's dashboard, and putting workload on it widens what one bad session
can disturb. The consequence is accepted rather than overlooked — an always-on
machine that would make an excellent capability host is not offering itself to
the hive.

**The discipline that makes it work:** an always-on session is the one that must
stay cheap. The core session routes, decides and starts; it should not do the
work itself, or a context that never ends fills with the details of finished
jobs. *Mechanical facts are data, judgment is a session.*

There is no mechanism proposed for this, and that is the decision: the main
project's `CLAUDE.md` is the routing card and stays small, the core session
delegates rather than works, and ordinary compaction handles the rest. Clearing
it on a timer was considered and rejected — the context it holds is the thing it
exists for, and a scheduled amnesia would discard exactly what makes it useful.

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

**The agent records it, by noticing the window is gone.** It already reports
every five seconds, so it can diff its previous view: a window that was there
and is not is a deactivation. That catches every case including the most natural
one — somebody typing `exit` in the pane — which an explicit-close-only rule
would miss, leaving the watchdog to reopen the session ten minutes later.

*Requirement, not a detail:* a tmux server restart or a reboot makes **every**
window vanish at once, and that must not deactivate the whole machine. The agent
has to tell a lost window from a lost tmux before writing anything down.

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

> **The core session is the actor.** Mail for a stopped project reaches that
> node's core session, which starts the target and lets the message deliver.
> Automatic from the sender's point of view, with exactly one permitted actor on
> each machine, and the judgment about whether a wake is warranted landing where
> judgment belongs. The coordinator deliberately does not start it directly: that
> would let any peer's message spend a node's resources with nothing exercising
> judgment in between.
>
> (This was reconciled from two answers that conflicted read literally, then
> confirmed.)

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

**The file is git-ignored: it is per-machine state, not project content.** That
places it in the same class as `~/.config/claude/env` and the boot folder list —
things this software scaffolds but does not own, holding a decision rather than
content. Ignoring it globally (`~/.config/git/ignore`, which git reads by
default) costs one line per machine and keeps every repository's own
`.gitignore` untouched; per-repo entries would mean editing every project to
support a feature none of them are part of.

Two consequences, and the first is arguably a feature:

- **The same project can describe itself differently on different machines.** A
  checkout that is where the mobile app actually gets built is not the same
  thing as a reference copy elsewhere, and a committed description would force
  them to lie about one of the two.
- **A fresh clone has no description**, so a project is not routable until
  someone writes one. That is real onboarding friction, and it argues for the
  file being written by a command rather than by hand — the surgical,
  one-line-at-a-time treatment `config set` and `boot add` already give to the
  other per-host files, rather than a fourth format for an operator to remember.

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

**Detection and declaration divide along the line already drawn:** the agent
detects what is checkable — platform, whether ffmpeg is present, audio devices,
a GPU — and the core session declares what is not, like "always on" or "this is
the machine that builds the iOS app". Where they disagree about a detectable
fact, detection wins: a declared microphone that is not there is simply wrong,
and a capability nobody can act on is worse than one nobody claimed.

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
| macOS | avfoundation | **CoreAudio process taps** | `AudioHardwareTapping.h` and `CATapDescription.h` are in the SDK; Swift toolchain present |
| Linux (real desktop) | pulse source | `<sink>.monitor` | works with ffmpeg alone |
| WSL | `RDPSource` works | **trap** — `RDPSink.monitor` carries only audio from Linux apps inside WSL | a meeting in Teams/Zoom/a browser never touches it; must refuse rather than record silence |

**Record two tracks, never one mix.** Separate mic and system tracks make speaker
attribution free — your track is you, the other is everyone else — and that is
worth more than any diarization model. Mixing is irreversible.

### The shape: a native capture helper per OS, one orchestrator

Each platform contributes a small helper that does nothing but capture and write
**timestamped PCM to stdout** — a small frame header carrying the capture clock
in front of each chunk of samples. One cross-platform orchestrator runs the helper, segments
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

**macOS uses CoreAudio process taps**, not ScreenCaptureKit. Both can capture
system audio and both are available, but ScreenCaptureKit requires Screen
Recording permission — a far broader grant than sound needs, and a considerably
more alarming prompt for a tool that only wants to hear a meeting. Taps are
audio-only and are the direct analogue of WASAPI loopback, so the two platforms
end up the same shape, including the later refinement to a single process.

**Take both tracks from the same side.** Capturing the microphone through
Windows as well as the loopback keeps both streams in one clock domain. Two
independently started capture streams drift, and drift is invisible until you try
to align a ninety-minute transcript.

**The platform hands us the fix, so this is construction rather than
correction.** `IAudioCaptureClient::GetBuffer` returns a QueryPerformanceCounter
position with every packet, and CoreAudio exposes host time the same way — so
both tracks carry stamps from one system clock, and alignment is arithmetic
rather than cross-correlation. That is why the helper's output is framed instead
of raw: the timestamp has to survive the pipe, and throwing it away at the
capture boundary would make it unrecoverable downstream.

## What this does not change

- Projects stay directories. No registry, no new identity system.
- The messaging plane, the auth model, the audit log and the agent protocol keep
  their shape. Pane addressing extends the addressing tuple; it does not alter
  how agents connect or how humans authenticate.
- Nothing loosens the safety properties: reaching the coordinator still means
  driving every pane, and the answer is still scoped credentials rather than
  trusting the network.

## Still open

Nothing structural. Every question raised while writing this was put to the
operator and answered, and the answers are in the text above with the costs they
carry.

What remains is the work itself — none of which is started, and none of which
should begin without re-reading the decision it depends on.
