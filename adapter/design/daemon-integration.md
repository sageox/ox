# Daemon Integration Design

## Adapter Process Lifecycle

The daemon maintains an `AdapterSupervisor` that manages all active adapter processes. One process per session (not per adapter type).

```
daemon
  AdapterSupervisor
    sessions: map[agentID → AdapterSession]
      "OxA1b2" → {type: "claude-code", pid: 1234, stdin, stdout, lastOffset: 1024}
      "OxB3c4" → {type: "claude-code", pid: 1235, stdin, stdout, lastOffset: 512}
      "OxC5d6" → {type: "kiro",         pid: 1236, stdin, stdout, lastOffset: 0}
```

Multiple sessions of the same adapter type = multiple processes. This keeps state isolated and avoids routing complexity inside adapter binaries.

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
