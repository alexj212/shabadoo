package node

// What this machine can do.
//
// Every node currently looks identical to the coordinator: a thing with tmux
// windows. That flattening is what stops nodes being brains with different
// abilities — one has a GPU, one has a platform toolchain, one has the media
// tools — and it makes routing by capability impossible, because the capability
// is not data anywhere.
//
// This is the DETECTED half, and it reports what can be checked rather than
// what someone wrote down. That is the principle the rest of the design runs
// on: mechanical facts are data, judgment is a session. A declared capability
// that is not true is worse than one nobody claimed, because something will
// route work to it and the work will fail at the far end.
//
// See docs/direction.md.

import (
	"os/exec"
	"sort"
	"sync"
)

// software is the toolchain vocabulary: the things whose presence actually
// distinguishes one machine from another when deciding where work should go.
//
// A curated list rather than an inventory of everything installed. Asking the
// package manager would return thousands of entries, none of which answers
// "which node can build this" — the useful question is whether a toolchain is
// here, and that is a short list that a router can hold in mind at once.
//
// Names are the capability; the command is only how presence is proven.
var software = map[string]string{
	// Languages and runtimes
	"go": "go", "node": "node", "python": "python3", "ruby": "ruby",
	"java": "java", "rust": "rustc", "dotnet": "dotnet", "php": "php",
	"deno": "deno", "bun": "bun", "swift": "swift", "erlang": "erl",

	// Building
	"make": "make", "cmake": "cmake", "gcc": "gcc", "clang": "clang",
	"cargo": "cargo", "gradle": "gradle", "maven": "mvn",
	"ios.build": "xcodebuild", "mingw": "x86_64-w64-mingw32-gcc",

	// Containers and infrastructure
	"docker": "docker", "podman": "podman", "kubectl": "kubectl",
	"helm": "helm", "terraform": "terraform", "ansible": "ansible",

	// Cloud and network CLIs
	"gh": "gh", "aws": "aws", "az": "az", "gcloud": "gcloud",
	"tailscale": "tailscale", "rsync": "rsync",

	// Data
	"postgres.client": "psql", "mysql.client": "mysql", "sqlite": "sqlite3",
	"redis.client": "redis-cli",

	// Media and models
	"ffmpeg": "ffmpeg", "imagemagick": "convert", "yt-dlp": "yt-dlp",
	"ollama": "ollama", "whisper": "whisper",

	// Hardware and the basics worth knowing are absent
	"gpu.nvidia": "nvidia-smi", "git": "git", "tmux": "tmux", "jq": "jq",
}

var (
	capsOnce sync.Once
	capsList []string
)

// Capabilities reports what this host can do, sorted and computed once.
//
// Sorted because this is compared and stored: a set that reordered itself
// between logins would read as a change every time. Computed once because it is
// a fact about a machine rather than about a moment — software does not appear
// while the agent is running, and an agent whose connection is flapping would
// otherwise re-scan on every reconnect. Installing something takes effect when
// the agent restarts, which is the same rule as every other per-host fact here.
//
// Presence only, deliberately: no versions. Establishing a version means
// executing each binary and parsing output that every tool formats differently,
// and it answers a question routing does not ask. "Can this node build Go" is
// the decision; "which Go" is something the session that gets the work can
// determine for itself, correctly, at the moment it matters.
func Capabilities() []string {
	capsOnce.Do(func() {
		for name, bin := range software {
			if _, err := exec.LookPath(bin); err == nil {
				capsList = append(capsList, name)
			}
		}
		sort.Strings(capsList)
	})
	return capsList
}
