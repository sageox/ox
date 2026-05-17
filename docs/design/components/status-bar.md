<!-- doc-audience: preserve-voice -->
---
component: status-bar
package: internal/dashboard/panes/statusbar
since: 0.7.0
family: feedback
renderer: freeze
exports: [Pane, View]
---

# Status bar

> Bottom row of a full-screen TUI: persistent state on the left, active hint on the right.

## When to use

Full-screen bubbletea programs (`ox dashboard`, `ox config`). Left half carries always-on state: daemon health (`●` online, `⬡` offline, `⚠ warning`), open-issue count, version. Right half carries the active hint string for the focused context ("? for help", "esc to cancel"). Transient toasts ("copied!", "synced 12s") swap the right side for ~2 seconds.

## When NOT to use

Inline commands — a status bar needs the bottom row of a controlled screen. Don't fake it in one-shot output where it'll scroll off.

## Anatomy

`ox dev catalog --component=status-bar`. Single line; left + right segments separated by computed space.

## API

Source: [`internal/dashboard/panes/statusbar/statusbar.go`](../../../internal/dashboard/panes/statusbar/statusbar.go)

```go
sb := statusbar.New(...)
sb.View(width int) string
sb.SetTransient(message string, ttl time.Duration)
```

## Accessibility & fallbacks

- Single-line; never wraps. Width-aware via `lipgloss.Width()`.
- Respects `NO_COLOR` — semantic glyphs (`●`/`⚠`) stay; color drops.

## Related patterns

- [Dashboard pattern](../patterns/dashboard.md)
