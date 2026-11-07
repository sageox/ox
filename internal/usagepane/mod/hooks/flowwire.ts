// Pure glue between the engine's events and the flow modules: which setting
// is on, how wide a lane is drawn, how a finished call is read, and which
// prompt row a hook attachment belongs to. No `$` here, so register.tsx stays
// readable and every rule is unit-testable.

import type { FlowEvent } from '../types'
import { byteTokens, classifyBash, classifyMcp, finishItems, finishTokens, type FlowStart } from './flow'
import { ANIM_MS } from './scene'

// --- The setting: /ox-session-report flow [on|off|still] ----------------------

export type FlowMode = 'on' | 'off' | 'still'

/** The store key holding the flow setting: true (on), false (off) or 'still'. */
export const FLOW_KEY = 'flow'

/** flowMode reads the stored value: unset or anything unknown is on. */
export function flowMode(stored: unknown): FlowMode {
  if (stored === false) {
    return 'off'
  }
  if (stored === 'still') {
    return 'still'
  }
  return 'on'
}

/** flowStored is what `flow <mode>` stores. */
export function flowStored(mode: FlowMode): boolean | 'still' {
  return mode === 'off' ? false : mode === 'still' ? 'still' : true
}

/** parseFlowArg: the mode `flow <arg>` names, undefined to report it, null when unknown. */
export function parseFlowArg(arg: string): FlowMode | undefined | null {
  const a = arg.trim().toLowerCase()
  if (a === '') {
    return undefined
  }
  return a === 'on' || a === 'off' || a === 'still' ? a : null
}

// --- Width --------------------------------------------------------------------

/** CSS px per cell of a remote surface's code font (13px Menlo-like advance). */
export const PX_PER_CELL = 7.8
export const LANE_MIN_PX = 240
export const LANE_MAX_PX = 960
export const LANE_DEFAULT_PX = 640

/** laneWidth: the lane's px width from the surface's cell width, clamped; the default when unmeasured. */
export function laneWidth(viewport: { columns?: number } | undefined): number {
  const cols = viewport?.columns
  if (typeof cols !== 'number' || !Number.isFinite(cols) || cols <= 0) {
    return LANE_DEFAULT_PX
  }
  return Math.min(LANE_MAX_PX, Math.max(LANE_MIN_PX, Math.round(cols * PX_PER_CELL)))
}

// --- Settling -----------------------------------------------------------------

/**
 * settleDelay: ms until every finished event in the row has settled (plus a
 * little slack), or null when nothing is in flight. A running event is not
 * waited on here: its finish redraws the row.
 */
export function settleDelay(events: readonly FlowEvent[], now: number): number | null {
  let latest = -Infinity
  for (const e of events) {
    if (e.endedAt !== undefined && now - e.endedAt < ANIM_MS) {
      latest = Math.max(latest, e.endedAt)
    }
  }
  return latest === -Infinity ? null : Math.max(1, ANIM_MS - (now - latest) + 50)
}

// --- Classifying a call, and reading how it ended -------------------------------

/** isMcpName: cheap prefix check before anything else runs for an MCP call. */
export function isMcpName(tool: unknown): tool is string {
  return typeof tool === 'string' && tool.startsWith('mcp__')
}

/**
 * withSent: an `out` start carrying the size of what the AI coworker sent:
 * an MCP call's arguments as JSON, or a murmur post's command line when its
 * body is literally in it. Any other Bash `out` call sends what it names but
 * does not hold (a `--file`, the recorded session, a `$MESSAGE`, a backtick
 * or a redirected input the shell expands); that is not read, so its start
 * carries no `sent` and finishOf marks its size unknown.
 */
export function withSent(start: FlowStart | null, payload: unknown): FlowStart | null {
  if (start === null || start.direction !== 'out') {
    return start
  }
  if (typeof payload === 'string' && (start.command !== 'murmur' || /[$`<]/.test(payload))) {
    return start
  }
  let text: string | undefined
  try {
    text = typeof payload === 'string' ? payload : JSON.stringify(payload)
  } catch {
    text = undefined
  }
  return typeof text === 'string' ? { ...start, sent: byteTokens(text) } : start
}

/** startOf: the flow a call starts, by tool and input; null for a call that moves no team data. */
export function startOf(tool: string, input: unknown): FlowStart | null {
  if (tool === 'Bash') {
    const command = input !== null && typeof input === 'object' ? (input as { command?: unknown }).command : undefined
    return typeof command === 'string' ? withSent(classifyBash(command), command) : null
  }
  return isMcpName(tool) ? withSent(classifyMcp(tool), input) : null
}

/** What `next(e)` resolved to in a `tool.call` hook, loosely. */
export type RanLike = {
  deny?: string
  isError?: boolean
  text?: string
  result?: unknown
}

export type FinishFields = {
  tokens: number
  items?: number
  itemsNoun?: string
  failed?: boolean
  interrupted?: boolean
  unsized?: boolean
}

function field(o: unknown, key: string): unknown {
  return o !== null && typeof o === 'object' ? (o as Record<string, unknown>)[key] : undefined
}

/**
 * finishOf: what a finished call moved. An `out` call counts what it sent
 * (withSent), never its acknowledgement, and is `unsized` when the start
 * could not measure it; an `in` call counts its result: the model's text
 * first, a Bash record's stdout when core gave none. A deny or an error
 * fails it; Bash's own `interrupted` flag marks an interrupt.
 */
export function finishOf(ran: RanLike, start: FlowStart): FinishFields {
  const stdout = field(ran.result, 'stdout')
  const text = typeof ran.text === 'string' ? ran.text : typeof stdout === 'string' ? stdout : undefined
  const failed = ran.deny !== undefined || ran.isError === true
  const interrupted = field(ran.result, 'interrupted') === true
  let out: FinishFields
  if (failed) {
    out = { tokens: 0, failed: true }
  } else if (start.direction === 'out') {
    out = typeof start.sent === 'number' ? { tokens: start.sent } : { tokens: 0, unsized: true }
  } else {
    out = { tokens: finishTokens(text), ...finishItems(text, start) }
  }
  if (interrupted) {
    out.interrupted = true
  }
  return out
}

// --- Which prompt row a hook attachment belongs to ------------------------------
//
// `prompt.attachment` carries no message id, and the engine raises it as a
// request carries it: SessionStart context with the first prompt's first
// request, UserPromptSubmit context with that prompt's. So the rule is:
//
//   a hook attachment belongs to the composer prompt row first drawn after
//   the latest prompt submit.
//
// A submit opens a new window. A composer UserMessage row drawn for the first
// time inside the window is that prompt's row: attachments that arrived
// before it (pending) are attached to it when it draws, and attachments that
// arrive after it go straight to it. Rows drawn before (old rows, redraws)
// never claim. Before any submit has been seen (a reload mid-turn), the
// first composer row drawn claims, so nothing is stranded.

export type Binder<T> = {
  /** Rows (message ids) seen drawn, so a redraw never claims. */
  seen: Set<string>
  /** The prompt row of the current window, once drawn. */
  current: string | null
  /** Attachments that arrived before the window's row was drawn. */
  pending: T[]
  /** Exact attachment texts already taken this window: one raised twice counts once. */
  taken: Set<string>
}

export function newBinder<T>(): Binder<T> {
  return { seen: new Set(), current: null, pending: [], taken: new Set() }
}

/**
 * submitted: a prompt was submitted; the next composer row drawn is its row.
 * The new window forgets the texts taken, so the same recall on a later
 * prompt draws under that prompt too.
 */
export function submitted<T>(b: Binder<T>): void {
  b.current = null
  b.taken.clear()
}

/**
 * arrived: a hook attachment arrived, `text` as the model reads it and `item`
 * what it brought. Returns the prompt row it belongs to now, or null when it
 * waits for that row (kept pending) or was taken already.
 */
export function arrived<T>(b: Binder<T>, text: string, item: T): string | null {
  if (b.taken.has(text)) {
    return null
  }
  b.taken.add(text)
  if (b.current !== null) {
    return b.current
  }
  b.pending.push(item)
  return null
}

/**
 * drawn: a UserMessage row drew. Returns the pending attachments it claims
 * (empty for a redraw, a non-composer row, or nothing pending).
 */
export function drawn<T>(b: Binder<T>, id: string, composer: boolean): T[] {
  if (b.seen.has(id)) {
    return []
  }
  b.seen.add(id)
  if (!composer || b.current !== null) {
    return []
  }
  b.current = id
  const claimed = b.pending
  b.pending = []
  return claimed
}
