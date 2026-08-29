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

### Getting work done on a machine

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
