// Oxy, SageOx's mascot, in Claude Code's thinking line, and under Claude's
// replies in the desktop app. Installed and kept up
// to date by `ox pet enable`; frames.ts is generated from the sprite sheet
// embedded in the ox binary.
//
// Every hook falls back to `next(e)`: a pet bug must never cost a turn.

import { atom, read, update } from 'claude-code'
import type { Elements, EngineInterface, Register, RenderSurface } from 'claude-code'

import type { Placement, Renderer, Size } from '../types'
import { FRAMES } from './frames'
import {
  ANIMS,
  cellsFile,
  classifyToolCall,
  clipSvg,
  clipTimings,
  formatDuration,
  frameAt,
  frameFile,
  hiresFrameFile,
  hiresLayout,
  isAnim,
  isSize,
  packClips,
  passMs,
  pickAnim,
  pixelBox,
  RAGE_AFTER_FAILURES,
  SVG_SOURCE_CAP,
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
const placement = atom({ plugin: 'ox-cli-oxy', key: 'placement' } as const, null as Placement | null)

// What outlives a session: the terminal's picture support, read once per
// process, and the size and visibility `$.store` keeps across sessions. A
// read that fails draws with the defaults and is tried again next time.
let terminal: Renderer | null = null
let saved: { size: Size; hidden: boolean; place: Placement } | null = null

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

async function savedPrefs($: EngineInterface): Promise<{ size: Size; hidden: boolean; place: Placement }> {
  if (saved === null) {
    try {
      const box = await $.store.get('size')
      saved = {
        size: isSize(box) ? box : 'medium',
        hidden: (await $.store.get('hidden')) === true,
        place: (await $.store.get('placement')) === 'pane' ? 'pane' : 'reply',
      }
    } catch {
      return { size: 'medium', hidden: false, place: 'reply' }
    }
  }
  return saved
}

// The renderer, size, visibility and desktop placement in force: this
// session's choice, else what the terminal supports and what was saved.
async function prefs($: EngineInterface): Promise<{ kind: Renderer; box: Size; hide: boolean; place: Placement }> {
  const kind = await read($, renderer)
  const box = await read($, size)
  const hide = await read($, hidden)
  const place = await read($, placement)
  const fallback = await savedPrefs($)
  return {
    kind: kind ?? (await terminalRenderer($)),
    box: box ?? fallback.size,
    hide: hide ?? fallback.hidden,
    place: place ?? fallback.place,
  }
}

const KEY = 'oxy'
const MAIN = 'main'
// The desktop's side pane, and the `ui.render` requestId it draws under.
const PANE = 'oxy'
// The narrowest dock the surface allows: 24 cells is about 190 px, room for
// large Oxy holding his widest prop (~105 px) without a wide empty column.
const PANE_COLUMNS = 24
const TICK_MS = 50
const USAGE = '/oxy [on|off|auto|reply|pane|small|medium|large|image|cells|<animation>]'
// The longest reply text the Markdown element draws; a longer block keeps
// the engine's own drawing.
const MARKDOWN_MAX = 100_000
// Rows of space between a reply's text and Oxy standing under it.
const REPLY_GAP = 1

type Running = Activity & { loop: string }

// The clip an SVG surface's site shows, and when it started playing there.
// Null on the terminal, which blits each frame instead.
type Clip = { anim: Anim; index: number; loop: boolean; shownAt: number }

// A drawn site: the thinking line of one loop, one turn's closing line
// (terminal only: no other surface raises TurnDuration), the desktop's side
// pane, or the newest reply he stands under; in those last two he works
// during a turn and rests between turns.
type Site =
  | { kind: 'spinner'; surface: RenderSurface; mode: string; message: string | null; drawn: string; clip: Clip | null }
  | { kind: 'turn'; anim: Anim; start: number; drawn: string }
  | { kind: 'rest'; surface: RenderSurface; working: boolean; drawn: string; clip: Clip | null }
  | { kind: 'reply'; surface: RenderSurface; working: boolean; drawn: string; clip: Clip | null }

// What Oxy does between turns: a pinned animation, else he idles.
const REST_ANIM: Anim = 'blink'

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
// Whether a turn is running: the Pane's props, unlike the Spinner's, carry
// no working flag, so turn.start and turn.complete keep it.
let turnRunning = false
let ticker: { cancel: () => void } | null = null
// The reply blocks Oxy stands under: each turn's last block of text on the
// main loop, by the turn's id. Replies drawn before the mod loaded (a resume,
// a reload) have none. `currentTurn` is the turn running, if any; the newest
// reply is the one he animates under, the older ones show a still.
let currentTurn: string | null = null
const turnBlocks = new Map<string, string>()
let newestReply: string | null = null
// Every reply block drawn so far: a redraw of one is never a new last block.
const seenBlocks = new Set<string>()
// The newest reply's celebration once its turn ends (confetti, or a fall
// when it failed), played once before he rests.
let replyClosing: OneShot | null = null
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

// The SVG surfaces can't open a file the way the terminal's Image does, so
// the PNG bytes go into the tree, packed into self-animating clips. Only the
// animations on show are kept, the two most recently used (a thinking line
// and a reply can each be playing one): all of them at once would be
// megabytes held for frames rarely drawn again. `hires` says which art the frames are: the
// high-resolution set when frames.ts has it and every read succeeds, else
// the 160x207 frames the terminal draws.
type Clips = { anim: Anim; hires: boolean; ms: number[]; frames: string[]; ranges: Array<[number, number]> }
type ClipEntry = { wantHires: boolean; loading: Promise<Clips>; ready: Clips | null }
const CLIP_CACHE_MAX = 2
// Least recently used first: a Map keeps insertion order.
const clipCache = new Map<Anim, ClipEntry>()

function readClips($: EngineInterface, anim: Anim, hires: boolean): Promise<Clips> {
  // Each art has its own frame count and timing: the hires set runs faster.
  const ms = clipTimings(FRAMES, anim, hires)
  const file = hires ? hiresFrameFile : frameFile
  return Promise.all(ms.map((_, i) => $.fs.read(file($.plugin.root, anim, i), { as: 'bytes' }).then(b => b.base64))).then(
    frames => ({ anim, hires, ms, frames, ranges: packClips(ms, frames.map(f => f.length), SVG_SOURCE_CAP) }),
  )
}

function clipsOf($: EngineInterface, anim: Anim): Promise<Clips> {
  // An older frames.ts has no hires block, and one may lack (or carry an
  // unusable box for) this animation: those draw the 160x207 frames.
  const wantHires = FRAMES.hires !== undefined && hiresLayout(FRAMES.hires, anim, 'medium') !== null
  let entry = clipCache.get(anim)
  if (entry === undefined || entry.wantHires !== wantHires) {
    const loading = wantHires ? readClips($, anim, true).catch(() => readClips($, anim, false)) : readClips($, anim, false)
    const fresh: ClipEntry = { wantHires, loading, ready: null }
    loading.then(
      c => {
        fresh.ready = c
      },
      () => {
        // A failed read is tried again on the next draw, not cached.
        if (clipCache.get(anim) === fresh) {
          clipCache.delete(anim)
        }
      },
    )
    entry = fresh
  }
  clipCache.delete(anim)
  clipCache.set(anim, entry)
  for (const old of clipCache.keys()) {
    if (clipCache.size <= CLIP_CACHE_MAX) {
      break
    }
    clipCache.delete(old)
  }
  return entry.loading
}

function clipMs(clips: Clips, [start, end]: [number, number]): number {
  return clips.ms.slice(start, end).reduce((a, b) => a + b, 0)
}

// Moves an SVG site to its next clip once the one on show has played out;
// true when that takes a redraw. A one-shot holds its last clip (frozen on
// the last frame), and a looping animation that fits one clip loops inside
// the SVG, so neither redraws.
function advanceClip(site: { clip: Clip | null }, anim: Anim, loop: boolean, now: number): boolean {
  const clip = site.clip
  if (clip === null || clip.anim !== anim || clip.loop !== loop) {
    site.clip = { anim, index: 0, loop, shownAt: now }
    return true
  }
  const loaded = clipCache.get(anim)?.ready ?? null
  const range = loaded?.ranges[clip.index]
  if (loaded == null || range === undefined || now - clip.shownAt < clipMs(loaded, range)) {
    return false
  }
  if (clip.index + 1 < loaded.ranges.length) {
    clip.index++
  } else if (loop && loaded.ranges.length > 1) {
    clip.index = 0
  } else {
    return false
  }
  clip.shownAt = now
  return true
}

// The clip an SVG site shows now, registered and ticking: null when
// nothing can be drawn (frames unreadable, no layout, over the cap), so
// the caller hands the site back to the engine.
async function clipOf(
  $: EngineInterface,
  requestId: string,
  site: Site & { kind: 'spinner' | 'rest' | 'reply' },
  f: { anim: Anim; loop: boolean },
  box: Size,
  now: number,
) {
  let loaded: Clips
  try {
    loaded = await clipsOf($, f.anim)
  } catch {
    return null
  }
  // A redraw for other props keeps the clip's place; the ticker owns when
  // it moves on.
  const prev = sites.get(requestId)
  const kept = prev !== undefined && prev.kind === site.kind ? prev.clip : null
  site.clip =
    kept !== null && kept.anim === f.anim && kept.loop === f.loop && kept.index < loaded.ranges.length
      ? kept
      : { anim: f.anim, index: 0, loop: f.loop, shownAt: now }
  const [start, end] = loaded.ranges[site.clip.index]
  const ms = loaded.ms.slice(start, end)
  const layout = loaded.hires && FRAMES.hires !== undefined ? hiresLayout(FRAMES.hires, f.anim, box) : null
  if (loaded.hires && layout === null) {
    return null
  }
  const source = clipSvg(loaded.frames.slice(start, end), ms, f.loop && loaded.ranges.length === 1, layout ?? undefined)
  if (source.length > SVG_SOURCE_CAP) {
    return null
  }
  site.drawn = `${f.anim}#${site.clip.index}`
  sites.set(requestId, site)
  startTicker($)
  return { source, px: layout ?? pixelBox(box), layout }
}

// Oxy standing still under an older reply: one frame of his resting
// animation, read alone so it never evicts the clip on show. Kept per size;
// null when it can't be drawn, and a failed read is tried again.
const stills = new Map<Size, Promise<{ source: string; px: { width: number; height: number } } | null>>()

function stillOf($: EngineInterface, box: Size) {
  let still = stills.get(box)
  if (still === undefined) {
    const layout = FRAMES.hires !== undefined ? hiresLayout(FRAMES.hires, REST_ANIM, box) : null
    const file = layout !== null ? hiresFrameFile : frameFile
    const ms = clipTimings(FRAMES, REST_ANIM, layout !== null)
    still = $.fs
      .read(file($.plugin.root, REST_ANIM, 0), { as: 'bytes' })
      .then(b => {
        const source = clipSvg([b.base64], [ms[0] ?? 100], false, layout ?? undefined)
        return source.length > SVG_SOURCE_CAP ? null : { source, px: layout ?? pixelBox(box) }
      })
      .catch(() => {
        stills.delete(box)
        return null
      })
    stills.set(box, still)
  }
  return still
}

// A one-shot lasts the longer of its two arts' passes, so an SVG clip at
// the hires timing reaches its frozen last frame before the next animation
// takes over; the terminal simply holds its last frame for the difference.
function oneShotOf(anim: Anim, now: number): OneShot {
  const pass = Math.max(passMs(FRAMES.animations[anim]), passMs({ ms: clipTimings(FRAMES, anim, true) }))
  return { anim, start: now, until: now + pass }
}

function playOnce(anim: Anim, now: number) {
  oneShot = oneShotOf(anim, now)
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
    return { anim: site.anim, index, drawn: `${site.anim}:${index}`, loop: false }
  }
  if (site.kind === 'reply' && !site.working && pin === null && replyClosing !== null && now < replyClosing.until) {
    const anim = replyClosing.anim
    const index = frameAt(FRAMES.animations[anim], replyClosing.start, now, false)
    return { anim, index, drawn: `${anim}:${index}`, loop: false }
  }
  if ((site.kind === 'rest' || site.kind === 'reply') && !site.working) {
    const anim: Anim = pin !== null && isAnim(pin) ? pin : REST_ANIM
    const index = frameAt(FRAMES.animations[anim], loopStart, now, true)
    return { anim, index, drawn: `${anim}:${index}`, loop: true }
  }
  // A working pane plays what the main loop's thinking line would.
  const anim =
    site.kind === 'rest' || site.kind === 'reply' ? spinnerAnim(MAIN, { mode: 'thinking', message: null }, now, pin) : spinnerAnim(requestId, site, now, pin)
  // One-shot timing only while that one-shot is playing; a pinned or
  // expired one loops like any other animation.
  const shot = pin === null && oneShot !== null && now < oneShot.until && anim === oneShot.anim ? oneShot : null
  const index = frameAt(FRAMES.animations[anim], shot?.start ?? loopStart, now, shot === null)
  return { anim, index, drawn: `${anim}:${index}`, loop: shot === null }
}

// Swaps each drawn site to its next frame: a blit on the terminal, no
// redraw. The SVG surfaces have no blit and redraw at most ten times a
// second, too slow for 40 ms frames, so each SVG plays a clip of frames
// itself and this asks for one redraw per clip boundary.
async function tick($: EngineInterface) {
  const now = await $.clock.now()
  const pin = await read($, pinned)
  const { kind, box } = await prefs($)
  let redraw = false
  for (const [requestId, site] of sites) {
    const f = frameOf(requestId, site, now, pin)
    if (site.kind !== 'turn' && site.surface !== 'terminal') {
      redraw = advanceClip(site, f.anim, f.loop, now) || redraw
      continue
    }
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
  if (redraw) {
    $.ui.invalidate('ui.render')
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

// Opens Oxy's side pane where the desktop app is attached and `/oxy pane`
// chose it. Its thinking row is one text line tall and clips pictures to it,
// and its band above the prompt sits in a full-width card a plugin can't
// restyle, so by default he stands under each reply instead; the pane is the
// other choice. `transparent` asks the surface to paint nothing under the
// body, so there is no card there either. A terminal never opens it: there
// he lives in the thinking line. Null when nothing was opened.
async function openPane($: EngineInterface) {
  const { hide, place } = await prefs($)
  if (hide || place !== 'pane' || !(await $.session.surfaces()).includes('desktop')) {
    return null
  }
  return $.ui.open({ id: PANE, title: 'Oxy', backgroundColor: 'transparent', columns: PANE_COLUMNS })
}

async function closePane($: EngineInterface) {
  sites.delete(PANE)
  if ((await $.ui.panes()).some(p => p.id === PANE)) {
    await $.ui.close({ id: PANE })
  }
}

async function setHidden($: EngineInterface, hide: boolean) {
  await update($, hidden, () => hide)
  await $.store.set('hidden', hide)
  saved = { ...(await savedPrefs($)), hidden: hide }
}

export const register: Register = on => {
  on('session.start', async ($, e, next) => {
    await $.command.register({
      name: 'oxy',
      description: 'Oxy in the thinking line, or under replies on desktop: off, auto, reply, pane, a size, image, cells, or an animation to pin',
      argumentHint: '[on|off|auto|reply|pane|small|medium|large|image|cells|<animation>]',
    })
    await openPane($).catch(() => undefined)
    return next(e)
  })

  // The desktop app may attach after the session starts.
  on('session.attach', async ($, e, next) => {
    const done = await next(e)
    if (e.surface === 'desktop') {
      await openPane($).catch(() => undefined)
    }
    return done
  }).catch(($, e, next) => next(e))

  // A pane the person closes by hand stays closed in later sessions too;
  // /oxy brings it back.
  on('ui.close', { id: PANE }, async ($, e, next) => {
    const done = await next(e)
    sites.delete(PANE)
    if (e.origin.kind === 'person') {
      await setHidden($, true)
    }
    return done
  }).catch(($, e, next) => next(e))

  on('command.run', { command: 'oxy' }, async ($, e) => {
    const arg = e.args.trim().toLowerCase()
    if (arg === '' || arg === 'on' || arg === 'off') {
      const hide = arg === 'off'
      const was = (await prefs($)).hide
      await setHidden($, hide)
      if (hide) {
        await closePane($).catch(() => undefined)
        return { text: 'Oxy is hidden; Claude Code draws its own spinner. /oxy brings him back.' }
      }
      // Asked for, so the surface places it even where an unasked open waits.
      const opened = await openPane($).catch(() => null)
      if (opened !== null) {
        return { text: opened.isPlaced ? 'Oxy is in his pane.' : `Oxy's pane is open but not shown: ${opened.reason}` }
      }
      return { text: was ? 'Oxy hatched into the thinking line.' : 'Oxy is already here.' }
    }
    if (arg === 'reply' || arg === 'pane') {
      await update($, placement, () => arg)
      await $.store.set('placement', arg)
      saved = { ...(await savedPrefs($)), place: arg }
      $.ui.invalidate('ui.render')
      if (arg === 'reply') {
        await closePane($).catch(() => undefined)
        return { text: 'Oxy stands under each of Claude\'s replies on the desktop app.' }
      }
      const opened = await openPane($).catch(() => null)
      if (opened === null) {
        return { text: 'Oxy will stand in a side pane on the desktop app.' }
      }
      return { text: opened.isPlaced ? 'Oxy is in his pane.' : `Oxy's pane is open but not shown: ${opened.reason}` }
    }
    if (arg === 'auto') {
      await update($, pinned, () => null)
      $.ui.invalidate('ui.render')
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
      $.ui.invalidate('ui.render')
      return { text: `Oxy is ${arg} now.` }
    }
    if (isAnim(arg)) {
      await update($, pinned, () => arg)
      $.ui.invalidate('ui.render')
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
    turnRunning = true
    currentTurn = e.turnId
    replyClosing = null
    startTicker($)
    $.ui.invalidate('ui.render')
    return next(e)
  })

  on('turn.complete', async ($, e, next) => {
    const ended = await $.clock.now()
    closing = { anim: turnFailed ? 'fail' : 'confetti', start: ended }
    replyClosing = oneShotOf(closing.anim, ended)
    turnRunning = false
    currentTurn = null
    newestReply = turnBlocks.get(e.turnId) ?? newestReply
    $.ui.invalidate('ui.render')
    // An SVG surface never says its Spinner went away (the terminal's blit
    // does, by a deny), and the turn's end is when it does; past it the
    // ticker would redraw for nothing.
    for (const [requestId, site] of sites) {
      if ((site.kind === 'spinner' && site.surface !== 'terminal') || (site.kind === 'reply' && requestId !== newestReply)) {
        sites.delete(requestId)
      }
    }
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

  // Oxy stands at the foot of his pane: the working animations while a
  // turn runs, resting (blink, or the pinned animation) between turns.
  on('ui.render', { component: 'Pane', requestId: PANE }, async ($, e, next) => {
    const { box, hide } = await prefs($)
    // The terminal has no Svg, and never opens this pane itself.
    if (hide || e.surface === 'terminal') {
      sites.delete(e.requestId)
      return next(e)
    }
    const now = await $.clock.now()
    const site: Site & { kind: 'rest' } = { kind: 'rest', surface: e.surface, working: turnRunning, drawn: '', clip: null }
    const f = frameOf(e.requestId, site, now, await read($, pinned))
    const clip = await clipOf($, e.requestId, site, f, box, now)
    if (clip === null) {
      return next(e)
    }
    const { Box, Svg } = $.ui.resolve(e)
    return (
      <Box flexDirection="column" justifyContent="flex-end" alignItems="center" height={Math.max(1, e.props.scroll.bodyRows)}>
        {/* Never `isInteractive`; see the Spinner hook. */}
        <Svg key={KEY} source={clip.source} alt="Oxy" width={clip.px.width} height={clip.px.height} />
      </Box>
    )
  }).catch(($, e, next) => next(e))

  // On the desktop and VS Code, Oxy stands under the last block of text of
  // each reply: the reply drawn as markdown, then him. The newest reply's
  // Oxy works through the turn and rests after it; older ones stand still.
  on('ui.render', { component: 'AssistantMessage' }, async ($, e, next) => {
    if (e.surface === 'terminal' || e.props.isSummary === true || e.props.text.length > MARKDOWN_MAX) {
      return next(e)
    }
    const { box, hide, place } = await prefs($)
    if (hide || place !== 'reply') {
      sites.delete(e.requestId)
      return next(e)
    }
    // A block first drawn during a turn becomes that turn's last; a redraw
    // of an earlier one (this turn's or an old reply's) changes nothing.
    if (!seenBlocks.has(e.requestId)) {
      seenBlocks.add(e.requestId)
      if (currentTurn !== null) {
        // The block he stood under until now: this turn's previous last, or,
        // for the turn's first block, the last reply he worked under while
        // the turn had no text (running commands, reading files).
        const prev = turnBlocks.get(currentTurn) ?? newestReply
        turnBlocks.set(currentTurn, e.requestId)
        if (prev !== null) {
          sites.delete(prev)
          $.ui.invalidate('ui.render')
        }
      }
    }
    if (![...turnBlocks.values()].includes(e.requestId)) {
      return next(e)
    }
    // He animates under the running turn's last block, or, until that turn
    // writes any text, under the newest reply, working through its commands.
    const live =
      currentTurn !== null
        ? (turnBlocks.get(currentTurn) ?? newestReply) === e.requestId
        : newestReply === e.requestId
    let art: { source: string; px: { width: number; height: number } } | null
    if (live && e.props.onScreen !== null) {
      const now = await $.clock.now()
      const site: Site & { kind: 'reply' } = { kind: 'reply', surface: e.surface, working: turnRunning, drawn: '', clip: null }
      const f = frameOf(e.requestId, site, now, await read($, pinned))
      art = await clipOf($, e.requestId, site, f, box, now)
    } else {
      sites.delete(e.requestId)
      art = await stillOf($, box)
    }
    if (art === null) {
      return next(e)
    }
    const { Box, Markdown, Svg } = $.ui.resolve(e)
    return (
      <Box flexDirection="column">
        <Markdown key="reply" text={e.props.text} />
        <Box key="oxy-row" flexDirection="row" marginTop={REPLY_GAP}>
          {/* Never `isInteractive`; see the Spinner hook. */}
          <Svg key={KEY} source={art.source} alt="Oxy" width={art.px.width} height={art.px.height} />
        </Box>
      </Box>
    )
  }).catch(($, e, next) => next(e))

  on('ui.render', { component: 'Spinner' }, async ($, e, next) => {
    const { kind, box, hide } = await prefs($)
    if (hide) {
      return next(e)
    }
    if (e.surface === 'desktop') {
      // The desktop's thinking row is one text line tall and clips him to it:
      // he stands in the side pane instead; see openPane.
      return next(e)
    }
    const now = await $.clock.now()
    const site: Site = { kind: 'spinner', surface: e.surface, mode: e.props.mode, message: e.props.message, drawn: '', clip: null }
    const f = frameOf(e.requestId, site, now, await read($, pinned))
    if (e.surface !== 'terminal') {
      const clip = await clipOf($, e.requestId, site, f, box, now)
      if (clip === null) {
        return next(e)
      }
      const { source, px, layout } = clip
      const { Box, Svg, Text } = $.ui.resolve(e)
      // Hires SVGs differ in height with their props but all end on the
      // ground line, so the row aligns their bottoms and lifts the words to
      // his middle; the fallback frames are all one box and simply centre.
      return (
        <Box flexDirection="row" gap={1} alignItems={layout ? 'flex-end' : 'center'}>
          {/* Never `isInteractive`: the desktop scrubs an interactive Svg with a
              forbid-list that drops every `<image>` and any href not `#id`, so
              the PNG frames vanish and Oxy is drawn as his alt text. As a plain
              image the markup is not scrubbed, data: PNGs load, and Chromium
              runs SMIL inside an <img>, so the clip still animates. */}
          <Svg key={KEY} source={source} alt="Oxy" width={px.width} height={px.height} />
          <Box key="words" marginBottom={layout?.wordsLift ?? 0}>
            <Text color="claude">
              {e.props.message ?? e.props.word}
              {e.props.suffix}
            </Text>
          </Box>
        </Box>
      )
    }
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
