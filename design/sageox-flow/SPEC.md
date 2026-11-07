# Spec: the SageOx flow lane

Code: `internal/usagepane/mod/hooks/scene.ts` (drawing), `hooks/tokens.ts` (values).
Tags: `[DS]` sageox-design/DESIGN.md · `[H]` Claude Code's tool row (the host) · `[B]` brief, or
chosen for this lane (REFS.md, "Chosen for this lane") · `[C]` craft default.

## Tokens

- Canvas: viewBox `0 0 W 24`, intrinsic `width=W height=24`, W default 640, clamped 200–2400,
  `preserveAspectRatio="xMinYMid meet"`. `[B]`
- Lane centre y 12; text baseline y 16. `[C]`
- Colours, light / dark (dark via `@media (prefers-color-scheme: dark)` inside the SVG): `[DS]`
  - stream (solid line, dots, rings in flight): sage-700 `#4e7d4c` / sage-400 `#99c693`
  - node (the end that received): sage-800 `#3d643b` / sage-500 `#7aaa77`
  - text (digest, end labels): silt-600 `#4a4d42` / silt-300 `#b7b6a3`
  - quiet (open line, ring, stub, the end that sent): silt-500 `#6b6d60` / silt-400 `#8c8e7e`
  - danger (the word "failed" only): error-600 `#9f4838` / error-400 `#d77e6c`
  - Contrast: graphics ≥ 3:1, text ≥ 4.5:1 against dust-100 and white (light) and crater-800 and
    a `#262624` grey host (dark), rules 5 and 18 `[DS]`. Enforced by the `contrast` test in
    `tests/scene.test.tsx`. The faint end of the stub is below 3:1 by design; the stub's meaning
    is also in the digest words.
- Type: digest 11px / 400, `Inter, ui-sans-serif, system-ui` `[DS]`; numbers in the digest
  (tokens, durations) `ui-monospace`, tabular figures `[DS]`; end labels mono 10px / 500,
  uppercase, tracking 0.8px (0.08em) `[B]` at 10px `[DS]`.
- Strokes: open line 1.5 dotted `1.5 4` round caps `[B]` (1.5 is the sparkline stroke `[DS]`);
  ring r 3.5 stroke 1.5 `[B]`; nodes r 3 `[DS]`; stub 34 × 2, linear fade quiet 1 → 0 `[B]`.
- Settled line weight by tokens: 1 under 1k, 2 from 1k, 3 from 10k: hairline 1, edge 2, thick
  edge 3 `[DS]`.
- Dots: filled r 2.5 (in) `[B]`; hollow rings r 2.25, stroke 1.25 (out) `[DS]` rule 3 (filled dot
  vs hollow ring). Count `ceil(tokens / 1000)`, min 1, max 12 `[B]`.

## Anatomy (W ≥ 400)

`SAGEOX` x 0 · origin node x 50 · lane 55 → 225 · session node x 230 · `SESSION` x 238 ·
digest x 298 → W. `[B]` The lane has a fixed length on every row so stacked rows read as small
multiples `[DS]` (metric grid / same chart repeated).

W < 400: no end labels, nodes at 4 and 104, digest from 114. `[B]`
Rendered narrower than 400px (the host shrinks the image): `@media (max-width: 399px)` hides the
end labels. `[B]`

## Content

- Done, in: `searched team · ~1.2k from 5 discussions · 0.8s` `[H]` tool-row digest pattern.
- Done, out: `posted murmur · ~64 · 0.4s`; with items `saved plan · ~3.4k in 1 plan · 1.6s`.
- Running: the step alone, `searching team discussions` `[DS]` rule 19, no timer (the image is
  redrawn only on events, so a timer would freeze) `[B]`.
- Failed: `failed` (danger) ` while searching team discussions · 1.2s` `[H]` `[DS]` rule 3.
- Interrupted: `interrupted while searching team discussions · 3.4s`, no danger `[B]`.
- Zero tokens: `searched code · nothing came back · 0.3s` (out: `nothing sent`) `[B]`.
- Prompt strip: `team context ~15k · 3 murmurs · 1 ledger match`, no duration (nobody waits on
  it) `[H]` group-line pattern.
- Too long: the label shrinks with `…`; if even that does not fit, the label goes (the host row
  above names the call) and numbers stay; only then is the tail cut. `[C]` long-and-many.
- Alt and caption, the same sentence `[DS]` rule 18:
  `searched team: ~1.2k tokens from 5 discussions, 0.8s` ·
  `posted murmur: ~64 tokens sent to murmurs, 0.4s` ·
  `failed while searching team discussions after 1.2s; nothing came back` ·
  `searching team discussions, still running` ·
  `with this prompt: team context ~15k tokens, 3 murmurs, 1 ledger match; ~16k tokens in all`.

## States

| State | Lane | Nodes | Digest |
|---|---|---|---|
| running | dotted 1.5 quiet, origin → ring at destination `[B]` | origin quiet | step `[DS]` r19 |
| in flight (< ANIM_MS after end) | open line fades out, solid line draws in, dots travel `[B]` | destination filled node colour | done digest |
| settled | solid stream line, weight by tokens `[B]` `[DS]` | destination filled | done digest |
| failed / interrupted / zero | stub off the origin `[B]` | both quiet | words, danger only on "failed" |
| out | same marks, travel right → left as hollow rings `[DS]` r3 | SageOx end fills | "posted murmur" |
| prompt strip | one inbound lane summing every attachment | as above | parts joined by ` · ` |
| reduced motion (caller flag) | settled frame, no animation elements `[DS]` r2 | | |
| narrow / long label / dark | see Anatomy, Content, Tokens | | |

Absurd cases: 0 tokens (stub, "nothing came back"); 1M tokens (12 dots, weight 3, `~1000k`);
a 0ms call (`0.1s`); a call that never ends (dotted lane forever, no motion after its 400ms fade,
nothing loops); a 200k-character label (truncated to the digest width, so the source stays small);
no attachment events (`promptSvg([])` draws a stub and "nothing came back").

## Motion

- Lane draw-in: `stroke-dashoffset` 1 → 0 on `pathLength 1`, 600ms ease-out (`0 0 .58 1`;
  DESIGN.md: ease-out for exits, and this is the open state exiting). `[B]` `[DS]`
- Open line / ring / stub fade: 400ms, between the slow and slower tiers. `[B]` `[DS]`
- Dots: `cx` origin → destination 1.4s `calcMode="spline" keySplines="0.4 0 0.2 1"` `[DS]` easing;
  opacity `0;.9;.8;0` at `0;.12;.88;1` `[B]`; stagger 90ms `[B]` (so 12 dots read as a trickle on
  a 170-unit lane). Longer than the 500ms tier because it carries information: data arriving.
- Converge, never radiate: dots end inside the receiving node and vanish there. `[B]`
- Every animation plays once, `fill="freeze"`; nothing loops. `[DS]` rule 2.
- `ANIM_MS = 2400` ≥ 1400 + 11 × 90. After it, the SVG holds no `<animate>` at all. `[B]`
- Redraw continuity: every `begin` is `(stagger − (now − endedAt))`, negative once under way;
  dots whose run already finished are omitted. Verified in the harness by Play, which re-creates
  the image every 500ms (`shots/v2/film/film.png`: dots advance monotonically across redraws). `[B]`
- Static values are end states: dots sit at the destination with `opacity="0"`, the line's
  `stroke-dashoffset="0"`, the fading open line's group `opacity="0"`. A frame where SMIL does not
  run shows the settled lane. `[B]`

### Reduced motion: the limitation

`@media (prefers-reduced-motion: reduce) { * { animation: none !important } }` is in every SVG, but
it stops CSS animations only: **SMIL inside an `<img>` still runs under reduced motion**. So:

1. The caller passes `{ reducedMotion: true }` when it knows; the scene then draws the settled
   frame with no animation elements. The mod passes it for `/ox-session-report flow still` and
   for rows drawn off screen. The mod does not read the OS reduced-motion setting, so that
   setting alone does not stop the dots.
2. Without `still`, a reduced-motion viewer sees one 1.4s pass of dots per new call, never
   repeated on redraw or scroll. That is the residual exposure; it is bounded and one-shot.
   `/ox-session-report flow still` removes it; `flow off` removes the lanes.

## Size guard

`SVG_SOURCE_CAP = 131072` bytes. `fitSource` renders level 0 (everything), then level 1 (at most
3 dots, no end labels, no digest on a flowed lane), then level 2 (no animation, no text). A
12-dot lane is ~4.5 KB and a 3-source prompt strip ~2.5 KB; truncation keeps text bounded, so that
guard is a backstop. `[B]`

## Choices worth recording

- **Horizontal, one line.** One stream per row, built from four marks (stream, open line, ring,
  stub). `[B]`
- **Volume as stroke weight.** 1 / 2 / 3, the hairline / edge / thick-edge steps; no second hue
  (DESIGN.md allows none here). `[DS]`
- **Things move after mount.** The lane carries dots once per call, because "data is moving now"
  is the information. Bounded to 2.4s, once. `[B]` `[DS]` r2
- **Ring fill none.** We do not know the host's row colour, and the dotted line stops short of
  the ring, so hollow is enough. `[B]`
- **Destination node colour.** The receiving node fills with the node colour once something
  landed, the sending node stays quiet: the settled lane keeps its direction without motion. `[DS]` r3
- **Live pulse not used.** A breathing SageOx node while a call is open would need a loop and
  restarts on every redraw; the step text names the work instead. `[DS]` r2, r19

## Harness checks

- `harness.html?grid` (light + dark): `shots/v2/default-1280x800-{light,dark}.png`; v1 for comparison.
  The v2 shots predate the contrast pass (light node, light danger and dark quiet one stop
  stronger); geometry and motion are unchanged.
- Narrow: `shots/v2/default-390x300-*.png`.
- `look.mjs audit`: no issues in the lane (it cannot see inside an `<img>`); the one spacing warning
  is the harness's mock host row (`margin-left: auto`), not the component.
- `look.mjs motion` on Play: 0 pop-ins, 0 jumps.
