<!-- doc-audience: preserve-voice -->

# ox · Component catalog

Every reusable UX primitive in the ox CLI, with anatomy, when-to-use guidance, source pointers, and a recording.

The catalog is also a runnable program. Run it to see live output in your terminal:

```bash
ox dev catalog                       # render all components
ox dev catalog --component=timeline  # just one
ox dev catalog --json                # machine-readable manifest
```

`ox dev` is a hidden command — by design, end users and AI coworkers shouldn't trip over it during ordinary workflows. The published browser-rendered version lives at [sageox-design.netlify.app/2026-05-17-ox-cli-component-catalog/](https://sageox-design.netlify.app/2026-05-17-ox-cli-component-catalog/).

## Components

| Name | Family | Source | Spec |
|------|--------|--------|------|
| Box | layout | [`internal/ui/box.go`](../../internal/ui/box.go) | [components/box.md](components/box.md) |
| Columns | layout | [`internal/cli/columns.go`](../../internal/cli/columns.go) | [components/columns.md](components/columns.md) |
| Modal | layout | [`internal/dashboard/overlays/`](../../internal/dashboard/overlays/) | [components/modal.md](components/modal.md) |
| Nav tree | layout | [`internal/dashboard/panes/nav/`](../../internal/dashboard/panes/nav/) | [components/nav-tree.md](components/nav-tree.md) |
| Pane | layout | [`internal/dashboard/theme/styles.go`](../../internal/dashboard/theme/styles.go) | [components/pane.md](components/pane.md) |
| Wordmark | layout | [`internal/ui/wordmark.go`](../../internal/ui/wordmark.go) | [components/wordmark.md](components/wordmark.md) |
| Log formatter | data-display | [`internal/cli/logfmt.go`](../../internal/cli/logfmt.go) | [components/log-formatter.md](components/log-formatter.md) |
| Markdown | data-display | [`internal/ui/markdown.go`](../../internal/ui/markdown.go) | [components/markdown.md](components/markdown.md) |
| Timeline | data-display | [`internal/ui/timeline.go`](../../internal/ui/timeline.go) | [components/timeline.md](components/timeline.md) |
| Sparkline | viz | [`internal/tui/sparkline.go`](../../internal/tui/sparkline.go) | [components/sparkline.md](components/sparkline.md) |
| Confirm | input | [`internal/cli/confirm.go`](../../internal/cli/confirm.go) | [components/confirm.md](components/confirm.md) |
| Filter tabs | input | [`internal/dashboard/panes/timeline/`](../../internal/dashboard/panes/timeline/) | [components/filter-tabs.md](components/filter-tabs.md) |
| Multi-select | input | [`internal/cli/prompt.go`](../../internal/cli/prompt.go) | [components/multi-select.md](components/multi-select.md) |
| Prompt | input | [`internal/cli/prompt.go`](../../internal/cli/prompt.go) | [components/prompt.md](components/prompt.md) |
| Select (radio) | input | [`internal/cli/select.go`](../../internal/cli/select.go) | [components/select.md](components/select.md) |
| Spinner | feedback | [`internal/cli/spinner.go`](../../internal/cli/spinner.go) | [components/spinner.md](components/spinner.md) |
| Status bar | feedback | [`internal/dashboard/panes/statusbar/`](../../internal/dashboard/panes/statusbar/) | [components/status-bar.md](components/status-bar.md) |

## Patterns

Composite patterns showing how multiple components compose into the real CLI surfaces users see:

- [patterns/doctor-output.md](patterns/doctor-output.md) — how `ox doctor` composes Box + Timeline + summary
- [patterns/status-dashboard.md](patterns/status-dashboard.md) — how `ox status` composes Sparkline + Columns + Box
- [patterns/session-timeline.md](patterns/session-timeline.md) — how session views compose
- [patterns/dashboard.md](patterns/dashboard.md) — `ox dashboard` 4-pane TUI (Pane + Nav tree + Timeline + Inspector + Status bar + Modal)
- [patterns/config-editor.md](patterns/config-editor.md) — `ox config` full-screen TUI form (scoped editor with categorized fields)
- [patterns/init-wizard.md](patterns/init-wizard.md) — `ox init` inline multi-step wizard (Select + Multi-select + Confirm + Timeline)
- [patterns/login-flow.md](patterns/login-flow.md) — `ox login` OAuth flow (Box + Spinner)

## Theming

See [theming.md](theming.md) for how the palette flows from `sageox-design` upstream into `internal/theme/generated.go`. See [tokens.md](tokens.md) for the full semantic token reference.

## Export

`make catalog-export` produces `dist/design-catalog/` (text-only — `.cast`, `.svg`, `.html`, `.json`, vendored asciinema-player JS/CSS). `make publish-catalog` rsyncs that bundle into `sageox-design/bogota-v2/proposals/2026-05-17-ox-cli-component-catalog/`. A human runs `bash scripts/publish-mockups.sh` (or the `/publish-design-mockups` Claude skill) to deploy.
