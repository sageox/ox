<!-- doc-audience: ai -->

# ADR-019: Session Entity Lifecycle

**Status**: Accepted
**Date**: 2026-05-27

## Context

ADR-004 codified the **hook→phase** mapping (start/prompt/aftertool/compact/stop) — how heterogeneous agent events resolve to a small canonical phase set. That ADR addresses *what hooks do during a session*.

It does **not** address the higher-level question of what a "session" *is* as an entity, what states it can occupy, and how it moves between them. Multiple parts of the codebase — `internal/session/classify.go` (status enum), `internal/session/recording.go` (RecordingState), `internal/daemon/agentwork/session_finalize.go` (anti-entropy), `cmd/ox/agent_hook.go:stopSessionForClear` (clear boundary), `cmd/ox/agent_session_abort.go` (cancel) — each implement a *piece* of the entity lifecycle without a single source of truth.

This has caused recurring confusion:
- `/clear` is already a session boundary (`agent_hook.go:262-279`, `stopSessionForClear`), but no doc says so. New contributors assume `/clear` is invisible to ox.
- `/compact` is **not** a session boundary (same `forceReprime` path stops + restarts, but the design intent for compact is "in-session" — and current behavior is inconsistent with that intent: compact today also calls `stopSessionForClear`, finalizing the session, which is wrong for token compression).
- The `StatusPaused` constant means "user stopped, not uploaded" — a *classifier label*, not a lifecycle-active state. Adding pause/resume (ADR-020) requires a separate active state.
- Marker files (`.session_stopped.<agentID>`) are scattered across `cmd/ox/agent_session.go` and `internal/session/recording.go` with no canonical lifecycle doc.

This ADR codifies the session entity state machine so future work — including ADR-020 pause/resume — has a stable foundation.

## Decision

### Session as Entity

A **session** is one continuous unit of agent activity from `start` to terminal upload or discard. It has a stable identity (`session_name`, derived from timestamp+user+agentID at start), a recording cache directory (`state.SessionPath`), and a final ledger destination.

Sessions are owned by **one** `SAGEOX_AGENT_ID`. Subagents have their own sessions linked via `ParentSessionPath` / `ParentAgentID`.

### State Machine

```text
                  ┌─────────────────┐
                  │ not-recording   │
                  └────────┬────────┘
                           │ start
                           ▼
                  ┌─────────────────┐
                  │   recording     │◄────────┐
                  └────┬────────┬───┘         │
              suspend  │        │ /clear      │ resume
                       ▼        │  (boundary) │
              ┌─────────────┐   │             │
              │  suspended  │───┘             │
              └─────┬───┬───┘ ┌───────────────┘
                    │   └─────┘
              stop  │       PID dead + grace
                    │       (orphan path)
                    ▼
                  ┌─────────────────┐
                  │    stopped      │   (terminal-local)
                  └────────┬────────┘
                           │ upload
                           ▼
                  ┌─────────────────┐
                  │    uploaded     │   (terminal-remote)
                  └─────────────────┘

   Failure / discard branches:
   recording ──> ghost   (PID dead, no data, ≥ grace)
   recording ──> orphan  (PID dead, has data, ≥ grace) ──> local ──> uploaded
   any-recording-state ──> canceled (terminal, data discarded)
```

| State | `SessionStatus` constant | Meaning |
|---|---|---|
| not-recording | (no state file) | No active session for this agent |
| recording | `StatusRecording` | Tail-watcher/hooks appending entries |
| suspended | `StatusSuspended` (ADR-020) | User-paused; recording still appends locally, lifecycle marks the range excluded for upload |
| stopped | `StatusPaused` (legacy name — see "Naming note" below) | User stopped; data preserved locally; awaiting upload |
| local | `StatusLocal` | On-disk but not uploaded (recovered orphan or stopped) |
| uploaded | `StatusUploaded` | Committed to ledger |
| ghost | `StatusGhost` | Parent dead, no data — safe to delete |
| orphan | `StatusOrphan` | Parent dead, has data — needs recovery |
| canceled | `StatusCanceled` | User explicitly discarded (terminal — data deleted) |

### Naming note

The existing `StatusPaused` (`internal/session/classify.go:40`) labels a *stopped-but-not-uploaded* session. Despite the name it is **not** an active-pause state. ADR-020 introduces `StatusSuspended` for true active pause. The existing `StatusPaused` constant is **not** renamed — doing so would migrate `.recording.json` files on every user's disk and uploaded ledger metadata, for cosmetic gain.

### Transitions

| From | To | Trigger | Side effect |
|---|---|---|---|
| not-recording | recording | `StartRecording`, hook-init, prime auto-start | New `RecordingState` written |
| recording | suspended | `ox session pause` (ADR-020) | Lifecycle event + `.session_paused.<agentID>` marker |
| suspended | recording | `ox session resume` (ADR-020) | Lifecycle event + marker cleared |
| recording / suspended | stopped | `ox session stop`, `SessionEnd` hook | `StoppedAt` set; daemon finalizes |
| recording / suspended | canceled | `ox session abort` | Marker `.session_canceled`; cache deleted |
| stopped | local | Daemon detects stopped + has data | Eligible for upload |
| local | uploaded | Upload pipeline | Committed to ledger; cache pointer-ized per cache-only rule |
| recording / suspended | ghost | PID dead, no data, ≥ 5min `ghostHeuristicAge` | Cleanup eligible |
| recording / suspended | orphan | PID dead, has data, ≥ grace | Daemon recovery path |
| orphan | local | Daemon recovery finalizes | Same as stop |

### `/clear` is a Session Boundary

**Behavior** (already implemented in `cmd/ox/agent_hook.go:262-279,302-338`):
- `SessionStart` hook with `source = "clear"` triggers `forceReprime`.
- `stopSessionForClear` sets `StoppedAt` on the prior recording, fires a fire-and-forget daemon finalize IPC, and clears recording state.
- `runPrimeForHook` then starts a new session in the same `SAGEOX_AGENT_ID` lineage (via `CLAUDE_ENV_FILE` env persistence).

**Why** `/clear` is a boundary:
- `/clear` wipes the agent's conversation memory. The prior context is gone from the agent's perspective.
- Continuing a single `raw.jsonl` across `/clear` would conflate unrelated logical units.
- Agent ID lineage continuity is preserved separately so user identity feels stable.

**User-visible notice** (new in this ADR):
The post-`/clear` prime invocation emits a `UserNotification` describing the boundary transition. Format:

```text
[ox] /clear → previous session Ox{id} ({status}, {duration}) finalized.
     New session Ox{id} started. Recording: {on|off}.
```

When ADR-020 ships, the notice gains an inherited-pause variant.

### `/compact` is NOT a Session Boundary

**Intended behavior**: `/compact` is token-window compression. The agent summarizes prior turns; the session continues. Same `raw.jsonl` keeps growing.

**Current code** (`agent_hook.go:262`) treats `compact` the same as `clear` (`forceReprime = source == "clear" || source == "compact"`). This conflates two different events. **This ADR formally specifies that `/compact` should not finalize the session**; a follow-up issue tracks separating the two paths so `/compact` only re-primes context without stopping recording. Until that fix lands, callers who care about session continuity across compaction should rely on the daemon's incremental drain to keep raw.jsonl up to date.

### Marker File Invariants

| Marker | Owner | Survives `/clear` | Cleared by |
|---|---|---|---|
| `.session_stopped.<agentID>` | `MarkExplicitStop` (`recording.go:370`) | No (consumed at prime) | `ConsumeExplicitStop` |
| `.session_paused.<agentID>` (ADR-020) | `MarkExplicitPause` | **Yes** | resume / stop / abort / expiration |
| `.session_clear_notice.<agentID>` (this ADR) | `stopSessionForClear` env handoff | N/A — env-scoped, one-shot | prime subprocess read |

Markers are written to the canonical sessions cache directory (`paths.LedgerSessionCacheBase` when endpoint configured, else XDG cache). Reads search alternate XDG cache dirs (`sessionsSearchPaths` in `recording.go:438`) to bridge processes with different `XDG_CACHE_HOME` values (Conductor GUI vs terminal shell).

### PID-Death + Grace

`ghostHeuristicAge` = 5 minutes (`classify.go:50`). After grace:
- No data → `ghost` → cleanup eligible.
- Has data → `orphan` → daemon recovery via `session_finalize.go` finalize path.

ADR-020 adds `PauseGhostGrace` = 15 minutes for suspended-then-PID-dead sessions, applied via the existing orphan path.

## Consequences

**Benefits**:
- Single source of truth for the entity lifecycle (this ADR + the `SessionStatus` constants).
- ADR-020 has a clean foundation to build pause/resume on.
- New contributors don't reinvent partial state machines.
- The `/clear` notice surfaces an existing-but-invisible boundary to users.

**Tradeoffs**:
- `StatusPaused` legacy name persists. Disambiguated by `StatusSuspended` in ADR-020.
- The `/compact` separation from `/clear` is intent-only here; the code unification at `agent_hook.go:262` is tracked as a follow-up.

## Cross-References

- ADR-004: hook→phase mapping (the layer below this one).
- ADR-005: anti-entropy (daemon-side state recovery uses these states).
- ADR-016: session summarization (consumes uploaded sessions; the state machine guarantees what summarizer sees).
- ADR-020: session pause/resume (built on this ADR; introduces `StatusSuspended`).
- `internal/session/classify.go` — `SessionStatus` constants and `ClassifySession`.
- `internal/session/recording.go` — `RecordingState`, `MarkExplicitStop`, `ClearRecordingStateForAgent`.
- `cmd/ox/agent_hook.go:262-338` — `/clear` boundary implementation.
- `internal/daemon/agentwork/session_finalize.go` — orphan finalization path.
