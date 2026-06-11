# IPC Architecture

> **Audience**: AI agents and developers working on daemon/CLI communication

## Philosophy: The Independent Daemon

The ox daemon is designed to be **autonomous and self-sufficient**. Once started, it should accomplish its goals (syncing ledgers, team contexts, and maintaining repo health) **without any CLI interaction**.

### Core Principles

1. **IPC is Never Required** - The daemon must function correctly even if every IPC call fails. The CLI uses IPC to observe, nudge, and optimize—but never to control essential behavior.

2. **Fire-and-Forget by Default** - Most CLI→Daemon communication is advisory. Heartbeats, telemetry, and friction events are "nice to have" but the daemon continues regardless.

3. **Graceful Degradation** - If IPC breaks, the daemon keeps syncing. If credentials expire, it waits. If the socket disappears, it recreates it. No single failure mode should stop the daemon's mission.

4. **Self-Managing Lifecycle** - The daemon starts when needed (first `ox` command), runs independently, and shuts itself down after inactivity (1 hour). No external orchestration required.

### What This Means in Practice

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                         Daemon Independence Model                               │
├─────────────────────────────────────────────────────────────────────────────────┤
│                                                                                 │
│   CLI Commands                          Daemon                                  │
│   ────────────                          ──────                                  │
│                                                                                 │
│   ox init ──────────────────────────▶  Starts daemon (if not running)          │
│                                         │                                       │
│   ox session start ─── heartbeat ───▶   │  (advisory: "I'm active")            │
│                        (fire-forget)    │                                       │
│                                         │                                       │
│   ox status ─────── status query ───▶   │  (observation only)                  │
│              ◀────── status data ────   │                                       │
│                                         │                                       │
│   [nothing] ────────────────────────    │  Daemon syncs on timer               │
│                                         │  (every 5 min, independent of CLI)   │
│                                         │                                       │
│   [CLI crashes] ────────────────────    │  Daemon keeps running                │
│                                         │  (still syncs, still healthy)        │
│                                         │                                       │
│   [1 hour no activity] ─────────────    │  Daemon shuts itself down            │
│                                         │  (next ox command restarts it)       │
│                                                                                 │
└─────────────────────────────────────────────────────────────────────────────────┘
```

### The Daemon's Mission

The daemon exists to:
1. **Sync ledgers** - Pull session history from remote, push local sessions
2. **Sync team contexts** - Keep team knowledge bases up to date
3. **Report issues** - Surface problems when CLI asks (but don't block on it)

Everything else (telemetry, friction tracking, instance monitoring) is optimization, not core functionality.

---

## IPC Mechanism

**Technology**: Unix Domain Sockets (macOS/Linux) / Named Pipes (Windows)
**Protocol**: Line-delimited JSON
**Message Size Limit**: 1MB
**Connection Limit**: 100 concurrent

### Socket Locations

| Platform | Path |
|----------|------|
| Unix | `$XDG_RUNTIME_DIR/sageox/<workspace_id>.sock` |
| Windows | `\\.\pipe\sageox-daemon` |

### Workspace Isolation

Each workspace (repo) gets its own daemon instance:

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                         Multi-Workspace Architecture                            │
├─────────────────────────────────────────────────────────────────────────────────┤
│                                                                                 │
│   ~/project-a/                    ~/project-b/                                  │
│        │                               │                                        │
│        │ WorkspaceID()                 │ WorkspaceID()                          │
│        ▼                               ▼                                        │
│   SHA256("~/project-a")[:8]      SHA256("~/project-b")[:8]                      │
│        = "a1b2c3d4"                    = "e5f6g7h8"                              │
│        │                               │                                        │
│        ▼                               ▼                                        │
│   ┌────────────────────┐         ┌────────────────────┐                         │
│   │ $XDG_RUNTIME_DIR/  │         │ $XDG_RUNTIME_DIR/  │                         │
│   │ sageox/            │         │ sageox/            │                         │
│   │ a1b2c3d4.sock      │         │ e5f6g7h8.sock      │                         │
│   └─────────┬──────────┘         └─────────┬──────────┘                         │
│             │                              │                                    │
│             ▼                              ▼                                    │
│   ┌────────────────────┐         ┌────────────────────┐                         │
│   │   Daemon Instance  │         │   Daemon Instance  │                         │
│   │   (project-a)      │         │   (project-b)      │                         │
│   └────────────────────┘         └────────────────────┘                         │
│                                                                                 │
└─────────────────────────────────────────────────────────────────────────────────┘
```

---

## Message Protocol

### Request Structure

```json
{
  "type": "<message_type>",
  "workspace_id": "<8-char hash>",
  "payload": { ... }
}
```

### Response Structure

```json
{
  "success": true|false,
  "error": "message if failed",
  "data": { ... }
}
```

### Progress Response (for long operations)

```json
{
  "progress": {
    "stage": "cloning",
    "percent": 50,
    "message": "Cloning repository..."
  }
}
```

---

## API Reference

### Classification Legend

| Tag | Meaning |
|-----|---------|
| 🔴 **REQUIRED** | Daemon cannot function without this succeeding |
| 🔶 **CRITICAL+FALLBACK** | Critical for product, but CLI has fallback if IPC fails |
| 🟡 **OPTIMIZATION** | Improves behavior but daemon works without it |
| 🟢 **OBSERVATION** | Read-only query, no impact on daemon operation |
| ⚫ **FIRE-FORGET** | CLI doesn't wait for response, failure is silent |

---

### All IPC Message Types

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                     OBSERVATION APIs (Read-Only, Can Fail)                      │
│                              🟢 No Impact on Daemon                             │
├─────────────────────────────────────────────────────────────────────────────────┤
│                                                                                 │
│  ┌─────────────┐        {"type":"ping"}         ┌─────────────┐                 │
│  │   CLI       │ ───────────────────────────▶   │   Daemon    │                 │
│  │             │ ◀───────────────────────────   │             │                 │
│  └─────────────┘        {"data":"pong"}         └─────────────┘                 │
│  Purpose: Health check for CLI display                                         │
│  If fails: CLI shows "daemon not running" - daemon unaffected                  │
│                                                                                 │
│  ┌─────────────┐        {"type":"version"}      ┌─────────────┐                 │
│  │   CLI       │ ───────────────────────────▶   │   Daemon    │                 │
│  │             │ ◀───────────────────────────   │             │                 │
│  └─────────────┘        {"data":"0.13.0"}       └─────────────┘                 │
│  Purpose: Version display in ox status                                         │
│  If fails: CLI shows "unknown" - daemon unaffected                             │
│                                                                                 │
│  ┌─────────────┐        {"type":"status"}       ┌─────────────┐                 │
│  │   CLI       │ ───────────────────────────▶   │   Daemon    │                 │
│  │             │ ◀───────────────────────────   │             │                 │
│  └─────────────┘      {running, uptime,         └─────────────┘                 │
│                        workspaces, ...}                                         │
│  Purpose: ox status command display                                            │
│  If fails: CLI shows error message - daemon unaffected                         │
│                                                                                 │
│  ┌─────────────┐        {"type":"instances"}    ┌─────────────┐                 │
│  │   CLI       │ ───────────────────────────▶   │   Daemon    │                 │
│  │             │ ◀───────────────────────────   │             │                 │
│  └─────────────┘      {instances: [...]}        └─────────────┘                 │
│  Purpose: Show connected agents in ox agent list                               │
│  If fails: CLI shows empty list - daemon unaffected                            │
│                                                                                 │
│  ┌─────────────┐        {"type":"sync_history"} ┌─────────────┐                 │
│  │   CLI       │ ───────────────────────────▶   │   Daemon    │                 │
│  │             │ ◀───────────────────────────   │             │                 │
│  └─────────────┘      {events: [...]}           └─────────────┘                 │
│  Purpose: Show recent sync activity                                            │
│  If fails: CLI shows no history - daemon unaffected                            │
│                                                                                 │
│  ┌─────────────┐        {"type":"get_errors"}   ┌─────────────┐                 │
│  │   CLI       │ ───────────────────────────▶   │   Daemon    │                 │
│  │             │ ◀───────────────────────────   │             │                 │
│  └─────────────┘      {errors: [...]}           └─────────────┘                 │
│  Purpose: Retrieve daemon errors for display                                   │
│  If fails: CLI shows no errors - daemon unaffected                             │
│                                                                                 │
│  ┌─────────────┐        {"type":"doctor"}       ┌─────────────┐                 │
│  │   CLI       │ ───────────────────────────▶   │   Daemon    │                 │
│  │             │ ◀───────────────────────────   │             │                 │
│  └─────────────┘      {checks, issues}          └─────────────┘                 │
│  Purpose: Health check display                                                 │
│  If fails: CLI shows "cannot reach daemon" - daemon unaffected                 │
│                                                                                 │
└─────────────────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────────────────┐
│                    FIRE-AND-FORGET APIs (No Response Expected)                  │
│                         ⚫ Silent Failure, No Blocking                          │
├─────────────────────────────────────────────────────────────────────────────────┤
│                                                                                 │
│  ┌─────────────┐        {"type":"heartbeat",    ┌─────────────┐                 │
│  │   CLI       │ ──── payload:{agent_id,    ──▶ │   Daemon    │                 │
│  │  (async)    │        credentials, ...}}      │             │                 │
│  └─────────────┘                                └─────────────┘                 │
│       │                     (no response - 50ms timeout)                        │
│       └── goroutine, never blocks CLI                                           │
│  Purpose: Activity signal + credential refresh                                  │
│  If fails: Daemon uses cached credentials, continues syncing                   │
│            Eventually shuts down after 1hr inactivity (next ox restarts it)    │
│                                                                                 │
│  ┌─────────────┐        {"type":"telemetry",    ┌─────────────┐                 │
│  │   CLI       │ ──── payload:{event,props}} ─▶ │   Daemon    │                 │
│  │  (async)    │                                │             │                 │
│  └─────────────┘                                └─────────────┘                 │
│       │                     (no response - 50ms timeout)                        │
│       └── goroutine, never blocks CLI                                           │
│  Purpose: Product analytics                                                    │
│  If fails: Analytics gap - no operational impact                               │
│                                                                                 │
│  ┌─────────────┐        {"type":"friction",     ┌─────────────┐                 │
│  │   CLI       │ ──── payload:{kind,cmd,    ──▶ │   Daemon    │                 │
│  │  (async)    │        error_msg, ...}}        │             │                 │
│  └─────────────┘                                └─────────────┘                 │
│       │                     (no response - 50ms timeout)                        │
│       └── goroutine, never blocks CLI                                           │
│  Purpose: UX friction tracking                                                 │
│  If fails: Friction data lost - no operational impact                          │
│                                                                                 │
└─────────────────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────────────────┐
│                      OPTIMIZATION APIs (Nudge Daemon Behavior)                  │
│                    🟡 Improves UX But Daemon Works Without Them                 │
├─────────────────────────────────────────────────────────────────────────────────┤
│                                                                                 │
│  ┌─────────────┐        {"type":"sync"}         ┌─────────────┐                 │
│  │   CLI       │ ───────────────────────────▶   │   Daemon    │                 │
│  │             │ ◀─ {progress updates} ───────  │             │                 │
│  │             │ ◀─ {success: true} ──────────  │             │                 │
│  └─────────────┘        (30s timeout)           └─────────────┘                 │
│  Purpose: Trigger immediate sync instead of waiting for timer                  │
│  If fails: Daemon syncs on next timer tick (within 5 min) - minor delay        │
│  NOTE: The IPC CALL is optional. The sync OPERATION inside daemon is CRITICAL. │
│                                                                                 │
│  ┌─────────────┐        {"type":"team_sync"}    ┌─────────────┐                 │
│  │   CLI       │ ───────────────────────────▶   │   Daemon    │                 │
│  │             │ ◀─ {progress updates} ───────  │             │                 │
│  │             │ ◀─ {success: true} ──────────  │             │                 │
│  └─────────────┘        (60s timeout)           └─────────────┘                 │
│  Purpose: Trigger immediate team context sync                                  │
│  If fails: Daemon syncs on next timer tick - minor delay                       │
│  NOTE: The IPC CALL is optional. The sync OPERATION inside daemon is CRITICAL. │
│                                                                                 │
│  ┌─────────────┐        {"type":"checkout",     ┌─────────────┐                 │
│  │   CLI       │ ──── payload:{clone_url,   ──▶ │   Daemon    │                 │
│  │             │        repo_type}}             │             │                 │
│  │             │ ◀─ {progress updates} ───────  │             │                 │
│  │             │ ◀─ {success, path} ──────────  │             │                 │
│  └─────────────┘        (60s timeout)           └─────────────┘                 │
│  Purpose: Clone a new repo (ledger or team context)                            │
│  🔶 CRITICAL PATH: If fails, CLI falls back to direct git clone               │
│     (see cmd/ox/doctor_git_repos.go:cloneViaDaemon)                            │
│                                                                                 │
│  ┌─────────────┐        {"type":"mark_errors",  ┌─────────────┐                 │
│  │   CLI       │ ──── payload:{ids:[...]}} ──▶  │   Daemon    │                 │
│  │             │ ◀───────────────────────────   │             │                 │
│  └─────────────┘        {success: true}         └─────────────┘                 │
│  Purpose: Mark errors as viewed (UI cleanup)                                   │
│  If fails: Errors keep showing - cosmetic only                                 │
│                                                                                 │
└─────────────────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────────────────┐
│                         CONTROL APIs (Lifecycle Management)                     │
│                      🔴 Required For Specific User Actions                      │
├─────────────────────────────────────────────────────────────────────────────────┤
│                                                                                 │
│  ┌─────────────┐        {"type":"stop"}         ┌─────────────┐                 │
│  │   CLI       │ ───────────────────────────▶   │   Daemon    │                 │
│  │             │ ◀───────────────────────────   │   (shuts    │                 │
│  └─────────────┘        {success: true}         │    down)    │                 │
│                                                 └─────────────┘                 │
│  Purpose: User explicitly requests daemon shutdown                             │
│  If fails: Daemon keeps running - user may need to kill -9                     │
│  Note: This is the ONLY truly "required" API, and only when user               │
│        explicitly wants to stop the daemon. Daemon itself doesn't need it.     │
│                                                                                 │
└─────────────────────────────────────────────────────────────────────────────────┘
```

---

## API Classification Summary

| Message Type | Classification | Impact if Fails |
|--------------|----------------|-----------------|
| `ping` | 🟢 Observation | CLI shows "not running" |
| `version` | 🟢 Observation | CLI shows "unknown" |
| `status` | 🟢 Observation | CLI shows error |
| `instances` | 🟢 Observation | CLI shows empty list |
| `sync_history` | 🟢 Observation | CLI shows no history |
| `get_errors` | 🟢 Observation | CLI shows no errors |
| `doctor` | 🟢 Observation | CLI shows "cannot reach" |
| `heartbeat` | ⚫ Fire-Forget | Daemon uses cached creds, shuts down after 1hr |
| `telemetry` | ⚫ Fire-Forget | Analytics gap only |
| `friction` | ⚫ Fire-Forget | UX data lost only |
| `sync` | 🟡 Optimization | IPC call is optional; daemon syncs on timer (≤5 min). The sync *operation* itself is critical. |
| `team_sync` | 🟡 Optimization | IPC call is optional; daemon syncs on timer. The sync *operation* itself is critical. |
| `checkout` | 🔶 Critical+Fallback | CLI falls back to direct clone if IPC fails |
| `mark_errors` | 🟡 Optimization | Errors keep showing (cosmetic) |
| `stop` | 🔴 Required* | User can't gracefully stop daemon |

*Only "required" in the sense that the user explicitly requested it. The daemon itself never requires this to function.

---

## Timeout Strategy

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                              Timeout Hierarchy                                  │
├─────────────────────────────────────────────────────────────────────────────────┤
│                                                                                 │
│   Fire-and-Forget     │  50ms   │  heartbeat, telemetry, friction               │
│   ════════════════════════════════════════════════════════════════════          │
│   Rationale: Never block CLI. If daemon is slow, skip it.                       │
│                                                                                 │
│   Fast Commands       │  500ms  │  ping, status, instances, version, doctor     │
│   ════════════════════════════════════════════════════════════════════          │
│   Rationale: Localhost should respond <100ms. 500ms catches slow systems.       │
│                                                                                 │
│   Sync Operations     │  30s    │  sync (repo sync)                             │
│   ════════════════════════════════════════════════════════════════════          │
│   Rationale: Git operations can be slow. User expects to wait.                  │
│                                                                                 │
│   Network-bound       │  60s    │  team_sync, checkout/clone                    │
│   ════════════════════════════════════════════════════════════════════          │
│   Rationale: Clone operations are network-bound. Progress shown.                │
│                                                                                 │
└─────────────────────────────────────────────────────────────────────────────────┘
```

---

## Detailed Payload Structures

### Heartbeat (Fire-and-Forget)

```json
{
  "type": "heartbeat",
  "workspace_id": "a1b2c3d4",
  "payload": {
    "repo_path": "/home/user/project",
    "workspace_id": "a1b2c3d4",
    "agent_id": "Oxa7b3",
    "team_ids": ["team-123", "team-456"],
    "timestamp": "2025-02-01T10:15:30Z",
    "cli_version": "0.13.0",
    "credentials": {
      "token": "glpat-...",
      "server_url": "https://git.sageox.io",
      "expires_at": "2025-03-01T00:00:00Z",
      "auth_token": "oauth-...",
      "user_email": "user@example.com",
      "user_id": "user-456"
    }
  }
}
```

**If this fails**: Daemon uses previously cached credentials. If credentials expire and no new heartbeat arrives, daemon waits until next successful heartbeat. Sync continues with valid credentials; pauses with expired ones.

### Status Response

```json
{
  "success": true,
  "data": {
    "running": true,
    "pid": 12345,
    "version": "0.13.0",
    "uptime": "5m30s",
    "ledger_path": "/path/to/.sageox/ledger",
    "last_sync": "2025-02-01T10:15:30Z",
    "sync_interval_read": "5m",
    "workspaces": {
      "ledger": [{
        "id": "default",
        "type": "ledger",
        "path": "/path/to/.sageox/ledger",
        "clone_url": "https://git.sageox.io/...",
        "exists": true,
        "last_sync": "2025-02-01T10:15:30Z"
      }],
      "team-context": [{
        "id": "team-123",
        "team_name": "Engineering",
        "exists": true,
        "last_sync": "2025-02-01T09:00:00Z"
      }]
    },
    "authenticated_user": {
      "email": "user@example.com",
      "user_id": "user-456"
    },
    "unviewed_error_count": 0,
    "needs_help": false,
    "issues": []
  }
}
```

### Checkout Request (Optimization)

```json
{
  "type": "checkout",
  "workspace_id": "a1b2c3d4",
  "payload": {
    "repo_path": "/home/user/.sageox/teams/team-123",
    "clone_url": "https://git.sageox.io/teams/team-123.git",
    "repo_type": "team_context"
  }
}
```

**Progress updates (streamed)**:
```json
{"progress":{"stage":"connecting","percent":10,"message":"Connecting..."}}
{"progress":{"stage":"cloning","percent":50,"message":"Cloning repo..."}}
{"progress":{"stage":"verifying","percent":90,"message":"Verifying..."}}
```

**Final response**:
```json
{
  "success": true,
  "data": {
    "path": "/home/user/.sageox/teams/team-123",
    "already_exists": false,
    "cloned": true
  }
}
```

**If this fails**: Daemon's anti-entropy process will detect the missing repo and clone it on next cycle. User experiences delay, not failure.

### Friction Event (Fire-and-Forget)

```json
{
  "type": "friction",
  "payload": {
    "ts": "2025-02-01T10:15:30Z",
    "kind": "unknown-command",
    "command": "ox",
    "subcommand": "deploy",
    "actor": "agent",
    "agent_type": "claude-code",
    "path_bucket": "repo",
    "input": "ox deploy",
    "error_msg": "unknown command"
  }
}
```

**If this fails**: UX friction data is lost. No operational impact. Product team has gap in analytics.

---

## Security Model

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                            Security Characteristics                             │
├─────────────────────────────────────────────────────────────────────────────────┤
│                                                                                 │
│  Access Control:                                                                │
│  ┌───────────────────────────────────────────────────────────────────────┐      │
│  │ Unix:    Socket mode 0600, parent dir 0700 (owner-only)               │      │
│  │ Windows: SDDL with current user SID only                              │      │
│  │ No authentication layer - OS filesystem/ACL is the auth              │      │
│  └───────────────────────────────────────────────────────────────────────┘      │
│                                                                                 │
│  DoS Protections:                                                               │
│  ┌───────────────────────────────────────────────────────────────────────┐      │
│  │ • Max message size: 1MB (LimitReader)                                 │      │
│  │ • Max concurrent connections: 100 (semaphore)                         │      │
│  │ • Read timeout: 5s per message                                        │      │
│  │ • Write deadline: 5s for responses                                    │      │
│  └───────────────────────────────────────────────────────────────────────┘      │
│                                                                                 │
│  Credential Flow:                                                               │
│  ┌───────────────────────────────────────────────────────────────────────┐      │
│  │ CLI login ──▶ Credentials saved locally                               │      │
│  │            ──▶ Passed in heartbeats to daemon                         │      │
│  │ Daemon caches credentials, continues with cached if heartbeat fails   │      │
│  │ Token expiry tracked (expires_at field)                               │      │
│  └───────────────────────────────────────────────────────────────────────┘      │
│                                                                                 │
└─────────────────────────────────────────────────────────────────────────────────┘
```

---

## Key Files

| Component | Location |
|-----------|----------|
| Core IPC | `internal/daemon/ipc.go` |
| Unix sockets | `internal/daemon/ipc_unix.go` |
| Windows pipes | `internal/daemon/ipc_windows.go` |
| Daemon config | `internal/daemon/config.go` |
| CLI heartbeat | `cmd/ox/heartbeat.go` |
| Telemetry client | `internal/telemetry/client.go` |
| Tests | `internal/daemon/ipc_test.go` |

---

## Design Decisions

| Decision | Rationale |
|----------|-----------|
| Unix sockets + named pipes | No external dependencies, OS-native, fast |
| Line-delimited JSON | Human-readable, streaming-friendly, grepable |
| Fire-and-forget for non-critical | CLI remains responsive (never waits for daemon) |
| Progress streaming | Long ops can show real-time feedback without blocking |
| One socket per workspace | Supports multi-workspace without coordination |
| 500ms default timeout | Localhost should respond <100ms, 500ms is safe margin |
| No connection pooling | Overhead negligible, keeps code simple |
| Credentials in heartbeat | Daemon doesn't need filesystem access for tokens |
| 1-hour inactivity shutdown | Self-managing lifecycle, no zombie daemons |

---

## Anti-Patterns to Avoid

### ❌ Making CLI operations depend on IPC success

```go
// WRONG: CLI fails if daemon doesn't respond
func DoSomething() error {
    status, err := daemon.Status()
    if err != nil {
        return fmt.Errorf("daemon not available: %w", err)  // Blocks user!
    }
    // ... continue
}

// RIGHT: CLI works regardless, daemon enhances
func DoSomething() error {
    // Do the core work first
    result := doActualWork()

    // Try to notify daemon, but don't block on failure
    go func() {
        _ = daemon.Heartbeat(...)  // Fire-and-forget
    }()

    return result
}
```

### ❌ Requiring sync before operations

```go
// WRONG: Blocks on sync
func StartSession() error {
    if err := daemon.SyncWithProgress(...); err != nil {
        return err  // User can't start session!
    }
    // ... continue
}

// RIGHT: Start immediately, sync is optimization
func StartSession() error {
    // Start the session now
    session := createSession()

    // Request sync in background (optimization)
    go func() {
        _ = daemon.RequestSync()  // Nice to have
    }()

    return nil
}
```

### ❌ Blocking on telemetry/analytics

```go
// WRONG: Waiting for telemetry
func RunCommand() error {
    result := doWork()
    if err := daemon.SendTelemetry(...); err != nil {
        log.Warn("telemetry failed")  // Still blocking!
    }
    return result
}

// RIGHT: Fire-and-forget
func RunCommand() error {
    result := doWork()
    go daemon.SendTelemetry(...)  // Async, no waiting
    return result
}
```

---

## Current Conformance Status

This section documents how well the current codebase adheres to the daemon independence philosophy.

### ✅ Correctly Implemented (Fire-and-Forget)

| Pattern | Location | Status |
|---------|----------|--------|
| Heartbeat | `cmd/ox/heartbeat.go:18-114` | Background goroutine, 50ms timeout, silently ignores errors |
| Friction events | `cmd/ox/friction.go:107-139` | Background goroutine, 50ms timeout, fire-and-forget |
| Telemetry | `internal/telemetry/client.go` | Async `SendToDaemon()`, never blocks CLI |
| Doctor background | `cmd/ox/doctor.go:140-146` | Runs in goroutine, doesn't wait |
| Agent health | `cmd/ox/agent.go:330-369` | Uses `TryConnect()`, gracefully skips if unavailable |
| Status display | `cmd/ox/status.go:1247-1257` | Uses `TryConnectOrDirect()`, omits if unavailable |

### ⚠️ Intentional Design Decisions (Documented)

| Pattern | Location | Notes |
|---------|----------|-------|
| `ox sync` requires daemon | `cmd/ox/sync.go` | Pull operations delegated to daemon by architecture. User must restart daemon if it crashes. This is intentional per the "Daemon-CLI Git Operations Split" in CLAUDE.md. |

### ❌ Known Violations (Need Future Work)

#### 1. Clone operations require daemon

**Location**: `cmd/ox/doctor_git_repos.go:1281-1284`

```go
func cloneViaDaemon(cloneURL, targetPath, repoType string) error {
    if !daemon.IsRunning() {
        return fmt.Errorf("daemon not running - start with 'ox daemon start' first")
    }
    // ...
}
```

**Impact**: `ox doctor --fix` cannot clone repos if daemon isn't running, blocking project initialization.

**Affected callers**:
- `doctor_git_repos.go:1169` - Ledger clone
- `doctor_git_repos.go:1210` - Team context clone
- `doctor_git_repos.go:1379` - `fixMissingRepo()` issue resolution

**Recommended fix**: Add fallback to direct `git clone` when daemon unavailable, or auto-start daemon.

#### 2. `ox sync` errors on daemon unavailability

**Location**: `cmd/ox/sync.go:92-103`

```go
if !daemon.IsRunning() {
    // ... print error/hint ...
    return fmt.Errorf("daemon not running")
}
```

**Impact**: Users cannot trigger manual sync if daemon crashes.

**Recommended fix**: Either auto-start daemon, or provide advisory message + continue with degraded functionality.

---

## Future Improvements

### Priority 1: User-Blocking Issues

1. **Add fallback clone path** - `cloneViaDaemon()` should fall back to direct `git clone` if daemon unavailable
2. **Auto-start daemon in sync** - `ox sync` could start daemon if not running instead of erroring

### Priority 2: Robustness

1. **Add `--direct` flag to sync** - Allow daemon-independent pull for troubleshooting
2. **Clone retry with fallback** - After N daemon retries, attempt direct clone

### Priority 3: Documentation

1. **Document sync architecture** - Make clear that `ox sync` requires daemon (intentional)
2. **Add troubleshooting guide** - What to do when daemon is unavailable
