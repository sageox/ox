---
component: flow-lane
package: internal/flowlane
since: 0.27.0
family: viz
renderer: freeze
exports: [Render, Moving, Lane, Options]
---

# Flow lane

> One line showing what an ox call moved between SageOx and an AI coworker's session.

## When to use

One row per `ox` call that moved team data: a search, a plan pulled, a murmur posted, a session uploaded. The row says which way the data went, how much, and how long it took:

```text
SAGEOX ●┄┄•┄┄┄┄┄•┄┄┄┄┄•┄○ SESSION  searching team discussions
SAGEOX ○┄┄◦┄┄┄┄┄◦┄┄┄┄┄◦┄● SESSION  posting murmur
SAGEOX ●━━━━━━•━•━•━━━━━● SESSION  searched team memory · ~3k from 5 discussions · 0.8s
SAGEOX ●━━━━━━━━━━━━━━━━● SESSION  searched team memory · ~1.2k from 5 discussions · 0.8s
SAGEOX ●────────────────● SESSION  posted murmur · size unknown · 0.4s
SAGEOX ●╶─              ● SESSION  failed while searching code · 1.2s
```

- **Running:** a dotted line to an open ring, with dots marching toward where the data is going for as long as the call runs: filled dots left to right for a pull, hollow rings right to left for a push.
- **In flight:** for 2.4 seconds after the call ends, the line draws in and dots travel: filled dots left to right for context arriving, hollow rings right to left for anything sent back. It plays once.
- **Settled:** a solid line, thin under 1k tokens and thick from 1k.
- **Nothing moved** (failed, or an empty result): a short stub off the origin. A failed call's stub and both end dots are red; an empty result stays dim.

The track is a fixed 18 cells on every row, so stacked rows read as small multiples. It is drawn live in `ox statusline --watch`, and still where a host can only show text: under each ox call in Codex, and in the turn-end notice of Claude Code inside Conductor.

## When NOT to use

- A call that moves no team data (`ox doctor`, `ox status`, `ox config`): it draws no lane. `oxactivity.FlowOf` decides.
- Session totals: the status line's counts (`● ox  3 discussions · 1 plan`) say that.
- Anything needing more than one line per call.

## Anatomy

`ox dev catalog --component=flow-lane` or [sageox-design.netlify.app/catalog/cli/#c-flow-lane](https://sageox-design.netlify.app/catalog/cli/#c-flow-lane).

The catalog shows still frames. The motion lives at runtime: `Render` is a pure function of a lane and how long ago it ended, and the caller redraws while `Moving` reports true.

The same grammar is drawn twice: here in Go, and in the Claude Code plugin's terminal lane (`internal/usagepane/mod/hooks/textlane.ts`), which animates under each ox call in the transcript. Both test suites assert the same golden rows. `design/sageox-flow/SPEC.md`, "Terminal lane", is the design record.

## API

Source: [`internal/flowlane/flowlane.go`](../../../internal/flowlane/flowlane.go)

```go
flowlane.Render(l flowlane.Lane, sinceEnd time.Duration, opts flowlane.Options) string
flowlane.Moving(l flowlane.Lane, since time.Duration) bool
```

## Accessibility & fallbacks

- The glyphs carry the meaning without color: dotted is open, solid is flowed, a stub is nothing moved, `•` arrives and `◦` leaves. `Options.Color: false` emits no escape bytes.
- Color is ANSI 256, like the status line: green for what flowed, dim for the rest, and red (the status line's recording-dot red) for a failed lane's stub, its end dots and the word "failed".
- Under 60 columns the `SAGEOX` / `SESSION` labels are dropped. The digest shrinks its label first, then drops it; the numbers go last. A row never exceeds `Options.Columns`.
- Box-drawing and dot characters require UTF-8.
- `Options.Still` draws the settled frame for a caller that cannot or should not animate; a running lane is then the plain dotted line.

## Tests

[`internal/flowlane/flowlane_test.go`](../../../internal/flowlane/flowlane_test.go)
