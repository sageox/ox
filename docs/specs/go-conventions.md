---
audience: ai
ai_editing: allowed
refreshable: true
---

# Go Conventions & Implementation Details

Detailed coding conventions for ox development. See [development-philosophy.md](../guides/development-philosophy.md) for high-level principles.

## Go Style

### General
- Follow [Effective Go](https://go.dev/doc/effective_go)
- Use `gofmt` and `golangci-lint`
- Explicit over implicit
- Composition over inheritance

### Naming
- `PascalCase` for exported identifiers
- `camelCase` for unexported identifiers
- `UPPER_CASE` for constants
- `kebab-case` for file names and CLI flags
- Descriptive names; avoid excessive abbreviations

### Error Handling
- Return errors, don't panic
- Use `errors.Is()` and `errors.As()` for error checking
- Create sentinel errors for expected error conditions
- Wrap errors with context: `fmt.Errorf("operation failed: %w", err)`

### Interfaces
- Keep interfaces small and focused (Interface Segregation Principle)
- Define interfaces where they're used, not where they're implemented
- Prefer composition of small interfaces over large monolithic ones

### Context
- Pass `context.Context` as the first parameter
- Use `cmd.Context()` in Cobra command handlers
- Don't store contexts in structs

## Logging

### Format
Single-line, key=value format for grepability:

```go
// Good
slog.Info("request completed", "method", method, "path", path, "status", status)

// Bad - multiline breaks grep and log aggregation
slog.Info(fmt.Sprintf("Request completed:\n  Method: %s\n  Path: %s", method, path))
```

### Error Logging
```go
// Good - preserves stack trace
slog.Error("operation failed", "error", err, "context", ctx)

// Bad - loses error chain
slog.Error(fmt.Sprintf("operation failed: %v", err))
```

### Levels
- `Debug`: Development details, verbose tracing
- `Info`: Normal operations, state changes
- `Warn`: Recoverable issues, degraded operation
- `Error`: Failures requiring attention

## CLI Design

### User Experience
- Optimize for both human and agent consumption
- Use `--json` flag for machine-readable output
- Provide helpful error messages with remediation steps
- Follow [12 Factor CLI Apps](https://medium.com/@jdxcode/12-factor-cli-apps-dd3c227a0e46)

### Flags and Arguments
- Use `kebab-case` for flag names
- Provide short flags for common operations (`-v` for `--verbose`)
- Use environment variables with `OX_` prefix as fallback

### Output
- Default to human-friendly output
- Support `--quiet` for scripts
- Support `--json` for programmatic access
- Use color sparingly and respect `NO_COLOR`

## Testing

### Framework
Use [testify](https://github.com/stretchr/testify) combined with standard library `testing`:

```go
import (
    "testing"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestFoo(t *testing.T) {
    result, err := Foo()
    require.NoError(t, err)           // fail immediately if error
    assert.Equal(t, "expected", result) // continue on failure
}
```

Key components:
- `testify/assert` - assertions that continue on failure
- `testify/require` - assertions that fail immediately
- `testify/mock` - for creating mock objects
- Standard `testing` - for test structure (`*testing.T`, `t.Run()`)

### Structure
```
pkg/
  foo/
    foo.go
    foo_test.go      # Unit tests
    foo_integration_test.go  # Integration tests (build tag)
```

### Conventions
- Table-driven tests with `t.Run()` for multiple cases
- Use `t.Helper()` for test helpers
- Mock external dependencies
- Test error paths, not just happy paths
- **Use `t.Parallel()` for test speed** - see [testing-implementation.md](testing-implementation.md)
- **Never use `t.Setenv()` with `t.Parallel()`** - use dependency injection instead
- **Slow tests (>500ms) must skip in short mode** - `if testing.Short() { t.Skip(...) }`
- **Test intent, not implementation** — see [testing-implementation.md](testing-implementation.md) for anti-patterns and correct patterns

### Test Tiers

| Tier | Command | When | What |
|------|---------|------|------|
| **fast** | `make test` | Every commit | Unit tests <500ms. No git clone, no network. |
| **full** | `make test-all` | Pre-PR (`make test-preflight`) | All unit tests including expensive ones (git clone, SQLite concurrent, LFS). |
| **slow** | `make test-slow` | Pre-PR | Tests requiring real ox binary (build tag: `slow`). No Claude needed. |
| **acceptance** | `make test-acceptance` | PR/release gate | Deterministic journeys through a freshly compiled ox binary. |
| **digital twin** | `make test-digital-twin` | PR/release gate | Hermetic auth API, ledger, and KB behavior. |
| **integration** | `make test-integration` | Compatibility evidence | E2E with real Claude sessions; attested gating is tracked by `ox-ilrr.4`. |
| **release** | `make test-release` | Version tag | Enforceable full + ratchet + slow + acceptance + twin gate. |

```bash
make test             # Fast tests — coding feedback loop (<30s)
make test-preflight   # Pre-PR gate: lint + full + slow (~3-5min)
make test-all         # All unit tests without build tags
make test-slow        # Real ox binary tests (build tag: slow)
make test-acceptance  # Deterministic current-source compiled-binary journeys
make test-digital-twin # Hermetic auth, ledger, and KB twins
make test-integration # Print external ox-test-harness guidance, then exit non-zero
make test-release     # Enforceable in-repo release tiers
make coverage         # Fast tests with coverage report
```

**Adding new tests:** If a test takes >500ms (git clone, network, polling timeouts), add:
```go
if testing.Short() {
    t.Skip("short: <reason>")
}
```

See [testing-implementation.md](testing-implementation.md) for detailed patterns including `AuthClient` for test parallelization.

## Security

### Input Validation
- Validate all external input
- Use allowlists over denylists
- Sanitize file paths to prevent traversal

### Secrets
- Never log secrets or tokens
- Use environment variables for sensitive config
- Clear sensitive data from memory when done

### Dependencies
- Keep dependencies minimal
- Run `go mod tidy` regularly
- Review dependency updates for security

## Project Structure

```
ox/
├── cmd/ox/           # CLI commands
├── internal/         # Private packages
│   ├── api/          # API client
│   ├── cli/          # CLI utilities
│   ├── config/       # Configuration
│   └── ...
├── pkg/              # Public packages (reusable by others)
│   └── agentx/       # Agent detection, command management, and content-hash stamping
├── docs/             # Documentation
└── Makefile          # Build and dev commands
```

## Make Targets

```bash
make build      # Build binary
make install    # Install to $GOPATH/bin
make test       # Run tests
make lint       # Run linter
make clean      # Clean build artifacts
```
