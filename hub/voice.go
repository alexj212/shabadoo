package hub

// Minting a voice session, without shipping the key that pays for it.
//
// A conversational voice agent needs a credential to open its socket. The one
// thing that must not happen is that credential being the ElevenLabs API key:
// it is long-lived, it is account-wide, and it is billed per minute, so a copy
// inside a phone app is a copy inside every phone that ever installs it.
//
// So the coordinator holds the key and hands out short-lived signed URLs — the
// same arrangement as `--apprise-url`, and for the same reason: the credential
// and the routing config are one thing in one place, and a client able to reach
// the provider directly would need the ability to spend on it.
//
// # What the voice agent can do, and how that is enforced
//
// Nothing here grants any permission. The agent's tools run on the CLIENT,
// against this same API, with the device's own token — so a read-scoped phone's
// attempt to send into a pane is refused by `requireWrite` without the voice
// layer knowing scopes exist. The voice agent can never exceed the device
// holding it, because it is not a separate identity.
//
// It cannot answer a dialog, and that is enforced by NOT GIVING IT THE TOOL
// rather than by instructing it. An agent told "do not approve prompts" can be
// argued into approving one; an agent with no keypress tool cannot, whatever it
// decides. This project has three times refused to make approval easy —
// no answer button on a queue row, selecting from the queue opens the
// transcript, and the shipped question still says to read the pane — and a
// voice channel is the strongest possible version of answering without reading,
// on panes running with permissions disabled.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

var (
	// ElevenLabsKey is the account API key. Empty disables the endpoint
	// entirely rather than failing per-call, so a deployment without voice says
	// so once at startup instead of surprising someone mid-sentence.
	ElevenLabsKey string

	// elevenLabsBase is the provider's API root. A variable, not a constant,
	// solely so a test can point it at a stub — the mint is the part of this
	// file with real logic in it (headers, status handling, response shape),
	// and a hardcoded URL made all of it unreachable without an account.
	elevenLabsBase = "https://api.elevenlabs.io"

	// ElevenLabsAgent is the configured conversational agent's id. The agent's
	// prompt, voice and tool list live in the provider's dashboard, not here —
	// which means the tools it believes it has and the API it actually calls
	// can drift apart. Worth checking when either changes.
	ElevenLabsAgent string
)

const (
	// voiceMintTimeout bounds the outbound call. Someone is holding a phone
	// waiting to talk; a provider that hangs must not hold the request open.
	voiceMintTimeout = 10 * time.Second

	// voiceRateWindow and voiceRateLimit bound how often one device may mint.
	//
	// This is the first credential the coordinator hands out that costs money —
	// every session is billed per minute — so the limit is about SPEND, not
	// about guessing, which is what the redeem throttle is for. Generous enough
	// that a dropped connection can be retried, tight enough that a leaked
	// token cannot quietly run up a bill overnight.
	voiceRateWindow = time.Hour
	voiceRateLimit  = 30
)

// voiceLimiter tracks mints per device.
type voiceLimiter struct {
	mu sync.Mutex
	at map[string][]time.Time
}

var voiceMints = &voiceLimiter{at: map[string][]time.Time{}}

func (v *voiceLimiter) allow(id string, now time.Time) bool {
	v.mu.Lock()
	defer v.mu.Unlock()

	cutoff := now.Add(-voiceRateWindow)
	kept := v.at[id][:0]
	for _, t := range v.at[id] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= voiceRateLimit {
		v.at[id] = kept
		return false
	}
	v.at[id] = append(kept, now)
	return true
}

// voiceSession mints a short-lived signed URL for a conversational session.
//
// A GET would be more natural for something that reads no state, but this
// spends money and is rate limited per caller, so it is a POST: a URL that
// bills on retrieval has no business being prefetched, cached, or followed by
// something crawling links.
func (h *humanAPI) voiceSession(w http.ResponseWriter, r *http.Request) {
	if ElevenLabsKey == "" || ElevenLabsAgent == "" {
		http.Error(w, "voice is not configured on this coordinator", http.StatusNotFound)
		return
	}
	id, _ := IdentityFrom(r.Context())

	// Keyed on the credential, not the tenant: one device's enthusiasm must not
	// exhaust another's budget.
	if !voiceMints.allow(id.Sub, h.now()) {
		http.Error(w, fmt.Sprintf(
			"too many voice sessions: %d in the last hour for this device", voiceRateLimit),
			http.StatusTooManyRequests)
		return
	}

	signed, err := mintElevenLabsURL(r.Context(), ElevenLabsAgent)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	// Audited like every other action, and for the same reason: this one has a
	// bill attached, so "who started forty voice sessions on Tuesday" is a
	// question someone will eventually ask.
	h.scope(r.Context()).Audit(r.Context(), AuditEntry{
		Actor: actor(r.Context()), Action: "voice.session", Target: ElevenLabsAgent,
		Detail: "scope=" + scopeName(id.Scope),
	}, h.now())

	// The scope is reported so the client can shape what it offers — grey out
	// dictation on a read-only device rather than letting someone talk into a
	// 403. It is NOT what enforces anything: the tools call this API with this
	// device's token, and requireWrite is what actually refuses.
	writeJSON(w, map[string]any{
		"signed_url": signed,
		"agent_id":   ElevenLabsAgent,
		"scope":      scopeName(id.Scope),
	})
}

// mintElevenLabsURL asks the provider for a signed WebSocket URL.
func mintElevenLabsURL(ctx context.Context, agentID string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, voiceMintTimeout)
	defer cancel()

	endpoint := elevenLabsBase + "/v1/convai/conversation/get-signed-url?agent_id=" +
		url.QueryEscape(agentID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("xi-api-key", ElevenLabsKey)

	resp, err := (&http.Client{Timeout: voiceMintTimeout}).Do(req)
	if err != nil {
		return "", fmt.Errorf("voice provider unreachable: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))

	if resp.StatusCode/100 != 2 {
		// Deliberately does not echo the provider's body verbatim: an error
		// from an authenticated upstream call is a place account details leak
		// into a response an unprivileged-ish client reads.
		return "", fmt.Errorf("voice provider returned %s", resp.Status)
	}

	var out struct {
		SignedURL string `json:"signed_url"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.SignedURL == "" {
		return "", fmt.Errorf("voice provider returned no signed url")
	}
	return out.SignedURL, nil
}
