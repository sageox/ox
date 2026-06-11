# Pattern · Dashboard (full-screen TUI)

How `ox dashboard` and other bubbletea full-screen programs compose catalog primitives.

## Composition

```
┌──────────────────────────────────────────────────────────────┐
│  ╭ nav ╮ │ ╭═ timeline (focused) ═╮ │ ╭ inspector ╮          │
│  │ ... │ │ ║ filter tabs · search  ║ │ │ scrollable detail │  │
│  │ ... │ │ ║ entries...            ║ │ │ for selected      │  │
│  │ ... │ │ ║                       ║ │ │ target            │  │
│  ╰─────╯ │ ╰═══════════════════════╯ │ ╰───────────────────╯  │
├──────────────────────────────────────────────────────────────┤
│ ● daemon · 2 stale · ox 0.9.0       Tab pane · ? help · q    │
└──────────────────────────────────────────────────────────────┘
                              ╭─ modal overlay (Ctrl+K) ─╮
                              │ ⌘ Command palette         │
                              │ > sync                    │
                              │ ▸ Sync ledger             │
                              │   Sync all repos          │
                              ╰───────────────────────────╯
```

## Key decisions

- **Focus is visible.** Exactly one [Pane](../components/pane.md) at a time uses `PaneBorderFocused` (double border, primary); the rest use `PaneBorderUnfocused` (rounded, dim). Tab swaps focus.
- **Left rail is the index.** [Nav tree](../components/nav-tree.md) carries workspaces, teams, and recent activity. The selected nav row drives what the inspector pane shows.
- **Center pane carries the work.** Timeline, code, sync, sessions — whatever the active section is. Section number keys (1–5) jump.
- **Right pane is context-aware.** The inspector renders the selected target — session, workspace, issue. Twelve+ target renderers live in `internal/dashboard/panes/inspector/`.
- **Bottom row never lies.** [Status bar](../components/status-bar.md) reflects real daemon state; transient toasts override the right hint for ~2s.
- **Overlays own the keys.** [Modals](../components/modal.md) (help `?`, palette `Ctrl+K`) consume input until dismissed; underlying surface dims.

## Source

- Root model: [`internal/dashboard/app/model.go`](../../../internal/dashboard/app/model.go)
- Panes: [`internal/dashboard/panes/`](../../../internal/dashboard/panes/)
- Overlays: [`internal/dashboard/overlays/`](../../../internal/dashboard/overlays/)
- Theme: [`internal/dashboard/theme/`](../../../internal/dashboard/theme/)

## Components used

[Pane](../components/pane.md) · [Nav tree](../components/nav-tree.md) · [Timeline](../components/timeline.md) · [Sparkline](../components/sparkline.md) · [Status bar](../components/status-bar.md) · [Modal](../components/modal.md) · [Markdown](../components/markdown.md) · [Columns](../components/columns.md)
