<!-- doc-audience: preserve-voice -->
---
component: markdown
package: internal/ui
since: 0.5.0
family: data-display
renderer: freeze
exports: [RenderMarkdown]
---

# Markdown

> Long-form structured content rendered with brand-tuned styling.

## When to use

Release notes, agent responses, knowledge-bubble snippets — anything where headings, lists, and emphasis carry meaning. Glamour-backed; the theme is `SageOxDarkStyleJSON` (defined in `internal/ui/markdown.go`).

## When NOT to use

Short status output (markdown headers are overkill) or any performance-sensitive hot path — glamour parsing has measurable cost (~1ms per kilobyte).

## Anatomy

`ox dev catalog --component=markdown` or [sageox-design.netlify.app/catalog/cli/#c-markdown](https://sageox-design.netlify.app/catalog/cli/#c-markdown).

## API

Source: [`internal/ui/markdown.go`](../../../internal/ui/markdown.go)

```go
ui.RenderMarkdown(text string) string
```

## Accessibility & fallbacks

- glamour handles `NO_COLOR` automatically.
- Width defaults to terminal width clipped to 80; pass `WordWrap(n)` for custom.

## Tests

[`internal/ui/markdown_test.go`](../../../internal/ui/markdown_test.go)
