// Pure decisions for the Oxy pet: which animation plays, which frame shows.
// No `$` here, so every rule is unit-testable on its own.

// The animations the sprite sheet carries: what Oxy does while Claude works,
// and one per ox command (three to choose from for a plan).
export const ANIMS = [
  'blink',
  'wag',
  'wag-big',
  'wave',
  'laptop',
  'robot',
  'float',
  'think',
  'chatter',
  'game',
  'sleep',
  'jump',
  'confetti',
  'fail',
  'murmur',
  'doctor',
  'prime',
  'query',
  'plan-checklist',
  'plan-flowchart',
  'plan-kanban',
  'juggle',
  'laptop-rage',
  'laptop-tabs',
  'laptop-bug',
  'laptop-deploy',
  'laptop-loading',
  'laptop-doze',
] as const

export type Anim = (typeof ANIMS)[number]

export type SpinnerMode = 'requesting' | 'responding' | 'thinking' | 'tool-input' | 'tool-use'

// Picture sizes, smallest first; each is a fixed height in terminal rows and
// as wide as the art's aspect makes it (see FRAMES.sizes).
export const SIZES = ['small', 'medium', 'large'] as const

export type SizeName = (typeof SIZES)[number]

export type Size = { columns: number; rows: number }

// One size: the picture's box in terminal cells, and the half-block grid
// drawn in that same box (each cell shows two pixels stacked).
export type Box = Size & { cells: Size }

// Frame i shows for ms[i] milliseconds. The pictures are PNG files beside
// the mod (`frames/<name>/<i>.png`) and the half-block cells a `cells.json`
// there, read on first use; see `frameFile` and `Cells`.
export type AnimationFrames = { ms: number[] }

// An animation's half-block cells, per size, index-aligned with its frames.
export type Cells = Record<SizeName, string[]>

export type OxyFrames = {
  // a frame's width over its height
  aspect: number
  sizes: Record<SizeName, Box>
  animations: Record<Anim, AnimationFrames>
}

// Oxy's height in CSS pixels where the surface draws pictures as SVG (the
// desktop app, VS Code). The working row there is one ~20 px text line, so
// medium stands a little over two lines tall, as the terminal's medium does
// beside its one-row word.
export const PIXEL_HEIGHTS: Record<SizeName, number> = { small: 32, medium: 48, large: 64 }

// A frame PNG's own size; it must match what `ox pet enable` writes.
export const DESKTOP_FRAME = { width: 160, height: 207 } as const

// The part of the frame the SVG shows, in frame pixels. The full frame by
// default; set to the union of Oxy's bounds across every frame to crop the
// empty margin without cutting any pose.
export const DESKTOP_VIEWBOX = { x: 0, y: 0, width: DESKTOP_FRAME.width, height: DESKTOP_FRAME.height } as const

// The Svg element's `source` limit; a longer tree is refused.
export const SVG_SOURCE_CAP = 131_072

// Upper bounds on clipSvg's markup beside the base64 itself, so packClips
// can pack against the cap without building the string. A test holds
// clipSvg to them.
export const CLIP_IMAGE_MARKUP_MAX = 400
export const CLIP_WRAPPER_MAX = 200

export function pixelBox(size: SizeName): { width: number; height: number } {
  const height = PIXEL_HEIGHTS[size]
  return { width: Math.max(1, Math.round((height * DESKTOP_VIEWBOX.width) / DESKTOP_VIEWBOX.height)), height }
}

/**
 * Splits an animation's frames into clips, runs of consecutive frames whose
 * SVG fits under `cap` characters: the graphical surfaces redraw at most
 * ten times a second, too slow for the frames, so a clip animates itself
 * and only a clip boundary costs a redraw. A frame too big on its own still
 * gets a clip; the caller checks the built source. Each clip is
 * `[start, end)`.
 */
export function packClips(ms: number[], base64Lengths: number[], cap: number): Array<[number, number]> {
  if (ms.length !== base64Lengths.length) {
    throw new Error(`packClips: ${ms.length} durations for ${base64Lengths.length} frames`)
  }
  const clips: Array<[number, number]> = []
  let start = 0
  let used = CLIP_WRAPPER_MAX
  for (let i = 0; i < base64Lengths.length; i++) {
    const cost = base64Lengths[i] + CLIP_IMAGE_MARKUP_MAX
    if (i > start && used + cost > cap) {
      clips.push([start, i])
      start = i
      used = CLIP_WRAPPER_MAX
    }
    used += cost
  }
  if (start < base64Lengths.length) {
    clips.push([start, base64Lengths.length])
  }
  return clips
}

function keyTime(ms: number, total: number): string {
  return String(Math.round((ms / total) * 1e6) / 1e6)
}

/**
 * One clip as an SVG document: every frame an <image>, shown for its own
 * duration by a discrete SMIL animation of its visibility. `loop` repeats
 * the clip forever (an animation that fits in one clip loops with no
 * redraw at all); otherwise it plays once and freezes on its last frame
 * until the next clip replaces it. The first frame is visible without
 * SMIL, so a surface that ignores it still shows Oxy.
 */
export function clipSvg(frames: string[], ms: number[], loop: boolean): string {
  const { x, y, width, height } = DESKTOP_VIEWBOX
  const total = ms.reduce((a, b) => a + b, 0)
  const timing = loop ? 'repeatCount="indefinite"' : 'fill="freeze"'
  let offset = 0
  const images = frames.map((base64, i) => {
    const from = offset
    offset += ms[i]
    const values: string[] = []
    const times: string[] = []
    if (i > 0) {
      values.push('hidden')
      times.push('0')
    }
    values.push('visible')
    times.push(keyTime(from, total))
    if (offset < total) {
      values.push('hidden')
      times.push(keyTime(offset, total))
    }
    const animate =
      frames.length === 1
        ? ''
        : `<animate attributeName="visibility" calcMode="discrete" values="${values.join(';')}" keyTimes="${times.join(';')}" dur="${total}ms" ${timing}/>`
    return (
      `<image width="${DESKTOP_FRAME.width}" height="${DESKTOP_FRAME.height}" visibility="${i === 0 ? 'visible' : 'hidden'}" ` +
      `href="data:image/png;base64,${base64}">${animate}</image>`
    )
  })
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="${x} ${y} ${width} ${height}">${images.join('')}</svg>`
}

export function isSize(name: unknown): name is SizeName {
  return typeof name === 'string' && (SIZES as readonly string[]).includes(name)
}

export function isAnim(name: string): name is Anim {
  return (ANIMS as readonly string[]).includes(name)
}

// A tool running longer than this has Oxy pass the time: with his robot
// friend, his handheld, a soccer ball, or a stuck progress bar, whichever the
// moment picks; longer still, he naps, curled up or slumped at his laptop.
export const IDLE_AFTER_MS = 30_000
export const SLEEP_AFTER_MS = 180_000
const LONG_TOOL_ANIMS = ['robot', 'game', 'juggle', 'laptop-loading'] as const
const SLEEP_ANIMS = ['sleep', 'laptop-doze'] as const

// Two tool failures in a row in one turn: Oxy loses it at his laptop.
export const RAGE_AFTER_FAILURES = 2

// Each ox plan gets one of the three whiteboards.
const PLAN_ANIMS = ['plan-checklist', 'plan-flowchart', 'plan-kanban'] as const

export function planAnim(pick: number): Anim {
  return PLAN_ANIMS[Math.min(PLAN_ANIMS.length - 1, Math.max(0, Math.floor(pick * PLAN_ANIMS.length)))]
}

// SageOx MCP tools, by the tool's own name (the part after the last `__`).
const MCP_ANIMS: Record<string, Anim | 'plan'> = {
  prime: 'prime',
  search: 'query',
  enrichplan: 'plan',
  publishmurmur: 'murmur',
  listmurmurs: 'murmur',
}

const EDIT_TOOLS = new Set(['Edit', 'Write', 'NotebookEdit'])
const WEB_TOOLS = new Set(['WebFetch', 'WebSearch'])
const LOOKUP_TOOLS = new Set(['Read', 'Grep', 'Glob', 'LSP', 'ToolSearch'])

// What Oxy does while an ordinary tool runs, picked once per call so the
// laptop is one choice among several rather than the whole turn.
const BUSY_ANIMS = ['laptop', 'robot', 'juggle', 'wag-big', 'game', 'jump'] as const

/**
 * The animation a tool call earns on its own: an ox command or SageOx MCP
 * tool gets its dedicated animation, a file edit has Oxy at his laptop, a
 * read or search has him peering through his magnifying glass, a test run
 * has him swatting a bug, a push or release launches a rocket, and a web
 * search or fetch pops browser tabs; anything else `null` (the spinner's
 * mode decides). `pick` in [0, 1) chooses among a command's variants.
 */
export function classifyToolCall(tool: string, input: Record<string, unknown>, pick = Math.random()): Anim | null {
  if (tool === 'Bash' && typeof input.command === 'string') {
    return classifyCommand(input.command, pick)
  }
  if (EDIT_TOOLS.has(tool)) {
    return 'laptop'
  }
  if (WEB_TOOLS.has(tool)) {
    return 'laptop-tabs'
  }
  if (LOOKUP_TOOLS.has(tool)) {
    return 'query'
  }
  // mcp__<server>__<tool>; the server name may itself hold underscores.
  const parts = tool.split('__')
  if (parts.length >= 3 && parts[0] === 'mcp' && /sageox/i.test(parts.slice(1, -1).join('__'))) {
    const anim = MCP_ANIMS[(parts.at(-1) ?? '').toLowerCase()] ?? null
    return anim === 'plan' ? planAnim(pick) : anim
  }
  return null
}

/**
 * The animation for the first shell command that earns one: an `ox` command
 * (or `claude doctor`), a test run, or a push or release.
 */
export function classifyCommand(command: string, pick = Math.random()): Anim | null {
  // Blank out quoted text so a separator or "ox" inside a string never counts.
  const bare = command.replace(/'[^']*'|"(?:\\.|[^"\\])*"/g, '""')
  for (const segment of bare.split(/&&|\|\||[;|\n&()]/)) {
    const words = segment.trim().split(/\s+/).filter(Boolean)
    while (/^[A-Za-z_][A-Za-z0-9_]*=/.test(words[0] ?? '')) {
      words.shift()
    }
    const args = positionals(words.slice(1))
    if (/(^|\/)claude$/.test(words[0] ?? '') && args[0] === 'doctor') {
      return 'doctor'
    }
    const shell = classifyShell(words[0] ?? '', args)
    if (shell) {
      return shell
    }
    if (!/(^|\/)ox$/.test(words[0] ?? '')) {
      continue
    }
    const anim = classifyOxArgs(args, pick)
    if (anim) {
      return anim
    }
  }
  return null
}

// ox's global flags that take a value as the next word (`--config <file>`).
const VALUE_FLAGS = new Set(['-c', '--config'])

/** The words of a command line that aren't flags or a flag's value. */
function positionals(words: string[]): string[] {
  const out: string[] = []
  for (let i = 0; i < words.length; i++) {
    const w = words[i]
    if (VALUE_FLAGS.has(w)) {
      i++
    } else if (!w.startsWith('-')) {
      out.push(w)
    }
  }
  return out
}

// Test runners invoked directly, and the package managers and build tools
// whose `test` subcommand runs the tests.
const TEST_RUNNERS = new Set(['pytest', 'jest', 'vitest', 'gotestsum'])
const TEST_SUBCOMMAND = new Set(['go', 'npm', 'pnpm', 'yarn', 'bun', 'cargo', 'make', 'mix', 'deno'])

/** A test run swats a bug; a push, a new PR, or a release launches a rocket. */
function classifyShell(cmd: string, args: string[]): Anim | null {
  const name = cmd.split('/').at(-1) ?? ''
  const [sub, sub2] = args
  if (TEST_RUNNERS.has(name) || (TEST_SUBCOMMAND.has(name) && /^test(-|$)/.test(sub ?? ''))) {
    return 'laptop-bug'
  }
  if (name === 'git' && sub === 'push') {
    return 'laptop-deploy'
  }
  if (name === 'gh' && ((sub === 'pr' && sub2 === 'create') || (sub === 'release' && sub2 === 'create'))) {
    return 'laptop-deploy'
  }
  if (name === 'make' && sub === 'release') {
    return 'laptop-deploy'
  }
  return null
}

function classifyOxArgs(args: string[], pick: number): Anim | null {
  const [sub, sub2] = args
  if (sub === 'prime' || (sub === 'agent' && args.includes('prime'))) {
    return 'prime'
  }
  if (sub === 'query' || (sub === 'conversation' && sub2 === 'search')) {
    return 'query'
  }
  if (sub === 'plan') {
    return planAnim(pick)
  }
  if (sub === 'murmur') {
    return 'murmur'
  }
  if (sub === 'doctor') {
    return 'doctor'
  }
  return null
}

// What Oxy does for each thing Claude is doing.
const MODE_ANIMS: Record<SpinnerMode, Anim> = {
  requesting: 'think',
  thinking: 'think',
  responding: 'chatter',
  'tool-input': 'wag',
  'tool-use': 'laptop',
}

// Spinner text the engine shows when something is wrong or stalled, as
// opposed to a todo's "Running tests", which also arrives as `message`.
const TROUBLE = /retry|retrying|rate.?limit|overloaded|reconnect|waiting for|interrupted|timed out/i

export type Activity = { anim: Anim | null; since: number }

export type OneShot = { anim: Anim; start: number; until: number }

export type SpinnerState = {
  now: number
  mode: SpinnerMode | string
  message: string | null
  /** A one-pass animation (wave, fail) in progress. */
  oneShot: OneShot | null
  /** The tools running in this loop, oldest first. */
  activities: Activity[]
  /** `/oxy <anim>` pins one animation until `/oxy auto`. */
  pinned: Anim | null
}

// A stable pick in [0, n) from a tool's start time, so one long tool keeps
// one pastime; never negative, whatever the clock says.
function slot(since: number, n: number): number {
  return ((Math.floor(since) % n) + n) % n
}

/** The animation the thinking line plays right now, highest priority first. */
export function pickAnim(s: SpinnerState): Anim {
  if (s.pinned) {
    return s.pinned
  }
  if (s.oneShot && s.now < s.oneShot.until) {
    return s.oneShot.anim
  }
  if (s.message !== null && TROUBLE.test(s.message)) {
    return 'float'
  }
  const dedicated = s.activities.find(a => a.anim !== null)
  if (dedicated?.anim) {
    return dedicated.anim
  }
  const oldest = s.activities[0]
  if (oldest && s.now - oldest.since > SLEEP_AFTER_MS) {
    return SLEEP_ANIMS[slot(oldest.since, SLEEP_ANIMS.length)]
  }
  if (oldest && s.now - oldest.since > IDLE_AFTER_MS) {
    return LONG_TOOL_ANIMS[slot(oldest.since, LONG_TOOL_ANIMS.length)]
  }
  if (oldest) {
    return BUSY_ANIMS[slot(oldest.since, BUSY_ANIMS.length)]
  }
  return MODE_ANIMS[s.mode as SpinnerMode] ?? 'blink'
}

/** The PNG file of one frame, under the mod's folder. */
export function frameFile(root: string, anim: Anim, index: number): string {
  return `${root}/hooks/frames/${anim}/${index}.png`
}

/** The cells.json of an animation, under the mod's folder. */
export function cellsFile(root: string, anim: Anim): string {
  return `${root}/hooks/frames/${anim}/cells.json`
}

/** How long one pass of an animation takes, in milliseconds. */
export function passMs(frames: AnimationFrames): number {
  return frames.ms.reduce((sum, ms) => sum + ms, 0)
}

/**
 * The frame index at `now` for an animation started at `start`: looping, or
 * held on the last frame once a single pass is done.
 */
export function frameAt(frames: AnimationFrames, start: number, now: number, loop: boolean): number {
  const total = passMs(frames)
  let t = Math.max(0, now - start)
  if (total <= 0) {
    return 0
  }
  if (loop) {
    t %= total
  } else if (t >= total) {
    return frames.ms.length - 1
  }
  for (let i = 0; i < frames.ms.length; i++) {
    t -= frames.ms[i]
    if (t < 0) {
      return i
    }
  }
  return frames.ms.length - 1
}

/** A turn's duration the way Claude Code's closing line says it: 3s, 1m 4s, 1h 2m. */
export function formatDuration(ms: number): string {
  const total = Math.max(0, Math.round(ms / 1000))
  const h = Math.floor(total / 3600)
  const m = Math.floor((total % 3600) / 60)
  const s = total % 60
  if (h > 0) {
    return `${h}h ${m}m`
  }
  if (m > 0) {
    return `${m}m ${s}s`
  }
  return `${s}s`
}
