# The session framework itself — coordinator, per-host agents, and the payload of skills and conventions they install.
status: active
updated: 2026-08-29

## Now
Making a project's state legible from outside it. `MISSION.md` is the convention
half, shipped in the payload; the agent reporting it and a dashboard rendering
one card per project are the next two steps.

## Waiting on
- you: wrap-up rendering — dashboard cards are unwritten; the format is settled
- nobody: two no-op releases owed to minutes-mac, to prove a macOS grant
  survives an upgrade — cheap, and that node cannot test until it restarts

## Log
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
