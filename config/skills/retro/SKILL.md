---
name: retro
description: Run the periodic retrospective — harvest what peer sessions actually learned, distil only what was paid for, route each finding to where it will be read, and ship it so it reaches every machine. Use when asked to "run a retro", "harvest learnings", "improve the ethos", "what should we do better", or on the scheduled cadence. Not for reviewing one piece of work; this is fleet-wide and evidence-driven.
version: 1.0.0
---

# retro

Tenet 6 — *always look to improve* — as a procedure. Sessions accumulate evidence
about how this actually works and lose all of it when they end, so improvement
has to be harvested deliberately, on a schedule, from what happened.

**The output is a shipped payload change, not a document.** A retrospective that
ends in a summary has produced nothing: the next session starts with the same
guidance as the last one. It ends when the learning is installed on the machines
that will act on it.

## The one rule that makes it worth running

**Refuse to write down a principle nobody paid for.**

A plausible-sounding one costs more than nothing. It makes the guidance longer,
which makes the parts that *were* paid for less likely to be read — so inventing
good advice actively degrades the advice you already have. Every line that
survives a retro must name the instance that bought it.

If a round produces two findings, ship two. Padding to five is the failure mode.

## 1. Harvest

**Ask the sessions; do not read their panes.** Scrollback is lossy and a
transcript does not say which moments mattered — that judgement is the thing
being collected, and only the session holds it. Tailing is for a session that
cannot answer.

`session_send` these five to the sessions with real usage this period. They map
to the tenets, which is why they are these five:

| # | Question | Tenet |
|---|---|---|
| 1 | What did you do **repeatedly** that a convention should have removed? How many times? | put it away |
| 2 | Where did you **assume** instead of asking, and what did it cost? Include guesses that turned out fine. | ask, don't assume |
| 3 | What did you **put down rather than put away** — finished but unfiled? What is still sitting there? | put it away |
| 4 | What documentation was **stale**, and did you fix it at source or annotate it? | document as you go |
| 5 | What did you **trust** that verification later contradicted? Especially something I told you. | trust and verify |

Say explicitly that **"nothing here" is a real answer**, and that live work comes
first. Both matter: without the first you get filler, and without the second a
retro interrupts the work it exists to improve.

**The asking does work, not just collection — and that biases the answers.** Two
sessions independently reported that answering question 3 *caused* them to file
what they found, so the replies described a tidier state than existed an hour
earlier. One said so explicitly and flagged which item was still open. Neither
outcome is a problem, but they are different things and must not be confused:
the harvest is worth less than it looks, the round is worth more. Ask "what is
open **right now**" so the tense is unambiguous, expect the answer to be stale
by the time you read it, and count the tidying as output rather than noise.

Ask **peers on other machines too**. A change to anything platform-specific is
only visible from a node that is not yours, and the lagging node and the
different OS are features of the reviewer rather than obstacles.

## 2. Distil

Look for the **pattern across sessions**, not the individual complaints. One
session hitting something is an incident; three hitting the same shape is a
convention waiting to be written, and it is usually the shape rather than the
symptom that generalises.

Two failure modes to resist, both of which feel like doing the job well:

- **Writing the symptom.** "Check the composer for U+00A0" is worth one bug.
  "Pin the distinction, not an example of one side" is worth every parser since.
- **Adding a rule when the existing one was simply walked past.** The most
  useful thing said in one round was an argument *against* adding anything:
  every confident claim that week which turned out wrong was already covered by
  a rule in the payload — *"a fixture cannot tell you it is the wrong fixture; a
  pair can"* — and the session had quoted it in a commit message the same day it
  walked past it. When a finding is an instance of a rule that exists, the work
  is to make that rule harder to skip (a test, a checklist item, a default),
  never to restate it in new words.

- **Writing a rule for a thing that already has one.** Search the payload before
  adding. A second phrasing of an existing rule does not reinforce it; it splits
  it, and the two drift.

## 3. Route

Each finding goes where it will be **read**, per *Where a learning goes*:

| True on | Goes to | Reaches |
|---|---|---|
| any machine | payload `config/CLAUDE.md` or `config/skills/<name>/SKILL.md` | every session, after upgrade |
| this machine | `~/.claude/CLAUDE.local.md` | here only; never vendored |
| one estate | that repo's docs | anyone in it |

**Write it where it is read.** A launch trap belongs in the launcher skill so
the next agent reads it *before* launching, not in a retro nobody opens.

**Mirror the payload into the live overlay in the same act.** `config.local/`
wins over `config/` at install time, so a payload-only fix is masked on any
machine whose overlay carries its own copy — the change ships, installs, and
does nothing, which is worse than not shipping it. Diff the two afterwards.

## 4. Ship

The step that is skipped, and skipping it wastes the whole round:

```
make vendor-check && go test ./... && git commit
make release TAG=vX.Y.Z          # hub first
shabadoo upgrade --all           # then nodes; the payload installs at node start
```

**A session cannot see guidance added after it started** only for its MCP tool
surface; `CLAUDE.md` is re-read, so a payload change reaches running sessions at
their next context load. Say so when reporting, because peers ask.

## 5. Report and record

Tell the sessions that contributed what came of it — specifically. A session
whose finding was declined should hear *why*, or the next round gets less. This
is the feedback loop whose absence is the documented reason `session_status_set`
is set by nobody.

Then record the round in the project's `MISSION.md` `Log`, one line, with the
count that matters: **findings shipped, not findings received.**

## Cadence

**Weekly, or after a period of heavy use.** Not continuous — a retro that runs
with nothing new to harvest produces filler, and a habit that mostly produces
filler gets skipped when it finally has something. Not quarterly either: the
evidence lives in session context, and sessions do not last a quarter.

A round that finds nothing is a **successful** round. Record it as one line and
stop; padding it is the one way to make the next one worthless.
