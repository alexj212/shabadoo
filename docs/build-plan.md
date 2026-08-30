# Build plan

`docs/direction.md` says what this becomes and why, and should stay stable. This
says **in what order, and why that order** — and it goes stale as things ship,
which is the difference between the two.

Every phase is independently useful and independently abandonable: stopping
after any one of them leaves a system that works.

## Where this stands

**Phases 0–7 are shipped and deployed** — v0.4.47 on the coordinator and both
nodes. The plan they described is finished, and the sections that walked through
each one have been deleted rather than ticked: what runs is documented in
`CLAUDE.md`, and a plan that still describes shipped work as work is how a reader
loses track of which half is which.

| Phase | | Shipped |
|---|---|---|
| **0** | the window diff | v0.2.0 |
| **1** | `kind`, and projects that describe themselves | v0.2.0 |
| **2** | the node's main project and its core session | v0.2.0 |
| **3** | lifecycle, deferred delivery, the loop guard | v0.3.0 |
| **4** | tasks, and a reaper | v0.4.x |
| **5** | protocol negotiation | v0.4.x |
| **6** | pane addressing | v0.4.x |
| **7** | token accounting | v0.4.x |

**Three things came out differently from the plan**, kept because each records a
decision that would otherwise be re-made:

- Phase 2 was to hang the core-session restart on the window diff. It asks
  whether the session is running *now* instead — "always running" is a state, and
  asking the state also catches an agent that starts while the session is already
  down, and a relaunch that silently failed.
- Phase 3's loop guard was to be message provenance. There is no mechanical
  causal link between a message received and one later sent, so only the sender
  could supply it — and a guard that depends on the sender to declare itself is
  not a guard. It is a rate limit.
- Capabilities were to be reported upward by the core session. Once Phase 1 gave
  the agent a structured frontmatter parser, reading the file directly removed an
  endpoint, a storage decision and a lifecycle rule.

### What shipped that this plan never mentioned

Roughly as much again, and listing it matters: the plan is the record of what was
*intended*, so work that arrived from use rather than from design is invisible
here otherwise — and it is the better evidence of what this system is actually
for.

| | Why it happened |
|---|---|
| `MISSION.md` — a project says what it is *doing* | Sessions were already inventing one, separately, in different shapes |
| the fleet view, grouped by who is blocked | A list sorted by subject makes every reader scan all of it |
| blocker ages, and a closed-with-median trend | A snapshot cannot answer "is this getting better" |
| `shaba todo` / `shabadoo mission` | The terminal had no answer to a question the dashboard had rendered for weeks |
| tool distribution (`--tool`) | Every other tool on the fleet was installed by hand |
| node self-install of its own payload | An upgrade left the guidance beside it stale, indefinitely |
| `--ci-repo`, stuck-mail and blocked-session watchers | Three failures that were silent by construction |
| the nudge composer guard | The one write nobody consents to at the moment it happens |

**The pattern is worth naming, because it should drive what comes next:** almost
none of it came from review. It came from *using* the system and from peer
sessions reporting what they hit — the scoped-mission defect, the empty message,
the broadcast that reached zero, the `is-active` misreading, the wrapped line.
The plan below is therefore deliberately short on speculative structure and long
on things somebody has already asked for.

---

## The ordering principle, restated

Unchanged, and it still decides the order below: **cost of discovering something
late**, not dependencies alone. Ship a guard with the thing it guards; ship a
protocol change before the protocol; do the small foundation first when two
features stand on it.

One addition earned since: **prefer what a person has already asked for twice.**
Every item in Phases 8–10 was requested or reported, and the one speculative
phase is marked as such.

---

## Phase 8 — the papercuts, in one batch — **shipped**

`*` on boot-enabled sessions, the dashboard URL in every listing, and `open`
waiting until the coordinator has registered the session. Written up in
`CLAUDE.md`; the one thing worth carrying forward is the verification, because
it is what separates a fix from a longer sleep: **open a folder, address it by
name immediately, and require the send to land.**

## Phase 9 — reading a conversation, on a phone — **shipped**

`GET /api/claude/events`, backward-seeking and byte-cursored, plus a Chat tab in
the dashboard. Written up in `CLAUDE.md` and specified in `docs/mobile-client.md`.

Two things worth carrying forward rather than re-deriving:

- **The reader must count readable TURNS, not lines.** A transcript is mostly
  records nobody sees; the first version returned zero events for `limit=4`
  against a live file because its last four lines were noise.
- **Verify on the phone.** A reader that looks right at 1400px and is unusable at
  390px is the normal outcome of building it on the workstation, and the
  developing machine cannot produce the failing condition.

## Phase 10 — the board — **shipped**

Four read-only columns: Needs you, In flight, Waiting on others, Closed in 7
days. Written up in `CLAUDE.md`. Two things worth carrying forward:

- **Read-only was the right call and is load-bearing**, not caution. A card maps
  to a line in a hand-written `MISSION.md`; a write path would have to preserve
  the prose beside every row, and that is a separate piece of work rather than a
  bolt-on.
- **Verify a page headlessly.** A syntax check passes a call to a helper that
  does not exist; only running the branch catches it.

## Phase 10b — browsing a project's files — **shipped**

`GET /api/files`, rooted at the projects a node already reports. Written up in
`CLAUDE.md` and specified in `docs/mobile-client.md`. One thing to carry forward:
**confine by resolving, never by inspecting the string** — `..` and a symlink out
of the project are the same check, and only one of them is caught by a filter.

## Phase 11 — the ethos as a managed thing

The deepest of the three and the least specified, so it goes last and starts with
a written design rather than code.

shabadoo already carries the guidance — the payload installs `CLAUDE.md` and the
skills onto every machine, and a node reinstalls its own on start. What is missing
is the loop *back*: a session that learns something has to hand it to a human, who
has to edit the payload, in the repo, by hand. The `retro` skill names that
process; nothing mechanises it.

Three pieces, in the order that makes each independently useful:

1. **Show the drift.** A node already reports `payload_pending`, a count. Name the
   files, the way the fleet view now names the projects with no `MISSION.md` — a
   count is something a reader defers, and "which ones" was the first question
   asked of the last count that stood here.
2. **Layer the rules.** Payload → machine (`CLAUDE.local.md`) → project → session
   is the layering that exists in practice and is written down nowhere; making it
   explicit is what lets a project state a rule without forking the global file.
3. **Propose upward.** A session that has *paid* for a finding files it as a
   proposal against the payload, with the evidence attached. Deliberately a
   proposal and not a write: the whole value of the payload is that a human read
   every line of it, and a fleet that edits its own ethos unattended is the one
   failure mode nothing else here guards against.

**Speculative, and marked so.** Only step 1 has a concrete request behind it.
Steps 2 and 3 are worth designing before building, and worth building only if the
retro loop keeps producing findings faster than they get filed.

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

1. **Phase 11** — speculative but for its first step, and the retro loop works
   today with a human in it. Dropping it costs nothing that is currently hurting.
2. **Phase 10b** — the file browser is a convenience; `docs/` is readable on the
   machine that holds it, and every session that needs one is already there.
3. **Phase 10** — the board is assembly over data that is already served three
   other ways. Useful, not load-bearing.

**Phases 8 and 9 are the ones to keep.** Eight is measured in minutes and removes
friction every session pays daily; nine is the only item here that makes the
system usable from the device the notifications arrive on, which is the gap
between a dashboard you check and one you act from.

## What could make this plan wrong

- **The board may be the wrong shape entirely.** It is the one item whose
  requester said "let's discuss", and building it before that conversation is how
  a week goes into a layout nobody wanted. The decision is recorded and open.
- **Rendering conversations may not be worth its read surface.** If the honest
  answer is that a phone should show *what is being asked* and not the whole
  transcript, then Phase 9 shrinks to the dialog prompt it already has — and that
  is a smaller, safer system rather than a failure.
- **The ethos loop may not need mechanising.** Findings are currently filed by a
  human who reads them, and that human is the guard. Automating the path from
  finding to payload removes the one review step protecting a file every machine
  on the fleet obeys.
- **The always-on core session may still prove too expensive**, in tokens or
  drift. No mechanism here fixes that; it is a working discipline, and if the
  discipline fails the design needs revisiting rather than patching.
