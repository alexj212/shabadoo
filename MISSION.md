# The session framework itself — coordinator, per-host agents, and the payload of skills and conventions they install.
status: active
updated: 2026-08-29

## Now
Making a project's state legible from outside it. `MISSION.md` is the convention
half, shipped in the payload; the agent reporting it and a dashboard rendering
one card per project are the next two steps.

## Waiting on
- you: four decisions out of the retro — a mis-addressed send, a leaked key, 224MB
  of recordings, two undelivered meetings. All in the wrap-up, none are mine to make
- you: dashboard cards (step 3) — format settled, nothing renders it
- me: `shabadoo open` returns before the coordinator registers the session; two
  sessions independently wrote the same poll loop
- me: no scoped broadcast — running the retro cost wsl eight hand-copied sends
- nobody: spawn-with-inheritance, unsketched

## Log
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
