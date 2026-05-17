<!-- doc-audience: preserve-voice -->
---
component: session-list-screen
package: cmd/ox/session_list.go
since: 0.5.0
family: screen
renderer: freeze
exports: [runSessionList]
---

# Session-list screen

> Per-row list with session-specific badges: hydration state, duration coloring, agent type.

Live: `ox session list`. Catalog snapshot: `ox dev catalog --component=session-list-screen`. Companion narrative: [patterns/session-timeline.md](../patterns/session-timeline.md).

## When this is the right reference

A row-per-entity list where each row has a status the user might filter or sort by. The pattern: fixed-width column for the ID, secondary identity column (started/duration/turns), then a **state badge** with intent-aligned semantic color (`● live` in success, `⟳ stub` in warning, `✓ hydrated` in success), then a quiet agent column.

The badges aren't text styling — they're the row's *story* at a glance. Optimizing for scanning means the badges should be the first thing the eye lands on.

## When NOT to copy

Generic tabular data — use [Columns](columns.md). Multi-line content per row — use [Timeline](timeline.md) with one node per entry. Long lists (> 50 rows) — paginate or filter at the caller.

## Composition

Header row in `StyleDim`. ID column in `StyleAccent`. State badges use `StyleSuccess` / `StyleWarning` directly with their icon (`●`/`⟳`/`✓`). Agent column in `StyleSecondary` to dim it as supporting info.

Footer summarizes counts and offers the next-action: `5 sessions · 1 live · 3 hydrated · 1 stub · run \`ox session view <id>\``.

## Source

[`cmd/ox/session_list.go`](../../../cmd/ox/session_list.go)
