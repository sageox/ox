---
component: sparkline
package: internal/tui
since: 0.6.0
family: viz
renderer: freeze
exports: [RenderSparkline, RenderSparklineTimeMarkers]
---

# Sparkline

> Compact activity-over-time visualization in a single line.

## When to use

One-line cadence views — session activity, sync frequency, recent commits. Defaults to a 4-hour window bucketed into 5-minute slots (48 buckets), with hour separators every 12 slots. 8-level Unicode block characters (`▁▂▃▄▅▆▇█`).

## When NOT to use

Precise numerical readouts (use a table) or windows longer than ~6 hours where 5-minute buckets blur the signal. Pair with a longer-window aggregate elsewhere if you need both.

## Anatomy

`ox dev catalog --component=sparkline` or [sageox-design.netlify.app/catalog/cli/#c-sparkline](https://sageox-design.netlify.app/catalog/cli/#c-sparkline).

## API

Source: [`internal/tui/sparkline.go`](../../../internal/tui/sparkline.go)

```go
tui.RenderSparkline(timestamps []time.Time, buckets int, window time.Duration) string
tui.RenderSparklineTimeMarkers() string
```

## Accessibility & fallbacks

- Block characters require UTF-8.
- No color used in the line itself — the caller wraps it in `StyleDim`/`StyleAccent` as desired.

## Tests

[`internal/tui/sparkline_test.go`](../../../internal/tui/sparkline_test.go)
