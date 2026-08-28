---
name: claude-sessions
description: Use this skill for any request about OTHER Claude Code sessions — creating one ("create a session in /c/projects/foo", "new session for X", "spin up an agent", "kick off an agent and have it do Y"), handing one work, listing, closing, killing, reopening or clearing one, reading what its pane shows, or answering a dialog it is stuck on. Creating a session is `shabadoo win open`, never `tmux new-window`; handing it work is mail (`session_send`), not typed keystrokes. Covers this host and, through the coordinator, every connected node.
version: 1.3.0
---

# claude-sessions

Manage and drive Claude Code windows — locally in the shared `claude` tmux session (created by
`shabadoo attach`), and across hosts through the coordinator.

## When to use

Trigger on requests about **other Claude Code instances** — running or about to be:

- "list my claude sessions" / "what claude windows are running"
- **"create a session in /c/projects/foo"** / "new session for X" / "spin up an agent" /
  "start a claude session there" / "kick off an agent and have it do Y"
- "kill the claude session in /c/projects/foo" / "close the homelab window"
- "restart / reopen the claude session for <project>"
- "clear the context on my other claude" (mobile can't type `/clear`)
- **"tell the runner session to …" / "what is that window asking?" / "answer its prompt"**

Do **not** use for:
- The current Claude conversation — the user can type `/clear` themselves.
- Tmux in general — only the `claude` session is managed.

## Creating a session is never `tmux new-window`

**A session is not a window with `claude` running in it.** Every path that starts one goes through
`shabadoo` (`win open`, or `open` through the coordinator) because the launcher injects state at
window-creation time that cannot be added afterwards:

| What the launcher sets | What a hand-rolled `tmux new-window` gets instead |
|---|---|
| `CLAUDE_SESSION_ID=claude-<project>-<host>-<8hex>` | **nothing** — the session has no id, so `session_send` cannot address it and it cannot say who it is |
| the window name, `<project>-<host>-<8hex of sha1(path)>` | an arbitrary name — the "is this folder already open?" check misses it, so the next `open` adds a **duplicate** window for the same folder |
| `--remote-control <alias>` | no alias — the session never appears in the iOS / web Code app |
| `SSH_AUTH_SOCK` / `SSH_AGENT_PID`, forwarded explicitly | a **stale** agent socket. The tmux server snapshots its environment once, at first-session creation, so a window made later inherits whatever was true then — git over ssh fails inside it |
| `CLAUDE_ARGS`, `CLAUDE_RESUME` from `~/.config/claude/env` | none of the operator's launch settings |

None of that is visible from inside the new window. It looks like a working Claude, and it is
unaddressable, invisible to the phone, and duplicated on the next open. **If you catch yourself
typing `tmux`, the command you want is `shabadoo win open`.**

## Two planes, and which to reach for

| | Command | Scope |
|---|---|---|
| **Local** | `shabadoo win …` | This host's tmux directly. Works even when the coordinator is down. |
| **Coordinator** | `shabadoo sessions`, `open`, `send`, `tail`, `keys`, `command` | Every connected node. **`--node <host>` becomes required the moment more than one node is connected** — even to reach a pane on *this* host. |

Default to `shabadoo win` for listing and lifecycle on this machine; use the coordinator verbs to
*drive* a pane, and whenever the target might be on another host.

> **⚠️ Multi-node needs `--node` — do NOT drop to raw `tmux`.** With one node, `tail`/`send`/`keys`/`command`
> resolve the local pane on their own. The instant a second node joins (e.g. a Mac attaches), they refuse
> with `several nodes connected (mac, wsl) — pass --node`. The fix is the **flag**, not a workaround:
> add `--node <thishost>` (find it in `shabadoo sessions`; on this box it is `wsl`). Reaching past the skill
> into `tmux send-keys` is the wrong move — it bypasses the audit trail and the readiness handling.

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

## Giving a session work: mail, not keystrokes

There are two ways to put words in front of another session, and they are **not** interchangeable.

| | Mechanism | What it is for |
|---|---|---|
| **Mail** | `mcp__shabadoo__session_send` / `task_create`, or `shabadoo mail` | **handing over work** — a brief, a task, a question for a peer |
| **Keystrokes** | `shabadoo send` / `keys` / `command` | **driving a pane** — answering a dialog, firing a slash command, putting literal text in the composer |

**Default to mail for a handoff.** Four reasons, and the second is the one that changes how you
create sessions:

- **It is durable and acknowledged.** `shabadoo mail` shows whether the recipient actually drained it.
  A prompt typed into a pane leaves no record anywhere that it was received.
- **It works before the session exists.** Mail addressed to a project that is not running is stored
  against the session id that project *would* have, and the coordinator asks that node's core session
  to start it. When the session starts, the message is already waiting and drains into its context.
- **A running recipient is nudged immediately** — the coordinator types `check inbox` into its pane,
  firing the drain hook. You are not waiting for a human to come back to that window.
- **Typed text is swallowed by any modal**, silently, and `send` still reports success.

### The three shapes

**1. The project already exists — just mail it. Do not open anything.**

```sh
# runs it if it is running; wakes it through its node's core session if it is not
session_send to="homelab" title="..." body="..."
```

**2. A folder the system has never run — create it, then mail it.**

```sh
shabadoo win open /c/projects/new-thing     # idempotent; sets the session id
session_send to="new-thing" title="..." body="..."
```

Mail, not `send`, even though the window is right there: the brief lands in context as a message
rather than as typing that a trust dialog may eat.

**3. You genuinely need a keystroke** — a dialog is up, or you want a slash command run.

That is where `send`/`keys`/`command` belong, and there the verify-at-every-hop dance applies:

```sh
shabadoo tail foo                          # 1. see what the pane is actually showing
shabadoo keys --pane foo Enter             # 2. ONLY if a dialog is up
shabadoo send --pane foo "continue"        # 3. the keystrokes
shabadoo tail foo                          # 4. confirm they landed
```

**A folder Claude has not run in before opens with a trust dialog, and text typed into a modal is
swallowed** — `send` will appear to succeed and the prompt will have gone nowhere. The trust dialog is
arrow-selectable (`Down` reaches *Yes, I trust this folder*) and often needs **two** `Enter`s — one to
confirm the selection, one more before Claude reaches its prompt. `keys` takes tmux key names:
`Enter`, `Escape`, `Up`, `Down`, `1`, `y`.

**Always `tail` after `send`.** Nothing in this chain reports its own failure — a successful `send`
means the keystrokes were delivered, not that anything received them.

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
- **Reading the mail plane.** `shabadoo mail` shows the traffic — including whether a message was
  drained or is still waiting — and `shabadoo inbox` drains *this* session's queue (built for a
  UserPromptSubmit hook: silent and exit-0 when empty). Prefer the `mcp__shabadoo__session_*` tools
  when they are available in the current session; same bus, structured arguments.
- **An unknown recipient bounces, an ambiguous one is refused.** Names resolve exactly first, then by
  substring, across session id, alias and project — never the cwd. If a project name will not resolve,
  check `shabadoo sessions` and `shabadoo folders` rather than falling back to typing into a pane.
- `shabadoo audit` shows who drove which pane, newest last — the record when a pane did something
  unexpected.
