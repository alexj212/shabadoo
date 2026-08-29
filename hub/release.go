package hub

// Shipping a new binary to a node.
//
// Upgrading a node meant scp and a service restart, per host, per platform, by
// hand — which is exactly the ritual that produces version skew, and skew is
// the hazard the build stamps exist to make visible. The transport to fix it
// was already there: every agent holds an authenticated stream open.
//
// Two things this is NOT:
//
//   - It is not automatic. A node that logs in behind the hub stays behind
//     until someone says otherwise. A push fans out to every host at once, and
//     if the new build is broken the recovery is SSH to each machine — which is
//     precisely what dial-out agents exist to avoid needing.
//   - It is not a network fetch. The hub serves bytes an operator gave it; it
//     never reaches out to a third party for a binary. That distinction is what
//     keeps the "self-contained, no network install path" property intact.
//
// The hub cannot BUILD what it ships — it runs linux/amd64 in a container and
// the Mac needs darwin/arm64 — so releases are published to it (`make dist &&
// shabadoo publish dist/`) and served back out per platform.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// maxReleaseBytes bounds an upload. The binary is ~15 MB; this leaves room to
// grow without letting an authenticated client fill the disk in one request.
const maxReleaseBytes = 128 << 20

// keepVersions is how many versions to keep per platform.
//
// A publish is ~70 MB across four platforms and this directory sits inside the
// bind mount the nightly borg run covers, so unbounded growth is not just disk
// — it is disk in every backup, forever. Three is the current build, the one
// before it (what `<path>.prev` on each node corresponds to), and one more for
// the case where the answer to a bad deploy is "go back further".
const keepVersions = 3

// ReleaseStore holds published binaries on disk, one file per version and
// platform.
//
// On disk rather than in hub.db: a 15 MB blob per platform per version in
// SQLite bloats the file every backup touches, and the state directory is
// already the thing that gets backed up. The manifest is a sidecar JSON so a
// restart does not have to re-hash every file to know what it has.
type ReleaseStore struct {
	dir string

	mu   sync.Mutex
	rels map[string]Release // keyed version\x00platform
}

// Release is one published file.
//
// A release used to be one binary per platform, which is true of shabadoo and
// false of everything else. The meeting recorder is two files that must land
// together — an orchestrator and a native capture helper — and installing one
// without the other leaves a tool that refuses to run and blames the missing
// half. So a release is a SET, and this is one member of one.
type Release struct {
	// Tool is empty for shabadoo itself, which keeps every previously published
	// release addressable and every older agent's request answerable.
	Tool string `json:"tool,omitempty"`

	// Component is the file's installed name. Empty means the tool itself.
	Component string `json:"component,omitempty"`

	Version string `json:"version"`

	// Platform is the NODE this set is for, not the file's own architecture.
	// A linux/amd64 node running the recorder needs a linux orchestrator AND a
	// windows helper reached over interop, so both are published against
	// linux/amd64 — because the question a node asks is "what do I need", never
	// "what was this compiled for".
	Platform  string `json:"platform"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	Published int64  `json:"published"`
}

// ToolName is the tool a release belongs to, with the default spelled out.
func (r Release) ToolName() string {
	if r.Tool == "" {
		return "shabadoo"
	}
	return r.Tool
}

// FileName is what this component installs as.
func (r Release) FileName() string {
	if r.Component == "" {
		return r.ToolName()
	}
	return r.Component
}

func (r Release) key() string {
	return r.Tool + "\x00" + r.Version + "\x00" + r.Platform + "\x00" + r.Component
}

// platformFile is the on-disk name for a platform, with the slash removed so
// it is one path segment rather than a directory nobody asked for.
func platformFile(platform string) string {
	return strings.ReplaceAll(platform, "/", "-")
}

// OpenReleaseStore loads the manifest, creating the directory if needed.
func OpenReleaseStore(dir string) (*ReleaseStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &ReleaseStore{dir: dir, rels: map[string]Release{}}

	body, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return s, nil // no manifest yet is the fresh-install case, not an error
	}
	var list []Release
	if err := json.Unmarshal(body, &list); err != nil {
		// A corrupt manifest must not stop the coordinator from starting: the
		// binaries are still on disk and re-publishing rebuilds it.
		return s, nil
	}
	for _, rel := range list {
		if _, err := os.Stat(s.path(rel)); err == nil {
			s.rels[rel.key()] = rel
		}
	}
	return s, nil
}

func (s *ReleaseStore) path(rel Release) string {
	name := rel.Version + "-" + platformFile(rel.Platform)
	if rel.Tool != "" {
		name = rel.Tool + "-" + name
	}
	if rel.Component != "" {
		name += "-" + strings.ReplaceAll(rel.Component, "/", "-")
	}
	return filepath.Join(s.dir, name)
}

// Publish stores a binary and returns its record.
func (s *ReleaseStore) Publish(version, platform string, body io.Reader, now time.Time) (Release, error) {
	return s.PublishComponent("", "", version, platform, body, now)
}

// PublishComponent stores one member of one tool's release set.
//
// tool and component are empty for shabadoo itself, which keeps every release
// published before this addressable and every older agent's request answerable.
func (s *ReleaseStore) PublishComponent(tool, component, version, platform string, body io.Reader, now time.Time) (Release, error) {
	if version == "" || platform == "" {
		return Release{}, fmt.Errorf("version and platform are required")
	}
	if !strings.Contains(platform, "/") {
		return Release{}, fmt.Errorf("platform must be GOOS/GOARCH, got %q", platform)
	}
	rel := Release{
		Tool: tool, Component: component,
		Version: version, Platform: platform, Published: now.Unix(),
	}

	// Temp file then rename: a node may be downloading a release of the same
	// name, and a partially-written file served mid-upload would fail its
	// checksum at best and be executed at worst.
	tmp, err := os.CreateTemp(s.dir, ".upload-*")
	if err != nil {
		return Release{}, err
	}
	defer os.Remove(tmp.Name())

	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, sum), io.LimitReader(body, maxReleaseBytes))
	if err != nil {
		tmp.Close()
		return Release{}, err
	}
	if err := tmp.Close(); err != nil {
		return Release{}, err
	}
	if n == 0 {
		return Release{}, fmt.Errorf("empty upload")
	}
	rel.Size = n
	rel.SHA256 = hex.EncodeToString(sum.Sum(nil))

	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return Release{}, err
	}
	if err := os.Rename(tmp.Name(), s.path(rel)); err != nil {
		return Release{}, err
	}

	s.mu.Lock()
	s.rels[rel.key()] = rel
	s.mu.Unlock()

	s.prune()
	return rel, s.writeManifest()
}

// prune drops all but the newest keepVersions versions of each platform.
//
// On publish rather than on a timer: publishing is the only thing that grows
// this, so it is the only moment the answer changes — and a timer would be a
// goroutine whose failure mode is silent.
func (s *ReleaseStore) prune() {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Grouped by (tool, platform), and pruned by VERSION rather than by file.
	//
	// A release is a set, so keeping "the newest three files" would keep three
	// components of one version and delete the other half of it — leaving a
	// version that looks published and cannot be installed. Three versions of
	// the set, every component of each.
	byPlatform := map[string][]Release{}
	for _, rel := range s.rels {
		byPlatform[rel.Tool+"\x00"+rel.Platform] = append(byPlatform[rel.Tool+"\x00"+rel.Platform], rel)
	}
	for _, rels := range byPlatform {
		seen := map[string]bool{}
		for _, r := range rels {
			seen[r.Version] = true
		}
		if len(seen) <= keepVersions {
			continue
		}
		// Newest first by publish time — `git describe` strings cannot be
		// ordered, which is why the downgrade guard uses a timestamp too.
		sort.Slice(rels, func(i, j int) bool { return rels[i].Published > rels[j].Published })
		keep, doomed := map[string]bool{}, []Release{}
		for _, r := range rels {
			if len(keep) < keepVersions || keep[r.Version] {
				keep[r.Version] = true
				continue
			}
			doomed = append(doomed, r)
		}
		for _, old := range doomed {
			// Remove the file first: a manifest entry with no file is
			// self-healing (OpenReleaseStore drops it), while a file with no
			// entry is invisible disk nobody will ever look for.
			if err := os.Remove(s.path(old)); err != nil && !os.IsNotExist(err) {
				continue // keep the entry so the file stays findable
			}
			delete(s.rels, old.key())
		}
	}
}

// Get returns a release and the path to its bytes.
func (s *ReleaseStore) Get(version, platform string) (Release, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rel, ok := s.rels[Release{Version: version, Platform: platform}.key()]
	if !ok {
		return Release{}, "", false
	}
	return rel, s.path(rel), true
}

// Latest returns the most recently published release for a platform.
func (s *ReleaseStore) Latest(platform string) (Release, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best Release
	found := false
	for _, rel := range s.rels {
		if rel.Platform != platform {
			continue
		}
		// By publish time, not by version string: `git describe` output cannot
		// be ordered — the same reason the downgrade guard uses a timestamp.
		if !found || rel.Published > best.Published {
			best, found = rel, true
		}
	}
	return best, found
}

// List returns every release, newest first.
func (s *ReleaseStore) List() []Release {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Release, 0, len(s.rels))
	for _, rel := range s.rels {
		out = append(out, rel)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Published != out[j].Published {
			return out[i].Published > out[j].Published
		}
		return out[i].Platform < out[j].Platform
	})
	return out
}

func (s *ReleaseStore) writeManifest() error {
	body, err := json.MarshalIndent(s.List(), "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.dir, ".manifest.tmp")
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.dir, "manifest.json"))
}

// ---------------------------------------------------------------------------
// endpoints
// ---------------------------------------------------------------------------

// ReleaseRoutes registers the agent-plane download. It is on the agent plane
// because the downloader is an agent, holding an agent's token — the operator
// who publishes is a human and uses the human plane.
func (h *Hub) ReleaseRoutes(mux *http.ServeMux) {
	// Tool components get their own route rather than overloading the original
	// with an optional segment: an older agent asking the old path must keep
	// getting the old answer, and a path that means two things depending on
	// segment count is how a mixed fleet breaks quietly.
	mux.HandleFunc("GET /agent/release/{tool}/{component}/{version}/{platform}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := h.authed(r); !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if h.releases == nil {
			http.Error(w, "no releases published", http.StatusNotFound)
			return
		}
		platform := strings.ReplaceAll(r.PathValue("platform"), "-", "/")
		rel, path, ok := h.releases.GetComponent(
			r.PathValue("tool"), r.PathValue("component"), r.PathValue("version"), platform)
		if !ok {
			http.Error(w, "no such component", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Shabadoo-SHA256", rel.SHA256)
		http.ServeFile(w, r, path)
	})

	mux.HandleFunc("GET /agent/release/{version}/{platform}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := h.authed(r); !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if h.releases == nil {
			http.Error(w, "no releases published", http.StatusNotFound)
			return
		}
		platform := strings.ReplaceAll(r.PathValue("platform"), "-", "/")
		rel, path, ok := h.releases.Get(r.PathValue("version"), platform)
		if !ok {
			http.Error(w, "no such release", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Shabadoo-SHA256", rel.SHA256)
		http.ServeFile(w, r, path)
	})
}

// upgradeNode sends one node the command to replace itself.
//
// The platform check is here rather than on the node because getting it wrong
// is unrecoverable from the hub's side: a node that overwrites itself with a
// binary for another architecture cannot start, and therefore cannot be told
// anything again. A node that is not connected simply has no platform on
// record, which is the correct reason to refuse.
func (h *Hub) upgradeNode(ctx context.Context, tenant, node, version string) (Release, error) {
	if h.releases == nil {
		return Release{}, fmt.Errorf("no releases have been published to this coordinator")
	}
	platform, connected := h.nodePlatform(tenant, node)
	switch {
	case !connected:
		return Release{}, fmt.Errorf("node %q is not connected", node)
	case platform == "":
		// Connected, but on a build that predates platform reporting. Saying
		// "not connected" here sends someone to check the network when the
		// answer is one manual install — and this is the first upgrade on every
		// host, so it is the message most operators will actually meet.
		return Release{}, fmt.Errorf("node %q is connected but does not report its platform, "+
			"so it predates `shabadoo upgrade`. Install once by hand:\n"+
			"  scp dist/shabadoo-<goos>-<goarch> %s:bin/shabadoo && restart its service\n"+
			"after which it can upgrade itself", node, node)
	}

	var rel Release
	var ok bool
	if version == "" {
		if rel, ok = h.releases.Latest(platform); !ok {
			return Release{}, fmt.Errorf("nothing published for %s — `shabadoo publish` a %s build first",
				platform, platform)
		}
	} else if rel, _, ok = h.releases.Get(version, platform); !ok {
		return Release{}, fmt.Errorf("no %s release of %s", platform, version)
	}

	_, err := h.Call(ctx, tenant, node, "upgrade", map[string]any{
		"version":  rel.Version,
		"platform": rel.Platform,
		"sha256":   rel.SHA256,
		"path": fmt.Sprintf("/agent/release/%s/%s",
			rel.Version, platformFile(rel.Platform)),
	})
	return rel, err
}

// Protocol levels. Bumped when the coordinator gains an operation, or an
// argument, that an older agent would MISHANDLE rather than reject.
//
// Not bumped for anything additive that an old build safely ignores — a level
// that moves on every release teaches nobody anything and forces upgrades that
// were not needed.
const (
	// ProtocolBase is every build before negotiation existed. Reported as 0.
	ProtocolBase = 0

	// ProtocolNegotiation is the first build that says what it understands.
	ProtocolNegotiation = 1

	// ProtocolPanes is the first build that addresses a pane rather than a
	// window. An older agent ignores the field and writes to whichever pane is
	// active, which is the exact failure this addressing removes — and it is
	// indistinguishable from success at every layer.
	ProtocolPanes = 2

	// ProtocolCurrent is what this build speaks.
	ProtocolCurrent = ProtocolPanes
)

// NodeProtocol reports what a connected agent understands, and whether it is
// connected at all — the same empty-vs-absent distinction NodePlatform draws,
// for the same reason: "0" and "not here" are different problems.
func (h *Hub) NodeProtocol(tenant, node string) (level int, connected bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.byNode[nodeKey(tenant, node)]
	if !ok {
		return 0, false
	}
	return c.protocol, true
}

// RequireProtocol refuses an operation a node's build cannot handle.
//
// **Refuse, never degrade.** Falling back to the older behaviour is what makes
// a mixed-version fleet dangerous: the caller is told it worked, and the
// difference between "sent to that pane" and "sent to whichever pane happened
// to be active" is invisible until somebody reads a transcript. A refusal names
// the node and what to do about it.
func (h *Hub) RequireProtocol(tenant, node string, min int, what string) error {
	level, connected := h.NodeProtocol(tenant, node)
	if !connected {
		return ErrAgentOffline
	}
	if level < min {
		return fmt.Errorf("%s needs a newer agent on %s: it speaks protocol %d, "+
			"this needs %d — run `shabadoo upgrade %s`", what, node, level, min, node)
	}
	return nil
}

// CapabilitiesKnown reports whether this node's build says anything about its
// capabilities at all.
//
// Without it, an online node with an empty capability list is ambiguous between
// "this machine can do nothing" and "this build predates capability reporting" —
// and a router reading the first when the second is true will decline to send
// work a host could perfectly well have done.
//
// It costs nothing on the wire: capability reporting arrived with protocol
// negotiation, so an agent that negotiates is an agent that reports. `upgrade
// --all` is deliberately serial, so a mixed fleet exists during every upgrade
// and this is reachable rather than theoretical.
func (h *Hub) CapabilitiesKnown(tenant, node string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.byNode[nodeKey(tenant, node)]
	return ok && c.protocol >= ProtocolNegotiation
}

// NodeCapabilities reports what a connected agent says its host can do.
//
// Held on the connection for the same reason as the platform: it describes the
// machine currently answering to this node name, and a stale row would be a way
// to route audio work to a host that no longer has a microphone. A node nobody
// can reach can do nothing, so the answer for an offline node is nothing.
func (h *Hub) NodeCapabilities(tenant, node string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c, ok := h.byNode[nodeKey(tenant, node)]; ok {
		return c.caps
	}
	return nil
}

// NodeInstalledPayload reports whether a connected node's ~/.claude matches the
// payload in its own binary.
//
// `upgrade` replaces a binary and never runs the config step, so a node can
// carry a new skill inside itself while the old one sits on disk — silently,
// looking healthy. This is what makes that visible; the remedy is a human
// running `setup` on that machine.
func (h *Hub) NodeInstalledPayload(tenant, node string) NodePayload {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c, ok := h.byNode[nodeKey(tenant, node)]; ok {
		return c.payload
	}
	return NodePayload{}
}

// NodePlatform reports a connected agent's GOOS/GOARCH, or "".
//
// Held on the connection rather than in the database on purpose: it is only
// ever needed to decide what to send a node that is connected right now, and a
// stale row would be a way to send a Mac a Linux binary after someone moved the
// host name to a different machine.
func (h *Hub) NodePlatform(tenant, node string) string {
	p, _ := h.nodePlatform(tenant, node)
	return p
}

// nodePlatform separates "offline" from "online but says nothing", which are
// the same empty string and completely different problems: one is a network or
// service fault, the other is a node on a build older than platform reporting.
func (h *Hub) nodePlatform(tenant, node string) (platform string, connected bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.byNode[nodeKey(tenant, node)]
	if !ok {
		return "", false
	}
	return c.platform, true
}

// GetComponent finds one member of one tool's set.
func (s *ReleaseStore) GetComponent(tool, component, version, platform string) (Release, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rel, ok := s.rels[Release{Tool: tool, Component: component, Version: version, Platform: platform}.key()]
	if !ok {
		return Release{}, "", false
	}
	return rel, s.path(rel), true
}

// ToolSet returns every component of a tool's release for one node platform,
// and whether one exists at all.
//
// The distinction matters: "no set for your platform" and "a set exists and you
// are behind" are different answers, and a node told the wrong one either
// installs nothing forever or chases a version that cannot exist for it. Not
// every host can build every set — a capture helper needs MSVC or swiftc — so
// a missing platform is an ordinary state rather than an error.
func (s *ReleaseStore) ToolSet(tool, version, platform string) ([]Release, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	newest := version
	if newest == "" {
		for _, rel := range s.rels {
			if rel.Tool != tool || rel.Platform != platform {
				continue
			}
			if cur, ok := s.rels[Release{Tool: tool, Version: newest, Platform: platform}.key()]; newest == "" || (ok && rel.Published > cur.Published) {
				newest = rel.Version
			}
		}
	}
	if newest == "" {
		return nil, false
	}
	var set []Release
	for _, rel := range s.rels {
		if rel.Tool == tool && rel.Platform == platform && rel.Version == newest {
			set = append(set, rel)
		}
	}
	sort.Slice(set, func(i, j int) bool { return set[i].Component < set[j].Component })
	return set, len(set) > 0
}

// UpgradeNodeTool tells one node to install a tool's set for its own platform.
//
// Separate from upgradeNode because the two are not the same operation. Its own
// binary is replaced under a running process which then exits for its
// supervisor; another tool is simply installed, which is strictly simpler and
// must not borrow the restart dance.
func (h *Hub) UpgradeNodeTool(ctx context.Context, tenant, node, tool, version string) ([]Release, error) {
	if h.releases == nil {
		return nil, fmt.Errorf("no releases published")
	}
	platform := h.NodePlatform(tenant, node)
	if platform == "" {
		return nil, fmt.Errorf("%s has not reported a platform — it is either not "+
			"connected, or running a build that predates platform reporting", node)
	}
	set, ok := h.releases.ToolSet(tool, version, platform)
	if !ok {
		return nil, fmt.Errorf("no %s set published for %s. That is not the same as "+
			"being behind: not every host can build every set, so somebody with a "+
			"%s machine has to publish one", tool, platform, platform)
	}

	components := make([]map[string]any, 0, len(set))
	for _, rel := range set {
		components = append(components, map[string]any{
			"name":   rel.FileName(),
			"sha256": rel.SHA256,
			"path": fmt.Sprintf("/agent/release/%s/%s/%s/%s",
				rel.Tool, rel.Component, rel.Version, platformFile(rel.Platform)),
		})
	}
	_, err := h.Call(ctx, tenant, node, "install_tool", map[string]any{
		"tool":       tool,
		"version":    set[0].Version,
		"components": components,
	})
	return set, err
}
