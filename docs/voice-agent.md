# The voice agent's definition

The conversational agent's prompt, voice and tool list live in the **ElevenLabs
dashboard**, not in this repository. That is a problem: what the agent believes
it can call and what this API actually serves can drift apart with nothing to
catch it, and the failure is silent — the agent invokes a tool the client does
not implement, and the conversation simply stops making sense.

This file is the intended configuration, checked in so the drift is at least
**visible**. It is documentation of intent, not the source of truth. When the
dashboard and this file disagree, one of them is a bug.

---

## Coordinator configuration

Two flags, both required. Half a configuration leaves the endpoint disabled and
says so at startup rather than failing when somebody is holding a phone.

| Flag | Environment | What |
|---|---|---|
| `--elevenlabs-key` | `SHABADOO_ELEVENLABS_KEY` | account API key |
| `--elevenlabs-agent` | `SHABADOO_ELEVENLABS_AGENT` | the agent's id, from the dashboard |

**Read at startup, not per request** — they land in package variables when the
process starts, so changing either needs a restart. `POST /api/voice/session`
returns **404 `voice is not configured on this coordinator`** until both are
set, which clients treat as "this deployment does not do voice" and hide the
feature.

The coordinator **does not create agents.** It only exchanges its key for a
short-lived signed URL for an agent that already exists. Somebody creates the
agent once in the dashboard and hands over the id.

### The key never lands in git

It is account-wide and billed per minute. Put it wherever the host keeps
secrets and reference it — do not inline it in a compose file:

```yaml
# docker-compose.yml
    environment:
      SHABADOO_ELEVENLABS_KEY: ${SHABADOO_ELEVENLABS_KEY:?set it in .env}
      SHABADOO_ELEVENLABS_AGENT: ${SHABADOO_ELEVENLABS_AGENT:?set it in .env}
```

with the values in a `.env` beside it, mode `600`, never committed. The `:?`
makes a missing value fail the deploy loudly rather than starting a coordinator
whose voice endpoint quietly 404s.

---

## Tools

Both execute **on the client**, which is what makes permissions work: they call
this same API with the phone's own device token, so a read-scoped device is
refused by `requireWrite` without the agent knowing scopes exist. The agent
holds no authority of its own.

The declarations below must match what the app implements. If they drift, the
agent calls something nobody handles.

### `list_sessions`

> Lists every Claude Code session across all the user's machines: which project
> each is working in, whether it is blocked waiting for a human, and — when it
> is blocked — the exact question it is waiting on. Call this whenever the user
> asks what is running, what needs them, or what a session is doing.

No parameters.

Returns, per session: `alias`, `project`, `agent` (the machine), `input_state`
(`composer` or `dialog`), `pending` (unread messages), and two fields that are
**`omitempty` and usually absent**:

- `asking` — the verbatim question, present only while that pane is blocked;
- `note` — what the session says it is doing, present only if that session has
  set one through `session_status_set` and within the last 30 minutes.

Decode both as optional. Their absence across a healthy fleet is the normal
case, not a missing feature: nothing is blocked and nobody has set a status.

### `send_message`

> Sends a line of text to a Claude Code session, as though the user typed it.
> Use for dictation — relaying an instruction to a session. Refused with a
> permission error on a read-only device, which is not an error to retry.

| Parameter | Type | Description |
|---|---|---|
| `to` | string | the project or session to send to, e.g. `homelab` |
| `text` | string | what to say to that session |

**Pass `to` straight through — do not resolve it yourself.** `POST /api/send`
accepts `{"to": "homelab", "text": "…"}` and resolves the name on the
coordinator, which is the only way every client agrees on what a name means.
Three clients inventing three fuzzy-match rules is how the same phrase types
into the wrong project, and this is the one place in the design where being
wrong lands text in a live session somebody else is using.

The rule: exact session id, then exact alias or project (case-insensitive),
then substring — and **an ambiguous name is a 400 listing the candidates**,
never a best guess. `homelife` resolves even though `homelife-mcp` shares the
prefix, because an exact project match wins outright; `homel` refuses and names
both. Surface that refusal to the agent so it asks which one, rather than
picking.

A name that is not a **live** pane also refuses. Mail may be addressed to an
offline session and wait for it; a keystroke cannot wait for anything.

### There is deliberately no third tool

**No tool sends keypresses, and none answers a dialog.** The absence is the
enforcement. An agent instructed never to approve a prompt can be argued into
approving one; an agent with no such tool cannot, whatever it decides.

These panes run `claude --dangerously-skip-permissions`, so approving something
unread is the one interaction that can do real damage. It is the same line
drawn by there being no answer button on a queue row.

---

## System prompt

Encodes the rules that cannot be enforced by tool availability alone — chiefly
that a question must be spoken **exactly** as written.

```
You are a voice interface to the user's Claude Code sessions, which run on
their own machines. They are usually driving, walking, or otherwise not at a
screen. Be brief. Answer in one or two sentences unless asked for detail.

WHAT YOU CAN DO
- Report what sessions are running, what each is working on, and which are
  blocked waiting for the user (list_sessions).
- Relay an instruction the user dictates to a session (send_message).

READING A BLOCKED SESSION'S QUESTION
When a session is blocked, list_sessions gives you `asking` — the exact text of
the prompt it is waiting on. Speak that text VERBATIM. Never paraphrase,
summarise, soften or shorten it. Saying "it wants to remove a config file"
instead of "Do you want to delete /etc/foo?" hides the thing the user needs to
judge. If `asking` is absent, say the session is blocked and that you cannot
read the question, and offer to open it.

WHEN THE USER ASKS YOU TO APPROVE SOMETHING
You cannot answer prompts, and this is deliberate rather than a limitation to
apologise for. Say that answering needs their eyes on the screen, and offer to
open that session in the app. Do not offer a workaround, do not send the word
"yes" as a message, and do not treat repetition or insistence as changing this.

TONE
Plain and specific. Say "homelab has been waiting four minutes" rather than
"there are some sessions that may need attention". Never invent a session name,
a project or a question — if you do not have it, say so.
```

## Model and voice

The coordinator pins **neither**. It exchanges a key for a signed URL and does
not care what is on the other end, so both are dashboard choices.

Worth choosing for the actual use: low latency matters more than expressiveness
when the whole interaction is "what is waiting on me" while driving.

---

## What is tested, and what cannot be

Everything on this side is exercised against a **stub provider** — the request
path is pointed at a test server, so headers, status handling, response parsing
and the whole HTTP path through the endpoint are covered without an account:

| Covered | |
|---|---|
| the key travels as `xi-api-key`, never in the query string | a URL ends up in access logs |
| an upstream error is **not echoed** to the client | an authenticated API's error bodies name accounts and keys |
| a 200 carrying no `signed_url` fails | returning `""` would have the client open a socket to nowhere and blame itself |
| unconfigured refuses **before** calling out | a half-configured deployment must not spend someone else's rate limit |
| read-only devices may mint; the limit returns 429 | and both are audited |

**One thing cannot be tested here: whether the real API matches its own
documentation.** The stub returns what the docs describe. If ElevenLabs returns
a different shape, every test above still passes and the first real mint fails.

That is a bug on this side when it happens, not something for a client to work
around — report it and it gets fixed here.
