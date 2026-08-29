package hub

// The paging dialect, shared by every growing collection.
//
// Proposed by the author of a client that will read all of them, on the
// principle that three cursor dialects would be worse than one imperfect one.
// Specified here first against tasks, because tasks are small, live, and
// depended on by nothing yet.
//
// Every rule below is an application of two this codebase already holds: do not
// throw information away at the boundary, and an empty answer and a constrained
// one are different answers.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrCursorExpired means a cursor can no longer be honoured.
//
// Returned rather than silently restarting from the beginning, which is the
// failure that looks like data: a client paging a conversation would append the
// whole transcript a second time and read it as new activity.
var ErrCursorExpired = errors.New("cursor expired")

// Cursor is opaque to clients by CONTRACT, not by cryptography.
//
// It is base64 JSON and a determined client can decode it. What makes it opaque
// is that the format is unspecified and may change in any release — saying that
// plainly is better than implying a guarantee nothing enforces. A client that
// does arithmetic on one breaks, and the breakage is theirs.
type Cursor struct {
	// Dir is the direction this cursor was minted for. A collection whose rows
	// MUTATE cannot use one ordering for both: history pages by creation, which
	// is stable, while a live tail pages by last change, so a row that moved
	// comes back. Mixing them silently pages the wrong way at the boundary, so
	// a cursor carries which it is and the other direction refuses it.
	Dir string `json:"d"` // "a" after (tail, by updated_at) | "b" before (history, by created_at)
	TS  int64  `json:"t"`
	ID  string `json:"i"` // tie-break, so equal timestamps cannot loop or skip
}

func encodeCursor(c Cursor) string {
	raw, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(s, want string) (Cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, fmt.Errorf("%w: not a cursor", ErrCursorExpired)
	}
	var c Cursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return Cursor{}, fmt.Errorf("%w: not a cursor", ErrCursorExpired)
	}
	if c.Dir != want {
		return Cursor{}, fmt.Errorf("%w: that cursor pages %s, this request pages %s",
			ErrCursorExpired, dirName(c.Dir), dirName(want))
	}
	return c, nil
}

func dirName(d string) string {
	if d == "a" {
		return "forward"
	}
	return "backward"
}

// Page is what every paged endpoint returns.
type Page struct {
	// Next is ALWAYS present, including when the page is empty. Catching up must
	// be a stated answer rather than inferred from a zero-length array: a client
	// that gets {"items":[],"next":"…"} knows it is current, where one that gets
	// an empty body has to assume it.
	Next string `json:"next"`

	// Tail is present only on an INITIAL page — one requested with no cursor.
	//
	// Such a page is the entry point to both directions, and a client needs
	// both: `next` continues backward through history as somebody scrolls, and
	// `tail` follows forward as things change. Returning only one forces the
	// caller to synthesise the other, which it cannot do without parsing a
	// cursor it was told is opaque.
	Tail string `json:"tail,omitempty"`

	// Clamped says why a page is shorter than asked for, and is ABSENT when the
	// page was served whole. The two reasons mean opposite things to a client
	// tuning its scroll: "count" is ask for more next time, "bytes" is asking
	// for more will not help and may cost a round trip that returns less.
	Clamped string `json:"clamped,omitempty"` // "count" | "bytes"
}

// pageBudget bounds a response body.
//
// The coordinator proxies some collections from the node that owns them and
// buffers that through an 8 MB cap, so a page is not merely "large is slow" —
// past it the request fails outright. Half of it, because a client asking for
// 500 and getting 50 must be able to keep going rather than hit a wall.
const pageBudget = 4 << 20

// truncatedText cuts one oversized field and DECLARES it, rather than returning
// a short string that reads as the whole thing.
//
// Truncation here is the common path, not an edge case: a single task brief runs
// to kilobytes and a tool result to megabytes. A client cannot tell a truncated
// value from a genuinely short one, so a silent cut renders a fragment as fact.
func truncatedText(s string, max int) (string, bool, int) {
	if len(s) <= max {
		return s, false, len(s)
	}
	return s[:max], true, len(s)
}

// nowCursor mints the cursor a caller should send next.
func nowCursor(dir string, ts int64, id string) string {
	if ts == 0 {
		ts = time.Now().Unix()
	}
	return encodeCursor(Cursor{Dir: dir, TS: ts, ID: id})
}
