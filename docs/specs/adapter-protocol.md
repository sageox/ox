# Adapter Protocol Specification

Version: 1 (draft)

## Overview

All communication between ox/daemon and an adapter binary uses:
- **Transport**: stdin/stdout pipes (managed by daemon via `exec.Cmd`)
- **Encoding**: compact JSON (no pretty-printing — newlines are delimiters)
- **Direction**: two-way — daemon sends requests, adapter responds. Adapter also pushes unsolicited
  events for any session it has discovered via `find-session`.

Two message types on stdout:
- **Response**: `{"id": N, ...}` — response to a daemon request (always has `"id"`)
- **Event**: `{"event": "...", "agent_id": "...", "data": {...}}` — adapter-initiated push (no `"id"`)

Two operational modes:

| Mode | Use | Process lifetime |
|------|-----|-----------------|
| **One-shot** | Low-frequency calls: `info`, `detect`, `install-hooks`, `check-hooks` | Spawned, responds, exits |
| **Serve** | High-frequency calls: `find-session`, `read-from-offset` during a session | Spawned at session start, lives until session end |

---

## One-Shot Subcommands

Invoked as: `ox-adapter-<name> <subcommand> [flags]`

Environment set by ox/daemon:
```
OX_PROTOCOL_VERSION=1
OX_REPO_ROOT=/path/to/repo
OX_REPO_ID=a1b2c3d4e5f6...   # stable repo identity across path changes and worktrees
OX_TEAM_ID=team_xyz           # omitted for personal (non-team) usage
```

Stdout: one compact JSON object. Exit 0 = success, non-zero = failure.
Stderr: logs only (human-readable, not parsed).

All error responses include `"error"` alongside any partial data:
```json
{"error": "session file not found: /path/to/session.jsonl"}
```

---

### `info`

Returns adapter metadata and capability declarations.

```bash
ox-adapter-claude-code info
```

```json
{
  "protocol_version": 1,
  "name": "claude-code",
  "display_name": "Claude Code",
  "version": "1.2.0",
  "type": "session",
  "capabilities": ["session_reader", "hook_installer", "incremental_reader", "file_watcher"],
  "hook_env_values": ["claude-code"],
  "serve_mode": true
}
```

**Capabilities** (adapter declares what it supports):
- `session_reader` — implements `find-session`, `read`, `read-metadata` (required for all session adapters)
- `hook_installer` — implements `install-hooks`, `check-hooks`, `uninstall-hooks`
- `incremental_reader` — implements `read-from-offset` (required for serve mode recording)
- `file_watcher` — pushes entry events automatically after `find-session` (no explicit subscribe needed)
- `serve_mode` — supports `--serve` flag

`hook_env_values` — values of `AGENT_ENV` that identify this agent in hook invocations. Used by ox to map hook events to the correct adapter.

**Capability assertion**: ox asserts all required capabilities are present at session registration
time (after calling `info`), not at runtime. If a required capability is absent, the session is
rejected with a clear error rather than discovering the gap mid-recording.

---

### `detect`

Checks whether this agent is installed/detectable in the current environment.

```bash
ox-adapter-claude-code detect
```

```json
{"detected": true, "reason": "found ~/.claude/projects/"}
```

```json
{"detected": false, "reason": "claude binary not on PATH"}
```

---

### `check-hooks`

Returns whether hooks are currently installed. `hook_files` is an array — some agents have
multiple hook installation locations (CLI hooks, IDE hooks, VS Code settings, etc.).

```bash
ox-adapter-claude-code check-hooks --repo-root /path/to/repo --scope project
```

```json
{
  "installed": true,
  "scope": "project",
  "hook_files": ["/path/to/repo/.claude/settings.json"]
}
```

For agents with multiple hook locations (e.g. both CLI hooks and IDE configuration):
```json
{
  "installed": true,
  "scope": "project",
  "hook_files": [
    "/path/to/repo/.agent/settings.json",
    "/path/to/repo/.agent/hooks/ox-stop.hook"
  ]
}
```

---

### `install-hooks`

Writes agent-specific hook configuration. The adapter owns ALL knowledge of how to configure its agent — hook file paths, format, version-specific quirks, etc.

```bash
ox-adapter-claude-code install-hooks --repo-root /path/to/repo --scope project
```

`--scope`: `project` (writes to `.claude/settings.json`) or `user` (writes to `~/.claude/settings.json`)

```json
{
  "installed": true,
  "files_written": ["/path/to/repo/.claude/settings.json"],
  "hooks": ["SessionStart", "UserPromptSubmit", "PostToolUse", "Stop", "SessionEnd"]
}
```

The hook command template the adapter installs:
```
if command -v ox >/dev/null 2>&1; then
  AGENT_ENV=claude-code ox agent hook <EventName> 2>&1 || true
fi
```

The adapter handles version-specific differences. If Claude Code v2 changes its hook format, `ox-adapter-claude-code` v1.3.0 handles it — no ox core change.

---

### `uninstall-hooks`

Removes ox hook entries from agent configuration, preserving unrelated settings.

```bash
ox-adapter-claude-code uninstall-hooks --repo-root /path/to/repo --scope project
```

```json
{"uninstalled": true, "files_modified": ["/path/to/repo/.claude/settings.json"]}
```

---

### `read`

Reads all entries from a session file.

```bash
ox-adapter-claude-code read --session-file /path/to/session.jsonl
```

```json
{
  "entries": [
    {"timestamp": "2026-04-02T10:30:00Z", "role": "user", "content": "Fix the bug"},
    {"timestamp": "2026-04-02T10:30:05Z", "role": "assistant", "content": "..."}
  ],
  "metadata": {"agent_version": "1.2.3", "model": "claude-sonnet-4-20250514"}
}
```

---

### `read-metadata`

Returns only metadata (fast path — avoids reading full session).

```bash
ox-adapter-claude-code read-metadata --session-file /path/to/session.jsonl
```

```json
{"agent_version": "1.2.3", "model": "claude-sonnet-4-20250514"}
```

---

### `diagnose`

Returns structured health checks for this adapter. Called by `ox doctor` to surface adapter-specific
issues with actionable fix instructions. Timeout class: `diagnose` (5s).

```bash
ox-adapter-claude-code diagnose
```

```json
{
  "ok": false,
  "issues": [
    {
      "slug":     "claude-code:hooks-missing",
      "severity": "warning",
      "title":    "ox hooks not installed",
      "detail":   "Claude Code hooks are not configured for this project. Recording is disabled.",
      "fix":      "ox integrate install",
      "fix_safe": true
    }
  ]
}
```

When no issues are found:
```json
{"ok": true, "issues": []}
```

**Issue fields**:
- `slug` — stable identifier used by `ox doctor --fix-slug <slug>` for automated fixing
- `severity` — `"error"` (blocks recording) or `"warning"` (degrades recording)
- `fix` — shell command users can run to resolve the issue
- `fix_safe` — `true` if ox may run `fix` automatically without user confirmation

---

## Serve Mode

Entered via `--serve` flag. The daemon spawns and holds this process for the duration of a session.

```bash
ox-adapter-claude-code --serve
```

**Wire format**: newline-delimited JSON. Each line is one complete JSON object. Compact encoding required (no literal newlines in values — use `\n` escape).

```
stdin  (daemon writes):  one request per line
stdout (adapter writes): one response per line
stderr:                  logs only
```

**Request shape**:
```json
{"id": 1, "method": "find-session", "params": {...}}
```

**Response shape** (success):
```json
{"id": 1, "result": {...}}
```

**Response shape** (error):
```json
{"id": 1, "error": {"code": "internal_error", "message": "session file not found"}}
```

Requests are sequential (daemon sends next request only after receiving response). IDs are for debugging/correlation.

---

### Serve Mode: Session Multiplexing

Because one adapter process handles all active sessions of its type, every serve-mode request
includes `agent_id`. The adapter uses `agent_id` to look up per-session state (file handle, offset).

---

### serve: `find-session`

Locates the session file for a given agent. Called once per session at session start.

`repo_id` is a stable identity for the repo (survives path changes, worktree moves). `team_id` is
present only for team-workspace sessions; adapters that don't need team scoping may ignore it.

```json
{"id":1,"method":"find-session","params":{
  "agent_id":  "r7f3a2-OxA1b2",
  "repo_id":   "a1b2c3d4e5f6...",
  "team_id":   "team_xyz",
  "repo_root": "/path/to/repo",
  "since":     "2026-04-02T10:00:00Z"
}}
```

```json
{"id":1,"result":{"session_file":"/Users/user/.claude/projects/abc123/session.jsonl","offset":512}}
```

`offset` is the byte position at session start — entries before this offset predate the session and
must be filtered. The adapter computes this at find time so the daemon doesn't need to know the file
format. The adapter caches the open file handle keyed by `agent_id`.

**Timeout class**: `scan` (100ms default, configurable). Session file discovery involves filesystem
scanning. If the scan doesn't complete in the budget, the daemon logs a warning and falls back to
one-shot mode for this session.

---

### serve: `read-from-offset`

Reads new entries since the last known offset. Called on every PostToolUse hook. Must be fast.

**Timeout class**: `fast` (100ms default, configurable). This is the hot path — every tool call
waits on this response before completing. The adapter uses a cached file handle; no open/seek from
zero. Exceeding the timeout on consecutive calls degrades the session to one-shot mode.

```json
{"id":2,"method":"read-from-offset","params":{
  "agent_id":    "r7f3a2-OxA1b2",
  "repo_id":     "a1b2c3d4e5f6...",
  "session_file": "/path/session.jsonl",
  "offset":      512
}}
```

```json
{"id":2,"result":{"entries":[{"timestamp":"...","role":"user","content":"hello"}],"new_offset":1024}}
```

Returns empty entries (not an error) if nothing new:
```json
{"id":2,"result":{"entries":[],"new_offset":512}}
```

The adapter uses the cached file handle for `agent_id`. No repeated open/seek from zero.

---

### Automatic Push Events (file_watcher capability)

Adapters that declare `file_watcher` in their capabilities automatically push new entries for any
session discovered via `find-session`. No explicit subscribe/unsubscribe step — the adapter opens
an fsnotify watch on the session file at `find-session` time and pushes events as new entries
arrive. The adapter stops pushing when it receives `end-session` or `shutdown`.

```json
{"event":"entries","agent_id":"r7f3a2-OxA1b2","data":{"entries":[...],"new_offset":2048}}
{"event":"entries","agent_id":"r7f3a2-OxA1b2","data":{"entries":[...],"new_offset":3072}}
```

**Stdout serialization requirement**: Push events and request responses share the same stdout
pipe. Adapters MUST serialize all stdout writes — a mutex or single-writer goroutine (channel-based)
is required. `json.Encoder.Encode()` is not goroutine-safe; concurrent writes from the serve loop
and a file watcher goroutine will interleave partial JSON lines, producing corrupt NDJSON.

The daemon's stdout reader routes events by `agent_id` to the correct session handler. The daemon
does not need to send `read-from-offset` for sessions receiving push events.

**Adapters without `file_watcher`**: The daemon falls back to hook-driven `read-from-offset`
polling. No push events are expected from these adapters.

---

### serve: `end-session`

Tells the adapter a specific session has ended. The adapter closes the file handle, stops any
file watching, and releases all state for that `agent_id`.

```json
{"id":4,"method":"end-session","params":{"agent_id":"r7f3a2-OxA1b2"}}
```

```json
{"id":4,"result":null}
```

The adapter may continue serving other sessions. If this was the last active session, the daemon
may send `shutdown` to reclaim the process (see Process Lifecycle below).

---

### serve: `shutdown`

Graceful shutdown. Adapter closes all open resources for all sessions and exits.

```json
{"id":99,"method":"shutdown"}
```

```json
{"id":99,"result":null}
```

Process exits with code 0.

---

### Process Lifecycle

The daemon spawns `--serve` lazily (on first hook call for a session type) and manages its lifetime:

- **Spawn**: First hook call for a session type triggers spawn + `find-session`
- **Active**: Process stays alive as long as at least one session of that type is active
- **Idle shutdown**: When the last session of a type ends (via `end-session`), the daemon sends
  `shutdown` after a grace period (default: 30s). The grace period avoids rapid spawn/shutdown
  cycles when sessions start and stop frequently.
- **Daemon shutdown**: Sends `shutdown` to all adapter processes, waits 5s, SIGTERMs any remaining

### One-Shot Commands While Serve Is Running

One-shot subcommands (`info`, `detect`, `install-hooks`, `check-hooks`, `diagnose`) are always
invoked as separate short-lived processes, never routed through the `--serve` pipe. Two instances
of the same adapter binary may run simultaneously — one long-lived serve process and one short-lived
one-shot. One-shot commands are stateless and do not conflict with the serve process.

---

### serve: unknown method

Any unrecognized method returns a canonical not-found error. The daemon treats this as "capability
absent" and degrades gracefully (does not mark the session as errored).

```json
{"id":5,"method":"some-future-method","params":{}}
```

```json
{"id":5,"error":{"code":"method_not_found","message":"unknown method: some-future-method"}}
```

---

## RawEntry Schema

All entry reading returns the same `RawEntry` shape:

```json
{
  "timestamp":  "2026-04-02T10:30:00Z",  // RFC3339
  "role":       "user",                  // "user" | "assistant" | "system" | "tool"
  "content":    "Fix the auth bug",      // text content
  "tool_name":  "",                       // non-empty when role="tool"
  "tool_input": "",                       // JSON string of tool input
  "tool_output":"",                       // error output only (not full stdout)
  "is_error":   false,                    // true if tool call failed
  "call_id":    ""                        // correlates tool call with response
}
```

---

## Streaming / Future Incremental Distribution

The protocol is designed to support future streaming (mid-session distribution to other ox users in the team) without breaking changes.

The one-way pull model already supports it: the daemon accumulates entries in `raw.jsonl` on each `read-from-offset` call. A future "streaming upload" feature would have the daemon push incremental batches to the ledger during the session rather than only at session end. The adapter protocol is unchanged — it just reads and returns entries. The daemon decides when and how to upload them.

When streaming upload is implemented:
- Each `read-from-offset` response triggers both local append AND incremental ledger upload
- Other ox users' daemons fetch the incremental chunks
- Session replay is possible mid-session
- No adapter protocol changes needed

---

## Timeout Classes

Timeouts are configurable in ox daemon config (`adapter_timeouts` block). Defaults reflect the
intended performance envelope. Adapters that need more time for a specific operation (e.g., a slow
network-backed indexer doing `find-session`) can declare a preferred class in their `info` response,
or ox can be configured per-adapter.

| Class | Default | Used for |
|-------|---------|----------|
| `fast` | 100ms | `read-from-offset` — hot path, every tool call |
| `scan` | 100ms | `find-session` — filesystem scan at session start |
| `install` | 30s | `install-hooks`, `uninstall-hooks` — may do network/editor calls |
| `diagnose` | 5s | `diagnose` — health checks |
| `background` | none | Fire-and-forget: no response expected; daemon does not wait |

Fire-and-forget semantics: the daemon sends the request and does not block waiting for a response.
The adapter may or may not send a response; any response is silently discarded. Use for
non-critical notifications (e.g., session telemetry, usage hints) where loss is acceptable.

Consecutive `fast`-class timeouts (N=3, configurable) degrade the session to one-shot fallback
mode. The session is not terminated — just slower. `ox doctor` surfaces this as a warning.

## Protocol Version Evolution

`protocol_version` in the `info` response is the contract between ox and adapters.

- Within a major version: only additive changes (new optional subcommands, new optional fields)
- Adapters return `method_not_found` for unknown methods — ox degrades gracefully (not an error)
- Breaking changes (removed/renamed subcommands, changed required fields) require a new major version
- ox refuses to use adapters with a *lower* major version than its own minimum supported
- ox does **not** refuse adapters with a *higher* major version — it uses what it understands and
  ignores unknown capabilities. Best-effort compatibility beats hard version gating.

Example: ox v2 (minimum protocol v2) encounters an adapter speaking protocol v3:
- ox still uses `find-session`, `read-from-offset`, `end-session` — all protocol v2 methods work
- ox ignores any v3-only capabilities it doesn't know about
- The adapter degrades to v2 behavior for this ox instance

Current: `protocol_version: 1`
