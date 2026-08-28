// shabadoo bridges a browser to tmux: it lists tmux sessions and their windows
// with live status, and drives them (select / send / kill / reopen). One
// binary, three roles — coordinator (hub), per-host agent (node), and
// the standalone fallback server.
//
// It is also the installer for the portable ~/.claude config, which is
// embedded in this binary and written out by `shabadoo setup`. The launcher
// itself is no longer a script: `attach`, `win` and `boot` are subcommands.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"shabadoo/node"
)

func main() {
	args := os.Args[1:]

	// A bare invocation prints usage.
	//
	// It used to start the fallback server, so that `ExecStart=... --addr ...`
	// kept working when subcommands were introduced. Every unit this installs
	// now names `hub`, `node` or `boot` explicitly, so that compatibility was
	// protecting nothing — while a downloaded binary run once to see what it is
	// silently bound a port and served a dashboard, which is a startling first
	// impression and the opposite of what someone typing `shabadoo` expects.
	if len(args) == 0 {
		usage()
		return // exit 0: asking what a program does is not a failure
	}
	if strings.HasPrefix(args[0], "-") {
		if isHelpFlag(args[0]) {
			usage()
			return
		}
		// Before the fallthrough below, or `--version` would print usage.
		if args[0] == "--version" || args[0] == "-version" {
			printVersion(args[1:])
			return
		}
		// Flags with no subcommand. Refused rather than assumed: guessing
		// `serve` is how someone means to reach the coordinator and quietly
		// starts a second, local one instead.
		fmt.Fprintf(os.Stderr, "shabadoo: %q is a flag, not a command.\n", args[0])
		if strings.HasPrefix(args[0], "--addr") {
			fmt.Fprintf(os.Stderr, "  did you mean: shabadoo serve %s\n", strings.Join(args, " "))
		}
		fmt.Fprintln(os.Stderr)
		usage()
		os.Exit(2)
	}

	switch args[0] {
	case "serve":
		runServe(args[1:])
	case "node":
		runNode(args[1:])
	case "hub":
		runHub(args[1:])
	case "pair":
		runPair(args[1:])
	case "renew":
		runRenew(args[1:])
	case "mcp":
		runMCP(args[1:])
	case "inbox":
		runInbox(args[1:])
	case "sessions":
		runSessions(args[1:])
	case "who":
		runWho(args[1:])
	case "folders":
		runFolders(args[1:])
	case "open":
		runOpen(args[1:])
	case "send":
		runSend(args[1:])
	case "keys":
		runKeys(args[1:])
	case "tail", "capture":
		runTail(args[1:])
	case "command":
		runCommand(args[1:])
	case "kill":
		runKill(args[1:])
	case "audit":
		runAudit(args[1:])
	case "mail":
		runMail(args[1:])
	case "devices":
		runDevices(args[1:])
	case "publish":
		runPublish(args[1:])
	case "upgrade":
		runUpgrade(args[1:])
	case "releases":
		runReleases(args[1:])
	case "disconnect":
		runDisconnect(args[1:])
	case "revoke":
		runRevoke(args[1:])
	case "attach":
		runAttach(args[1:])
	case "win":
		runWin(args[1:])
	case "boot":
		// A bare `boot` still opens the windows. The cron watchdog runs it every
		// ten minutes, so turning this into a noun-only namespace would stop
		// autostart on every host, silently.
		rest := args[1:]
		if len(rest) > 0 {
			switch rest[0] {
			case "list", "ls":
				runBootList(rest[1:])
				return
			case "add":
				runBootAdd(rest[1:])
				return
			case "remove", "rm":
				runBootRemove(rest[1:])
				return
			}
		}
		runBoot(rest)
	case "config":
		runConfig(args[1:])
	case "setup":
		runSetup(args[1:])
	case "doctor":
		runDoctor(args[1:])
	case "uninstall":
		runUninstall(args[1:])
	case "version", "--version":
		// The first question when a host misbehaves is which build it is
		// running, and `setup --service` installs whatever binary invoked it.
		printVersion(args[1:])
	case "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "shabadoo: unknown command %q\n\n", args[0])
		usage()
		os.Exit(2)
	}
}

// printVersion reports this build. The first line stays bare so anything
// parsing it keeps working; `--json` is what `setup` uses to ask an already
// installed binary how old it is, since a human-readable line is a bad contract
// between two copies of this program.
func printVersion(args []string) {
	if len(args) > 0 && args[0] == "--json" {
		json.NewEncoder(os.Stdout).Encode(map[string]string{
			"version": version,
			"built":   buildTime,
			// What this binary can run on. `publish` reads it rather than
			// guessing from the filename: a mislabelled upload gets sent to a
			// host that cannot run it, and a host that overwrites itself with
			// the wrong architecture cannot be told anything again.
			"platform": node.Platform(),
		})
		return
	}
	fmt.Println(version)
	if buildTime != "" {
		fmt.Println("built", buildTime)
	}
}

func isHelpFlag(s string) bool {
	return s == "-h" || s == "--help" || s == "-help"
}

func usage() {
	fmt.Fprint(os.Stderr, `shabadoo — coordinator, per-host agent, and installer for the Claude
launcher toolchain they drive.

Start here:
  shabadoo setup --service --coord URL   join an existing coordinator
  shabadoo attach                        start/attach this folder's session
  shabadoo sessions                      every session on every machine

usage:
  shabadoo hub [flags]            run the coordinator
  shabadoo node --coord URL       run this host's agent against a coordinator
  shabadoo serve [--addr ...]     standalone fallback for THIS host only

  shabadoo pair [--self]          enrol a device: prints a pairing URL + code
  shabadoo renew                  extend this machine's device token
  shabadoo mcp                    MCP server over stdio (launched by Claude)
  shabadoo sessions               list every node's sessions
  shabadoo folders [--node N]     list startable folders on a node
  shabadoo open [--node N] DIR    start a Claude session in a folder
  shabadoo tail [NAME]            print what is on a pane right now
  shabadoo send --window N TEXT   type text into a pane (and submit it)
  shabadoo keys --window N KEY... answer a dialog with raw keypresses
  shabadoo command --pane N /cmd  run a slash command in a pane
  shabadoo kill [NAME]            close a window (asks first)
  shabadoo audit [--limit N]      who drove which pane, newest last
  shabadoo mail [--session S]     session-to-session messages
  shabadoo inbox                  drain THIS session's mail (for a prompt hook)
  shabadoo devices                enrolled clients: scope, days left, push
  shabadoo publish dist/          upload binaries the coordinator can push
  shabadoo releases               what is published, and each node's platform
  shabadoo upgrade <node>|--all   replace a node's binary and restart it
  shabadoo disconnect <node>      cut a node's live session immediately
  shabadoo revoke <device>        sign out an enrolled browser/phone/CLI

  shabadoo attach [--dir D]       start/attach this folder's session (local)
  shabadoo win <cmd> [args]       local windows: list open close reopen clear
  shabadoo boot [--dry-run]       open one window per folder in the boot list
  shabadoo boot list|add|remove   which folders autostart
  shabadoo config [set|unset]     launcher settings (host label, claude flags)

  shabadoo setup [flags]          install the toolchain onto this machine
  shabadoo doctor                 report what setup would change (no writes)
  shabadoo uninstall [--all]      remove the services setup installed
  shabadoo version                print this binary's build stamp
  shabadoo help                   this message

Run 'shabadoo <command> -h' for a command's flags.
`)
}
