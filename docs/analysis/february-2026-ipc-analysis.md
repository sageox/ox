# IPC Reliability Analysis - February 2026

> **Audience**: AI agents and developers working on ox IPC improvements

## Executive Summary

Analysis of IPC patterns and December 2025 industry best practices, with specific recommendations for ox daemon communication reliability.

## Current State Assessment

ox already follows many best practices:
- Unix domain sockets (macOS/Linux) / Named pipes (Windows)
- Line-delimited JSON (NDJSON) - debuggable with `cat`, `jq`, `socat`
- Fire-and-forget for non-critical operations
- Fallback patterns for critical paths
- Timeout hierarchy (50ms → 500ms → 30s → 60s)

### Strengths

| Pattern | Implementation | Status |
|---------|----------------|--------|
| NDJSON protocol | `internal/daemon/ipc.go` | Correct |
| Fire-and-forget | heartbeat, telemetry, friction | Correct |
| Progress streaming | sync, clone operations | Correct |
| Workspace isolation | Per-repo sockets | Correct |
| Security (mode 0600) | Socket permissions | Correct |

### Gaps Identified

1. **Hung daemon detection** - Socket exists but process unresponsive
2. **Timeout consistency** - Some code paths may lack explicit timeouts
3. **Liveness checks** - No health ping before expensive operations

---

## Binary vs JSON Protocols

### Binary Protocols: Fragile

Binary IPC is fragile for CLI tools:
- **Version skew**: Struct layout changes break everything silently
- **No introspection**: Cannot debug by looking at the wire
- **Tight coupling**: Both sides must match exactly
- **Endianness/padding**: Subtle platform differences cause corruption

**Exception**: protobuf/gRPC - binary but with strong schema versioning.

### JSON-Based: Recommended

JSON IPC wins for CLI simplicity:
- **Self-describing**: Can `cat` the socket and see what's happening
- **Version tolerant**: Ignore unknown fields, add new ones freely
- **Debuggable**: `jq` is your friend
- **Universal parsing**: Every language handles it

Overhead is negligible for CLI tools (microseconds).

**ox uses NDJSON correctly** - no changes needed here.

---

## Length-Prefix vs NDJSON

### Length-Prefix Framing

```
[4-byte length][JSON payload]
```

**Pros**:
- Handles embedded newlines in content
- Unambiguous message boundaries

**Cons**:
- Breaks `cat /path/to/socket` debugging
- Breaks `echo '{"cmd":"status"}' | socat - UNIX:/path/to/socket`
- Cannot pipe directly to `jq`
- Binary prefix defeats "human-readable" goal

### NDJSON (Current ox approach)

```
{"type":"request","id":1}\n
{"type":"response","id":1}\n
```

**Pros**:
- Fully debuggable with standard Unix tools
- Can grep log files
- `socat` and `nc` work directly

**Cons**:
- Embedded newlines would break framing

**Resolution**: The embedded newline "problem" is solved by JSON encoding itself:
- `\n` in JSON content becomes `\\n` on wire
- Only malformed JSON would break NDJSON
- ox message payloads don't contain raw newlines

**Recommendation**: Keep NDJSON. Debuggability outweighs theoretical risks.

---

## Socket Existence vs PID Files

### Socket-Only Detection

**Theory**: Socket existence = daemon running. OS cleans up socket on process death.

**Reality - Edge Cases**:

| Scenario | Socket Exists | Process State | Detection |
|----------|---------------|---------------|-----------|
| Normal running | Yes | Healthy | Works |
| Process crashed | No | Dead | Works |
| Process hung | Yes | Alive but unresponsive | **FAILS** |
| D-state (kernel) | Yes | Uninterruptible sleep | **FAILS** |
| Zombie (not reaped) | Maybe | Dead, not reaped | **UNRELIABLE** |
| Unclean shutdown | Maybe | Dead | **UNRELIABLE** |

**Windows Note**: Named pipes have different semantics. Socket-only may work better on Windows, but cross-platform consistency argues for hybrid approach.

### PID File Alone

**Problems**:
- Stale detection requires `/proc` (Linux) or `kill -0` (Unix)
- PID reuse on long-running systems
- No guarantee process is responsive (just alive)

### Hybrid Approach (Recommended)

**Socket + PID + Health Ping**:

```
1. Socket exists?
   └── No  → Daemon not running
   └── Yes → Continue...

2. Connect with 1s timeout
   └── Timeout → Socket stale, check PID...
   └── Connected → Continue...

3. Send health ping, expect response in 500ms
   └── Timeout → Daemon hung
   └── Response → Daemon healthy

4. On "hung" detection:
   └── Read PID file
   └── kill -0 PID (verify process exists)
   └── If exists but not responding → kill -9, restart
   └── Clean up stale socket
```

**Why PID helps**:
- Confirms process identity before killing
- Detects stale socket vs hung process
- Enables recovery without guessing

---

## Flock: Why It's Problematic

`flock()` has real issues:
- **NFS**: Doesn't work reliably
- **Stale locks**: Process death can leave orphans
- **Platform variance**: Behavior differs across systems
- **Race conditions**: Easy to get wrong

**ox doesn't use flock** - this is correct.

### Better Locking Alternatives

| Method | How It Works | Cleanup |
|--------|--------------|---------|
| Unix socket bind | Bind fails if already bound | OS cleans up on death |
| PID file + O_EXCL | Atomic create fails if exists | Stale detection via /proc |
| SQLite | Built-in locking | Automatic |

---

## Timeout Best Practices

### Every Operation Needs a Timeout

```go
// BAD: Can block forever
conn, err := net.Dial("unix", socket)

// GOOD: Bounded wait
conn, err := net.DialTimeout("unix", socket, 2*time.Second)
```

### ox Timeout Hierarchy (Current)

| Category | Timeout | Operations |
|----------|---------|------------|
| Fire-and-Forget | 50ms | heartbeat, telemetry, friction |
| Fast Commands | 500ms | ping, status, version, doctor |
| Sync Operations | 30s | repo sync |
| Network-bound | 60s | team_sync, checkout/clone |

### Recommended Additions

| Operation | Current | Recommended |
|-----------|---------|-------------|
| Socket connect | Implicit | **Explicit 1-2s** |
| Socket write | 5s deadline | OK |
| Health ping | N/A | **Add 500ms ping** |
| Total request | Implicit | **Add 60s max** |

---

## SQLite as IPC Alternative

Increasingly popular pattern (Firefox, Chrome, 1Password):

- WAL mode for concurrent reads
- ACID guarantees
- Survives crashes beautifully
- No custom protocol

**For ox**: Not recommended as replacement for socket IPC. Sockets work well for request/response. SQLite could be useful for daemon state persistence (separate from IPC).

---

## Recommendations for ox

### Priority 1: Hung Daemon Detection

Add health ping before expensive operations:

```go
func (c *Client) IsHealthy() bool {
    conn, err := net.DialTimeout("unix", c.socket, 1*time.Second)
    if err != nil {
        return false
    }
    defer conn.Close()

    conn.SetDeadline(time.Now().Add(500 * time.Millisecond))

    // Send ping, expect pong
    fmt.Fprintln(conn, `{"type":"ping"}`)

    scanner := bufio.NewScanner(conn)
    if scanner.Scan() {
        var resp struct{ Data string }
        json.Unmarshal(scanner.Bytes(), &resp)
        return resp.Data == "pong"
    }
    return false
}
```

### Priority 2: Timeout Audit

Ensure all IPC call sites have explicit timeouts:
- Connect timeout (1-2s)
- Write timeout (5s)
- Read timeout (varies by operation)
- Total request timeout (60s max)

### Priority 3: Keep PID as Safety Net

Current hybrid approach is correct. PID file enables:
- Stale socket detection
- Forced recovery when daemon hung
- Process identity verification before kill

### Priority 4: Document Recovery Flow

When daemon is unresponsive:
1. Health ping fails → Daemon hung
2. Check PID file → Process exists?
3. If yes: `kill -9 PID`, remove socket, restart
4. If no: Remove stale socket, restart

---

## Files to Audit

| File | Purpose | Timeout Check |
|------|---------|---------------|
| `internal/daemon/ipc.go` | Core IPC | Verify all paths |
| `internal/daemon/client.go` | CLI client | Connect + request timeouts |
| `cmd/ox/heartbeat.go` | Fire-and-forget | Already 50ms |
| `cmd/ox/sync.go` | Sync trigger | 30s timeout |
| `cmd/ox/status.go` | Status query | 500ms timeout |

---

## Summary

| Pattern | Status | Action |
|---------|--------|--------|
| NDJSON protocol | Correct | Keep |
| Socket + PID hybrid | Correct | Keep |
| Fire-and-forget | Correct | Keep |
| Timeout hierarchy | Mostly correct | Audit gaps |
| Hung daemon detection | Gap | Add health ping |
| Length-prefix framing | Not used | Don't add |
| flock() | Not used | Don't add |
