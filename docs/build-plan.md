# Build plan

`docs/direction.md` says what this becomes and why, and should stay stable. This
says **in what order, and why that order** — and it goes stale as things ship,
which is the difference between the two.

Every phase is independently useful and independently abandonable: stopping
after any one of them leaves a system that works.

## Where this stands

| Phase | | Status |
|---|---|---|
| **0** | the window diff | **shipped** — v0.2.0 |
| **1** | `kind`, and projects that describe themselves | **shipped** — v0.2.0 |
| **2** | the node's main project and its core session | **shipped** — v0.2.0 |
| **3** | lifecycle, deferred delivery, the loop guard | **shipped** — v0.3.0 |
| **4** | tasks, and a reaper | **built** — awaiting deploy |
| **5** | protocol negotiation | **built** — awaiting deploy |
| **6** | pane addressing | **built** — awaiting deploy |
| **7** | token accounting | **built** — awaiting deploy |

Every phase is written. 0-3 are running on both nodes and the coordinator, with
a core session live on each machine; 4-7 are committed and not yet deployed.

Two of the later phases turned out smaller than planned, both because work
already done was doing more than the plan credited it with. Phase 7 was billed
as "the caching is the real work" — `claudelog` already caches incrementally, so
an unchanged transcript costs a stat and a grown one costs only its new lines.
Measured on eleven live sessions: a cold report 1.57s, a warm one 122ms.

**Three things came out differently from the plan**, and each is written up in
its phase below rather than quietly corrected:

- Phase 2 was to hang the core-session restart on the window diff. It asks
  whether the session is running *now* instead — "always running" is a state,
  and asking the state also catches an agent that starts while the session is
  already down, and a relaunch that silently failed.
- Phase 3's loop guard was to be message provenance. There is no mechanical
  causal link between a message received and one later sent, so only the sender
  could supply it — and a guard that depends on the sender to declare itself is
  not a guard. It is a rate limit.
- Capabilities were to be reported upward by the core session. Once Phase 1 gave
  the agent a structured frontmatter parser, reading the file directly removed
  an endpoint, a storage decision and a lifecycle rule.

**Two defects arrived from the field rather than from tests**, both fixed: an
empty message was accepted, stored and delivered while looking successful at
every layer, and a notification tag that matched no destination lost the
notification rather than falling back.

## The ordering principle

Order is driven by **what it costs to discover something late**, not by
dependencies alone. Three forces, and they do not always agree:

- **Ship together or not at all.** A guard that arrives after the thing it
  guards has already been dangerous in production. Auto-start and loop
  protection are one change, not two.
- **Ship before, never retrofit.** Protocol negotiation added *after* the
  protocol changed means supporting a version that should never have existed.
- **Foundations first, when they are small.** One piece of work underpins two
  separate features and takes an afternoon. That goes first regardless of which
  feature is more interesting.

---

## Phase 0 — the window diff

**Small, and two later phases stand on it.** The agent compares its view of the
windows between reports: a window that was present and is now gone is an event,
not just an absence.

Two consumers, from one observation:

- a session that vanished is **deactivated** (Phase 3);
- unless it is the **core session**, which is restarted instead (Phase 2).

The reason to do it first is that both features need it, it needs nothing
itself, and it changes no wire format.

**The hard part is not the diff.** A tmux server restart or a reboot makes every
window vanish at once, and that must not deactivate the entire machine. The
agent has to tell *a lost window* from *a lost tmux* before it writes anything
down. Get this wrong and a reboot silently disables every session on the host.

**Verify:** close one window, confirm one event. Restart the tmux server,
confirm none. Both against a fixture session, never a real one — this repository
has twice been bitten by tests that named a live target.

---

## Phase 1 — `kind`, and the self-description file

Two small additions that unblock everything else.

**`kind` on a session — `claude` | `worker` | `core`.** Today the session table
claims every tmux window is a Claude session; `top` in a window reports itself as
a project. Until a non-Claude tool can register *as itself*, workers cannot be
first-class, and `ResolveSession` only passes through `claude-`-prefixed ids so
they cannot even be addressed.

**The self-description.** Frontmatter on the project's existing `CLAUDE.md`,
read by the agent during the folder enumeration it already performs. No new
file: `CLAUDE.md` is already what marks a project root, so this makes "is a
project" and "can be routed to" the same fact. Committed, so a fresh clone is
routable immediately.

Write it as **trigger text** — when should this be reached for — not as a
summary, and keep it to one line with an enforced limit. A router holds every
description at once, and a vague line does not fail loudly: it delivers work to
the wrong expert.

Both changes are additive fields, so no protocol risk.

**Verify:** a folder with a description and no session appears as routable. A
non-Claude window reports `kind: worker` and stops claiming to be a project. A
`CLAUDE.md` with no frontmatter, or malformed frontmatter, leaves the project
undescribed rather than breaking enumeration — every other folder still reports.

---

## Phase 2 — the node's main project and its core session

`<shabadoo-dir>/<host label>/`, named for the host, holding that machine's
`CLAUDE.md`, memory and capability declaration. The agent derives the path from
state directory plus host label — no discovery, nothing to configure.

- It runs in a tmux window like anything else, in the boot list, always on.
- **The agent restarts it** when the Phase 0 diff says it vanished — one report
  cycle, not the ten-minute watchdog — **with backoff**, or a core session that
  fails immediately turns its own supervisor into the outage.
- **`uninstall --purge` must skip it.** Purge removes the state directory and
  warns about tokens and the audit log; a node's `CLAUDE.md` and memory are
  neither. Same rule as the env file: not owned on the way in, not deleted on
  the way out.
- Window names are `<basename>-<host>`, which renders this project `wsl-wsl`.
  When a project's name already is the host label, do not say it twice.

**Capabilities** land here too: declared by the core session (the way status is
declared today), detected by the agent where detectable, detection winning on
conflict. They persist while the agent is connected rather than ageing out —
a node does not lose its microphone because a session got busy.

**Verify:** kill the core session; it returns within a report cycle. Break it
deliberately; confirm backoff rather than a five-second relaunch loop. Run
`uninstall --purge --dry-run` and confirm the project is reported as kept.

---

## Phase 3 — lifecycle, auto-start, and the loop guard **in one change**

This is the phase with a hazard in it, and the guard is not a follow-up.

**What ships:**

- Deactivation is recorded and **honoured by every path that opens a window** —
  `boot`, the watchdog, `open`. A state nobody consults is decorative.
- `ResolveSession` learns about projects that are known but stopped, so mail to
  a closed project stops bouncing.
- Mail for a stopped project routes to that node's **core session**, which
  starts the target and lets the message deliver.

**And in the same commit, because this is where mail starts causing code to
run:**

- **Provenance on the envelope** — a hop chain, refused past a small limit, and
  **audited on refusal** exactly as `message.bounce` already is. Without it,
  A→B→A is unbounded, and the prior implementation's recursion guard was deleted
  as dead code when agents began dialling out. The hazard did not go away; it
  changed shape.
- **A per-node start budget**, so auto-start cannot spawn without limit.

Until this phase, mail is passive. After it, an inbound message can launch a
session running with permissions disabled. That is the single largest change in
blast radius in the whole plan, and it deserves to be the most carefully tested.

**Verify:** a message addressed in a cycle stops and is audited. A stopped
project receives mail and wakes. A deactivated session survives a watchdog run.
The budget refuses rather than starts, and says so.

---

## Phase 4 — tasks, and a reaper

Wire the `tasks` table that has been in the schema from the beginning with
nothing reading or writing it: accepted, in progress, blocked, done.

**Ship the staleness watcher with it**, or the table is a write-only log and
"what did I delegate that never came back" stays unanswerable. `hub/blocked.go`
is the exact shape to reuse — edge-triggered, a grace period, one reminder an
hour, per-key state, pinned by tests because every mistake in edge detection
looks the same from one report.

**Verify:** a task left in progress notifies once, then hourly, and stops when
it moves. Its tests should be the blocked-watcher tests with the nouns changed.

---

## Phase 5 — protocol negotiation

**Before Phase 6, never after.** Today the only thing exchanged at login is a
build stamp; there is no version or capability negotiation. `upgrade --all` is
deliberately serial, so **mixed versions are guaranteed during every upgrade**.

Minimal form: the node reports a protocol level at login, and the coordinator
**refuses** operations a node cannot support rather than degrading silently. A
refusal is diagnosable; a silent degrade in Phase 6 means a keystroke landing in
the wrong pane, which is the exact failure Phase 6 exists to fix.

**Verify:** an old node and a new coordinator produce a clear refusal, not a
misdirected write.

---

## Phase 6 — pane addressing

The largest single piece of work in the plan. `{node, session, window}` becomes
`{node, session, window, pane}` across the wire protocol, every op in
`handleOp`, `reportSessions`, `input_state`, and the dashboard's row model.

- **Pane 0 keeps the session id it has today.** Ids are how mail is addressed;
  renaming them all at once orphans every undrained handoff.
- It fixes a live hazard: `target()` is `session:window`, which tmux resolves to
  whichever pane is *active*, so a multi-pane window already accepts writes
  aimed at the wrong pane.
- The dashboard's flat list becomes a tree.

**This phase is optional in a way the others are not.** It is what makes a
spawned child addressable — able to receive mail and hold a task. If sub-project
spawning turns out not to be used, this can be deferred indefinitely without
blocking anything else in the plan.

**Verify:** two panes in one window receive distinct sends. Existing single-pane
sessions keep their ids and their mail.

---

## Phase 7 — token accounting

Independent of everything above; could run at any point.

The direction document calls context the scarce resource and then measures none
of it at fleet level. `claudelog` **already parses** input, output, cache-read
and cache-write token counts and sums them per session — it is served on demand
per session and shown in the detail panel, but is absent from the session list.
So nothing aggregates across the hive, nothing notices the session that spent
two million tokens overnight, and the router cannot weigh cost.

**The plumbing is trivial; the caching is the work.** Re-reading transcripts on
every five-second report is not affordable, so this needs a cache keyed on file
modification time. Do not start this believing it is a small change.

**Verify:** totals in the session list match the detail panel. Report latency is
unchanged with a warm cache and does not grow with transcript size.

---

## The recorder — a parallel track

Blocked only on Phase 1 (`kind`, so it can register as a worker). Its capture
work depends on nothing here and can start immediately.

- **R1 — Windows capture, and nothing else.** MSVC, system-wide WASAPI loopback,
  **framed timestamped chunks** on stdout rather than raw PCM, driven from the
  Linux side over interop. Prove two non-silent tracks before building anything
  on top. Preflight must **refuse** where system audio is unreachable rather
  than record silence.
- **R2 — orchestrator:** segments, manifest, `start`/`stop`, storage on disk.
- **R3 — transcription**, per track, merged on the shared clock.
- **R4 — summary and delivery** through the local socket into a project's inbox.
- **R5 — macOS**, CoreAudio process taps, same framed-stdout shape.

**The first two are where the risk is.** Transcription and summarisation are
well-trodden; capturing two aligned tracks on someone else's operating system is
not.

---

## If the plan has to shrink

In order of what to drop first:

1. **Phase 6** — large, and only pays off if spawning is actually used.
2. **Phase 7** — genuinely useful, entirely optional, and the caching is real work.
3. **Phase 4** — tasks matter once delegation is common; before that, mail is enough.

**Phases 0–3 are the spine.** They are what turn a session list into an
operating system: sessions that can stop and be restarted, projects that stay
addressable while asleep, and work that arrives on its own. Nothing in Phase 3
should ship without its guard.

## What could make this plan wrong

- **Pane addressing may never be needed.** If sub-project spawning goes unused,
  Phase 6 is a large change bought for nothing. Watch whether anyone actually
  spawns before committing to it.
- **The always-on core session may prove too expensive**, in tokens or in drift.
  No mechanism here fixes that; it is a working discipline, and if the
  discipline fails the design needs revisiting rather than patching.
- **Auto-start may turn out to be the wrong default.** It is the one decision
  that lets a message spend money. If it surprises anyone once, queueing instead
  is a one-line change — and that is worth remembering rather than defending.
