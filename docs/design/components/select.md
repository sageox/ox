<!-- doc-audience: preserve-voice -->
---
component: select
package: internal/cli
since: 0.4.0
family: input
renderer: asciinema
exports: [SelectOne, SelectOneValue, SelectMany]
---

# Select

> Pick one or many from a known set of options.

## When to use

Arrow-key navigation in a TTY; numbered-prompt fallback in pipes and CI — same contract either way. Generic `SelectOneValue[T]` avoids index-juggling at call sites. Use `SelectMany` for checkbox-style multi-pick.

## When NOT to use

Free-form text (use [Prompt](prompt.md)) or yes/no (use [Confirm](confirm.md)). Lists longer than ~20 items — paginate or filter at the caller before showing.

## Anatomy

`ox dev catalog --component=select` (animated recording shows arrow-key navigation) or [sageox-design.netlify.app/catalog/cli/#c-select](https://sageox-design.netlify.app/catalog/cli/#c-select).

## API

Source: [`internal/cli/select.go`](../../../internal/cli/select.go), [`internal/cli/prompt.go`](../../../internal/cli/prompt.go)

```go
cli.SelectOne(title string, options []string, defaultIdx int) (int, error)
cli.SelectOneValue[T comparable](title string, options []cli.SelectOption[T], defaultIdx int) (T, error)
cli.SelectMany(title string, options []cli.MultiSelectOption) ([]string, error)
```

## Accessibility & fallbacks

- Bubbletea-backed in TTY; falls back to a numbered prompt in non-TTY automatically.
- Respects `--no-interactive` flag and CI env vars.

## Tests

[`internal/cli/select_test.go`](../../../internal/cli/select_test.go), [`internal/cli/prompt_test.go`](../../../internal/cli/prompt_test.go)
