// Design values for the flow lane (hooks/scene.ts), from the SageOx design
// system (sageox-design/DESIGN.md). design/sageox-flow/SPEC.md tags where
// each one comes from.
//
// Every colour that carries meaning clears 3:1 against the host's surfaces in
// its theme (DESIGN.md §11 rule 18); text clears 4.5:1 (rule 5). The host is
// Claude Code, not a SageOx surface, so the check runs against dust-50/white
// in light and crater-800 through a #262624-class grey in dark.

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
  stream: '#4e7d4c', // sage-700
  node: '#3d643b', // sage-800; sage-500 is 2.4:1 on dust
  text: '#4a4d42', // silt-600
  quiet: '#6b6d60', // silt-500
  danger: '#9f4838', // error-600, the light error text; error-500 is 3.9:1
}

export const DARK: Palette = {
  stream: '#99c693', // sage-400
  node: '#7aaa77', // sage-500
  text: '#b7b6a3', // silt-300
  quiet: '#8c8e7e', // silt-400; silt-500 is 2.9:1 on a grey host
  danger: '#d77e6c', // error-400
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
  /** ease-out, for the lane drawing in. */
  easeOut: '0 0 0.58 1',
} as const
