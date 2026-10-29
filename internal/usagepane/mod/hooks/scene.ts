// The flow lane: a one-line SVG drawn under a transcript row, from SageOx (left)
// to the session (right). Context arriving travels the lane as filled dots;
// anything the session sends back travels it right to left as hollow rings.
// A solid sage line flowed, a dotted line and a ring are still open, a short
// faint stub ended with nothing.
//
// The desktop draws the source as a plain <img>, so: SMIL only (Chromium runs
// it inside an image), no script, no on* attributes, no external refs, no
// hover. Each redraw recreates the image, so every function here is a pure
// function of (event, now): an animation already under way resumes in phase
// through a negative `begin`, one older than ANIM_MS is drawn settled with no
// animation elements at all, and every animation plays once and freezes.
//
// The static value of every animated attribute is its end state, so a frame
// where SMIL never runs (or a reduced-motion caller) is honest.
//
// design/sageox-flow/SPEC.md is the design record for every value below.

import type { FlowDirection, FlowEvent, FlowLane } from '../types'
import { formatTokens, utf8Bytes } from './logic'
import { DARK, FONT_MONO, FONT_SANS, LIGHT, TOKENS as T, type Palette } from './tokens'

/** How long after `endedAt` an animation can still be in flight. */
export const ANIM_MS = 2400

/** The most source the desktop gets for one image; we degrade below it. */
export const SVG_SOURCE_CAP = 131072

export type SceneOpts = {
  /** viewBox and intrinsic width in px. Default 640; clamped to 200..2400. */
  width?: number
  /** Draw the settled frame: the caller knows the person asked for no motion. */
  reducedMotion?: boolean
}

export type Phase = 'running' | 'flight' | 'settled'
type Outcome = 'open' | 'flowed' | 'failed' | 'interrupted' | 'empty'

const LANE_NAMES: Record<FlowLane, string> = {
  'team-context': 'team context',
  ledger: 'ledger',
  murmurs: 'murmurs',
  code: 'code',
  cloud: 'cloud',
}

// ---- small pure helpers --------------------------------------------------

/** One dot per 1k tokens, at least one, at most 12. */
export function dotCount(tokens: number): number {
  const t = Math.max(0, tokens)
  return Math.min(T.maxDots, Math.max(1, Math.ceil(t / T.tokensPerDot)))
}

/** Stroke weight of a settled lane: 1 under 1k tokens, 2 from 1k, 3 from 10k. */
export function strokeWeight(tokens: number): number {
  for (const step of T.weightAt) {
    if (tokens >= step.min) {
      return step.w
    }
  }
  return 1
}

/** `0.8s`, `3.4s`, `12s`, `1m 12s`. */
export function formatDuration(ms: number): string {
  // Round before splitting, so 9.96s reads 10s and 119.6s reads 2m 0s.
  const tenths = Math.max(1, Math.round(ms / 100))
  if (tenths < 100) {
    return `${(tenths / 10).toFixed(1)}s`
  }
  const s = Math.round(ms / 1000)
  if (s < 60) {
    return `${s}s`
  }
  return `${Math.floor(s / 60)}m ${s % 60}s`
}

export function phaseOf(e: { endedAt?: number }, now: number, reducedMotion = false): Phase {
  if (e.endedAt === undefined) {
    return 'running'
  }
  if (reducedMotion) {
    return 'settled'
  }
  return now - e.endedAt < ANIM_MS ? 'flight' : 'settled'
}

function outcomeOf(e: FlowEvent): Outcome {
  if (e.endedAt === undefined) {
    return 'open'
  }
  if (e.interrupted) {
    return 'interrupted'
  }
  if (e.failed) {
    return 'failed'
  }
  return e.tokens > 0 ? 'flowed' : 'empty'
}

function esc(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
}

function num(n: number): string {
  return String(Math.round(n * 100) / 100)
}

function sec(ms: number): string {
  return `${num(ms / 1000)}s`
}

function hash(s: string): string {
  let h = 5381
  for (let i = 0; i < s.length; i++) {
    h = ((h << 5) + h + s.charCodeAt(i)) >>> 0
  }
  return h.toString(36)
}

// ---- words ---------------------------------------------------------------

/** A run of digest text; `n` is a number (mono, tabular), `bad` is danger. */
type Seg = { text: string; cls?: 'n' | 'bad' }

const SEP: Seg = { text: ' · ' }

function itemsPhrase(e: FlowEvent): string | null {
  if (e.items === undefined || e.items <= 0) {
    return null
  }
  return `${e.items} ${e.itemsNoun ?? 'items'}`
}

/** The digest beside a single lane, by outcome. */
function laneDigest(e: FlowEvent): Seg[] {
  const outcome = outcomeOf(e)
  const dur = e.endedAt === undefined ? '' : formatDuration(e.endedAt - e.startedAt)
  switch (outcome) {
    case 'open':
      return [{ text: e.step }]
    case 'failed':
      return [{ text: 'failed', cls: 'bad' }, { text: ` while ${e.step}` }, SEP, { text: dur, cls: 'n' }]
    case 'interrupted':
      return [{ text: `interrupted while ${e.step}` }, SEP, { text: dur, cls: 'n' }]
    case 'empty':
      return [{ text: e.label }, SEP, { text: e.direction === 'in' ? 'nothing came back' : 'nothing sent' }, SEP, { text: dur, cls: 'n' }]
    case 'flowed': {
      const items = itemsPhrase(e)
      const amount: Seg[] = [{ text: formatTokens(e.tokens), cls: 'n' }]
      if (items) {
        amount.push({ text: e.direction === 'in' ? ` from ${items}` : ` in ${items}` })
      }
      return [{ text: e.label }, SEP, ...amount, SEP, { text: dur, cls: 'n' }]
    }
  }
}

/** "3 murmurs" when items are known, else "team context ~15k". */
function attachmentPart(e: FlowEvent): Seg[] {
  const items = itemsPhrase(e)
  if (items) {
    return [{ text: items }]
  }
  return [{ text: `${LANE_NAMES[e.lane]} ` }, { text: formatTokens(e.tokens), cls: 'n' }]
}

function attachmentWords(e: FlowEvent): string {
  return itemsPhrase(e) ?? `${LANE_NAMES[e.lane]} ${formatTokens(e.tokens)} tokens`
}

/**
 * The sentence for the non-visual channel, used as both the image's alt and
 * its aria-label. One event: "searched team: ~1.2k tokens from 5 discussions,
 * 0.8s". Several (a prompt's attachments): "with this prompt: team context
 * ~15k tokens, 3 murmurs, 1 ledger match; ~16k tokens in all".
 */
export function caption(input: FlowEvent | FlowEvent[]): string {
  if (Array.isArray(input)) {
    return promptCaption(input)
  }
  const e = input
  const dur = e.endedAt === undefined ? '' : formatDuration(e.endedAt - e.startedAt)
  switch (outcomeOf(e)) {
    case 'open':
      return `${e.step}, still running`
    case 'failed':
      return `failed while ${e.step} after ${dur}; ${nothingMoved(e)}`
    case 'interrupted':
      return `interrupted while ${e.step} after ${dur}; ${nothingMoved(e)}`
    case 'empty':
      return `${e.label}: ${nothingMoved(e)}, ${dur}`
    case 'flowed': {
      const tok = `${formatTokens(e.tokens)} tokens`
      if (e.direction === 'out') {
        return `${e.label}: ${tok} sent to ${LANE_NAMES[e.lane]}, ${dur}`
      }
      const items = itemsPhrase(e)
      return items ? `${e.label}: ${tok} from ${items}, ${dur}` : `${e.label}: ${tok}, ${dur}`
    }
  }
}

function nothingMoved(e: FlowEvent): string {
  return e.direction === 'in' ? 'nothing came back' : 'nothing sent'
}

function promptCaption(events: FlowEvent[]): string {
  if (events.length === 0) {
    return 'nothing from SageOx with this prompt'
  }
  const running = events.find((e) => e.endedAt === undefined)
  if (running) {
    return `${running.step}, still running`
  }
  const ok = events.filter((e) => !e.failed && !e.interrupted && e.tokens > 0)
  const bad = events.length - ok.length
  const parts = ok.map(attachmentWords)
  let s = parts.length ? `with this prompt: ${parts.join(', ')}` : 'nothing from SageOx with this prompt'
  if (ok.length > 1) {
    s += `; ${formatTokens(ok.reduce((n, e) => n + e.tokens, 0))} tokens in all`
  }
  if (bad > 0) {
    s += `; ${bad} failed`
  }
  return s
}

// ---- layout --------------------------------------------------------------

type Geo = { w: number; a: number; b: number; digestX: number; labels: boolean }

function geo(width: number | undefined): Geo {
  const w = Math.min(2400, Math.max(200, Math.round(width ?? T.width)))
  if (w < T.narrowBelow) {
    return { w, a: 4, b: 104, digestX: 114, labels: false }
  }
  return { w, a: 50, b: 230, digestX: 298, labels: true }
}

function segWidth(s: Seg): number {
  return s.text.length * (s.cls === 'n' ? T.monoCharW : T.sansCharW)
}

/** Shrink the first run (the label or step) to fit; then drop from the end. */
function fitDigest(segs: Seg[], room: number): Seg[] {
  const total = (xs: Seg[]) => xs.reduce((n, s) => n + segWidth(s), 0)
  if (segs.length === 0 || total(segs) <= room) {
    return segs
  }
  const [first, ...rest] = segs
  const restW = total(rest)
  const chars = Math.floor((room - restW) / T.sansCharW) - 1
  if (chars >= 12 && first.cls === undefined) {
    return [{ text: `${first.text.slice(0, chars).trimEnd()}…` }, ...rest]
  }
  // The host row above already names the call: drop the label before any number.
  if (first.cls === undefined && rest[0] === SEP && total(rest.slice(1)) <= room) {
    return rest.slice(1)
  }
  const out: Seg[] = []
  let used = T.sansCharW
  for (const s of segs) {
    const w = segWidth(s)
    if (used + w > room) {
      const left = Math.floor((room - used) / (s.cls === 'n' ? T.monoCharW : T.sansCharW))
      if (left > 0) {
        out.push({ text: s.text.slice(0, left).trimEnd(), cls: s.cls })
      }
      break
    }
    out.push(s)
    used += w
  }
  out.push({ text: '…' })
  return out
}

// ---- drawing -------------------------------------------------------------

/** Everything the drawing needs, from one event or a prompt's several. */
type Model = {
  key: string
  direction: FlowDirection
  tokens: number
  startedAt: number
  endedAt?: number
  outcome: Outcome
  digest: Seg[]
}

function css(p: Palette): string {
  return [
    `.l{fill:${p.text}}`,
    `.d{fill:${p.text}}`,
    `.bad{fill:${p.danger}}`,
    `.flow,.dot-out{stroke:${p.stream}}`,
    `.dot-in{fill:${p.stream}}`,
    `.open,.ring{stroke:${p.quiet}}`,
    `.nq{fill:${p.quiet}}`,
    `.nd{fill:${p.node}}`,
    `.s0,.s1{stop-color:${p.quiet}}`,
  ].join('')
}

function style(): string {
  return (
    '<style>' +
    `.l{font:500 ${T.labelPx}px ${FONT_MONO};letter-spacing:${T.labelTracking}px}` +
    `.d{font:400 ${T.digestPx}px ${FONT_SANS}}` +
    `.n{font-family:${FONT_MONO};font-variant-numeric:tabular-nums}` +
    '.bad{font-weight:500}' +
    '.flow{fill:none;stroke-linecap:round}' +
    `.open{fill:none;stroke-width:${T.quietW};stroke-dasharray:${T.openDash};stroke-linecap:round}` +
    `.ring{fill:none;stroke-width:${T.quietW}}` +
    '.dot-out{fill:none;stroke-width:1.25}' +
    '.s0{stop-opacity:1}.s1{stop-opacity:0}' +
    css(LIGHT) +
    `@media (prefers-color-scheme: dark){${css(DARK)}}` +
    '@media (prefers-reduced-motion: reduce){*{animation:none!important;transition:none!important}}' +
    `@media (max-width: ${T.narrowBelow - 1}px){.l{display:none}}` +
    '</style>'
  )
}

function anim(attr: string, values: string, durMs: number, beginMs: number, extra = ''): string {
  return `<animate attributeName="${attr}" values="${values}" dur="${sec(durMs)}" begin="${sec(beginMs)}" fill="freeze"${extra}/>`
}

/** Fade in over fadeMs from `since` ms ago; nothing once it has finished. */
function fadeIn(since: number): string {
  return since < T.fadeMs ? anim('opacity', '0;1', T.fadeMs, -since) : ''
}

function digestSvg(segs: Seg[], x: number): string {
  if (segs.length === 0) {
    return ''
  }
  const runs = segs
    .map((s) => (s.cls ? `<tspan class="${s.cls}">${esc(s.text)}</tspan>` : esc(s.text)))
    .join('')
  return `<text class="d" x="${x}" y="${T.textY}">${runs}</text>`
}

type Level = 0 | 1 | 2

function draw(m: Model, now: number, opts: SceneOpts, level: Level): string {
  const g = geo(opts.width)
  const reduced = opts.reducedMotion === true || level === 2
  const phase = phaseOf(m, now, reduced)
  const y = T.midY
  const inbound = m.direction === 'in'
  // Origin and destination x, and the lane between their nodes.
  const from = inbound ? g.a : g.b
  const to = inbound ? g.b : g.a
  const dir = inbound ? 1 : -1
  const gap = T.nodeR + 2
  const x0 = from + dir * gap
  const x1 = to - dir * gap

  const parts: string[] = []
  const sinceStart = Math.max(0, now - m.startedAt)
  const sinceEnd = m.endedAt === undefined ? 0 : Math.max(0, now - m.endedAt)
  const animate = phase === 'flight'

  // End labels: SAGEOX left of its node, SESSION right of the session node.
  if (g.labels && level === 0) {
    parts.push(`<text class="l" x="0" y="${T.textY}">SAGEOX</text>`)
    parts.push(`<text class="l" x="${g.b + 8}" y="${T.textY}">SESSION</text>`)
  }

  // The origin node is quiet; the destination fills once something landed.
  const landed = m.outcome === 'flowed'
  parts.push(`<circle class="nq" cx="${from}" cy="${y}" r="${T.nodeR}"/>`)

  if (m.outcome === 'open') {
    // Dotted line to a ring where it would land. Fades in once, from startedAt.
    const ringX = to
    const end = ringX - dir * (T.ringR + 2)
    const fade = reduced ? '' : fadeIn(sinceStart)
    parts.push(`<g>${fade}<line class="open" x1="${x0}" y1="${y}" x2="${end}" y2="${y}"/>`)
    parts.push(`<circle class="ring" cx="${ringX}" cy="${y}" r="${T.ringR}"/></g>`)
  } else if (m.outcome === 'flowed') {
    parts.push(`<circle class="nd" cx="${to}" cy="${y}" r="${T.nodeR}"/>`)
    const w = strokeWeight(m.tokens)
    // The open line it replaces fades out (base opacity 0: gone when frozen).
    if (animate && sinceEnd < T.fadeMs) {
      parts.push(
        `<g opacity="0">${anim('opacity', '1;0', T.fadeMs, -sinceEnd)}` +
          `<line class="open" x1="${x0}" y1="${y}" x2="${x1}" y2="${y}"/></g>`,
      )
    }
    // The solid line draws in from the origin once (base offset 0: drawn).
    const draw =
      animate && sinceEnd < T.drawMs
        ? anim('stroke-dashoffset', '1;0', T.drawMs, -sinceEnd, ` calcMode="spline" keyTimes="0;1" keySplines="${T.easeOut}"`)
        : ''
    const dash = draw ? ' pathLength="1" stroke-dasharray="1" stroke-dashoffset="0"' : ''
    parts.push(`<line class="flow" x1="${x0}" y1="${y}" x2="${x1}" y2="${y}" stroke-width="${w}"${dash}>${draw}</line>`)
    // Dots (in) or rings (out) travel origin to destination once, staggered,
    // and vanish into the node. Static frame: at the destination, opacity 0.
    if (animate) {
      const n = level === 0 ? dotCount(m.tokens) : Math.min(3, dotCount(m.tokens))
      const cls = inbound ? 'dot-in' : 'dot-out'
      const r = inbound ? T.dotR : T.ringDotR
      for (let i = 0; i < n; i++) {
        const begin = i * T.dotStaggerMs - sinceEnd
        if (begin + T.dotMs <= 0) {
          continue
        }
        parts.push(
          `<circle class="${cls}" cx="${x1}" cy="${y}" r="${r}" opacity="0">` +
            anim('cx', `${x0};${x1}`, T.dotMs, begin, ` calcMode="spline" keyTimes="0;1" keySplines="${T.easeHouse}"`) +
            anim('opacity', '0;0.9;0.8;0', T.dotMs, begin, ' keyTimes="0;0.12;0.88;1"') +
            '</circle>',
        )
      }
    }
  } else {
    // Ended with nothing: a short faint stub off the origin, fading in once.
    const id = `f${hash(m.key)}`
    const sx = inbound ? x0 : x0 - T.stubLen
    const grad = inbound ? 'x1="0" x2="1"' : 'x1="1" x2="0"'
    parts.push(
      `<defs><linearGradient id="${id}" ${grad} y1="0" y2="0">` +
        '<stop offset="0" class="s0"/><stop offset="1" class="s1"/></linearGradient></defs>',
    )
    const fade = animate ? fadeIn(sinceEnd) : ''
    parts.push(`<circle class="nq" cx="${to}" cy="${y}" r="${T.nodeR}"/>`)
    parts.push(
      `<rect x="${sx}" y="${y - T.stubH / 2}" width="${T.stubLen}" height="${T.stubH}" fill="url(#${id})">${fade}</rect>`,
    )
  }

  // Level 1 drops a flowed lane's words (its line and aria-label say it);
  // level 2 drops all text.
  if (level < 2 && (level === 0 || m.outcome !== 'flowed')) {
    parts.push(digestSvg(fitDigest(m.digest, g.w - g.digestX - 2), g.digestX))
  }
  return parts.join('')
}

/** The in-SVG aria-label is bounded; the host's `alt` carries the whole caption. */
const ARIA_MAX = 400

function wrap(m: Model, now: number, opts: SceneOpts, level: Level, caption: string): string {
  const g = geo(opts.width)
  const label = caption.length > ARIA_MAX ? `${caption.slice(0, ARIA_MAX - 1)}…` : caption
  return (
    `<svg xmlns="http://www.w3.org/2000/svg" width="${g.w}" height="${T.height}" viewBox="0 0 ${g.w} ${T.height}" ` +
    `preserveAspectRatio="xMinYMid meet" role="img" aria-label="${esc(label)}">` +
    style() +
    draw(m, now, opts, level) +
    '</svg>'
  )
}

/** Render at the richest level whose source fits under `cap`. */
export function fitSource(render: (level: Level) => string, cap = SVG_SOURCE_CAP): string {
  for (const level of [0, 1, 2] as const) {
    const s = render(level)
    if (utf8Bytes(s) <= cap) {
      return s
    }
  }
  return render(2)
}

function eventModel(e: FlowEvent): Model {
  return {
    key: e.id,
    direction: e.direction,
    tokens: e.tokens,
    startedAt: e.startedAt,
    endedAt: e.endedAt,
    outcome: outcomeOf(e),
    digest: laneDigest(e),
  }
}

/** One tool row's lane. */
export function laneSvg(event: FlowEvent, now: number, opts: SceneOpts = {}): string {
  const m = eventModel(event)
  const label = caption(event)
  return fitSource((level) => wrap(m, now, opts, level, label))
}

/**
 * The prompt row's strip: every attachment event of one prompt summed into one
 * inbound lane. Open while any is open; settled when all have ended.
 */
export function promptSvg(events: FlowEvent[], now: number, opts: SceneOpts = {}): string {
  const label = caption(events)
  const running = events.find((e) => e.endedAt === undefined)
  const ok = events.filter((e) => !e.failed && !e.interrupted && e.tokens > 0)
  const bad = events.length - ok.length
  const startedAt = events.reduce((t, e) => Math.min(t, e.startedAt), events[0]?.startedAt ?? now)
  const endedAt = running || events.length === 0 ? undefined : events.reduce((t, e) => Math.max(t, e.endedAt ?? t), startedAt)

  const digest: Seg[] = []
  ok.forEach((e, i) => {
    if (i > 0) {
      digest.push(SEP)
    }
    digest.push(...attachmentPart(e))
  })
  if (bad > 0) {
    if (digest.length) {
      digest.push(SEP)
    }
    digest.push({ text: `${bad} failed`, cls: 'bad' })
  }

  // No events at all draws as an empty lane that ended now.
  let outcome: Outcome
  if (events.length === 0) {
    outcome = 'empty'
  } else if (running) {
    outcome = 'open'
  } else if (ok.length) {
    outcome = 'flowed'
  } else {
    outcome = events.some((e) => e.failed) ? 'failed' : 'empty'
  }
  const m: Model = {
    key: events.map((e) => e.id).join('|') || 'prompt',
    direction: 'in',
    tokens: ok.reduce((n, e) => n + e.tokens, 0),
    startedAt,
    endedAt: events.length === 0 ? now : endedAt,
    outcome,
    digest: running ? [{ text: running.step }] : digest.length ? digest : [{ text: 'nothing came back' }],
  }
  return fitSource((level) => wrap(m, now, opts, level, label))
}
