// Pure helpers for the ox brief pane: parsing `ox brief --json`, spotting an
// ox command in a Bash call, and laying out the pane's lines. No `$` here, so
// every function is unit-testable under `claude plugin test`.

import type { UsageRecentCall, UsageReport } from '../types'

export const PANE = 'ox-brief'
export const PLUGIN = 'ox-cli-brief-pane'
export const REFRESH_MS = 15_000
export const RUN_TIMEOUT_MS = 8_000
export const BRIEF_ARGV = ['ox', 'brief', '--json', '--recent', '0'] as const

const ELLIPSIS = '…'

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

function num(v: unknown): number {
  return typeof v === 'number' && Number.isFinite(v) ? v : 0
}

function str(v: unknown): string | undefined {
  return typeof v === 'string' ? v : undefined
}

// parseReport reads `ox brief --json` stdout into the slice the pane draws,
// or null when it is not a session report. Missing fields read as empty.
export function parseReport(stdout: string): UsageReport | null {
  let raw: unknown
  try {
    raw = JSON.parse(stdout)
  } catch {
    return null
  }
  if (!isRecord(raw)) {
    return null
  }
  if (raw.view !== undefined && raw.view !== 'session') {
    return null
  }
  if (!isRecord(raw.session) && !isRecord(raw.calls) && !isRecord(raw.trace)) {
    return null
  }
  const session = isRecord(raw.session) ? raw.session : {}
  const trace = isRecord(raw.trace) ? raw.trace : {}
  const calls = isRecord(raw.calls) ? raw.calls : {}
  const injected = isRecord(raw.injected) ? raw.injected : {}
  const used = isRecord(raw.used) ? raw.used : {}
  const recent: UsageRecentCall[] = (Array.isArray(calls.recent) ? calls.recent : [])
    .filter(isRecord)
    .map(c => ({
      label: str(c.label) ?? '',
      command: str(c.command) ?? '',
      seq: typeof c.seq === 'number' ? c.seq : undefined,
      tokens: num(c.tokens),
      failed: c.failed === true,
      ts: str(c.ts),
    }))
  return {
    view: 'session',
    session: {
      name: str(session.name),
      agent_id: str(session.agent_id),
      harness: str(session.harness),
      recording: session.recording === true,
      turns: typeof session.turns === 'number' ? session.turns : undefined,
    },
    trace: {
      available: trace.available !== false,
      source: str(trace.source),
      reason: str(trace.reason),
    },
    calls: { total: num(calls.total), failed: num(calls.failed), recent },
    injected: { total_tokens: num(injected.total_tokens), estimated: injected.estimated === true },
    surfaced: Array.isArray(raw.surfaced) ? raw.surfaced : [],
    used: { status: str(used.status) ?? 'unknown' },
  }
}

// An ox invocation at the start of a command or after a shell separator,
// optionally behind env assignments or a path (./ox, /usr/local/bin/ox).
// `echo "ox query"` and `oxford` do not count.
const OX_COMMAND = /(^|[;&|(]|\$\()\s*(\w+=\S*\s+)*(\S*\/)?ox(\s|$)/

export function isOxCommand(command: unknown): boolean {
  return typeof command === 'string' && OX_COMMAND.test(command)
}

// formatTokens: `~80`, `~1.2k`, `~15k`.
export function formatTokens(n: number): string {
  const t = Math.max(0, Math.round(n))
  if (t < 1000) {
    return `~${t}`
  }
  const k = t / 1000
  return k < 10 ? `~${(Math.round(k * 10) / 10).toFixed(1).replace(/\.0$/, '')}k` : `~${Math.round(k)}k`
}

// formatClock: HH:MM of an RFC 3339 timestamp, shifted by the local offset
// (minutes east of UTC, so the caller passes -Date#getTimezoneOffset()).
export function formatClock(ts: string | undefined, offsetMin = 0): string {
  if (ts === undefined) {
    return '--:--'
  }
  const ms = Date.parse(ts)
  if (Number.isNaN(ms)) {
    return '--:--'
  }
  const d = new Date(ms + offsetMin * 60_000)
  const hh = String(d.getUTCHours()).padStart(2, '0')
  const mm = String(d.getUTCMinutes()).padStart(2, '0')
  return `${hh}:${mm}`
}

// truncate cuts `s` to `width` cells, ending in an ellipsis when cut.
export function truncate(s: string, width: number): string {
  const w = Math.max(0, Math.floor(width))
  const chars = [...s]
  if (chars.length <= w) {
    return s
  }
  if (w <= 1) {
    return chars.slice(0, w).join('')
  }
  return chars.slice(0, w - 1).join('') + ELLIPSIS
}

// oldestFirst: the report lists recent calls newest first.
export function oldestFirst(recent: readonly UsageRecentCall[]): UsageRecentCall[] {
  return [...recent].reverse()
}

export type Row = { text: string; failed: boolean }

// formatRow lays out `HH:MM  label  command  ~tokens` in `columns` cells,
// cutting the command (then the label) so the token count always shows.
export function formatRow(call: UsageRecentCall, columns: number, offsetMin = 0): Row {
  const time = formatClock(call.ts, offsetMin)
  const tokens = formatTokens(call.tokens)
  const label = call.label || 'ox'
  const command = call.command || label
  const fixed = time.length + 2 + label.length + 2 + 2 + tokens.length
  const room = columns - fixed
  let text: string
  if (room >= 1) {
    text = `${time}  ${label}  ${truncate(command, room)}  ${tokens}`
  } else {
    text = truncate(`${time}  ${label}  ${tokens}`, columns)
  }
  return { text, failed: call.failed === true }
}

function humanStatus(status: string): string {
  return status.replace(/_/g, ' ')
}

// headerLines: the title's tail (after `● ox brief`) and the summary line.
export function headerLines(report: UsageReport | null): { identity: string; summary: string } {
  if (report === null) {
    return { identity: 'loading…', summary: '' }
  }
  const s = report.session
  const parts = [s.name || s.agent_id || 'this session']
  if (s.harness) {
    parts.push(s.harness)
  }
  parts.push(s.recording ? '● recording' : '○ not recording')
  const summary = [
    `calls ${report.calls.total}${report.calls.failed > 0 ? ` (${report.calls.failed} failed)` : ''}`,
    `injected ${formatTokens(report.injected.total_tokens)} tok`,
    `surfaced ${report.surfaced.length}`,
    `used ${humanStatus(report.used.status)}`,
  ].join(' · ')
  return { identity: parts.join(' · '), summary }
}

// errorLine turns a failed refresh into one short line for the pane.
export function errorLine(failure: { exitCode?: number; stderr?: string; thrown?: unknown }): string {
  if (failure.thrown !== undefined) {
    const msg = failure.thrown instanceof Error ? failure.thrown.message : String(failure.thrown)
    if (/ENOENT|not found|no such file|cannot find|executable/i.test(msg)) {
      return 'ox not found on PATH'
    }
    if (/time(d)? ?out/i.test(msg)) {
      return 'ox brief timed out'
    }
    return `ox brief failed: ${firstLine(msg) || 'could not start'}`
  }
  const line = firstLine(failure.stderr ?? '')
  return `ox brief failed: ${line || `exit ${failure.exitCode ?? '?'}`}`
}

function firstLine(s: string): string {
  return (
    s
      .split('\n')
      .map(l => l.trim())
      .find(l => l !== '') ?? ''
  )
}

// isAtEnd: whether a scroll offset leaves the window at the tree's bottom.
export function isAtEnd(offset: number, bodyRows: number, contentRows: number): boolean {
  return offset >= Math.max(0, contentRows - bodyRows)
}

// shouldScrollToEnd: a refresh that added rows pulls the window to the end,
// unless the person scrolled up to read (follow false).
export function shouldScrollToEnd(prevRows: number | undefined, nextRows: number, follow: boolean): boolean {
  return follow && nextRows > (prevRows ?? 0)
}
