// Pure helpers for the ox brief pane: parsing `ox brief --json`, spotting an
// ox command in a Bash call, and laying out the pane's lines. No `$` here, so
// every function is unit-testable under `claude plugin test`.

import type { UsageContextSource, UsageRecentCall, UsageReport, UsageSurfaced } from '../types'

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
  const context = isRecord(raw.context) ? raw.context : {}
  const relevant = isRecord(raw.relevant) && typeof raw.relevant.count === 'number' ? raw.relevant : undefined
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
  const bySource: UsageContextSource[] = (Array.isArray(context.by_source) ? context.by_source : [])
    .filter(isRecord)
    .map(s => ({ source: str(s.source) ?? '', tokens: num(s.tokens), count: typeof s.count === 'number' ? s.count : undefined }))
    .filter(s => s.source !== '')
  const surfaced: UsageSurfaced[] = (Array.isArray(raw.surfaced) ? raw.surfaced : []).filter(isRecord).map(item => {
    const used = isRecord(item.used) ? item.used : undefined
    return {
      ref: str(item.ref) ?? '',
      ref_type: str(item.ref_type) ?? '',
      from: str(item.from),
      from_name: str(item.from_name),
      topic: str(item.topic),
      via: str(item.via),
      title: str(item.title),
      url: str(item.url),
      seq: typeof item.seq === 'number' ? item.seq : undefined,
      used:
        used === undefined
          ? undefined
          : { evidence: str(used.evidence) ?? '', seq: typeof used.seq === 'number' ? used.seq : undefined, grade: str(used.grade) },
    }
  })
  return {
    view: 'session',
    session: { recording: session.recording === true },
    trace: {
      available: trace.available !== false,
      source: str(trace.source),
      reason: str(trace.reason),
    },
    calls: { total: num(calls.total), failed: num(calls.failed), recent },
    context: { tokens: num(context.tokens), estimated: context.estimated === true, by_source: bySource },
    surfaced,
    relevant: relevant === undefined ? undefined : { count: num(relevant.count), surfaced_count: num(relevant.surfaced_count) },
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

// headerLines: the title's tail (after `● ox brief`) and the summary line.
export function headerLines(report: UsageReport | null): { marker: string; summary: string } {
  if (report === null) {
    return { marker: 'loading…', summary: '' }
  }
  const failed = report.calls.failed > 0 ? ` (${report.calls.failed} failed)` : ''
  const summary = [
    `calls ${report.calls.total}${failed}`,
    `${formatTokens(report.context.tokens)} tokens`,
    `read ${readItems(report).length}`,
    `useful ${usefulCount(report)}`,
  ].join(' · ')
  return { marker: report.session.recording ? '● recording' : '○ not recording', summary }
}

const MURMUR = 'murmur'
const SKILL = 'skill'
const MAX_BAR = 6

// readItems: what ox put in front of the AI coworker that a person can open,
// i.e. every surfaced item but murmurs.
export function readItems(report: UsageReport): UsageSurfaced[] {
  return report.surfaced.filter(s => s.ref_type !== MURMUR)
}

// usefulCount: the report's relevant count, else the surfaced items with use evidence.
export function usefulCount(report: UsageReport): number {
  return report.relevant?.count ?? report.surfaced.filter(s => s.used !== undefined).length
}

// bar: up to MAX_BAR cells of ▇, proportional to the largest value; at least
// one cell for any non-zero value.
export function bar(n: number, max: number): string {
  if (max <= 0 || n <= 0) {
    return ''
  }
  return '▇'.repeat(Math.max(1, Math.round((n * MAX_BAR) / max)))
}

export type ContextRow = { key: string; text: string; bar: string }

// contextRows: one `source  ~Nk tokens` row per metered source, padded so the
// token counts line up.
export function contextRows(report: UsageReport): ContextRow[] {
  const sources = report.context.by_source
  const max = Math.max(0, ...sources.map(s => s.tokens))
  const width = Math.max(0, ...sources.map(s => [...s.source].length))
  return sources.map(s => ({
    key: s.source,
    text: `${s.source.padEnd(width)}  ${formatTokens(s.tokens)} tokens`,
    bar: bar(s.tokens, max),
  }))
}

export type ReadRow = { key: string; text: string; used: boolean; url?: string }

// readRows: one `kind  title` row per read item, skills folded into one row.
export function readRows(report: UsageReport): ReadRow[] {
  const items = readItems(report)
  const skills = items.filter(s => s.ref_type === SKILL)
  const rest = items.filter(s => s.ref_type !== SKILL)
  const width = Math.max(0, ...rest.map(s => [...(s.ref_type || 'ref')].length))
  const rows: ReadRow[] = rest.map((s, i) => ({
    key: `read:${i}`,
    text: `${(s.ref_type || 'ref').padEnd(width)}  ${s.title || s.ref}`,
    used: s.used !== undefined,
    url: s.url || undefined,
  }))
  if (skills.length > 0) {
    rows.push({
      key: 'read:skills',
      text: `${skills.length} team skill${skills.length === 1 ? '' : 's'}`,
      used: skills.some(s => s.used !== undefined),
    })
  }
  return rows
}

// murmurRows: only the murmurs the AI coworker used; the rest is noise here.
export function murmurRows(report: UsageReport): ReadRow[] {
  return report.surfaced
    .filter(s => s.ref_type === MURMUR && s.used !== undefined)
    .map((s, i) => ({
      key: `murmur:${i}`,
      text: [s.from_name || s.from || 'a teammate', s.topic ? `[${s.topic}]` : ''].filter(Boolean).join('  '),
      used: true,
    }))
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
