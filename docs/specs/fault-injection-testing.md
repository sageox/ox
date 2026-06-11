# Fault Injection Testing for Daemon IPC

This document describes the fault injection testing framework for testing daemon IPC reliability.

## Overview

The `FaultDaemon` (`internal/daemon/testutil/fault_daemon.go`) is a **real daemon** that can be put into various broken IPC states. Unlike mock daemons that fake responses, this creates actual failure conditions:

- Acquires the real daemon lock file (makes `IsRunning()` return true)
- Listens on the real Unix socket path
- Handles connections with configurable fault injection

This enables testing that the client correctly detects and recovers from **actual** daemon failures.

## Fault Categories

### Connection-Level Faults

These faults trigger before reading the client's request.

| Fault | Behavior | Use Case |
|-------|----------|----------|
| `FaultHangOnAccept` | Accepts connection, blocks forever | Daemon stuck in accept loop |
| `FaultCloseImmediately` | Accepts then closes immediately | Crash during handshake |
| `FaultRefuseAfterAccept` | Same as above | Backlog exhaustion simulation |
| `FaultDropConnection` | Drops every Nth connection | Retry logic testing |
| `FaultSlowAccept` | Accepts, delays before reading | Overloaded daemon |

### Post-Read Faults

These faults trigger after successfully reading the client's request.

| Fault | Behavior | Use Case |
|-------|----------|----------|
| `FaultCloseAfterRead` | Reads request, closes without response | Crash after receiving request |
| `FaultHangBeforeResponse` | Reads request, blocks forever | Handler deadlock |
| `FaultSlowResponse` | Configurable delay before responding | Timeout boundary testing |
| `FaultDeadlock` | Acquires mutex, never releases | Real deadlock scenario |
| `FaultPanicInHandler` | Calls `panic()` | Panic recovery testing |

### Protocol Corruption Faults

These faults send malformed or unexpected responses.

| Fault | Response Sent | Tests |
|-------|---------------|-------|
| `FaultCorruptResponse` | `"not json garbage \x00\xff\xfe\n"` | Binary garbage handling |
| `FaultInvalidJSON` | `{"success":true,"data":"bad\x00char"}` | Unmarshal error handling |
| `FaultPartialResponse` | `{"success":true,"data":` (incomplete) | Partial read recovery |
| `FaultResponseWithoutNewline` | Valid JSON, no trailing `\n` | Delimiter timeout |
| `FaultMultipleResponses` | Two complete JSON lines | Protocol desync detection |
| `FaultChunkedResponse` | One byte at a time with delays | Fragmented I/O handling |
| `FaultWriteHalfThenHang` | Half the response, then blocks | Partial write timeout |
| `FaultResponseTooLarge` | 2MB response | Size limit enforcement |
| `FaultEmbeddedNewlines` | Valid JSON with `\n` in strings | JSON vs line framing |
| `FaultVerySlowResponse` | Response after 15 seconds | Long timeout enforcement |

## Usage

### Basic Usage

```go
func TestClientHandlesHungDaemon(t *testing.T) {
    env := testutil.NewTestEnvironment(t)  // isolated XDG paths

    d := testutil.NewFaultDaemon(t, testutil.FaultConfig{
        Fault: testutil.FaultHangBeforeResponse,
    })
    d.Start()
    defer d.Stop()

    err := daemon.IsHealthy()
    assert.Error(t, err)
    assert.Contains(t, err.Error(), "not responsive")
}
```

### Convenience Constructors

```go
// Common scenarios have dedicated constructors
d := testutil.NewHungDaemon(t)           // FaultHangBeforeResponse
d := testutil.NewSlowDaemon(t, 200*ms)   // FaultSlowResponse with delay
d := testutil.NewCrashingDaemon(t)       // FaultCloseImmediately
d := testutil.NewCorruptDaemon(t)        // FaultCorruptResponse
d := testutil.NewFlakyDaemon(t, 3)       // Drop every 3rd connection
d := testutil.NewHealthyFaultDaemon(t)   // No faults (baseline)
```

### Configurable Faults

```go
d := testutil.NewFaultDaemon(t, testutil.FaultConfig{
    Fault:             testutil.FaultSlowResponse,
    SlowResponseDelay: 500 * time.Millisecond,
})

d := testutil.NewFaultDaemon(t, testutil.FaultConfig{
    Fault:      testutil.FaultDropConnection,
    DropEveryN: 2,  // drop every 2nd connection
})
```

### Message-Type Targeting

Apply faults only to specific message types:

```go
d := testutil.NewFaultDaemon(t, testutil.FaultConfig{
    Fault:              testutil.FaultHangBeforeResponse,
    FaultOnMessageType: daemon.MsgTypeSync,  // only hang on sync requests
})

// Ping works normally
err := daemon.IsHealthy()
assert.NoError(t, err)

// Sync hangs
client := daemon.NewClientWithTimeout(100 * time.Millisecond)
err = client.SyncWithProgress(nil)
assert.Error(t, err)  // times out
```

### Switching Faults Mid-Test

The fault configuration can be changed while the daemon is running (thread-safe):

```go
d := testutil.NewHealthyFaultDaemon(t)
d.Start()
defer d.Stop()

// Initially healthy
err := daemon.IsHealthy()
require.NoError(t, err)

// Switch to hung state
d.SetFault(testutil.FaultHangBeforeResponse)
err = daemon.IsHealthy()
assert.Error(t, err)

// Switch back to healthy
d.SetFault(testutil.FaultNone)
err = daemon.IsHealthy()
assert.NoError(t, err)
```

### Connection Counting

Track how many connections the daemon has handled:

```go
d := testutil.NewHealthyFaultDaemon(t)
d.Start()
defer d.Stop()

assert.Equal(t, int64(0), d.ConnectionCount())
_ = daemon.IsHealthy()
assert.Equal(t, int64(1), d.ConnectionCount())
```

## Test Organization

Tests are organized into **fast** and **slow** categories:

### Fast Tests (Always Run)

Tests that complete in <100ms without waiting for timeouts:

```go
func TestFaultDaemon_Fast_Healthy(t *testing.T) {
    // No timeout wait - daemon responds immediately
}

func TestFaultDaemon_Fast_CloseImmediately(t *testing.T) {
    // Connection closes immediately - no timeout wait
}
```

### Slow Tests (Skipped with `-short`)

Tests that involve timeouts (100ms+):

```go
func TestFaultDaemon_Slow_HangBeforeResponse(t *testing.T) {
    if testing.Short() {
        t.Skip("skipping slow fault test")
    }
    // This test waits for client timeout
}
```

### Running Tests

```bash
# Fast tests only (developer iteration)
go test -short ./internal/daemon/testutil/...

# All tests including slow (release testing)
go test -count=1 ./internal/daemon/testutil/...

# Specific fault test
go test -v -run "TestFaultDaemon_Fast_CorruptResponse" ./internal/daemon/testutil/...
```

## Test Environment Isolation

Always use `NewTestEnvironment` to isolate XDG paths:

```go
func TestSomething(t *testing.T) {
    env := testutil.NewTestEnvironment(t)
    // env sets unique XDG_RUNTIME_DIR, XDG_STATE_HOME, etc.
    // Socket and lock paths are isolated to temp directory

    d := testutil.NewFaultDaemon(t, config)
    d.Start()
    defer d.Stop()
}
```

This prevents tests from interfering with each other or with a real running daemon.

## Implementation Details

### Real Lock Acquisition

```go
// FaultDaemon acquires the real daemon lock
lockFile, _ := os.OpenFile(daemon.LockPath(), os.O_CREATE|os.O_RDWR, 0600)
syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
```

This makes `daemon.IsRunning()` return `true`, enabling realistic testing of the "daemon is running but unresponsive" scenario.

### Thread-Safe Configuration

```go
// Configuration is protected by mutex
func (d *FaultDaemon) getConfig() FaultConfig {
    d.configMu.RLock()
    defer d.configMu.RUnlock()
    return d.config  // returns copy
}

// Each connection gets a config snapshot
func (d *FaultDaemon) handleConn(conn net.Conn) {
    cfg := d.getConfig()  // snapshot for this connection
    // ... use cfg throughout handler
}
```

### Graceful Shutdown

```go
// Stop cancels context, closes listener, waits for goroutines
func (d *FaultDaemon) Stop() {
    d.cancel()           // signal shutdown
    d.listener.Close()   // stop accepting
    d.wg.Wait()          // wait for handlers
    // cleanup lock file and socket
}
```

Hung handlers check `d.ctx.Done()` to exit cleanly during shutdown.

## Adding New Faults

1. Add constant to `fault_daemon.go`:
   ```go
   const FaultNewFailure Fault = "new_failure"
   ```

2. Implement in `handleConn()`:
   ```go
   case FaultNewFailure:
       // implement failure behavior
       return
   ```

3. Add test in `fault_daemon_test.go`:
   ```go
   func TestFaultDaemon_Fast_NewFailure(t *testing.T) {
       // or TestFaultDaemon_Slow_* if it involves timeouts
   }
   ```

4. Add to table-driven test if applicable:
   ```go
   {"new_failure", FaultNewFailure, true},  // in TestClientPing_AllFaults
   ```

## See Also

- [IPC Architecture](./ipc-architecture.md) - Protocol design and reliability patterns
- [February 2026 IPC Analysis](../analysis/february-2026-ipc-analysis.md) - Expert review findings
