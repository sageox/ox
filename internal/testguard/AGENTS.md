<!-- doc-audience: ai -->

# testguard: Test Environment Isolation

Tests that run `ox` as a subprocess MUST use `testguard.RunOx` or `testguard.OxCmd`. **NEVER** use `exec.Command` + `os.Environ()` to run `ox` in tests -- this leaks the developer's real auth tokens, daemon sockets, and `SAGEOX_ENDPOINT`, causing tests to hit production infrastructure.

## Rules

- `testguard.RunOx(t, oxBin, dir, envVars, args...)` for running ox subprocesses
- `testguard.SafeMockServer(t, handler)` for mock API servers (validates responses don't contain production URLs)
- `testguard.BuildOxBinary(t, projectRoot)` for building the ox binary in tests
- `make lint-test-env` checks for unguarded `os.Environ()` in test files
- If `os.Environ()` is genuinely needed (e.g., `go build`), annotate with `// safe: <reason>`

```go
// WRONG: leaks developer's real environment
cmd := exec.Command(oxBin, "doctor")
cmd.Env = append(os.Environ(), "EXTRA=val")

// RIGHT: isolated environment, production URLs blocked
output, exitCode, dur := testguard.RunOx(t, oxBin, dir, envVars, "doctor")
```

## What testguard provides

| Function | Purpose |
|----------|---------|
| `RunOx` | Execute ox subprocess with isolated env, 60s timeout |
| `OxCmd` / `OxCmdContext` | Build isolated `exec.Cmd` for ox |
| `MinimalEnv` | Allowlisted env vars + `OX_NO_DAEMON=1` |
| `SafeMockServer` | Mock HTTP server that rejects production URLs in responses |
| `BuildOxBinary` | Compile ox binary (only place `os.Environ()` is acceptable) |
| `StopDaemonCleanup` | Register t.Cleanup to stop any test daemon |

## Production URL validation

- Env values are checked against `productionHostPatterns` (`sageox.ai`)
- `test.sageox.ai` and `localhost` are exempted via `allowedTestHostPatterns`
- Mock server responses are NOT exempted -- mocks must use `localhost` URLs
