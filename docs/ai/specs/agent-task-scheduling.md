<!-- doc-audience: ai -->

# Agent Task Scheduling

Internal mechanism for scheduling units of work that the **next available AI
coworker** picks up and executes — typically as a fresh-context subagent. It
replaces ad-hoc one-off signals (the `.needs-doctor-agent` file drop) and the
daemon's `claude -p` fork-out for anti-entropy work, which billed against a
separate account instead of riding on the developer's live session.

This is an **internal** feature. It is NOT a replacement for beads (`bd`),
which tracks human-facing project work. Agent tasks are ephemeral, machine
-scheduled, local-only chores executed on behalf of the developer's session.

## Why

- The daemon (or other internal producers) sometimes discovers work that
  requires an LLM: finalizing/summarizing a stale recording, running doctor
  repairs, anti-entropy. Today it either drops a marker file or forks
  `claude -p`. Forking is billed separately and loses the warm session; the
  marker file is a single-purpose hack.
- A durable, priority-ordered task queue lets any producer schedule work and
  lets the live coding agent execute it inside the session the developer is
  already paying for — ideally dispatched to a subagent so it does not pollute
  the main context window.

## Storage

One **shared, project-local** queue per repo:

```
.sageox/agent_tasks/agent_tasks.jsonl   # gitignored, ephemeral, local-only
.sageox/agent_tasks/agent_tasks.jsonl.lock
```

- Shared (not per-user): the whole point is "next available agent" — whoever
  primes next can pick it up. Contrast with `.sageox/agent_instances/<user>/`,
  which is per-user by design.
- JSONL, append-only, last-write-wins by `id` on read — identical concurrency
  model to `internal/agentinstance`. `gofrs/flock` guards writes.
- Gitignored via `.sageox/.gitignore` (`agent_tasks/`).

## Task model

```jsonc
{
  "id": "0191...",            // UUIDv7 (time-sortable)
  "title": "Finalize stale session",
  "body": "Run `ox agent <id> session recover` ...",
  "kind": "doctor",           // category: doctor | session-finalize | anti-entropy | custom
  "priority": 20,             // lower = higher priority (matches agentwork.WorkItem)
  "status": "ready",          // ready | in_progress | completed | canceled
  "source": "daemon",         // producer: daemon | doctor | cli
  "target_agent": "claude",   // optional: restrict to an agent type; "" = any
  "dedup_key": "doctor-agent",// optional: at most one active task per key
  "payload": { "...": "..." },
  "created_at": "...",
  "expires_at": "...",        // optional; zero = never. Dropped once past.

  // lease (set only while in_progress)
  "claimed_by_agent_id": "Oxa7b3",
  "claimed_by_pid": 12345,
  "claimed_host": "hostname",
  "claimed_at": "...",
  "lease_expires_at": "...",  // reverts to ready if not completed in time
  "attempts": 1,

  // terminal
  "completed_at": "...",
  "result": "summarized 3 sessions"
}
```

### Lifecycle

```
        ┌─────────── claim (next) ───────────┐
        ▼                                     │
     ready ──claim──▶ in_progress ──done────▶ completed
        ▲                  │      ──cancel──▶ canceled
        │                  │
        └── reclaim ◀──────┘  (lease expired OR claiming PID dead on same host)
```

- **ready → in_progress**: `Claim` atomically pops the highest-priority ready
  task whose `target_agent` matches (empty matches any), stamps the claiming
  agent id, PID, host, and a lease deadline.
- **reclaim**: on every read (`List`/`Claim`) the store reconciles. An
  `in_progress` task reverts to `ready` (incrementing `attempts`) when its
  lease expired, OR when `claimed_host` is this host and `claimed_by_pid` is
  no longer alive (`proc.IsAlive`). Cross-host claims can only be reclaimed by
  lease expiry — a PID is meaningless on another machine.
- **terminal**: `completed`/`canceled` are kept briefly (retention window) so
  `list` can show recent outcomes, then pruned. There is no separate "closed"
  state.

## Command surface (`ox agent ... tasks`)

Agent-facing (require an agent id, JSON by default, `--text` for humans):

| Command | Effect |
|---------|--------|
| `ox agent <id> tasks list` | List ready (+ in-progress) tasks, priority-sorted |
| `ox agent <id> tasks next` | Atomically claim the top ready task → `in_progress` |
| `ox agent <id> tasks done <task-id> [--result ...]` | Mark `completed` |
| `ox agent <id> tasks cancel <task-id> [--reason ...]` | Mark `canceled` |
| `ox agent <id> tasks extend <task-id>` | Extend the lease on a long task |

Producer-facing (no agent id; hidden — for the daemon, scripts, tests):

| Command | Effect |
|---------|--------|
| `ox agent tasks add --title ... --kind ... [--priority N] [--body ...] [--target claude] [--expires 2h] [--dedup-key K] [--source S]` | Enqueue a task |
| `ox agent tasks list` | Inspect the queue (debugging) |

There is intentionally **no user-facing create command** on the top level —
tasks are scheduled by internal producers, not authored by humans. Creation
also has a Go API: `agenttask.Enqueue(projectRoot, Task{...})`.

## Surfacing

Tasks surface through the **UserPromptSubmit hook only** (`handlePrompt`),
the single reliable channel into Claude's context mid-session. On each prompt,
`emitAgentTasks` checks for ready tasks targeting this agent and emits a
throttled `<system-reminder>` block instructing the agent to dispatch each
task to a **subagent with a fresh context**, then mark it done.

Throttling (token conservation): a per-agent cursor in
`.sageox/cache/agent_tasks_seen/<agentID>.json` records the signature (hash of
sorted ready task ids) last surfaced. The block is re-emitted **only when the
ready set changes** — a new task, or a completed/claimed one. An unchanged
pending queue is never re-injected turn after turn (that would burn the user's
tokens on identical context), and an idle queue costs zero context. The agent
can always pull on demand with `ox agent <id> tasks list`.

## Daemon producer

The daemon is the primary producer. Within `internal/daemon/agentwork`, on the
existing doctor-interval timer (and `ForceDetect`), `produceAgentTasks` runs
**independent of `agent_worker.enabled`** — the whole point is that even when
the daemon cannot (or should not) fork `claude -p`, it can still hand work to
the live agent:

- **Doctor bridge** (`produceDoctorTask`): if the `.needs-doctor-agent` marker
  is present, enqueue a deduped `doctor` task (dedup key `doctor-agent`) telling
  the live agent to finalize incomplete sessions.
- **Session finalize** (`produceFinalizeTasks`): runs **only when no local LLM
  worker is authed/enabled** (`!isEffectivelyEnabled`). This is the direct
  replacement for the separately-billed `claude -p` anti-entropy fork: it asks
  the registered `SessionFinalizeHandler` to detect stale recordings needing
  summaries and enqueues a per-session `session-finalize` task (deduped
  `session-finalize:<name>`, priority 30, capped at 25/cycle). When a worker IS
  available the normal queue forks it (delegated mode, ADR-016) and no task is
  produced — avoiding duplicate work. When a worker is available but finalize is
  explicitly disabled, the user opted out, so no task is produced either.

`agentwork` cannot import `internal/doctor` (would cycle:
`daemon → agentwork → doctor → daemon`), so the doctor producer checks the
marker via `os.Stat` on the canonical path.

## Status surfacing

`ox status` and `ox agent list` render a one-line summary of the queue
(`N ready, M in progress`) when non-empty, pointing at
`ox agent <id> tasks list` for detail. Reading the queue reconciles stale
leases as a side effect. An empty queue renders nothing.

## Reaper / doctor check

`ox doctor` gains a check that surfaces tasks stuck `in_progress` past their
lease and tasks that have exhausted `attempts`, and prunes expired/terminal
rows (`--fix`). The store self-heals on read; the doctor check is the
human-visible backstop.

## Non-goals

- Not a distributed queue. Single repo, local filesystem, best-effort.
- Not durable across `git clone` — `.sageox/agent_tasks/` is gitignored.
- Not a beads replacement. No human workflow, no dependencies, no graph.
