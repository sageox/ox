# `pkg/adapterruntime` — Go SDK for Adapter Authors

`pkg/adapterruntime` is a Go library that handles the entire adapter protocol, so Go adapter
authors can focus on agent-specific logic rather than JSON framing, serve loops, and graceful
shutdown.

The protocol itself is language-agnostic — any binary that reads stdin and writes stdout can be
an ox adapter. `pkg/adapterruntime` is a **Go convenience layer** on top of that protocol. It is
not required. Non-Go adapters implement the same protocol directly against the spec.

---

## What the SDK provides

| Without SDK | With SDK |
|---|---|
| Write the serve loop — scanner, decoder, `switch` on method | Register handler functions; SDK dispatches |
| Marshal/unmarshal every request and response manually | Typed params and results; SDK handles serialization |
| Handle `shutdown` gracefully in your switch | SDK catches `shutdown`, flushes, exits 0 |
| Return `method_not_found` for unknown methods | SDK does this automatically |
| Wire up `pkg/ndjson` scanner with 1MB buffer | SDK wires it for you |
| Handle subcommand dispatch in `main()` | SDK dispatches based on `os.Args[1]` |
| Serialize stdout safely when using file watchers | SDK provides a thread-safe writer |

The SDK makes the protocol invisible to adapter authors in the common case. The protocol spec
remains the authoritative contract; the SDK is the reference implementation.

---

## API shape

### One-shot subcommand handlers

Register typed handlers for each subcommand. The SDK calls your function, serializes the result,
and exits.

```go
import "github.com/sageox/ox/pkg/adapterruntime"

func main() {
    adapterruntime.Run(adapterruntime.Config{
        Info:            handleInfo,
        Detect:          handleDetect,
        InstallHooks:    handleInstallHooks,
        CheckHooks:      handleCheckHooks,
        UninstallHooks:  handleUninstallHooks,
        Read:            handleRead,
        ReadMetadata:    handleReadMetadata,
        Diagnose:        handleDiagnose,
        Serve:           handleServe,  // called when os.Args[1] == "--serve"
    })
}
```

`adapterruntime.Run` reads `os.Args[1]`, dispatches to the matching handler, serializes the
return value as compact JSON, and exits. Unknown subcommands print usage to stderr and exit 1.

### Typed handler signatures

```go
// One-shot handlers return (result, error).
// The SDK serializes result as JSON on success; logs error to stderr and exits 1 on failure.

func handleInfo() (*adapterprotocol.InfoResponse, error)
func handleDetect() (*adapterprotocol.DetectResponse, error)
func handleInstallHooks(p adapterprotocol.HookParams) (*adapterprotocol.HookResult, error)
func handleCheckHooks(p adapterprotocol.HookParams) (*adapterprotocol.HookResult, error)
func handleUninstallHooks(p adapterprotocol.HookParams) (*adapterprotocol.HookResult, error)
func handleDiagnose(p adapterprotocol.DiagnoseParams) (*adapterprotocol.DiagnoseResult, error)
func handleRead(p adapterprotocol.ReadParams) (*adapterprotocol.ReadResult, error)
func handleReadMetadata(p adapterprotocol.ReadParams) (*adapterprotocol.ReadMetadataResult, error)
```

### Serve mode handler

The serve handler receives a `*adapterruntime.Session` per serve-mode method call. The SDK
manages the scanner loop, decodes requests, calls the right handler, and encodes responses.

```go
func handleServe(srv *adapterruntime.Server) {
    srv.OnFindSession(func(ctx context.Context, p adapterprotocol.FindSessionParams) (*adapterprotocol.FindSessionResult, error) {
        // locate session file, open fd, cache it
        return &adapterprotocol.FindSessionResult{
            SessionFile: file,
            Offset:      startOffset,
        }, nil
    })

    srv.OnReadFromOffset(func(ctx context.Context, p adapterprotocol.ReadFromOffsetParams) (*adapterprotocol.ReadFromOffsetResult, error) {
        // use cached fd, read new entries
        return &adapterprotocol.ReadFromOffsetResult{
            Entries:   entries,
            NewOffset: newOffset,
        }, nil
    })

    srv.OnEndSession(func(ctx context.Context, p adapterprotocol.EndSessionParams) error {
        // close fd, release state for p.AgentID
        return nil
    })

    srv.Serve() // blocks; handles shutdown, unknown methods, malformed requests
}
```

`srv.Serve()` handles:
- `shutdown` → flushes, sends ack, exits 0
- Unknown methods → returns `method_not_found`
- Malformed JSON → logs to stderr, continues (does not crash)
- EOF on stdin → clean exit

### Session state helper

For adapters that maintain per-session state (file handles, SQLite connections, byte offsets),
the SDK provides a typed state store:

```go
// store is safe to use concurrently across handler goroutines
store := adapterruntime.NewSessionStore[*MySessionState]()

srv.OnFindSession(func(ctx context.Context, p adapterprotocol.FindSessionParams) (*adapterprotocol.FindSessionResult, error) {
    state := &MySessionState{File: f, Offset: offset}
    store.Set(p.AgentID, state)
    return &adapterprotocol.FindSessionResult{...}, nil
})

srv.OnReadFromOffset(func(ctx context.Context, p adapterprotocol.ReadFromOffsetParams) (*adapterprotocol.ReadFromOffsetResult, error) {
    state, ok := store.Get(p.AgentID)
    if !ok {
        return nil, adapterruntime.ErrSessionNotFound
    }
    // use state.File, state.Offset
    return &adapterprotocol.ReadFromOffsetResult{...}, nil
})

srv.OnEndSession(func(ctx context.Context, p adapterprotocol.EndSessionParams) error {
    state, _ := store.Delete(p.AgentID)
    if state != nil {
        state.File.Close()
    }
    return nil
})
```

`SessionStore` is a `sync.Map` wrapper with typed generics. It eliminates the `map[string]*T +
mutex` boilerplate that every adapter needs.

### Thread-safe writer for push events

If your adapter implements `file_watcher` (pushing entry events from a background goroutine),
use `srv.Writer()` for all stdout writes. The SDK ensures responses from the request handler
and push events from the watcher goroutine don't interleave:

```go
srv.OnFindSession(func(ctx context.Context, p adapterprotocol.FindSessionParams) (*adapterprotocol.FindSessionResult, error) {
    // start watching the session file
    go watchFile(ctx, p.AgentID, srv.Writer())
    return &adapterprotocol.FindSessionResult{...}, nil
})

func watchFile(ctx context.Context, agentID string, w *adapterruntime.Writer) {
    // when new entries arrive:
    w.PushEvent(adapterprotocol.EntriesEvent{
        AgentID:   agentID,
        Entries:   newEntries,
        NewOffset: newOffset,
    })
}
```

---

## Complete minimal adapter (with SDK)

```go
package main

import (
    "github.com/sageox/ox/pkg/adapterprotocol"
    "github.com/sageox/ox/pkg/adapterruntime"
)

func main() {
    adapterruntime.Run(adapterruntime.Config{
        Info:    handleInfo,
        Detect:  handleDetect,
        Serve:   handleServe,
        // other handlers...
    })
}

func handleInfo() (*adapterprotocol.InfoResponse, error) {
    return &adapterprotocol.InfoResponse{
        ProtocolVersion: 1,
        Name:            "myagent",
        DisplayName:     "My Agent",
        Version:         "1.0.0",
        Type:            "session",
        Capabilities:    []string{"session_reader", "hook_installer", "incremental_reader", "serve_mode"},
        HookEnvValues:   []string{"myagent"},
        ServeMode:       true,
    }, nil
}

func handleDetect() (*adapterprotocol.DetectResponse, error) {
    // check agent installation indicators
    return &adapterprotocol.DetectResponse{Detected: true, Reason: "..."}, nil
}

type sessionState struct {
    file   *os.File
    offset int64
}

func handleServe(srv *adapterruntime.Server) {
    store := adapterruntime.NewSessionStore[*sessionState]()

    srv.OnFindSession(func(ctx context.Context, p adapterprotocol.FindSessionParams) (*adapterprotocol.FindSessionResult, error) {
        f, offset, err := openSession(p.RepoRoot, p.Since)
        if err != nil {
            return nil, err
        }
        store.Set(p.AgentID, &sessionState{file: f, offset: offset})
        return &adapterprotocol.FindSessionResult{SessionFile: f.Name(), Offset: offset}, nil
    })

    srv.OnReadFromOffset(func(ctx context.Context, p adapterprotocol.ReadFromOffsetParams) (*adapterprotocol.ReadFromOffsetResult, error) {
        s, ok := store.Get(p.AgentID)
        if !ok {
            return nil, adapterruntime.ErrSessionNotFound
        }
        entries, newOffset := readNewEntries(s.file, p.Offset)
        s.offset = newOffset
        return &adapterprotocol.ReadFromOffsetResult{Entries: entries, NewOffset: newOffset}, nil
    })

    srv.OnEndSession(func(ctx context.Context, p adapterprotocol.EndSessionParams) error {
        s, _ := store.Delete(p.AgentID)
        if s != nil {
            s.file.Close()
        }
        return nil
    })

    srv.Serve()
}
```

Compare this to the raw protocol skeleton in `design/adapter-author-guide.md` — the SDK removes
~40 lines of boilerplate and eliminates the manual switch, scanner setup, and error codec.

---

## What the SDK does NOT abstract

The SDK handles the protocol. It does not handle:

- **Session file discovery** — where the agent writes sessions is agent-specific
- **Transcript parsing** — converting agent-specific formats to `RawEntry` is agent-specific
- **Hook installation** — each agent has its own config format
- **Detection heuristics** — how to tell if the agent is installed

These are exactly the parts that differ between adapters. The SDK makes everything else
disappear so you can focus on them.

---

## Non-Go adapters

`pkg/adapterruntime` is Go-only. Adapters written in other languages (Rust, Python, TypeScript)
implement the same protocol directly:

- **Protocol contract**: [`protocol/spec.md`](../protocol/spec.md) is the canonical reference.
  All behavior the SDK provides is documented in the spec.
- **Validation**: [`pkg/adapterprotocol/compliance`](../design/testing.md) is a black-box test
  suite that validates any adapter binary, regardless of implementation language. A Python adapter
  runs the same compliance suite as a Go adapter.
- **Reference implementation**: `pkg/adapterruntime` itself shows exactly what any language SDK
  needs to implement. The Go source is the spec made executable.

Language-specific SDKs (`ox-adapter-sdk-rust`, `ox-adapter-sdk-python`) are natural community
contributions as the adapter ecosystem grows. The compliance suite provides the acceptance criteria
for any such SDK.

---

## Related

- [Adapter Author Guide](adapter-author-guide.md) — full protocol documentation, including the
  raw-protocol approach for non-Go authors
- [Protocol Spec](../protocol/spec.md) — canonical wire format reference
- [Testing Strategy](testing.md) — compliance suite, fixture format, how to run the suite
