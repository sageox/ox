<!-- doc-audience: preserve-voice -->

# Pattern · `ox config` editor (full-screen TUI)

How the interactive config editor composes catalog primitives.

## Composition

```
┌──────────────────────────────────────────────────────────────┐
│  ox config — settings editor                                 │
│  Scope: ( ) local  (•) user  ( ) global       [Tab to switch]│
├──────────────────────────────────────────────────────────────┤
│  GENERAL                                                     │
│   ▸ default endpoint           api.sageox.ai                 │
│     auto-sync interval         5m                            │
│     pager                      less -R                       │
│                                                              │
│  PRIVACY                                                     │
│     murmur visibility          team                          │
│     redaction profile          aggressive                    │
│                                                              │
│  AGENTS                                                      │
│     claude-code hooks          [x] enabled                   │
│     codex-cli hooks            [ ] enabled                   │
├──────────────────────────────────────────────────────────────┤
│ ↑/↓ navigate · Enter edit · Tab switch scope · s save · esc  │
└──────────────────────────────────────────────────────────────┘
```

## Key decisions

- **Three scopes via a radio row.** Local / user / global picked through a [Select](../components/select.md)-style row at the top. `Tab` cycles; the active scope's values populate the body. Inactive-scope values are hidden — fall back to read-only previews if mixed editing is ever needed.
- **Categories as section headers.** GENERAL / PRIVACY / AGENTS / ENDPOINTS / DAEMON. Section style mirrors [Nav tree](../components/nav-tree.md)'s `NavSectionStyle` so the visual vocabulary is consistent.
- **Inline editors per field.** Pressing `Enter` on a row swaps the value column with the matching input widget: [Prompt](../components/prompt.md) for free text, [Select](../components/select.md) for enums, [Confirm](../components/confirm.md) for booleans, [multi-select](../components/multi-select.md) for sets. The catalog primitive does the input; the config TUI handles persistence.
- **Save is explicit.** `s` writes; `esc` discards. The dashboard's [status-bar](../components/status-bar.md) pattern carries the "unsaved" state in the bottom right while editing.

## Source

- [`cmd/ox/config_tui.go`](../../../cmd/ox/config_tui.go) — `configModel`

## Components used

[Select](../components/select.md) (scope picker, enum fields) · [Multi-select](../components/multi-select.md) (set-valued fields) · [Prompt](../components/prompt.md) (free-text fields) · [Confirm](../components/confirm.md) (boolean fields) · [Nav tree](../components/nav-tree.md) (section styling) · [Status bar](../components/status-bar.md) (save state)
