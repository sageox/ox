<!-- doc-audience: preserve-voice -->
---
component: pane
package: internal/dashboard/theme
since: 0.7.0
family: layout
renderer: freeze
exports: [PaneBorderFocused, PaneBorderUnfocused]
---

# Pane

> Bordered region in a full-screen TUI with focused / unfocused styling.

## When to use

Full-screen bubbletea programs that split work across regions: `ox dashboard`, `ox config` (TUI mode), session viewers. The **focused** pane gets a double border in `ColorPrimary`; **unfocused** panes get a rounded border in `ColorDim`. Tab swaps focus.

## When NOT to use

One-shot command output — a bordered region steals vertical space without paying it back. Use [Box](box.md) for static callouts that don't compete for focus.

## Anatomy

`ox dev catalog --component=pane` (shows focused + unfocused side-by-side) or live in `ox dashboard`.

## API

Source: [`internal/dashboard/theme/styles.go`](../../../internal/dashboard/theme/styles.go)

```go
theme.PaneBorderFocused    // lipgloss.Style — double border, primary color
theme.PaneBorderUnfocused  // lipgloss.Style — rounded border, dim
```

Caller composes width/height/padding on top:

```go
focused := theme.PaneBorderFocused.Padding(0, 2).Width(40).Height(20)
focused.Render(content)
```

## Accessibility & fallbacks

- Border characters require UTF-8.
- Focused/unfocused contrast survives `NO_COLOR` (double vs rounded border still differs).

## Tests

Covered through dashboard pane integration tests in `internal/dashboard/panes/`.

## Related patterns

- [Dashboard pattern](../patterns/dashboard.md) — composition of multiple panes.
