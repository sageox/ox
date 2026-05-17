<!-- doc-audience: preserve-voice -->
---
component: status-screen
package: cmd/ox/status.go
since: 0.4.0
family: screen
renderer: freeze
exports: [renderTable]
---

# Status screen

> Tufte-minimal section-table pattern. Bold-secondary section headers, dim left-column labels, semantic-colored values, no borders.

Live: `ox status`. Catalog snapshot: `ox dev catalog --component=status-screen`. Composition narrative: [patterns/status-dashboard.md](../patterns/status-dashboard.md).

## When this is the right reference

Grouped key/value facts where each section is a category and each row is a label → value. Used by `ox status`, `ox teams`, `ox murmur status` — the consistency itself is the design.

Borders are deliberately absent. Section headers do the grouping; whitespace does the separation. This is the most-reached-for screen pattern in ox; copy it for any new informational command.

## When NOT to copy

Wide tabular data with > 3 columns — use [Columns](columns.md). Long-form or hierarchical state — use [Timeline](timeline.md). One-shot status lines — `cli.StyleSuccess.Render` inline is enough.

## Composition

Section headers in `StyleSecondary.Bold`. Labels (left column, 22-char pad) in `StyleDim`. Values in `StyleAccent` for facts, `StyleSuccess` / `StyleWarning` for state. Optional [Sparkline](sparkline.md) for time-bucketed activity.

## Source

[`cmd/ox/status.go`](../../../cmd/ox/status.go) — `renderTable` (and the same shape duplicated in `teams.go`, `murmur_status.go`; lift to `internal/cli/sectable.go` if a fourth consumer appears).
