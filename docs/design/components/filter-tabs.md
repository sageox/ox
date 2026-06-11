---
component: filter-tabs
package: internal/dashboard/panes/timeline
since: 0.7.0
family: input
renderer: freeze
exports: [renderFilterBar]
---

# Filter tabs

> Numbered tab row for quick single-key switching between filtered views.

## When to use

The narrow row above a content pane where the user toggles which slice of the data to see — timeline topic filters (All / WIP / Blocked / Decisions / Reviews), log-level filters, mode pickers. Number keys 1–9 jump tabs; pressing `/` opens an inline search line below the row.

## When NOT to use

More than ~6 tabs — the single-key shortcut breaks down past 9, and visual scanning starts to lose. Use [Select](select.md) for longer enumerations.

Section-level switches (Overview / Sync / Code / Sessions / Feed in the dashboard chrome) — that's a different pattern living on the top status bar, not in the content pane.

## Anatomy

`ox dev catalog --component=filter-tabs` or live in the dashboard timeline pane.

- Inactive tab: `[N]` in `NavDimStyle`, label in default foreground.
- Active tab: `[N]label` wrapped in `NavSelectedStyle` (bold, inverted).
- Search line (when open): `/ query█` with the `/` in `NavSectionStyle` and a block cursor.

## API

Source: [`internal/dashboard/panes/timeline/timeline.go`](../../../internal/dashboard/panes/timeline/timeline.go) (`renderFilterBar`)

The current implementation is private to the timeline pane. If a second consumer appears, lift to `internal/dashboard/components/filtertabs.go` rather than duplicate.

## Accessibility & fallbacks

- Bracketed numbers double as visible keyboard hints — no need to also render a "press N" legend.
- Active state survives `NO_COLOR` (background inversion is structural).

## Related patterns

- [Dashboard pattern](../patterns/dashboard.md)
