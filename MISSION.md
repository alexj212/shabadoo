# The session framework itself — coordinator, per-host agents, and the payload of skills and conventions they install.
status: active
updated: 2026-08-31

## Now
Nothing in flight. The public release path works for the first time — v0.4.81 is
Latest on GitHub and on both nodes, verified by fetching it unauthenticated,
checking the published sum, running it, and installing into a throwaway HOME.
Today was peer-driven: six sessions found things, I shipped and verified them.

## Waiting on
- you: four decisions out of the 08-29 retro — ElevenLabs key, 5.5GB of home audio, two unrouted meetings, one mis-addressed send · CARRIED, NOT RE-VERIFIED since 08-30; homelab and wsl hold the detail · risk of skipping: a leaked key keeps billing and the audio keeps growing · cost: one conversation
- me: no scoped broadcast — today's fleet fan-out cost 25 hand-copied sends, not the 8 this row claimed · risk of skipping: every check-in scales with the fleet and Alex just hit it · cost: a day, and a design decision about who may address whom
- me: the board's Waiting-on extractor runs past a blank line to the next `##`, swallowing the charter paragraph into the last row · wsl diagnosed the cause FROM THE RENDERING and told four sessions to delete text the checker requires; backups later established the checker was never wrong — my extractor is the only broken half · risk: near-miss on fleet-wide scope-statement deletion, caught by dev-env reading the source · cost: stop the block at the first blank line — and observability wants mission-check.py to ASSERT its extraction matches the board's and go red on divergence, which needs my extraction callable from outside: a SHARED function, never a second parser, or the class returns as two bugs instead of one
- me: `shabadoo sessions` takes 31s (3/3 runs: 31.18/31.25/31.54) while /healthz answers in 94ms — deterministic, not load, and it gates devops' mission-check.py · POINTER from observability: deterministic to 0.4s is a fixed timeout being waited out, NOT a slow query — look for a deadline constant, not something to optimise · corroborated: `PATH=/usr/bin:/bin` hides the binary and mission-check.py drops 120s+ to 0.167s on two machines · risk: every CLI call and any script looping it · cost: unknown, not diagnosed
- me: `shaba todo` states "ages are bounded by the coordinator's uptime (2h)" while printing rows aged 41h — the two contradict, and a board that disagrees with its own caveat teaches a reader to discount both · found during the 08-31 check-in, NOT diagnosed · risk: Alex reads the fleet board today · cost: unknown until the age source is traced
- me: `shabadoo mission` serves the PARENT card for a mission nested under another project's root — reproduced on v0.4.81 in devops/missions/software, which has a valid card of its own · the card-served-from-parent defect I fixed in the coordinator path and missed in the CLI · risk: I told sessions to self-check with this command and it answers confidently and wrongly · cost: small, `missionFor` already exists

## Log
- 2026-08-31 FIVE payload findings are QUEUED AND UNWRITTEN because payload edits are
  work and the check-in froze them. Recorded here so a pause does not lose them:
  (1) `--author` is not a session discriminator on a fleet sharing one git identity —
      dns filtered `git log --author="Alex"`, matched all thirteen sessions, and got a
      plausible well-formed wrong answer; caught only because it was implausibly LONG.
      Path and hash are the only reliable filters. Generalises: an attribution mechanism
      that cannot distinguish its subjects returns confident-wrong, not empty — nastier
      than empty-vs-unknown, which you notice.
  (2) CORRECTED AT SOURCE 15:17 by backups, who read mission-check.py rather than
      trusting the report. There was NEVER a disagreement: line 32's Waiting-on regex
      already stops at the next `## `, and line 83 is a position-independent substring
      test. One tool read correctly, one read past its terminator, and THE CORRECT ONE
      WAS INVISIBLE BECAUSE IT HAS NO RENDERED OUTPUT. Their line, better than mine:
      "the rendered tool became the de facto spec even though it was the one that was
      wrong — the checker had been right the whole time and could not say so."
      FINAL WORDING, observability's, and it carries its own why where mine did not:
      "A rendering has no privileged claim to be right, and it will get one anyway — it
      is the only consumer that can speak. Before changing an artifact to satisfy one
      consumer, name them all and establish which is broken; THE MUTE ONE IS NOT THE
      SILENT PARTNER, IT IS USUALLY THE CORRECT ONE."
      The predictive part: the broken consumer was the one with output and THAT IS NOT
      A COINCIDENCE. A tool that renders gets read, quoted and believed BECAUSE it is
      visible; a correct tool with no output cannot participate in the argument at all.
      So the bias is not random — it systematically favours the most visible consumer,
      which is frequently the one furthest from the source of truth. "Two mechanisms
      disagree" invites RECONCILE THEM, wrong here since one needed no change. Fifth instance of the class; the previous four were
      all alerting, which disguised it as an alerting problem.
      Follows: dev-env's `## The brief` heading is NOT a workaround — it hands the
      broken extractor the terminator it was already looking for, and the checker never
      cared about position. The fleet-wide advice is sound, not a patch over a bug.
  (3) A `-A` sweep adopts previously-UNTRACKED files nobody is watching: 89f1140 took
      another mission's brand-new work and stood nine hours unnoticed.
  (4) dev-env refused a fleet-wide instruction from the routing session on the strength
      of having read the source. The useful behaviour is the refusal, not the check.
  (8) observability ran BOTH paths after the heading edit, because
      `PATH=/usr/bin:/bin` makes the liveness subprocess fail into its except branch —
      a plausible way to accidentally SKIP checks rather than speed them up. Slow path
      and fast path both: 13 missions, 0 fail, 0 warn. They agree, so it is a speed fix.
      Had it been a check-skipping fix nobody would have noticed and everybody would
      have been running the fast one.
  (6) backups' portable form of the `.prom` lesson, better than "nothing verifies
      itself" because it says what to DO: ask what the artifact would say if the job
      had STOPPED. A stale success file says "success" forever, so it cannot answer
      "did it run" — a store can only hold snapshots that were actually written.
  (7) backups spotted that "the checker requires the paragraph" + "add a heading above
      it" was an UNTESTED COMBINATION at the moment it was being recommended to ~26
      sessions, and that its own 0-fail predated the edit. Re-ran after: 13 missions,
      0 fail, 0 warn, and read the two source lines to establish WHY rather than that
      it passed. Nobody asked.
  (5) dns audited all thirteen of its commits after ONE was flagged — treating a flagged
      instance as a sample rather than the population — which is the only reason (3)
      was found. Nobody asked it to.
- 2026-08-31 my own inference on the 31s was wrong too: I read 25.7s->31s as growth under
  load. A fixed timeout fits better — a different count of things timing out, not a trend
- 2026-08-31 check-in churned this card three times: dormant rows out, live defects in. Four
  of the six are now mine and found by peers today, which is the honest shape of the project
- 2026-08-31 software-wsl found the card-served-from-parent bug still live in the CLI path;
  I fixed the coordinator side in v0.4.46 and never checked the command I tell people to verify with
- 2026-08-31 swapped the dormant spawn-with-inheritance row for a live one: `shaba todo`
  contradicts its own age caveat. Still unbuilt, still wanted, just not worth a slot today
- 2026-08-31 the public release path was never running: `release.yml` fires on tag
  push and I had been tagging locally and never pushing, so the newest thing anyone
  outside could download was v0.4.75 while the fleet read green. v0.4.79/80/81 published
- 2026-08-31 a transcript is a credential store — a preserved meeting transcript held a
  root password read aloud, filed into a work repo one `git add -A` from a push. Rule in
  the minutes skill; the rotation is wsl's row and is the only actual remedy
- 2026-08-31 my fix went in the wrong file: the payload vendors the minutes skill and the
  repo owns it. Found by minutes-mac from the one host I had not contaminated. `shaba rules`
  now compares them; repo is the source, payload vendors
- 2026-08-31 `shabadoo update` advertised a downgrade as an update — difference, never
  ordering, over-applied. It now compares build stamps and refuses, installing only when
  it genuinely cannot tell
- 2026-08-31 I published the credential string in v0.4.79 while removing it from the
  neighbouring file: routing the fix to its owner was right, pushing before it landed was not
- 2026-08-31 a pair discriminates only along the axis you vary — runner-wsl's finding,
  five lines in the core sharpening a rule already there
- 2026-08-31 the phone pass: 23 font sizes to 6 tokens, 44px targets, four empty columns
  that had said the same four words
- 2026-08-30 the board's shape and how much conversation a phone shows are both
  ANSWERED — status-viewing and the full reader, the latter now verified on device
- 2026-08-30 core ethos cut 1039 -> 462 lines; receipts, formats and session
  mechanics moved to skills, on the rule that only what fires unprompted stays
- 2026-08-30 an agent restart was killing every session on the host: the tmux
  server sits in the unit's cgroup and upgrade restarts by design. KillMode=process
- 2026-08-30 queued mail was acked at session start and never read, behind a clean
  `pending: 0`. Startup peeks now; only a prompt drains
- 2026-08-30 the plan had run out: phases 0-7 shipped while build-plan.md still said
  "awaiting deploy" and direction.md still called three built primitives missing.
  Rewrote both; phases 8-11 are all things somebody asked for, none invented
- 2026-08-30 `blockers` read four mechanical states and never the rows people write,
  so 37 human-owned blockers across 14 projects answered "nothing is stuck". `shaba todo`
  is the table; blockers counts them now
- 2026-08-30 seven scoped sessions all advertised their parent's card — mission was read
  at the project root while the session was named by its subfolder. Reported by recon-wsl
  with the cost measured, which is what made it obvious it was worse than showing nothing
- 2026-08-30 a wrapped MISSION.md line parsed as its own entry: an unattributed blocker
  nobody wrote, spending a slot against the six-row cap. Found by running the parser by hand
- 2026-08-30 a boot list of nineteen folders opened two — exiting a session to restart it
  is indistinguishable at the exit from closing it to free resources
- 2026-08-29 first retro: 8 sessions asked, 14 findings shipped to the payload;
  the round found more by being asked than by being read
- 2026-08-29 signed the binary on the Mac it lands on: ad-hoc signing meant every
  release silently revoked TCC grants, which cost a peer a 12-minute measurement run
- 2026-08-29 tool distribution — a release is a SET, published per node platform,
  partial by design because no host can build every set
- 2026-08-29 the paging dialect, specified against `/api/tasks` first; the iOS client
  was deliberately waiting rather than writing against a shape about to change
- 2026-08-29 the offline-delivery claim in the docs was false, and my test of it was
  worthless — it exercised only the case that works
- 2026-08-28 nudges were dead fleet-wide for ten hours over a non-breaking space; a
  second observer now watches for mail nobody picked up, because the nudge fails silently
- 2026-08-28 surveyed eight sessions on using this thing. Six replied. The findings
  drove most of the above, and the sharpest was from a session that never used it at all
