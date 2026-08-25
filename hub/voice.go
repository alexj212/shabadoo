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
	"log"
	"net/http"
	"net/url"
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

// voiceMints bounds how often one device may mint. See rateLimiter — the same
// mechanism now bounds session-to-session messages, which is the other place a
// cost can run away.
var voiceMints = newRateLimiter(voiceRateWindow, voiceRateLimit)

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
	now := h.now()
	if !voiceMints.allow(id.Sub, now) {
		http.Error(w, fmt.Sprintf(
			"too many voice sessions: %d in the last hour for this device", voiceRateLimit),
			http.StatusTooManyRequests)
		return
	}

	signed, err := mintElevenLabsURL(r.Context(), ElevenLabsAgent)
	if err != nil {
		// Nothing was spent, so nothing is charged. See refund.
		voiceMints.refund(id.Sub, now)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	// Audited like every other action, and for the same reason: this one has a
	// bill attached, so "who started forty voice sessions on Tuesday" is a
	// question someone will eventually ask.
	h.scope(r.Context()).Audit(r.Context(), AuditEntry{
		Actor: actor(r.Context()), Action: "voice.session", Target: ElevenLabsAgent,
		Detail: "scope=" + scopeName(id.Scope),
	}, now)

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

// truncate bounds a provider-supplied string before it reaches the log. The
// message is theirs, not ours, so its length is not something to trust.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
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
		// The client is told the status and nothing else: an error from an
		// authenticated upstream call is a place account details leak into a
		// response an unprivileged-ish client reads.
		//
		// But that left the actual explanation nowhere. Configuring this the
		// first time produced a bare 401 on the phone while the provider was
		// saying, in the body, exactly what was wrong — "missing the permission
		// convai_write" — and reading it took a hand-run curl on the host. So
		// the diagnosis goes to the LOG, which lives on the machine that
		// already holds the key and is read by whoever deployed it.
		//
		// The provider's own fields only, never the raw body: an unbounded echo
		// of an authenticated response into a log is the same leak, somewhere
		// quieter.
		var e struct {
			Detail struct {
				Status  string `json:"status"`
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"detail"`
		}
		if json.Unmarshal(body, &e) == nil && (e.Detail.Status != "" || e.Detail.Message != "") {
			log.Printf("hub: voice mint refused by provider: %s status=%q code=%q: %s",
				resp.Status, e.Detail.Status, e.Detail.Code, truncate(e.Detail.Message, 200))
		} else {
			log.Printf("hub: voice mint refused by provider: %s (no parsable detail)", resp.Status)
		}
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
