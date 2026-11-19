// The flow lane as text, for the terminal. Failure prevented: a terminal
// coworker sees no lane, a lane whose direction or amount is wrong, a row that
// wraps, or a track the engine refuses (a wide or missing glyph).
//
// The golden rows here are the same strings internal/flowlane asserts in Go:
// the plugin and `ox statusline --watch` must draw one grammar.

import { describe, expect, test } from 'claude-code/testing'

import type { FlowEvent } from '../types'
import { ANIM_MS, eventModel } from '../hooks/scene'
import { encodeCells, fitCells, lanePlain, MARCH_MAX_MS, MAX_TEXT_DOTS, NARROW_COLUMNS, RGB_BAD, RGB_DEFAULT, RGB_QUIET, RGB_STREAM, textLane, TRACK, trackCells, trackString } from '../hooks/textlane'

const T0 = 1_760_000_000_000

function ev(over: Partial<FlowEvent> = {}): FlowEvent {
  return {
    id: 'toolu_01abc',
    lane: 'team-context',
    direction: 'in',
    label: 'searched team memory',
    step: 'searching team discussions',
    tokens: 1180,
    items: 5,
    itemsNoun: 'discussions',
    startedAt: T0,
    endedAt: T0 + 800,
    ...over,
  }
}

const settledAt = T0 + 800 + ANIM_MS + 1
const row = (e: FlowEvent, now: number, columns = 120, still = false) => lanePlain(textLane(eventModel(e), now, { columns, still }))

describe('golden rows (shared with internal/flowlane)', () => {
  const cases: Array<[string, FlowEvent, number, string]> = [
    // 2.4s into a running call: dots every six cells, on their way in.
    ['running', ev({ endedAt: undefined }), T0 + 2401, 'SAGEOX ●┄┄•┄┄┄┄┄•┄┄┄┄┄•┄○ SESSION  searching team discussions'],
    ['settled, in, 1k+', ev(), settledAt, 'SAGEOX ●━━━━━━━━━━━━━━━━● SESSION  searched team memory · ~1.2k from 5 discussions · 0.8s'],
    ['settled, in, under 1k', ev({ tokens: 420, items: undefined }), settledAt, 'SAGEOX ●────────────────● SESSION  searched team memory · ~420 · 0.8s'],
    [
      'settled, out',
      ev({ direction: 'out', lane: 'murmurs', label: 'posted murmur', step: 'posting murmur', tokens: 64, items: undefined, endedAt: T0 + 400 }),
      settledAt,
      'SAGEOX ●────────────────● SESSION  posted murmur · ~64 · 0.4s',
    ],
    ['failed', ev({ failed: true, tokens: 0, endedAt: T0 + 1200 }), settledAt, 'SAGEOX ●╶─              ● SESSION  failed while searching team discussions · 1.2s'],
    ['nothing came back', ev({ tokens: 0, items: undefined, endedAt: T0 + 300 }), settledAt, 'SAGEOX ●╶─              ● SESSION  searched team memory · nothing came back · 0.3s'],
    [
      'out, nothing moved: the stub leaves the session end',
      ev({ direction: 'out', label: 'posted murmur', step: 'posting murmur', interrupted: true, tokens: 0, endedAt: T0 + 3400 }),
      settledAt,
      'SAGEOX ●              ─╶● SESSION  interrupted while posting murmur · 3.4s',
    ],
  ]
  for (const [name, e, now, want] of cases) {
    test(name, () => {
      expect(row(e, now)).toBe(want)
    })
  }
})

describe('in flight', () => {
  test('in: the line draws in from SageOx and filled dots travel toward the session', () => {
    const e = ev({ tokens: 3000 })
    const end = e.endedAt as number
    const early = trackString(trackCells(eventModel(e), end + 300).cells)
    expect(early).toBe('●━•━•━━━━┄┄┄┄┄┄┄┄●')
    const mid = trackString(trackCells(eventModel(e), end + 900).cells)
    expect(mid).toBe('●━━━━━━•━•━•━━━━━●')
    const frames = new Set<string>()
    for (let t = 0; t < ANIM_MS; t += 50) {
      frames.add(trackString(trackCells(eventModel(e), end + t).cells))
    }
    expect(frames.size).toBeGreaterThan(10)
  })

  test('out: hollow rings travel from the session back to SageOx', () => {
    const e = ev({ direction: 'out', tokens: 2000 })
    const mid = trackString(trackCells(eventModel(e), (e.endedAt as number) + 900).cells)
    expect(mid).toBe('●━━━━━◦━◦━━━━━━━━●')
    expect(mid).not.toContain('•')
  })

  test('dots are capped so they stay apart, however many tokens moved', () => {
    const e = ev({ tokens: 500_000 })
    let most = 0
    for (let t = 0; t < ANIM_MS; t += 25) {
      const s = trackString(trackCells(eventModel(e), (e.endedAt as number) + t).cells)
      most = Math.max(most, (s.match(/•/g) ?? []).length)
    }
    expect(most).toBe(MAX_TEXT_DOTS)
  })

  test('a running lane marches toward the destination: a pull left to right, a push right to left', () => {
    const track = (e: FlowEvent, t: number, still = false) => trackString(trackCells(eventModel(e), T0 + t, still).cells)
    const pull = ev({ endedAt: undefined })
    expect(track(pull, 0)).toBe('●•┄┄┄┄┄•┄┄┄┄┄•┄┄┄○')
    expect(track(pull, 120)).toBe('●┄•┄┄┄┄┄•┄┄┄┄┄•┄┄○')
    expect(track(pull, 240)).toBe('●┄┄•┄┄┄┄┄•┄┄┄┄┄•┄○')
    const push = ev({ direction: 'out', endedAt: undefined })
    expect(track(push, 0)).toBe('○┄┄┄◦┄┄┄┄┄◦┄┄┄┄┄◦●')
    expect(track(push, 120)).toBe('○┄┄◦┄┄┄┄┄◦┄┄┄┄┄◦┄●')
    expect(track(push, 120)).not.toContain('•')
    // Asked for no motion, or a start too old to believe: the plain dotted line, no ticker.
    expect(track(pull, 240, true)).toBe('●┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄○')
    expect(track(pull, MARCH_MAX_MS)).toBe('●┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄○')
    expect(textLane(eventModel(pull), T0 + MARCH_MAX_MS).moving).toBe(false)
    expect(textLane(eventModel(pull), T0 + 5, { still: true }).moving).toBe(false)
  })

  test('a failed lane is drawn in the error colour, ends included; empty and interrupted stay quiet', () => {
    const tones = (e: FlowEvent) => trackCells(eventModel(e), settledAt).cells.filter((c) => c.ch !== ' ').map((c) => c.tone)
    expect(tones(ev({ failed: true, tokens: 0 }))).toEqual(['bad', 'bad', 'bad', 'bad'])
    expect(tones(ev({ tokens: 0, items: undefined }))).toEqual(['quiet', 'quiet', 'quiet', 'quiet'])
    expect(tones(ev({ interrupted: true, tokens: 0 }))).toEqual(['quiet', 'quiet', 'quiet', 'quiet'])
    const bytes = Uint8Array.from(atob(encodeCells([{ ch: '●', tone: 'bad' }])), (c) => c.charCodeAt(0))
    expect(new DataView(bytes.buffer).getUint32(4, true)).toBe(RGB_BAD)
  })

  test('moving while running or in flight; still, failed and settled rows need no ticker', () => {
    const e = ev()
    const end = e.endedAt as number
    expect(textLane(eventModel(e), end + 100).moving).toBe(true)
    expect(textLane(eventModel(e), end + 100, { still: true }).moving).toBe(false)
    expect(textLane(eventModel(e), end + ANIM_MS).moving).toBe(false)
    expect(textLane(eventModel(ev({ endedAt: undefined })), T0 + 5).moving).toBe(true)
    expect(textLane(eventModel(ev({ failed: true })), end + 100).moving).toBe(false)
    // Still is the settled frame.
    expect(row(e, end + 100, 120, true)).toBe(row(e, settledAt))
  })
})

describe('every frame is a legal track', () => {
  test('always TRACK cells of one BMP code point each', () => {
    const events = [ev(), ev({ direction: 'out' }), ev({ endedAt: undefined }), ev({ failed: true }), ev({ tokens: 0 }), ev({ tokens: 90_000 })]
    for (const e of events) {
      for (let t = -100; t < ANIM_MS + 200; t += 37) {
        const { cells } = trackCells(eventModel(e), (e.endedAt ?? T0) + t)
        expect(cells.length).toBe(TRACK)
        for (const c of cells) {
          expect([...c.ch].length).toBe(1)
          expect(c.ch.codePointAt(0) as number).toBeLessThan(0x10000)
        }
      }
    }
  })

  test('encodeCells: little-endian [codePoint, foreground, default background] triplets', () => {
    const b64 = encodeCells([
      { ch: '●', tone: 'stream' },
      { ch: '┄', tone: 'quiet' },
    ])
    const bytes = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0))
    expect(bytes.length).toBe(24)
    const view = new DataView(bytes.buffer)
    expect(view.getUint32(0, true)).toBe(0x25cf)
    expect(view.getUint32(4, true)).toBe(RGB_STREAM)
    expect(view.getUint32(8, true)).toBe(RGB_DEFAULT)
    expect(view.getUint32(12, true)).toBe(0x2504)
    expect(view.getUint32(16, true)).toBe(RGB_QUIET)
  })
})

describe('fitting the row', () => {
  test('narrow terminals lose the end labels, never the track', () => {
    const wide = textLane(eventModel(ev()), settledAt, { columns: NARROW_COLUMNS })
    const narrow = textLane(eventModel(ev()), settledAt, { columns: NARROW_COLUMNS - 1 })
    expect(wide.labels).toBe(true)
    expect(narrow.labels).toBe(false)
    expect(narrow.track.length).toBe(TRACK)
    expect(lanePlain(narrow).startsWith('●')).toBe(true)
  })

  test('the row never exceeds the columns it was given', () => {
    for (let columns = 20; columns <= 140; columns += 3) {
      for (const e of [ev(), ev({ label: 'a very long label that names a great many things at once', tokens: 123_456 }), ev({ failed: true })]) {
        expect([...row(e, settledAt, columns)].length).toBeLessThanOrEqual(Math.max(columns, TRACK))
      }
    }
  })

  test('the label shrinks, then goes; the numbers stay', () => {
    const segs = eventModel(ev()).digest
    const text = (room: number) =>
      fitCells(segs, room)
        .map((s) => s.text)
        .join('')
    expect(text(60)).toBe('searched team memory · ~1.2k from 5 discussions · 0.8s')
    expect(text(50)).toBe('searched team m… · ~1.2k from 5 discussions · 0.8s')
    expect(text(34)).toBe('~1.2k from 5 discussions · 0.8s')
    expect(text(10)).toMatch(/…$/)
    expect(text(0)).toBe('')
  })
})
