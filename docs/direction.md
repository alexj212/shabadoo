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

**Each project describes itself in one line — as frontmatter on the `CLAUDE.md`
that already marks it as a project.** There is no new file, and that is the
point: the project root rule is *the nearest `CLAUDE.md` that is a git root*, so
`CLAUDE.md` is already the marker. Putting the description there makes **"is a
project" and "can be routed to" the same fact** rather than two facts that can
disagree.

The agent reads it during the folder enumeration it already performs — a
structured block, not prose, so the objection that kept the capability manifest
out of `CLAUDE.md` does not apply here. It is committed, so it travels: a fresh
clone is routable immediately.

The two tiers fall out without being designed: **frontmatter is tier 1**, cheap
enough that a router can hold every project at once, and **the body is tier 2**,
loaded only by sessions actually working in that project.

*Considered and rejected:* a dedicated dotfile — a fourth hand-edited format,
marking something already marked. And a per-project **skill**, whose frontmatter
is genuinely the right shape but whose body would be a second document
describing how to work in a project that already has one, which is the drift
this codebase keeps legislating against. A skill would win if sessions were
expected to load each other's manuals; they are not. They talk instead.

*Accepted cost:* a committed description cannot differ per machine. That was the
argument for keeping it local, and it is real but uncommon — a project's purpose
does not usually change with the checkout.

### The description is trigger text, not a summary

If the working mode is sessions talking to each other, then **the quality of the
description is the quality of the routing.** A vague line does not fail
loudly — it delivers work to the wrong expert, who does it worse and slower than
the right one.

So it should be written the way a skill's `description` is written, because that
field solves the identical problem: *when should this be reached for*, phrased
for the reader deciding, not for someone browsing a catalogue. "Use for X, Y,
Z" rather than "this project is about X".

And brevity is a constraint rather than a preference. A router holds every
description at once; that is the whole reason this is separate from the body.
One line, with a real limit, not a paragraph that happens to be short today.

## The three primitives that are missing

**1. `kind` on a session — `claude` | `worker` | `core`.**
The session table silently claims every tmux window is a Claude session, and
`ResolveSession` only passes through `claude-`-prefixed ids, so a non-Claude tool
cannot be addressed at all. It is also the honest fix for a real inaccuracy: the
blocked-session watcher applies Claude-shaped heuristics to whatever is on screen.

**2. A capability manifest per node, read from its project's frontmatter.**
Two halves answering different questions. The agent **detects** what is
checkable — toolchains, a GPU, the platform — from a curated vocabulary, not an
inventory of everything installed: asking a package manager returns thousands of
entries and answers none of the questions a router asks. The node's project
**declares** what no probe can establish: always on, in the meeting room, the
build host.

**Detection wins where they disagree about something checkable.** A declared
`ffmpeg` on a machine without ffmpeg is not an opinion, it is wrong, and the
cost of believing it lands after a handoff — on another machine, where it is
most expensive to diagnose. Anything outside the detectable vocabulary is taken
at its word, because there is nothing to check it against.

*This reverses an earlier decision, deliberately.* The plan was for the core
session to report capabilities upward, chosen when the agent had no structured
parser and the alternative was reading prose. Once the project description moved
into `CLAUDE.md` frontmatter the agent had one, and reading the file directly
removes an endpoint, removes a lifecycle rule, and removes the cost that plan
accepted: a node that can say nothing about itself until its core session has
started.

Presence only, no versions: "can this node build Go" is the decision a router
makes, and "which Go" is something the session that receives the work can
establish correctly at the moment it matters.

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

**Select a toolchain by completeness, not by version.** Found while building the
recorder against this: an installation can have `vcvars64.bat` and lack the
`vcvarsall.bat` it calls. "Newest wins" then chooses one that cannot compile and
fails a level down, naming the wrong file — an afternoon lost to a message that
points somewhere else. Probe for both.

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

## Rejected

**A default `fleet` topic.** Broadcast reaches nobody, because nothing
subscribes to anything. The proposal was one auto-subscribed topic carrying
substrate changes — "the tool surface you are holding moved" — on the grounds
that a session cannot discover that alone.

Not built, and the reasoning is worth keeping so it is not re-proposed.
Subscribing every session to a channel makes broadcast a wake-and-interrupt
amplifier: each message costs every session a turn, most of which will do
nothing with it, and a channel that mostly wastes turns gets ignored exactly as
a notifier that cries wolf gets muted.

The one case that motivated it is now covered without a channel: a session's
stale tool surface is reported as `tools_stale` and visible through
`session_list`, so the fact is reachable by anything that looks. Directed mail
carries the things that need doing; nothing needed a broadcast.

## Still open

Nothing structural. Every question raised while writing this was put to the
operator and answered, and the answers are in the text above with the costs they
carry.

What remains is the work itself — none of which is started, and none of which
should begin without re-reading the decision it depends on.
