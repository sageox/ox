# Failure Modes & Edge Cases

The adapter system introduces many new failure points. Every failure must be handled gracefully — a misbehaving adapter must never crash or hang ox or the daemon.

---

## Adapter Binary Failures

### Adapter binary not found in adapter dirs
**When**: Daemon tries to spawn `ox-adapter-<name> --serve` but binary doesn't exist.
**Behavior**: Session falls back to generic adapter (if available) or records nothing. Hook exits cleanly. ox logs a warning once, then stops trying for that session.
**User-visible**: `ox adapter list` shows agent as undetected. `ox integrate install` prompts to install missing adapter.

### Adapter binary not executable
**When**: File exists but chmod wasn't set correctly (e.g., extracted without +x).
**Behavior**: Daemon catches `permission denied` on exec, logs error, falls back.
**Prevention**: `ox adapter install` always sets +x. Post-install step verifies binary can be executed.

### Adapter binary wrong platform (e.g., darwin binary on linux)
**When**: User copies binary manually, wrong platform.
**Behavior**: Exec fails with `exec format error`. Daemon catches, logs, falls back.
**Detection**: `ox adapter install` calls `info` after install and rejects non-working binaries.

### Adapter binary for wrong protocol version
**When**: Old adapter with ox that expects newer protocol, or vice versa.
**Behavior**: `info` returns `protocol_version: 1`, ox expects minimum 2 → adapter is skipped, not registered.
**User-visible**: `ox adapter list` shows adapter as "incompatible — upgrade with ox adapter upgrade claude-code".

### Adapter binary crashes immediately on startup
**When**: `--serve` process exits before responding to first message.
**Behavior**: Daemon detects pipe EOF, marks session as degraded, falls back to one-shot mode.
**Retry**: Daemon retries spawn once with a 1s delay. After two failures, gives up for this session.

### Adapter binary hangs and never responds
**When**: Adapter deadlocks or enters infinite loop inside `--serve`.
**Behavior**: Daemon applies a per-request timeout (100ms for `fast`-class calls like `read-from-offset`, configurable). On timeout, SIGTERMs the process, marks session degraded, falls back to one-shot.
**Hook impact**: Hook waits up to the request timeout, then exits normally. Claude Code is not blocked indefinitely.

### Adapter binary exits mid-session (unexpected)
**When**: Adapter crashes during a session (OOM, panic, etc.).
**Behavior**: Daemon detects pipe EOF. Respawns adapter, sends `find-session` to re-establish. Resumes from last checkpointed offset. Max 3 respawns per session. After that, session falls back to one-shot mode.
**Data loss**: None — last good offset is checkpointed to disk after each successful read.

### Adapter binary produces invalid JSON
**When**: Adapter writes malformed JSON to stdout.
**Behavior**: `json.Unmarshal` fails. Daemon logs the raw bytes (for debugging), returns an error to the hook. Recording is skipped for that call. Next call tries again.
**No cascade**: One bad response doesn't kill the session.

### Adapter binary writes too much to stdout
**When**: Adapter bug causes huge output (e.g., infinite loop writing to stdout).
**Behavior**: Daemon reads with a max-line-length limit (e.g., 10MB). Lines exceeding this are discarded and treated as an error.

---

## Session File Failures

### Session file not found at `find-session` time
**When**: Adapter starts before the agent has created its session file (race condition at session start).
**Behavior**: `find-session` returns `{"error":"no session found"}`. Daemon retries after 2s, up to 3 times. If still not found, session records nothing.
**Common cause**: ox SessionStart hook fires before Claude Code creates its JSONL file.

### Session file deleted mid-session
**When**: Agent rotates/deletes its session file (e.g., on compact or restart).
**Behavior**: `read-from-offset` sees the file is gone or smaller than last offset. Adapter returns `{"error":"session file rotated"}`. Daemon calls `find-session` again to re-discover the new file. Offset resets to 0 (filtered by timestamp — only entries after session start are captured).

### Session file grows faster than reads
**When**: Very fast agent activity with large tool outputs.
**Behavior**: `read-from-offset` returns everything since last offset. No data loss. Daemon may accumulate a larger batch per call than usual. Memory impact is bounded by max-line-length limit.

### Session file path changes (agent updates internal location)
**When**: Agent version update changes where it writes session files.
**Behavior**: `find-session` uses agent-native discovery (not hardcoded paths), so it finds the new location. Existing sessions in flight may need `find-session` retry. This is handled by the session file rotation recovery above.

### Session file is in a network path / slow filesystem
**When**: Home directory is on NFS, or session file is on a slow drive.
**Behavior**: All file reads go through the adapter binary, not ox itself. Adapter's per-request timeout (5s) applies. Daemon is insulated from slow I/O.

---

## Daemon Failures

### Daemon crashes while adapter processes are running
**When**: Daemon OOM, panic, or SIGKILL.
**Behavior**: Adapter processes (children) receive SIGHUP and exit. Recording state on disk preserves last good offset. On daemon restart, adapter processes are respawned lazily on next hook call. No data loss.

### Daemon takes too long to start (hook fires before daemon is ready)
**When**: Machine is slow, daemon starting up.
**Behavior**: Hook falls back to one-shot mode: spawns adapter binary directly, calls `read-from-offset`, exits. Slower but recording continues.

### Daemon is running but IPC socket is full / backlogged
**When**: Many concurrent hook calls arrive simultaneously (parallel agent sessions).
**Behavior**: Unix socket has a listen backlog. If full, IPC connection fails. Hook falls back to one-shot mode.

### Daemon IPC call times out
**When**: Daemon is overloaded, taking too long to route the request.
**Behavior**: Hook applies a configurable IPC timeout (default 3s, distinct from the per-request adapter timeout). On timeout, hook falls back to one-shot and exits. Recording continues.

---

## Installation Failures

### Registry fetch fails (no network)
**When**: `ox adapter install` or `ox integrate install` can't reach the registry.
**Behavior**: Falls back to embedded registry (bundled in ox binary). Version information may be stale. Install proceeds with bundled URLs if available.

### Binary download fails or is interrupted
**When**: Network drops during download.
**Behavior**: Partial download written to a temp file. On failure, temp file deleted. Error returned to user. Original installed binary (if any) is untouched.

### sha256 checksum mismatch
**When**: Corrupted download or tampered binary.
**Behavior**: Binary is deleted, error returned, install aborts. User is shown the expected vs actual checksum.

### `info` verification fails after install
**When**: Binary installed but doesn't respond to `info` correctly.
**Behavior**: Binary is deleted. Error returned: "ox-adapter-claude-code failed verification: [reason]". Install is rolled back.

### Adapter dir not writable
**When**: `~/.local/share/ox/adapters/` permissions wrong.
**Behavior**: Error with clear message: "Cannot write to adapter directory. Run: chmod 755 ~/.local/share/ox/adapters/"

---

## Hook Failures

### Hook fires but no active session for that agent_id
**When**: Hook fires after session was already stopped, or agent reuses a session ID.
**Behavior**: Daemon or one-shot adapter returns `{"entries":[],"new_offset":0}`. Hook exits cleanly. Nothing recorded.

### Hook fires but recording is not enabled for this repo
**When**: ox not initialized in this repo, or adapter not installed.
**Behavior**: Hook script checks `command -v ox` (it's there), ox checks `.sageox/` exists. If not initialized, hook exits immediately and cleanly. No error shown to user.

### Multiple hooks fire concurrently (parallel agent invocations)
**When**: Agent fires multiple hooks simultaneously (rare but possible in subagent scenarios).
**Behavior**: Daemon serializes per-session (requests are sequential per AdapterSession). Concurrent hooks for different sessions are handled independently. No cross-session interference.

### Hook fires for unknown AGENT_ENV value
**When**: New agent version changes its `AGENT_ENV` value, or misconfigured hook.
**Behavior**: ox can't find a registered adapter for that `AGENT_ENV`. Hook logs a warning and exits cleanly. No recording.

---

## Edge Cases

### Same agent binary running in two different repos simultaneously
**When**: User has two terminals, Claude Code running in different directories.
**Behavior**: Each repo has its own daemon (per-project isolation). Each daemon manages its own adapter sessions. No interference.

### Agent starts before ox daemon
**When**: User opens Claude Code, daemon hasn't started yet.
**Behavior**: SessionStart hook fires, daemon not running, hook falls back to one-shot mode. Subsequent hooks also fall back. If daemon starts later (user runs `ox daemon start`), future hook calls will use the daemon. Entries captured in one-shot mode are already in `raw.jsonl`.

### Very long session (8+ hours, large transcript)
**When**: Agent session runs all day. Session file grows to many MB.
**Behavior**: `read-from-offset` uses byte-offset seeking — it doesn't re-read from the beginning. Memory per call is bounded to new entries since last offset, not total file size. `raw.jsonl` grows incrementally.

### System clock skew between hook calls
**When**: NTP adjustment causes time to go backwards between hook calls.
**Behavior**: Timestamp filtering (entries must be after session start time) uses the session start timestamp set at `find-session` time, not re-evaluated. Entries are filtered by the adapter's own timestamps, which are consistent within the adapter's process. Unlikely to cause issues in practice.

### Adapter installed but agent not present
**When**: User has `ox-adapter-<name>` installed but the corresponding agent is not installed.
**Behavior**: `detect` returns `{"detected": false}`. Adapter is not selected for auto-detection. No error.

### Two adapters claim the same AGENT_ENV value
**When**: Conflicting adapter registrations (e.g., two versions of claude-code adapter).
**Behavior**: First registered wins. `$OX_ADAPTER_PATH` is scanned before system paths, so local/dev adapters take priority. ox logs a warning about the conflict.

### Adapter protocol version mismatch after upgrade
**When**: ox upgrades, new minimum protocol version, old adapter installed.
**Behavior**: Daemon calls `info`, sees old protocol version, logs: "ox-adapter-gemini protocol version 1 is below minimum required (2). Run: ox adapter upgrade gemini". Falls back to built-in adapter if available, or records nothing.
