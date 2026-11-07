# References

Two references: the SageOx design system and the host row the lane sits under. Values are design
values only; no source is copied here. Nothing under `refs/` is committed.

## Ref 1 (primary): sageox-design/DESIGN.md

Why this one: it is the public design system every SageOx surface draws from, and it already
answers most of what one line of data-viz needs: palette roles, diagram strokes, motion tiers and
easing, and the rules for colour + shape, labels, motion budgets and non-visual channels.

### Take

| Token | Dark | Light |
|---|---|---|
| stream (solid line, dots, rings in flight) | sage-400 `#99c693` | sage-700 `#4e7d4c` |
| node (the end that received) | sage-500 `#7aaa77` | sage-800 `#3d643b` |
| text (digest, end labels) | silt-300 `#b7b6a3` | silt-600 `#4a4d42` |
| quiet (open line, ring, stub, origin node) | silt-400 `#8c8e7e` | silt-500 `#6b6d60` |
| danger (failed word) | error-400 `#d77e6c` | error-600 `#9f4838` |

- Light node, light danger and dark quiet sit one stop deeper/lighter than the first draft
  (sage-500, error-500, silt-500): those measured 2.4:1, 3.9:1 and 2.9:1 on the surfaces this lane
  lands on, under the 3:1 (graphics) and 4.5:1 (text) floors of rules 5 and 18. The adaptive
  terminal palette ox vendors (`internal/theme/generated.go`) uses the same light error stop.
- Diagram: node r 8 (one-line context: r 3), edge 2 / 3 thick. Sparkline stroke 1.5. Hairline 1.
- Labels 10–12px `ui-sans-serif, system-ui` (Inter where present); numbers `ui-monospace`, tabular.
- Motion: default easing `cubic-bezier(0.4, 0, 0.2, 1)`; durations 150/250/350/500ms tiers;
  longer motion must communicate something specific (ours: data arriving).
- Rules 2 (OS reduced motion, loops stop by 3 cycles), 3 (colour + shape: filled dot vs hollow
  ring is the house example), 17 (label directly, no legend), 18 (non-visual channel, 3:1 for
  meaningful graphics), 19 (name the step, withdraw on every exit path).
- Data viz (§6): small multiples, no chartjunk, no legend, the same chart repeated.

### Leave

- Diagram node r 8 and glow: a diagram canvas, not one text line. Scaled to r 3.
- The specialised hues (progress, public, admin, info): DESIGN.md allows sage, silt, crater/dust
  and danger here; volume is carried by stroke weight, not by a second hue.

## Ref 2 (secondary, placement and words): Claude Code's tool row

Why: the lane lives directly under it, so it must read as that row's quiet second line.

### Take

- One line per call: tool, digest of arguments, duration, status.
- Running: a present-participle label ("Reading 4 of 9 sources"); done: past tense and duration.
- Failed: the error in a few words.
- Group line pattern for several items: "Read 6 files · 1.2s" (our prompt strip:
  "team context ~15k · 3 murmurs · 1 ledger match").

### Leave

- Its shimmer and live timer: our image is redrawn only on events, so a timer would freeze and lie.
  The step text alone names the work (DESIGN rule 19).

## Chosen for this lane (from the brief, no external source)

The stroke grammar and the motion are this lane's own, sized to one 24px line and checked in the
harness. SPEC.md tags each `[B]` or `[C]`.

- **Marks and what each encodes** (never decoration): solid sage line = it flowed; dotted line +
  ring = still open, nothing has landed yet; short faint stub = it ended, nothing will; stroke
  weight = how much moved.
- **Open line:** `stroke-dasharray 1.5 4`, quiet colour, 1.5 wide; round caps turn the dashes into
  dots. **Ring:** r 3.5, 1.5 quiet stroke, where the line would land. **Stub:** 2 × 34, a linear
  fade from quiet at full opacity to 0.
- **End labels:** monospace 10px, weight 500, uppercase, tracking 0.08em.
- **Motion:** the solid line draws in once (`pathLength 1`, offset 1 → 0, 600ms ease-out); the
  open line, ring and stub fade in over 400ms; dots (filled r 2.5) travel once in 1.4s on the house
  easing, opacity 0 → .9 (12%) → .8 (88%) → 0, staggered 90ms, converging into the receiving node.
  Nothing moves after that, and nothing loops.
- **Words:** state words short and past-tense or present-state; no exclamation marks, no
  adjectives, counts as digits; state in words on every row so no meaning rests on hue alone.
