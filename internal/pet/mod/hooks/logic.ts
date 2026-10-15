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

export function isSize(name: unknown): name is SizeName {
  return typeof name === 'string' && (SIZES as readonly string[]).includes(name)
}

export function isAnim(name: string): name is Anim {
  return (ANIMS as readonly string[]).includes(name)
}

// A tool running longer than this has Oxy pass the time: with his robot
// friend or his handheld, whichever the moment picks; longer still, he naps.
export const IDLE_AFTER_MS = 30_000
export const SLEEP_AFTER_MS = 180_000
const LONG_TOOL_ANIMS = ['robot', 'game'] as const

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

/**
 * The animation a tool call earns on its own: an ox command or SageOx MCP
 * tool gets its dedicated animation, a file edit has Oxy at his laptop,
 * anything else `null` (the spinner's mode decides). `pick` in [0, 1) chooses
 * among a command's variants.
 */
export function classifyToolCall(tool: string, input: Record<string, unknown>, pick = Math.random()): Anim | null {
  if (tool === 'Bash' && typeof input.command === 'string') {
    return classifyCommand(input.command, pick)
  }
  if (EDIT_TOOLS.has(tool)) {
    return 'laptop'
  }
  // mcp__<server>__<tool>; the server name may itself hold underscores.
  const parts = tool.split('__')
  if (parts.length >= 3 && parts[0] === 'mcp' && /sageox/i.test(parts.slice(1, -1).join('__'))) {
    const anim = MCP_ANIMS[(parts.at(-1) ?? '').toLowerCase()] ?? null
    return anim === 'plan' ? planAnim(pick) : anim
  }
  return null
}

/** The animation for the first `ox` (or `claude doctor`) invocation in a shell command, if any. */
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
  'tool-input': 'laptop',
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
  /** `/hatch-pet <anim>` pins one animation until `/hatch-pet auto`. */
  pinned: Anim | null
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
    return 'sleep'
  }
  if (oldest && s.now - oldest.since > IDLE_AFTER_MS) {
    return LONG_TOOL_ANIMS[oldest.since % LONG_TOOL_ANIMS.length]
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
