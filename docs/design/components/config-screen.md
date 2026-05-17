<!-- doc-audience: preserve-voice -->
---
component: config-screen
package: cmd/ox/config_tui.go
since: 0.6.0
family: screen
renderer: freeze
exports: [configModel]
---

# Config screen

> The full `ox config` interactive TUI — bordered frame, categorized settings, cursor selection, scope-precedence arrows.

## When this is the right reference

You're building a new interactive ox screen and need a model of what a composed surface looks like end-to-end: chrome (border + overlaid title), structure (categorized rows), interactivity (cursor + value column), and the precedence indicator pattern (`← local`, `← user`, `← default`) showing where each value comes from.

## When NOT to copy this

For inline commands. The frame and per-row source arrows assume a controlled full-screen surface. Don't fake the chrome inside scrollable output.

For *primitives*. This is a screen, not a reusable building block. If you find yourself extracting parts of it for reuse, lift them to `internal/dashboard/` first (as components, not full screens), then update both call sites.

## Anatomy

```
╭─ ox config ──────────────────────────────────────────────────────╮
│  GENERAL                                                          │
│    default.endpoint           api.sageox.ai            ← user     │
│  > auto-sync.interval         5m                       ← local    │
│    pager                      less -R                  ← default  │
│                                                                   │
│  PRIVACY                                                          │
│    murmur.visibility          team                     ← user     │
│    ...                                                            │
│  ────────────────────────────────────────────────────             │
│  auto-sync.interval                                               │
│  How often the daemon pulls upstream changes.                     │
│  Scopes: default 1m  → local 5m  user —  global —                 │
│                                                                   │
│  ↑/↓ navigate · Enter edit · Tab switch scope · s save · esc quit │
╰───────────────────────────────────────────────────────────────────╯
```

Live: `ox dev catalog --component=config-screen`. Real version: `ox config`.

## Source

[`cmd/ox/config_tui.go`](../../../cmd/ox/config_tui.go) — `configModel` (bubbletea Model)

## Composition

Built from these catalog primitives:

- [Box](box.md) — the rounded frame chrome.
- [Wordmark](wordmark.md) — *not used here* (the config screen is a working surface, not a destination — banner-free by design).
- [Nav tree](nav-tree.md)-style category headers (`NavSectionStyle`).
- [Select](select.md) (radio) — inline value editor for enum-typed fields.
- [Multi-select](multi-select.md) — inline editor for set-valued fields.
- [Prompt](prompt.md) — inline editor for free-text fields.
- [Confirm](confirm.md) — inline editor for booleans.
- [Status bar](status-bar.md)-style help footer at the bottom.

## Related

- [`patterns/config-editor.md`](../patterns/config-editor.md) — the narrative companion to this visual.
