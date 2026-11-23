// Pure glue between the engine's events and the flow modules: which setting
// is on, how wide a lane is drawn and how a finished call is read. No `$`
// here, so register.tsx stays readable and every rule is unit-testable.

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
 * withSent: an `out` start carrying the size of what the AI teammate sent:
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
