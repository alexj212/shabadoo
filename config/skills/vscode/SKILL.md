---
name: vscode
description: Open a file, a line, a folder or a diff in Visual Studio Code on this machine — "open X in vscode", "open that in code", "show me that file in the editor", "open it at the error", "open the file you just changed", "diff these two in vscode". Resolves a vague reference to an exact path and line first, then runs the `code` CLI. Not for reading a file yourself (use Read); this puts it in front of the human.
---

# Open in VS Code

The `code` CLI already does this. The skill exists so the path is exact, the
command is right, and the report says honestly where the window went.

## 1. Resolve the target first

The user rarely gives a path. "The hub handler", "where that test fails", "the
file you just edited" — turn it into an **absolute path and, where one is
meant, a line number** before running anything.

- Use what this conversation already established (a file you edited, a
  `file:line` from a test failure or a grep hit).
- Otherwise search (`git grep -n`, Grep/Glob with an explicit path).
- **More than one plausible match: ask, listing them.** Opening the wrong file
  confidently is worse than one question.
- A path that does not exist: say so. `code` will happily open an empty buffer
  for a missing file, which looks like success.

## 2. Check the CLI exists

```bash
type -a code
```

Nothing found:

- **macOS** — the CLI is not installed by default. The human runs *Shell
  Command: Install 'code' command in PATH* from VS Code's command palette. Tell
  them that; do not hunt for the app bundle and invent a path.
- **Linux without a desktop** (a server, an ssh-only host) — there is no editor
  here to open. Say so.

## 3. Open it

| Want | Command |
|---|---|
| a file | `code -r "<abs path>"` |
| a file at a line (and column) | `code -r -g "<abs path>:<line>[:<col>]"` (on WSL use the `cmd.exe` form below — for every row) |
| a folder / project | `code -r "<abs dir>"` (`-n` for a new window instead) |
| two files side by side | `code -d "<left>" "<right>"` |
| several files | `code -r "<a>" "<b>" …` |

`-r` reuses the most recently focused window rather than opening a new one each
time. Always quote paths — paths under a Windows home or `Program Files` routinely contain
spaces.

**WSL: bypass the shell shim — call Windows' own `code` through `cmd.exe`.**

```bash
(cd "$(wslpath -u 'C:\')" && cmd.exe /c code -r -g "$(wslpath -w "<abs path>"):<line>")
```

`wslpath -w` gives the Windows path; `cmd.exe /c code` runs `code.cmd`, which
hands the file to the running editor and returns in about a second. The `cd` is
only there because `cmd.exe` started from a Linux-filesystem directory prints a
"UNC paths are not supported" warning (harmless, but noise). Paths containing
`&`, `^` or `%` are unsafe through `cmd.exe`; ask before opening one.

**Why not the `code` on `PATH`.** Inside WSL that is a bash shim that picks a
route by `$WSL_DISTRO_NAME` alone (its fallback matches only WSL1 kernels), and
all three variants seen on this fleet misbehaved while exiting 0 or never
exiting at all:

- **Variable unset** (sessions under a tmux server started by a systemd
  service) with a Linux path — handed to Windows verbatim: `/c/projects/x.md`
  opens as `C:\c\projects\x.md`, "file not found".
- **Variable set** with a Linux path — routed through the Remote-WSL extension,
  which on a host with a non-default `[automount] root` (drives at `/c`)
  reported a doubled drive, `c:\c:\Users\…`. Which component doubles it is not
  established.
- **Variable unset** with a Windows path — the shim runs
  `ELECTRON_RUN_AS_NODE=1 Code.exe cli.js`, but only its WSL branch adds that
  variable to `WSLENV`, so Windows never sees it and `Code.exe` starts as a
  full Electron main process. It worked once where an instance was already
  running and **hung for 120s** in another session, printing main-process
  startup logs.

`cmd.exe` sidesteps all of it: the variable is set on the Windows side, by
Windows' own script. A file on the Linux filesystem becomes
`\\wsl.localhost\<distro>\…` — untested; Windows may ask to allow that host
first.

## 4. Report honestly

`code` exits 0 once it has handed the request to the editor; it does **not**
confirm a window appeared or which window took it. So say what you asked for,
not what the human is looking at:

> Asked VS Code on **wsl** to open `hub/human.go:212`.

**Name the host.** The window opens on the display of the machine this session
runs on. A human driving this session from a phone, the dashboard or remote
control sees nothing there — if that is likely, say so, and offer to show the
relevant lines in the reply instead.

If the human says nothing opened, the usual causes are: VS Code not running and
slow to start (wait, then retry once), the window opened behind others, or on
WSL the WSL extension missing. Do not retry in a loop — each retry is another
window.
