<!-- doc-audience: preserve-voice -->
---
component: columns
package: internal/cli
since: 0.4.0
family: layout
renderer: freeze
exports: [ColumnWidths, FormatRow]
---

# Columns

> Aligned tabular output with auto-computed widths.

## When to use

Rows that share semantic meaning across columns (session lists, team rosters, sync state, repo tables). Widths auto-compute from content with caller-supplied min/max clamps so a single wide value doesn't blow up the table.

## When NOT to use

Single-row key=value displays (use [Log formatter](log-formatter.md)). Content that can't survive truncation on narrow terminals — design column widths around 80 columns; use `TruncateID` for long opaque IDs.

## Anatomy

`ox dev catalog --component=columns`. Four-row example (one header + three rows).

## API

Source: [`internal/cli/columns.go`](../../../internal/cli/columns.go), [`internal/cli/truncate.go`](../../../internal/cli/truncate.go)

```go
cli.ColumnWidths(rows [][]string, minWidths []int, maxWidths []int) []int
cli.FormatRow(row []string, widths []int) string
cli.TruncateID(id string, verbose bool) string
cli.TruncateUUID(uuid string, verbose bool) string
```

## Accessibility & fallbacks

- Pure text; no Unicode required.
- Color is the caller's choice (wrap header in `StyleBrand`, etc.).

## Tests

[`internal/cli/columns_test.go`](../../../internal/cli/columns_test.go), [`internal/cli/truncate_test.go`](../../../internal/cli/truncate_test.go)
