<!-- doc-audience: ai -->
# Daemon State Design Principles

Distilled from the [Beads/Dolt failure analysis](/tmp/attachments/pasted_text_2026-03-22_08-18-43.txt) — 7 families of catastrophic failures that occurred when the beads project migrated from SQLite to Dolt for its local state management.

These principles apply to **all persistent state in the ox daemon** — whisper store, future caches, any new SQLite databases, or any other local state mechanism.

## Principle 1: Embedded-only, no sidecar processes

**Beads failure**: Server Lifecycle Hell (#2685) — zombie dolt processes consuming 600MB each, 5.5s cold-start penalties, infinite restart loops, stale cleanup killing healthy servers from other repos. "What looked like separate bugs was really one subsystem failing through different entry points."

**Rule**: The daemon's state store is an **embedded library** (SQLite via `modernc.org/sqlite`), not a separate server process. No ports, no PIDs, no ownership tracking, no lifecycle management. The store opens when the daemon starts and closes when it stops.

## Principle 2: Single authoritative state model

**Beads failure**: Runtime state (ports, PIDs, ownership, data dirs) was reconstructed independently by CLI code, reopen helpers, doctor checks, repair flows, and storage code — and they didn't reconstruct the same thing (#2685).

**Rule**: One `Store` struct owns all state access. No parallel paths to the same data. `ox doctor` reads the same `Store` the daemon uses — never reconstructs state independently. All callers go through the same API surface.

## Principle 3: Never version machine-local state

**Beads failure**: Machine-local metadata (timestamps, auto-push commits) was committed to Dolt's versioned history, causing merge conflicts on every multi-machine sync (#2466). `bd init` on a second clone created unrelated history (#2580).

**Rule**: The whisper DB is **local-only cache** in `.sageox/cache/`. Never committed to git. Never synced between machines. Never versioned. The source of truth is the murmur files in the ledger (git-tracked). The SQLite DB is a derived index.

## Principle 4: Concurrent access must be safe by design, not by bolt-on locking

**Beads failure**: Journal corruption with 3 concurrent agents (#2430). Concurrent `initSchema` DDL races corrupted the journal (#2672). Fix was bolting on `GET_LOCK/RELEASE_LOCK` — a workaround, not a cure. Even read-only commands panicked under concurrency (#2571).

**Rule**: SQLite WAL mode provides safe concurrent reads with a single writer (the daemon). Multiple CLI processes access state via the daemon's IPC layer, not direct file access. No bolt-on locking. No multi-process direct DB access.

## Principle 5: Transparent auto-recovery, never require user intervention

**Beads failure**: `bd doctor` reported false errors, started servers when it shouldn't, didn't connect to configured servers (#2694/#2722). Fresh `bd init` succeeded but immediately reported failures (#2656). "For a tool called doctor, it was sick more often than the patients."

**Rule**: If the state DB is corrupt → delete it → recreate it → continue. Callers never see the error. No manual recovery steps. The DB is ephemeral cache — losing it costs nothing. Log a warning for debugging, but the user experience is: nothing happened.

**`ox doctor` integration**: Doctor auto-fixes state DB corruption at `FixLevelAuto` (no `--fix` flag needed). The `--fix` flag is reserved for destructive/ambiguous repairs that require human oversight.

## Principle 6: SQL engine must be battle-tested, not "mostly compatible"

**Beads failure**: Dolt's SQL engine cast UUIDs to float64 producing +Inf (#2760). `DOLT_CHECKOUT` didn't persist across calls. `INFORMATION_SCHEMA` worked differently between modes.

**Rule**: Use `modernc.org/sqlite` — a pure-Go port of the canonical SQLite C codebase. Not "mostly SQLite-compatible," but actual SQLite. Same behavior everywhere. Already proven in this codebase via CodeDB.

## Principle 7: State must be rebuildable from the source of truth

**Beads failure**: Moving to Dolt broke the atomic git model — issue data became decoupled from code in a gitignored directory (#2489). If Dolt state was lost, the data was gone.

**Rule**: The state DB is a **derived cache**, not a primary store. Source of truth:
- **Murmur files**: `data/murmurs/` in the ledger (git-tracked)
- **Whisper entries**: re-scannable from murmur files on demand
- **Cursors**: reset gracefully (agents get one noisy cycle, then normal)
- **Relayed tracking**: lost = some murmurs re-deliver once (harmless)

If the entire DB disappears, the relay re-scans the ledger on the next sync tick and repopulates everything. Zero data loss. Zero user impact.

---

## Anti-Pattern Checklist

Before adding any persistent state to the daemon, verify:

- [ ] **No sidecar process** — is it embedded in the daemon, or does it require managing a separate server?
- [ ] **Single state owner** — is there exactly one code path that reads/writes this state, or do multiple paths reconstruct it independently?
- [ ] **No versioned local state** — is machine-local state kept out of git/sync, or could it cause merge conflicts?
- [ ] **Safe concurrency** — does the concurrency model work by design (e.g., WAL + single writer), or does it require bolt-on locking?
- [ ] **Invisible recovery** — if the state is corrupt, can it auto-recover without user intervention?
- [ ] **Battle-tested engine** — is the storage engine proven at scale, or "mostly compatible" with something proven?
- [ ] **Rebuildable from source of truth** — if the state disappears, can it be fully reconstructed from authoritative data?
