# Daemon Integration Design

## Adapter Process Lifecycle

The daemon maintains an `AdapterSupervisor` that manages one process per **adapter type**. All
active sessions of a given type are multiplexed through that single process using `agent_id`.

```
daemon
  AdapterSupervisor
    processes: map[adapterType → AdapterProcess]
      "claude-code" → {pid: 1234, stdin, stdout,
                       sessions: {
                         "r7f3a2-OxA1b2": {sessionFile: "...", lastOffset: 1024},
                         "r7f3a2-OxB3c4": {sessionFile: "...", lastOffset: 512},
                       }}
      "amp"         → {pid: 1236, stdin, stdout,
                       sessions: {
                         "r9b1c4-OxC5d6": {sessionFile: "...", lastOffset: 0},
                       }}
```

Every serve-mode request includes `agent_id` so the adapter maintains per-session state
internally. The daemon tracks per-session offsets and file paths; the adapter tracks per-session
file handles.

**On adapter process crash**: all sessions of that type are affected. The daemon respawns the
process and re-sends `find-session` for every session that was active, restoring from the last
checkpointed offset for each.

## IPC Extension

The daemon's existing Unix socket IPC gains two new message types:

**Hook → Daemon (read request)**:
```json
{"type": "adapter.read", "agent_id": "OxA1b2", "offset": 512}
```

**Daemon → Hook (read response)**:
```json
{"type": "adapter.read.result", "entries_captured": 3, "new_offset": 1024, "error": null}
```

The hook CLI process sends one IPC message, waits for response, exits. The daemon does all the heavy work: routing to the right adapter process, piping JSON-RPC, writing to `raw.jsonl`, updating offset.

**Per-agent request queue**: When multiple hook processes fire concurrently for the same session
(overlapping tool calls), the daemon queues IPC requests per `agent_id`. The second hook's
`adapter.read` blocks on the IPC socket until the first completes. This prevents duplicate reads
and offset corruption. Different `agent_id`s are not blocked by each other.

## Lazy Startup

Adapter processes start on the first hook call for a session, not at session registration. This avoids spawning adapters for sessions that start and immediately end without tool calls.

First hook call for agent `OxA1b2`:
1. Lookup `OxA1b2` in sessions map — miss
2. Read `recording_state.json` for `OxA1b2` — get `adapter_name: "claude-code"`
3. Spawn `ox-adapter-claude-code --serve`
4. Send `find-session` — get session file + start offset
5. Store as `AdapterSession{...}`
6. Proceed with `read-from-offset`

## Fallback: Daemon Not Reachable

```go
func handleAfterTool(ctx *HookContext) error {
    if client := daemon.TryConnect(ctx.ProjectRoot); client != nil {
        // fast path: IPC to daemon, no spawn
        return client.AdapterRead(ctx.AgentID, ctx.Offset)
    }
    // slow path: one-shot spawn
    return oneShot.ReadFromOffset(ctx.AdapterName, ctx.SessionFile, ctx.Offset)
}
```

## Streaming / Future Mid-Session Distribution

The daemon currently uploads session data only at `session stop`. The architecture supports future incremental uploads without protocol changes:

Each `read-from-offset` call returns new entries. Currently: daemon appends to `raw.jsonl` only. Future: daemon also pushes each batch to the ledger as a partial upload. Other ox daemons on the team can subscribe to partial uploads and replay them in real time.

The adapter protocol is unchanged — it just reads and returns entries. The daemon decides the upload policy.
