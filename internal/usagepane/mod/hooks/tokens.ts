// Design values for the flow lane (hooks/scene.ts), from the SageOx design
// system (sageox-design/DESIGN.md).
// design/sageox-flow/SPEC.md tags where each one comes from.

export type Palette = {
  /** Solid line where context flowed, and the dots or rings travelling it. */
  stream: string
  /** The node that received. */
  node: string
  /** Digest and end labels. */
  text: string
  /** Open dotted line, ring, stub, the node that sent. */
  quiet: string
  /** The word "failed". */
  danger: string
}

export const LIGHT: Palette = {
  stream: '#4e7d4c',
  node: '#7aaa77',
  text: '#4a4d42',
  quiet: '#6b6d60',
  danger: '#bd5b49',
}

export const DARK: Palette = {
  stream: '#99c693',
  node: '#7aaa77',
  text: '#b7b6a3',
  quiet: '#6b6d60',
  danger: '#d77e6c',
}

export const FONT_SANS = 'Inter, ui-sans-serif, system-ui, -apple-system, sans-serif'
export const FONT_MONO = 'ui-monospace, SFMono-Regular, Menlo, monospace'

export const TOKENS = {
  /** viewBox height and the intrinsic height: one text line. */
  height: 24,
  /** Intrinsic width when the caller gives none; the host scales. */
  width: 640,
  /** Below this the end labels go (layout and media query). */
  narrowBelow: 400,
  midY: 12,
  textY: 16,
  digestPx: 11,
  labelPx: 10,
  labelTracking: 0.8,
  /** Average advance in user units, for truncation only. */
  sansCharW: 5.6,
  monoCharW: 6.6,

  nodeR: 3,
  ringR: 3.5,
  dotR: 2.5,
  ringDotR: 2.25,
  quietW: 1.5,
  hairW: 1,
  openDash: '1.5 4',
  stubLen: 34,
  stubH: 2,

  /** Stroke weight of a settled lane by tokens moved. */
  weightAt: [
    { min: 10_000, w: 3 },
    { min: 1_000, w: 2 },
    { min: 0, w: 1 },
  ],
  /** One dot per this many tokens, at least one, at most maxDots. */
  tokensPerDot: 1000,
  maxDots: 12,

  drawMs: 600,
  fadeMs: 400,
  dotMs: 1400,
  dotStaggerMs: 90,
  /** cubic-bezier(0.4, 0, 0.2, 1), the house easing. */
  easeHouse: '0.4 0 0.2 1',
  /** ease-out for the draw-in. */
  easeOut: '0 0 0.58 1',
} as const
