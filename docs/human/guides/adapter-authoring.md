# Adapter Author Guide

This guide explains how to build an ox adapter binary. Adapters let ox record sessions from
any AI coding agent without changes to ox itself. When you ship an adapter, ox users can install
it with `ox adapter install <name>` and recording just works.

---

## What an adapter does

An adapter binary (`ox-adapter-<name>`) bridges ox and one specific AI coding agent. It knows:

- Where the agent writes session files
- How to parse the agent's transcript format into ox's `RawEntry` shape
- How to install and remove ox hooks in the agent's config
- Whether the agent is installed on the current machine

ox never touches agent session files directly. Every agent-specific detail lives in the adapter.

---

## Naming

Your binary must be named `ox-adapter-<name>` where `<name>` is lowercase, kebab-case, and
unique across the ecosystem. Examples: `ox-adapter-claude-code`, `ox-adapter-gemini`.

Choose a name that matches the agent's canonical name. Do not use ox's name in the agent portion.

---

## Language

Adapters are standalone binaries that speak NDJSON over stdin/stdout. Any language that can read
stdin, write stdout, and emit compact JSON works: Go, Rust, Python, TypeScript, C++, etc.

### Go: use `pkg/adapterruntime`

Go adapter authors should use `pkg/adapterruntime`, which handles the entire protocol — serve
loop, subcommand dispatch, JSON framing, graceful shutdown, and thread-safe output. You write
typed handler functions; the SDK makes the protocol invisible.

See the **[Adapter SDK Guide](adapter-sdk.md)** for the SDK API, a complete minimal adapter
example, and a side-by-side comparison with raw protocol.

Prerequisites:
- Go 1.22+
- `github.com/sageox/ox/pkg/adapterruntime` — serve loop and subcommand dispatch
- `github.com/sageox/ox/pkg/adapterprotocol` — shared types
- `github.com/sageox/ox/pkg/ndjson` — NDJSON framing utilities

All three packages are public and versioned. Pin them in your `go.mod`.

The raw-protocol examples in this guide show how the protocol works. Go authors will normally
write handler functions registered with the SDK rather than the explicit serve loop shown here.

### Non-Go languages

The protocol spec (`protocol/spec.md`) is the complete contract. Any language that implements it
correctly produces a valid adapter. There is no Go code to understand or translate.

The compliance test suite (`pkg/adapterprotocol/compliance`) validates any adapter binary
regardless of implementation language. Run it to verify your adapter against the spec:

```bash
# from the ox repo
go test ./pkg/adapterprotocol/compliance/... -adapter ./path/to/ox-adapter-myagent
```

See the "Protocol requirements for all languages" section below for the handful of behaviors
your implementation must get right (buffer sizes, timeout handling, etc.).

---

## Quickstart: minimal working adapter

```
ox-adapter-myagent/
  main.go          ← subcommand dispatch
  info.go          ← info, detect
  hooks.go         ← install-hooks, check-hooks, uninstall-hooks
  session.go       ← find-session, read-from-offset
  serve.go         ← --serve loop
  diagnose.go      ← diagnose
```

```go
// main.go
func main() {
    if len(os.Args) < 2 {
        fmt.Fprintln(os.Stderr, "usage: ox-adapter-myagent <subcommand>")
        os.Exit(1)
    }

    switch os.Args[1] {
    case "info":        runInfo()
    case "detect":      runDetect()
    case "install-hooks":   runInstallHooks()
    case "check-hooks":     runCheckHooks()
    case "uninstall-hooks": runUninstallHooks()
    case "read":            runRead()
    case "read-metadata":   runReadMetadata()
    case "diagnose":        runDiagnose()
    case "--serve":          runServe()
    default:
        fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", os.Args[1])
        os.Exit(1)
    }
}

// emit writes a single compact JSON line to stdout, which is the
// required output format for all one-shot subcommands.
func emit(v any) {
    enc := json.NewEncoder(os.Stdout)
    enc.SetEscapeHTML(false)
    if err := enc.Encode(v); err != nil {
        fmt.Fprintln(os.Stderr, "output error:", err)
        os.Exit(1)
    }
}
```

---

## One-shot subcommands

Each one-shot subcommand:
1. Reads its arguments from flags or environment variables
2. Writes exactly one compact JSON object to stdout
3. Exits 0 (success) or non-zero (failure)

Never write more than one JSON object. Never pretty-print (no indentation or extra newlines).

### `info`

Declares the adapter's identity and capabilities. ox calls this at install time and on startup.

```go
func runInfo() {
    emit(adapterprotocol.InfoResponse{
        ProtocolVersion: 1,
        Name:            "myagent",
        DisplayName:     "My Agent",
        Version:         "1.0.0",
        Type:            "session",
        Capabilities:    []string{"session_reader", "hook_installer", "incremental_reader", "serve_mode"},
        HookEnvValues:   []string{"myagent"},
        ServeMode:       true,
    })
}
```

**Capabilities** — declare only what you implement:

| Capability | Meaning |
|---|---|
| `session_reader` | Implements `find-session`, `read`, `read-metadata` |
| `hook_installer` | Implements `install-hooks`, `check-hooks`, `uninstall-hooks` |
| `incremental_reader` | Implements `read-from-offset` (required for serve mode) |
| `serve_mode` | Supports `--serve` flag |
| `file_watcher` | Pushes entry events automatically after `find-session` (no explicit subscribe) |

**`hook_env_values`** — the value(s) of `AGENT_ENV` that your hook installs. ox uses this to
route hook calls to your adapter. Must match what your `install-hooks` writes.

### `detect`

Returns whether the agent is installed on the current machine.

```go
func runDetect() {
    // check common installation indicators
    homeDir, _ := os.UserHomeDir()
    agentDir := filepath.Join(homeDir, ".myagent")

    if _, err := os.Stat(agentDir); err == nil {
        emit(map[string]any{"detected": true, "reason": "found " + agentDir})
        return
    }
    if _, err := exec.LookPath("myagent"); err == nil {
        emit(map[string]any{"detected": true, "reason": "myagent binary on PATH"})
        return
    }
    emit(map[string]any{"detected": false, "reason": "myagent not found"})
}
```

### `install-hooks`

Writes ox's hook configuration into the agent's config file(s). The adapter owns all knowledge
of the agent's config format — hook file paths, JSON structure, version quirks.

Flags: `--repo-root <path>`, `--scope project|user`

```go
func runInstallHooks() {
    // parse --repo-root and --scope flags
    // write hook configuration to agent's settings file
    // the hook command to install:
    //   if command -v ox >/dev/null 2>&1; then
    //     AGENT_ENV=myagent ox agent hook <EventName> 2>&1 || true
    //   fi

    emit(map[string]any{
        "installed":     true,
        "files_written": []string{"/path/to/.myagent/settings.json"},
        "hooks":         []string{"SessionStart", "PostToolUse", "Stop"},
    })
}
```

Hook command template to install for each event:
```
if command -v ox >/dev/null 2>&1; then
  AGENT_ENV=myagent ox agent hook <EventName> 2>&1 || true
fi
```

Replace `<EventName>` with the event name (e.g., `SessionStart`, `PostToolUse`, `Stop`).

### `check-hooks`

Returns whether ox hooks are currently installed, without modifying anything.

```go
func runCheckHooks() {
    // check if hook entries exist in agent's settings
    emit(map[string]any{
        "installed":   true,
        "scope":       "project",
        "hook_files":  []string{"/path/to/.myagent/settings.json"},
    })
}
```

`hook_files` is an array — include all files where hooks were installed (some agents need
hooks in multiple locations: CLI config, IDE config, editor settings).

### `uninstall-hooks`

Removes ox's hook entries from agent config, preserving everything else.

**Critical**: remove only the ox entries. Do not delete the parent config key if other content
remains in it — other tools may depend on its presence.

```go
func runUninstallHooks() {
    // read settings, remove only ox hook entries, write back
    emit(map[string]any{
        "uninstalled":    true,
        "files_modified": []string{"/path/to/.myagent/settings.json"},
    })
}
```

### `diagnose`

Returns structured health checks. Called by `ox doctor`. Each issue must include a `slug` for
targeted fixing via `ox doctor --fix-slug <slug>`.

```go
func runDiagnose() {
    var issues []map[string]any

    // example: check if hooks are installed
    if !hooksInstalled() {
        issues = append(issues, map[string]any{
            "slug":     "myagent:hooks-missing",
            "severity": "warning",
            "title":    "ox hooks not installed",
            "detail":   "ox hooks are not configured. Session recording is disabled.",
            "fix":      "ox integrate install",
            "fix_safe": true,
        })
    }

    emit(map[string]any{
        "ok":     len(issues) == 0,
        "issues": issues,
    })
}
```

Severity levels: `"error"` (recording blocked), `"warning"` (recording degraded).
`fix_safe: true` means ox may run the fix automatically without a user prompt.

### `read` and `read-metadata`

`read` returns all entries from a session file. `read-metadata` returns only metadata (fast path).

See the [Protocol Spec](../protocol/spec.md) for the full `RawEntry` shape.

```go
func runRead() {
    // parse --session-file flag
    entries := parseSessionFile(sessionFile)
    emit(map[string]any{
        "entries":  entries,
        "metadata": map[string]string{"agent_version": "1.0.0"},
    })
}
```

---

## Serve mode

Serve mode keeps your adapter alive across multiple hook calls. The daemon spawns one
`ox-adapter-<name> --serve` process and routes all sessions of your agent type through it.

**Key design point**: one process handles ALL active sessions simultaneously. Every request
includes `agent_id` — use it to maintain per-session state (open file handles, byte offsets).

### Wire format

```
stdin  (daemon → adapter): one JSON request per line
stdout (adapter → daemon): one JSON response per line, or unsolicited event lines
stderr:                    human-readable logs only (never parsed)
```

Requests:
```json
{"id": 1, "method": "find-session", "params": {...}}
```

Responses (success):
```json
{"id": 1, "result": {...}}
```

Responses (error):
```json
{"id": 1, "error": {"code": "internal_error", "message": "session file not found"}}
```

Unknown methods must return `method_not_found` — ox treats this as "capability absent" and
degrades gracefully:
```json
{"id": 5, "error": {"code": "method_not_found", "message": "unknown method: some-future-method"}}
```

### Serve loop skeleton

```go
func runServe() {
    scanner := ndjson.NewScanner(os.Stdin)  // 1MB buffer, mandatory
    encoder := ndjson.NewEncoder(os.Stdout)
    sessions := make(map[string]*SessionState) // keyed by agent_id

    for scanner.Scan() {
        var req adapterprotocol.Request
        if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
            fmt.Fprintln(os.Stderr, "malformed request:", err)
            continue // do not crash; skip and wait for next
        }

        var result any
        var rpcErr *adapterprotocol.RPCError

        switch req.Method {
        case "find-session":
            result, rpcErr = handleFindSession(sessions, req.Params)
        case "read-from-offset":
            result, rpcErr = handleReadFromOffset(sessions, req.Params)
        case "shutdown":
            encoder.Encode(adapterprotocol.Response{ID: req.ID, Result: nil})
            os.Exit(0)
        default:
            rpcErr = &adapterprotocol.RPCError{
                Code:    "method_not_found",
                Message: "unknown method: " + req.Method,
            }
        }

        resp := adapterprotocol.Response{ID: req.ID}
        if rpcErr != nil {
            resp.Error = rpcErr
        } else {
            resp.Result = result
        }
        encoder.Encode(resp)
    }
    if err := scanner.Err(); err != nil {
        fmt.Fprintln(os.Stderr, "stdin read error:", err)
        os.Exit(1)
    }
}
```

### `find-session`

Called once per session at session start. Locate the session file and return the starting byte
offset. Cache the open file handle indexed by `agent_id`.

```go
func handleFindSession(sessions map[string]*SessionState, params json.RawMessage) (any, *adapterprotocol.RPCError) {
    var p adapterprotocol.FindSessionParams
    json.Unmarshal(params, &p)

    sessionFile := discoverSessionFile(p.RepoRoot, p.Since)
    if sessionFile == "" {
        return nil, &adapterprotocol.RPCError{Code: "not_found", Message: "no session found"}
    }

    f, err := os.Open(sessionFile)
    if err != nil {
        return nil, &adapterprotocol.RPCError{Code: "internal_error", Message: err.Error()}
    }

    // seek to the starting offset — entries before `since` are from prior sessions
    offset := findStartOffset(f, p.Since)

    sessions[p.AgentID] = &SessionState{File: f, Offset: offset}
    return map[string]any{"session_file": sessionFile, "offset": offset}, nil
}
```

**Offset semantics**: offset is opaque to the daemon — it's whatever your format requires. For
JSONL agents, it's a byte position. For JSON blob formats (history arrays), it may be an entry
count. Be consistent: whatever `find-session` returns, `read-from-offset` must accept.

### `read-from-offset`

The hot path — called on every PostToolUse hook. Must be fast. Use the cached file handle from
`find-session`. Never re-open from zero.

```go
func handleReadFromOffset(sessions map[string]*SessionState, params json.RawMessage) (any, *adapterprotocol.RPCError) {
    var p adapterprotocol.ReadFromOffsetParams
    json.Unmarshal(params, &p)

    s, ok := sessions[p.AgentID]
    if !ok {
        return nil, &adapterprotocol.RPCError{Code: "not_found", Message: "no active session for agent_id"}
    }

    // seek to last known offset (file handle already open)
    s.File.Seek(p.Offset, io.SeekStart)

    entries, newOffset := readNewEntries(s.File, p.Offset)
    s.Offset = newOffset

    return map[string]any{"entries": entries, "new_offset": newOffset}, nil
}
```

Return `{"entries": [], "new_offset": <same offset>}` when there is nothing new — not an error.

### Crash safety

- Never share mutable state across sessions without a mutex
- A panic or error in one session's handler must not corrupt another session's state
- Log errors to stderr; return an RPC error response, never crash the process
- If implementing `file_watcher` capability: all stdout writes (responses AND push events) MUST be
  serialized via a mutex or single-writer goroutine. `json.Encoder.Encode()` is not goroutine-safe —
  concurrent writes from the serve loop and a file watcher goroutine will corrupt NDJSON output

---

## Timeout contract

Your adapter must respond within the timeout for each request class:

| Request | Class | Default |
|---|---|---|
| `read-from-offset` | `fast` | 100ms |
| `find-session` | `scan` | 100ms |
| `install-hooks`, `uninstall-hooks` | `install` | 30s |
| `diagnose` | `diagnose` | 5s |

Timeouts are configurable in ox daemon config (`adapter_timeouts`). If your adapter exceeds
the `fast` timeout on three consecutive calls, the daemon downgrades that session to one-shot
mode. The session is not terminated — recording just gets slower.

Design for speed on the hot path (`read-from-offset`). Keep the file handle open. Avoid syscalls
beyond the read itself.

---

## Protocol requirements for all languages

These requirements apply regardless of implementation language. The Go `pkg/ndjson` package handles
them automatically; non-Go adapters must implement equivalent behavior.

1. **Line buffer ≥ 1MB.** The default line-reading buffer in most languages is too small (64KB in
   Go's `bufio.Scanner`, similar in Python's `readline`). Large tool call outputs (file writes,
   search results) will silently truncate. Set your line reader's max buffer to at least 1MB.

2. **Check for read errors after the scan loop.** A truncated line due to buffer overflow is not
   always reported as a per-line error — it may only surface when the reader is finished. Always
   check the final error state.

3. **Stdin read timeout for one-shot subcommands.** Some IDE hook runners hold stdin open without
   sending data. One-shot subcommands that read stdin must apply a ~100ms timeout and treat no
   input as empty (not an error).

4. **Compact JSON only.** Output must be one JSON object per line with no literal newlines inside
   the JSON. This is the default for most JSON serializers but some (Python's `json.dumps`) can
   produce pretty-printed output if configured that way.

5. **Stdout serialization in serve mode.** If your adapter uses `file_watcher` (push events from
   a background thread), all stdout writes must go through a single writer or mutex. Concurrent
   writes from the request handler and file watcher will corrupt the NDJSON stream.

## Using `pkg/ndjson` (Go)

Go adapters should use `pkg/ndjson` rather than implementing the above requirements manually.

```go
import "github.com/sageox/ox/pkg/ndjson"

// serve mode
scanner := ndjson.NewScanner(os.Stdin)
for scanner.Scan() { ... }
if err := scanner.Err(); err != nil { ... } // always check

// one-shot: read stdin with timeout (required for hook subcommands)
data, err := ndjson.ReadStdinWithTimeout(os.Stdin, 100*time.Millisecond)
if data == nil {
    // no input — treat as empty, not an error
    // some IDE hook runners hold stdin open without sending data
}
```

---

## Testing your adapter

The stdin/stdout transport makes adapters easy to test from the shell:

```bash
# one-shot
ox-adapter-myagent info
ox-adapter-myagent detect

# serve mode — pipe a sequence of requests
printf '{"id":1,"method":"find-session","params":{"agent_id":"r7f3a2-OxA1b2","repo_root":"/tmp/test","since":"2026-01-01T00:00:00Z"}}\n{"id":2,"method":"shutdown"}\n' \
  | ox-adapter-myagent --serve
```

For Go unit tests, use the compliance suite from `github.com/sageox/ox/pkg/adapterprotocol/compliance`.
It replays fixture NDJSON conversations and verifies your responses match the protocol contract:

```go
func TestCompliance(t *testing.T) {
    compliance.RunSuite(t, compliance.Config{
        AdapterBinary: "./ox-adapter-myagent",
        Fixtures:      "testdata/fixtures/",
    })
}
```

Fixtures are plain NDJSON files with request/response pairs. Write them by hand or capture them
from a real ox + daemon session.

---

## Common pitfalls

**Multi-location session discovery.**
Some agents write sessions in different locations depending on whether they ran as CLI, IDE extension, or web. Your `find-session` may need a tiered fallback: (1) IDE workspace sessions, (2) CLI data store, (3) empty placeholder. Test all modes the agent supports.

**Execution log enrichment.**
Some IDE-mode agents write minimal content to the main session file ("On it.", "I'll handle that.") while real tool use detail lives in a separate execution log. If your adapter only reads the main file, IDE session recordings will be nearly empty. Check whether the agent has a secondary log that needs merging.

**Stale session ID files on crash.**
If the stop hook doesn't fire (agent crash, terminal kill), sidecar session ID files may point to a dead session. Your `find-session` must handle stale references. The serve-mode model (daemon owns session lifecycle via `end-session`) avoids this for sessions managed through the daemon.

**`time.Now().UnixNano()` as fallback ID.**
Do not use nanosecond timestamps as unique identifiers. Fast VM clones or containers can produce identical timestamps. Use `crypto/rand` or a UUID library.

**Global state indexed by path string.**
Do not use `repo_root` as a map key for shared state. Git worktrees produce different paths for the same repository content. Key all state by `agent_id` (already globally unique).

---

## Distribution

### Community adapter (your own repo)

Create a GitHub release with binaries for each platform:

```
ox-adapter-myagent_darwin_amd64
ox-adapter-myagent_darwin_arm64
ox-adapter-myagent_linux_amd64
ox-adapter-myagent_linux_arm64
```

Users install directly from GitHub:
```bash
ox adapter install github.com/yourname/ox-adapter-myagent
```

ox fetches the latest release from the GitHub releases API, downloads the platform-appropriate
binary, verifies the sha256 (if included in the release body), and installs it.

Include the sha256 checksum in your release notes:
```
## SHA256
darwin_amd64: abc123...
linux_amd64:  def456...
```

### Official adapter (in `sageox/ox-adapters`)

Open a PR to `github.com/sageox/ox-adapters`. Once merged, your adapter is listed in `registry.yaml`
and users can install it with `ox adapter install <name>`.

Requirements for official adapters:
- Passes the compliance suite
- Has `detect` implemented (so auto-detection works)
- Has `diagnose` implemented
- Includes fixture files for the compliance suite

---

## Checklist

Before publishing your adapter:

- [ ] `info` returns correct `capabilities` and `hook_env_values`
- [ ] `detect` works without the agent installed (returns `{"detected": false}`)
- [ ] `install-hooks` is idempotent (running twice produces the same result)
- [ ] `check-hooks` returns accurate status without side effects
- [ ] `uninstall-hooks` leaves non-ox config untouched
- [ ] `diagnose` covers all failure modes users will hit
- [ ] `--serve` respond to `shutdown` and exits 0
- [ ] `--serve` returns `method_not_found` for unknown methods (not a panic)
- [ ] Uses `ndjson.Scanner` (not raw `bufio.Scanner`) in serve mode
- [ ] One-shot stdin reads use `ndjson.ReadStdinWithTimeout`
- [ ] Per-session state is keyed by `agent_id`, not `repo_root`
- [ ] Tested with `printf ... | ox-adapter-<name> --serve`
- [ ] Compliance suite passes
- [ ] Binaries released for all 5 platforms (darwin_amd64, darwin_arm64, linux_amd64, linux_arm64, windows_amd64)
