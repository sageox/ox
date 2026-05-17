<!-- doc-audience: preserve-voice -->
---
component: nav-tree
package: internal/dashboard/panes/nav
since: 0.7.0
family: layout
renderer: freeze
exports: [Pane, View]
---

# Nav tree

> Left-rail hierarchical navigation for full-screen TUIs.

## When to use

Workspaces, teams, sessions, recent activity — anywhere a TUI needs a persistent index of "what can I jump to?" on the left. Sections in `Secondary` color; items in `NavItem`; the selected row inverts to `NavSelected` (bold dark text on primary background). Cursor moves with arrow keys; `e` expands/collapses; `enter` jumps.

## When NOT to use

Flat lists belong in [Columns](columns.md). Trees deeper than 3 levels become their own navigation problem — paginate or filter at the caller first.

## Anatomy

`ox dev catalog --component=nav-tree` or live in `ox dashboard`. Section headers + indented items + inverted selection.

## API

Source: [`internal/dashboard/panes/nav/nav.go`](../../../internal/dashboard/panes/nav/nav.go)

```go
type Pane struct { /* ... */ }

p := nav.New(...)
p.View(width, height int) string
p.SetNodes(nodes []domain.NavNode)
```

Theme styles live in [`internal/dashboard/theme/styles.go`](../../../internal/dashboard/theme/styles.go): `NavSectionStyle`, `NavItemStyle`, `NavSelectedStyle`, `NavDimStyle`.

## Accessibility & fallbacks

- Selected row uses inverted color, so `NO_COLOR` callers still see the contrast (background swap survives).
- Long item names truncate via `cli.TruncateID` rather than wrap.

## Related patterns

- [Dashboard pattern](../patterns/dashboard.md)
