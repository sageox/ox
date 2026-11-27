---
component: wordmark
package: internal/ui
since: 0.4.0
family: layout
renderer: freeze
exports: [RenderWordmark, WriteWordmark, RenderMark]
---

# Wordmark

> Two-tone ASCII SageOx logo with optional version line. Used by `ox doctor` and other "destination" commands.

## When to use

Commands that anchor a moment: `ox doctor`, `ox init` success screen, release banners. The wordmark uses `theme.ColorWordmarkSage` (lighter sage) for the "Sage" half and `theme.ColorWordmarkOx` (darker sage) for the "Ox" half, with adaptive light/dark detection via lipgloss.

## When NOT to use

Routine command output. The wordmark is four lines of vertical space — it earns that cost only when the command is a destination, not a step in a pipeline. Don't print it from automation paths or in non-interactive output.

## Anatomy

`ox dev catalog --component=wordmark`. See it in action with `ox doctor`.

## API

Source: [`internal/ui/wordmark.go`](../../../internal/ui/wordmark.go)

```go
ui.RenderWordmark(version string, fixMode bool) string
ui.WriteWordmark(w io.Writer, version string, fixMode bool)
ui.RenderMark() []string
```

Pass `version=""` to render the wordmark alone (no version line). Pass `fixMode=true` to append a dim `— fix mode` tag to the version.

`RenderMark` is the SageOx mark on its own: 6 rows of 21 columns, in braille, for a spot beside other text (`ox login`'s success block, and the header of bare `ox`). Both show it only under the `new_logo` brand rollout (`flags.Get().NewLogoEnabled`; `FEATURE_NEW_LOGO=1` previews it). It is drawn from a 42 by 24 bitmap in the same file; edit the bitmap, not the braille.

## Accessibility & fallbacks

- ASCII glyphs (`▞▀▖▚▄▝`) require a UTF-8 locale.
- Two-tone color uses `ColorWordmarkSage` + `ColorWordmarkOx`; both fall back gracefully under `NO_COLOR=1` (the wordmark still reads as ASCII).
- Width is fixed at 18 columns — fits everywhere. The mark is 21.
- The mark needs braille glyphs (U+2800 block), which every font that draws the [Spinner](spinner.md) already has.

## Tests

Covered indirectly through `cmd/ox/doctor_header.go` and the catalog drift-check test.
