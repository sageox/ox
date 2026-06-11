---
component: doctor-screen
package: cmd/ox/doctor.go
since: 0.5.0
family: screen
renderer: freeze
exports: [renderDoctorHeader]
---

# Doctor screen

> The canonical "destination command" composition: wordmark + categorized check timeline + closing summary.

Live: `ox doctor`. Catalog snapshot: `ox dev catalog --component=doctor-screen`. Composition narrative: [patterns/doctor-output.md](../patterns/doctor-output.md).

## When this is the right reference

Any command the user runs to *resolve* something, not just describe it — a destination, not a pipeline step. The wordmark earns its vertical space because the user is landing here intentionally. The timeline carries the per-category check results. The summary box closes with the actionable next step ("run `ox doctor --fix`…").

## When NOT to copy

Pipeline commands. The wordmark is four lines of brand chrome — never print it from automation paths, scripts, or anything that gets piped.

## Composition

[Wordmark](wordmark.md) (header) · [Timeline](timeline.md) (per-category checks with passes/warns/fails) · [Box](box.md) (RenderSummaryBox at the close).

## Source

[`cmd/ox/doctor.go`](../../../cmd/ox/doctor.go) + [`cmd/ox/doctor_header.go`](../../../cmd/ox/doctor_header.go)
