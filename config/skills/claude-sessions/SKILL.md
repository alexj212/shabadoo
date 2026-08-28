---
name: claude-sessions
description: Use this skill when the user asks to list, open/start, close, kill, reopen, or clear the context of running Claude Code instances — or to *drive* one: send it a prompt, read what its pane is showing, answer a dialog it is stuck on, or launch a new session and give it its first instruction ("kick off an agent in /c/projects/foo", "what is the homelab claude asking", "tell the runner session to continue"). Covers local tmux windows in the shared `claude` session and, through the coordinator, sessions on every connected node.
version: 1.1.0
---

# claude-sessions

Manage and drive Claude Code windows — locally in the shared `claude` tmux session (created by
`shabadoo attach`), and across hosts through the coordinator.

## When to use

Trigger on requests about **other Claude Code instances** — running or about to be:

- "list my claude sessions" / "what claude windows are running"
- "start a claude session in /c/projects/foo" / "kick off an agent there"
- "kill the claude session in /c/projects/foo" / "close the homelab window"
- "restart / reopen the claude session for <project>"
- "clear the context on my other claude" (mobile can't type `/clear`)
- **"tell the runner session to …" / "what is that window asking?" / "answer its prompt"**

Do **not** use for:
- The current Claude conversation — the user can type `/clear` themselves.
- Tmux in general — only the `claude` session is managed.

## Two planes, and which to reach for

| | Command | Scope |
|---|---|---|
| **Local** | `shabadoo win …` | This host's tmux directly. Works even when the coordinator is down. |
| **Coordinator** | `shabadoo sessions`, `open`, `send`, `tail`, `keys`, `command` | Every connected node. Takes `--node` to target another host. |

Default to `shabadoo win` for listing and lifecycle on this machine; use the coordinator verbs to
*drive* a pane, and whenever the target might be on another host.

```
shabadoo win list                    # windows + project dirs (marks the ACTIVE one)
shabadoo win open   <path>           # start a claude window for <path> (no attach, idempotent)
shabadoo win close  <name>           # kill a window
shabadoo win reopen <name>           # kill and re-launch claude in the same dir
shabadoo win clear  <name>           # send /clear to the claude in that window

shabadoo sessions                    # every node's sessions
shabadoo folders [--node N]          # folders a node can start
shabadoo open [--node N] <folder>    # start a session on a node
shabadoo tail <name> [--lines N]     # what is on that pane right now
shabadoo send --window N "text"      # type text into a pane AND submit it
shabadoo keys --pane <name> <key>…   # raw keypresses — how a dialog gets answered
shabadoo command --pane <name> /cmd  # run a slash command in a pane
```

`<name>` may be the exact window name from `list` (e.g. `homelab-4b602ded`) or a unique substring
(e.g. `homelab`). If the substring matches several windows the command refuses and lists candidates —
pass a more specific pattern. `send`, `keys` and `command` also accept `--window N` (the index from
`list`) instead of `--pane`.

There is **no `win kill`** — the verb is `close`. (`shabadoo kill` exists at the top level and asks
first; `win close` does not.)

## Launching a session and giving it its first prompt

This is the sequence that matters, because `open` on its own just produces an **idle** window — a
Claude sitting at an empty prompt. Opening is not kicking off. Four steps:

```sh
shabadoo win open /c/projects/foo          # 1. create the window (idempotent)
shabadoo tail foo                          # 2. see what it is actually showing
shabadoo keys --pane foo Enter             # 3. ONLY if a dialog is up (see below)
shabadoo send --pane foo "Read ./TASK.md and begin."   # 4. the first instruction
shabadoo tail foo                          # 5. confirm the prompt landed and it started
```

**Step 3 is the one that silently ruins a launch.** A folder Claude has not run in before opens with a
trust/permission dialog, and **text typed into a modal is swallowed** — `send` will appear to succeed,
the pane will look fine, and the prompt will have gone nowhere. Always `tail` before `send`, and if a
dialog is up, clear it with `keys` first. `keys` takes tmux key names: `Enter`, `Escape`, `Up`, `Down`,
`1`, `y`.

**Always `tail` after `send`, too.** Nothing in this chain reports its own failure — a successful
`send` means the keystrokes were delivered, not that they were received by the thing you meant.

## How to handle requests

1. **Start with `shabadoo win list`** to show the user what's running and resolve which window they
   mean. For sessions on **other** hosts use `shabadoo sessions` instead — that goes through the
   coordinator and covers every connected node.
2. **Never close or reopen the current window** — `list` marks it with `yes` in the `ACTIVE` column.
   If the user asks to kill/reopen it, warn first (it terminates the current conversation).
3. **For `clear`**: Claude Code may show a confirmation prompt after `/clear`. If the user reports it
   didn't clear, `tail` the pane — it is probably sitting on that confirmation, which `keys … Enter`
   answers.
4. **For `reopen`**: the launcher re-derives the same `CLAUDE_SESSION_ID` from the cwd, so session mail
   keeps routing correctly. History is auto-resumed (`--continue`) if a `.jsonl` exists for that project.
5. **For `open`**: accepts an absolute or relative path; resolves to realpath. Idempotent — if a window
   for that path already exists, the command just reports it. Uses `-d` so it never steals focus from a
   terminal already attached to the shared session.
6. **When driving a pane on the user's behalf, say what you sent.** The user cannot see the other
   window; a one-line echo of the text and the target is the only record they get.

## Nuances

- **Opened windows are not persistent.** The boot watchdog (cron `*/10`, plus the
  `claude-sessions.service` user unit) only re-opens folders listed in
  `~/.config/claude-sessions/folders` — managed with `shabadoo boot list|add|remove`. A window from
  `win open` is **ephemeral**: close it, or reboot, and it is gone. That is usually correct for
  short-lived task sessions; add the folder to the boot list only when it should survive.
- The shared session name defaults to `claude`; override via `CLAUDE_SESSION_NAME` if the user has
  retargeted the launcher.
- `clear` works by sending `C-u /clear <Enter>`. Best-effort, and subject to the same modal-swallowing
  caveat as `send`.
- `reopen` does **not** attach — it rebuilds the window in the shared session. The user stays wherever
  they invoked this from (e.g. a mobile Claude conversation).
- **Session-to-session mail is a different mechanism.** `shabadoo mail` reads the traffic and
  `shabadoo inbox` drains this session's queue (built for a UserPromptSubmit hook — silent and exit-0
  when empty). Use mail to *message* a peer session; use `send` to *type into* its terminal. If the
  `mcp__shabadoo__session_*` tools are available in the current session, prefer those for messaging —
  they are the same bus with structured arguments.
- `shabadoo audit` shows who drove which pane, newest last — the record when a pane did something
  unexpected.
