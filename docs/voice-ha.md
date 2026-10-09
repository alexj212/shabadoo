# "Hey Shaba" — a room voice device as a shabadoo client

**Status: design, 2026-10-09. Nothing here is built.** Requested by the
operator through the homelab session. The existing phone voice path
(`docs/voice-agent.md`, `POST /api/voice/session`) is unchanged by this.

## What it is for

Saying "Hey Shaba, what is blocked?" to a voice satellite in a room and hearing
the answer. Status, what is waiting, the inbox, and handing a message to a
project, without opening the dashboard.

## The split

A home-automation hub (Home Assistant here) already owns the satellite: it
carries the audio, runs wake word, speech-to-text and text-to-speech, and
controls the house. It keeps all of that. **shabadoo takes one stage only: the
conversation stage, text in and text out.** Rebuilding the audio pipeline inside
shabadoo would buy nothing a session needs.

```
 satellite ──audio──► Home Assistant: wake word, speech-to-text
                           │  text of the request
                           ▼
                      shabadoo conversation agent (a small HA integration)
                           │  POST /api/voice/ask   {text, conversation_id, source}
                           ▼
                      coordinator: decide, act within the voice rule, answer
                           │  {speech, continue}
                           ▼
                      Home Assistant: text-to-speech ──audio──► satellite
```

## The mechanism: a custom conversation agent, on its own pipeline

Three were considered.

| Mechanism | Verdict | Why |
|---|---|---|
| **A custom Home Assistant integration that registers a conversation agent** and forwards each request to the coordinator | **Chosen** | It is the interface Home Assistant defines for exactly this stage. About a hundred lines, text in and text out, and it can carry a conversation id for a follow-up question. The coordinator's API stays shabadoo's own shape |
| An OpenAI- or Ollama-compatible endpoint on the coordinator, used through an existing integration | Rejected | The coordinator would have to imitate another product's chat API, including streaming and model listing, to deliver a sentence. That is a second protocol to keep compatible with releases this project does not control, and it frames a command router as a chat model |
| Sentence-trigger automations calling the coordinator over REST | Rejected | A catch-all sentence would swallow house commands, since sentence triggers are matched before built-in intents. Keeping it from doing so means a keyword in every request, said after the wake word that already means the same thing |

**Two wake words, two pipelines.** The satellite is given a second wake word.
"Hey Shaba" selects a pipeline whose conversation agent is shabadoo; the
existing wake word keeps selecting the existing pipeline, untouched. The split
between "house" and "sessions" is then made by which word was said, and nothing
depends on one agent declining a request so another can try it.

This is why the "prefer handling commands locally" fallback is not relied on. It
would make every shabadoo request first pass through the house's intent
matcher, where "turn on the office session" is one fuzzy match from a light
switch. The existing default pipeline is not edited at all.

**Not yet measured, and each could change the above:**

- Whether this satellite's firmware exposes a pipeline choice per wake word. If
  it offers one pipeline for both, the fallback is a single shabadoo pipeline
  that forwards anything it does not recognise to the house agent.
- Whether Home Assistant on its host can reach the coordinator's URL.
- Latency end to end. A spoken answer that takes more than a few seconds reads
  as a failure.

## The coordinator's side: `POST /api/voice/ask`

Text in, a sentence out. It is deliberately not Home Assistant's shape, so the
room satellite is one client of it and a phone or a desktop can be another
later without a second design.

```
request   {"text": "what is blocked", "conversation_id": "…", "source": "office"}
response  {"speech": "Two sessions are waiting on you: homelab and iptv.",
           "continue": false}
```

`continue: true` keeps the microphone open for a follow-up, used when a name was
ambiguous ("homelife or homelife-mcp?"). An ambiguous name is asked about,
never guessed, which is the rule every other client of this API already
follows.

**The first version matches a fixed set of requests in Go, with no language
model.** A model in the path adds seconds and a bill to answer "what is
blocked", and makes "what may voice do" a property of a prompt. A fixed set is
fast, free, testable, and its limits are the code. Anything unrecognised is
answered with what it can do.

| Said | Does | Reads from |
|---|---|---|
| "status" / "how are we doing" | counts: sessions, how many wait on a prompt, undrained mail, offline nodes | the same builder as `shaba blockers` |
| "what is blocked" / "what needs me" | names the sessions at a prompt and what each is asking | `input_state` and the dialog question |
| "inbox" / "any mail" | how many messages are waiting and for which projects. It does not read mail aloud | pending deliveries |
| "what is `<project>` doing" | that session's own status line, or that it has not said | `session_status` |
| "tell `<project>` `<message>`" | stores mail for that project | see the rule below |

Speech-to-text mangles project names ("home lab", "eye p t v"), so names are
matched after stripping spaces and punctuation, against live projects only.

## The rule for what voice may do

**A room has no identity.** Anyone in it, a guest, a television, a recording,
can say the wake word. A device token says which *device* asked. It cannot say
who was speaking, and nothing in this design can.

So voice is treated as an unauthenticated speaker holding a narrow credential:

1. **Voice never types into a pane and never answers a dialog.** No request
   maps to `send`, `keys`, `command`, `kill`, `open` or `upgrade`. This is the
   line already drawn for the phone voice agent, for the same reason: these
   panes run with permissions disabled, and the absence of the capability is
   the enforcement.
2. **Voice reads counts and names, not content.** It says that homelab has two
   messages waiting. It does not read them out to the room.
3. **"Tell `<project>`" is mail, marked as spoken.** It is stored like any
   message, attributed to `voice:<source>`, and its body opens with a fixed
   line saying it was spoken aloud by an unverified speaker. The receiving
   session decides what to do with it, as it does with any peer's mail. It is
   rate limited per source. **Whether this ships in the first version is the
   operator's decision**, because mail to a session that runs with permissions
   disabled is still a way to cause an action.
4. **A new device scope, `voice`, admits `POST /api/voice/ask` and nothing
   else.** The token Home Assistant holds cannot read a transcript, capture a
   pane or list devices even if it leaks. Enforced in the middleware, where
   `read` is, and not by the handler declining things.
5. **Every request is audited** with its source and the recognised text, since
   "who told homelab to do that" has to have an answer.

## Where the token lives

Home Assistant stores it in the integration's config entry, entered through the
integration's setup form. That puts it in Home Assistant's own storage
directory, not in a YAML file and not in a compose file's arguments. It is a
`voice`-scoped token, so its exposure is bounded by rule 4. It is revoked like
any device, with `shabadoo revoke`.

## Order of work

1. `voice` scope and `POST /api/voice/ask` with the read-only requests, tested,
   plus the entry in `docs/mobile-client.md` that a new human-plane endpoint
   requires.
2. The Home Assistant integration: one conversation agent, one setup form.
3. A second pipeline that uses it. The default pipeline is not changed.
4. **Try it by typing** into Home Assistant's Assist box with that pipeline
   selected. This needs no microphone and no speech-to-text.
5. Only then the wake word, which is the device owner's work and independent.

## Not in this design

- Controlling the house. That stays with the house's own agent on its own wake word.
- Starting a session by voice. "Start work on X" spends a machine's resources
  on an unverified speaker's word; it waits for a decision after the read-only
  version has been used.
- A language model for free-form requests. Possible later, behind the same
  endpoint and the same rule.
