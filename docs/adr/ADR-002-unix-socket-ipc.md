# ADR-002: Unix Domain Socket IPC

**Status**: Accepted
**Date**: 2026-04-02

## Context

ox has a long-running daemon (one per repo) that manages background sync, CodeDB indexing, whisper relay, and session watching. The CLI needs to communicate with this daemon from many independent processes: hook invocations fire on every tool call (10-50x/minute per agent), `ox status` polls state, heartbeats deliver credentials, and multiple agents in the same repo may run simultaneously.

This is a **many-to-one** problem. stdin/stdout is inherently 1:1 (parent-child process relationship). gRPC adds a protobuf toolchain dependency (violates the simplicity principle). HTTP over TCP requires port management and lacks OS-level access control.

## Decision

### Transport: Unix Domain Sockets

- **macOS/Linux**: `$XDG_RUNTIME_DIR/sageox/{workspace_id}.sock` with mode `0600` (owner-only)
- **Windows**: Named pipe `\\.\pipe\sageox-daemon` with SDDL restricting access to current user SID
- **Per-repo isolation**: Socket path derived from `SHA256(repo_id)[:8]`, so multiple repos get independent daemons
- **Discovery**: CLI computes socket path from `.sageox/config.json` repo_id. Fallback to daemon registry (`registry.json`) if workspace ID drifts across worktrees.

### Protocol: NDJSON (Newline-Delimited JSON)

One compact JSON object per line in both directions. No protobuf, no length-prefix framing.

```
Request:  {"type":"ping","workspace_id":"a1b2c3d4","payload":{...}}
Response: {"success":true,"data":{...}}
```

Chosen for debuggability — `echo '...' | socat` and `jq` work for manual testing.

### Message Classification

Messages are classified into two patterns based on whether the caller needs a response:

| Pattern | Timeout | Use Cases |
|---------|---------|-----------|
| **Request/Response** | 5s read/write | `ping`, `status`, `sync`, `doctor`, `checkout` |
| **Fire-and-Forget** | 50ms write-only | `heartbeat`, `telemetry`, `friction`, `session_finalize`, `murmur_*` |

Fire-and-forget messages never block the CLI. The 50ms write deadline means if the daemon's socket buffer is full, the message is silently dropped. This is intentional — heartbeats and telemetry are best-effort.

### Progress Streaming

Long operations (`sync`, `checkout`, `code_index`) use a hybrid: request/response with intermediate progress lines. The daemon streams NDJSON progress updates over the same connection before sending the final response.

### Concurrency

- Server accepts up to 100 concurrent connections (semaphore-gated)
- Each connection handled in a dedicated goroutine
- Excess connections rejected immediately (not queued)
- Connection activity tracked for daemon idle shutdown (1hr inactivity)

### Graceful Degradation

When the daemon is unavailable:
- Fire-and-forget operations silently drop (no error to user)
- Observation operations (`status`, `ping`) return error, CLI shows "daemon not running"
- Critical-path exception: `checkout` falls back to direct `git clone`
- **IPC is never required for CLI correctness** — the daemon is an optimization, not a dependency

## Consequences

**Benefits**:
- OS filesystem permissions provide authentication — no token exchange needed
- Many concurrent CLI instances (hooks, status, heartbeats) handled naturally
- NDJSON is trivially debuggable and testable
- Fire-and-forget pattern keeps hook latency <50ms even under load
- Per-repo socket isolation prevents cross-project interference

**Tradeoffs**:
- Unix domain sockets don't work across machines (no remote daemon). Acceptable — daemon is always colocated with the repo.
- 1MB message size limit (DoS protection) constrains payload size. No current message approaches this.
- No multiplexing — each request is one connection. Acceptable at current scale (hooks fire sequentially per agent).
- Socket file can be orphaned if daemon crashes without cleanup. Registry and `ox doctor` handle this.
