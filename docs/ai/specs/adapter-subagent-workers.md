# Subagent Workers: Adapter-Controlled Agentic Sessions

## The Two Adapter Roles

Adapters currently act as **observers**: the user runs Claude Code, ox watches and records the
session. Sub-workers introduce a second role: **controller**. ox directs an adapter to spawn
and run a headless agent session programmatically to do autonomous work.

```
Observer role (current):
  User runs Claude Code → hooks fire → adapter reads session → daemon records

Controller role (new):
  Daemon sends task → adapter spawns headless Claude Code → agent runs autonomously
  → adapter streams progress back → daemon surfaces result
```

These roles are complementary. The same `ox-adapter-claude-code` binary can observe user-initiated
sessions and control daemon-spawned worker sessions.

---

## Why stdin/stdout Stays the Control Plane

A sub-worker session may run for 3 seconds or 1 hour. The adapter's `--serve` pipe cannot block
on a long-running agent invocation — it would be unavailable for `read-from-offset` calls, other
session management, and cancellations during that time.

The solution: **the adapter spawns the agent as a separate child process** and manages it
independently. The serve pipe stays responsive. Progress and completion flow back as push events
— the same push event mechanism used for file watching.

```
daemon
  │
  ├── [stdin/stdout pipe] ──→ ox-adapter-claude-code --serve
  │       Control plane:                │
  │         spawn-subagent              │ adapter manages separately:
  │         subagent-status     ←───────┤
  │         cancel-subagent             ├── claude --headless (worker A, pid 4521)
  │         push events                 ├── claude --headless (worker B, pid 4522)
  │                                     └── claude --headless (worker C, pid 4523)
  │
  └── [push events from adapter]
        subagent.progress  {worker_id, output_chunk}
        subagent.completed {worker_id, result}
        subagent.failed    {worker_id, error, exit_code}
```

The adapter process is the supervisor for its worker subprocesses. The daemon is the supervisor
for the adapter process. The daemon never directly manages agent processes.

---

## Capability Declaration

Adapters that support being used as controllers declare it:

```json
{
  "protocol_version": 1,
  "name": "claude-code",
  "capabilities": [
    "session_reader",
    "hook_installer",
    "incremental_reader",
    "file_watcher",
    "subagent_controller"
  ],
  "subagent_config": {
    "max_concurrent":  4,
    "models":          ["claude-opus-4-6", "claude-sonnet-4-6", "claude-haiku-4-5"],
    "default_model":   "claude-sonnet-4-6",
    "credential_hint": "ANTHROPIC_API_KEY or ~/.claude/credentials"
  }
}
```

`subagent_config` is informational — helps `ox config` surface valid options. The adapter
enforces limits internally. `credential_hint` tells the user where to look if a worker fails
to authenticate.

---

## New Serve-Mode Methods

### `spawn-subagent`

Start a headless agent session. Returns immediately with a `worker_id` — the agent runs
asynchronously. Progress arrives as push events.

```json
{"id":10,"method":"spawn-subagent","params":{
  "worker_id":   "w-OxA1b2-0001",
  "agent_id":    "r7f3a2-OxA1b2",
  "repo_id":     "a1b2c3d4...",
  "team_id":     "team_xyz",
  "repo_root":   "/path/to/repo",
  "model":       "claude-sonnet-4-6",
  "task":        "Add unit tests for the auth module. Follow existing test patterns.",
  "context":     {"files": ["src/auth/*.go"], "max_tokens": 8192},
  "timeout_sec": 1800
}}
```

```json
{"id":10,"result":{"worker_id":"w-OxA1b2-0001","status":"starting"}}
```

`worker_id` is assigned by the daemon and passed to the adapter — globally unique, scoped to the
originating session (`agent_id` prefix). The adapter uses it to correlate push events.

`timeout_sec` is set by the daemon (from ox config or the requesting agent). The adapter enforces
it — kills the worker process and sends a `subagent.failed` event if exceeded.

**Timeout class**: `background` — daemon does not wait. The initial response is just
acknowledgment; the real result comes via events.

---

### `subagent-status`

Poll the current state of a worker. Used by ox dashboard and `ox agent <id> workers`.

```json
{"id":11,"method":"subagent-status","params":{"worker_id":"w-OxA1b2-0001"}}
```

```json
{"id":11,"result":{
  "worker_id":    "w-OxA1b2-0001",
  "status":       "running",
  "started_at":   "2026-04-02T10:30:00Z",
  "elapsed_sec":  47,
  "output_lines": 312,
  "last_activity":"2026-04-02T10:30:47Z"
}}
```

Statuses: `starting` | `running` | `completed` | `failed` | `canceled` | `canceling` | `timed_out`

**Timeout class**: `fast`

---

### `cancel-subagent`

Request graceful cancellation of a running worker. The adapter sends SIGTERM to the agent
process, waits up to 10 seconds, then SIGKILL.

```json
{"id":12,"method":"cancel-subagent","params":{"worker_id":"w-OxA1b2-0001","reason":"user_requested"}}
```

```json
{"id":12,"result":{"worker_id":"w-OxA1b2-0001","status":"canceling"}}
```

A `subagent.failed` event with `exit_reason: "canceled"` follows when the process exits.

**Timeout class**: `fast` (acknowledgment only; cancellation is async)

---

## Push Events for Worker Progress

While a worker runs, the adapter pushes events over the same stdout pipe as entry events.

### `subagent.progress`

Incremental output from the running agent. Frequency is adapter-determined — the adapter
batches output and sends when meaningful (new file written, tool call completed, etc.).

```json
{
  "event":     "subagent.progress",
  "agent_id":  "r7f3a2-OxA1b2",
  "data": {
    "worker_id":    "w-OxA1b2-0001",
    "output_type":  "tool_use",
    "tool":         "Write",
    "description":  "Wrote src/auth/auth_test.go (342 lines)",
    "offset":       1024
  }
}
```

`output_type`: `tool_use` | `message` | `thinking` | `error`

The daemon surfaces these to the requesting session's context (prime output, live status).

### `subagent.completed`

Worker finished successfully.

```json
{
  "event":    "subagent.completed",
  "agent_id": "r7f3a2-OxA1b2",
  "data": {
    "worker_id":       "w-OxA1b2-0001",
    "exit_code":       0,
    "duration_sec":    183,
    "files_modified":  ["src/auth/auth_test.go", "src/auth/token_test.go"],
    "summary":         "Added 47 unit tests covering auth flows. All tests pass.",
    "session_file":    "/path/to/worker-session.jsonl",
    "final_offset":    49152
  }
}
```

`session_file` points to the worker's session JSONL — the daemon can index this as a regular
session recording. Workers are first-class sessions from the recording perspective.

### `subagent.failed`

Worker exited with an error.

```json
{
  "event":    "subagent.failed",
  "agent_id": "r7f3a2-OxA1b2",
  "data": {
    "worker_id":    "w-OxA1b2-0001",
    "exit_reason":  "error",
    "exit_code":    1,
    "duration_sec": 12,
    "error":        "Authentication failed: ANTHROPIC_API_KEY not set",
    "session_file": "/path/to/worker-session.jsonl"
  }
}
```

`exit_reason`: `error` | `canceled` | `timed_out` | `adapter_crash`

---

## Worker Session Recording

Worker sessions are recorded the same way as user-initiated sessions. The worker process writes
to a session JSONL file; the adapter already knows how to read it. On `spawn-subagent`, the
adapter opens a file watcher on the worker's session file and streams entries back as `subagent.progress`
events. On completion, `subagent.completed` includes `session_file` — the daemon indexes it as
a normal session recording.

This means **worker sessions are stored in the ledger** and are visible to teammates.
They're first-class sessions, just daemon-initiated rather than user-initiated.

---

## ox config: Worker Configuration

Users declare which adapters they want to use as workers and configure resource limits:

```yaml
# .sageox/config.yaml (or ~/.config/ox/config.yaml for user-level)

subagents:
  workers:
    - adapter:         claude-code
      model:           claude-sonnet-4-6
      max_concurrent:  2           # across all repos/sessions on this host
      timeout_sec:     1800        # 30 minutes max per worker
      enabled:         true

    - adapter:         gemini
      model:           gemini-2.0-pro
      max_concurrent:  1
      timeout_sec:     3600
      enabled:         false       # opt-in per-user

  # global limits
  max_total_workers: 4            # across all adapter types
  require_confirmation: true      # prompt before spawning (default: true)
```

Credentials are NOT specified here — the adapter owns credential discovery. The adapter's
`credential_hint` (from `info`) tells users where to configure them. ox does not handle API
keys for agent binaries.

`require_confirmation` controls whether an agent session can spawn a worker without prompting
the user. When true, `ox` surfaces a confirmation step before sending `spawn-subagent`. This
is a significant guardrail — autonomous agents spawning autonomous agents is high-blast-radius.

---

## Multi-Tenant Worker Isolation

In the shared daemon model:

- Workers are scoped to the session that requested them (`agent_id` prefix in `worker_id`)
- Team-scoped sessions only spawn workers within the same team context (`team_id` flows through)
- Worker count limits apply per-user (credential owner), not per-daemon
- The adapter enforces concurrency limits internally — the daemon enforces team/user quotas

A worker cannot access another team's repo_root. The daemon enforces this at the IPC layer
before sending `spawn-subagent` to the adapter.

---

## Adapter Responsibility Boundary

The adapter owns:
- Spawning and killing the agent subprocess
- Monitoring the agent process (stdout/stderr capture, exit code)
- Enforcing `timeout_sec`
- Streaming progress events
- Credential resolution for the agent (API key lookup, OAuth refresh)
- Worker concurrency limiting (up to `max_concurrent` in config)

The daemon owns:
- Deciding when to spawn a worker (policy, confirmation, quotas)
- Routing `spawn-subagent` to the correct adapter process
- Team and user isolation at the IPC boundary
- Storing worker session recordings in the ledger
- Surfacing worker status in `ox agent <id> workers` and the dashboard

ox core owns:
- The `ox config` UI for worker configuration
- Confirmation prompts before spawning
- Worker result surfacing to the requesting session's context

---

## Interaction With the Existing Session Model

When a user is in an active Claude Code session and that session triggers a worker:

```
User session OxA1b2 (claude-code, interactive)
  │
  └── spawns worker w-OxA1b2-0001 (claude-code, headless)
        │
        ├── adapter file-watches worker's session file
        ├── streams progress back to OxA1b2 context
        └── on completion: session file indexed, files committed
```

Both sessions use the same adapter process (`ox-adapter-claude-code --serve`). The adapter
maintains separate state for each `agent_id` (the user session) and each `worker_id`. The
daemon correlates workers back to their parent session for display.

This means a user can ask Claude Code "write tests for auth" → Claude Code tells ox to spawn
a worker → worker runs headless → progress appears in the user's session context → result
committed when done. The user's interactive session is not blocked.

---

## Future: Worker-to-Worker Spawning

A worker session could itself request another worker. The protocol supports this — `spawn-subagent`
includes `agent_id` which identifies the requesting session, whether interactive or a worker.

Depth limits and cycle detection are daemon-side policy. The adapter does not need to know
whether its parent is a user session or a worker session.
