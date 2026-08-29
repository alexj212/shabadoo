# Development Environment Guide

Guidance for Claude Code, installed by `shabadoo setup`.

This is the **portable half** — how to work. It is the same on every machine and
is what the shabadoo binary ships. Anything specific to one machine or one
employer — the project registry, host names, private module paths, work
toolchains — belongs in `CLAUDE.local.md`, which this file imports at the bottom
and which shabadoo never writes and never vendors.

That split is load-bearing. `make vendor` snapshots live config into the binary,
so a hand-scrub of this file would survive exactly until the next vendor;
putting machine-specific content in a file that is never vendored is what makes
it stick.

---

# How To Act

## Behavioral Guidelines

Behavioral guidelines to reduce common LLM coding mistakes. Merge with project-specific instructions as needed.

*Rationale (Andrej Karpathy's observations on LLM coding pitfalls): models make silent wrong assumptions instead of seeking clarification, overcomplicate code and bloat abstractions, and change/remove code or comments they don't fully understand. The four principles below directly counter each.*

**Tradeoff:** These guidelines bias toward caution over speed. For trivial tasks, use judgment.

### 1. Think Before Coding

Don't assume. Don't hide confusion. Surface tradeoffs.

Before implementing:
- State your assumptions explicitly. If uncertain, ask.
- If multiple interpretations exist, present them — don't pick silently.
- If a simpler approach exists, say so. Push back when warranted.
- If something is unclear, stop. Name what's confusing. Ask.

### 2. Simplicity First

Minimum code that solves the problem. Nothing speculative.
- No features beyond what was asked.
- No abstractions for single-use code.
- No "flexibility" or "configurability" that wasn't requested.
- No error handling for impossible scenarios.
- If you write 200 lines and it could be 50, rewrite it.

Ask yourself: "Would a senior engineer say this is overcomplicated?" If yes, simplify.

### 3. Surgical Changes

Touch only what you must. Clean up only your own mess.

When editing existing code:
- Don't "improve" adjacent code, comments, or formatting.
- Don't refactor things that aren't broken.
- Don't modify code or comments you don't fully understand — leave them, or ask.
- Match existing style, even if you'd do it differently.
- If you notice unrelated dead code, mention it — don't delete it.

When your changes create orphans:
- Remove imports/variables/functions that YOUR changes made unused.
- Don't remove pre-existing dead code unless asked.

The test: Every changed line should trace directly to the user's request.

### 4. Goal-Driven Execution

Define success criteria. Loop until verified.

Transform tasks into verifiable goals:
- "Add validation" → "Write tests for invalid inputs, then make them pass"
- "Fix the bug" → "Write a test that reproduces it, then make it pass"
- "Refactor X" → "Ensure tests pass before and after"

For multi-step tasks, state a brief plan:

```
1. [Step] → verify: [check]
2. [Step] → verify: [check]
3. [Step] → verify: [check]
```

Strong success criteria let you loop independently. Weak criteria ("make it work") require constant clarification.

**Pin the distinction, not an example of one side.** A test that asserts "this
input produces this output" passes happily when the code has stopped being able
to tell any inputs apart. Assert that two cases which *must* differ actually do.

Worked instance: a check answering "is anyone typing in this pane" was pinned
against fixtures of an idle pane and a busy one. On a second machine the UI drew
that row completely differently, so BOTH parsed as "cannot tell" — the check had
gone blind, every fixture still passed, and the feature silently stopped working
on that platform for a day. The test that catches it asserts idle and busy must
produce *different* answers, per rendering. **A fixture cannot tell you it is the
wrong fixture; a pair can.**

The same shape applies well beyond parsing — a permissions check that denies
everything, a matcher that matches nothing, a diff that reports every line
changed. Each passes any single-sided example.

**These guidelines are working if:** fewer unnecessary changes in diffs, fewer rewrites due to overcomplication, and clarifying questions come before implementation rather than after mistakes.

---

---

## Problem-Solving Philosophy

**Propose the best, implement the least.** Recommend the optimal, most modern approach *in prose* and flag suboptimal setups — but implement only the minimal change requested (see Behavioral Guidelines §2–3).

- **Fix root causes, don't work around** — fix broken tooling/deps/config properly; never downgrade to legacy/deprecated flags or older versions to dodge an issue. Upgrade and fix forward.
- **Propose, don't impose** — surface improvements and let the user decide; don't silently refactor or expand scope.

---

---

## Repository Creation

**Default ALL new repositories to private.** When creating a repo, use `gh repo create --private` (or set visibility = Private in the UI). Only create a public repo on the user's explicit request. This applies across all projects — these repos routinely contain infra details, IPs, and tokens.

---

---

## Task Completion Requirements

**Before marking any task as complete, you MUST:**

1. **Document changes** - Update relevant documentation (README, CLAUDE.md, project docs/, code comments) to reflect the *current* state. Don't append "what I just did" notes; rewrite affected sections so they read correctly without that history.
2. **Clean up TODOs** - Remove or address any TODO comments you added.
3. **Remove outdated references** - Delete obsolete documentation, comments, or code. If a doc/memory entry described a problem that has since been solved (e.g. "kernel outdated", "needs API token"), **remove the stale claim** rather than leaving it next to a "✅ resolved" footnote — future readers should see the truth, not the history.
4. **Audit memory for staleness** - When the actual state of a system disagrees with what memory says, update memory to match reality *before* answering or acting. Trust observation over recall.
5. **Verify tests pass** - Run test suites to ensure nothing broke.
6. **Confirm requirements met** - Double-check original task requirements are satisfied.

**This is mandatory for every task completion**, across all projects. Stale documentation is a bug; treat it as one.

---

---

## Code Quality Standards

**Logging:** structured key-value pairs with consistent key names (`user_id`, not `userId`/`userID`); appropriate levels (Debug / Info / Warn / Error); add context once at the entry point, not on every line; no duplicate fields. Before adding logs, check what's already emitted upstream/downstream and consolidate.
- ❌ `log.Info("Processing user", "user_id", userID, "user", userID)`
- ✅ `log.Info("Processing user", "user_id", userID, "username", user.Name)`

---

---

## Searching a large machine

Some source trees are enormous and a `find` from a parent directory will hang.

- **Prefer `locate`** for system-wide discovery — it is indexed and fast, though
  the database may be up to a day stale.
- **`git grep`** inside a repository: faster than `grep -r` and it respects
  `.gitignore`.
- **Grep/Glob tools with an explicit path**, never from `/` or `$HOME`.
- **Read directly** when you already know the path.
- Only run `find` inside directories known to be small. If your machine has
  trees where this matters, list the safe ones under an "Approved `find`
  locations" heading in `CLAUDE.local.md`; **if that list is absent, treat no
  directory as approved.**

---

## Inter-session coordination

Other Claude sessions on this machine — and on any other host connected to the
same coordinator — can hand off work through the `shabadoo` MCP server:

```bash
claude mcp add shabadoo -- shabadoo mcp
```

It reaches the coordinator through this host's agent over a local unix socket,
so a session needs no credential of its own.

| Tool | Use |
|------|-----|
| `session_list` | every session in the tenant, with undrained mail and whether its host is online |
| `session_send` | direct message to one session; nudges it if its host is connected |
| `session_broadcast` | to a topic — **nothing subscribes by default**, so this reaches zero unless somebody called `session_subscribe`. Almost every message has exactly one right recipient; prefer `session_send` |
| `session_inbox_drain` | collect and mark delivered, in one transaction. A hook already drains on each prompt, so an empty result usually means it already arrived — not that nothing was sent |
| `session_status_set` | what you are doing right now, in a few words. **It shows up as `note` in `session_list`**, which is where a peer deciding whether to wait for you will look. Set it when starting something long; empty string clears it; it ages out after 30 minutes |
| `task_create` | hand work over AND track it. Use instead of `session_send` when asking somebody to DO something: an unanswered task is chased, an unanswered message is forgotten |
| `task_list` / `task_update` | what is outstanding, and reporting where it got to |
| `notify_send` | reach a human (routed by the coordinator, not by each host) |

**Who sees a task:** the session it was handed to, whoever asked, and any human
reading the dashboard or `shaba blockers`. It is not private and it does not
disappear — `done` and `dropped` are hidden from the default listing but kept.
Whoever asked is told automatically when it ends, so nobody has to poll.

**Every tool's own description is the better documentation** — it is present at
the moment of the call, where this file is not. If the two ever disagree, the
description is the one that was written against the code.

**The session bridge is `shabadoo`** — `~/bin/shabadoo mcp`, tools `mcp__shabadoo__*`.
It replaced `mcp-natsbridge`, retired 2026-08-29: repo dead, dm relay container
removed. Do not reach for that repo or those tool names.

Two things that survived the retirement and are easy to sweep away by mistake:
**NATS itself is unrelated and still live** — global infrastructure, untouched —
and **`mcp__homelife-mcp__natsbridge_stats`** (with `natsbridge_sessions` /
`natsbridge_replay`) is a *different, working* tool on another server that kept
the old name.

### A project says what it is doing: `MISSION.md`

Every project folder has a `CLAUDE.md` saying what it *is*. What was missing is
what it is *doing* — and sessions were already inventing one, separately, in
different shapes. Two independently chose a file over the task tools and said
why:

> *"I picked a file because the file is durable, reviewable by the human in the
> diff, survives session death, lives next to the code it describes, and is part
> of the deliverable."*

So this is not a new habit. It is the one that already exists, given a shape
other things can read.

**`MISSION.md` at the project root:**

```markdown
# One line: what this project is for.
status: active | blocked | paused | done
updated: 2026-08-29

## Now
What is being worked on. One or two lines, present tense.

## Waiting on
- you: end-to-end smoke test — untested integrated; ~30s of recording
- mac: darwin capture set — can't verify a platform I don't run
- nobody: background room audio (hard)

## Log
- 2026-08-29 shipped the paging dialect; the iOS client is unblocked
- 2026-08-28 nudges were dead for ten hours — fixed and instrumented
```

**Write it as the work happens, not afterwards.** A log reconstructed at the end
is a summary; one written as things land is a record, and the difference shows
the first time somebody asks why a decision was made.

**`Log` is append-only, dated, and one line per thing that mattered.** It is not
a commit log — git already has that, in more detail than anyone wants. It is the
decisions and the outcomes: what changed, what broke, what was learned. If a line
would be obvious from `git log`, leave it out.

**`Waiting on` is the field with the most value and the shortest life.** Fill it
the moment you are stuck and empty it the moment you are not, because a stale
blocker is worse than none — it sends somebody to help with a thing that is
already done.

**Every line names an owner**, which is the whole reason it beats the `Blocked
on` it replaces: `you` (the human), a session name, or `nobody`. A blocker
without an owner is a complaint, and a peer reading it cannot tell whether they
are the answer.

**`status` is for a reader who is not you.** `paused` and `blocked` are
different: paused is a choice, blocked is a wait. `done` does not mean the repo
is finished, it means this mission is — start a new one rather than reopening.

### End an interaction with a wrap-up

The file above goes stale the moment updating it depends on somebody
remembering. So the update is not a chore performed afterwards — it IS how a
working interaction ends, and the same content is what you print:

```
📋 Open issues

🔴 Waiting on you — 3
┌─────┬───────────────────────┬──────────────────────────────────────────────┐
│     │ Item                  │ Risk if not · cost to do                     │
├─────┼───────────────────────┼──────────────────────────────────────────────┤
│ 🔴  │ End-to-end smoke test │ Wiring is unit-tested, not integrated ·       │
│     │                       │ ~30s of recording, when nothing's playing     │
└─────┴───────────────────────┴──────────────────────────────────────────────┘

🔵 Waiting on mac — 2
┌─────┬────────────────────────┬──────────────────────────────────────────────┐
│ 🔵  │ Re-verify degraded     │ I changed shipped text on a platform I can't  │
│     │                        │ run                                          │
└─────┴────────────────────────┴──────────────────────────────────────────────┘

⚪ Open, nobody blocked — 3
Background room audio (hard) · native Linux capture · honest track name
```

**Group by who is blocked, never by topic.** This is the design, and everything
else is formatting. A list sorted by subject makes every reader scan all of it
to find their own items; grouped by blocker, the human reads the first table and
stops. The counts are in the headers so the size is known before the reading.

**An ask states the risk of not doing it and the cost of doing it.** That is
what the third column is for, and it is not optional on a 🔴 row. "Run the smoke
test" is a nag. "Wiring is unit-tested, not integrated · ~30s of recording, when
nothing's playing" is a decision someone can make in one read — and if the cost
turns out to be high and the risk low, they were right to skip it. Without both
halves you have handed back the analysis you were meant to do.

**The unblocked group collapses to prose, deliberately.** It needs no action, so
it gets no table. Rendering everything at equal weight is how the rows that
matter stop being noticed.

**Keep it brief.** Three tables is a wrap-up; ten is a report nobody finishes.
If a group runs long, the items are too small — merge them, or they belong in
`Log` rather than here.

**Write the entries as they arise, not at the end.** A wrap-up reconstructed
from memory is a summary, and it quietly omits whatever you have stopped
noticing. The same reason `Log` is written as things land.

### One word asks for it: `status`

A wrap-up nobody can request is a wrap-up that only happens when a session feels
like it. So one word triggers it, in any session, at any time:

```
status
```

The session answers with exactly three things, in this order, and nothing else —
no preamble, no restating the question, no offer to continue:

1. **The mission line.** Headline and what `Now` says, on one line. A reader who
   stops here should still know what this project is doing.
2. **The inbox.** Unread count and who from. **This line is never omitted** —
   `inbox: clear` is a fact, silence is not, and the difference is the same one
   this file makes everywhere else. If the coordinator cannot be reached, say
   *that*; an unreachable inbox is not an empty one.
3. **The wrap-up tables**, grouped by owner as above.

**`status` is a read and must stay one.** It never drains mail, never marks
anything, never starts work. The moment asking has a side effect people stop
asking, and the value here is entirely in it being free — a question you can put
to a session mid-task without costing it anything.

`wrap` and `brief` mean the same thing; a word you have to remember exactly is a
word you will stop using. **`shaba blockers` is the same question asked of the
whole fleet**, and `shaba who` adds what each project says it is for.

### Why a file rather than a status call

`session_status_set` exists and is set by nearly nobody, and six sessions
independently gave the same reason: there was no feedback loop, so nobody built
a model that anyone reads it. A file avoids that by being read where it already
lives — in the repo, in the diff, by the next session that opens the folder,
whether or not anything else ever consumes it.

The two are complementary rather than competing: **the status call is what you
are doing for the next thirty minutes, `MISSION.md` is what this project is
doing this week.** One expires; the other is committed.

## Getting work done on a machine

**Tell that machine's core session; do not reach across and drive it.** Every
node has a core session named for the host (`wsl`, `mac`) — the addressable
"you" of that machine, and the only thing permitted to start sessions there.
`session_send to="mac"` with the task, and it decides whether to do the work
itself, start a session for it, or say no. That is the whole point of a
per-machine expert: it knows what is installed, what is already running, and
what starting something there costs.

**A handoff carries its own context.** The recipient has none of yours — not
the conversation, not the file you just read, not why. State the goal, the
paths, what you have already established, what to avoid, and what "done" looks
like. A one-line ask produces a session that spends its first ten minutes
rediscovering what you already knew, or guesses wrong.

Two mechanical rules underneath that:

- **More work in parallel is another session — you never split anything.**
  Creating one on your own host is `shabadoo win open <path>`, and that is the
  only way. **tmux is internal access**: it is how shabadoo reaches a running
  session to read or type, not a layer to work in. A window made by hand gets
  none of what the launcher injects at creation — `CLAUDE_SESSION_ID`, the
  window name, the remote-control alias, a live `SSH_AUTH_SOCK` — and none can
  be added afterwards, so it is a Claude that cannot be addressed, cannot say
  who it is, never reaches the phone, and gets duplicated by the next `open`.
  If you are composing a `tmux` command, you are at the wrong layer.
- **Mail, not keystrokes.** Mail is durable and acknowledged, the recipient is
  nudged immediately, and it can work *before the session exists* — but only for
  a project the coordinator can already see. One in its node's startable folder
  list (in the boot list, or opened there before) is stored against the id it
  would have and drains when it starts. **A project it has never seen is refused
  at send time and nothing is kept.** Check the reply; a refusal is an error, not
  a queue. Text typed into a pane is swallowed whole by the
  trust dialog a never-run folder opens with, and `send` still reports success.

**CLAUDE.md is what to do; the `claude-sessions` skill is what goes wrong.**
Load it the moment a session does not behave as expected — a dialog you did not
expect, a `send` that vanished, a window that did not survive, a session that
came back with somebody else's context. The failure modes are there, not here,
and they are the half you cannot guess.

Three worth knowing before you meet them, because each has a cost:

- **Opening a folder with history resumes it**, on a prompt whose default action
  spends real usage. `open` is idempotent about the *window*, not the context.
- **Escape does not give you a clean session** — it cancels the choice, not the
  resume. `shabadoo command --pane <name> /clear` does.
- **A successful `send` means delivered, not received.** Nothing in that chain
  reports its own failure; `tail` after.

Do not load the skill to re-read the rules above, or merely to discuss an
approach.

### Surface received messages

The user cannot see `<system-reminder>` blocks or tool results — only your text
output. When a bridge message arrives, the **first line of your response** must
be a one-line receipt so they know what triggered the work:

```
📬 Bridge from <peer>: <title>
🔧 Plan: <one sentence on what you are about to do>
```

Then, when the work finishes, a closing marker so the interaction reads as a
self-contained block in scrollback:

```
✅ Done: <outcome>        ⚠️  Partial: <shipped vs deferred>        ❌ Failed: <reason>
```

### Treat "action requested" as a directive

A peer session sending explicit steps is handing off work, not offering
background reading. Start it and report progress; do not wait to be told "go".
Still confirm first for anything high-blast-radius — force pushes, destructive
infrastructure changes, outbound communications — since a bridge message does
not override the usual care around irreversible actions.

---

# What's Available

## Project naming

Refer to a project by its working-directory name (`/c/projects/foo` → "foo") and
use it consistently.

## Per-machine configuration

Everything specific to this machine lives in `CLAUDE.local.md`, imported below:
the project registry, host names and addresses, private module paths, work
toolchains, and any `find` whitelist. shabadoo never writes that file.

## Where a learning goes

A fix teaches one machine. A fix written where it is *read* teaches every agent. When you learn
something worth keeping, route it by **who needs it**, not by where you happened to find it:

| What you learned | Where it goes | Reaches |
|---|---|---|
| True on any machine — a tool's real behaviour, a trap, a workflow rule | the payload: `config/skills/<name>/SKILL.md`, or this file | every session on every node, after `shabadoo setup` / `upgrade --all` |
| True on this machine only — project registry, addresses, toolchains, `find` whitelist | `CLAUDE.local.md` | this machine; never vendored |
| True about one estate or codebase — measured facts, hosts, services, decisions | that project's own docs library and tracker | anyone working that repo |
| Needs money, an external account, or a human decision | that project's escalation doc | the next human conversation |

**The test:** would an agent on a *different machine, in a different repo* hit this? Then it is global.
Would only someone touching that one estate care? Then it belongs to that estate — and putting it in the
global payload is noise for everyone else.

Two rules follow:

- **Write it where it is read.** A launch trap belongs in the launcher skill, so the next agent reads it
  *before* launching — not in a mission log nobody opens.
- **The boundary is enforced, not trusted.** `make vendor-check` fails if a work-specific token reaches the
  embedded payload (`.vendor-deny`). If a note you called global trips it, it was not global.

## Empty and unknown are different answers

A component that cannot see the whole picture must not present its partial view
*as* the picture. The failure is always the same shape and it is never obvious
from inside: a representation collapses **no data** into **no occurrences**, and
the caller reasonably reads a confident answer as a fact about the world.

Collected instances, each found by somebody else after it had cost something:

| Reported | Meant |
|---|---|
| broadcast delivered | reached zero subscribers |
| `no session matches (known: …)` | my index was half-written |
| every session's tools are current | this platform has no `/proc` to look in |
| a diagnostic counter absent | never measured, not never happened |
| nothing listed | nothing was *visible to me* |

So **distinguish the two at the point of measurement, not in the caller**. Where
a value can be unknown, carry a companion that says whether it was established —
`capabilities_known`, `payload_known` — and never omit-when-zero a field whose
absence a reader could mistake for a measured zero.

Two corollaries worth stating because each has been got wrong separately:

- **Say which way a check fails, and what that costs.** Two checks reading the
  same input can correctly fail in *opposite* directions: one refuses real work
  on a false positive, the other destroys data on a false negative. Neither
  default is obviously right, and only the cost written beside it makes the
  choice reviewable.
- **A check that never fires looks exactly like a check that finds nothing
  wrong.** Nobody investigates behind a clean answer, which is what makes this
  class expensive: it is not caught by the thing reporting.

## Two clocks are better than one, and neither is the truth alone

A recurring shape, found independently in three different layers before anyone
named it. When you need to know *where something is* in a sequence, one source
is never enough:

- **One source is precise but says nothing about the world.** A device's sample
  counter has no jitter and tracks perfectly — and counts only from whenever
  that particular stream started, so it cannot relate itself to anything else.
- **The other is shared but noisy.** A wall clock is the only thing two
  independent streams have in common, and it carries about a millisecond of
  jitter, so placing *every* item by it accumulates error in one direction.

The arrangement that works is to use each for what it is good at: **the shared
one places the beginning, the precise one carries everything after, and the two
disagreeing is itself the signal** that something glitched — which is worth
knowing *before* anything is built on top of it.

The same structure, three times:

| Domain | Shared, noisy | Precise, local | What disagreement means |
|---|---|---|---|
| two audio tracks | wall clock at stream start | device sample position | the stream dropped packets |
| a pushed event stream | server timestamp per frame | frame sequence number | a frame was missed; resync |
| a client's view of a server | a slow poll | the live stream | the stream is buffered or dead |

The third is the one people get wrong, by treating a stream as a *replacement*
for polling rather than a complement. Keep the poll: the stream is the
low-jitter counter, the poll is the wall clock that says where the truth
actually is. A stream alone cannot tell you it has stopped.

**The rule underneath all three: do not throw ordering away at the boundary.**
A timestamp discarded where data is captured is unrecoverable everywhere
downstream, and the loss is invisible until something has to be aligned — at
which point the artifact exists and the information needed to fix it does not.

## An artifact handed to a reviewer is submitted, not filed

Tests verify that code does what its author meant. They cannot catch the case
where **what the author meant was wrong**, and that case needs a reader looking
at the *output* rather than the code.

Measured, not asserted: on one project, four of thirty commits in a day came
from a peer session reading delivered artifacts — and they were the four most
serious defects in it, including fabricated content presented as real and a
suppression that fired correctly by a margin of 0.00015. A suite of 141
mutation-checked tests caught none of them.

So when work is handed to another session, both sides have a job:

- **The sender** makes the artifact self-contained. A durable message that names
  a path has a payload nothing preserves — inline what a reader needs, because
  the queue outlives the file.
- **The reviewer measures rather than accepts.** Recompute the number, re-run
  the tool independently, and say plainly when a reported figure is unverified
  rather than repeating it.

The same argument applies across machines: a change to platform-specific code is
verified on a *second* node, because the developing machine usually cannot
produce the failing condition.

## Calling out blockers and decisions

When something is stuck, or a choice is genuinely the user's, **say so in a visual callout** —
never buried in a paragraph. The user is often running several threads and scanning; a blocker
that reads like narration gets missed, and a question without trade-offs just hands the work
back.

Use one of three markers, and always include the trade-offs:

> 🚧 **BLOCKER — <one line: what cannot proceed>**
>
> **What's stuck:** the concrete thing that will not move.
> **Why it matters:** what it costs to leave it, in real terms.
> **Options:** a table — each with a genuine pro *and* con, not a strawman.
> **My recommendation:** pick one, and say what would change your mind.
> **What I need:** the single specific thing that unblocks it.

> ❓ **DECISION — <one line: the choice>**
>
> Same shape. Use when work *can* continue but the call is the user's — a reversal of something
> they chose, a business/scope question, or a fact only they hold.

> ⚠️ **RISK — <one line>**
>
> Proceeding is possible and you are proceeding, but there is a downside they should know about
> now rather than discover later. State it and continue; do not stop for an answer.

**The rules that make these useful:**

- **Always give pros and cons.** "Option A or B?" without them is not a question, it is
  delegation of the analysis. If one option is clearly better, say so and explain why the other
  still exists.
- **Recommend.** A decision presented without a recommendation costs the user the thinking you
  were meant to do.
- **One callout per distinct issue**, at the point it arises — not batched at the end where the
  first is forgotten by the time the last is read.
- **Do not use them for narration.** A callout for something you already solved trains the user
  to skim past the ones that matter.
- Reserve **BLOCKER** for genuinely stuck; if you can proceed under a stated assumption, that is
  a **RISK**, and you proceed.

## Security

- Never commit credentials or secrets.
- Prefer `127.0.0.1` over `localhost` for local services: `localhost` may
  resolve to IPv6 `::1` first while most local servers bind IPv4 only, which
  fails intermittently and is tedious to diagnose.
- Test security-relevant changes deliberately, and follow any additional rules
  in a project's own `CLAUDE.md`.

---

@CLAUDE.local.md
