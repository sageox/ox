# ADR-014: Shared Package Structure for NDJSON, Protocol Types, and Progress

- **Status:** Proposed
- **Date:** 2026-04-02

## Context

Several cross-cutting concerns have emerged across the CLI↔daemon and daemon↔adapter IPC layers that are currently either duplicated or incorrectly scoped:

- The CLI↔daemon IPC and daemon↔adapter IPC both use NDJSON over different transports (Unix socket vs stdin/stdout). The framing code is duplicated.
- External adapter authors writing Go need to import ox protocol types (`RawEntry`, `InfoResponse`, serve-mode request/response shapes, protocol version constants). These currently live in `internal/adapterprotocol/` — the `internal/` restriction prevents external repos from importing them.
- The daemon has `ProgressWriter`/`ProgressCallback` patterns for long-running operations (sync, checkout). `ox adapter install` needs the same pattern for downloads. `ox doctor fix` execution needs it too.
- `ox`'s existing daemon IPC already has a 50ms default timeout and uses `bufio.Scanner` with 1MB max line size. These constants should be shared, not re-invented for adapter IPC.

## Decision

Three shared packages:

### 1. `pkg/ndjson` (public, importable by external adapters)

Shared NDJSON framing utilities used by both IPC layers:

```go
package ndjson

const MaxLineBytes = 1 << 20  // 1MB, matches existing daemon IPC limit

// Scanner wraps bufio.Scanner with the correct buffer size and explicit Err() checking.
// Both the daemon IPC server and adapter serve-mode readers use this.
type Scanner struct { ... }
func NewScanner(r io.Reader) *Scanner
func (s *Scanner) Scan() bool
func (s *Scanner) Bytes() []byte
func (s *Scanner) Err() error  // always check this after Scan() returns false

// Encoder wraps json.Encoder with compact encoding enforced.
type Encoder struct { ... }
func NewEncoder(w io.Writer) *Encoder
func (e *Encoder) Encode(v any) error
```

**Why public:** adapter authors writing `--serve` mode need this exact scanner (with correct buffer size and `Err()` check). They must not roll their own and accidentally use the default 64KB `bufio.Scanner` limit, which truncates large tool call outputs silently.

### 2. `pkg/adapterprotocol` (public, importable by external adapters)

All protocol types. External adapter authors import only this package — nothing from ox internals.

```go
package adapterprotocol

const ProtocolVersion = 1

// One-shot response types
type InfoResponse struct {
    ProtocolVersion int      `json:"protocol_version"`
    Name            string   `json:"name"`
    DisplayName     string   `json:"display_name"`
    Version         string   `json:"version"`
    Type            string   `json:"type"`   // "session", "vcs", "indexer", "test"
    Capabilities    []string `json:"capabilities"`
    HookEnvValues   []string `json:"hook_env_values"`
    ServeMode       bool     `json:"serve_mode"`
}

type DetectResponse struct {
    Detected bool   `json:"detected"`
    Reason   string `json:"reason"`
}

type InstallHooksResponse struct {
    Installed    bool     `json:"installed"`
    FilesWritten []string `json:"files_written"`
    Hooks        []string `json:"hooks"`
}

type CheckHooksResponse struct {
    Installed  bool     `json:"installed"`
    Scope      string   `json:"scope"`
    HookFiles  []string `json:"hook_files"`  // array: agents may have multiple hook locations
}

// Serve mode message shapes
type Request struct {
    ID     int             `json:"id"`
    Method string          `json:"method"`
    Params json.RawMessage `json:"params"`
}

type Response struct {
    ID     int       `json:"id"`
    Result any       `json:"result,omitempty"`
    Error  *RPCError `json:"error,omitempty"`
}

type RPCError struct {
    Code    string `json:"code"`     // "method_not_found", "invalid_params", "internal_error"
    Message string `json:"message"`
}

type Event struct {
    Event   string          `json:"event"`    // "entries"
    AgentID string          `json:"agent_id"`
    Data    json.RawMessage `json:"data"`
}

// Session data types
type RawEntry struct {
    Timestamp  string `json:"timestamp"`   // RFC3339 UTC
    Role       string `json:"role"`         // "user" | "assistant" | "system" | "tool"
    Content    string `json:"content"`
    ToolName   string `json:"tool_name,omitempty"`
    ToolInput  string `json:"tool_input,omitempty"`
    ToolOutput string `json:"tool_output,omitempty"`
    IsError    bool   `json:"is_error,omitempty"`
    CallID     string `json:"call_id,omitempty"`
}

type SessionMetadata struct {
    AgentVersion string `json:"agent_version,omitempty"`
    Model        string `json:"model,omitempty"`
}

// Serve mode param types
type FindSessionParams struct {
    AgentID  string `json:"agent_id"`
    RepoID   string `json:"repo_id"`
    TeamID   string `json:"team_id,omitempty"`
    Since    string `json:"since"`    // RFC3339
    RepoRoot string `json:"repo_root"`
}

type ReadFromOffsetParams struct {
    AgentID     string `json:"agent_id"`
    RepoID      string `json:"repo_id"`
    SessionFile string `json:"session_file"`
    Offset      int64  `json:"offset"`
}

type EndSessionParams struct {
    AgentID string `json:"agent_id"`
    RepoID  string `json:"repo_id"`
}
```

**Why public:** a third-party adapter author should be able to do:

```go
import "github.com/sageox/ox/pkg/adapterprotocol"
```

and get all the types they need to implement a compliant adapter. Zero ox internals.

### 3. `internal/progress` (internal, shared between daemon IPC handlers and ox adapter install)

Reuse the existing `ProgressWriter`/`ProgressCallback` pattern. Move it from `daemon/ipc.go` into its own package so `ox adapter install` can use the same progress reporting without importing daemon internals.

```go
package progress

type Callback func(stage string, percent *int, message string)

// Writer sends progress updates over a connection. Best-effort: skipped if the write would block.
type Writer struct { ... }
func NewWriter(conn net.Conn, timeout time.Duration) *Writer
func (w *Writer) Send(stage string, percent *int, message string)
```

Used by:

- daemon sync/checkout handlers (existing)
- `ox adapter install`/`upgrade` (new)
- `ox doctor --fix` execution (new)

## Consequences

- External adapter authors get a stable, versioned import path for protocol types.
- NDJSON framing bugs (buffer size, `Err()` check) are fixed once, not re-broken in adapter binaries.
- Progress UX is consistent across install, upgrade, sync, and doctor fix flows.
- Adding new protocol types requires only a `pkg/adapterprotocol` update — no adapter binary needs to vendor ox internals.
