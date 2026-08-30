package claudelog

// Turns from a transcript, for a client that wants to READ a conversation
// rather than count it.
//
// `Summarize` folds a whole file into totals and caches by offset, which is
// right for a five-second fleet report and useless for showing somebody what
// was said. This returns the messages.
//
// **It reads BACKWARDS from the end, and that is the load-bearing decision.**
// Transcripts on this fleet reach 136 MB; a tail implemented as "scan the file
// and keep the last N" would read all of it on every poll, for every session
// somebody has open. Seeking to the end and walking back in chunks costs the
// size of what is returned, not the size of the file.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

// Bounds. Every one of these exists because the response crosses `proxyGet`,
// which buffers a peer's answer through io.ReadAll with an 8 MB ceiling — a
// single pasted file in one turn can exceed that on its own, so truncation
// happens ON THE WIRE and not in the client.
const (
	MaxEvents   = 200      // per request
	maxText     = 4000     // runes of message text kept per event
	maxToolIn   = 600      // runes of a tool call's input
	maxToolOut  = 1200     // runes of a tool result
	chunkSize   = 1 << 18  // 256 KB per backward read
	maxScanBack = 64 << 20 // give up walking back after 64 MB
)

// ToolCall is one tool_use block, collapsed.
//
// The name and a short input, never the whole thing: tool blocks are the bulk
// of a transcript's bytes and almost never what somebody is reading. A client
// renders one line and expands on demand.
type ToolCall struct {
	Name      string `json:"name"`
	Input     string `json:"input,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// Event is one turn as a reader sees it.
type Event struct {
	Type string `json:"type"` // user | assistant | tool_result | meta
	Role string `json:"role,omitempty"`
	Time int64  `json:"time,omitempty"`

	Text string `json:"text,omitempty"`
	// Truncated and Len report a clamped body AND its true size. A client
	// cannot tell a short message from a cut one, and a state that is known and
	// not shown is indistinguishable from that state not existing.
	Truncated bool `json:"truncated,omitempty"`
	Len       int  `json:"len,omitempty"` // runes, before clamping

	Tools []ToolCall `json:"tools,omitempty"`
	Model string     `json:"model,omitempty"`

	// Sidechain marks a subagent's message. Rendered differently rather than
	// hidden: a reader who cannot tell a subagent's words from the session's own
	// is being shown a conversation that did not happen.
	Sidechain bool `json:"sidechain,omitempty"`

	// Offset is the byte position just past this record. It is the cursor to
	// poll forward from, and it is per-record rather than per-page so a client
	// that renders half a page still has a correct watermark.
	Offset int64 `json:"offset"`
}

// EventPage is one screenful.
type EventPage struct {
	Events []Event `json:"events"`
	// Cursor polls FORWARD: pass it back as `after` to get what has been
	// written since. It is the end of the file as of this read, not the end of
	// the last event, so a record skipped for being unreadable is not re-read
	// forever.
	Cursor int64 `json:"cursor"`
	// Prev pages BACKWARD: pass it as `before` for older turns. Zero means the
	// beginning of the file was reached — which is a different answer from
	// "there might be more", and a client must be able to stop.
	Prev int64 `json:"prev"`
	// More says whether Prev is meaningful. Encoding "no more" as Prev==0 alone
	// would be indistinguishable from an offset that happens to be zero.
	More bool  `json:"more"`
	Size int64 `json:"size"`
}

// EventOpts selects a page. Exactly one direction applies: After polls forward,
// Before pages back, neither tails the end.
type EventOpts struct {
	After  int64 // read forward from this byte offset
	Before int64 // read backward from this byte offset
	Limit  int
}

// Events returns a page of turns from a transcript file.
func Events(path string, opts EventOpts) (EventPage, error) {
	if opts.Limit <= 0 || opts.Limit > MaxEvents {
		opts.Limit = 50
	}
	f, err := os.Open(path)
	if err != nil {
		return EventPage{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return EventPage{}, err
	}
	size := st.Size()
	page := EventPage{Events: []Event{}, Cursor: size, Size: size}

	if opts.After > 0 {
		// Forward: what has been appended since the client last looked. A file
		// that SHRANK means the transcript was rotated or replaced, and reading
		// from a stale offset would splice two conversations together — so
		// treat it as a fresh tail rather than trusting the cursor.
		if opts.After > size {
			opts.After = 0
		} else {
			evs, err := forward(f, opts.After, size, opts.Limit)
			if err != nil {
				return page, err
			}
			page.Events = evs
			if n := len(evs); n > 0 {
				page.Cursor = evs[n-1].Offset
			} else {
				page.Cursor = opts.After
			}
			page.More = opts.After > 0
			page.Prev = opts.After
			return page, nil
		}
	}

	end := size
	if opts.Before > 0 && opts.Before < size {
		end = opts.Before
	}
	evs, start, err := backward(f, end, opts.Limit)
	if err != nil {
		return page, err
	}
	page.Events = evs
	page.Prev = start
	page.More = start > 0
	if opts.Before > 0 {
		// Paging back does not advance the forward cursor: the client already
		// holds one for the newest end, and overwriting it with an older offset
		// would make it re-read everything in between as if it were new.
		page.Cursor = opts.Before
	}
	return page, nil
}

// forward reads records from `from` towards the end, stopping at limit.
func forward(f *os.File, from, size int64, limit int) ([]Event, error) {
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return nil, err
	}
	out := make([]Event, 0, limit)
	pos := from
	r := bufio.NewReaderSize(f, 64<<10)
	for len(out) < limit && pos < size {
		line, n, err := readLine(r)
		pos += int64(n)
		if len(bytes.TrimSpace(line)) > 0 {
			if e, ok := decodeEvent(line); ok {
				e.Offset = pos
				out = append(out, e)
			}
		}
		if err != nil {
			return out, nil // EOF or an over-long record: what was read is valid
		}
	}
	return out, nil
}

// backward collects the last `limit` READABLE turns ending at `end`, walking
// the file towards the start in chunks.
//
// Counting readable turns rather than lines is the whole difficulty. A
// transcript is mostly records a reader never sees — tool plumbing, meta
// injections, file-history snapshots — so a loop that stops after N LINES
// routinely returns nothing at all: the last four lines of a live transcript
// here decoded to zero events, which is how this was found.
//
// Returns the byte offset of the first turn kept, which is what a client pages
// back from; zero means the start of the file was reached.
func backward(f *os.File, end int64, limit int) ([]Event, int64, error) {
	type parsed struct {
		bytes int64 // the line plus its newline
		ev    Event
		ok    bool
	}
	var (
		lines []parsed // newest first
		found int      // readable turns among them
		carry []byte
		pos   = end
		read  int64
	)
	chunk := make([]byte, chunkSize)

	for pos > 0 && found <= limit && read < maxScanBack {
		n := int64(chunkSize)
		if pos < n {
			n = pos
		}
		pos -= n
		read += n
		if _, err := f.Seek(pos, io.SeekStart); err != nil {
			return nil, 0, err
		}
		if _, err := io.ReadFull(f, chunk[:n]); err != nil {
			return nil, 0, err
		}
		block := append(append([]byte{}, chunk[:n]...), carry...)
		parts := bytes.Split(block, []byte{'\n'})
		// The first part may continue from earlier in the file, so it is
		// carried rather than parsed — unless the start has been reached, where
		// it is a whole line.
		if pos > 0 {
			carry = append([]byte{}, parts[0]...)
			parts = parts[1:]
		} else {
			carry = nil
		}
		for i := len(parts) - 1; i >= 0 && found <= limit; i-- {
			p := parsed{bytes: int64(len(parts[i])) + 1}
			if len(bytes.TrimSpace(parts[i])) > 0 {
				p.ev, p.ok = decodeEvent(parts[i])
				if p.ok {
					found++
				}
			}
			lines = append(lines, p)
		}
	}

	// One extra readable turn is collected only to learn whether anything
	// precedes the page. "There might be more" and "this is the beginning" are
	// different answers, and a client scrolling up has to be able to stop.
	more := found > limit
	if more {
		// Drop everything from the extra turn backwards, so the page holds
		// exactly `limit` and the offsets still close on `end`.
		seen := 0
		for i, p := range lines {
			if p.ok {
				seen++
				if seen > limit {
					lines = lines[:i]
					break
				}
			}
		}
	}

	// Offsets are derived by measuring FORWARD from the oldest kept line, so a
	// cursor is a real file position rather than a count of rendered rows. The
	// arithmetic closes on `end` by construction.
	var consumed int64
	for _, p := range lines {
		consumed += p.bytes
	}
	start := end - consumed
	if start < 0 {
		start = 0
	}
	if !more {
		start = 0 // the beginning was reached; say so rather than implying more
	}

	out := make([]Event, 0, limit)
	off := end - consumed
	for i := len(lines) - 1; i >= 0; i-- { // oldest first
		off += lines[i].bytes
		if lines[i].ok {
			e := lines[i].ev
			e.Offset = off
			out = append(out, e)
		}
	}
	return out, start, nil
}

// eventRecord is the subset a reader needs. Deliberately its own type rather
// than reusing `record`: that one exists to be summed and would drag the whole
// usage block through every message here.
type eventRecord struct {
	Type        string `json:"type"`
	Timestamp   string `json:"timestamp"`
	IsSidechain bool   `json:"isSidechain"`
	IsMeta      bool   `json:"isMeta"`
	Message     *struct {
		Role    string          `json:"role"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// eventBlock is one element of a content array, with the fields a reader shows.
type eventBlock struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Content json.RawMessage `json:"content"`
}

// decodeEvent turns one transcript line into an Event, reporting whether it is
// one a reader should see.
//
// Unreadable lines are DROPPED rather than rendered as an error row. A
// transcript's last line is routinely a partial write, and a page whose final
// entry is "could not parse" would show that every few seconds on a live
// session — training the reader to ignore the one time it means something.
func decodeEvent(line []byte) (Event, bool) {
	var rec eventRecord
	if json.Unmarshal(line, &rec) != nil {
		return Event{}, false
	}
	if rec.Type != "user" && rec.Type != "assistant" {
		return Event{}, false
	}
	if rec.IsMeta {
		return Event{}, false // injected context, not something anybody said
	}
	e := Event{Type: rec.Type, Sidechain: rec.IsSidechain}
	if t, err := time.Parse(time.RFC3339, rec.Timestamp); err == nil {
		e.Time = t.Unix()
	}
	if rec.Message == nil {
		return Event{}, false
	}
	e.Role, e.Model = rec.Message.Role, rec.Message.Model

	// Content is either a bare string or an array of blocks, and both shapes
	// occur in a single file.
	var text string
	if err := json.Unmarshal(rec.Message.Content, &text); err == nil {
		e.Text, e.Len, e.Truncated = clamp(text, maxText)
		return e, e.Text != ""
	}
	var blocks []eventBlock
	if json.Unmarshal(rec.Message.Content, &blocks) != nil {
		return Event{}, false
	}
	var sb strings.Builder
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				if sb.Len() > 0 {
					sb.WriteString("\n\n")
				}
				sb.WriteString(b.Text)
			}
		case "tool_use":
			in, _, cut := clamp(compactJSON(b.Input), maxToolIn)
			e.Tools = append(e.Tools, ToolCall{Name: b.Name, Input: in, Truncated: cut})
		case "tool_result":
			// A result belongs to the call above it and is the single biggest
			// contributor to transcript size. Kept short and unnamed; a client
			// that wants the whole thing has the pane.
			s, _, cut := clamp(blockText(b.Content), maxToolOut)
			if s != "" {
				e.Tools = append(e.Tools, ToolCall{Name: "result", Input: s, Truncated: cut})
			}
		}
	}
	e.Text, e.Len, e.Truncated = clamp(sb.String(), maxText)
	return e, e.Text != "" || len(e.Tools) > 0
}

// blockText pulls readable text out of a tool_result's content, which is
// itself either a string or an array of blocks.
func blockText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []eventBlock
	if json.Unmarshal(raw, &blocks) == nil {
		var sb strings.Builder
		for _, b := range blocks {
			if b.Text != "" {
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(b.Text)
			}
		}
		return sb.String()
	}
	return ""
}

func compactJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var out bytes.Buffer
	if json.Compact(&out, raw) != nil {
		return string(raw)
	}
	return out.String()
}

// clamp bounds a string in RUNES and reports its true length.
//
// Runes, not bytes: a transcript is prose and code, and a byte cut splits a
// multi-byte character into invalid UTF-8 — which `encoding/json` then replaces
// silently, so the corruption reaches the client looking like content.
func clamp(s string, max int) (string, int, bool) {
	s = strings.TrimSpace(s)
	n := utf8.RuneCountInString(s)
	if n <= max {
		return s, n, false
	}
	return string([]rune(s)[:max]) + "…", n, true
}

func (p EventPage) String() string {
	return fmt.Sprintf("%d event(s), cursor %d, prev %d, more %v",
		len(p.Events), p.Cursor, p.Prev, p.More)
}
