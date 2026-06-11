---
component: modal
package: internal/dashboard/overlays
since: 0.7.0
family: layout
renderer: freeze
exports: [Overlay, OverlayConfirm]
---

# Modal

> Floating overlay inside a full-screen TUI — help, command palette, confirmation gates.

## When to use

Inside a bubbletea program that needs to capture all keys for a transient interaction without losing position. Canonical instances:

- **Help overlay** (`?`) — three-column keymap reference.
- **Command palette** (`Ctrl+K`) — search-as-you-type with executable items.
- **Confirm overlay** — multi-step destructive operation gating.

Backdrop dims the underlying surface so focus is obvious.

## When NOT to use

One-shot commands — there's no underlying TUI to overlay. Use [Box](box.md) for static callouts, or `cli.ConfirmDangerousOperation` for inline destructive confirmations.

## Anatomy

`ox dev catalog --component=modal` (renders a command-palette example). In a live dashboard, hit `?` for help or `Ctrl+K` for palette.

## API

Source: [`internal/dashboard/overlays/overlay.go`](../../../internal/dashboard/overlays/overlay.go)

```go
type Overlay interface {
    View(width, height int) string
    Update(msg tea.Msg) (Overlay, tea.Cmd)
    IsOpen() bool
}
```

Implementations: `overlays/help/`, `overlays/palette/`, `OverlayConfirm` for confirmation flows.

## Accessibility & fallbacks

- Centered via `lipgloss.Place(width, height, Center, Center, content)`.
- Caller must restore focus to the previous pane on close.

## Related patterns

- [Dashboard pattern](../patterns/dashboard.md)
