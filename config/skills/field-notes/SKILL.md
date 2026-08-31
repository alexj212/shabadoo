---
name: field-notes
description: The evidence behind the core tenets — what actually went wrong on this fleet and what it cost. Load when writing a check or a test and you want the failure modes somebody already paid for, when running a retro, when a verification looks suspicious, or when you want to know WHY a rule in CLAUDE.md exists rather than just what it says. Not needed for ordinary work; the tenets carry the rules, this carries the receipts.
---

# Field notes

The core tenets in `CLAUDE.md` are the rules. These are the **receipts** — the
specific failures that bought each one, with what they cost.

They live here rather than in `CLAUDE.md` for the reason the tenets themselves
give: a finding is only worth the place it is read, and 255 lines of war stories
in a file every session loads at startup is a tax on every session to serve the
few that need the detail. **The rule fires unprompted; the evidence is looked
up.**

Nothing here is a principle nobody paid for. If you are adding to this file and
cannot name the instance, it does not go in.

Harvested from sessions across the fleet, each naming what bought it. They are
here rather than in a retrospective document because a finding is only worth the
place it is read. Nothing is listed that somebody did not pay for.

**A success report is not evidence of effect.** Four sessions, independently, in
one period: a firewall API returned a fully-populated object with an `_id` and
provisioned nothing; a PUT accepted a filter and silently reverted it on
read-back; every CoreAudio call returned `noErr` while zero packets arrived; an
HTTP client returned 200 and then delivered nothing; a line reader dropped the
empty lines that terminate SSE frames, so nine frames arrived and no events
fired, with no error anywhere. A fifth arrived within an hour of this being
written, and it is the sharpest: a preflight check reported `mic ok` for a
microphone macOS had denied — the device opened, every call returned success, and
it handed over zeros. **The gate built to catch this class was itself the thing
that lied.** What fixed it was not restating the rule but a check that could go
red: a *categorical* test (is the signal constant?) rather than a threshold,
mutation-tested both ways — a zero-check passes a device pinned at a non-zero DC
offset, and an always-true check passes a synthetic noise floor.

**Verify the effect, not the call.** The call is
what the system tells you about itself, and it is the one thing that cannot fail
to be reassuring.

**Two sources agreeing is not corroboration when both read the same wrong
field.** Two audio properties both reported 48 kHz while audio arrived at 44.1;
two verifications of a DNS block both failed for unrelated vantage-point
reasons. Corroboration requires an *independent* oracle — a different mechanism,
not a second reader of the same one.

**The artifact under test may be older than its source.** Four times in two days
on one machine, once nearly filing a bug against another session's code for a
local stale build; elsewhere a compile failed, the previous binary in `/tmp` ran
instead, and printed `all passed`. **Anything that reports a version should be
able to say it is older than the source it was built from** — nearly free
wherever a build stamp already exists, and it converts the most confusing class
of bug into a line of output.

**Committed, pushed, released and installed are four states.** Reported as
"released" twice in one session while the work sat on one laptop; a peer found
it by fetching. Use the word for the state that is actually true.

**And this is about ARTIFACTS, not about releases** — filing it as a
release-reporting rule is what let it miss. A session held the rule "the privacy
policy is amended in the same commit as any change to the read surface", honoured
it every single time, and it bought nothing: the file it names lived in their
repo while the document anybody actually reads is served from another one.
Committed and published, again, in a legal document's clothes. In their words:
*"the version of that lesson I had already written down was about releases, so I
did not recognise it wearing a legal document's clothes."*

**An invariant whose two halves live in different repositories is not an
invariant, it is a habit** — and it reports success by being followed. If a rule
says two things must move together, something has to be able to see both of them
at once. Nothing did.

**A number you did not measure must not be stated in the grammar of one.** "It
costs one prompt" was a prediction wearing the clothes of a measurement; it was
quoted onward as the reason a change was urgent, and measured later at zero.
Elsewhere: a causal story invented for four empty files because it sounded
right, and a defect asserted from a plausible mechanism without checking the
function that already handled it. Say "I expect" when you have not measured. It
travels very differently between sessions, and between sessions is where the
confidence gets stripped.

**A verification must be able to fail.** The strongest finding of the round —
one session counted **six** instances of it in itself. The cleanest specimen is a
webhook update "verified" by asserting the value was 79 characters — both the old
and new URLs were 79, so the check could not have distinguished them, and the
update had silently no-op'd. The **most expensive** one that session reported is
the shape people actually hit: three deploys reported success while a dead
webhook stayed live, because Alertmanager does not watch its config file.
**Config written is not config loaded**, and the check that could not fail sat
downstream of a fix that had never applied. Alongside it: a guard that could not tell a staging
certificate from a production one, a config written but never reloaded, an
upsert comparing content while ignoring two fields, a test that went on passing
when the bug it covered was reinstated, and — while writing this section — a
scripted check whose patch silently failed to apply, reported by the test runner
as `ok (cached)`. **Before trusting a check, name the input that would make it
red. If you cannot, it is decoration.** That one question would have caught five
of the six.

**A check wired to your own activity cannot see what happens when you are not
there.** The third variant, and the one nobody looks for. A version-pin checker
ran `on: push` only — so it could report drift **only while somebody was already
editing the repository**, which is precisely when drift does not accumulate. It
went green on one release and was structurally blind to the five after it. Its
author's words: *"a check wired to my own activity cannot see what happens when I
am not there."* Ask what has to happen for a check to RUN, not only what makes
it red.

**A check that has never gone GREEN is as broken as one that has never gone
red.** The other half of the rule above, and the one nobody thinks to ask: name
the input that would make it *pass*, and actually run it. A checker that had only
ever failed was read as rigour, and carried three defects at once — written an
hour earlier for the express purpose of catching this class:

- It banned a word the page was **required** to carry, in the past tense, so a
  reader of the old policy is told what they read is no longer true. Its author
  specified that sentence and then wrote a check forbidding it. **Assert the
  claim a document has to make, never the presence of a word.**
- It had no assertion at all about the one disclosure that was actually wrong, so
  it was red for a reason that was not real while blind to one that was — and
  *the false alarm is what hid the real gap*. Fixing only the first defect would
  have turned it fully green on a wrong page.
- It matched against **wrapped** text. A phrase present on the page, broken
  across a line, was reported absent — a silent pass. Roughly half its assertions
  were passing by luck about where a line happened to break. Flatten whitespace
  before matching prose.

Fixture it in **both directions**: the known-bad input must go red, and a correct
one — with deliberately awkward formatting — must go green. The green fixture is
the one that gets skipped, and it is the one that catches a check which cannot
pass.

**And confirm the check ran, because "did not run" wears good disguises.** Three
in one evening, each read as a pass:

| Disguise | What it actually was |
|---|---|
| `ok (cached)` | a scripted patch whose anchor had gone stale, so nothing was injected |
| a silent `diff … && echo OK` | the diff always failed, so the echo never fired |
| a narrow `grep` on test output | it filtered out `FAIL [build failed]`, and the next command's `ok` was read as the verdict |

The third is the worst because the filter was mine and it was hiding exactly the
line that mattered. So: **assert the injection is present before believing the
result**, run with `-count=1`, and never pipe a verdict through a pattern narrow
enough to drop it. A grep written to find the failure you expect will not show
you the failure you did not.

**Absence of an error is not evidence of success — and a negative check has a
time horizon.** Four emails were reported as "no bounce, delivery verified clean
at our end". The MX was `0 localhost.`, so nothing could ever accept them; the
delivery-failure notices arrived about a day later, from a different mailbox,
and were found by a third session entirely.

The correction matters more than the original claim. That domain does not
*discard* mail, it fails to connect — so the check was not wrong forever, it was
**taken too early**: "no bounce" was true when it was made and false by
morning. A negative result is only as strong as the time you waited for the
failure to arrive, and asynchronous failures arrive on their own schedule.

**Which means the loud failure was luck.** A domain parked with a genuinely
discarding MX accepts and drops, no notice is ever generated, and silence reads
as delivery permanently. That version has already been paid for on this fleet
once. So: before reading quiet as good, ask what a failure would look like,
*and* how long it would take to show up — and if the answer is longer than you
are willing to wait, say the check is pending rather than passed.

**Recorded doubt is not discharged doubt.** The same session had written, in its
own notes and in its own words, that a transcribed domain was *"worth confirming
before sending"* — and then sent to it four times across three prompts, ignoring
an explicit warning from a peer, before one `dig MX` settled it in a second.
Writing the doubt down felt like handling it. **If you write that something
might be wrong, you have just written yourself a blocking check** — act on it
before the next irreversible step, or delete it.

**Often the answer is not the human — it is one command away.** Four assumptions
in one session, every one mechanically answerable: package pins invented rather
than looked up, a config value guessed while the image shipped its own one
`--entrypoint cat` away, a gid inferred from a uid that `id` would have printed,
and SELinux blamed with no AVC denial present (it was a `0700` directory the
session had created itself). *When in doubt, ask* has a cheaper sibling that
comes first: **read the source of truth instead of predicting it**, and reserve
the human for what only they hold.

**A banner is a bet that every future reader reads top-down.** Four sessions
annotated a stale document rather than correcting it, and each said the same
thing afterwards: annotating felt safer and was faster. It produced files where
a reader must get past a correct banner to reach three hundred wrong lines. The
counter-example from the same period: a peer deleted a resolved issue outright
rather than ticking it, and replaced a 223-line dead design doc with a 59-line
tombstone that *points at* the live one instead of restating it. Keep a
tombstone only where its job is stopping re-adoption; everywhere else, correct
the fact and delete the history.

**A peer's "I have not chased it" is not evidence there is nothing there.** One
such dismissal, chased anyway, turned up an unpinned `:latest` image running
three different versions across a cluster. Unexamined is not clean — the same
distinction as empty versus unknown, applied to somebody else's report.

**`systemctl is-active` cannot tell "stopped" from "does not exist".** Both
print `inactive`. A peer read that as "the hub is down", told its operator the
dashboard was broken, and was probing an address the architecture had moved past
a month earlier — the service had not stopped, it had been *deleted*, and the
work had moved to another host. `is-enabled` distinguishes them: a missing unit
fails with `No such file or directory`, a stopped one answers `enabled` or
`disabled`. Verified on this machine, both units, side by side.

It is the same shape as everything else in this section, in a command everybody
uses without thinking: **absent and idle rendered identically**, and the reading
that comes naturally is the wrong one — you go looking for why a thing broke
instead of learning it was never there.

**Check your own register before reporting a finding as new.** A drift reported
to a peer as fresh had sat in the reporting project's own issue file for three
days with the same root cause. The cost is a peer's attention, which is the
resource this arrangement spends most freely.

**Twice is a script — and look for the one that already exists.** Nine hand-edits
across four version fields that had already drifted apart under a comment
claiming they were kept in step; a compiler file list reassembled six times. A
repeated multi-file edit belongs in a script the moment it is done the second
time. The inverse is just as common and more embarrassing: one session
hand-wrote an ad-hoc link checker **six times** while the repo shipped
`bin/linkcheck.py` and other agents in the same repo were calling it. Search
before you improvise.

**A source read is a hypothesis; the running state is the evidence.** Three
times in one session, configuration and live state disagreed and the
configuration was believed: a Rails source read said registration was ungated
and one empirical POST showed it gated all along; a QA database called a domain
dead for four years while production had traded on it in June; a leak was
attributed to the server being closed down and the affected repositories turned
out to live on the other one. Reading the source tells you what should happen.
**The cheap falsifying check — one POST, one production query, one `ls` — is
what should gate filing a finding**, and it is nearly always cheaper than the
read that produced the hypothesis.

**A claim loses its hedge when it crosses a session boundary.** Two sessions,
same shape: a peer's *prediction* ("that rename will cost one prompt") was
relayed onward as the measured reason a change was urgent — it cost zero — and a
human's instruction ("use the .pem") was relayed to a third session with an
unchecked premise attached, which would have meant re-downloading a leaked
private key from a public repository. Both were caught by the recipient, neither
by the relay.

The relaying session's own correction is the useful half, and it is not about
hedging harder: **a hedge is invisible when everything around it is evidence.**
That prediction arrived inside a message otherwise full of real measurements and
was not sorted from them — camouflage rather than carelessness. So the burden
sits at both ends. Sending: mark an unmeasured claim in the line itself, because
its neighbours will vouch for it otherwise. Receiving: when a number is about to
become the reason for something, ask *did you measure that or predict it?* It is
the question nobody thinks to put to a figure that arrived in good company.

**A document derived from stale documents inherits their staleness, and
launders it.** A mission charter written from docs that had not been re-verified
listed a task that had been true for months and pointed at a hypothesis that was
wrong — and it now read as a fresh, authoritative artifact rather than as a
copy. Writing a plan, a charter or a brief IS writing documentation, and it
carries the same obligation to verify before recording.

**Record the rule, not the instance.** A machine's `claude` binary was found
off-PATH and the path was written down; the *rule* — on this machine assume a
tool is off-PATH before assuming it is absent — was not, so Go was hunted for
from scratch an hour later. The instance helps once. The shape helps every time.

**The residue of proving something is what goes homeless.** The sharpest finding
of the round, and it was self-reported: *"I put things away well when they were
the deliverable and badly when they were the residue of proving it."* Four
sessions had unfiled work at that moment — a platform reader and its tests
living only in a session-scoped scratchpad that dies with the session, applied
dotfile fixes recorded nowhere, a design rationale published at a URL linked
from nothing, and a security decision that existed solely in one session's
context. All four were *finished*. None were put away. **Deliverables get filed
because somebody is waiting for them; the evidence, the workings and the
decisions-not-to-act have no such pressure, and they are what the next person
needs.**


---


## Two clocks are better than one, and neither is the truth alone

A recurring shape, found independently in three different layers before anyone
named it. When you need to know *where something is* in a sequence, one source
is never enough:

- **One source is precise but says nothing about the world.** A device's sample
  counter has no jitter and tracks perfectly — and counts only from whenever
  that particular stream started, so it cannot relate itself to anything else.
- **The other is shared but noisy.** A wall clock is the only thing two
  independent streams have in common, and it carries about a millisecond of
  jitter, so placing *every* item by it accumulates error in one direction.

The arrangement that works is to use each for what it is good at: **the shared
one places the beginning, the precise one carries everything after, and the two
disagreeing is itself the signal** that something glitched — which is worth
knowing *before* anything is built on top of it.

The same structure, three times:

| Domain | Shared, noisy | Precise, local | What disagreement means |
|---|---|---|---|
| two audio tracks | wall clock at stream start | device sample position | the stream dropped packets |
| a pushed event stream | server timestamp per frame | frame sequence number | a frame was missed; resync |
| a client's view of a server | a slow poll | the live stream | the stream is buffered or dead |

The third is the one people get wrong, by treating a stream as a *replacement*
for polling rather than a complement. Keep the poll: the stream is the
low-jitter counter, the poll is the wall clock that says where the truth
actually is. A stream alone cannot tell you it has stopped.

**The rule underneath all three: do not throw ordering away at the boundary.**
A timestamp discarded where data is captured is unrecoverable everywhere
downstream, and the loss is invisible until something has to be aligned — at
which point the artifact exists and the information needed to fix it does not.

## An artifact handed to a reviewer is submitted, not filed

Tests verify that code does what its author meant. They cannot catch the case
where **what the author meant was wrong**, and that case needs a reader looking
at the *output* rather than the code.

Measured, not asserted: on one project, four of thirty commits in a day came
from a peer session reading delivered artifacts — and they were the four most
serious defects in it, including fabricated content presented as real and a
suppression that fired correctly by a margin of 0.00015. A suite of 141
mutation-checked tests caught none of them.

So when work is handed to another session, both sides have a job:

- **The sender** makes the artifact self-contained. A durable message that names
  a path has a payload nothing preserves — inline what a reader needs, because
  the queue outlives the file.
- **The reviewer measures rather than accepts.** Recompute the number, re-run
  the tool independently, and say plainly when a reported figure is unverified
  rather than repeating it.

The same argument applies across machines: a change to platform-specific code is
verified on a *second* node, because the developing machine usually cannot
produce the failing condition.


---

# The behavioural guidelines, in full

`CLAUDE.md` carries the four rules. This carries the worked instances —
including the one that shows why a pair beats a fixture.

Behavioral guidelines to reduce common LLM coding mistakes. Merge with project-specific instructions as needed.

*Rationale (Andrej Karpathy's observations on LLM coding pitfalls): models make silent wrong assumptions instead of seeking clarification, overcomplicate code and bloat abstractions, and change/remove code or comments they don't fully understand. The four principles below directly counter each.*

**Tradeoff:** These guidelines bias toward caution over speed. For trivial tasks, use judgment.

### 1. Think Before Coding

Don't assume. Don't hide confusion. Surface tradeoffs.

Before implementing:
- State your assumptions explicitly. If uncertain, ask.
- If multiple interpretations exist, present them — don't pick silently.
- If a simpler approach exists, say so. Push back when warranted.
- If something is unclear, stop. Name what's confusing. Ask.

### 2. Simplicity First

Minimum code that solves the problem. Nothing speculative.
- No features beyond what was asked.
- No abstractions for single-use code.
- No "flexibility" or "configurability" that wasn't requested.
- No error handling for impossible scenarios.
- If you write 200 lines and it could be 50, rewrite it.

Ask yourself: "Would a senior engineer say this is overcomplicated?" If yes, simplify.

### 3. Surgical Changes

Touch only what you must. Clean up only your own mess.

When editing existing code:
- Don't "improve" adjacent code, comments, or formatting.
- Don't refactor things that aren't broken.
- Don't modify code or comments you don't fully understand — leave them, or ask.
- Match existing style, even if you'd do it differently.
- If you notice unrelated dead code, mention it — don't delete it.

When your changes create orphans:
- Remove imports/variables/functions that YOUR changes made unused.
- Don't remove pre-existing dead code unless asked.

The test: Every changed line should trace directly to the user's request.

### 4. Goal-Driven Execution

Define success criteria. Loop until verified.

Transform tasks into verifiable goals:
- "Add validation" → "Write tests for invalid inputs, then make them pass"
- "Fix the bug" → "Write a test that reproduces it, then make it pass"
- "Refactor X" → "Ensure tests pass before and after"

For multi-step tasks, state a brief plan:

```
1. [Step] → verify: [check]
2. [Step] → verify: [check]
3. [Step] → verify: [check]
```

Strong success criteria let you loop independently. Weak criteria ("make it work") require constant clarification.

**Pin the distinction, not an example of one side.** A test that asserts "this
input produces this output" passes happily when the code has stopped being able
to tell any inputs apart. Assert that two cases which *must* differ actually do.

Worked instance: a check answering "is anyone typing in this pane" was pinned
against fixtures of an idle pane and a busy one. On a second machine the UI drew
that row completely differently, so BOTH parsed as "cannot tell" — the check had
gone blind, every fixture still passed, and the feature silently stopped working
on that platform for a day. The test that catches it asserts idle and busy must
produce *different* answers, per rendering. **A fixture cannot tell you it is the
wrong fixture; a pair can.**

The same shape applies well beyond parsing — a permissions check that denies
everything, a matcher that matches nothing, a diff that reports every line
changed. Each passes any single-sided example.

**These guidelines are working if:** fewer unnecessary changes in diffs, fewer rewrites due to overcomplication, and clarifying questions come before implementation rather than after mistakes.

---

---


---

## An error code can be a behaviour, not a fault

`OSStatus 268435460` (`0x10000004`) came back from a microphone open and looked
like a device failure. Nobody on the fleet could name it, and two sessions
declined to guess — correctly, since it is not even a four-character code, so
naming it would have meant inventing which numeric space it belonged to.

It was settled by **producing it deliberately**. A capture was started and left
alone: it blocked for 3m25s, provably waiting — process elapsed time confirmed —
with the system logging `allow prompt: Allow` the whole time, and then returned
that code. So it is what an **unanswered consent wait** returns, not a fault. The
device was fine. A human had not clicked.

**Pinning a constant by observation beats decoding it**, and it is the same move
as verifying an effect rather than a call: the question is not *what does this
number mean* but *what do I have to do to produce it*. That is answerable on the
machine in front of you, in minutes, without a reference.

The cost of the alternative was live: three plausible mechanisms were offered on
this fleet in one afternoon and all three were wrong.


---

## A system accepting your configuration is not the same as it being able to honour it

The familiar version of this is *config written is not config loaded* — a file
changed and never reloaded. This is worse, because every step reported success.

A hostname redirect was written into a Pages `_redirects` file in the documented
absolute-URL form, **accepted into the file, deployed without complaint, and
silently ignored**: `_redirects` matches paths, not hosts. The old hostnames kept
answering **200 with content**. Nothing errored, nothing warned, and the config
was genuinely present and genuinely loaded — the engine simply had no capability
for the rule it had accepted.

Caught only by reading the **status code of the old host**, rather than trusting
the deploy. The right mechanism was a zone-level rule in a different phase
entirely, which the stored credential could not write — a permission error that
only appeared once somebody went looking for why the accepted config did nothing.

**Permissive parsing turns an unsupported feature into a silent no-op.** Ask what
the system would do with a rule it cannot honour: if the answer is "accept it",
then acceptance proves nothing and only the effect does.

The instance that paid for it: redirecting three legacy hostnames to a new
domain took **three mechanisms, and the first two failed in opposite ways.** A
Cloudflare Pages `_redirects` file cannot match on hostname — but the
absolute-URL form is *accepted*, deploys with no complaint and a green tick, and
is then **silently ignored**, with the old hosts still answering 200 and serving
content. Caught only by reading the status code rather than the deploy result. A
zone Redirect Rule is the textbook answer and returned 403 on the
dynamic-redirect entrypoint for every stored token — which at least fails
loudly. What worked was a Pages Function (`functions/_middleware.js`): it runs on
every custom domain of the project, needs no permission anybody has to grant, and
is reviewable in a diff.

Two things worth carrying out of it. **The honest cost was written down** — the
site is no longer purely static, so a second Function has to argue for itself.
And **the check to build is one that fetches the live URL and fails if the
content is wrong, never one that verifies the configuration**: a domain move is
precisely the event that recreates *correct in the repo, wrong where anybody
reads it*. Verified here by following the redirect to a 200 on the real policy,
not by reading the rule.

**A scan finds only what names itself, and a clean result is not a clean file.**
A session preserving two meeting transcripts grepped both for
`password|secret|token|credential`. One came back with zero hits and was nearly
reported clean. The scan could not have caught the dirty one either: the root
password in it was **spoken aloud as "welcome 01" with the character
substitutions described in words**, and the file's only keyword hit was an
unrelated garbled line nine seconds earlier. It sat untracked and un-gitignored
in a repo twenty commits ahead of a work remote — one `git add -A` from a push.

The shape generalises past transcripts to every search for unlabelled content:
secrets in prose, personal data in logs, a licence buried in a vendored tree. **A
keyword scan tests whether the thing announces itself**, and the reason it is
sensitive is usually that it does not. So do not convert a clean scan into a
clean verdict; the honest report is what that session eventually wrote into the
surviving file's README — *I do not know whether this is clean* — which is worth
more than a green tick a reader would have trusted.

Two corollaries, both cheap:

- **Where a machine cannot check, say so rather than defaulting to safe.** This
  is the same distinction as [empty versus unknown], applied to a scanner.
- **Destroying your copy is cleanup, not remediation.** A credential spoken on a
  call exists on the far end whatever you delete; the rotation still has to
  happen, and only a person can start it.

**A pipeline reports the LAST stage's exit status, so `if cmd | tail` tests the
wrong process.** Twice in one session, in the two places it does most damage:
`go build ... | head -5 && echo BUILD_OK` printed BUILD_OK for a build that could
not have failed the check either way, and `if go test ... | tail -6` printed
*passed* while `--- FAIL:` was visible three lines above it in the same output.
Both were **teeth-checks** — the ritual that exists to prove a check can fail —
so the failure was a verification of a verification quietly reporting on
`head(1)`.

It is invisible for the reason the rest of this file keeps naming: the happy path
and the broken path print the same word. Read the tool's own output, or capture
the status before anything else touches it:

    go test ./... ; rc=$?        # not: go test ./... | tail
    set -o pipefail              # or make the pipeline carry the real status

The general form is worth more than the shell tip: **when you check a check, say
which process's answer you are reading.** An exit status that passed through a
pipe is a fact about the pipe.

**Two checks of the same fact, written with different clients, are not the same
test — and the disagreement stays invisible until the world changes.** Two
scripts verified the same policy URL. One used Python's `urllib`, which follows
redirects by default; the other used `curl` without `-L`, which does not. They
agreed for as long as no URL in play had ever redirected. The day the legacy host
started 301ing, the second fetched an empty body and reported the URL
**unreachable** while the host was answering perfectly.

Nothing revealed it earlier because nothing could: the divergence was in the
DEFAULTS of two libraries, not in either script's logic, and both had passed
every day of their lives. So when two checks assert the same fact, ask what they
would do differently under a condition neither has met — a redirect, an empty
body, a slow response, a 5xx. **Agreement so far is not agreement; it may only be
a world that has not yet asked them to differ.**

The sharper half is what happened during the audit for it. The auditor grepped
their own scripts for calls lacking `-L` with `grep -v -- '-L'` — **which does not
match `-fsSL`**, so it flagged two calls that do follow redirects and would have
reported "all clean" on a pattern incapable of telling the two spellings apart.
They caught it by reading the calls one at a time instead, and the real answer was
better than the grep's: the non-following calls all fail *loudly* if a redirect
appears, so nothing was blind.

**A pattern that cannot distinguish the two cases it exists to separate is not a
weaker check, it is a different check that happens to produce a verdict** — the
same family as a keyword scan over unlabelled content, met here inside the audit
written to find the first defect.
