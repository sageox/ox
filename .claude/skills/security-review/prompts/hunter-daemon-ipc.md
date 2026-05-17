# Hunter — daemon IPC (/tmp/ox.sock peer-cred, authz, race, TOCTOU)

## OUTPUT CONTRACT (READ FIRST — STRICTLY ENFORCED)

Respond with **exactly one JSON object** matching this shape:

```json
{"findings": [<finding-object>, <finding-object>, ...]}
```

The CLI enforces this via `--json-schema`. Zero findings → `{"findings": []}`. JSONL (one finding per line) is also accepted. No prose. No markdown. No commentary.

**Perspective frame: I am another process on the same machine, running as the same user, that is NOT ox.** "A misbehaving editor extension. A shell hook from a blog post the user pasted. A typo-squatted CLI in the user's `$PATH`. A pwned VS Code plugin. Can I connect to `/tmp/ox.sock`, send any NDJSON, and exfiltrate tokens, hijack sessions, or drive a destructive operation? The only thing standing between me and every daemon handler is one peercred check and Unix file permissions."

See `security/SECURITY.md#hunter-daemon-ipc` for the threat model anchor. This is a hard class: any confirmed `daemon-ipc-authz-bypass` finding routes to the Opus validator per `security/config.yml` `hard_classes`.

## Why this surface is interesting

Same-UID isn't "trusted." The user runs dozens of tools as the same UID, half of which are downloaded JS code (editor plugins, MCP servers, language servers). The peercred check (`internal/daemon/ipc.go:1502`) keeps cross-user attackers out — that's not the threat. The threat is the third-party process that's already on the box and decides to drive the daemon. Every byte that gets past `handleConnection` is implicitly trusted by handler code.

What the daemon will do for whoever asks (sample, see `MsgType*` constants at `internal/daemon/ipc.go:32-62` and handlers at `internal/daemon/ipc_handlers.go`):

- `handleMurmur` — writes+commits a file into a ledger / team-context repo
- `handleSessionWatchStart` — opens a path the caller chose, starts tailing
- `handleCheckout` — git-clones a URL the caller chose into a path the caller chose
- `handleStop` — terminates the daemon
- `handleTriggerGC` — force-recloning team contexts
- `handleSessionFinalize` — uploads a local session to the ledger
- `handleMurmurPause` / `handleMurmurResume` — pauses prod nudging behavior

The peercred check is the *only* authorization. Once past it, no handler re-checks "should this caller be allowed to do this?"

## Sinks to chase

| Sink | Pattern | Why it matters |
|---|---|---|
| Any new `handle<Name>` in `internal/daemon/ipc_handlers.go` | Add itself to the dispatch table; reads `msg.Payload` JSON | Becomes a same-UID-callable verb. Even a "read" verb leaks state. |
| State-mutating IPC handler (`handleCheckout`, `handleMurmur`, `handleStop`, `handleSessionFinalize`, `handleTriggerGC`, `handleSessionWatch*`, `handleMurmur{Pause,Resume}`) | Wires `msg.Payload` → disk / network / process control | Any same-UID caller can drive arbitrary state changes |
| `filepath.Join(<caller-supplied dir>, <caller-supplied path>)` inside a handler | E.g. `MurmurPayload.TargetDir` + `MurmurPayload.RelPath` at `internal/daemon/ipc.go:341` | Caller-side IPC can write outside the intended workspace |
| `exec.Command(..., callerArgs...)` originating from an IPC handler | git clone with caller URL/path, etc. | RCE via flag-smuggle in URL, command-injection if shell involved |
| `net.Listen("unix", path)` / `os.MkdirAll(parent, 0700)` / `os.Chmod(parent, 0700)` at socket setup | `internal/daemon/ipc_unix.go:26` is the canonical pattern | A regression here (e.g. parent left 0755) means any local user reads the socket name; symlink-in-parent attack lets a non-owner intercept connect attempts |
| `ipc.go:1502` peercred gate (`if !s.peerCredDisabled`) | The single point of authentication | Any change that gates *less* (env override, debug flag, missing platform stub) is critical |
| Per-platform `peerUID` stubs (`ipc_peercred_linux.go`, `ipc_peercred_darwin.go`, `ipc_peercred_other.go`, `ipc_peercred_windows.go`) | Each returns a UID or an error | A stub that returns `0, nil` for an unsupported platform fails OPEN |
| `bufio.NewReader(io.LimitReader(conn, maxIPCMessageSize))` at `ipc.go:1521` | 1 MB cap | Removing the LimitReader, or a per-handler `io.Copy` post-cap, is a DoS gap |
| Connection acceptance loop (search for `accept` + `maxConcurrentConnections`) | 100 concurrent connection cap | Regression → connection-flood DoS |

## What to look for

1. **New `handle<Name>` without peercred-equivalent in the dispatch path.** Currently the gate is at `handleConnection`, so any new handler inherits the gate — unless the new handler is registered on a *different* listener (a debug socket, a TCP listener, a named pipe). Find the listener wiring; verify all paths into the dispatcher pass through `handleConnection`.
2. **State-mutating handler whose payload contains a path or URL** with no `filepath.Clean` + workspace-root prefix check. Diff `MurmurPayload`, `CheckoutPayload`, `SessionWatchStartPayload`, `SessionFinalizeIPCPayload` carefully — every one of those has a path field.
3. **Peercred gate weakened.** Any new `if` branch that skips the check based on a flag, env var, build tag, or "trusted local" heuristic. The only sanctioned bypass is `s.peerCredDisabled` for in-process tests.
4. **Platform stub regression.** `ipc_peercred_other.go` (or any new `ipc_peercred_<arch>.go`) returning success without a real check. The existing Linux/Darwin implementations fail closed; a new stub MUST also fail closed.
5. **Socket-file race / TOCTOU**. `listen()` at `internal/daemon/ipc_unix.go:26` removes the existing socket file then `net.Listen`. If anything between those steps lets an attacker insert a symlink at the path, the listener binds via the symlink target. Diff for any change to the removal / listen sequence; verify the parent directory is 0700.
6. **Predictable socket path in a world-writable parent.** `/tmp/<predictable-name>` is exploitable if `/tmp` is shared (multi-user dev host). Verify the path is under a user-owned 0700 dir.
7. **LimitReader removal or message-size regression.** `maxIPCMessageSize = 1 MB` (`ipc.go:25`). Any handler that re-reads `conn` past that limit (e.g. `io.Copy` for a "streaming" verb) bypasses the cap and is a DoS / memory-pressure vector.
8. **Connection cap regression.** Goroutine-per-connection without `maxConcurrentConnections` enforcement = fd exhaustion. A regression here is rare but very damaging.
9. **Workspace ID misuse for authz.** `msg.WorkspaceID` is *informational* (see `ipc.go:1541` — warning only on mismatch). Any handler that gates behavior on `msg.WorkspaceID` is using untrusted input as a capability check.
10. **`handleStop` and other destructive handlers.** These should arguably require *more* than peercred (e.g. a fresh challenge). At minimum they must not run while a session is mid-upload.

## Output format

```json
{
  "class": "daemon-ipc",
  "subclass": "missing-peercred|stub-fails-open|state-mutating-no-authz|path-traversal-in-handler|socket-file-race|world-writable-parent|message-size-bypass|connection-cap-bypass|workspace-id-as-authz|destructive-no-confirm",
  "severity": "critical|high|medium|low|info",
  "title": "<one sentence>",
  "file": "path/to/file.go",
  "line": 123,
  "handler": "MsgType* constant or function name",
  "attack": "one paragraph: I am process X running as the same UID. I open /tmp/ox.sock, send NDJSON {...}, and observe Y. Include the exact JSON payload and the resulting state change.",
  "fix": "one paragraph: per-handler capability token, prefix-check workspace root against an allowlist, fail-closed stub, restore LimitReader, add a per-IPC-call challenge for destructive verbs"
}
```

## Severity rubric

| Severity | When |
|---|---|
| critical | Peercred gate removed/weakened; platform stub returns success without checking; any new state-mutating handler reachable without the gate; socket-file race that lets a same-UID attacker intercept connects |
| high | State-mutating handler whose payload writes outside the workspace via path traversal; destructive handler (stop/GC/finalize) with no rate-limit and no confirm; LimitReader / connection-cap regression |
| medium | Read-only handler leaking workspace state the caller shouldn't see (cross-workspace info disclosure); workspace ID used as authz |
| low | Defensive — add a per-handler nonce, add explicit "this handler mutates" comment, tighten payload schema |
| info | Stylistic — comment drift, missing log line on a handler taking destructive action |

## Don't

- Don't flag the existing peercred check at `ipc.go:1502` as insufficient *without* a concrete same-UID attacker scenario. The decision was deliberate (see comment on `peerUID`).
- Don't flag `s.peerCredDisabled` as a bypass — it's gated to in-process tests; if you see it set anywhere else in production code, *then* flag it.
- Don't propose mTLS / token auth on top of Unix sockets without engaging with the design — the daemon's contract is "owner of the workspace is the daemon, owner of the workspace runs the CLI." A per-handler capability token (rotated per session, stored in `~/.config/sageox/daemon.token` 0600) is the proportionate fix; suggest that.
- Don't flag every IPC handler as "needs authz" — read the handler. `handlePing`, `handleVersion`, `handleStatus` are intentionally readable by any same-UID process for `ox doctor` use cases.
- Don't write a finding for the existing 1 MB / 100-conn caps as "too high" or "too low" without a concrete DoS path through the diff.

---

## FINAL REMINDER

Your entire response is one JSON object or pure JSONL. Begin with `{`. If zero findings: `{"findings":[]}`. No prose. No markdown. No commentary.
