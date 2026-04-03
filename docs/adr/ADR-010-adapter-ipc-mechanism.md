# ADR-010: IPC Mechanism — stdin/stdout, NDJSON, Two-Way

**Status**: Accepted
**Date**: 2026-04-02

## Context

The adapter binary is a subprocess. Ox/daemon needs to exchange data with it. Options: stdin/stdout, Unix sockets, gRPC, HTTP over socket, named pipes.

For serve mode (long-lived process), a framing protocol is also needed.

## Decisions

### Transport: stdin/stdout

The daemon holds the adapter's stdin/stdout pipes directly (via `exec.Cmd`). No socket paths to manage, no cleanup on crash, no port allocation. Works everywhere. Crash detection is immediate — pipe EOF.

Ruled out:
- **gRPC**: Requires protobuf toolchain. Community adapter authors shouldn't need `protoc`. Hard to test without `grpcurl`.
- **HTTP over Unix socket**: HTTP overhead, header parsing, socket path lifecycle.
- **Named pipes**: Two pipes needed for bidirectional. More setup than exec.Cmd.

**Windows forward-compatibility**: stdin/stdout transport was chosen in part because it works on
Windows without modification. Named pipes, `%APPDATA%` paths, and `.exe` binary extension handling
are deferred to a Windows implementation phase — the protocol itself makes no Unix-only assumptions.

### Framing (serve mode): NDJSON

One compact JSON object per line. `bufio.Scanner` reads it. No parser needed. The only rule: JSON must be compact (no literal newlines) — which is the default for `json.Marshal`.

Ruled out:
- **LSP headers** (`Content-Length: 87\r\n\r\n{...}`): Extra header parsing complexity. LSP needs it because it handles arbitrary/pretty-printed JSON. We control both sides and can mandate compact.
- **Length-prefix binary**: Can't test with shell tools.

### Direction: Two-Way (daemon drives; adapter pushes events automatically)

The daemon sends requests, the adapter responds. **In addition**, adapters that declare the
`file_watcher` capability automatically push entry events for any session discovered via
`find-session` — no explicit subscribe/unsubscribe step.

Two message flows coexist on the same stdout pipe:
1. **Response messages**: `{"id": N, "result": {...}}` — response to a specific daemon request
2. **Event messages**: `{"event": "entries", "agent_id": "...", "data": {...}}` — adapter-initiated push

The daemon's stdout reader distinguishes them by the presence of `"id"` (response) vs `"event"` (push).
The two flows do not interleave within a single line — NDJSON framing guarantees atomicity per line.

**Why two-way**: Real-time recording between hook calls enables instant session indexing — other team
members' ox instances can receive session entries mid-session rather than waiting until session end.
Hook-driven recording (pull-only) is bounded by tool-call frequency and introduces per-tool latency.
Push events deliver entries the moment the agent writes them to disk.

**Push is optional**: Adapters that do not support real-time file watching (e.g., SQLite-backed
agents where polling is acceptable) simply never send `event` messages. The daemon falls back to
hook-driven `read-from-offset` polling for those adapters.

Complexity trade-offs accepted:
- Adapter needs a background goroutine for the fsnotify loop
- Daemon's stdout reader must be async (reads events while also sending requests)
- All stdout writes must be serialized (mutex or single-writer goroutine)
- Testing uses a controllable event injector (see design/testing.md)

Requests remain sequential from daemon → adapter (one active request at a time per adapter process).
Events are adapter-initiated and do not need a request to precede them.

### Protocol Shape: Minimal JSON-RPC-inspired

One-shot mode: flags in, compact JSON to stdout, exit. No request framing needed.

Serve mode:
```
Request  (daemon → adapter stdin):  {"id":1,"method":"...","params":{...}}\n
Response (adapter → daemon stdout): {"id":1,"result":{...}}\n
Error:                              {"id":1,"error":{"code":"internal_error","message":"..."}}\n
```

IDs allow the daemon to match responses to requests if needed. Requests are sent sequentially (one in-flight at a time per session), so IDs are mainly for debugging.

## Shared Implementation: pkg/ndjson

Both IPC layers (CLI→daemon over Unix socket, daemon→adapter over stdin/stdout) use the same
NDJSON framing. A shared `pkg/ndjson` package provides:

- `ndjson.Scanner` — wraps `bufio.Scanner` with 1MB buffer limit (matching the existing daemon IPC
  limit) and explicit `Err()` checking after the scan loop. Using the default `bufio.Scanner`
  (64KB) causes silent truncation for large tool call outputs.
- `ndjson.Encoder` — wraps `json.Encoder` with compact encoding enforced.

External adapter authors import `pkg/ndjson` directly. This prevents them from accidentally using
the wrong buffer size. See ADR-007.

## Stdin Timeout Guard for One-Shot Subcommands

Any one-shot subcommand that reads from stdin (e.g., `parse-hook` or future hook-data commands)
must apply a read timeout. Some IDE hook runners hold stdin open without sending data, which would
block the process indefinitely.

```go
// 100ms: matches the fast timeout class
data, err := ndjson.ReadStdinWithTimeout(os.Stdin, 100*time.Millisecond)
if data == nil {
    // no input — treat as empty, not an error
}
```

This is a real production bug pattern: some IDE hook runners hold stdin open without sending data,
blocking the process indefinitely until the IDE decides to close the pipe.

## Testability

This choice makes adapters trivially testable:

```bash
# one-shot test
ox-adapter-claude-code detect
# → {"detected":true}

# serve mode test with a text fixture
printf '{"id":1,"method":"find-session","params":{...}}\n{"id":2,"method":"shutdown"}\n' \
  | ox-adapter-claude-code --serve

# stub the whole adapter with a shell script for daemon tests
```

Any adapter can be exercised with `echo`, `cat`, and `jq`. No special tooling needed.
