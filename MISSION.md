# The session framework itself — coordinator, per-host agents, and the payload of skills and conventions they install.
status: active
updated: 2026-08-30

## Now
Web UI pass next — the backend is settled and every phase in the plan is shipped.
The conversation view is the default on both clients on Alex's side-by-side
verdict; the board, files and chat readers are live but have had no design pass.

## Waiting on
- you: four decisions out of the retro — a leaked ElevenLabs key, 5.5GB of home audio, two unrouted Jeff meetings, one mis-addressed send · homelab and wsl carry the detail
- you: web UI pass scope — which surfaces matter most on a phone · one conversation, I have a recommendation
- me: no scoped broadcast — one fleet fan-out still costs N hand-copied sends · filed, low risk to skip
- nobody: Phase 11 step 3 — whether a session may file a change to the ethos it obeys · a decision, not a task
- nobody: spawn-with-inheritance, mentioned three times and designed zero
- nobody: a skill to open VS Code on a project folder · same shape as `shaba dash`, WSL needs the Windows side · ~15 min

## Log
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
