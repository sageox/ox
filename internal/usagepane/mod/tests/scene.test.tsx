import { describe, expect, test } from 'claude-code/testing'

import type { FlowEvent } from '../types'
import {
  ANIM_MS,
  caption,
  dotCount,
  formatDuration,
  fitSource,
  laneSvg,
  promptSvg,
  strokeWeight,
  SVG_SOURCE_CAP,
} from '../hooks/scene'
import { DARK, LIGHT, TOKENS } from '../hooks/tokens'

const T0 = 1_760_000_000_000

function ev(over: Partial<FlowEvent> = {}): FlowEvent {
  return {
    id: 'toolu_01abc',
    lane: 'team-context',
    direction: 'in',
    label: 'searched team',
    step: 'searching team discussions',
    tokens: 1180,
    items: 5,
    itemsNoun: 'discussions',
    startedAt: T0,
    endedAt: T0 + 800,
    ...over,
  }
}

const count = (s: string, re: RegExp) => (s.match(re) ?? []).length
const dots = (s: string) => count(s, /<circle class="dot-(in|out)"/g)
const bytes = (s: string) => new TextEncoder().encode(s).length

describe('dot count and weight', () => {
  test('one dot per 1k tokens, at least one, capped at 12', () => {
    expect(dotCount(0)).toBe(1)
    expect(dotCount(80)).toBe(1)
    expect(dotCount(1180)).toBe(2)
    expect(dotCount(14_820)).toBe(12)
    expect(dotCount(1_000_000)).toBe(12)
  })

  test('the lane draws that many dots just after the call ends', () => {
    expect(dots(laneSvg(ev({ tokens: 80 }), T0 + 800))).toBe(1)
    expect(dots(laneSvg(ev(), T0 + 800))).toBe(2)
    expect(dots(laneSvg(ev({ tokens: 14_820 }), T0 + 800))).toBe(12)
  })

  test('stroke weight is 1 under 1k, 2 from 1k, 3 from 10k', () => {
    expect(strokeWeight(80)).toBe(1)
    expect(strokeWeight(1000)).toBe(2)
    expect(strokeWeight(10_000)).toBe(3)
    expect(laneSvg(ev({ tokens: 14_820 }), T0 + 60_000)).toContain('stroke-width="3"')
  })
})

describe('a pure function of (event, now)', () => {
  test('a 400ms-old event resumes in phase with negative begins', () => {
    const svg = laneSvg(ev(), T0 + 800 + 400)
    expect(svg).toContain('begin="-0.4s"')
    expect(svg).toContain('begin="-0.31s"')
    expect(svg).toContain('fill="freeze"')
  })

  test('dots whose run already finished are left out', () => {
    // 12 dots, 1.5s on: dot i began at i*90ms, so dot 0 (ended 1.4s) is gone.
    expect(dots(laneSvg(ev({ tokens: 14_820 }), T0 + 800 + 1500))).toBe(10)
  })

  test('older than ANIM_MS: settled, no animation elements', () => {
    const svg = laneSvg(ev({ tokens: 14_820 }), T0 + 800 + ANIM_MS)
    expect(svg).not.toContain('<animate')
    expect(dots(svg)).toBe(0)
    expect(svg).toContain('class="flow"')
  })

  test('ANIM_MS covers the last dot of a full lane', () => {
    expect(ANIM_MS).toBeGreaterThanOrEqual(TOKENS.dotMs + (TOKENS.maxDots - 1) * TOKENS.dotStaggerMs)
  })

  test('reducedMotion draws the settled frame even mid-flight', () => {
    const svg = laneSvg(ev(), T0 + 900, { reducedMotion: true })
    expect(svg).not.toContain('<animate')
    expect(svg).toContain('class="flow"')
  })

  test('nothing ever loops', () => {
    for (const now of [T0 + 100, T0 + 900, T0 + 2000]) {
      expect(laneSvg(ev({ endedAt: undefined }), now)).not.toContain('indefinite')
      expect(laneSvg(ev({ tokens: 9000 }), now)).not.toContain('indefinite')
    }
  })

  test('static values are end states: dots wait at the destination, invisible', () => {
    const svg = laneSvg(ev(), T0 + 900)
    expect(svg).toMatch(/<circle class="dot-in" cx="225" cy="12" r="2.5" opacity="0">/)
    expect(svg).toContain('stroke-dashoffset="0"')
  })

  test('same input, same string', () => {
    expect(laneSvg(ev(), T0 + 1000)).toBe(laneSvg(ev(), T0 + 1000))
    const evs = [ev({ id: 'a' }), ev({ id: 'b', lane: 'murmurs', items: 3, itemsNoun: 'murmurs' })]
    expect(promptSvg(evs, T0 + 1000)).toBe(promptSvg(evs, T0 + 1000))
  })
})

describe('states', () => {
  test('running: dotted lane, a ring, the step, no dots', () => {
    const svg = laneSvg(ev({ endedAt: undefined }), T0 + 1200)
    expect(svg).toContain('class="open"')
    expect(svg).toContain('stroke-dasharray:1.5 4')
    expect(svg).toContain('class="ring"')
    expect(svg).toContain('searching team discussions')
    expect(dots(svg)).toBe(0)
    expect(svg).not.toContain('class="flow"')
  })

  test('outbound: hollow rings travelling right to left', () => {
    const svg = laneSvg(ev({ direction: 'out', lane: 'murmurs', label: 'posted murmur', tokens: 64, items: undefined }), T0 + 900)
    expect(svg).toContain('.dot-out{fill:none')
    expect(svg).toContain('<circle class="dot-out"')
    expect(svg).not.toContain('class="dot-in"')
    const m = svg.match(/attributeName="cx" values="(\d+);(\d+)"/)
    expect(m).not.toBeNull()
    expect(Number(m![1])).toBeGreaterThan(Number(m![2]))
  })

  test('failed: a stub and the danger word, no solid lane', () => {
    const svg = laneSvg(ev({ failed: true, tokens: 0, endedAt: T0 + 1210 }), T0 + 60_000)
    expect(svg).toMatch(/<rect x="55" y="11" width="34" height="2" fill="url\(#f[a-z0-9]+\)">/)
    expect(svg).toContain('<tspan class="bad">failed</tspan>')
    expect(svg).toContain('.bad{fill:#9f4838}')
    expect(svg).not.toContain('class="flow"')
  })

  test('interrupted and zero tokens: a stub, no danger', () => {
    const interrupted = laneSvg(ev({ interrupted: true, tokens: 0 }), T0 + 60_000)
    expect(interrupted).toContain('<rect')
    expect(interrupted).not.toContain('class="bad"')
    const zero = laneSvg(ev({ tokens: 0, items: undefined }), T0 + 60_000)
    expect(zero).toContain('nothing came back')
    expect(zero).toContain('<rect')
  })

  test('a long label is truncated with an ellipsis, numbers kept', () => {
    const label = 'searched team for "why did we move retry backoff out of the daemon"'
    const svg = laneSvg(ev({ label }), T0 + 60_000).match(/<text class="d".*<\/text>/)![0]
    expect(svg).toContain('…')
    expect(svg).not.toContain('out of the daemon')
    expect(svg).toContain('~1.2k')
    expect(svg).toContain('0.8s')
  })

  test('narrow: no end labels, label dropped before numbers', () => {
    const svg = laneSvg(ev(), T0 + 60_000, { width: 320 })
    expect(svg).toContain('viewBox="0 0 320 24"')
    expect(svg).not.toContain('SAGEOX')
    expect(svg).toContain('~1.2k')
    expect(svg).toContain('0.8s')
  })

  test('prompt strip sums its sources', () => {
    const svg = promptSvg(
      [
        ev({ id: 'a', label: 'loaded team context', tokens: 14_820, items: undefined }),
        ev({ id: 'b', lane: 'murmurs', tokens: 310, items: 3, itemsNoun: 'murmurs' }),
        ev({ id: 'c', lane: 'ledger', tokens: 520, items: 1, itemsNoun: 'ledger match' }),
      ],
      T0 + 60_000,
    )
    expect(svg).toContain('team context <tspan class="n">~15k</tspan> · 3 murmurs · 1 ledger match')
    expect(svg).toContain('stroke-width="3"')
  })
})

describe('safe as a plain <img>', () => {
  const samples = [
    laneSvg(ev(), T0 + 900),
    laneSvg(ev({ endedAt: undefined }), T0 + 100),
    laneSvg(ev({ failed: true, label: '<script>alert(1)</script> onload="x"' }), T0 + 900),
    promptSvg([ev({ id: 'a' }), ev({ id: 'b' })], T0 + 900),
  ]

  test('no script, no handlers, no images, no external refs', () => {
    for (const svg of samples) {
      expect(svg).not.toContain('<script')
      expect(svg).not.toMatch(/\son[a-z]+=/)
      expect(svg).not.toContain('<image')
      expect(svg).not.toMatch(/(?:xlink:)?href=/)
      // The only URL is the SVG namespace.
      expect(svg.replace('xmlns="http://www.w3.org/2000/svg"', '')).not.toContain('http')
    }
  })

  test('dark scheme and reduced motion media queries are present', () => {
    for (const svg of samples) {
      expect(svg).toContain('@media (prefers-color-scheme: dark)')
      expect(svg).toContain('@media (prefers-reduced-motion: reduce)')
      expect(svg).toContain('preserveAspectRatio="xMinYMid meet"')
    }
  })

  test('a 12-dot lane and a 3-source strip are well under the cap', () => {
    expect(bytes(laneSvg(ev({ tokens: 14_820 }), T0 + 800))).toBeLessThan(SVG_SOURCE_CAP / 10)
    const evs = [ev({ id: 'a', tokens: 14_820 }), ev({ id: 'b', tokens: 310 }), ev({ id: 'c', tokens: 520 })]
    expect(bytes(promptSvg(evs, T0 + 800))).toBeLessThan(SVG_SOURCE_CAP / 10)
  })

  test('a huge label stays bounded', () => {
    const svg = laneSvg(ev({ label: 'x'.repeat(200_000) }), T0 + 800, { width: 100_000 })
    expect(bytes(svg)).toBeLessThan(SVG_SOURCE_CAP / 10)
  })

  test('over the cap it degrades: fewer dots, then no labels or motion', () => {
    const levels: number[] = []
    const out = fitSource((level) => {
      levels.push(level)
      return level === 2 ? 'small' : 'x'.repeat(200)
    }, 100)
    expect(out).toBe('small')
    expect(levels).toEqual([0, 1, 2])
  })
})

describe('the non-visual channel', () => {
  test('the caption is the image aria-label', () => {
    expect(caption(ev())).toBe('searched team: ~1.2k tokens from 5 discussions, 0.8s')
    expect(laneSvg(ev(), T0 + 60_000)).toContain('aria-label="searched team: ~1.2k tokens from 5 discussions, 0.8s"')
  })

  test('every outcome reads as a sentence', () => {
    expect(caption(ev({ endedAt: undefined }))).toBe('searching team discussions, still running')
    expect(caption(ev({ failed: true, endedAt: T0 + 1210 }))).toBe(
      'failed while searching team discussions after 1.2s; nothing came back',
    )
    expect(caption(ev({ direction: 'out', lane: 'murmurs', label: 'posted murmur', tokens: 64, endedAt: T0 + 420 }))).toBe(
      'posted murmur: ~64 tokens sent to murmurs, 0.4s',
    )
    expect(caption(ev({ tokens: 14_820, items: undefined, label: 'loaded team context', endedAt: T0 + 1130 }))).toBe(
      'loaded team context: ~15k tokens, 1.1s',
    )
  })

  test('an outbound event that failed or was cut short sent nothing', () => {
    const out = { direction: 'out' as const, lane: 'murmurs' as const, label: 'posted murmur', step: 'posting murmur', endedAt: T0 + 400 }
    expect(caption(ev({ ...out, failed: true }))).toBe('failed while posting murmur after 0.4s; nothing sent')
    expect(caption(ev({ ...out, interrupted: true }))).toBe('interrupted while posting murmur after 0.4s; nothing sent')
  })

  test('an outbound event of unknown size says so, never "nothing sent"', () => {
    const e = ev({ direction: 'out', lane: 'ledger', label: 'saved plan', tokens: 0, items: undefined, unsized: true, endedAt: T0 + 900 })
    expect(caption(e)).toBe('saved plan: sent to ledger, size unknown, 0.9s')
    expect(laneSvg(e, T0 + 60_000)).not.toContain('nothing sent')
  })

  test('durations carry rounded seconds into minutes', () => {
    expect(formatDuration(800)).toBe('0.8s')
    expect(formatDuration(9_960)).toBe('10s')
    expect(formatDuration(12_000)).toBe('12s')
    expect(formatDuration(59_600)).toBe('1m 0s')
    expect(formatDuration(72_000)).toBe('1m 12s')
    expect(formatDuration(119_600)).toBe('2m 0s')
  })

  test('a prompt strip sums its sources in words', () => {
    expect(
      caption([
        ev({ id: 'a', tokens: 14_820, items: undefined }),
        ev({ id: 'b', lane: 'murmurs', tokens: 310, items: 3, itemsNoun: 'murmurs' }),
        ev({ id: 'c', lane: 'ledger', tokens: 520, items: 1, itemsNoun: 'ledger match' }),
      ]),
    ).toBe('with this prompt: team context ~15k tokens, 3 murmurs, 1 ledger match; ~16k tokens in all')
    expect(caption([])).toBe('nothing from SageOx with this prompt')
  })

  test('text is escaped', () => {
    const svg = laneSvg(ev({ label: 'a < b & "c"' }), T0 + 60_000)
    expect(svg).toContain('a &lt; b &amp; &quot;c&quot;')
  })
})

describe('contrast (DESIGN.md rules 5 and 18)', () => {
  const lum = (hex: string) => {
    const [r, g, b] = [1, 3, 5].map((i) => {
      const c = parseInt(hex.slice(i, i + 2), 16) / 255
      return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4
    })
    return 0.2126 * r + 0.7152 * g + 0.0722 * b
  }
  const ratio = (a: string, b: string) => {
    const [lo, hi] = [lum(a), lum(b)].sort((x, y) => x - y)
    return (hi + 0.05) / (lo + 0.05)
  }
  // dust-100 and white in light; crater-800 and a mid-grey host row in dark.
  const surfaces = { light: ['#f4f2ef', '#ffffff'], dark: ['#111411', '#262624'] }

  test('graphics clear 3:1 and text 4.5:1 on every surface, in both themes', () => {
    for (const [theme, p] of [['light', LIGHT], ['dark', DARK]] as const) {
      for (const bg of surfaces[theme]) {
        for (const key of ['stream', 'node', 'quiet'] as const) {
          expect(ratio(p[key], bg)).toBeGreaterThanOrEqual(3)
        }
        for (const key of ['text', 'danger'] as const) {
          expect(ratio(p[key], bg)).toBeGreaterThanOrEqual(4.5)
        }
      }
    }
  })
})
