// The flow lane as text, for the terminal: the surface with no Svg. The same
// model the SVG draws (hooks/scene.ts) becomes one row of cells:
//
//   SAGEOX ●┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄○ SESSION  searching team discussions      running
//   SAGEOX ●━━━━━•━•━•┄┄┄┄┄┄● SESSION  searched team memory · ~1.2k…   in flight
//   SAGEOX ●━━━━━━━━━━━━━━━━● SESSION  searched team memory · ~1.2k…   settled
//   SAGEOX ●╶─              ● SESSION  failed while searching team…    nothing moved
//
// Context arriving travels left to right as filled dots; anything sent back
// travels right to left as hollow rings. The track is a fixed TRACK cells on
// every row so stacked rows read as small multiples.
//
// Everything here is a pure function of (model, now): the plugin redraws the
// row on events and repaints the track in place between them ($.ui.blit).
// design/sageox-flow/SPEC.md, "Terminal lane", is the record.

import { ANIM_MS, dotCount, strokeWeight, SEP, type Model, type Seg } from './scene'
import { TOKENS as T } from './tokens'

/** Cells in the track, both end nodes included. */
export const TRACK = 18
/** Below this many columns the SAGEOX / SESSION end labels go. */
export const NARROW_COLUMNS = 60
/** The most dots or rings a text lane shows in flight. */
export const MAX_TEXT_DOTS = 5
/** Gap between dots leaving the origin: two cells at the dots' speed. */
export const TEXT_DOT_STAGGER_MS = 180
/** Columns the host's own row indent takes; kept free so the row never wraps. */
export const ROW_MARGIN = 6

export const LABEL_SAGEOX = 'SAGEOX '
export const LABEL_SESSION = ' SESSION'
export const DIGEST_GAP = '  '

const INNER = TRACK - 2

const NODE = '●'
const RING = '○'
const DOTTED = '┄'
const THIN = '─'
const THICK = '━'
const STUB_START = '╶'
const DOT_IN = '•'
const DOT_OUT = '◦'

/** Raster colours. One mid sage and one mid grey that hold 3:1 on light and dark. */
export const RGB_STREAM = 0x5f9a5b
export const RGB_QUIET = 0x8a8a8a
/** Bit 24 alone: the terminal's own default colour. */
export const RGB_DEFAULT = 0x01000000

export type Tone = 'stream' | 'quiet'
export type Cell = { ch: string; tone: Tone }

export type TextLaneOpts = {
  /** The conversation's width in cells. Absent: wide enough for labels. */
  columns?: number
  /** Draw the settled frame: the person asked for no motion. */
  still?: boolean
}

export type TextLane = {
  /** Whether the SAGEOX / SESSION end labels fit. */
  labels: boolean
  cells: Cell[]
  /** The track's glyphs as a string: what a frame is compared by. */
  track: string
  /** The digest, fitted to the room left of the track. */
  digest: Seg[]
  /** True while the track still changes with time. */
  moving: boolean
}

/** The line glyph of a settled lane: thin under 1k tokens, thick from 1k. */
export function lineGlyph(tokens: number): string {
  return strokeWeight(tokens) >= 2 ? THICK : THIN
}

/** The track's cells for a model at `now`. */
export function trackCells(m: Model, now: number, still = false): { cells: Cell[]; moving: boolean } {
  // along[k] is the k-th cell from the origin to the destination.
  const along: Cell[] = []
  let moving = false
  const quiet = (ch: string): Cell => ({ ch, tone: 'quiet' })
  const stream = (ch: string): Cell => ({ ch, tone: 'stream' })

  switch (m.outcome) {
    case 'open':
      along.push(quiet(NODE))
      for (let k = 0; k < INNER; k++) {
        along.push(quiet(DOTTED))
      }
      along.push(quiet(RING))
      break
    case 'flowed': {
      const line = lineGlyph(m.tokens)
      const t = m.endedAt === undefined ? ANIM_MS : now - m.endedAt
      const flight = !still && t >= 0 && t < ANIM_MS
      along.push(quiet(NODE))
      const reach = flight && t < T.drawMs ? Math.floor((INNER * t) / T.drawMs) : INNER
      for (let k = 1; k <= INNER; k++) {
        along.push(k <= reach ? stream(line) : quiet(DOTTED))
      }
      along.push(stream(NODE))
      if (flight) {
        const dot = m.direction === 'in' ? DOT_IN : DOT_OUT
        const n = Math.min(MAX_TEXT_DOTS, dotCount(m.tokens))
        for (let i = 0; i < n; i++) {
          const ti = t - i * TEXT_DOT_STAGGER_MS
          if (ti >= 0 && ti < T.dotMs) {
            along[1 + Math.min(INNER - 1, Math.floor((INNER * ti) / T.dotMs))] = stream(dot)
          }
        }
        moving = true
      }
      break
    }
    default:
      // failed, interrupted, empty: a short stub off the origin, both ends quiet.
      along.push(quiet(NODE), quiet(STUB_START), quiet(THIN))
      for (let k = 3; k <= INNER; k++) {
        along.push(quiet(' '))
      }
      along.push(quiet(NODE))
  }
  return { cells: m.direction === 'in' ? along : along.reverse(), moving }
}

/** The cells as one string. */
export function trackString(cells: readonly Cell[]): string {
  return cells.map((c) => c.ch).join('')
}

/** How many cells the digest may take beside the track. */
export function digestRoom(columns: number | undefined): { labels: boolean; room: number } {
  if (columns === undefined || !Number.isFinite(columns)) {
    return { labels: true, room: 120 }
  }
  const cols = Math.max(0, Math.floor(columns))
  const labels = cols >= NARROW_COLUMNS
  const prefix = TRACK + (labels ? LABEL_SAGEOX.length + LABEL_SESSION.length : 0) + DIGEST_GAP.length
  return { labels, room: Math.max(0, cols - prefix - ROW_MARGIN) }
}

const cellsOf = (segs: readonly Seg[]) => segs.reduce((n, s) => n + [...s.text].length, 0)

/**
 * Fit the digest to `room` cells, in the SVG's order: shrink the label, then
 * drop it (the host row above names the call), then cut the tail.
 */
export function fitCells(segs: Seg[], room: number): Seg[] {
  if (segs.length === 0 || cellsOf(segs) <= room) {
    return segs
  }
  const [first, ...rest] = segs
  const chars = room - cellsOf(rest) - 1
  if (chars >= 12 && first.cls === undefined) {
    return [{ text: `${[...first.text].slice(0, chars).join('').trimEnd()}…` }, ...rest]
  }
  if (first.cls === undefined && rest[0] === SEP && cellsOf(rest.slice(1)) <= room) {
    return rest.slice(1)
  }
  const out: Seg[] = []
  let used = 1
  for (const s of segs) {
    const w = [...s.text].length
    if (used + w > room) {
      const left = room - used
      if (left > 0) {
        out.push({ text: [...s.text].slice(0, left).join('').trimEnd(), cls: s.cls })
      }
      break
    }
    out.push(s)
    used += w
  }
  if (room > 0) {
    out.push({ text: '…' })
  }
  return out
}

/** One lane row for the terminal. */
export function textLane(m: Model, now: number, opts: TextLaneOpts = {}): TextLane {
  const { cells, moving } = trackCells(m, now, opts.still === true)
  const { labels, room } = digestRoom(opts.columns)
  return { labels, cells, track: trackString(cells), digest: fitCells(m.digest, room), moving }
}

/** The whole row as plain text: what a terminal with no colour shows. */
export function lanePlain(lane: TextLane): string {
  const digest = lane.digest.map((s) => s.text).join('')
  const row = (lane.labels ? LABEL_SAGEOX : '') + lane.track + (lane.labels ? LABEL_SESSION : '')
  return digest === '' ? row : row + DIGEST_GAP + digest
}

const B64 = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/'

function base64(bytes: Uint8Array): string {
  let out = ''
  for (let i = 0; i < bytes.length; i += 3) {
    const a = bytes[i]
    const b = i + 1 < bytes.length ? bytes[i + 1] : 0
    const c = i + 2 < bytes.length ? bytes[i + 2] : 0
    out += B64[a >> 2] + B64[((a & 3) << 4) | (b >> 4)]
    out += i + 1 < bytes.length ? B64[((b & 15) << 2) | (c >> 6)] : '='
    out += i + 2 < bytes.length ? B64[c & 63] : '='
  }
  return out
}

/**
 * The cells as a Raster's `cells`: base64 of little-endian u32 triplets
 * [codePoint, foreground, background], one per cell.
 */
export function encodeCells(cells: readonly Cell[]): string {
  const bytes = new Uint8Array(cells.length * 12)
  const view = new DataView(bytes.buffer)
  cells.forEach((c, i) => {
    view.setUint32(i * 12, c.ch.codePointAt(0) ?? 0x20, true)
    view.setUint32(i * 12 + 4, c.tone === 'stream' ? RGB_STREAM : RGB_QUIET, true)
    view.setUint32(i * 12 + 8, RGB_DEFAULT, true)
  })
  return base64(bytes)
}
