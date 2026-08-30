# The session framework itself — coordinator, per-host agents, and the payload of skills and conventions they install.
status: active
updated: 2026-08-30

## Now
Phase 8 — the papercuts batch: a `*` on boot-enabled sessions in every listing,
the dashboard URL in output, and `open` waiting until the coordinator has
registered the session. Phase 9 (reading a conversation on a phone) is next and
is the one thing the mobile client cannot do at all.

## Waiting on
- you: the board's shape — kanban or grouped tables · a week of layout nobody wanted · one conversation, options in build-plan Phase 10
- you: how much of a conversation a phone shows · the transcript store holds anything ever pasted · one call, argued in Phase 9
- you: four decisions out of the retro — a mis-addressed send, a leaked key, 224MB of recordings, two undelivered meetings · all in the wrap-up
- me: Phase 8 papercuts — `*` on boot rows, dashboard URL, `open` waits for registration · three sessions wrote the same poll loop · an afternoon
- mac: node offline since the 29th — its two sessions and three open tasks are frozen · one `launchctl` check on that machine
- nobody: scoped broadcast, and spawn-with-inheritance — mentioned three times, designed zero

## Log
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
