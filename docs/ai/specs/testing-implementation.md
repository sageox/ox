---
audience: ai
ai_editing: allowed
refreshable: true
---

# Testing Implementation Guide

Detailed testing patterns and anti-patterns for ox. See [testing-philosophy.md](../../human/guides/testing-philosophy.md) for high-level principles.

## Test Tiers Implementation

### Tier 1: Unit Tests (Fast)

**Target:** < 5 seconds total
**When:** Every file save, pre-commit, continuous during development

**Characteristics:**
- Pure function tests (no I/O)
- In-memory data structures only
- All external dependencies mocked
- No file system, network, or git operations

**Example files:** `doctor_test.go`, `agent_test.go`

```go
// Good: Tests pure logic
func TestParseVersion(t *testing.T) {
    cases := []struct {
        input    string
        expected Version
    }{
        {"1.2.3", Version{1, 2, 3}},
        {"0.1.0", Version{0, 1, 0}},
    }
    for _, tc := range cases {
        got := ParseVersion(tc.input)
        if got != tc.expected {
            t.Errorf("ParseVersion(%q) = %v, want %v", tc.input, got, tc.expected)
        }
    }
}
```

### Tier 2: Integration Tests

**Target:** < 30 seconds total
**When:** Pre-push, PR checks

**Characteristics:**
- Real file system operations (in temp dirs)
- Real git operations (temp repos)
- Config file parsing
- CLI argument handling

**Example files:** `init_test.go`, `hooks_test.go`, `doctor_fix_test.go`

```go
// Good: Tests real file system behavior
func TestInitCreatesConfig(t *testing.T) {
    tmpDir := t.TempDir()
    // ... test actual file creation
}
```

### Tier 3: E2E Tests

**Target:** 1-5 minutes
**When:** Pre-deploy, nightly builds

**Characteristics:**
- Full workflow tests (`ox init` -> `ox doctor` -> `ox doctor --fix`)
- Real API calls (to staging)
- Cross-platform verification
- Runs the actual compiled binary

**Example file:** `e2e_test.go`

## Running Tests

```bash
# Fast development feedback (skips slow tests >500ms)
make test          # Runs with -short flag

# Full test suite including slow tests
make test-all      # No -short flag, runs everything

# Only slow tests (timeout/delay tests)
make test-slow     # Runs specific slow test patterns

# E2E tests
go test ./... -tags=e2e
```

## Slow Test Separation

**CRITICAL**: Tests with intentional delays >500ms MUST skip in short mode.

```go
func TestSlowOperation(t *testing.T) {
    if testing.Short() {
        t.Skip("skipping slow test in short mode")
    }
    t.Parallel()
    // ... test with intentional delays
}
```

Tests that require this pattern:
- Timeout tests (waiting for HTTP timeouts)
- Retry/backoff tests (OAuth slow_down responses)
- Polling tests (device flow authorization)

**Why?** `make test` is run frequently during development. Slow tests delay developers and reduce feedback loop quality. Keep `make test` under 15 seconds.

## Test Parallelization with AuthClient

**Problem**: `t.Setenv()` panics when used with `t.Parallel()` because environment variables are process-global.

**Solution**: Use dependency injection via `AuthClient` instead of environment variables.

```go
// BAD: Cannot parallelize - t.Setenv() panics with t.Parallel()
func TestRefreshToken_Success(t *testing.T) {
    server := httptest.NewServer(...)
    defer server.Close()
    t.Setenv("SAGEOX_ENDPOINT", server.URL)  // BLOCKS t.Parallel()
    result, err := RefreshToken(ctx)
}

// GOOD: Fully parallelizable - inject dependencies
func TestRefreshToken_Success(t *testing.T) {
    t.Parallel()
    server := httptest.NewServer(...)
    defer server.Close()
    client := auth.NewAuthClientWithDir(t.TempDir()).WithEndpoint(server.URL)
    result, err := client.RefreshToken(ctx)
}
```

### AuthClient Pattern

The `AuthClient` struct provides isolated storage and endpoint configuration:

```go
// Create client with isolated temp directory and test server endpoint
client := auth.NewAuthClientWithDir(t.TempDir()).WithEndpoint(server.URL)

// Use client methods instead of package-level functions
resp, err := client.RequestDeviceCode()
err := client.Login(ctx, deviceCode, statusCallback)
token, err := client.EnsureValidToken(300)
```

**Key benefits**:
- Each test has isolated token storage (`t.TempDir()`)
- Each test can use its own `httptest.Server` URL
- Tests run in parallel for 5-10x speedup

### When to Use AuthClient vs Package Functions

| Scenario | Use |
|----------|-----|
| Unit/integration tests | `NewAuthClientWithDir(t.TempDir()).WithEndpoint(server.URL)` |
| CLI commands (production) | Package-level functions like `auth.Login()` |
| Tests needing isolation | Always use `AuthClient` |

## Anti-Patterns to Avoid

### 1. Separate Tests for Trivial Variations

```go
// Bad: 8 separate test functions
func TestIsNewerVersion_MajorDifference(t *testing.T) { ... }
func TestIsNewerVersion_MinorDifference(t *testing.T) { ... }
func TestIsNewerVersion_PatchDifference(t *testing.T) { ... }
// ... 5 more

// Good: One table-driven test
func TestIsNewerVersion(t *testing.T) {
    cases := []struct {
        a, b     string
        expected bool
    }{
        {"2.0.0", "1.0.0", true},
        {"1.1.0", "1.0.0", true},
        {"1.0.1", "1.0.0", true},
        {"1.0.0", "1.0.0", false},
    }
    // ...
}
```

### 2. Duplicate Precondition Tests

```go
// Bad: Same test repeated across files
func TestFunctionA_NotInGitRepo(t *testing.T) { ... }
func TestFunctionB_NotInGitRepo(t *testing.T) { ... }
func TestFunctionC_NotInGitRepo(t *testing.T) { ... }

// Good: Test precondition once, mock elsewhere
func TestNotInGitRepoHandling(t *testing.T) {
    // Test that findGitRoot returns "" outside git repo
    // Other tests can mock this behavior
}
```

### 3. Testing Implementation Instead of Behavior

```go
// Bad: Tests internal state
if len(daemon.connectionPool) != 3 { t.Error(...) }

// Good: Tests observable behavior
if resp, err := daemon.HandleRequest(req); err != nil { t.Error(...) }
```

### 4. I/O Heavy Tests Without Mocking

```go
// Bad: Actually executes commands in unit test
func TestDaemonFix(t *testing.T) {
    exec.Command("ox", "doctor").Run()
    // ...
}

// Good: Mock the execution or use integration test tag
func TestDaemonFix(t *testing.T) {
    executor := &mockExecutor{}
    fix := NewDaemonFix(executor)
    // ...
}
```

### 5. Missing Boundary Tests

```go
// Bad: Only tests middle values
TestPriority(1)  // works
TestPriority(2)  // works

// Good: Tests boundaries and invalid
TestPriority(-1) // invalid - expect error
TestPriority(0)  // boundary - min valid
TestPriority(4)  // boundary - max valid
TestPriority(5)  // boundary - first invalid
```

### 6. Slow Tests Without Short Mode Skip

```go
// Bad: Always runs 25-second delay, blocks make test
func TestLogin_SlowDown(t *testing.T) {
    t.Parallel()
    // ... test with slow_down retry that takes 25 seconds
}

// Good: Skips in short mode, only runs in make test-all
func TestLogin_SlowDown(t *testing.T) {
    if testing.Short() {
        t.Skip("skipping slow test (25s delay) in short mode")
    }
    t.Parallel()
    // ... test with slow_down retry
}
```

### 7. Using t.Setenv() Without Considering Parallelization

```go
// Bad: Blocks all parallelization in the package
func TestAPICall(t *testing.T) {
    t.Setenv("ENDPOINT", server.URL)  // Panics with t.Parallel()
    // ...
}

// Good: Use dependency injection pattern
func TestAPICall(t *testing.T) {
    t.Parallel()
    client := NewClientWithEndpoint(server.URL)
    // ...
}
```

## File Organization

```
cmd/ox/
├── doctor_test.go          # Tier 1: Fast unit tests
├── agent_test.go           # Tier 1: Agent logic
├── init_test.go            # Tier 2: Integration
├── hooks_test.go           # Tier 2: Hook installation
├── doctor_fix_test.go      # Tier 2: Fix operations
└── e2e_test.go             # Tier 3: Full workflow
```

## Build Tags

Use build tags to control which tests run:

```go
//go:build !slow
// Unit tests - run always

//go:build integration
// Integration tests - run on PR

//go:build e2e
// E2E tests - run on deploy
```

## ox Specific Test Coverage

### Well-Covered (Maintain)

| Area | Why It's Well-Tested |
|------|---------------------|
| `ox init` workflow | Creates project config - must work correctly |
| `ox doctor --fix` | Modifies user files - data integrity critical |
| Git operations | File tracking, commits - potential data loss |
| Hook installation | Integrates with AI agents - must be reliable |

### Needs Attention

| Area | Gap | Priority |
|------|-----|----------|
| API error handling | Network failures, auth expiry | Medium |
| Cross-platform paths | Windows vs Unix paths | Medium |
| Concurrent operations | Multiple ox instances | Low |

### Skip These

- Display formatting tests (manually verify)
- Simple getters/setters
- Tests that duplicate Go stdlib guarantees

## Test Intent vs Implementation

Tests encode **what should happen**, not **what the code does today**.

### The Litmus Test

For every test, ask: "If someone introduced a bug in the production code, would this test catch it?"

If the answer is "no, because the test reimplements the same logic," the test is theater.

### Anti-Patterns

| Anti-Pattern | Why It Fails | Fix |
|-------------|-------------|-----|
| Copy production gate into test | If gate removed from production, test still passes | Call production function, assert the outcome |
| Assert function doesn't touch unrelated dir | Trivially true for any function | Test the actual failure recovery path |
| Reimplement dedup/search logic in test | Production logic can break independently | Test the invariant the logic depends on |
| Wrap assertion in `if stat == nil` | Test passes vacuously when stat fails | Use unconditional `require.FileExists` |
| Test with `skipLFS=true` for "LFS failure" | Tests the skip path, not failure path | Use a scenario that triggers actual failure |

### Correct Patterns

```go
// WRONG: copies production gate
agentSessionID := ""
if agentSessionID != "" {
    WriteSessionMarker(...)  // never executes — tests nothing
}

// RIGHT: tests the function's own defense
err := WriteSessionMarker(&SessionMarker{AgentSessionID: ""})
require.Error(t, err, "must reject empty session ID")
```

```go
// WRONG: conditional assertion
if _, err := os.Stat(path); err == nil {
    assert.Equal(t, expected, actual)  // skipped if file missing
}

// RIGHT: unconditional assertion
require.FileExists(t, path, "file must exist after operation")
data, _ := os.ReadFile(path)
assert.Equal(t, expected, string(data))
```

```go
// WRONG: reimplements production dedup
existing := make(map[string]bool)
for _, s := range store1Sessions { existing[s.Name] = true }
for _, s := range store2Sessions {
    if !existing[s.Name] { ... }  // copies runSessionList logic
}

// RIGHT: tests the invariant dedup depends on
assert.Equal(t, sessions1[0].SessionName, sessions2[0].SessionName,
    "SessionName must be identical across stores for dedup to work")
```
