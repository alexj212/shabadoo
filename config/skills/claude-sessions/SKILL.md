---
name: claude-sessions
description: Use this skill when the user asks to list, open/start, close, kill, reopen, restart, or clear the context of running Claude Code instances — e.g. "what claude sessions are running", "start a claude session in /c/projects/foo", "kill the homelab claude window", "restart the claude session in foo", "clear context on my other claude" (especially useful when `/clear` can't be typed, such as from Claude mobile). Scope is local tmux windows inside the shared `claude` session created by `shabadoo attach`.
version: 1.0.0
---

# claude-sessions

Manage Claude Code windows running locally inside the shared `claude` tmux session (created by `shabadoo attach`).

## When to use

Trigger on requests about **other currently-running Claude Code instances** on this machine. Examples:

- "list my claude sessions" / "what claude windows are running"
- "kill the claude session in /c/projects/foo" / "close the homelab window"
- "restart / reopen the claude session for <project>"
- "clear the context on my other claude" (mobile can't type `/clear`)

Do **not** use for:
- The current Claude conversation — the user can type `/clear` themselves.
- Remote hosts — this skill is local-only.
- Tmux in general — only the `claude` session is managed.

## Tool

A single CLI: `shabadoo win` (on PATH, at `~/bin/shabadoo`). These are the *local* commands — they drive this host's tmux directly and work even when the coordinator is down.

```
shabadoo win list                    # show windows + project dirs
shabadoo win open   <path>           # start a claude window for <path> (no attach, idempotent)
shabadoo win close  <name>           # kill a window
shabadoo win kill   <name>           # alias for close
shabadoo win reopen <name>           # kill and re-launch claude in the same dir
shabadoo win clear  <name>           # send /clear to the claude in that window
```

`<name>` may be the exact window name from `list` (e.g. `homelab-4b602ded`) or a unique substring (e.g. `homelab`). If the substring matches multiple windows the command refuses and lists candidates — pass a more specific pattern.

## How to handle requests

1. **Start with `shabadoo win list`** to show the user what's running and resolve which window they mean. It lists this host's `claude` session windows.

   For sessions on **other** hosts, use `shabadoo sessions` instead — that goes through the coordinator and covers every connected node.
2. **Never close or reopen the current window** — `list` marks it with `yes` in the `ACTIVE` column. If the user asks to kill/reopen it, warn first (it terminates the current conversation).
3. **For `clear`**: note that Claude Code may show a confirmation prompt after `/clear`; if the user reports it didn't clear, suggest they confirm manually once, or re-run.
4. **For `reopen`**: the launcher re-derives the same `CLAUDE_SESSION_ID` from the cwd so the NATS session-bridge keeps routing correctly. History is auto-resumed (`--continue`) if a `.jsonl` exists for that project.
5. **For `open`**: accepts an absolute or relative path; resolves to realpath. Idempotent — if a window for that path already exists, the command just reports it. Uses `-d` so it never steals focus from a terminal already attached to the shared session.

## Nuances

- The shared session name defaults to `claude`; override via `CLAUDE_SESSION_NAME` if the user has retargeted the launcher.
- `clear` works by sending `C-u /clear <Enter>` to the window. Best-effort: if the window is in a modal state (permission prompt, bash tool still running, etc.), the keystrokes may be consumed by that modal instead. Tell the user to check.
- `reopen` does **not** attach — it just rebuilds the window in the shared session. The user remains wherever they invoked this from (e.g. a mobile Claude conversation).
- Battery/sleep-driven nothing here — all commands are immediate.
