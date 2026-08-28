# Mission — Local PC cleanup: the Claude Code environment

**You are an agent on a focused local mission.** This machine's **Claude Code configuration** has drifted:
skills that document a CLI that has moved on, a global `CLAUDE.md` naming tools that no longer exist, MCP
entries for servers that don't answer. Make it true again.

**This is not a disk-space sweep.** Scope is configuration, documentation and tooling correctness — not
reclaiming gigabytes.

## The rule that created this mission

Every finding below was discovered *sideways*, by an agent doing unrelated work, and patched ad hoc.
That is the failure mode this mission exists to end:

> **Promote, don't absorb.** When you find something wrong with the environment while doing other work,
> it belongs **here**, raised as an item — not fixed quietly in the margin of whatever you were doing.
> If you are unsure whether something is in scope, **promote it and ask.** Silence is the expensive option.

## Sources of truth — know which file owns what

| Layer | Path | Owns |
|---|---|---|
| **Payload** | `/c/projects/shabadoo/config/` | The **portable** `~/.claude` — `CLAUDE.md`, `settings.json`, `skills/`. Committed. Ships to any machine via `shabadoo setup`. |
| **Overlay** | `/c/projects/shabadoo/config.local/` | This operator's personal `~/.claude`, mirrored by `make vendor`. **Never committed.** |
| **Installed** | `~/.claude/` | What Claude actually reads. Rebuilt by `shabadoo setup`. |
| Machine | `~/.claude/CLAUDE.local.md` | Project registry, `find` whitelist, work toolchain. Never vendored. |
| Per-machine | `~/.claude.json` | MCP servers. **Not synced** — re-added per machine. |
| Launcher | `~/.config/claude-sessions/folders`, `~/.config/claude/env` | Boot list and launcher knobs. Outside the repo by design. |

⚠️ **The precedence trap.** `setup` merges `config/` **then** `config.local/`, last write wins — so
**the overlay silently overwrites the payload.** A fix applied only to `config/` is masked by a stale
overlay copy and appears to do nothing. Verified in `setup.go` (`mergePayloads`). Always check both.

Editing `~/.claude/` directly is pointless: the next `setup` overwrites it. Fix at the source, then sync.

## Already done (2026-08-28, by the runner agent — recorded here so it isn't redone)

- **`claude-sessions` skill → v1.1.0.** Was v1.0.0, dated 2026-07-30, and documented only
  `shabadoo win {list,open,close,reopen,clear}` — no way to *drive* a session. Added `send`/`tail`/`keys`/
  `command`, the launch recipe, the modal-swallows-text failure, the ephemeral-window/boot-list rule.
  Removed a **false `win kill` alias** (tested: falls through to usage). Synced to all three layers.
- **Global `CLAUDE.md`:** the whole coordination section named `mcp__natsbridge__*` tools that **do not
  exist** — the server is `shabadoo`. Corrected, plus the MCP table row (`~/bin/mcp-natsbridge` is not on
  disk). Added the missing **`unifi`** row. Kept `mcp__homelife-mcp__natsbridge_stats`, which is real.

## Do now

- [ ] **Audit every skill in `~/.claude/skills/` the way `claude-sessions` was audited** — run its CLI's
      `--help` and check each documented verb actually exists. That skill was accurate-but-stale and had one
      invented command; assume the others may too. ~20 skills; report per-skill verdict.
- [ ] **Reconcile the MCP table against `claude mcp list`.** Known live gaps: `google-workspace` **fails to
      connect** (30 s timeout) while `CLAUDE.md` claims full read+write "verified 2026-06-24"; four
      `cloudflare` plugin servers say *needs authentication*; `jetbrains` is disabled per-project. Fix the
      claims or fix the servers — do not leave a doc asserting access that is not there.
- [ ] **Verify the retirement claims** in `CLAUDE.md` / `CLAUDE.local.md`: `claude-install.sh`, `tmuxbridge`,
      ttyd, Caddy are all described as retired 2026-07-29. Confirm nothing still references or runs them.
- [ ] **Check `CLAUDE.local.md`'s project registry** against what is on disk — paths that moved, projects
      that no longer exist, the `find` whitelist entries.
- [ ] **`~/.claude/commands/`** — the work-specific slash commands, deliberately not vendored. Confirm each
      still resolves to a live tool.
- [ ] **Launcher state:** `~/.config/claude-sessions/folders` vs `deactivated`, and whether the `*/10`
      cron watchdog matches what is actually wanted.
- [ ] **Re-date the docs.** `CLAUDE.md` says *Last Updated 2026-07-30*. Anything that survives this audit
      gets a current date; anything that fails gets corrected, not annotated.

## STOP at these gates
- ❌ **Delete nothing without asking.** Correct claims in place; **propose** removals as a list. This is a
  machine holding work repos, credentials and live sessions.
- ❌ **Never touch `~/.claude/CLAUDE.local.md` content that is work-specific** without flagging it — it is
  deliberately excluded from the shabadoo payload (`make vendor-check` enforces this; do not defeat it).
- ❌ **Never commit a secret.** `~/.claude.json` and `~/.config/claude/env` hold tokens.
- ❌ **Do not kill or reopen live Claude windows** — check `shabadoo win list` for the `ACTIVE` marker.
- ❌ Do not edit `~/.claude/` as the fix. Fix the source, sync, verify all three layers agree.

## Definition of done
Every skill verified against its live CLI; the MCP table matches `claude mcp list`; no doc claims a tool,
path or access that does not exist; payload / overlay / installed agree; and the removal proposals are a
list awaiting a human, not an action already taken.

## Coordination
Report to **mission-control** (the `devops` session) — one line per item cleared or blocked. Peers:
**runner**, **dns**. If a peer finds environment drift, it promotes it here rather than fixing it inline.

## Log
-
