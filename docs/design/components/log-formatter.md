---
component: log-formatter
package: internal/cli
since: 0.6.0
family: data-display
renderer: freeze
exports: [FormatSlogLine, StreamFormattedLogs, ParseSlogLine]
---

# Log formatter

> Structured slog output rendered for humans without breaking grep.

## When to use

Piping daemon logs to a terminal. Levels get colored (`DEBUG` dim, `INFO` neutral, `WARN` warning, `ERROR` error), timestamps compacted to HH:MM:SS, key=value pairs preserved verbatim. Use `StreamFormattedLogs(in, out)` for live tailing.

## When NOT to use

JSON-mode output (`--json`) or anything that downstream tooling parses. Pretty-printing breaks aggregators. Multi-line "pretty" log helpers are banned by [.claude/rules/design.md](../../../.claude/rules/design.md) rule #8 — single-line key=value stays.

## Anatomy

`ox dev catalog --component=log-formatter`. Four lines covering each level.

## API

Source: [`internal/cli/logfmt.go`](../../../internal/cli/logfmt.go)

```go
cli.FormatSlogLine(line string) string
cli.StreamFormattedLogs(r io.Reader, w io.Writer) error
cli.ParseSlogLine(line string) (LogFields, bool)
```

## Accessibility & fallbacks

- Respects `NO_COLOR`. Lines stay single-line — never wrapped.
- Unrecognized lines pass through unchanged.

## Tests

[`internal/cli/logfmt_test.go`](../../../internal/cli/logfmt_test.go), [`internal/cli/logfmt_extended_test.go`](../../../internal/cli/logfmt_extended_test.go)
