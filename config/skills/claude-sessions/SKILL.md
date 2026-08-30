---
name: claude-sessions
description: Use for anything involving OTHER Claude Code sessions as concurrent workers — spawning one ("create a session in /c/projects/foo", "spin up an agent", "kick off an agent and have it do Y"), delegating work and getting the result back, running several in parallel, checking what you handed off, seeing what each is doing, waiting on one, or unsticking one that is blocked on a dialog. Also listing, closing, reopening and clearing sessions, and reading what a pane shows. Think of a session as a thread with its own context and its own machine; this is the primitive table for spawn, delegate, join, poll and kill.
version: 2.0.0
---

# claude-sessions

**A session is a thread.** It has its own context, its own working directory, usually its own
machine, and it runs whether or not you are watching. This skill is the primitive table.

The one structural difference from threads in a process: **there is no shared memory.** Nothing is
implicit — not your conversation, not the file you just read, not why. Everything a session needs
has to travel in the message.

## The primitives

| You want to | Use | Notes |
|---|---|---|
| **spawn with work** | `task_create to="<project>" brief="…"` | one call: delegates *and* tracks. The default for "go do X" |
| **spawn on a specific machine** | `session_send to="<node>"` (`wsl`, `mac`) | that node's core session decides whether to do it, start a session, or refuse. It owns its own resources |
| **spawn locally, no work yet** | `shabadoo win open <path>` | the only way to create one. Idempotent about the WINDOW, not the context — see below |
| **tell, not ask** | `session_send to="…"` | no tracking. Use when nothing is expected back |
| **join / get the result** | *nothing — it comes to you* | when a task reaches `done` or `dropped`, the requester is mailed and nudged automatically |
| **poll your handles** | `task_list requested_by="<your id>"` | what did I hand off, and where did it get to |
| **check your own queue** | `task_list` | what has been handed to you |
| **report progress** | `task_update id=… state=active\|blocked\|done\|dropped` | `blocked` wants a note saying what you are stuck on |
| **advertise what you are doing** | `session_status_set "…"` | visible to every peer; ages out after 30 min; empty string clears |
| **list the threads** | `session_list` (or `shabadoo sessions`) | project, status, online, undrained mail |
| **ask if anything is stuck** | `shaba blockers` (or `shaba good`) | one screen: sessions waiting on a prompt, mail nobody picked up, blocked tasks, offline nodes. Silence means nothing is stuck |
| **see who is out there** | `shabadoo who` | one screen: every session, what it is FOR, what it says it is doing. A blank means a peer knows the name and nothing else |
| **see what a node could start** | `shabadoo folders --node <host>` | startable folders, `*` marks already open. Ask this BEFORE assuming a project exists on another machine |
| **kill, with a prompt** | `shabadoo kill <name>` | asks first; `win close` does not |
| **read one's screen** | `shabadoo tail <name>` | |
| **unblock one** | `shabadoo keys --pane <name> Enter` | it is sitting on a dialog; `tail` first and read the question |
| **start it clean** | `shabadoo command --pane <name> /clear` | a folder with history RESUMES on open. Escape dismisses the prompt but the context still loads |
| **kill** | `shabadoo win close <name>` | `reopen` rebuilds it in the same directory |

**The unit of concurrency is a session. You spawn one — you never split anything.** More work in
parallel means another session, in its own directory, with its own context. Narrower work under a
project is a session in a subfolder, reported as `<parent>/<child>`.

**tmux is internal access, not a concept you work in.** It is how shabadoo reaches a running session
to read or type; nothing above that layer should mention panes, windows or tmux commands. If you are
composing a `tmux` command, you are working at the wrong layer — the operation you want is in the
table above.

**You never block.** There is no `wait()`. A task's completion arrives as mail, and a stalled task is
chased for you — untouched for 6 hours raises it once, then daily. Polling `task_list` in a loop is
the thing this design exists to remove.

## Delegating: `task_create` over `session_send`

Use `task_create` whenever you are asking someone to **do** something; `session_send` when you are
merely **telling** them something. The difference is what happens when nobody answers: an unanswered
task is chased, an unanswered message is forgotten.

**The brief is the whole handoff.** State the goal, the paths, what you have already established,
what to avoid, and what "done" looks like — written so somebody could act on it without asking you a
question first. A one-line ask buys a session that spends its first ten minutes rediscovering what
you already knew, or guesses wrong.

Addressing is **by project** — `homelab`, `iptv` — never by session id. Exact match wins, then
substring; ambiguous is refused rather than guessed, and unknown bounces with the list of what
exists. Matching deliberately ignores the cwd, or `home` would match every session on a Linux box.

**Mail queues for a project the coordinator can see, and BOUNCES for one it
cannot.** The line is not running-or-not; it is whether that project appears in
its node's startable folder list — which means it is in the boot list, or has
been opened there before. A project like that is stored against the id it would
have and drains when it starts. **Anything else is refused at send time and
nothing is kept**, with the list of what exists.

So a real checkout on a real machine that has simply never been opened is NOT a
valid recipient. `shaba folders --node <host>` is what says which is which, and
adding a folder to the boot list makes it addressable before it has ever run.

Check the reply rather than assuming: a refusal is an error, not a queued
message. Anything with a fallback — writing the handoff to a file, into the
peer's repo — should use it on refusal, not only when the coordinator is
unreachable.

The node's core session is asked whether to start a queued recipient, so you do
not open a window first just to have somewhere to send to — provided the project
is one the coordinator can see.

## Running several at once

Spawn them all, then stop thinking about them — each completion arrives on its own.

```
task_create to="iptv"     brief="…"
task_create to="homelife" brief="…"
task_create to="homelab"  brief="…"
task_list requested_by="<your id>"     # only if you need the picture before they report
```

Sessions on the same node share that machine, so three heavy builds on one host is one machine's
worth of work, not three. `session_list` shows which node each is on.

## Opening a folder that has been used before

The first ten seconds after `win open` are where this goes wrong, and only the
rare half of it used to be written down.

**The trust dialog is the rare case** — a folder Claude has never run in.
Arrow-selectable, `Down` reaches *Yes, I trust this folder*, often wants **two**
`Enter`s.

**The resume prompt is the ordinary case, and it has a price tag:**

```
This session is 18d 4h old and 678.8k tokens.
Resuming the full session will consume a substantial portion of your usage limits.
❯ 1. Resume from summary (recommended)
  2. Resume full session as-is
  3. Don't ask me again
  Enter to confirm · Esc to cancel
```

Enter is the default and it **spends real usage**. This is the concrete reason
nothing here presses Enter on a pane it has not classified first.

**`open` is idempotent about the window, not the context.** It will not
duplicate a window — but it can hand you an 18-day-old session whose last work
was something else entirely, in a folder you asked for fresh work in.
"Idempotent" reads as *safe, gives me a session*; it does not read as *gives you
someone else's context, aged, and bills you for it*.

**Escape does not give you a clean session.** It cancels the *choice*, not the
*resume* — the context loads anyway. Measured:

```
after Escape:   ◔ 678,483 (30%)     ← prompt dismissed, context loaded
after /clear:   § 0 tokens, ◔ 0 (100%)
```

The clean-start operation is `shabadoo command --pane <name> /clear`.

## Driving a pane directly

For the two things mail cannot do: answering a dialog, and running a slash command.

```sh
shabadoo tail foo                          # 1. what is it actually showing?
shabadoo keys --pane foo Enter             # 2. only if a dialog is up
shabadoo command --pane foo /clear         # slash commands
shabadoo send --pane foo "continue"        # literal text into the composer
shabadoo tail foo                          # 3. confirm it landed
```

**Text typed into a modal is swallowed and `send` still reports success.** A folder Claude has not
run in before opens with a trust dialog: arrow-selectable (`Down` reaches *Yes, I trust this
folder*), often needing **two** `Enter`s. `keys` takes key names — `Enter`, `Escape`, `Up`, `Down`,
`1`, `y`. Always `tail` after: a successful `send` means keystrokes were delivered, not
received.

**Read the question before answering it.** A blocked session is blocked on something, and these
panes run with permissions disabled — "yes" to an unread prompt is how something gets deleted.

## Two planes

| | Command | Scope |
|---|---|---|
| **Local** | `shabadoo win list\|open\|close\|reopen\|clear` | this host only, straight through its agent; works with the coordinator down |
| **Coordinator** | `shabadoo sessions\|open\|tail\|send\|keys\|command`, and every MCP tool | every connected node |

`--node <host>` becomes **required** the moment a second node connects, even for a pane on this host;
with one, it is inferred. The fix is the flag, not a workaround.

`<name>` is an exact window name or a unique substring; ambiguous refuses and lists candidates.

**`shaba` is `shabadoo`** — a symlink `setup` installs beside the binary. Every
command here works under either name, and the short one is what to write when a
command is being typed by a person or repeated in a brief.

## Worth knowing

- **Windows from `win open` are ephemeral.** The boot watchdog only reopens folders in
  `~/.config/claude-sessions/folders` (`shabadoo boot list|add|remove`). Right for task sessions; add
  the folder only if it should survive a reboot.
- **A closed session stays closed.** Exiting records intent, so the watchdog will not reopen it.
  Opening clears that, and so does `shabadoo boot add <dir>` — listing a folder for boot is an
  explicit request to run it, and the newer statement wins.
  **This is the trap to know about:** exiting a session to restart Claude is indistinguishable, at
  the exit, from closing one to free resources. A boot list of nineteen folders once opened two for
  exactly this reason, and the skips went to a cron log nobody tails. `shabadoo boot list` marks a
  held folder `x`; `boot add` clears it.
- **`shabadoo todo`** is every open item on the fleet in one table, grouped by who is blocked —
  the `## Waiting on` rows projects write, plus blocked delegated tasks and panes sitting at a
  prompt. `--mine` narrows it to rows owned by the human, `--closed` adds what stopped being
  listed and how long each stood, `--project X` scopes it.
  **`shaba blockers` is the terse version and was for a long time the WRONG one:** it read four
  mechanical states — a pane at a dialog, undrained mail, a blocked task, an offline node — and
  never the rows people write, so a fleet with forty human-owned blockers standing answered
  "nothing is stuck". It counts them now and points here. An age in this table is bounded by the
  coordinator's uptime and says so when it is; a row may have stood far longer than it reads.
- **`shabadoo dash [name]`** opens the dashboard in this machine's browser, and with a name opens it
  focused on that pane with the conversation showing. It always PRINTS the URL first, so it is still
  useful over ssh or where no opener exists — which is also the form to paste when pointing a human
  at a pane. The name resolves the way `tail` does. On WSL it goes through `wslview`, because
  `xdg-open` is present there and wrong: the browser is on the Windows side.
- **`shabadoo mission`** prints this project's `MISSION.md` as the fleet reads it — the parse, not
  the file — so "does mine actually report?" is answerable from the folder that owns it rather than
  from a dashboard on another host. It also names the waiting rows the six-row cap discarded, which
  are invisible everywhere else. `shabadoo mission init` scaffolds one; it states nothing on the
  project's behalf, because a generated `status: active` is indistinguishable on the dashboard from
  one somebody meant.
- **Mail needs a coordinator.** Under the standalone fallback (`shabadoo serve`) `/api/messages` is
  501 — no database, no mail plane, so keystrokes are the only route. A single machine can run both
  halves (`setup --service --device-tokens`) and keep everything.
- **A new tool does not reach a running session.** Each session launches `shabadoo mcp` at start and
  keeps that tool list until the window restarts. `/clear` does not fix it.
- `shabadoo mail` reads the traffic and shows whether a message was drained or is still waiting;
  `shabadoo audit` shows who drove which pane.
- **Say what you sent.** The user cannot see the other window; a one-line echo of the text and the
  target is the only record they get.
