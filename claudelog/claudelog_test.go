package claudelog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSlug(t *testing.T) {
	// Every non-alphanumeric character becomes '-', which is why the encoding
	// is lossy and Resolve has to verify against the recorded cwd.
	cases := map[string]string{
		"/c/projects/claude.sh":    "-c-projects-claude-sh",
		"/c/projects/groq_flow":    "-c-projects-groq-flow",
		"/c/projects/homelab":      "-c-projects-homelab",
		"/home/operator/src.local/x":  "-home-operator-src-local-x",
		"/c/projects/acme/widgets": "-c-projects-acme-widgets",
	}
	for cwd, want := range cases {
		if got := slug(cwd); got != want {
			t.Errorf("slug(%q) = %q, want %q", cwd, got, want)
		}
	}
}

// line builds one transcript record.
func line(v map[string]any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b) + "\n"
}

func assistant(model string, in, out, cacheRead, cacheWrite int64, tools ...string) map[string]any {
	content := []any{map[string]any{"type": "text", "text": "ok"}}
	for _, name := range tools {
		content = append(content, map[string]any{"type": "tool_use", "name": name, "id": "t"})
	}
	return map[string]any{
		"type":      "assistant",
		"sessionId": "sess-1",
		"cwd":       "/c/projects/demo.app",
		"timestamp": "2026-07-29T17:30:00.000Z",
		"message": map[string]any{
			"role":    "assistant",
			"model":   model,
			"content": content,
			"usage": map[string]any{
				"input_tokens":                in,
				"output_tokens":               out,
				"cache_read_input_tokens":     cacheRead,
				"cache_creation_input_tokens": cacheWrite,
			},
		},
	}
}

// fixture writes a project tree at a temp projectsDir and returns the
// transcript path. Callers get a realistic mix: bookkeeping records, a meta
// user record that must not count as a turn, and real turns.
func fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	projectsDir = dir
	t.Cleanup(func() { projectsDir = filepath.Join(home(), ".claude", "projects") })

	proj := filepath.Join(dir, "-c-projects-demo-app")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(proj, "sess-1.jsonl")

	var b strings.Builder
	b.WriteString(line(map[string]any{
		"type": "user", "sessionId": "sess-1", "cwd": "/c/projects/demo.app",
		"gitBranch": "main", "version": "2.1.0", "timestamp": "2026-07-29T17:25:00.000Z",
		"message": map[string]any{"role": "user", "content": "hello"},
	}))
	b.WriteString(line(map[string]any{"type": "custom-title", "customTitle": "demo-wsl"}))
	b.WriteString(line(map[string]any{"type": "permission-mode", "permissionMode": "bypassPermissions"}))
	b.WriteString(line(map[string]any{
		"type": "user", "isMeta": true, // injected context — not a human turn
		"message": map[string]any{"role": "user", "content": "<system-reminder>x</system-reminder>"},
	}))
	b.WriteString(line(assistant("claude-opus-5", 2, 100, 5000, 300, "Bash", "Read")))
	b.WriteString(line(map[string]any{"type": "last-prompt", "lastPrompt": "  build the thing  "}))
	b.WriteString(line(assistant("claude-opus-5", 3, 50, 6000, 100, "Bash")))

	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSummarize(t *testing.T) {
	fixture(t)
	cache.entries = map[string]Summary{}

	s, err := Summarize("/c/projects/demo.app")
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}

	if s.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want sess-1", s.SessionID)
	}
	if s.Title != "demo-wsl" {
		t.Errorf("Title = %q, want demo-wsl", s.Title)
	}
	if s.GitBranch != "main" || s.Version != "2.1.0" {
		t.Errorf("branch/version = %q/%q, want main/2.1.0", s.GitBranch, s.Version)
	}
	if s.PermissionMode != "bypassPermissions" {
		t.Errorf("PermissionMode = %q", s.PermissionMode)
	}
	if s.Model != "claude-opus-5" || s.Models["claude-opus-5"] != 2 {
		t.Errorf("model = %q, models = %v", s.Model, s.Models)
	}
	// The isMeta user record is injected context, not something the human
	// typed, so it must not inflate the turn count.
	if s.UserTurns != 1 {
		t.Errorf("UserTurns = %d, want 1 (isMeta record must not count)", s.UserTurns)
	}
	if s.AssistantTurns != 2 || s.Messages != 3 {
		t.Errorf("turns = %d assistant / %d total, want 2/3", s.AssistantTurns, s.Messages)
	}
	want := Tokens{Input: 5, Output: 150, CacheRead: 11000, CacheWrite: 400}
	if s.Tokens != want {
		t.Errorf("Tokens = %+v, want %+v", s.Tokens, want)
	}
	if s.Tools["Bash"] != 2 || s.Tools["Read"] != 1 {
		t.Errorf("Tools = %v, want Bash:2 Read:1", s.Tools)
	}
	if s.LastPrompt != "build the thing" {
		t.Errorf("LastPrompt = %q, want it trimmed", s.LastPrompt)
	}
	if s.Started != time.Date(2026, 7, 29, 17, 25, 0, 0, time.UTC).Unix() {
		t.Errorf("Started = %d", s.Started)
	}
	if s.Last != time.Date(2026, 7, 29, 17, 30, 0, 0, time.UTC).Unix() {
		t.Errorf("Last = %d", s.Last)
	}
}

// A growing transcript must fold incrementally: the second call parses only
// the appended bytes, and the totals must match a scan from byte zero.
func TestSummarizeIncremental(t *testing.T) {
	path := fixture(t)
	cache.entries = map[string]Summary{}

	first, err := Summarize("/c/projects/demo.app")
	if err != nil {
		t.Fatal(err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(line(assistant("claude-opus-5", 1, 25, 7000, 50, "Edit")))
	f.Close()

	second, err := Summarize("/c/projects/demo.app")
	if err != nil {
		t.Fatal(err)
	}
	if second.Offset <= first.Offset {
		t.Errorf("offset did not advance: %d -> %d", first.Offset, second.Offset)
	}

	cache.entries = map[string]Summary{} // force a cold scan
	fresh, err := Summarize("/c/projects/demo.app")
	if err != nil {
		t.Fatal(err)
	}
	if second.Tokens != fresh.Tokens {
		t.Errorf("incremental tokens %+v != full-scan %+v", second.Tokens, fresh.Tokens)
	}
	if second.Messages != fresh.Messages || second.Tools["Edit"] != 1 {
		t.Errorf("incremental %d msgs tools %v, full-scan %d msgs",
			second.Messages, second.Tools, fresh.Messages)
	}
}

// A record still being written has no trailing newline. It must not be folded
// in, and the cursor must stay behind it so the completed line is read once.
func TestPartialTailIsNotConsumed(t *testing.T) {
	path := fixture(t)
	cache.entries = map[string]Summary{}

	complete, err := Summarize("/c/projects/demo.app")
	if err != nil {
		t.Fatal(err)
	}

	partial := strings.TrimSuffix(line(assistant("claude-opus-5", 1, 10, 0, 0, "Grep")), "\n")
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(partial[:len(partial)/2])
	f.Close()

	mid, err := Summarize("/c/projects/demo.app")
	if err != nil {
		t.Fatal(err)
	}
	if mid.Offset != complete.Offset {
		t.Errorf("cursor advanced into a partial record: %d -> %d", complete.Offset, mid.Offset)
	}
	if mid.Messages != complete.Messages {
		t.Errorf("half-written record was counted: %d -> %d", complete.Messages, mid.Messages)
	}

	// Finish the record; now it should be picked up exactly once.
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(partial[len(partial)/2:] + "\n")
	f.Close()

	done, err := Summarize("/c/projects/demo.app")
	if err != nil {
		t.Fatal(err)
	}
	if done.Messages != complete.Messages+1 || done.Tools["Grep"] != 1 {
		t.Errorf("completed record not folded once: %d msgs, tools %v", done.Messages, done.Tools)
	}
}

// Resolve must not trust the encoded directory name: two different cwds can
// encode to the same directory, and only the recorded cwd distinguishes them.
func TestResolveCollidingSlugs(t *testing.T) {
	dir := t.TempDir()
	projectsDir = dir
	t.Cleanup(func() { projectsDir = filepath.Join(home(), ".claude", "projects") })

	// "/c/x/a.b" and "/c/x/a-b" both encode to "-c-x-a-b".
	proj := filepath.Join(dir, "-c-x-a-b")
	os.MkdirAll(proj, 0o755)

	older := filepath.Join(proj, "older.jsonl")
	newer := filepath.Join(proj, "newer.jsonl")
	os.WriteFile(older, []byte(line(map[string]any{"type": "user", "cwd": "/c/x/a.b"})), 0o600)
	os.WriteFile(newer, []byte(line(map[string]any{"type": "user", "cwd": "/c/x/a-b"})), 0o600)
	old := time.Now().Add(-time.Hour)
	os.Chtimes(older, old, old)

	got, err := Resolve("/c/x/a.b")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != older {
		t.Errorf("Resolve picked %q, want %q — the newest file has a different cwd",
			filepath.Base(got), filepath.Base(older))
	}
}

func TestResolveNotFound(t *testing.T) {
	projectsDir = t.TempDir()
	t.Cleanup(func() { projectsDir = filepath.Join(home(), ".claude", "projects") })

	if _, err := Resolve("/c/projects/nothing-here"); err != ErrNotFound {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
