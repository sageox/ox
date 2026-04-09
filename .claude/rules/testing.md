---
paths:
  - "**/*_test.go"
  - "tests/**"
---

# Testing Philosophy: E2E Reality Over Unit Isolation

**A feature is not done until it works in a real session.** Unit tests verify logic; integration tests verify reality. E2E is the release gate.

## Test Tiers

| Tier | Command | When |
|------|---------|------|
| Fast (<500ms) | `make test` | Every commit. Target: <30s |
| Full (expensive) | `make test-all` | Before PRs. Includes git clone, SQLite concurrent, LFS repair |
| Slow (real binary) | `make test-slow` | Build tag: `slow`. No agent needed |
| Integration (real sessions) | `make test-integration` | Release gate. Lives in `sageox/ox-test-harness` |
| Pre-PR gate | `make test-preflight` | lint + full + slow (~3-5min) |

**Output:** Makefile is quiet by default. Use `V=1 make test` for verbose.

## Slow Test Guard

Any test >500ms must skip in short mode:
```go
if testing.Short() {
    t.Skip("short: <reason — git clone, polling timeout, etc.>")
}
```

## Core Principles

**No test theater.** Each test must answer: "What real-world failure does this prevent?"

**Test intent, not implementation.** Write the assertion first based on the requirement, then build the scenario. If a test would pass even with broken code, it's not testing anything.

**Failure-mode tests are required.** For every function/bug fix, test: (1) side-effecting functions with the side effect skipped, (2) search functions with the target in each possible location, (3) multi-step pipelines where step N fails — step N-1 output must survive.

**Organize by behavioral domain, not by function.** Name tests after the failure they prevent. Include a one-line comment stating what breaks without this test.

```go
// GOOD: organized by domain, documents the failure
// --- A. Baseline lifecycle ---
// TestBuildBaseline_FlagReleasedOnFailure verifies the baseline flag is always
// released, even when indexing fails.
// Failure prevented: transient failure permanently wedges baseline indexing.
```

**Test independent lifecycles independently.** Prove subsystems don't interfere: (1) one running doesn't block the other, (2) one failing doesn't corrupt the other, (3) one's disappearance doesn't affect the other.

**Concurrency is a first-class test axis.** Test: (1) no deadlocks, (2) flags released under all exit paths, (3) readers see consistent data during writes. Use test hooks, not `time.Sleep`:

```go
// GOOD: deterministic control via hook
release := make(chan struct{})
mgr.testHook = func() { <-release }
go mgr.BuildBaseline(ctx, path)
close(release)

// BAD: hoping timing works out
time.Sleep(100 * time.Millisecond)
```

**Test graceful defaults before initialization.** Stats(), Status() etc. must return clean zero-values before the subsystem has run.

## Anti-Patterns

- Copying production gates into test bodies (gate removal = test still passes)
- Testing that a function doesn't touch unrelated directories (trivially true)
- Reimplementing production logic in the test instead of calling production code
- Wrapping assertions in conditionals (`if stat == nil { assert... }` — passes vacuously)

## File Decomposition

When a `_test.go` exceeds ~1000 lines, split by behavioral domain. Name files after the domain (e.g., `whisper_format_test.go`), not the function.

## Coverage

Target: 85%+ for internal packages. Check: `go test ./internal/... -coverprofile=coverage.out && go tool cover -func=coverage.out | grep total`

## Bug Fix Regression Tests

Every bug fix MUST include a regression test. Reproduce exact conditions; test must fail without fix, pass with it. Integration-level regressions go in `sageox/ox-test-harness`.

## Handling Test Failures

**DO NOT automatically rollback code** when tests fail. Check with the user first.
- If user intentionally changed behavior → update the tests
- If tests have wrong assumptions → update the tests, not the code
- NEVER revert new code just to make tests pass

**Test helpers:** `config.CreateInitializedProject(t)`, `config.CreateInitializedProjectWithConfig(t, cfg)`, `config.RequireSageoxDir(t, path)`
