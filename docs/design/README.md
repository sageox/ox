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
| Timeline | data-display | [`internal/ui/timeline.go`](../../internal/ui/timeline.go) | [components/timeline.md](components/timeline.md) |
| Sparkline | viz | [`internal/tui/sparkline.go`](../../internal/tui/sparkline.go) | [components/sparkline.md](components/sparkline.md) |
| Markdown | data-display | [`internal/ui/markdown.go`](../../internal/ui/markdown.go) | [components/markdown.md](components/markdown.md) |
| Select | input | [`internal/cli/select.go`](../../internal/cli/select.go) | [components/select.md](components/select.md) |
| Prompt | input | [`internal/cli/prompt.go`](../../internal/cli/prompt.go) | [components/prompt.md](components/prompt.md) |
| Confirm | input | [`internal/cli/confirm.go`](../../internal/cli/confirm.go) | [components/confirm.md](components/confirm.md) |
| Spinner | feedback | [`internal/cli/spinner.go`](../../internal/cli/spinner.go) | [components/spinner.md](components/spinner.md) |
| Log formatter | data-display | [`internal/cli/logfmt.go`](../../internal/cli/logfmt.go) | [components/log-formatter.md](components/log-formatter.md) |
| Columns | layout | [`internal/cli/columns.go`](../../internal/cli/columns.go) | [components/columns.md](components/columns.md) |

## Patterns

Composite patterns showing how multiple components compose into the real CLI surfaces users see:

- [patterns/doctor-output.md](patterns/doctor-output.md) — how `ox doctor` composes Box + Timeline + summary
- [patterns/status-dashboard.md](patterns/status-dashboard.md) — how `ox status` composes Sparkline + Columns + Box
- [patterns/session-timeline.md](patterns/session-timeline.md) — how session views compose

## Theming

See [theming.md](theming.md) for how the palette flows from `sageox-design` upstream into `internal/theme/generated.go`. See [tokens.md](tokens.md) for the full semantic token reference.

## Export

`make catalog-export` produces `dist/design-catalog/` (text-only — `.cast`, `.svg`, `.html`, `.json`, vendored asciinema-player JS/CSS). `make publish-catalog` rsyncs that bundle into `sageox-design/bogota-v2/proposals/2026-05-17-ox-cli-component-catalog/`. A human runs `bash scripts/publish-mockups.sh` (or the `/publish-design-mockups` Claude skill) to deploy.
