<!-- doc-audience: preserve-voice -->
---
component: spinner
package: internal/cli
since: 0.3.0
family: feedback
renderer: freeze
exports: [WithSpinner, WithSpinnerNoResult]
---

# Spinner

> Async-operation indicator with auto-hide for fast paths.

## When to use

Wrap any operation expected to take > 300ms (network, git clone, daemon RPC). `WithSpinner[T]` returns the function's value; `WithSpinnerNoResult` returns only an error. Auto-hides if the work completes in < 300ms, so fast paths don't flicker.

## When NOT to use

Operations under 100ms (the spinner appears, blinks, vanishes — noise without value). Non-TTY environments: the spinner falls back silently (correct), but it also means progress isn't visible. For long batch operations consider periodic log lines instead.

## Anatomy

`ox dev catalog --component=spinner` — static snapshot showing the in-progress + done frames. The actual spinner animation only exists in live commands (e.g. `ox sync`); a single-frame .cast adds player chrome without showing motion, which the catalog deliberately avoids per [.claude/rules/design.md](../../../.claude/rules/design.md) ("only .cast when there is actual motion").

## API

Source: [`internal/cli/spinner.go`](../../../internal/cli/spinner.go)

```go
cli.WithSpinner[T any](message string, fn func() (T, error)) (T, error)
cli.WithSpinnerNoResult(message string, fn func() error) error
```

## Accessibility & fallbacks

- Bubbletea spinner in TTY; silent in non-TTY and CI.
- Respects `--no-interactive` and `NO_COLOR`.

## Tests

`internal/cli/` (spinner is integration-tested through commands that wrap it).
