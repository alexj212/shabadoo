// Package claudelog reads Claude Code's own session transcripts — the JSONL
// files it writes under ~/.claude/projects/<encoded-cwd>/<session-uuid>.jsonl —
// and summarizes them.
//
// This is a different layer from the tmux package: tmux reports what a window
// is (name, command, idle time), claudelog reports what the Claude session
// running inside it is doing (model, turns, tokens, tools). The two are joined
// by the window's pane_current_path, which is the session's cwd.
package claudelog

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrNotFound means no transcript exists for the requested cwd — a tmux window
// that has never run Claude, or one whose history has been cleared away.
var ErrNotFound = errors.New("no claude session for this directory")

// projectsDir is where Claude Code keeps per-folder history. It is a variable
// so tests can point it at a fixture tree.
//
// Deliberately NOT read from $CLAUDE_CONFIG_DIR: in this codebase that name
// already means the launcher's own config dir (~/.config/claude, see
// joinConfig in setup.go), which is a different directory entirely. Reusing
// the variable here would resolve to the wrong tree on any host that sets it.
var projectsDir = filepath.Join(home(), ".claude", "projects")

func home() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return os.Getenv("HOME")
}

// maxLine caps how much of a single JSONL record we are willing to hold in
// memory. Records carry whole tool results, so a few megabytes is normal;
// anything past this is treated as corrupt and skipped rather than parsed.
const maxLine = 32 << 20

// ---------------------------------------------------------------------------
// resolution: cwd -> transcript file
// ---------------------------------------------------------------------------

// slug encodes a cwd the way Claude Code names its project directories: every
// character outside [A-Za-z0-9] becomes '-', so /c/projects/claude.sh is
// "-c-projects-claude-sh".
//
// The encoding is lossy — claude.sh, claude-sh and claude_sh all collapse to
// the same directory — so it is only ever a hint. Resolve confirms the match
// by reading the cwd back out of the transcript itself.
func slug(cwd string) string {
	var b strings.Builder
	b.Grow(len(cwd))
	for _, r := range cwd {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// Resolve returns the path of the most recent transcript recorded for cwd.
//
// It tries the encoded directory name first, then — because that encoding is
// lossy — falls back to scanning every project directory and matching on the
// cwd each transcript reports. The fast path is a stat; the fallback only runs
// for paths whose slug collides or is otherwise unexpected.
func Resolve(cwd string) (string, error) {
	if cwd == "" {
		return "", errors.New("cwd required")
	}
	if f, err := newestMatching(filepath.Join(projectsDir, slug(cwd)), cwd); err == nil {
		return f, nil
	}

	dirs, err := os.ReadDir(projectsDir)
	if err != nil {
		return "", ErrNotFound
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		if f, err := newestMatching(filepath.Join(projectsDir, d.Name()), cwd); err == nil {
			return f, nil
		}
	}
	return "", ErrNotFound
}

// newestMatching returns the newest transcript in dir whose recorded cwd is
// want. It walks newest-first rather than trusting the first file, so a
// directory shared by two colliding paths still resolves correctly.
func newestMatching(dir, want string) (string, error) {
	files, err := transcripts(dir)
	if err != nil {
		return "", err
	}
	for _, f := range files {
		if cwd, err := readCWD(f); err == nil && cwd == want {
			return f, nil
		}
	}
	return "", ErrNotFound
}

// transcripts lists a project directory's .jsonl files, newest first. The
// newest is the live session: --continue appends to the existing file and
// /clear starts a new one, so mtime order is session order.
func transcripts(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	type item struct {
		path string
		mod  time.Time
	}
	var items []item
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, item{filepath.Join(dir, e.Name()), info.ModTime()})
	}
	if len(items) == 0 {
		return nil, ErrNotFound
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mod.After(items[j].mod) })
	paths := make([]string, len(items))
	for i, it := range items {
		paths[i] = it.path
	}
	return paths, nil
}

// readCWD pulls the cwd out of a transcript's opening records. Not every
// record carries one (bookkeeping types like custom-title do not), so it scans
// a bounded number of lines before giving up.
func readCWD(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	r := bufio.NewReader(f)
	for i := 0; i < 200; i++ {
		line, _, err := readLine(r)
		if len(line) > 0 {
			var rec struct {
				CWD string `json:"cwd"`
			}
			if json.Unmarshal(line, &rec) == nil && rec.CWD != "" {
				return rec.CWD, nil
			}
		}
		if err != nil {
			break
		}
	}
	return "", ErrNotFound
}

// readLine reads one newline-terminated record, returning it along with the
// number of bytes it consumed — which is not len(line) when an over-long
// record is dropped, and the caller's byte cursor has to account for those
// bytes anyway.
//
// Lines routinely run to megabytes (a record holds a whole tool result), so
// bufio.Scanner is not usable here: its default 64KB token limit would abort
// the scan partway through a file. Anything past maxLine is consumed but not
// buffered.
//
// A returned line that does not end in '\n' is the tail of a file being
// written right now; callers must not treat it as complete.
func readLine(r *bufio.Reader) (line []byte, n int, err error) {
	var buf []byte
	dropped := false
	for {
		chunk, err := r.ReadSlice('\n')
		n += len(chunk)
		if !dropped && len(buf)+len(chunk) <= maxLine {
			buf = append(buf, chunk...)
		} else {
			dropped = true // over-long record: keep consuming, discard the bytes
			buf = nil
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		return buf, n, err
	}
}

// ---------------------------------------------------------------------------
// the transcript record
// ---------------------------------------------------------------------------

// record is the subset of a transcript line this package reads. Claude writes
// many record types (attachment, file-history-snapshot, mode, …); the fields
// below are the ones present across the types we summarize.
type record struct {
	Type           string `json:"type"`
	SessionID      string `json:"sessionId"`
	CWD            string `json:"cwd"`
	GitBranch      string `json:"gitBranch"`
	Version        string `json:"version"`
	Timestamp      string `json:"timestamp"`
	CustomTitle    string `json:"customTitle"`
	PermissionMode string `json:"permissionMode"`
	LastPrompt     string `json:"lastPrompt"`
	IsSidechain    bool   `json:"isSidechain"`
	IsMeta         bool   `json:"isMeta"`
	Message        *struct {
		Role    string          `json:"role"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			Input      int64 `json:"input_tokens"`
			Output     int64 `json:"output_tokens"`
			CacheRead  int64 `json:"cache_read_input_tokens"`
			CacheWrite int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// contentBlock is one element of a message's content array. Only the fields
// needed to count tool calls are decoded.
type contentBlock struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

// ---------------------------------------------------------------------------
// summary
// ---------------------------------------------------------------------------

// Tokens are the usage totals summed across a session's assistant messages.
// Cache reads dominate in practice — they are what a long session costs least
// to keep alive — so they are reported separately rather than folded into
// Input.
type Tokens struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
}

// Summary is everything the dashboard shows about a Claude session.
//
// There is deliberately no cost estimate: it would need a per-model price
// table baked into the binary, which goes stale silently. The token split
// carries the same signal without the staleness.
type Summary struct {
	SessionID      string         `json:"session_id"`
	File           string         `json:"file"`
	Title          string         `json:"title,omitempty"`
	CWD            string         `json:"cwd"`
	GitBranch      string         `json:"git_branch,omitempty"`
	Version        string         `json:"version,omitempty"`
	PermissionMode string         `json:"permission_mode,omitempty"`
	Model          string         `json:"model,omitempty"` // most recent model used
	Models         map[string]int `json:"models,omitempty"`
	Started        int64          `json:"started"` // unix seconds
	Last           int64          `json:"last"`
	Messages       int            `json:"messages"`
	UserTurns      int            `json:"user_turns"`
	AssistantTurns int            `json:"assistant_turns"`
	Sidechains     int            `json:"sidechains"` // subagent messages
	Tokens         Tokens         `json:"tokens"`
	Tools          map[string]int `json:"tools,omitempty"`
	LastPrompt     string         `json:"last_prompt,omitempty"`
	Size           int64          `json:"size"`   // transcript bytes
	Offset         int64          `json:"offset"` // bytes consumed; the cursor for incremental reads
}

// lastPromptMax bounds the prompt preview sent to the browser. The dashboard
// shows one line of it; whole prompts can be enormous (pasted files).
const lastPromptMax = 400

// Summarize resolves cwd to its live transcript and returns its summary.
func Summarize(cwd string) (Summary, error) {
	path, err := Resolve(cwd)
	if err != nil {
		return Summary{}, err
	}
	return SummarizeFile(path)
}

// SummarizeFile summarizes one transcript, reusing cached work when the file
// has only been appended to since the last call.
func SummarizeFile(path string) (Summary, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Summary{}, err
	}
	return cache.summarize(path, info.Size())
}

// scan folds a transcript's records into sum, starting at byte offset
// sum.Offset. Callers pass either a zero Summary (full scan) or a previously
// returned one (incremental — only the appended bytes are parsed).
func scan(path string, sum *Summary, size int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	if sum.Offset > 0 {
		if _, err := f.Seek(sum.Offset, io.SeekStart); err != nil {
			return err
		}
	}

	if sum.Models == nil {
		sum.Models = map[string]int{}
	}
	if sum.Tools == nil {
		sum.Tools = map[string]int{}
	}
	sum.File = path
	sum.Size = size

	var lastTS string
	r := bufio.NewReaderSize(f, 256<<10)
	for {
		line, n, err := readLine(r)
		complete := len(line) > 0 && line[len(line)-1] == '\n'

		// A trailing record with no newline is a line the live session is
		// still writing. Leave the cursor before it so the next pass re-reads
		// it once it is whole, and do not fold a half-written record in.
		if !complete && err == io.EOF {
			break
		}
		sum.Offset += int64(n)
		if len(line) > 0 {
			if ts := fold(sum, line); ts != "" {
				lastTS = ts
			}
		}
		if err != nil {
			if err != io.EOF {
				return err
			}
			break
		}
	}

	if lastTS != "" {
		if t, err := time.Parse(time.RFC3339, lastTS); err == nil {
			sum.Last = t.Unix()
		}
	}
	return nil
}

// fold accumulates one record into sum and returns the record's timestamp.
func fold(sum *Summary, line []byte) string {
	var rec record
	if json.Unmarshal(line, &rec) != nil {
		return "" // not JSON (a truncated tail, say) — skip it
	}

	// Identity fields: last writer wins, so a session that changes branch or
	// permission mode mid-flight reports its current state.
	if rec.SessionID != "" {
		sum.SessionID = rec.SessionID
	}
	if rec.CWD != "" {
		sum.CWD = rec.CWD
	}
	if rec.GitBranch != "" {
		sum.GitBranch = rec.GitBranch
	}
	if rec.Version != "" {
		sum.Version = rec.Version
	}
	if rec.CustomTitle != "" {
		sum.Title = rec.CustomTitle
	}
	if rec.PermissionMode != "" {
		sum.PermissionMode = rec.PermissionMode
	}
	if rec.LastPrompt != "" {
		sum.LastPrompt = truncate(rec.LastPrompt, lastPromptMax)
	}
	if rec.Timestamp != "" && sum.Started == 0 {
		if t, err := time.Parse(time.RFC3339, rec.Timestamp); err == nil {
			sum.Started = t.Unix()
		}
	}

	switch rec.Type {
	case "user":
		if rec.IsMeta {
			break // injected context, not something the human typed
		}
		sum.Messages++
		sum.UserTurns++
		if rec.IsSidechain {
			sum.Sidechains++
		}
	case "assistant":
		sum.Messages++
		sum.AssistantTurns++
		if rec.IsSidechain {
			sum.Sidechains++
		}
		if rec.Message == nil {
			break
		}
		if m := rec.Message.Model; m != "" {
			sum.Models[m]++
			sum.Model = m
		}
		if u := rec.Message.Usage; u != nil {
			sum.Tokens.Input += u.Input
			sum.Tokens.Output += u.Output
			sum.Tokens.CacheRead += u.CacheRead
			sum.Tokens.CacheWrite += u.CacheWrite
		}
		var blocks []contentBlock
		if json.Unmarshal(rec.Message.Content, &blocks) == nil {
			for _, b := range blocks {
				if b.Type == "tool_use" && b.Name != "" {
					sum.Tools[b.Name]++
				}
			}
		}
	}
	return rec.Timestamp
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ---------------------------------------------------------------------------
// cache
// ---------------------------------------------------------------------------

// summaryCache keeps the last computed summary per transcript so a growing
// file is only ever parsed once. Transcripts reach tens of megabytes and the
// dashboard polls every few seconds; re-reading from byte zero each time would
// cost more than everything else the server does combined.
//
// Entries are keyed by path and never evicted: one entry per project folder
// the user has opened (a few dozen), each a few hundred bytes.
type summaryCache struct {
	mu      sync.Mutex
	entries map[string]Summary
}

var cache = &summaryCache{entries: map[string]Summary{}}

func (c *summaryCache) summarize(path string, size int64) (Summary, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	sum, ok := c.entries[path]
	switch {
	case !ok, sum.Offset > size:
		// Unseen, or the file shrank — it was rewritten, so start over.
		sum = Summary{}
	case sum.Offset == size:
		return copySummary(sum), nil // nothing appended since last time
	}

	if err := scan(path, &sum, size); err != nil {
		return Summary{}, err
	}
	c.entries[path] = sum
	return copySummary(sum), nil
}

// copySummary hands callers their own maps: the cached entry is shared and
// must not be mutated (or serialized) while a later scan folds into it.
func copySummary(s Summary) Summary {
	s.Models = cloneCount(s.Models)
	s.Tools = cloneCount(s.Tools)
	return s
}

func cloneCount(m map[string]int) map[string]int {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// String renders a one-line summary, for logs and debugging.
func (s Summary) String() string {
	return fmt.Sprintf("%s %s %d msgs %d out/%d cached",
		s.SessionID, s.Model, s.Messages, s.Tokens.Output, s.Tokens.CacheRead)
}

// ---------------------------------------------------------------------------
// project discovery
// ---------------------------------------------------------------------------

// Project is a folder Claude Code has a transcript for.
type Project struct {
	Path       string    `json:"path"`
	LastActive time.Time `json:"last_active"`
}

// Projects lists every folder this host has run a Claude session in, most
// recently active first.
//
// The real cwd is read out of each project's newest transcript rather than
// decoded from the directory name: that encoding is lossy (claude.sh,
// claude-sh and claude_sh all collapse to the same slug), so reversing it
// would invent paths that never existed. A directory whose transcripts are
// unreadable is skipped — a folder list is a convenience, and one bad file
// should not empty it.
func Projects() ([]Project, error) {
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // a host that has never run Claude is not an error
		}
		return nil, err
	}

	var out []Project
	seen := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		files, err := transcripts(filepath.Join(projectsDir, e.Name()))
		if err != nil || len(files) == 0 {
			continue
		}
		cwd, err := readCWD(files[0])
		if err != nil || cwd == "" || seen[cwd] {
			continue
		}
		info, err := os.Stat(files[0])
		if err != nil {
			continue
		}
		seen[cwd] = true
		out = append(out, Project{Path: cwd, LastActive: info.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastActive.After(out[j].LastActive) })
	return out, nil
}
