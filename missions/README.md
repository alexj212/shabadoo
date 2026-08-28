# Missions

Focused agent missions against **this operator's machine** — the environment shabadoo installs and
maintains. Each folder holds a `MISSION.md` charter; the agent obeys **prep → present → await go**,
one visible step at a time, and stops at the ❌ gates regardless of any go.

They live here, in the shabadoo repo, because shabadoo owns what they maintain: the portable payload
(`config/`), the personal overlay (`config.local/`), and the installed `~/.claude` the two produce.

| Folder | Mission |
|---|---|
| `pc-cleanup/` | Claude Code environment hygiene — skills, `CLAUDE.md`, MCP servers, launcher state |

**The standing rule these exist to enforce: promote, don't absorb.** Environment drift found while
doing other work gets raised as an item here, not patched in the margin of an unrelated task.

## How they learn — distributed, not per-session

A mission that only *fixes* a machine has taught nobody. Every finding is expected to travel:

```
agent finds it  →  fix at the SOURCE (config/, not ~/.claude)
                →  make vendor + shabadoo setup   →  every session on this machine
                →  shabadoo publish / upgrade --all →  every connected node
```

So the unit of work is not "I fixed my `~/.claude`" — it is **"the payload now knows this, so no agent
on any machine will hit it again."** A skill corrected here is a skill corrected everywhere the binary
lands; that is what makes these agents distributed rather than merely parallel.

**Not everything belongs in the payload.** Global knowledge goes global; estate knowledge goes to that
estate's own library. The routing table is **"Where a learning goes"** in `config/CLAUDE.md` — read it
before deciding where to write a finding. The split is machine-enforced: `make vendor-check` fails if a
work-specific token reaches the embedded payload.

Two corollaries:

- **Write the finding where it is read, not where it was found.** A trap discovered while debugging a
  launch belongs in the `claude-sessions` skill, so the next agent reads it *before* launching — not in
  a mission log nobody opens.
- **Peers promote to each other.** A mission that learns something outside its own charter hands it to
  the mission that owns it, rather than fixing it inline or dropping it. The hand-off is the point.
