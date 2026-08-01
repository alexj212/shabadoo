package main

// The operator CLI: the human API, reachable from a terminal.
//
// These commands speak the same endpoints the dashboard does and authenticate
// the same way the iOS app will — with a device token. That is deliberate:
// enrolling the CLI exercises the token path on every use, so the credential
// the app depends on is not first tried on the day the app ships.

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// tokenFile holds the CLI's own device token. Same directory as the agent key,
// same reasoning: per-host credentials the operator owns, outside the repo.
func tokenFile() string { return filepath.Join(home(), ".config", "shabadoo", "cli_token") }

// coordFile records which coordinator this host's CLI talks to, so the flag is
// needed once rather than on every command.
func coordFile() string { return filepath.Join(home(), ".config", "shabadoo", "coord") }

// resolveCoord picks the coordinator URL: the flag, then $SHABADOO_COORD, then
// the remembered file. Nothing is guessed — a wrong default would send an
// operator's keystrokes to a machine they did not name.
func resolveCoord(flagVal string) (string, error) {
	if flagVal != "" {
		return strings.TrimRight(flagVal, "/"), nil
	}
	if v := os.Getenv("SHABADOO_COORD"); v != "" {
		return strings.TrimRight(v, "/"), nil
	}
	if b, err := os.ReadFile(coordFile()); err == nil {
		if v := strings.TrimSpace(string(b)); v != "" {
			return strings.TrimRight(v, "/"), nil
		}
	}
	return "", fmt.Errorf("no coordinator: pass --coord URL, set $SHABADOO_COORD, or run `shabadoo pair --coord URL` once")
}

// client is a small typed wrapper over the human API.
type client struct {
	coord string
	token string

	// renewed guards the opportunistic renewal in noteExpiry: once per
	// process, and never recursively — renew() calls do(), which would
	// otherwise land straight back in noteExpiry.
	renewed bool
}

func newClient(coordFlag string) (*client, error) {
	coord, err := resolveCoord(coordFlag)
	if err != nil {
		return nil, err
	}
	c := &client{coord: coord}
	// A missing token is not an error here: a coordinator running
	// --trust-network or --insecure-no-access admits the call anyway, and
	// failing early would break those postures. The deployment itself runs
	// --device-tokens, so in practice an unenrolled CLI gets a 403 from `do`,
	// which is where the "not enrolled" hint lives.
	if b, err := os.ReadFile(tokenFile()); err == nil {
		c.token = strings.TrimSpace(string(b))
	}
	return c, nil
}

// doStream is do() for a body that should not be buffered — a 15 MB binary
// read into memory to be marshalled would be pure waste — with a timeout that
// suits an upload rather than an API call.
func (c *client) doStream(method, path string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequest(method, c.coord+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return out, nil
}

// renewBelow is how close to expiry the CLI renews itself.
//
// A device token lasts 90 days and nothing renewed it, so a CLI that had not
// been used in three months would find itself locked out — and recovering from
// THAT means restarting the coordinator with --bootstrap, because an expired
// credential cannot renew itself. 30 days is two months of chances to be run
// once, and once is a full fresh term.
const renewBelow = 30 * 24 * time.Hour

// noteExpiry reads the coordinator's expiry header and renews if the credential
// is getting old.
//
// Opportunistic rather than scheduled: the CLI has no daemon, and a cron entry
// is one more thing to install on every host. Any command the operator runs
// keeps the credential alive, which is the same bargain the dashboard makes.
func (c *client) noteExpiry(resp *http.Response) {
	if c.renewed || c.token == "" {
		return
	}
	unix, err := strconv.ParseInt(resp.Header.Get("X-Shabadoo-Token-Expires"), 10, 64)
	if err != nil || unix == 0 {
		return
	}
	if time.Until(time.Unix(unix, 0)) > renewBelow {
		return
	}
	// Once per process, and never recursively: renew() calls do(), which would
	// otherwise land back here.
	c.renewed = true
	if _, err := c.do("POST", "/api/devices/renew", nil); err != nil {
		warnf("this credential expires soon and could not be renewed: %v", err)
		return
	}
	warnf("credential was close to expiry; renewed for another 90 days")
}

func (c *client) do(method, path string, body any) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.coord+path, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	c.noteExpiry(resp)
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 400 {
		msg := strings.TrimSpace(string(out))
		// 403 is what the identity middleware returns for a missing or unknown
		// credential; 401 comes from redeem. Both mean "this CLI is not enrolled
		// with that coordinator", which is the one error worth a hint.
		//
		// The hint must NOT suggest `pair --self`: that mints a code first, so
		// on an unenrolled CLI it hits this very error and the advice becomes
		// "run the command you just ran". Enrolling a first device is a
		// chicken-and-egg — minting needs a credential — so the only honest
		// advice is where to get a code from someone who has one.
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			// 401 is an AUTHENTICATION failure: this CLI has no usable
			// credential. The coordinator says why in WWW-Authenticate.
			hint := "   Get a pairing code from an already-enrolled device (`shabadoo pair`)\n" +
				"   or from the coordinator's log if it was started with --bootstrap, then:\n" +
				"     shabadoo pair --code <CODE> --coord " + c.coord
			if strings.Contains(resp.Header.Get("WWW-Authenticate"), `reason="expired"`) {
				hint = "   This CLI's token EXPIRED. Renew before expiry next time (`shabadoo renew`);\n" +
					"   an expired token cannot renew itself, so pair again:\n" +
					"     shabadoo pair --code <CODE> --coord " + c.coord
			}
			msg += "\n  (not enrolled with " + c.coord + ".\n" + hint + ")"

		case http.StatusForbidden:
			// 403 is an AUTHORIZATION failure: the credential is fine. Saying
			// "not enrolled" here would send someone to re-pair a working token.
			msg += "\n  (the token is valid but not permitted to do this — " +
				"a read-only device cannot drive a pane)"
		}
		return nil, fmt.Errorf("%s %s: %s", method, path, msg)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// pair
// ---------------------------------------------------------------------------

func runPair(args []string) {
	fset := flag.NewFlagSet("pair", flag.ExitOnError)
	coord := fset.String("coord", "", "coordinator base URL (remembered after the first run)")
	label := fset.String("label", "", "name for the device being paired")
	self := fset.Bool("self", false, "redeem the code immediately and store the token for this CLI")
	code := fset.String("code", "", "redeem this existing code instead of minting one (bootstrap)")
	qr := fset.Bool("qr", false, "print a QR code of the pairing URL, to scan with a phone")
	scope := fset.String("scope", "", "`read` for a device that can watch but not drive a pane")
	fset.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: shabadoo pair [flags]

Mints a single-use pairing code and prints the URL to open on the device being
enrolled. Codes expire after five minutes.

  shabadoo pair --label "Alex's iPhone"         # name the device you enrol
  shabadoo pair --self                          # enrol this CLI, store the token
  shabadoo pair --qr                            # print a QR to scan with a phone
  shabadoo pair --qr --scope read               # ...and it can watch, never type
  shabadoo pair --code ABC12345 --coord URL     # redeem a --bootstrap code

Minting requires an already-enrolled credential, which the first device on a
fresh coordinator does not have. That one is broken with --code, redeeming the
code `+"`hub --bootstrap`"+` printed to its log.

flags:
`)
		fset.PrintDefaults()
	}
	fset.Parse(args)

	c, err := newClient(*coord)
	if err != nil {
		fatalf("%v", err)
	}

	// --code is the bootstrap path: redeem a code somebody else minted, rather
	// than minting one. Without it, enrolling the FIRST device is impossible —
	// minting needs a credential, and getting a credential needs a mint. The
	// coordinator breaks that loop once with --bootstrap, printing a code to
	// its log; this is the other half of that escape hatch, and until it
	// existed the only way through was a hand-written curl.
	if *code != "" {
		name := *label
		if name == "" {
			name = "cli@" + hostLabel()
		}
		token, err := redeem(c, strings.ToUpper(strings.TrimSpace(*code)), name)
		if err != nil {
			fatalf("redeem: %v", err)
		}
		if err := writeAtomic(coordFile(), []byte(c.coord+"\n"), 0o644); err != nil {
			warnf("could not remember coordinator: %v", err)
		}
		if err := writeAtomic(tokenFile(), []byte(token+"\n"), 0o600); err != nil {
			fatalf("store token: %v", err)
		}
		fmt.Printf("paired %q — token stored in %s\n", name, tokenFile())
		return
	}

	// A code minted for somebody ELSE must carry a name. Without one the device
	// names itself, and the operator's device list — the list they revoke from —
	// fills up with whatever each client decided to call itself. --self is
	// exempt: it names itself cli@<host>, which is already the operator's own
	// machine.
	name := strings.TrimSpace(*label)
	if name == "" {
		if *self {
			name = "cli@" + hostLabel()
		} else {
			fatalf("--label is required: name the device you are enrolling, " +
				"e.g. --label \"Alex's iPhone\".\n" +
				"  It is what you will see in `shabadoo pair` output and have to " +
				"recognise when revoking.")
		}
	}

	body := map[string]any{"label": name}
	if *scope != "" {
		if *scope != "read" {
			fatalf("--scope: only `read` is a scope (omit it for full access)")
		}
		body["scope"] = "read"
	}
	raw, err := c.do("POST", "/api/devices/code", body)
	if err != nil {
		fatalf("mint code: %v", err)
	}
	var minted struct {
		Code      string `json:"code"`
		ExpiresIn int    `json:"expires_in"`
		Scope     string `json:"scope"`
		Label     string `json:"label"`
	}
	if err := json.Unmarshal(raw, &minted); err != nil {
		fatalf("mint code: %s", raw)
	}

	// Remember the coordinator so later commands need no flag.
	if err := writeAtomic(coordFile(), []byte(c.coord+"\n"), 0o644); err != nil {
		warnf("could not remember coordinator: %v", err)
	}

	if *self {
		token, err := redeem(c, minted.Code, name)
		if err != nil {
			fatalf("redeem: %v", err)
		}
		if err := writeAtomic(tokenFile(), []byte(token+"\n"), 0o600); err != nil {
			fatalf("store token: %v", err)
		}
		fmt.Printf("paired %q — token stored in %s\n", name, tokenFile())
		return
	}

	// The code rides in the fragment so it is never sent to the server as part
	// of the request line: not in access logs, not in a Referer.
	// The label rides in the fragment alongside the code, so the device can show
	// "pairing as: Alex's iPhone" before the user confirms. It is display only —
	// the coordinator uses the label it recorded at mint time, so a tampered
	// fragment renames nothing.
	pairURL := fmt.Sprintf("%s/pair#code=%s", c.coord, minted.Code)
	if minted.Label != "" {
		// %20 for spaces, not "+". QueryEscape emits "+", which only means
		// "space" under the query-string convention — a client that percent-
		// decodes the fragment without knowing that convention reads a literal
		// plus. %20 decodes to a space under BOTH readings, so half the parsing
		// trap simply stops existing. (The other half — a label containing an
		// encoded & or = — is the client's to handle; see docs/mobile-client.md.)
		pairURL += "&label=" + strings.ReplaceAll(url.QueryEscape(minted.Label), "+", "%20")
	}

	if *qr {
		grid, err := Encode(pairURL)
		if err != nil {
			warnf("could not render a QR (%v) — use the URL below", err)
		} else {
			fmt.Print("\n" + RenderTerminal(grid))
		}
	}

	fmt.Printf("device: %s\n", minted.Label)
	kind := "full access"
	if minted.Scope == "read" {
		kind = "READ-ONLY — this device can watch but never type into a pane"
	}
	fmt.Printf("scope: %s\n", kind)

	fmt.Printf(`pairing code: %s   (valid %ds, single use)

Open this on the device:
  %s

Or enter the code manually at %s/pair
`, minted.Code, minted.ExpiresIn, pairURL, c.coord)
	// A QR of that URL would be the nicer phone flow. It is deliberately absent:
	// encoding one needs a dependency or a few hundred lines of QR encoder, and
	// this project is stdlib-only. Left as a --qr flag to add if typing annoys.
}

func redeem(c *client, code, label string) (string, error) {
	raw, err := c.do("POST", "/api/devices/redeem", map[string]any{"code": code, "label": label})
	if err != nil {
		return "", err
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Token == "" {
		return "", fmt.Errorf("unexpected response: %s", raw)
	}
	return out.Token, nil
}

// resolveNode fills in --node when it was omitted.
//
// With one node connected there is nothing to choose, and making someone name
// it on every command is friction for the common self-hosted case. With
// several, it refuses: picking one would eventually type into the wrong
// machine's pane.
func resolveNode(c *client, flagVal string) (string, error) {
	if flagVal != "" {
		return flagVal, nil
	}
	raw, err := c.do("GET", "/api/sessions", nil)
	if err != nil {
		return "", err
	}
	var out struct {
		Nodes []struct {
			Node   string `json:"node"`
			Online bool   `json:"online"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	var online []string
	for _, n := range out.Nodes {
		if n.Online {
			online = append(online, n.Node)
		}
	}
	switch len(online) {
	case 1:
		return online[0], nil
	case 0:
		return "", fmt.Errorf("no agents are connected")
	default:
		sort.Strings(online)
		return "", fmt.Errorf("several nodes connected (%s) — pass --node", strings.Join(online, ", "))
	}
}

// ---------------------------------------------------------------------------
// sessions / folders / open
// ---------------------------------------------------------------------------

type cliSession struct {
	Alias      string `json:"alias"`
	Window     string `json:"window"`
	CWD        string `json:"cwd"`
	Status     string `json:"status"`
	Command    string `json:"command"`
	InputState string `json:"input_state"`
	Pending    int    `json:"pending"`
	Note       string `json:"note"`

	TmuxSession string `json:"tmux_session"`
	Index       int    `json:"index"`
}

// pane is one addressed window: everything a write needs to name it.
type pane struct {
	node    string
	session string
	window  int
	alias   string
}

func (p pane) String() string {
	return fmt.Sprintf("%s (%s:%d%s)", p.alias, p.session, p.window, nodeSuffix(p.node))
}

// fetchSessions returns every node's windows.
func fetchSessions(c *client) ([]cliNode, error) {
	raw, err := c.do("GET", "/api/sessions", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Nodes []cliNode `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return out.Nodes, nil
}

type cliNode struct {
	Node     string       `json:"node"`
	Online   bool         `json:"online"`
	Version  string       `json:"version"`
	Sessions []cliSession `json:"sessions"`
}

// nameAndFlags pulls a leading positional NAME out of an argument list and
// parses the rest as flags.
//
// Go's flag package stops at the first non-flag argument, so `tail homelab
// --lines 200` parses NOTHING after "homelab" — the flags end up in the name
// and the error message reads "no session matching \"homelab --lines 200\"".
// Both orders have to work; the operator does not know where the parser stops.
func nameAndFlags(fset *flag.FlagSet, args []string) string {
	if pos := argsAndFlags(fset, args); len(pos) > 0 {
		return pos[0]
	}
	return ""
}

// argsAndFlags is nameAndFlags for a command taking several positionals, and
// tolerates flags anywhere among them.
//
// `upgrade mac --version probe1` parsed as three NODE NAMES and reported
// `node "--version" is not connected`, because flag.Parse stops dead at the
// first non-flag argument. Anything that takes a list has the same problem.
func argsAndFlags(fset *flag.FlagSet, args []string) []string {
	var positional []string
	for {
		fset.Parse(args)
		rest := fset.Args()
		if len(rest) == 0 {
			return positional
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

// resolvePane turns what the operator typed into one window.
//
// Two ways to say it, because they suit different moments: `--window 7` when
// you already have the number in front of you, and a NAME when you do not —
// which is most of the time, since window indices shift as sessions come and
// go and nobody remembers them.
//
// Name matching follows the folder rule exactly: exact match first, then
// substring, and an ambiguous match is an error rather than a guess. Guessing
// here types into the wrong project's Claude session.
func resolvePane(c *client, nodeFlag, sessionFlag string, windowFlag int, name string) (pane, error) {
	if windowFlag >= 0 {
		target, err := resolveNode(c, nodeFlag)
		if err != nil {
			return pane{}, err
		}
		return pane{node: target, session: sessionFlag, window: windowFlag,
			alias: fmt.Sprintf("%s:%d", sessionFlag, windowFlag)}, nil
	}
	if name == "" {
		return pane{}, fmt.Errorf("name a session, or give --window N — see `shabadoo sessions`")
	}

	nodes, err := fetchSessions(c)
	if err != nil {
		return pane{}, err
	}
	return matchPane(nodes, nodeFlag, name)
}

// matchPane is resolvePane's rule, split out so it is testable without a
// coordinator: the matching is the part that can be wrong, and the fetch is not.
func matchPane(nodes []cliNode, nodeFlag, name string) (pane, error) {
	var exact, partial []pane
	for _, n := range nodes {
		if nodeFlag != "" && n.Node != nodeFlag {
			continue
		}
		for _, s := range n.Sessions {
			p := pane{node: n.Node, session: s.TmuxSession, window: s.Index, alias: s.Alias}
			switch {
			case s.Alias == name:
				exact = append(exact, p)
			// Substring on the alias and on the FOLDER NAME — not the whole
			// path. Matching the full path means "home" hits every session on
			// a Linux host through /home/<user>/…, which turns a useful
			// shorthand into a permanent ambiguity error.
			case strings.Contains(s.Alias, name) || strings.Contains(filepath.Base(s.CWD), name):
				partial = append(partial, p)
			}
		}
	}
	matches := exact
	if len(matches) == 0 {
		matches = partial
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return pane{}, fmt.Errorf("no session matching %q — try `shabadoo sessions`", name)
	default:
		var lines []string
		for _, m := range matches {
			lines = append(lines, m.String())
		}
		sort.Strings(lines)
		return pane{}, fmt.Errorf("%q is ambiguous:\n  %s", name, strings.Join(lines, "\n  "))
	}
}

func runSessions(args []string) {
	fset := flag.NewFlagSet("sessions", flag.ExitOnError)
	coord := fset.String("coord", "", "coordinator base URL")
	fset.Parse(args)

	c, err := newClient(*coord)
	if err != nil {
		fatalf("%v", err)
	}
	nodes, err := fetchSessions(c)
	if err != nil {
		fatalf("%v", err)
	}

	waiting := 0
	for _, n := range nodes {
		state := "offline"
		if n.Online {
			state = "online"
		}
		fmt.Printf("%s (%s, %d session%s)\n", n.Node, state, len(n.Sessions), plural(len(n.Sessions)))
		for _, s := range n.Sessions {
			mark := " "
			if s.InputState == "dialog" {
				mark = "!" // waiting on a prompt — the only row that needs a human
				waiting++
			}
			mail := ""
			if s.Pending > 0 {
				mail = fmt.Sprintf("  %d unread", s.Pending)
			}
			fmt.Printf("  %s %-28s %-10s %-8s %s%s\n", mark, s.Alias, s.Window, s.Status, s.CWD, mail)
			// The session's own words go on their own line: they are prose, and
			// squeezing them into a column would truncate the useful half.
			if s.Note != "" {
				fmt.Printf("      ↳ %s\n", s.Note)
			}
		}
	}
	if waiting > 0 {
		// Read before answering: `tail` first, deliberately. Sending Enter to a
		// prompt you have not seen is how "yes" reaches a question that was
		// asking about deleting something.
		fmt.Printf("\n! %d session%s waiting on a prompt\n"+
			"    shabadoo tail NAME     # see what it is asking\n"+
			"    shabadoo keys --window W Enter\n",
			waiting, plural(waiting))
	}
}

func runFolders(args []string) {
	fset := flag.NewFlagSet("folders", flag.ExitOnError)
	coord := fset.String("coord", "", "coordinator base URL")
	node := fset.String("node", "", "node to list folders on")
	fset.Parse(args)

	c, err := newClient(*coord)
	if err != nil {
		fatalf("%v", err)
	}
	target, err := resolveNode(c, *node)
	if err != nil {
		fatalf("%v", err)
	}
	list, err := fetchFolders(c, target)
	if err != nil {
		fatalf("%v", err)
	}
	for _, f := range list {
		mark := " "
		if f.Open {
			mark = "*" // already has a window; opening again would duplicate it
		}
		fmt.Printf(" %s %-10s %s\n", mark, f.Source, f.Path)
	}
}

type cliFolder struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Source string `json:"source"`
	Open   bool   `json:"open"`
}

func fetchFolders(c *client, node string) ([]cliFolder, error) {
	raw, err := c.do("GET", "/api/folders?node="+url.QueryEscape(node), nil)
	if err != nil {
		return nil, err
	}
	var list []cliFolder
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return list, nil
}

func runOpen(args []string) {
	fset := flag.NewFlagSet("open", flag.ExitOnError)
	coord := fset.String("coord", "", "coordinator base URL")
	node := fset.String("node", "", "node to open the session on")
	fset.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: shabadoo open [--node NODE] <folder>

Starts a Claude session in a folder on a node. The folder may be an absolute
path or a unique substring of one this node knows (see `+"`shabadoo folders`"+`).

flags:
`)
		fset.PrintDefaults()
	}
	fset.Parse(args)

	if fset.NArg() != 1 {
		fset.Usage()
		os.Exit(2)
	}
	c, err := newClient(*coord)
	if err != nil {
		fatalf("%v", err)
	}

	target, err := resolveNode(c, *node)
	if err != nil {
		fatalf("%v", err)
	}
	path, err := resolveFolder(c, target, fset.Arg(0))
	if err != nil {
		fatalf("%v", err)
	}
	if _, err := c.do("POST", "/api/open", map[string]any{"node": target, "path": path}); err != nil {
		fatalf("open: %v", err)
	}
	fmt.Printf("opened %s%s\n", path, nodeSuffix(target))
}

// resolveFolder turns what someone typed into a path the node knows.
//
// An absolute path is taken as given — the caller may be opening something with
// no history. Anything else is matched against the node's folder list, and an
// ambiguous match is an error rather than a guess: opening a session in the
// wrong project is not something to be casual about.
func resolveFolder(c *client, node, want string) (string, error) {
	if strings.HasPrefix(want, "/") {
		return want, nil
	}
	list, err := fetchFolders(c, node)
	if err != nil {
		return "", err
	}
	var exact, partial []string
	for _, f := range list {
		if f.Name == want {
			exact = append(exact, f.Path)
		} else if strings.Contains(f.Path, want) {
			partial = append(partial, f.Path)
		}
	}
	matches := exact
	if len(matches) == 0 {
		matches = partial
	}
	sort.Strings(matches)

	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf("no folder matching %q%s — try `shabadoo folders`", want, nodeSuffix(node))
	default:
		return "", fmt.Errorf("%q is ambiguous:\n  %s", want, strings.Join(matches, "\n  "))
	}
}

// ---------------------------------------------------------------------------
// send / keys
// ---------------------------------------------------------------------------

func runSend(args []string) {
	fset := flag.NewFlagSet("send", flag.ExitOnError)
	coord := fset.String("coord", "", "coordinator base URL")
	node := fset.String("node", "", "node owning the pane")
	session := fset.String("session", "claude", "tmux session")
	window := fset.Int("window", -1, "tmux window index")
	pane := fset.String("pane", "", "session name, instead of --window")
	noEnter := fset.Bool("no-enter", false, "type the text but do not submit it")
	fset.Parse(args)

	if (*window < 0 && *pane == "") || fset.NArg() == 0 {
		fmt.Fprintln(os.Stderr,
			"usage: shabadoo send --pane NAME|--window N [--node NODE] <text>")
		os.Exit(2)
	}
	c, err := newClient(*coord)
	if err != nil {
		fatalf("%v", err)
	}
	// By name as well as by index. The index form stays because it is what you
	// have in front of you when reading `sessions`, but a name is what survives
	// a window being opened or closed in between.
	p, err := resolvePane(c, *node, *session, *window, *pane)
	if err != nil {
		fatalf("%v", err)
	}
	text := strings.Join(fset.Args(), " ")
	_, err = c.do("POST", "/api/send", map[string]any{
		"node": p.node, "session": p.session, "window": p.window,
		"text": text, "enter": !*noEnter,
	})
	if err != nil {
		fatalf("send: %v", err)
	}
	fmt.Printf("sent to %s\n", p)
}

func runKeys(args []string) {
	fset := flag.NewFlagSet("keys", flag.ExitOnError)
	coord := fset.String("coord", "", "coordinator base URL")
	node := fset.String("node", "", "node owning the pane")
	session := fset.String("session", "claude", "tmux session")
	window := fset.Int("window", -1, "tmux window index")
	pane := fset.String("pane", "", "session name, instead of --window")
	fset.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: shabadoo keys --pane NAME|--window N [--node NODE] <key>...

Sends raw keypresses — how a dialog gets answered, since text typed into a
modal is swallowed. Keys are tmux key names: Enter, Escape, Up, Down, 1, y.

  shabadoo keys --pane homelab Enter    # accept the highlighted option
  shabadoo keys --window 3 2 Enter      # choose option 2

flags:
`)
		fset.PrintDefaults()
	}
	fset.Parse(args)

	if (*window < 0 && *pane == "") || fset.NArg() == 0 {
		fset.Usage()
		os.Exit(2)
	}
	c, err := newClient(*coord)
	if err != nil {
		fatalf("%v", err)
	}
	p, err := resolvePane(c, *node, *session, *window, *pane)
	if err != nil {
		fatalf("%v", err)
	}
	_, err = c.do("POST", "/api/keys", map[string]any{
		"node": p.node, "session": p.session, "window": p.window, "keys": fset.Args(),
	})
	if err != nil {
		fatalf("keys: %v", err)
	}
	fmt.Printf("sent %s to %s\n", strings.Join(fset.Args(), " "), p)
}

func nodeSuffix(node string) string {
	if node == "" {
		return ""
	}
	return " on " + node
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func warnf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "shabadoo: "+format+"\n", a...)
}

// runRenew slides this CLI's own device token forward. Without it the 90-day
// TTL has exactly one recovery path — restarting the coordinator with
// --bootstrap — which is a trip to a terminal, and impossible from the phone
// that just expired.
func runRenew(args []string) {
	fset := flag.NewFlagSet("renew", flag.ExitOnError)
	coord := fset.String("coord", "", "coordinator base URL (remembered after pairing)")
	fset.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: shabadoo renew [--coord URL]

Extends this machine's device token by a full term. The token itself does NOT
change, so nothing needs re-pairing.

Renew while the token still works: an expired one cannot renew itself, and
recovering from that means re-running the coordinator with --bootstrap.

flags:
`)
		fset.PrintDefaults()
	}
	fset.Parse(args)

	c, err := newClient(*coord)
	if err != nil {
		fatalf("%v", err)
	}
	raw, err := c.do("POST", "/api/devices/renew", map[string]any{})
	if err != nil {
		fatalf("renew: %v", err)
	}
	var out struct {
		Expires int64  `json:"expires"`
		Label   string `json:"label"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		fatalf("renew: %s", raw)
	}
	exp := time.Unix(out.Expires, 0)
	fmt.Printf("renewed %q — valid until %s (%d days)\n",
		out.Label, exp.Format("2006-01-02"), int(time.Until(exp).Hours()/24))
}

// ---------------------------------------------------------------------------
// read commands
// ---------------------------------------------------------------------------
//
// The CLI could drive a pane before it could look at one, which made the
// natural sequence impossible from a terminal: see that something is waiting,
// read what it is asking, answer it. Only the first and last steps existed, and
// answering a prompt you have not read is how "yes" reaches a question about
// deleting something.

func runTail(args []string) {
	fset := flag.NewFlagSet("tail", flag.ExitOnError)
	coord := fset.String("coord", "", "coordinator base URL")
	node := fset.String("node", "", "node owning the pane")
	session := fset.String("session", "claude", "tmux session (with --window)")
	window := fset.Int("window", -1, "tmux window index")
	lines := fset.Int("lines", 40, "how many lines to print, from the bottom")
	scrollback := fset.Int("scrollback", 0, "extra history to fetch above the visible pane")
	color := fset.Bool("color", false, "keep the pane's ANSI colour")
	fset.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: shabadoo tail [NAME] [--window N] [--node NODE]

Prints what is on a pane right now — the terminal's answer to "what is it
asking?", and the step that belongs between `+"`shabadoo sessions`"+` showing a
session waiting and `+"`shabadoo keys`"+` answering it.

  shabadoo tail homelab            # by name: exact, then substring
  shabadoo tail --window 7         # by index, when you already have it
  shabadoo tail homelab --lines 200        # last 200 lines
  shabadoo tail homelab --scrollback 500   # reach further back than the screen

flags:
`)
		fset.PrintDefaults()
	}
	name := nameAndFlags(fset, args)

	c, err := newClient(*coord)
	if err != nil {
		fatalf("%v", err)
	}
	p, err := resolvePane(c, *node, *session, *window, name)
	if err != nil {
		fatalf("%v", err)
	}
	q := url.Values{
		"node": {p.node}, "session": {p.session},
		"window": {strconv.Itoa(p.window)}, "lines": {strconv.Itoa(*scrollback)},
	}
	if *color {
		q.Set("color", "1")
	}
	raw, err := c.do("GET", "/api/capture?"+q.Encode(), nil)
	if err != nil {
		fatalf("capture: %v", err)
	}
	// The endpoint answers with text, not JSON — it is a pane's bytes.
	//
	// `lines` here means what it means in tail(1): the last N. The endpoint's
	// own `lines` is scrollback DEPTH (how far above the visible pane to
	// capture), which is the right knob for the dashboard's viewer and the
	// wrong one for a command called tail — `tail --lines 4` returning a full
	// screen would be a plain lie. So depth is `--scrollback` and the trim
	// happens here.
	out := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	// Trailing blank lines are the unused bottom of the pane; keeping them
	// would spend the whole budget on whitespace.
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	if *lines > 0 && len(out) > *lines {
		out = out[len(out)-*lines:]
	}
	fmt.Println(strings.Join(out, "\n"))
}

func runAudit(args []string) {
	fset := flag.NewFlagSet("audit", flag.ExitOnError)
	coord := fset.String("coord", "", "coordinator base URL")
	limit := fset.Int("limit", 40, "how many entries, newest first")
	fset.Parse(args)

	c, err := newClient(*coord)
	if err != nil {
		fatalf("%v", err)
	}
	raw, err := c.do("GET", "/api/audit?limit="+strconv.Itoa(*limit), nil)
	if err != nil {
		fatalf("audit: %v", err)
	}
	var out struct {
		Entries []struct {
			At     int64  `json:"at"`
			Actor  string `json:"actor"`
			Action string `json:"action"`
			Target string `json:"target"`
			Detail string `json:"detail"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		fatalf("decode: %v", err)
	}
	// Oldest first: a log read in a terminal scrolls, and the newest line
	// should end up next to the prompt rather than off the top of the screen.
	for i := len(out.Entries) - 1; i >= 0; i-- {
		e := out.Entries[i]
		fmt.Printf("%s  %-28s %-14s %-22s %s\n",
			time.Unix(e.At, 0).Format("01-02 15:04:05"),
			trunc(e.Actor, 28), e.Action, trunc(e.Target, 22), e.Detail)
	}
	if len(out.Entries) == 0 {
		fmt.Println("(nothing recorded yet)")
	}
}

func runMail(args []string) {
	fset := flag.NewFlagSet("mail", flag.ExitOnError)
	coord := fset.String("coord", "", "coordinator base URL")
	limit := fset.Int("limit", 30, "how many messages")
	session := fset.String("session", "", "one session's thread, instead of the last 24h")
	fset.Parse(args)

	c, err := newClient(*coord)
	if err != nil {
		fatalf("%v", err)
	}
	q := url.Values{"limit": {strconv.Itoa(*limit)}}
	if *session != "" {
		q.Set("session", *session)
	}
	raw, err := c.do("GET", "/api/messages?"+q.Encode(), nil)
	if err != nil {
		fatalf("mail: %v", err)
	}
	var out struct {
		Messages []struct {
			FromSession string `json:"from_session"`
			ToSession   string `json:"to_session"`
			Topic       string `json:"topic"`
			Title       string `json:"title"`
			Body        string `json:"body"`
			CreatedAt   int64  `json:"created_at"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		fatalf("decode: %v", err)
	}
	for i := len(out.Messages) - 1; i >= 0; i-- {
		m := out.Messages[i]
		to := m.ToSession
		if to == "" {
			to = "#" + m.Topic
			if m.Topic == "" {
				to = "everyone"
			}
		}
		fmt.Printf("%s  %s → %s\n", time.Unix(m.CreatedAt, 0).Format("01-02 15:04"),
			shortSession(m.FromSession), shortSession(to))
		if m.Title != "" {
			fmt.Printf("    %s\n", m.Title)
		}
		// One line of body. The full text is what the recipient drains; this is
		// a listing, and a wall of wrapped prose would bury the next message.
		fmt.Printf("    %s\n", trunc(strings.Join(strings.Fields(m.Body), " "), 100))
	}
	if len(out.Messages) == 0 {
		fmt.Println("(no messages)")
	}
}

func runDevices(args []string) {
	fset := flag.NewFlagSet("devices", flag.ExitOnError)
	coord := fset.String("coord", "", "coordinator base URL")
	fset.Parse(args)

	c, err := newClient(*coord)
	if err != nil {
		fatalf("%v", err)
	}
	raw, err := c.do("GET", "/api/devices", nil)
	if err != nil {
		fatalf("devices: %v", err)
	}
	var out struct {
		Devices []struct {
			ID      string    `json:"ID"`
			Label   string    `json:"Label"`
			Scope   string    `json:"Scope"`
			Expires time.Time `json:"Expires"`
			Push    bool      `json:"push"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		fatalf("decode: %v", err)
	}
	now := time.Now()
	for _, d := range out.Devices {
		scope := d.Scope
		if scope == "" {
			scope = "full"
		}
		push := ""
		if d.Push {
			push = "  push"
		}
		// Days remaining rather than a date: the question anyone asks of a
		// 90-day credential is "how long have I got", not "when was it minted".
		fmt.Printf("%-32s %-5s %3.0fd  %s%s\n", trunc(d.Label, 32), scope,
			d.Expires.Sub(now).Hours()/24, shortID(d.ID), push)
	}
	if len(out.Devices) == 0 {
		fmt.Println("(no devices enrolled)")
	}
}

func runCommand(args []string) {
	fset := flag.NewFlagSet("command", flag.ExitOnError)
	coord := fset.String("coord", "", "coordinator base URL")
	node := fset.String("node", "", "node owning the pane")
	session := fset.String("session", "claude", "tmux session (with --window)")
	window := fset.Int("window", -1, "tmux window index")
	pane := fset.String("pane", "", "session name, instead of --window")
	fset.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: shabadoo command --pane NAME|--window N <slash-command>

Clears the input line, types a slash command and submits it.

  shabadoo command --pane homelab /clear
  shabadoo command --pane homelab /remote-control

flags:
`)
		fset.PrintDefaults()
	}
	fset.Parse(args)
	if fset.NArg() == 0 {
		fset.Usage()
		os.Exit(2)
	}

	c, err := newClient(*coord)
	if err != nil {
		fatalf("%v", err)
	}
	p, err := resolvePane(c, *node, *session, *window, *pane)
	if err != nil {
		fatalf("%v", err)
	}
	cmd := strings.Join(fset.Args(), " ")
	if _, err := c.do("POST", "/api/command", map[string]any{
		"node": p.node, "session": p.session, "window": p.window, "command": cmd,
	}); err != nil {
		fatalf("command: %v", err)
	}
	fmt.Printf("ran %s in %s\n", cmd, p)
}

func runKill(args []string) {
	fset := flag.NewFlagSet("kill", flag.ExitOnError)
	coord := fset.String("coord", "", "coordinator base URL")
	node := fset.String("node", "", "node owning the pane")
	session := fset.String("session", "claude", "tmux session (with --window)")
	window := fset.Int("window", -1, "tmux window index")
	yes := fset.Bool("yes", false, "skip the confirmation")
	name := nameAndFlags(fset, args)

	c, err := newClient(*coord)
	if err != nil {
		fatalf("%v", err)
	}
	p, err := resolvePane(c, *node, *session, *window, name)
	if err != nil {
		fatalf("%v", err)
	}
	// Killing a window ends whatever that Claude session was in the middle of,
	// and by NAME it is one substring away from ending the wrong one. The
	// dashboard has a visible row to click; a terminal has only what was typed.
	if !*yes {
		fmt.Printf("kill %s? [y/N] ", p)
		var answer string
		fmt.Scanln(&answer)
		if !strings.HasPrefix(strings.ToLower(answer), "y") {
			fmt.Println("cancelled")
			return
		}
	}
	if _, err := c.do("POST", "/api/kill", map[string]any{
		"node": p.node, "session": p.session, "window": p.window,
	}); err != nil {
		fatalf("kill: %v", err)
	}
	fmt.Printf("killed %s\n", p)
}

// shortSession drops the `claude-` prefix and the trailing 8-hex from a session
// id. The hash is what makes two sessions on one project distinguishable and is
// exactly what a person reading a list does not need.
func shortSession(id string) string {
	if s, ok := strings.CutPrefix(id, "human:"); ok {
		return s
	}
	s := strings.TrimPrefix(id, "claude-")
	if i := strings.LastIndex(s, "-"); i > 0 && len(s)-i == 9 {
		return s[:i]
	}
	return s
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

// ---------------------------------------------------------------------------
// publish / upgrade
// ---------------------------------------------------------------------------
//
// Upgrading a node was scp plus a service restart, per host, per platform, by
// hand — the ritual that produces the version skew the build stamps exist to
// make visible. This is that ritual, as two commands.
//
// Deliberately operator-triggered rather than automatic. The coordinator can
// already drive every pane on every node, so the trust to push a binary was
// always there; what is not there is a good answer to "the new build is broken
// on all four hosts at once". A human deciding when, one node at a time, is the
// answer.

func runPublish(args []string) {
	fset := flag.NewFlagSet("publish", flag.ExitOnError)
	coord := fset.String("coord", "", "coordinator base URL")
	version := fset.String("version", "", "version to publish as (default: from the filename or `shabadoo version`)")
	fset.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: shabadoo publish <file-or-dir>...

Uploads binaries to the coordinator so nodes can be upgraded to them. The
platform is read from each file by running it, not guessed from its name — a
mislabelled upload would be sent to a host that cannot run it.

  make dist && shabadoo publish dist/     # every platform at once
  shabadoo publish dist/shabadoo-darwin-arm64

flags:
`)
		fset.PrintDefaults()
	}
	paths := argsAndFlags(fset, args)
	if len(paths) == 0 {
		fset.Usage()
		os.Exit(2)
	}

	c, err := newClient(*coord)
	if err != nil {
		fatalf("%v", err)
	}

	var files []string
	for _, arg := range paths {
		info, err := os.Stat(arg)
		if err != nil {
			fatalf("%v", err)
		}
		if !info.IsDir() {
			files = append(files, arg)
			continue
		}
		entries, err := os.ReadDir(arg)
		if err != nil {
			fatalf("%v", err)
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasPrefix(e.Name(), binName) {
				files = append(files, filepath.Join(arg, e.Name()))
			}
		}
	}
	sort.Strings(files)
	if len(files) == 0 {
		fatalf("no shabadoo binaries found in %s", strings.Join(paths, " "))
	}

	for _, f := range files {
		ver, platform, err := binaryStamp(f)
		if err != nil {
			warnf("%s: %v", filepath.Base(f), err)
			continue
		}
		if *version != "" {
			ver = *version
		}
		rel, err := publishFile(c, f, ver, platform)
		if err != nil {
			warnf("%s: %v", filepath.Base(f), err)
			continue
		}
		fmt.Printf("published %s %s  %s  %.1f MB\n",
			rel.Version, rel.Platform, rel.SHA256[:12], float64(rel.Size)/(1<<20))
	}
}

type cliRelease struct {
	Version  string `json:"version"`
	Platform string `json:"platform"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
}

// binaryStamp asks a binary what it is.
//
// Two ways, in order of trust:
//
//  1. Run it (`version --json`). Definitive — it is the binary answering.
//  2. Read its Go build info (`go version -m`), which records both the
//     `-ldflags` stamp and GOOS/GOARCH. Needed for a cross-compiled sibling,
//     which cannot be executed on the machine that built it.
//
// It deliberately does NOT fall back to the filename plus this CLI's own
// version, which is what it did first and what got a darwin build published
// under the version of whatever was in ~/bin — a binary labelled as something
// it is not, then shipped to a host on that label. Guessing the version of a
// file you are about to make every node run is not a thing to do quietly.
func binaryStamp(path string) (version, platform string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	out, runErr := exec.CommandContext(ctx, path, "version", "--json").Output()
	if runErr == nil {
		var got struct {
			Version  string `json:"version"`
			Platform string `json:"platform"`
		}
		if json.Unmarshal(out, &got) == nil && got.Version != "" && got.Platform != "" {
			return got.Version, got.Platform, nil
		}
	}
	return buildInfoStamp(ctx, path)
}

// buildInfoStamp reads a Go binary's embedded build settings.
//
// `go version -m` prints, among others:
//
//	build   -ldflags="-X main.version=8205036 -X main.buildTime=..."
//	build   GOOS=darwin
//	build   GOARCH=arm64
//
// which is exactly the three facts publishing needs, from the file itself.
func buildInfoStamp(ctx context.Context, path string) (version, platform string, err error) {
	out, err := exec.CommandContext(ctx, "go", "version", "-m", path).Output()
	if err != nil {
		return "", "", fmt.Errorf("cannot determine version/platform: it does not run "+
			"here and `go version -m` failed (%v) — pass --version, or publish from "+
			"a machine with Go", err)
	}
	var goos, goarch string
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "build" {
			continue
		}
		switch {
		case strings.HasPrefix(f[1], "GOOS="):
			goos = strings.TrimPrefix(f[1], "GOOS=")
		case strings.HasPrefix(f[1], "GOARCH="):
			goarch = strings.TrimPrefix(f[1], "GOARCH=")
		case strings.HasPrefix(f[1], "-ldflags="):
			version = ldflagValue(strings.Join(f[1:], " "), "main.version")
		}
	}
	if version == "" || goos == "" || goarch == "" {
		return "", "", fmt.Errorf("build info is incomplete (version=%q %s/%s) — "+
			"an unstamped build; use `make dist`, or pass --version", version, goos, goarch)
	}
	return version, goos + "/" + goarch, nil
}

// ldflagValue pulls `-X name=value` out of a recorded -ldflags string.
func ldflagValue(ldflags, name string) string {
	_, rest, found := strings.Cut(ldflags, "-X "+name+"=")
	if !found {
		return ""
	}
	// Ends at the next space or the closing quote of the whole -ldflags value.
	return strings.TrimRight(strings.Fields(rest)[0], `"`)
}

func publishFile(c *client, path, version, platform string) (cliRelease, error) {
	f, err := os.Open(path)
	if err != nil {
		return cliRelease{}, err
	}
	defer f.Close()

	q := url.Values{"version": {version}, "platform": {platform}}
	raw, err := c.doStream("POST", "/api/releases?"+q.Encode(), f)
	if err != nil {
		return cliRelease{}, err
	}
	var rel cliRelease
	if err := json.Unmarshal(raw, &rel); err != nil {
		return cliRelease{}, fmt.Errorf("decode: %w", err)
	}
	return rel, nil
}

func runUpgrade(args []string) {
	fset := flag.NewFlagSet("upgrade", flag.ExitOnError)
	coord := fset.String("coord", "", "coordinator base URL")
	version := fset.String("version", "", "version to install (default: the newest published for that platform)")
	all := fset.Bool("all", false, "upgrade every connected node, one at a time")
	fset.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: shabadoo upgrade <node>... | --all

Tells a node to replace its own binary with one published to the coordinator
and restart. The node verifies the checksum AND runs the staged binary before
swapping, so a wrong-architecture or truncated download is refused rather than
installed.

  shabadoo publish dist/ && shabadoo upgrade --all
  shabadoo upgrade mac --version a8a7cfc

flags:
`)
		fset.PrintDefaults()
	}
	targets := argsAndFlags(fset, args)

	c, err := newClient(*coord)
	if err != nil {
		fatalf("%v", err)
	}

	if *all {
		nodes, err := fetchSessions(c)
		if err != nil {
			fatalf("%v", err)
		}
		targets = nil
		for _, n := range nodes {
			if n.Online {
				targets = append(targets, n.Node)
			}
		}
	}
	if len(targets) == 0 {
		fset.Usage()
		os.Exit(2)
	}

	failed := 0
	for _, target := range targets {
		// One at a time, and each is confirmed back online before the next is
		// touched. Upgrading in parallel would turn one bad build into every
		// node offline simultaneously, which is the failure this whole design
		// is arranged to avoid.
		if err := upgradeOne(c, target, *version); err != nil {
			failed++
			fmt.Printf("%-8s FAILED  %v\n", target, err)
			continue
		}
	}
	if failed > 0 {
		os.Exit(1)
	}
}

func upgradeOne(c *client, node, version string) error {
	before := nodeVersion(c, node)

	raw, err := c.do("POST", "/api/upgrade", map[string]any{"node": node, "version": version})
	if err != nil {
		return err
	}
	var out struct {
		Version  string `json:"version"`
		Platform string `json:"platform"`
	}
	json.Unmarshal(raw, &out)

	if before == out.Version {
		fmt.Printf("%-8s %s  already current\n", node, out.Version)
		return nil
	}
	fmt.Printf("%-8s %s -> %s (%s), waiting for reconnect", node, before, out.Version, out.Platform)

	// The agent exits and its service manager restarts it three seconds later.
	// Waiting here rather than returning immediately is the point: an upgrade
	// nobody confirmed is a node you believe is fine.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		fmt.Print(".")
		if v := nodeVersion(c, node); v == out.Version {
			fmt.Printf(" online at %s\n", v)
			return nil
		}
	}
	fmt.Println()
	return fmt.Errorf("did not come back at %s within 60s — the previous binary is at "+
		"<path>.prev on that host", out.Version)
}

// nodeVersion reports what a node last logged in as, or "" if it is offline.
func nodeVersion(c *client, node string) string {
	nodes, err := fetchSessions(c)
	if err != nil {
		return ""
	}
	for _, n := range nodes {
		if n.Node == node && n.Online {
			return n.Version
		}
	}
	return ""
}

func runReleases(args []string) {
	fset := flag.NewFlagSet("releases", flag.ExitOnError)
	coord := fset.String("coord", "", "coordinator base URL")
	fset.Parse(args)

	c, err := newClient(*coord)
	if err != nil {
		fatalf("%v", err)
	}
	raw, err := c.do("GET", "/api/releases", nil)
	if err != nil {
		fatalf("%v", err)
	}
	var out struct {
		Releases []struct {
			cliRelease
			Published int64 `json:"published"`
		} `json:"releases"`
		Nodes map[string]struct {
			Platform string `json:"platform"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		fatalf("decode: %v", err)
	}
	for _, r := range out.Releases {
		fmt.Printf("%-12s %-14s %s  %.1f MB  %s\n", r.Version, r.Platform,
			r.SHA256[:12], float64(r.Size)/(1<<20),
			time.Unix(r.Published, 0).Format("01-02 15:04"))
	}
	if len(out.Releases) == 0 {
		fmt.Println("(nothing published — `shabadoo publish dist/`)")
	}
	if len(out.Nodes) > 0 {
		fmt.Println()
		for node, n := range out.Nodes {
			fmt.Printf("%-8s %s\n", node, n.Platform)
		}
	}
}

// ---------------------------------------------------------------------------
// revocation
// ---------------------------------------------------------------------------

func runDisconnect(args []string) {
	fset := flag.NewFlagSet("disconnect", flag.ExitOnError)
	coord := fset.String("coord", "", "coordinator base URL")
	fset.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: shabadoo disconnect <node>...

Drops a node's live session: its stream closes and its token stops working
immediately.

This is the half of revocation that authorized_agents cannot do. Removing a key
from that file stops the NEXT login, so an agent already holding a token keeps
driving every pane on its host until it happens to reconnect — possibly days.

To cut a host off permanently, do both:

  1. remove its key from the coordinator's authorized_agents
  2. shabadoo disconnect <node>

On its own this is temporary: the agent dials back in within seconds, and is
refused only if its key is gone.

flags:
`)
		fset.PrintDefaults()
	}
	nodes := argsAndFlags(fset, args)
	if len(nodes) == 0 {
		fset.Usage()
		os.Exit(2)
	}

	c, err := newClient(*coord)
	if err != nil {
		fatalf("%v", err)
	}
	for _, node := range nodes {
		raw, err := c.do("POST", "/api/nodes/disconnect", map[string]any{"node": node})
		if err != nil {
			warnf("%s: %v", node, err)
			continue
		}
		var out struct {
			Disconnected bool `json:"disconnected"`
		}
		json.Unmarshal(raw, &out)
		if out.Disconnected {
			fmt.Printf("%-8s disconnected — it will dial back in unless its key is "+
				"removed from authorized_agents\n", node)
		} else {
			fmt.Printf("%-8s was not connected\n", node)
		}
	}
}

func runRevoke(args []string) {
	fset := flag.NewFlagSet("revoke", flag.ExitOnError)
	coord := fset.String("coord", "", "coordinator base URL")
	fset.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: shabadoo revoke <device>...

Revokes an enrolled human credential — a browser, a phone, a CLI. The device is
named by its label or its id prefix, as shown by `+"`shabadoo devices`"+`.

Effective immediately and permanently: the token is deleted, so the client is
signed out on its next request and cannot renew. Re-pairing is the only way
back. To cut off a NODE instead, see `+"`shabadoo disconnect`"+`.

flags:
`)
		fset.PrintDefaults()
	}
	wanted := argsAndFlags(fset, args)
	if len(wanted) == 0 {
		fset.Usage()
		os.Exit(2)
	}

	c, err := newClient(*coord)
	if err != nil {
		fatalf("%v", err)
	}
	raw, err := c.do("GET", "/api/devices", nil)
	if err != nil {
		fatalf("%v", err)
	}
	var list struct {
		Devices []struct {
			ID    string `json:"ID"`
			Label string `json:"Label"`
			Scope string `json:"Scope"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		fatalf("decode: %v", err)
	}

	for _, want := range wanted {
		// Same resolution rule as panes and folders: exact first, then prefix
		// or substring, and an ambiguous match is an error. Revoking the wrong
		// credential signs someone out of a device they are holding.
		var matches []struct{ id, label string }
		for _, d := range list.Devices {
			switch {
			case d.Label == want || d.ID == want:
				matches = append(matches[:0], struct{ id, label string }{d.ID, d.Label})
				goto resolved
			case strings.HasPrefix(d.ID, want) || strings.Contains(strings.ToLower(d.Label), strings.ToLower(want)):
				matches = append(matches, struct{ id, label string }{d.ID, d.Label})
			}
		}
	resolved:
		if len(matches) == 0 {
			warnf("no device matching %q — try `shabadoo devices`", want)
			continue
		}
		if len(matches) > 1 {
			var lines []string
			for _, m := range matches {
				lines = append(lines, fmt.Sprintf("%s (%s)", m.label, shortID(m.id)))
			}
			sort.Strings(lines)
			warnf("%q is ambiguous:\n  %s", want, strings.Join(lines, "\n  "))
			continue
		}

		m := matches[0]
		fmt.Printf("revoke %s (%s)? this signs it out permanently [y/N] ", m.label, shortID(m.id))
		var answer string
		fmt.Scanln(&answer)
		if !strings.HasPrefix(strings.ToLower(answer), "y") {
			fmt.Println("cancelled")
			continue
		}
		if _, err := c.do("POST", "/api/devices/revoke", map[string]any{"device_id": m.id}); err != nil {
			warnf("%s: %v", m.label, err)
			continue
		}
		fmt.Printf("revoked %s\n", m.label)
	}
}
