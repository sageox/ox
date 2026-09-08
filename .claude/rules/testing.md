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
| Full (expensive) | `make test-all` | Before PRs. Includes git clone, SQLite concurrent, LFS pointer verification |
| Slow (real binary) | `make test-slow` | Build tag: `slow`. No agent needed |
| Acceptance (compiled binary) | `make test-acceptance` | Deterministic PR/release gate |
| Digital twins | `make test-digital-twin` | Hermetic auth, ledger, and KB PR/release gate |
| Integration (real sessions) | `make test-integration` | External compatibility evidence; attested gating tracked by `ox-ilrr.4` |
| Release | `make test-release` | Enforceable in-repo full + ratchet + slow + acceptance + twin gate |
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

## Failure Paths That Render Identically To Success

**The shape:** a fault and a healthy state produce the same value, so nothing
announces itself. The test passes, the check reports clean, the guard does not
fire — and none of it is evidence, because the failing path and the working path
are indistinguishable at the point you are looking.

Six instances landed on one branch in one afternoon. The individual fixes do not
teach the pattern, so it is written down here.

| Mechanism | Renders as | Real consequence |
|---|---|---|
| `os.Chmod(0o000)` on Windows | unreadable file → readable | `require.Error` fails; the test asserted nothing |
| `os.Symlink` skip-then-`Fatalf` | isolation absent → isolation present | the test fails instead of skipping, on the one platform it was not written for |
| `PATH` joined with `":"` on Windows | fake binary unreachable → fake installed | the REAL `git credential reject` ran against a live credential store |
| `filepath.IsAbs("/etc/x")` on Windows | rejected path → accepted | a trust boundary strictly weaker on one platform |
| write failure reported as neither written nor unprotected | "nothing to protect" | `Apply`'s gate never fired; vendor files materialized visible to git |
| `git ls-files` error read as empty | unreadable repo → clean repo | doctor reported "no ox-managed files tracked" for a repo it could not read |

### The rules

1. **A test that isolates itself with a platform-specific mechanism must either
   skip explicitly where the mechanism does not hold, with a comment saying why,
   or assert the isolation rather than assume it.** `os.Chmod`, `os.Symlink`,
   `PATH` manipulation, path-absoluteness, process signals, and file permissions
   are all in this class.

2. **Prefer an honest skip to a port nobody can exercise.** A port written blind
   for a platform the author cannot run is how the next fail-open is authored.

3. **Never make an error mean the same thing as an empty success.** Distinguish
   "there is nothing here" from "I could not look". If a lookup fails, say so;
   returning an empty list makes the caller act on a false premise.

4. **Fix the product, not the test.** When a test catches a platform-specific
   weakness, the tempting repair is to make the test match the platform. That
   turns the job green and leaves the hole open. `/etc/skills` stayed in the
   refuse set; the guard was hardened instead.

### Portable failure injection

`os.Chmod` no-ops on Windows and `os.Symlink` needs privileges there. When a test
needs a read or write to fail, use a shape that fails on every platform:

- a **directory where a file is expected** — `ReadFile`/`WriteFile` fail (`EISDIR`)
- a **regular file where a directory component is needed** — `MkdirAll` fails (`ENOTDIR`)
- **corrupt bytes** where a parser expects structure — e.g. a garbage `.git/index`

These are also realistic: a bad merge, an interrupted extraction, or a stray
`mkdir` all produce them.

**Confirm which gate you actually hit.** The lever trips whichever check reads the
path first. Making `.git/info/sparse-checkout` a directory fails an *earlier*
readability check and never reaches the repair branch you meant to cover. A
passing test is not evidence you reached the branch you named.

### Verify a red result, not only a green one

A red-first proof is the cheapest way to find out which branch a test actually
reaches — twice on this branch it exposed a test that passed while exercising
nothing. But a red result needs its mechanism checked too: an assertion capturing
**stdout** went red against a warning that `cli.PrintWarning` writes to **stderr**,
which reads exactly like "the code stayed silent." Trusting that red would have
meant "fixing" a bug that did not exist.

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

Every bug fix MUST include a regression test that **fails without the fix and passes with it** — verify this explicitly (neuter the fix, watch the test go red, restore it). A test that passes both ways guards nothing.

**Test the CLASS of failure, not the specific patch.** A bug is one instance of a broader failure mode. The patch fixes the instance; the test suite must cover the class. Before writing the test, ask: *what is the general failure here, and where else can it occur?* Then test those too.

- Generalize the trigger: the reported wedge was a stale `rebase-merge`, but the class is "the repo is stuck in a persistent git state that blocks sync." So also cover `rebase-apply`, an aborted merge/cherry-pick, leftover lock files — same recovery contract, different entry point.
- Generalize the input axis: if a bug hit one path/encoding/timing, test the target in *each* possible location/state, not only the one that broke.
- Name the test after the class it prevents, not the ticket number.

**Simulate the ENVIRONMENT the failure occurred in, not a stripped-down stub.** Reproduce the conditions that produced the bug — real subprocesses, real divergent history, real concurrent writers, real multi-cycle loops — so the test exercises the actual interaction, not a mock that can't reproduce it. Build up reusable harnesses (a real wedged-repo factory, a bare-remote-plus-clone fixture) that future tests in the same domain reuse; this makes the suite progressively more cross-cutting instead of a pile of isolated unit checks.

- A unit test on a helper (e.g. "does `RebaseAge` read the mtime") does NOT prove the daemon recovers — it would pass even if recovery were never wired in. Drive the actual decision path.
- Prove the system *makes progress after recovery* (un-wedged AND the next pull reconciles), not merely that one function returned the right value.
- Where a true end-to-end belongs above the unit layer, put the integration regression in `sageox/ox-test-harness`.

**The intent is what's under test — the durable behavior the user relies on — across the whole class of bugs, not a snapshot of one code path.** Optimize each regression test to catch the *next* variant of the same mistake, not just a re-run of the exact one already fixed.

## Handling Test Failures

**DO NOT automatically rollback code** when tests fail. Check with the user first.
- If user intentionally changed behavior → update the tests
- If tests have wrong assumptions → update the tests, not the code
- NEVER revert new code just to make tests pass

**Test helpers:** `config.CreateInitializedProject(t)`, `config.CreateInitializedProjectWithConfig(t, cfg)`, `config.RequireSageoxDir(t, path)`
