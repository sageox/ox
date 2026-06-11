# ADR-020: Session Pause/Resume

**Status**: Accepted
**Date**: 2026-05-27

## Context

GH issue #134 asked for `ox session pause` / `ox session resume` so users can temporarily suspend recording during work that should not be part of the session summary — debugging an unrelated issue, system maintenance, side errands. Today the only options are:

- **Stop + Start** → two unrelated sessions; loses logical continuity.
- **Keep recording** → captures unrelated work that pollutes the eventual summary.

ADR-019 codifies the session entity lifecycle. This ADR builds the pause/resume feature on top of it.

## Decision

### Pause is a temporal redaction scope

A pause does not stop ingestion. The local cache `raw.jsonl` keeps growing through the suspended interval. At upload time (`cmd/ox/session_stop.go:processSession`), a **segment mask** computed from the lifecycle timeline excludes all entries whose 0-indexed position falls in any `[pause_seq, resume_seq)` range. Local cache stays complete (recovery available); ledger upload is scrubbed.

This unifies pause with the existing post-hoc redaction infrastructure (`internal/session/redact_rules.go`). Pause IS redaction — just with temporal scope instead of pattern matching.

### Single active-pause state, no rename

A new `SessionStatus` constant `StatusSuspended` represents active pause. The existing `StatusPaused` (which actually means "user stopped, not uploaded") is **not** renamed — that would migrate `.recording.json` files on every user's disk and uploaded ledger metadata for cosmetic gain. ADR-019 documents the legacy name; `ClassifySession` returns `StatusSuspended` when `Recording && SuspendedAt != nil && agent_pid_alive`.

### Tail-watcher never gates appends

Pause is purely a CLI-side state mutation. The daemon tail-watcher and hook-driven append paths keep writing to local cache throughout the suspended interval. No append-race during the pause boundary. No new IPC. Mask runs only at upload.

This collapses the race-condition surface to zero — there's no point during the suspended window where a write needs to be blocked.

### Marker file survives `/clear`

`internal/session/pause_marker.go` writes `.session_paused.<agentID>` to the canonical sessions cache directory on pause. Unlike `.session_stopped.<agentID>` (consumed at the next prime), the pause marker **survives `/clear`**. New post-`/clear` sessions inherit the suspended state via `PeekExplicitPause` in `cmd/ox/agent_prime.go:startSessionRecording`. The marker is cleared by:

- `ox session resume`
- `ox session stop`
- `ox session abort`
- daemon expiration after PID-death grace

### Per-agent stickiness only

Pause stickiness is keyed by `SAGEOX_AGENT_ID`. New terminal / new agent process = new agent ID = fresh start, no inheritance. This favors over-capture (recoverable) over silent under-capture (irrecoverable).

A project-level "all agents paused" or TTL-bounded variant was rejected for failing toward silent data loss.

### Subagent inheritance

| Scenario | Behavior |
|---|---|
| Existing subagent running at pause time | Keeps recording. Finishes + uploads its own session normally. No cascade. |
| Subagent spawned during pause | `startSessionRecording` detects parent's pause marker (or in-flight `SuspendedAt`) and skips `StartRecording` entirely. Subagent runs without ox tracking. Prime emits a one-line notice. |
| Subagent outlives parent's pause | Subagent stays unrecorded for its lifetime. No retroactive recording. |

### Per-prompt nudge

While `state.SuspendedAt != nil`, `handlePrompt` in `cmd/ox/agent_hook.go` emits a `<system-reminder>` on every `UserPromptSubmit`:

```text
[ox] ⏸ Recording SUSPENDED (3h 14m ago). Resume: /ox-session-resume · Stop: /ox-session-stop
```

Scoped to the **current agent's** suspended session only — other repo paused sessions don't bleed into the nudge.

### `/clear` notice

`stopSessionForClear` captures finalized-session info (name, status including `was_suspended`, duration) into `OX_CLEAR_PRIOR_SESSION` env. The prime subprocess reads it via `parseClearNoticeEnv` and emits a `UserNotification`:

```text
[ox] /clear → previous session Ox{id} ({status}, {duration}) finalized.
     New session Ox{id} started. Recording: {on|off}.
```

The suspended variant emits:

```text
[ox] /clear → previous session Ox{id} (suspended, {duration}) finalized.
     Paused range excluded from upload.
     New session Ox{id} started — RECORDING SUSPENDED (carried from previous).
     Resume: /ox-session-resume · Stop: /ox-session-stop
```

### Expiration

A suspended session with a dead parent PID past the existing `session.GhostGracePeriod` (10 min) falls through to the existing orphan finalization path. That path now applies the segment mask via `processSession`, so paused work is excluded from the upload regardless of whether the user explicitly resumed.

No separate `StatusExpired` state. No hard time cap — process lifetime IS the cap, augmented by `ox doctor` as the user-facing escape hatch for stuck pauses.

### Client-side validator

`cmd/ox/session_validate.go:validateMaskInvariant` runs after `ApplySegmentMask` in `processSession`. It computes the expected masked count from the lifecycle timeline and compares against the actual post-mask count. Mismatch aborts upload — paused work cannot leak.

Cloud-side validator is out of scope for this ADR.

### Idempotency

| Command | When | Behavior |
|---|---|---|
| `ox session pause` | already suspended | no-op, friendly message, exit 0 |
| `ox session resume` | not suspended | no-op, friendly message, exit 0 |
| `ox session pause` | not recording | friendly error, exit 1 |
| `ox session resume` | not recording | friendly error, exit 1 |

## Implementation

| Component | Location |
|---|---|
| `StatusSuspended` constant | `internal/session/classify.go` |
| `LifecycleEvent` + `LifecycleAction` | `internal/session/recording.go` |
| `RecordingState.SuspendedAt`, `PauseCount`, `Lifecycle`, `InheritedPause`, `InheritedFromSession` | `internal/session/recording.go` |
| `MarkExplicitPause` / `PeekExplicitPause` / `ConsumeExplicitPause` / `ClearExplicitPause` | `internal/session/pause_marker.go` |
| `BuildSegmentRanges` / `ApplySegmentMask` / `CountMaskedEntries` | `internal/session/segment_mask.go` |
| `ox agent <id> session pause` | `cmd/ox/agent_session_pause.go` |
| `ox agent <id> session resume` | `cmd/ox/agent_session_resume.go` |
| `/clear` post-prime notice handoff | `cmd/ox/clear_notice.go`, `cmd/ox/agent_hook.go:stopSessionForClear`, `cmd/ox/agent_prime.go` |
| `/clear` × pause inheritance | `cmd/ox/agent_prime.go:startSessionRecording` |
| Subagent inheritance | `cmd/ox/agent_prime.go:startSessionRecording` |
| Per-prompt nudge | `cmd/ox/agent_hook.go:handlePrompt`, `emitSuspendedNudge` |
| Upload mask application | `cmd/ox/session_stop.go:processSession` |
| Mask invariant validator | `cmd/ox/session_validate.go:validateMaskInvariant` |
| `ox session status` surface | `cmd/ox/session_status.go` |
| Skills | `.claude/commands/ox-session-pause.md`, `.claude/commands/ox-session-resume.md` |

## Consequences

**Benefits**:
- One unified mask path at upload — no parallel masking infrastructure.
- Tail-watcher is unchanged; zero new race surface.
- Local cache stays complete for forensic recovery.
- Per-agent stickiness avoids silent data loss across context switches.
- Adapter-agnostic — every adapter that writes monotonic seq entries to raw.jsonl works.

**Tradeoffs**:
- Paused work briefly occupies local disk until the next stop/abort. Acceptable — cache cleanup is separate.
- The legacy `StatusPaused` name persists. Disambiguated in code and ADR.
- Amp (cloud-first) records the lifecycle timeline but cannot gate cloud-side capture. Documented in agent-support-matrix.md.
- Server-side mask validator is future work — client-side validator is fail-closed today.

## Cross-references

- ADR-019: session entity lifecycle (foundation).
- ADR-004: hook → phase mapping (the layer below).
- `internal/session/segment_mask_test.go`: 200-trial property test.
- GH issue #134.
