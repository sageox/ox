// Oxy, SageOx's mascot, in Claude Code's thinking line. Installed and kept up
// to date by `ox pet enable`; frames.ts is generated from the sprite sheet
// embedded in the ox binary.
//
// Every hook falls back to `next(e)`: a pet bug must never cost a turn.

import { atom, read, update } from 'claude-code'
import type { Elements, EngineInterface, Register } from 'claude-code'

import type { Renderer, Size } from '../types'
import { FRAMES } from './frames'
import {
  ANIMS,
  cellsFile,
  classifyToolCall,
  formatDuration,
  frameAt,
  frameFile,
  isAnim,
  isSize,
  passMs,
  pickAnim,
  RAGE_AFTER_FAILURES,
  type Activity,
  type Anim,
  type Cells,
  type OneShot,
} from './logic'

// This session's choices, null until made. `$.state` is the session's, and a
// /clear or an in-app resume starts a new session with none and no
// session.start, so every read goes through `prefs`, never the atom alone.
const hidden = atom({ plugin: 'ox-cli-oxy', key: 'hidden' } as const, null as boolean | null)
const renderer = atom({ plugin: 'ox-cli-oxy', key: 'renderer' } as const, null as Renderer | null)
const size = atom({ plugin: 'ox-cli-oxy', key: 'size' } as const, null as Size | null)
const pinned = atom({ plugin: 'ox-cli-oxy', key: 'pinned' } as const, null as string | null)

// What outlives a session: the terminal's picture support, read once per
// process, and the size and visibility `$.store` keeps across sessions. A
// read that fails draws with the defaults and is tried again next time.
let terminal: Renderer | null = null
let saved: { size: Size; hidden: boolean } | null = null

async function terminalRenderer($: EngineInterface): Promise<Renderer> {
  if (terminal === null) {
    try {
      const term = (await $.env.get('TERM_PROGRAM')) ?? ''
      const kitty = (await $.env.get('KITTY_WINDOW_ID')) !== undefined
      const tmux = (await $.env.get('TMUX')) !== undefined
      terminal = !tmux && (kitty || /ghostty|kitty/i.test(term)) ? 'image' : 'cells'
    } catch {
      return 'cells'
    }
  }
  return terminal
}

async function savedPrefs($: EngineInterface): Promise<{ size: Size; hidden: boolean }> {
  if (saved === null) {
    try {
      const box = await $.store.get('size')
      saved = { size: isSize(box) ? box : 'medium', hidden: (await $.store.get('hidden')) === true }
    } catch {
      return { size: 'medium', hidden: false }
    }
  }
  return saved
}

// The renderer, size and visibility in force: this session's choice, else
// what the terminal supports and what was saved.
async function prefs($: EngineInterface): Promise<{ kind: Renderer; box: Size; hide: boolean }> {
  const kind = await read($, renderer)
  const box = await read($, size)
  const hide = await read($, hidden)
  const fallback = await savedPrefs($)
  return {
    kind: kind ?? (await terminalRenderer($)),
    box: box ?? fallback.size,
    hide: hide ?? fallback.hidden,
  }
}

const KEY = 'oxy'
const MAIN = 'main'
const TICK_MS = 50
const USAGE = '/oxy [on|off|auto|small|medium|large|image|cells|<animation>]'

type Running = Activity & { loop: string }

// A drawn site: the thinking line of one loop, or one turn's closing line.
type Site =
  | { kind: 'spinner'; mode: string; message: string | null; drawn: string }
  | { kind: 'turn'; anim: Anim; start: number; drawn: string }

// Module state: what is running and where Oxy is drawn. A reload of the mod
// starts it over, which only costs the frame in flight.
const running = new Map<string, Running>()
const sites = new Map<string, Site>()
// Each closing line's resting animation, once it has played.
const finished = new Map<string, Anim>()
let oneShot: OneShot | null = null
let loopStart = 0
let turnFailed = false
// Tool calls that failed back to back this turn; a success resets it.
let failStreak = 0
let closing: { anim: Anim; start: number } | null = null
let ticker: { cancel: () => void } | null = null
// Each animation's half-block cells, read from its cells.json on first use.
const cells = new Map<Anim, Promise<Cells>>()

function cellsOf($: EngineInterface, anim: Anim): Promise<Cells> {
  let loading = cells.get(anim)
  if (loading === undefined) {
    loading = $.fs.read(cellsFile($.plugin.root, anim)).then(text => JSON.parse(text) as Cells)
    loading.catch(() => cells.delete(anim))
    cells.set(anim, loading)
  }
  return loading
}

function pictureOf($: EngineInterface, anim: Anim, index: number) {
  return { file: frameFile($.plugin.root, anim, index), format: 'png' as const }
}

function playOnce(anim: Anim, now: number) {
  oneShot = { anim, start: now, until: now + passMs(FRAMES.animations[anim]) }
}

function spinnerAnim(requestId: string, site: { mode: string; message: string | null }, now: number, pin: string | null) {
  const own = [...running.values()].filter(r => r.loop === requestId)
  const activities = (own.length > 0 ? own : [...running.values()].filter(r => r.loop === MAIN)).sort(
    (a, b) => a.since - b.since,
  )
  return pickAnim({
    now,
    mode: site.mode,
    message: site.message,
    oneShot,
    activities,
    pinned: pin !== null && isAnim(pin) ? pin : null,
  })
}

// The frame a site shows at `now`, and the key that says whether it changed.
function frameOf(requestId: string, site: Site, now: number, pin: string | null) {
  if (site.kind === 'turn') {
    const index = frameAt(FRAMES.animations[site.anim], site.start, now, false)
    return { anim: site.anim, index, drawn: `${site.anim}:${index}` }
  }
  const anim = spinnerAnim(requestId, site, now, pin)
  // One-shot timing only while that one-shot is playing; a pinned or
  // expired one loops like any other animation.
  const shot = pin === null && oneShot !== null && now < oneShot.until && anim === oneShot.anim ? oneShot : null
  const index = frameAt(FRAMES.animations[anim], shot?.start ?? loopStart, now, shot === null)
  return { anim, index, drawn: `${anim}:${index}` }
}

// Swaps each drawn site to its next frame, without a redraw.
async function tick($: EngineInterface) {
  const now = await $.clock.now()
  const pin = await read($, pinned)
  const { kind, box } = await prefs($)
  for (const [requestId, site] of sites) {
    const f = frameOf(requestId, site, now, pin)
    if (f.drawn === site.drawn) {
      continue
    }
    const frames = FRAMES.animations[f.anim]
    let done: { deny?: string }
    try {
      done =
        kind === 'image'
          ? await $.ui.blit({ requestId, key: KEY, source: pictureOf($, f.anim, f.index) })
          : await $.ui.blit({ requestId, key: KEY, cells: (await cellsOf($, f.anim))[box][f.index] ?? '' })
    } catch {
      // A frame that can't load (a cells.json gone missing) stops this site,
      // so the ticker can wind down instead of retrying every tick.
      sites.delete(requestId)
      continue
    }
    if (done.deny === undefined) {
      site.drawn = f.drawn
    } else if (kind === 'image' && /alt/i.test(done.deny)) {
      // The terminal can't show pictures here after all: redraw as cells.
      await update($, renderer, () => 'cells')
    } else {
      sites.delete(requestId)
    }
    if (site.kind === 'turn' && f.index === frames.ms.length - 1) {
      sites.delete(requestId)
    }
  }
  if (closing !== null && now - closing.start > 5_000) {
    closing = null
  }
  if (sites.size === 0 && running.size === 0 && closing === null) {
    ticker?.cancel()
    ticker = null
  }
}

function startTicker($: EngineInterface) {
  ticker ??= $.clock.every(TICK_MS, () => void tick($).catch(() => undefined))
}

// How far down the words sit: the bottom row of Oxy's box, so they read as a
// caption at his feet rather than floating beside the helmet.
function wordsOffset(kind: Renderer, box: Size): number {
  const b = FRAMES.sizes[box]
  return (kind === 'image' ? b.rows : b.cells.rows) - 1
}

async function sprite($: EngineInterface, { Image, Raster }: Elements['terminal'], kind: Renderer, box: Size, anim: Anim, index: number) {
  const b = FRAMES.sizes[box]
  if (kind === 'image') {
    return <Image key={KEY} source={pictureOf($, anim, index)} columns={b.columns} rows={b.rows} alt=" " />
  }
  const frame = (await cellsOf($, anim))[box][index] ?? ''
  return <Raster key={KEY} cells={frame} columns={b.cells.columns} rows={b.cells.rows} />
}

export const register: Register = on => {
  on('session.start', async ($, e, next) => {
    await $.command.register({
      name: 'oxy',
      description: 'Oxy in the thinking line: off, auto, small, medium, large, image, cells, or an animation to pin',
      argumentHint: '[on|off|auto|small|medium|large|image|cells|<animation>]',
    })
    return next(e)
  })

  on('command.run', { command: 'oxy' }, async ($, e) => {
    const arg = e.args.trim().toLowerCase()
    if (arg === '' || arg === 'on' || arg === 'off') {
      const hide = arg === 'off'
      const was = (await prefs($)).hide
      await update($, hidden, () => hide)
      await $.store.set('hidden', hide)
      saved = { ...(await savedPrefs($)), hidden: hide }
      if (hide) {
        return { text: 'Oxy is hidden; Claude Code draws its own spinner. /oxy brings him back.' }
      }
      return { text: was ? 'Oxy hatched into the thinking line.' : 'Oxy is already here.' }
    }
    if (arg === 'auto') {
      await update($, pinned, () => null)
      return { text: 'Oxy follows what Claude is doing.' }
    }
    if (arg === 'image' || arg === 'cells') {
      await update($, renderer, () => arg)
      return { text: `Oxy is drawn as ${arg === 'image' ? 'a picture' : 'half-block cells'}.` }
    }
    if (isSize(arg)) {
      await update($, size, () => arg)
      await $.store.set('size', arg)
      saved = { ...(await savedPrefs($)), size: arg }
      return { text: `Oxy is ${arg} now.` }
    }
    if (isAnim(arg)) {
      await update($, pinned, () => arg)
      return { text: `Oxy plays ${arg} until /oxy auto.` }
    }
    return { text: `Usage: ${USAGE}. Animations: ${ANIMS.join(', ')}.` }
  })

  on('turn.start', async ($, e, next) => {
    const now = await $.clock.now()
    loopStart = now
    turnFailed = false
    failStreak = 0
    closing = null
    playOnce('wave', now)
    startTicker($)
    return next(e)
  })

  on('turn.complete', async ($, e, next) => {
    closing = { anim: turnFailed ? 'fail' : 'confetti', start: await $.clock.now() }
    return next(e)
  })

  on('tool.call', async ($, e, next) => {
    const id = e.tool_use_id
    running.set(id, {
      anim: classifyToolCall(e.tool, e as unknown as Record<string, unknown>),
      since: await $.clock.now(),
      loop: e.agentId ?? MAIN,
    })
    startTicker($)
    try {
      const ran = await next(e)
      const failed = ran.deny === undefined && ran.isError === true
      turnFailed = failed
      failStreak = failed ? failStreak + 1 : 0
      if (failed) {
        playOnce(failStreak >= RAGE_AFTER_FAILURES ? 'laptop-rage' : 'fail', await $.clock.now())
      }
      return ran
    } finally {
      running.delete(id)
    }
  }).catch(($, e, next) => next(e))

  on('ui.render', { component: 'Spinner' }, async ($, e, next) => {
    if (e.surface !== 'terminal') {
      return next(e)
    }
    const { kind, box, hide } = await prefs($)
    if (hide) {
      return next(e)
    }
    const now = await $.clock.now()
    const site: Site = { kind: 'spinner', mode: e.props.mode, message: e.props.message, drawn: '' }
    const f = frameOf(e.requestId, site, now, await read($, pinned))
    site.drawn = f.drawn
    sites.set(e.requestId, site)
    startTicker($)
    const els = $.ui.resolve(e)
    const { Box, Text } = els
    return (
      <Box flexDirection="row" gap={1}>
        {await sprite($, els, kind, box, f.anim, f.index)}
        <Box key="words" marginTop={wordsOffset(kind, box)}>
          <Text color="claude">
            {e.props.message ?? e.props.word}
            {e.props.suffix}
          </Text>
        </Box>
      </Box>
    )
  }).catch(($, e, next) => next(e))

  on('ui.render', { component: 'TurnDuration' }, async ($, e, next) => {
    if (e.surface !== 'terminal') {
      return next(e)
    }
    const { kind, box, hide } = await prefs($)
    if (hide) {
      return next(e)
    }
    const now = await $.clock.now()
    let site = sites.get(e.requestId)
    if ((site === undefined || site.kind !== 'turn') && closing !== null && !finished.has(e.requestId)) {
      // The newest turn's closing line: play its animation once.
      site = { kind: 'turn', anim: closing.anim, start: closing.start, drawn: '' }
      sites.set(e.requestId, site)
      closing = null
      startTicker($)
    }
    const anim: Anim = site?.kind === 'turn' ? site.anim : (finished.get(e.requestId) ?? 'confetti')
    finished.set(e.requestId, anim)
    const start = site?.kind === 'turn' ? site.start : 0
    const index = frameAt(FRAMES.animations[anim], start, now, false)
    if (site) {
      site.drawn = `${anim}:${index}`
    }
    const els = $.ui.resolve(e)
    const { Box, Text } = els
    return (
      <Box flexDirection="row" gap={1}>
        {await sprite($, els, kind, box, anim, index)}
        <Box key="words" marginTop={wordsOffset(kind, box)}>
          <Text dimColor>
            {e.props.word} for {formatDuration(e.props.durationMs)}
          </Text>
        </Box>
      </Box>
    )
  }).catch(($, e, next) => next(e))
}
