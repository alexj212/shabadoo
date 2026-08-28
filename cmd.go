package main

// Subcommand wiring for the two new roles: `shabadoo node` (per-host agent)
// and `shabadoo hub` (coordinator).

import (
	"path/filepath"
	"context"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"shabadoo/hub"
	"shabadoo/node"
)

// version is stamped into agent logins so the dashboard can surface build skew
// across nodes.
//
// Set at link time by the Makefile (`-X main.version=...`, from `git
// describe`). It is a var, not a const, for exactly that reason — and the
// default is honest about being unstamped, because the failure this guards
// against is a stale binary, and a stale binary would carry a hardcoded
// version string that still looked like a real release.
var version = "dev (unstamped)"

// buildTime is the stamped commit date (RFC 3339), empty in an unstamped build.
//
// It exists because `git describe` output cannot be ORDERED: given "a376549"
// and "1c8fb18" there is no way to tell which came first, so version strings
// alone cannot answer "am I about to install an older binary over a newer one".
// A timestamp can, and the commit date rather than the build date keeps it
// reproducible for a given commit.
var buildTime = ""

// runNode connects this host to a coordinator and serves commands.
func runNode(args []string) {
	fset := flag.NewFlagSet("node", flag.ExitOnError)
	coord := fset.String("coord", os.Getenv("SHABADOO_COORD"),
		"coordinator base URL (or $SHABADOO_COORD)")
	nodeName := fset.String("node", "", "this node's name (default: the host label)")
	keyFile := fset.String("key", os.Getenv("SHABADOO_KEY"),
		"private key to authenticate with (default: ssh-agent via $SSH_AUTH_SOCK)")
	noConfig := fset.Bool("no-config", false,
		"do not install this build's ~/.claude payload at startup")
	fset.Parse(args)

	if *coord == "" {
		log.Fatal("node: --coord (or $SHABADOO_COORD) is required")
	}
	name := *nodeName
	if name == "" {
		name = hostLabel()
	}

	// The node masters its own config. `upgrade` replaces this binary and
	// restarts the process, so startup is exactly where the payload it now
	// carries should reach the disk beside it — otherwise the two drift with
	// nothing but a badge to say so.
	if !*noConfig {
		installPayload(defaultClaudeDir())
		if exe, err := os.Executable(); err == nil {
			ensureShorthand(filepath.Dir(exe))
		}
	}

	// Every report is also a diff, so the agent notices a window that has gone.
	observe := watchedReporter()

	c := node.New(node.Config{
		Coord:   *coord,
		Node:    name,
		Version: version,
		KeyFile: *keyFile,
		// Detected plus declared, merged here because the declaration lives in
		// this node's own project and the node package knows nothing of those.
		Capabilities: nodeCapabilities(),
		// Whether this node's installed config still matches the binary's.
		// Cheap: cached for five minutes, since it changes when somebody runs
		// setup and not otherwise.
		Facts: func() any { return payloadPending(defaultClaudeDir()) },
		// The adapter is the transport's `any`-typed seam; reportSessions itself
		// is concretely typed so `serve` can use its result without asserting.
	}, handleOp, func(ctx context.Context) (any, error) {
		sessions, err := reportSessions(ctx)
		if err != nil {
			return nil, err
		}
		// Every report is also a diff: a window that was here and is not is an
		// event. Only the agent is positioned to see it — the coordinator is
		// told what exists, never what stopped existing.
		observe(ctx, sessions)
		return sessions, nil
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The local socket is how a Claude session on this host reaches the
	// coordinator — see node/socket.go. Started alongside the command stream
	// and independent of it: a session must get "not connected" rather than a
	// refused socket while the agent is reconnecting, because those two mean
	// very different things to whoever reads the error.
	go func() {
		if err := c.ServeLocal(ctx); err != nil {
			log.Printf("node: local socket unavailable: %v", err)
		}
	}()

	log.Printf("node %s: connecting to %s", name, *coord)
	if err := c.Run(ctx); err != nil {
		log.Fatalf("node: %v", err)
	}
}

// runHub serves the coordinator.
func runHub(args []string) {
	fset := flag.NewFlagSet("hub", flag.ExitOnError)
	// 8787, the same as `serve` and the same as what `setup` bakes into the
	// unit. It was 8788 so both could run on one machine during development —
	// a convenience for one person that cost every new operator the twenty
	// minutes it takes to notice a one-digit difference between the flag they
	// read about and the flag they typed. Run both locally with an explicit
	// --addr instead.
	addr := fset.String("addr", "127.0.0.1:8787", "listen address")
	dbPath := fset.String("db", "hub.db", "SQLite database path")
	keys := fset.String("agents", "authorized_agents", "authorized agent keys file")
	team := fset.String("access-team", os.Getenv("SHABADOO_ACCESS_TEAM"),
		"Cloudflare Access team domain, e.g. example.cloudflareaccess.com")
	aud := fset.String("access-aud", os.Getenv("SHABADOO_ACCESS_AUD"),
		"Cloudflare Access application AUD tag")
	insecure := fset.Bool("insecure-no-access", false,
		"serve the human plane with NO authentication (local development only)")
	bootstrap := fset.Bool("bootstrap", false,
		"print a one-time device pairing code at startup (self-hosted first run)")
	deviceTokens := fset.Bool("device-tokens", false,
		"authenticate humans by device token only — no Access, no network trust")
	ciRepo := fset.String("ci-repo", os.Getenv("SHABADOO_CI_REPO"),
		"owner/name of a PUBLIC GitHub repo to watch; notifies when its default "+
			"branch goes red. Needs --apprise-url; no token, the API is public")
	apprise := fset.String("apprise-url", os.Getenv("SHABADOO_APPRISE_URL"),
		"notification relay endpoint, e.g. http://apprise:8000/notify/homelab "+
			"(empty disables notify_send)")
	tsAllow := fset.String("tailscale-allow", os.Getenv("SHABADOO_TAILSCALE_ALLOW"),
		"comma-separated tailnet logins admitted without pairing, e.g. "+
			"alex@example.com (empty disables the tailnet provider)")
	elevenKey := fset.String("elevenlabs-key", os.Getenv("SHABADOO_ELEVENLABS_KEY"),
		"ElevenLabs API key, held here so no client ever holds it "+
			"(empty disables the voice endpoint)")
	elevenAgent := fset.String("elevenlabs-agent", os.Getenv("SHABADOO_ELEVENLABS_AGENT"),
		"ElevenLabs conversational agent id")
	releaseDir := fset.String("releases", "",
		"directory for binaries published for node upgrades (empty disables `shabadoo upgrade`)")
	auditDays := fset.Int("audit-retention-days", 90,
		"how long to keep audit and retrieval rows; 0 keeps them forever")
	fset.Parse(args)

	hub.Version = version
	hub.AppriseURL = *apprise
	hub.CIRepo = *ciRepo
	hub.ElevenLabsKey, hub.ElevenLabsAgent = *elevenKey, *elevenAgent

	// A secret on the command line is world-readable. /proc/<pid>/cmdline is
	// mode 444, so any user on the host reads it with `ps`, and this key is
	// account-wide and billed per minute.
	//
	// The flag stays, because removing a documented interface is a worse
	// surprise than a warning — but a deployment that passes the key this way
	// should be told once, at the moment somebody is looking at the log.
	if keyOnCommandLine(os.Args) {
		log.Printf("hub: WARNING --elevenlabs-key was passed on the command line, " +
			"where any user on this host can read it from ps. Set " +
			"SHABADOO_ELEVENLABS_KEY in the environment instead and drop the flag.")
	}

	// 0 means keep forever, which is a legitimate choice for someone who wants
	// the audit log to be the permanent record — but it has to be chosen, not
	// arrived at by nobody having written the deletion.
	if *auditDays > 0 {
		hub.AuditRetention = time.Duration(*auditDays) * 24 * time.Hour
	} else {
		hub.AuditRetention = 1 << 62 // ~146 years: never, without a nil check everywhere
	}

	// `tailscale:PORT` is resolved here rather than baked into the unit, so a
	// re-assigned tailnet address still binds without reinstalling.
	listen, err := resolveAddr(*addr)
	if err != nil {
		log.Fatalf("hub: --addr %s: %v", *addr, err)
	}

	// Loaded from the file and re-read when it changes: adding a node should be
	// an edit, not a restart that disconnects every agent already connected.
	auth, err := hub.NewAuthorizerFromFile(*keys)
	if err != nil {
		log.Fatalf("hub: %v", err)
	}
	store, err := hub.Open(*dbPath)
	if err != nil {
		log.Fatalf("hub: %v", err)
	}
	defer store.Close()

	h := hub.New(auth, store)
	devices, err := hub.OpenDeviceStore(context.Background(), store)
	if err != nil {
		log.Fatalf("hub: load devices: %v", err)
	}

	// Releases are opt-in: a coordinator with nowhere to keep binaries simply
	// refuses to upgrade nodes, which is the honest state for a deployment that
	// has not chosen to store them.
	if *releaseDir != "" {
		rs, rerr := hub.OpenReleaseStore(*releaseDir)
		if rerr != nil {
			log.Fatalf("hub: releases: %v", rerr)
		}
		h.SetReleases(rs)
		log.Printf("hub: node upgrades enabled, %d release(s) in %s", len(rs.List()), *releaseDir)
	}

	mux := http.NewServeMux()
	h.HealthRoutes(mux)   // GET /healthz — unauthenticated by necessity, see there
	h.Routes(mux)         // agent plane: SSH-key auth, verified per request
	h.ReleaseRoutes(mux)  // agent-plane binary download for `shabadoo upgrade`
	h.AgentAPIRoutes(mux) // session messaging — what the MCP bridge (mcp-natsbridge) will call
	// The enrolment page ships in the embedded tree; hand it to the store,
	// which owns the unauthenticated routes.
	if page, err := staticFS.ReadFile("static/pair.html"); err == nil {
		hub.PairPage = page
	}
	hub.QREncoder = RenderSVG
	devices.PublicRoutes(mux) // device enrolment: single-use code, no prior identity

	// Human plane, behind whichever identity providers this deployment uses.
	human := http.NewServeMux()
	hub.HumanRoutes(human, h, store, devices)

	// The dashboard itself. Served from the same embedded tree the flock used,
	// and behind the same identity middleware as the API — an unauthenticated
	// caller must not even learn which projects exist from the page shell.
	pages, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Fatalf("hub: %v", err)
	}
	human.Handle("GET /", http.FileServerFS(pages))

	// The iOS app presents a device token in both deployment modes; only the
	// browser's path differs between self-hosted and hosted. Composing them
	// means one set of endpoints serves both clients.
	providers := hub.AnyOf{devices}

	// The tailnet, when the operator has named who may use it.
	//
	// Tried BEFORE device tokens so a listed user never has to pair at all —
	// which also dissolves the bootstrap paradox: minting the first pairing
	// code needs an enrolled credential, and someone standing up their own
	// coordinator meets that on day one. Device tokens stay for everything the
	// tailnet cannot speak for: the iOS app, a CLI on a host outside it.
	//
	// Membership is deliberately NOT the gate. A tailnet holds phones, TVs and
	// family devices; reaching this dashboard means driving panes running
	// --dangerously-skip-permissions.
	if allow := splitList(*tsAllow); len(allow) > 0 {
		providers = append(hub.AnyOf{&hub.TailscaleProvider{Allow: allow}}, providers...)
		log.Printf("hub: tailnet identity enabled for %s", strings.Join(allow, ", "))
	}

	if *insecure {
		// Loud, because a process started this way and left running is the exact
		// shape of the accident this whole design is trying to prevent. The
		// provider itself also refuses anything that is not loopback.
		log.Printf("WARNING: --insecure-no-access is set. The human plane has NO " +
			"authentication for loopback callers: anyone who can reach this address " +
			"can drive every Claude pane on every connected agent. Never use this on " +
			"a routable address.")
		providers = append(providers, hub.InsecureProvider{})
	} else if *deviceTokens {
		// Device tokens alone. This is the self-hosted posture with real
		// authentication and no Cloudflare in front: every human client — the
		// browser, the CLI, the app — presents a token it was enrolled with.
		// Pair the first one with --bootstrap, which is the only way in before
		// any device exists.
		log.Printf("hub: human plane requires a device token; " +
			"enrol the first with --bootstrap, then `shabadoo pair`")
	} else if len(splitList(*tsAllow)) > 0 {
		// Tailnet identity alone is a complete posture: WireGuard authenticated
		// the peer before the first byte of HTTP, and the allowlist decides who
		// among them may drive panes. Nothing to enrol, nothing to expire.
		log.Printf("hub: human plane authenticated by the tailnet; " +
			"device tokens remain available for clients outside it")
	} else {
		if *team == "" || *aud == "" {
			log.Fatal("hub: an auth posture is required — one of " +
				"--device-tokens, --tailscale-allow, --access-team/--access-aud, " +
				"or --insecure-no-access (loopback only)")
		}
		verifier, err := hub.NewAccessVerifier(*team, *aud)
		if err != nil {
			log.Fatalf("hub: %v", err)
		}
		providers = append(providers, verifier)
	}
	mux.Handle("/", hub.Middleware(providers, human))
	log.Printf("hub: human plane providers: %s", providers.Name())

	if *bootstrap {
		code := devices.Bootstrap(hub.DefaultTenant)
		log.Printf("hub: BOOTSTRAP PAIRING CODE %s (valid 5 minutes, single use) — "+
			"redeem with: curl -sX POST <url>/api/devices/redeem -H 'Content-Type: application/json' "+
			`-d '{"code":"%s","label":"my device"}'`, code, code)
	}

	// Bounded background maintenance: expired mail is the only thing that grows
	// the database without limit.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go vacuumLoop(ctx, store, h)
	// A stuck handoff is two sessions waiting on each other, so it gets its own
	// fast timer rather than riding the hourly maintenance tick: finding out an
	// hour late costs an hour of nothing happening, which is the failure it
	// exists to end.
	go stuckLoop(ctx, h)

	srv := &http.Server{
		Addr:              listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		// No WriteTimeout: the agent stream is long-lived by design.
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()

	// Said once at startup rather than discovered per-call, same as the
	// notifier: someone finding out mid-sentence that voice was never
	// configured is a worse way to learn it.
	switch {
	case *elevenKey != "" && *elevenAgent != "":
		log.Printf("hub: voice sessions enabled for agent %s", *elevenAgent)
	case *elevenKey != "" || *elevenAgent != "":
		log.Printf("hub: voice is HALF configured (need both --elevenlabs-key and " +
			"--elevenlabs-agent); the endpoint stays disabled")
	}

	// Said once at startup rather than discovered per-call: a session that tries
	// to notify a human and silently cannot is worse than one told up front.
	if hub.AppriseURL == "" {
		log.Printf("hub: no --apprise-url, so notify_send is unavailable to sessions")
	}
	if *ciRepo != "" && *apprise == "" {
		log.Printf("hub: --ci-repo %s is set but --apprise-url is not, so build "+
			"failures have nowhere to go; the watcher is off", *ciRepo)
	} else if *ciRepo != "" {
		log.Printf("hub: watching %s for build failures", *ciRepo)
	} else {
		log.Printf("hub: notifications relay to %s", hub.AppriseURL)
		// The same relay carries stuck-session alerts. Only enabled alongside a
		// notifier: with nowhere to send, the watcher would be bookkeeping for
		// messages that go nowhere.
		h.EnableBlockedNotifications()
		log.Printf("hub: notifying when a session waits at a prompt for %s", hub.BlockedGrace)
	}
	// Starting with no agents is allowed — it is what a fresh coordinator looks
	// like — but it must not be quiet about it, or the operator's first
	// experience is a dashboard reporting "No agents connected" with nothing to
	// suggest why.
	if auth.Count() == 0 {
		log.Printf("hub: no authorized agents yet. Add a machine's public key to %s "+
			"(one per line, node name as the comment); the file is re-read when it "+
			"changes, so no restart is needed.", *keys)
	}
	log.Printf("hub %s listening on %s (db %s, %d authorized agents, %d enrolled devices)",
		version, listen, *dbPath, auth.Count(), devices.Count())
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

// splitList parses a comma-separated flag, dropping blanks so a trailing comma
// does not become an empty allowlist entry that matches an empty login.
func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// stuckLoop looks for mail that arrived and was never picked up.
//
// The nudge is what normally closes a handoff and it fails silently — a skipped
// nudge and a delivered one look identical from every side. This is the second
// observer, and it is deliberately not the nudge: it reads what the coordinator
// knows (undrained mail) rather than trusting the mechanism that was supposed
// to have worked.
func stuckLoop(ctx context.Context, h *hub.Hub) {
	t := time.NewTicker(2 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.SweepStuck(ctx)
		}
	}
}

func vacuumLoop(ctx context.Context, store *hub.Store, h *hub.Hub) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// Chase work that has gone quiet. Piggybacked on the existing
			// maintenance tick rather than given its own: the watcher is
			// edge-triggered, so looking hourly costs nothing and only bounds
			// how late the first mention can be.
			h.SweepTasks(ctx)
			// And whether this project's own build is broken. Same tick, same
			// edge-triggered shape, and the same argument for piggybacking:
			// looking hourly costs nothing and only bounds how late the first
			// mention can be.
			h.CheckCI(ctx)
			if res, err := store.Vacuum(ctx, time.Now()); err != nil {
				log.Printf("hub: vacuum: %v", err)
			} else if res.Any() {
				log.Printf("hub: vacuumed %s", res)
			}
		}
	}
}

// keyOnCommandLine reports whether a secret-bearing flag carries a value in
// argv, as opposed to being read from the environment.
//
// Only a NON-EMPTY value counts: compose files commonly expand an unset
// variable to `--elevenlabs-key=`, and warning about an empty flag would be
// noise on every deployment that does not use voice at all.
func keyOnCommandLine(args []string) bool {
	for i, a := range args {
		if v, ok := strings.CutPrefix(a, "--elevenlabs-key="); ok {
			return v != ""
		}
		if a == "--elevenlabs-key" && i+1 < len(args) {
			return args[i+1] != ""
		}
	}
	return false
}
