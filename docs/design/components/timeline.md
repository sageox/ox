<!-- doc-audience: preserve-voice -->
---
component: timeline
package: internal/ui
since: 0.5.0
family: data-display
renderer: freeze
exports: [RenderTimeline, TimelineNode, TimelineItem]
---

# Timeline

> Vertical activity flow with dots, connectors, icons, and a closing summary.

## When to use

Multi-step output where order matters — doctor results, sync stages, post-install steps. Each `TimelineNode` anchors a phase; `TimelineItem`s inside report findings with `IconPass`/`IconWarn`/`IconFail`/`IconSkip`/`IconInfo`/`IconAgent` and an optional badge (e.g., `[--fix]`).

## When NOT to use

Single-line status (use `cli.StyleSuccess` directly) or tabular history where order matters less than alignment (use [Columns](columns.md)). Timelines are vertical; horizontal scans want tables.

## Anatomy

See `ox dev catalog --component=timeline` or [sageox-design.netlify.app/catalog/cli/#c-timeline](https://sageox-design.netlify.app/catalog/cli/#c-timeline). Rendered as a static snapshot — the timeline output IS the visual; the streaming render in `--fix` mode is a `cmd/ox/doctor.go` concern, not a property of the component.

## API

Source: [`internal/ui/timeline.go`](../../../internal/ui/timeline.go)

```go
ui.RenderTimeline(nodes []TimelineNode, endLabel string) string
ui.RenderTimelineNode(node TimelineNode) string
ui.RenderTimelineConnector() string
ui.RenderTimelineEnd(label string) string
```

## Accessibility & fallbacks

- Uses `│`, `●`, `○`, `⎿` — requires UTF-8 locale.
- Respects `NO_COLOR` (icons stay; color drops).
- Width-stable: nodes do not exceed 60 columns even for long titles.

## Tests

[`internal/ui/timeline_test.go`](../../../internal/ui/timeline_test.go)
