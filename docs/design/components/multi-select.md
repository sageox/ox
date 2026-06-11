---
component: multi-select
package: internal/cli
since: 0.4.0
family: input
renderer: freeze
exports: [SelectMany, MultiSelectOption]
---

# Multi-select

> Checkbox group — pick zero-or-more from a known set.

## When to use

`ox init` agent-hook installation ("which AI coworkers to enable?"), multi-team enablement, feature opt-ins. Bubbletea-driven in a TTY; falls back to a numbered toggle prompt in non-TTY. Space toggles, enter confirms.

## When NOT to use

Exactly-one selection — use [Select](select.md) for radio-style single-pick. Free text — use [Prompt](prompt.md). Sets larger than ~15 items — paginate or filter at the caller before showing.

## Anatomy

`ox dev catalog --component=multi-select` or live in `ox init`. Checkboxes render as `[x]` (selected) and `[ ]` (unselected); cursor row prefixed with `▸`.

## API

Source: [`internal/cli/prompt.go`](../../../internal/cli/prompt.go)

```go
type MultiSelectOption struct {
    Label    string
    Value    string
    Default  bool   // pre-checked
    Disabled bool
}

cli.SelectMany(title string, options []MultiSelectOption) ([]string, error)
```

## Accessibility & fallbacks

- TTY: bubbletea with arrow keys + space toggle.
- Non-TTY / CI: numbered prompt asking the user to type comma-separated indices.
- Respects `--no-interactive` (errors out instead of hanging).

## Tests

[`internal/cli/prompt_test.go`](../../../internal/cli/prompt_test.go)
