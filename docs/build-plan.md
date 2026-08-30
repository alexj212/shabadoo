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

## Phase 8 — the papercuts, in one batch

Small, unrelated, and each one costs somebody a few seconds every day. Batched
because individually none justifies a release and collectively they are an
afternoon.

- **`session list` marks boot-enabled folders.** A `*` for a session whose folder
  is in the boot list, so "will this come back if I close it" is answerable from
  the listing rather than from a second file. `boot list` already computes the
  set; nothing new is needed but the join.
- **Every listing prints the dashboard URL.** The coordinator address is in
  `~/.config/shabadoo/coord` and reachable from the phone; a session that wants
  to point a human at a pane currently has to be told the URL by the human.
- **`shabadoo open` waits until the session is registered.** Three sessions
  independently wrote the same poll loop, which is the definition of a missing
  primitive. `open` returns as soon as tmux has the window, before the
  coordinator has seen it, so the next call by name fails.
- **A deactivated folder that is also in the boot list is a contradiction**, and
  `boot`, `boot list` and `boot add` now say so — done in v0.4.47. Kept here as
  the reason the batch exists: it cost seventeen sessions that did not restart.

**Verify:** open a folder, immediately address it by name from another session,
and require the send to land. That is the failure the poll loops were papering
over, and it is the only assertion that distinguishes a fix from a longer sleep.

## Phase 9 — reading a conversation, on a phone

**Promoted ahead of the board, because it is the one thing the mobile client
cannot do at all.** The dashboard can drive every pane from a phone and can
barely show you what any of them said: `/api/capture` returns whatever is still
in tmux scrollback as flat text, wrapped for a terminal, with tool output at the
same weight as the answer. On a 390px screen that is unreadable, and it is what a
person actually reaches for when a notification says a session is blocked.

The reader underneath already exists and already returns a byte cursor.
`claudelog` parses the transcripts, caches incrementally — an unchanged one costs
a stat — and `GET /api/claude/session` serves the summary. What is missing is the
turns.

**`GET /api/claude/events`** — cursor-paginated user/assistant turns, tool calls
collapsed by default. Four constraints are already known and each one shapes the
API rather than the CSS:

- **`proxyGet` buffers a peer response through `io.ReadAll` with an 8 MB cap.**
  So the endpoint must paginate hard and truncate individual tool results *on the
  wire*, not in the client. A single pasted file can exceed the cap on its own.
- **Poll must APPEND using the cursor**, never rewrite `innerHTML`. Rewriting
  destroys scroll position every few seconds, which on a phone means the page
  fights the reader — the failure is not subtle and it is not fixable in CSS.
- **Newest-last, anchored to the bottom**, like every chat a person has used. The
  summary header already exists and belongs above it, collapsed.
- **Tool calls collapse to one line and expand on tap.** They are the bulk of the
  bytes and almost never the thing being read. Collapsed-by-default is also what
  keeps a page within the transfer budget on a phone connection.

**Read `github.com/osteele/claude-chat-viewer` before designing the rendering.**
It solves the same problem from the same transcript format, and the parts worth
taking are its message-shape handling, not its stack.

**What must not be lost in making it pretty:** this widens the read surface
knowingly. The transcript store holds file contents, memory directories, and
anything ever pasted into a prompt, for every session ever run in that folder,
indefinitely. Rendering it well makes that content easier to read — which is the
point, and also the risk. It is acceptable on the current tailnet-only,
device-token basis, and it is the strongest argument for `pair --scope read`
meaning something narrower before this reaches a phone that leaves the house.

**Verify on the phone, not on the workstation.** A conversation reader that looks
right at 1400px and is unusable at 390px is the normal outcome of building it
here, and the developing machine cannot produce the failing condition — the same
rule this project already applies to platform-specific code.

## Phase 10 — the board: inbox, todo, blockers, done

The largest thing asked for, and mostly **assembly rather than new mechanism** —
every input already exists and is already served:

| Column | Source | State |
|---|---|---|
| inbox, including past | `GET /api/messages` (24h + per-session thread) | built |
| todo / open | `mission_waiting` on every session | built |
| blockers | the same rows, owner `you`, plus blocked tasks | built |
| done | `GET /api/missions/resolved` + tasks in `done`/`dropped` | built |

So this is a **rendering decision, not a data one**, which is why it is worth
doing before anything that adds a new store. The shape is an open question — see
the decision below — and it should be settled before the first column is drawn,
because a board and a set of grouped tables want different data on the wire.

**The one real risk** is that a board invites moving cards, and moving a card is a
write into somebody's `MISSION.md`. That is a file a human edits and reviews in a
diff; a UI that rewrites it from a parsed model would delete every reason written
beside every row — the failure `shabadoo config` and `boot add` already refuse to
commit. **Read-only first.** Whether a card can ever be dragged is a separate
decision, taken after the read view has been used.

## Phase 10b — browsing a project's files

Asked for to read doc libraries — the `docs/` trees several projects now carry,
reachable today only by being on the machine. It rides Phase 9's read surface, so
it is cheap once that exists and pointless to design separately.

**Root it at project directories the node already reports**, rather than at the
filesystem. A file browser over an agent's node is otherwise arbitrary file read
on that machine, bounded by whatever the handler decides; a project root is
already a first-class concept here, and rooting the browser at one turns an
unbounded capability into an enumerable one. That is the recommendation and it is
also the cheaper build.

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
