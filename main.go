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

	// `shabadoo` and `shabadoo --addr ...` still serve, so a flag-only
	// invocation keeps working now that subcommands exist.
	if len(args) == 0 {
		runServe(nil)
		return
	}
	if strings.HasPrefix(args[0], "-") {
		if isHelpFlag(args[0]) {
			usage()
			return
		}
		// Answered before the flag-only invocation falls through to serve —
		// otherwise `shabadoo --version` starts a server on port 8787.
		if args[0] == "--version" || args[0] == "-version" {
			printVersion(args[1:])
			return
		}
		runServe(args)
		return
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
	case "sessions":
		runSessions(args[1:])
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
		runBoot(args[1:])
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

usage:
  shabadoo [--addr HOST:PORT]     serve the local dashboard (default command)
  shabadoo serve [--addr ...]     same, explicitly — the standalone fallback
  shabadoo node --coord URL       run this host's agent against a coordinator
  shabadoo hub [flags]            run the coordinator

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
  shabadoo devices                enrolled clients: scope, days left, push
  shabadoo publish dist/          upload binaries the coordinator can push
  shabadoo releases               what is published, and each node's platform
  shabadoo upgrade <node>|--all   replace a node's binary and restart it
  shabadoo disconnect <node>      cut a node's live session immediately
  shabadoo revoke <device>        sign out an enrolled browser/phone/CLI

  shabadoo attach [--dir D]       start/attach this folder's session (local)
  shabadoo win <cmd> [args]       local windows: list open close reopen clear
  shabadoo boot [--list F]        open one window per folder in the boot list

  shabadoo setup [flags]          install the toolchain onto this machine
  shabadoo doctor                 report what setup would change (no writes)
  shabadoo uninstall [--all]      remove the services setup installed
  shabadoo version                print this binary's build stamp
  shabadoo help                   this message

Run 'shabadoo <command> -h' for a command's flags.
`)
}
