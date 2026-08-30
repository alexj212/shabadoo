---
name: mission-and-wrapup
description: How to write a project's MISSION.md and how to end an interaction — the Waiting-on owner format, the risk-and-cost rule for an ask, the wrap-up tables grouped by who is blocked, and the continue-or-hold prompt. Load when writing or fixing a MISSION.md, when asked for `status` / `wrap` / `brief`, when finishing a piece of work with open items, or when deciding whether something is a table row or a decision. The trigger words and the three-line answer are in CLAUDE.md; the formats are here.
---

# MISSION.md, and how an interaction ends

`CLAUDE.md` carries the triggers — that `status` is answered in three parts, that
an interaction with open items ends by asking. This carries the **formats**,
which are looked up while writing rather than needed unprompted.

`shabadoo mission` prints what the fleet actually reads from a `MISSION.md`,
including rows the six-row cap discarded; `shabadoo mission init` scaffolds one
and deliberately states nothing on the project's behalf. `shaba todo` is the same
grouping across the whole fleet.

### A project says what it is doing: `MISSION.md`

Every project folder has a `CLAUDE.md` saying what it *is*. What was missing is
what it is *doing* — and sessions were already inventing one, separately, in
different shapes. Two independently chose a file over the task tools and said
why:

> *"I picked a file because the file is durable, reviewable by the human in the
> diff, survives session death, lives next to the code it describes, and is part
> of the deliverable."*

So this is not a new habit. It is the one that already exists, given a shape
other things can read.

**`MISSION.md` at the project root:**

```markdown
# One line: what this project is for.
status: active | blocked | paused | done
updated: 2026-08-29

## Now
What is being worked on. One or two lines, present tense.

## Waiting on
- you: end-to-end smoke test — untested integrated; ~30s of recording
- mac: darwin capture set — can't verify a platform I don't run
- nobody: background room audio (hard)

## Log
- 2026-08-29 shipped the paging dialect; the iOS client is unblocked
- 2026-08-28 nudges were dead for ten hours — fixed and instrumented
```

**Write it as the work happens, not afterwards.** A log reconstructed at the end
is a summary; one written as things land is a record, and the difference shows
the first time somebody asks why a decision was made.

**`Log` is append-only, dated, and one line per thing that mattered.** It is not
a commit log — git already has that, in more detail than anyone wants. It is the
decisions and the outcomes: what changed, what broke, what was learned. If a line
would be obvious from `git log`, leave it out.

**`Waiting on` is the field with the most value and the shortest life.** Fill it
the moment you are stuck and empty it the moment you are not, because a stale
blocker is worse than none — it sends somebody to help with a thing that is
already done.

**Every line names an owner**, which is the whole reason it beats the `Blocked
on` it replaces: `you` (the human), a session name, or `nobody`. A blocker
without an owner is a complaint, and a peer reading it cannot tell whether they
are the answer.

**`status` is for a reader who is not you.** `paused` and `blocked` are
different: paused is a choice, blocked is a wait. `done` does not mean the repo
is finished, it means this mission is — start a new one rather than reopening.

### End an interaction with a wrap-up

The file above goes stale the moment updating it depends on somebody
remembering. So the update is not a chore performed afterwards — it IS how a
working interaction ends, and the same content is what you print:

```
📋 Open issues

🔴 Waiting on you — 3
┌─────┬───────────────────────┬──────────────────────────────────────────────┐
│     │ Item                  │ Risk if not · cost to do                     │
├─────┼───────────────────────┼──────────────────────────────────────────────┤
│ 🔴  │ End-to-end smoke test │ Wiring is unit-tested, not integrated ·       │
│     │                       │ ~30s of recording, when nothing's playing     │
└─────┴───────────────────────┴──────────────────────────────────────────────┘

🔵 Waiting on mac — 2
┌─────┬────────────────────────┬──────────────────────────────────────────────┐
│ 🔵  │ Re-verify degraded     │ I changed shipped text on a platform I can't  │
│     │                        │ run                                          │
└─────┴────────────────────────┴──────────────────────────────────────────────┘

⚪ Open, nobody blocked — 3
Background room audio (hard) · native Linux capture · honest track name
```

**Group by who is blocked, never by topic.** This is the design, and everything
else is formatting. A list sorted by subject makes every reader scan all of it
to find their own items; grouped by blocker, the human reads the first table and
stops. The counts are in the headers so the size is known before the reading.

**An ask states the risk of not doing it and the cost of doing it.** That is
what the third column is for, and it is not optional on a 🔴 row. "Run the smoke
test" is a nag. "Wiring is unit-tested, not integrated · ~30s of recording, when
nothing's playing" is a decision someone can make in one read — and if the cost
turns out to be high and the risk low, they were right to skip it. Without both
halves you have handed back the analysis you were meant to do.

**A quantity invites a threshold judgment; a description invites a decision.**
Reported from a row that had read *"5.2 GB with no expiry anyone chose"* for two
days without being acted on. Rewritten as *"recordings of you and your home"*, it
was acted on. A number asks the reader to decide whether it is big enough, which
is a question they can defer forever; naming what the thing actually is does not
offer that exit. Where you want somebody to act rather than assess, describe.

**The unblocked group collapses to prose, deliberately.** It needs no action, so
it gets no table. Rendering everything at equal weight is how the rows that
matter stop being noticed.

**Keep it brief.** Three tables is a wrap-up; ten is a report nobody finishes.
If a group runs long, the items are too small — merge them, or they belong in
`Log` rather than here.

**One file, one writer, when a project spans machines — and say so in the file.**
The path rule makes a project checked out on two hosts *one* project with *one*
`MISSION.md` per host, and nothing said who writes it. Two sessions editing on a
five-second report cycle collided twice before agreeing one by hand.

So the header carries it:

```markdown
owner: minutes-mac
```

The other session sends its rows across rather than editing. **Visible rather
than enforced**, deliberately: those collisions were caught quickly and cheaply,
and what was missing was the file saying who writes it — a lock would be real
machinery for a rare event, and one nobody can clear is worse than the collision.

**Absent means nobody declared, not "this node owns it".** On a single-machine
project that is normal and the dashboard says nothing. On one that spans
machines, the dashboard flags it — and flags two checkouts naming *different*
owners more loudly, because disagreeing about the writer is worse than having
none.

**Write the entries as they arise, not at the end.** A wrap-up reconstructed
from memory is a summary, and it quietly omits whatever you have stopped
noticing. The same reason `Log` is written as things land.

### One word asks for it: `status`

A wrap-up nobody can request is a wrap-up that only happens when a session feels
like it. So one word triggers it, in any session, at any time:

```
status
```

The session answers with exactly three things, in this order, and nothing else —
no preamble, no restating the question, no offer to continue:

1. **The mission line.** Headline and what `Now` says, on one line. A reader who
   stops here should still know what this project is doing.
2. **The inbox.** Unread count and who from. **This line is never omitted** —
   `inbox: clear` is a fact, silence is not, and the difference is the same one
   this file makes everywhere else. If the coordinator cannot be reached, say
   *that*; an unreachable inbox is not an empty one.
3. **The wrap-up tables**, grouped by owner as above.

**`status` is a read and must stay one.** It never drains mail, never marks
anything, never starts work. The moment asking has a side effect people stop
asking, and the value here is entirely in it being free — a question you can put
to a session mid-task without costing it anything.

`wrap` and `brief` mean the same thing; a word you have to remember exactly is a
word you will stop using. **`shaba blockers` is the same question asked of the
whole fleet**, and `shaba who` adds what each project says it is for.

### When work is done and something is still open, ask

A wrap-up reports. It does not ask, and that is the gap: a session finishes what
it was told to do, prints its tables, and stops — leaving a row that has stood
for three days sitting in the transcript with nobody deciding anything about it.
The person reads it, has no question in front of them, and moves on. The row
stands for a fourth day.

So **when an interaction ends with open items owned by the human, put the choice
to them explicitly**, in one line, after the tables:

```
▶ 3 open for you. Continue, or hold — and if hold, which one is blocking?
```

The two words are not decoration; they are the two real answers and they mean
different things:

- **Continue** — the open rows are known and are not stopping the next piece of
  work. Say so and carry on. This is the common answer and it must stay cheap to
  give, or the prompt becomes a toll on every interaction.
- **Hold** — one of those rows blocks what comes next. The answer names *which*,
  and that row is then promoted into the mission's `Waiting on` with the risk and
  the cost written beside it, so the next session inherits a blocker rather than
  a memory.

**Three rules keep this from becoming noise**, and the failure it prevents is the
one this file names everywhere else — a prompt that mostly cries wolf gets
skimmed, which costs the one that mattered:

- **Ask only about rows owned by the human.** Peer-owned and `nobody` rows need
  no decision from the person reading; listing them here turns a question into a
  recital. They are already in the tables above.
- **Ask once, at the end, never mid-task.** An interaction that stops to ask
  about its own backlog before it has produced anything has inverted the point.
- **No open rows, no prompt.** Print nothing. A question with an empty subject
  teaches the reader that the line is ritual, and then they stop reading it on
  the day it has three things under it.

**Do not answer it yourself.** Continuing because continuing seemed likely is the
assumption this exists to stop — the whole value is that the decision is made by
whoever holds the information, and a session that pre-empts it has converted an
ask into narration.

### Why a file rather than a status call

`session_status_set` exists and is set by nearly nobody, and six sessions
independently gave the same reason: there was no feedback loop, so nobody built
a model that anyone reads it. A file avoids that by being read where it already
lives — in the repo, in the diff, by the next session that opens the folder,
whether or not anything else ever consumes it.

The two are complementary rather than competing: **the status call is what you
are doing for the next thirty minutes, `MISSION.md` is what this project is
doing this week.** One expires; the other is committed.
