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
| `session_broadcast` | to a topic |
| `session_inbox_drain` | collect and mark delivered, in one transaction |
| `notify_send` | reach a human (routed by the coordinator, not by each host) |

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

## Security

- Never commit credentials or secrets.
- Prefer `127.0.0.1` over `localhost` for local services: `localhost` may
  resolve to IPv6 `::1` first while most local servers bind IPv4 only, which
  fails intermittently and is tedious to diagnose.
- Test security-relevant changes deliberately, and follow any additional rules
  in a project's own `CLAUDE.md`.

---

@CLAUDE.local.md
