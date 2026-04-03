# ADR-005: Daemon as Adapter Process Supervisor

**Status**: Proposed
**Date**: 2026-04-02

## Context

Hooks fire as separate short-lived `ox` process invocations — one per tool call. There is no persistent ox CLI process between hook calls. Yet adapters benefit from staying alive across calls (open file handles, in-memory offset, SQLite connections).

A single daemon serves a single repo/ledger. Within that repo, multiple coding agents of different types may be active simultaneously — e.g., Claude Code and Kiro running in parallel, each with their own session. The daemon must manage adapter processes for all of them concurrently.

## Decision

The daemon owns the lifecycle of all adapter processes. It:
1. Spawns `ox-adapter-<name> --serve` when a session of that type starts
2. Holds the stdin/stdout pipes
3. Routes requests from hook calls (via IPC) to the appropriate adapter process
4. Supervises and restarts on crash
5. Kills adapter processes when sessions end or daemon shuts down

## Multi-Adapter Model

```
daemon
  ├── AdapterProcess{type: "claude-code", pid: 1234}
  │     └── ox-adapter-claude-code --serve
  │           handles sessions: OxA1b2, OxB3c4 (multiplexed by agent_id)
  └── AdapterProcess{type: "kiro", pid: 1236}
        └── ox-adapter-kiro --serve
              handles sessions: OxC5d6
```

**Key**: one adapter process per *type*, shared across all active sessions of that type. Multiple
Claude Code sessions route through one `ox-adapter-claude-code --serve` process. Every serve-mode
request includes `agent_id` so the adapter can maintain per-session state internally (file handles,
byte offsets, watch subscriptions).

**Why per-type rather than per-session**:
- Fewer processes when multiple sessions of the same agent type are active (common in team environments)
- SQLite-backed adapters (Kiro) maintain a single DB connection shared across sessions — more efficient
- Event push (watch mode) is naturally multiplexed: events already carry `agent_id`
- The adapter handles session isolation; the daemon handles routing by type

The adapter is responsible for keeping per-`agent_id` state isolated. A crash in session OxA1b2's
watch loop must not corrupt OxB3c4's offset. This is a higher implementation bar than per-session
isolation — adapter authors must handle concurrent sessions.

## Hook Call Path

```
PostToolUse hook fires (Claude Code session OxA1b2)
  → ox CLI process starts
  → IPC to daemon: {"type":"adapter.read","agent_id":"OxA1b2","offset":512}
  → daemon looks up AdapterSession for OxA1b2
  → writes to that process's stdin: {"id":7,"method":"read-from-offset","params":{...}}
  → reads from that process's stdout: {"id":7,"result":{"entries":[...],"new_offset":1024}}
  → daemon writes entries to raw.jsonl, updates offset
  → IPC response to ox CLI: {"entries_captured":3}
  → ox CLI exits
```

Total time: one IPC roundtrip + one pipe write/read. No process spawn on the hot path.

## Crash Recovery

Adapter process exits unexpectedly → daemon detects pipe EOF → respawns → resumes from last checkpointed offset (written to `recording_state.json` after each successful read).

Max restarts: 3. After that, session marked as degraded but ox continues operating.

## Daemon-Not-Running Fallback

If the daemon is unreachable, hook falls back to one-shot mode: spawn adapter binary, call `read-from-offset`, exit. Slower (one spawn per tool call, ~15-20ms) but recording continues. This preserves the current ox principle: "IPC is advisory."

## Adapter Process Startup

The daemon spawns adapter processes lazily — on the first hook call for a session, not at session start. This avoids spawning adapters for sessions that end quickly without tool calls.

Sequence on first hook call for a new session:
1. Look up agent_id in AdapterSession map — not found
2. Look up session's adapter type from recording_state
3. Spawn `ox-adapter-<type> --serve`
4. Send `find-session` to get session file path
5. Cache as AdapterSession
6. Proceed with `read-from-offset`

## Daemon Shutdown

On shutdown, daemon sends `{"method":"shutdown"}` to all adapter processes, waits up to 5 seconds, then SIGTERMs any still alive.

## On Daemon Restart

Daemon reads active sessions from `recording_state.json` files on disk. For each active session, lazily respawns the adapter process (on next hook call, not immediately). Last good offset is preserved from disk — no data loss.
