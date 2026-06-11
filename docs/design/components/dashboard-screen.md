---
component: dashboard-screen
package: internal/dashboard/app
since: 0.7.0
family: screen
renderer: freeze
exports: [Model]
---

# Dashboard screen

> Full-screen 4-pane bubbletea TUI: Nav · Timeline · Inspector · Status bar.

Live: `ox dashboard`. Catalog snapshot: `ox dev catalog --component=dashboard-screen`. Composition narrative: [patterns/dashboard.md](../patterns/dashboard.md).

## When this is the right reference

You're building a new full-screen ox program and need a model of how multiple panes coexist:
- Exactly one [Pane](pane.md) is focused (double border, primary); others are unfocused (rounded, dim).
- [Nav tree](nav-tree.md) on the left anchors what the center pane shows.
- Center pane carries the work; top row holds [filter-tabs](filter-tabs.md) for that pane's slice.
- Right pane is a context-aware inspector (12+ target renderers in `internal/dashboard/panes/inspector/`).
- [Status bar](status-bar.md) on the bottom carries persistent state + active hint.
- [Modal](modal.md) overlays own the keys until dismissed.

## When NOT to copy

Single-purpose flows. The four-pane chrome assumes the user is *exploring*; if the user is *executing* (init, login, doctor), a wizard or destination command is the right shape — not a dashboard.

## Source

[`internal/dashboard/app/model.go`](../../../internal/dashboard/app/model.go) and the surrounding `app/` package. Panes live in [`internal/dashboard/panes/`](../../../internal/dashboard/panes/), overlays in [`internal/dashboard/overlays/`](../../../internal/dashboard/overlays/).
