<!-- doc-audience: preserve-voice -->
---
component: box
package: internal/ui
since: 0.4.0
family: layout
renderer: freeze
exports: [RenderBox, RenderSummaryBox, BoxVariant]
---

# Box

> Bordered container for a small payload that benefits from a visible boundary.

## When to use

Group a tip, callout, or summary that earns visual separation. Variants color the border by intent: `BoxInfo`, `BoxWarning`, `BoxError`, `BoxSuccess`, `BoxDefault`. `RenderSummaryBox` is the doctor-style pass/warn/fail count with an optional hint line.

## When NOT to use

Wrapping long-form output — boxes around paragraphs add cost without information. For multi-step output use [Timeline](timeline.md). For a single status line use `cli.StyleSuccess` directly.

## Anatomy

See `ox dev catalog --component=box` or the published snapshot at [sageox-design.netlify.app/catalog/cli/#c-box](https://sageox-design.netlify.app/catalog/cli/#c-box).

## API

Source: [`internal/ui/box.go`](../../../internal/ui/box.go)

```go
ui.RenderBox(title, content string, variant BoxVariant) string
ui.RenderSummaryBox(pass, warn, fail, skip int, hint string) string
```

## Accessibility & fallbacks

- Border uses rounded box-drawing characters (`╭─╯`). Requires UTF-8 locale — set `LANG=en_US.UTF-8`.
- Respects `NO_COLOR` (border color drops; box still renders).
- Render width auto-fits content. Test wide content with `COLUMNS=80`.

## Tests

[`internal/ui/box_test.go`](../../../internal/ui/box_test.go)
