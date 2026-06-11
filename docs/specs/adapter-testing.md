# Testing Strategy

Three layers, each testing a different boundary.

---

## Layer 1: Unit Tests (inside adapter binary)

Tests live in `cmd/ox-adapter-<name>/` alongside the adapter code. They call session-reading functions directly in-process — no binary spawn, no protocol. Fast.

```go
// cmd/ox-adapter-claude-code/session_test.go
func TestReadFromOffset_ReturnsNewEntries(t *testing.T) {
    f := writeFakeSession(t, []string{
        `{"type":"user","message":{"content":[{"text":"fix bug"}]}}`,
        `{"type":"assistant","message":{"content":[{"text":"ok"}]}}`,
    })
    entries, newOffset, err := readFromOffset(f.Name(), 0)
    require.NoError(t, err)
    assert.Len(t, entries, 2)
    assert.Greater(t, newOffset, int64(0))
}

func TestReadFromOffset_EmptyWhenNoNewContent(t *testing.T) {
    f := writeFakeSession(t, []string{`{"type":"user",...}`})
    entries, _, _ := readFromOffset(f.Name(), 999999) // offset past end
    assert.Empty(t, entries)
}

func TestFindSession_ReturnsNewestFile(t *testing.T) { ... }
func TestHookInstall_WritesCorrectSettings(t *testing.T) { ... }
func TestHookInstall_PreservesExistingSettings(t *testing.T) { ... }
```

---

## Layer 2: Protocol Compliance Tests

A shared test suite in `github.com/sageox/ox` at `internal/adapterprotocol/compliance/`. Any adapter binary can be run against it. Tests spawn the binary, exercise every subcommand, check responses conform to the spec.

```go
// internal/adapterprotocol/compliance/suite.go
type Suite struct {
    Binary      string        // path to adapter binary
    SessionFile string        // a real or fake session file
    AgentID     string        // fake agent ID for tests
}

func (s *Suite) RunAll(t *testing.T) {
    t.Run("info",                  s.TestInfo)
    t.Run("info/protocol_version", s.TestInfoProtocolVersion)
    t.Run("detect",                s.TestDetect)
    t.Run("serve/startup",         s.TestServeStartup)
    t.Run("serve/find-session",    s.TestServeFindSession)
    t.Run("serve/read-from-offset/empty",    s.TestServeReadEmpty)
    t.Run("serve/read-from-offset/entries",  s.TestServeReadEntries)
    t.Run("serve/read-from-offset/idempotent", s.TestServeReadIdempotent)
    t.Run("serve/shutdown",        s.TestServeShutdown)
    t.Run("serve/unknown-method",  s.TestServeUnknownMethod)
}

func (s *Suite) TestInfo(t *testing.T) {
    out := s.execOnce(t, "info")
    var resp InfoResponse
    require.NoError(t, json.Unmarshal(out, &resp))
    assert.Equal(t, ProtocolVersion, resp.ProtocolVersion)
    assert.NotEmpty(t, resp.Name)
    assert.NotEmpty(t, resp.Version)
}

func (s *Suite) TestServeShutdown(t *testing.T) {
    sess := s.startServe(t)
    sess.Send(t, Request{ID: 1, Method: "shutdown"})
    resp := sess.Read(t)
    assert.Equal(t, 1, resp.ID)
    assert.Nil(t, resp.Error)
    // process should exit cleanly within 2s
    done := make(chan struct{})
    go func() { sess.Wait(); close(done) }()
    select {
    case <-done:
    case <-time.After(2 * time.Second):
        t.Fatal("adapter did not exit after shutdown")
    }
}
```

**Running against any binary**:
```bash
# official adapter
OX_ADAPTER_BINARY=./bin/ox-adapter-claude-code \
  go test ./internal/adapterprotocol/compliance/ -tags compliance -v

# community adapter
OX_ADAPTER_BINARY=/usr/local/bin/ox-adapter-myagent \
  go test github.com/sageox/ox/internal/adapterprotocol/compliance -tags compliance
```

**Makefile**:
```makefile
test-compliance:
    @for a in $(ADAPTERS); do \
        echo "=== compliance: $$a ==="; \
        OX_ADAPTER_BINARY=./bin/ox-adapter-$$a \
            go test ./internal/adapterprotocol/compliance/ -tags compliance; \
    done
```

---

## Layer 3: ExternalAdapter Wrapper Tests

Tests for the Go `ExternalAdapter` struct that ox uses to call adapter binaries. Use a fake binary (shell script) to avoid needing a real adapter. Tests the IPC plumbing, timeout handling, error parsing.

```go
// internal/session/adapters/external_test.go
func TestExternalAdapter_ReadFromOffset(t *testing.T) {
    binary := fakeBinary(t, map[string]string{
        "read-from-offset": `{"result":{"entries":[{"role":"user","content":"hello"}],"new_offset":42}}`,
    })
    adapter := &ExternalAdapter{binaryPath: binary}
    entries, newOffset, err := adapter.ReadFromOffset("/any/path", 0)
    require.NoError(t, err)
    assert.Len(t, entries, 1)
    assert.Equal(t, int64(42), newOffset)
}

func TestExternalAdapter_HandlesTimeout(t *testing.T) {
    binary := hangingBinary(t) // binary that never responds
    adapter := &ExternalAdapter{binaryPath: binary, timeout: 100 * time.Millisecond}
    _, _, err := adapter.ReadFromOffset("/any/path", 0)
    assert.ErrorIs(t, err, ErrAdapterTimeout)
}

func TestExternalAdapter_HandlesInvalidJSON(t *testing.T) {
    binary := fakeBinary(t, map[string]string{
        "detect": `not valid json`,
    })
    adapter := &ExternalAdapter{binaryPath: binary}
    _, err := adapter.Detect()
    assert.Error(t, err)
}

// fakeBinary writes a shell script that echoes canned responses
func fakeBinary(t *testing.T, responses map[string]string) string {
    t.Helper()
    script := "#!/bin/sh\ncase \"$1\" in\n"
    for cmd, resp := range responses {
        script += fmt.Sprintf("  %s) echo '%s';;\n", cmd, resp)
    }
    script += "  *) echo '{\"error\":\"unknown subcommand\"}'; exit 1;;\nesac\n"
    f := filepath.Join(t.TempDir(), "fake-adapter")
    os.WriteFile(f, []byte(script), 0755)
    return f
}
```

---

## Failure Mode Tests

Dedicated tests for every failure mode in `design/failure-modes.md`:

```go
// internal/session/adapters/external_failures_test.go

func TestExternalAdapter_BinaryNotFound(t *testing.T) { ... }
func TestExternalAdapter_BinaryNotExecutable(t *testing.T) { ... }
func TestExternalAdapter_WrongProtocolVersion(t *testing.T) { ... }
func TestExternalAdapter_CrashOnStartup(t *testing.T) { ... }
func TestExternalAdapter_HangsOnRequest(t *testing.T) { ... }
func TestExternalAdapter_OutputTruncation(t *testing.T) { ... }
func TestExternalAdapter_SessionFileRotation(t *testing.T) { ... }
```

---

## Fixture Files

Real interactions captured from live sessions, used for regression testing:

```
internal/adapterprotocol/compliance/fixtures/
  claude-code-serve-session.ndjson   ← real daemon↔adapter exchange
  claude-code-one-shot-detect.json   ← real detect response
  gemini-serve-session.ndjson
  amp-serve-session.ndjson
```

Fixture tests replay recorded interactions and verify the responses match. Used for regression when agent file formats change.

---

## Makefile Targets

```makefile
test:             # unit tests only (fast)
test-adapters:    # build adapters + run compliance suite
test-failures:    # failure mode tests
test-all:         # everything
```
