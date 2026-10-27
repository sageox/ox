// Pure helpers for the ox brief pane: parsing `ox brief --json`, spotting an
// ox command in a Bash call, and laying out the pane's lines. No `$` here, so
// every function is unit-testable under `claude plugin test`.

import type {
  UsageAttribution,
  UsageContextSource,
  UsageEvidenceCounts,
  UsageRecentCall,
  UsageRelevant,
  UsageReport,
  UsageStatus,
  UsageSurfaced,
  UsageUsedItem,
} from '../types'

export const PANE = 'ox-brief'
export const PLUGIN = 'ox-cli-brief-pane'
export const REFRESH_MS = 15_000
export const RUN_TIMEOUT_MS = 8_000

// briefRunInit is how the pane runs `ox brief`. The pane runs it from the
// Claude Code host process, which carries none of the variables the AI
// coworker's shell gets, so ox cannot tell which session to report on and
// reads it as not recording while the status line says it is. Claude Code's
// own session id lets ox find the session the same way the status line does.
export function briefRunInit(sessionId: string): { timeoutMs: number; env?: Record<string, string> } {
  if (sessionId === '') return { timeoutMs: RUN_TIMEOUT_MS }
  return { timeoutMs: RUN_TIMEOUT_MS, env: { CLAUDE_CODE_SESSION_ID: sessionId } }
}
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
    surfaced_by_type: parseCounts(raw.surfaced_by_type),
    relevant: parseRelevant(raw.relevant),
    attribution: parseAttribution(raw.attribution),
  }
}

// parseCounts: a ref_type → count map, or undefined when absent (an older ox).
function parseCounts(v: unknown): Record<string, number> | undefined {
  if (!isRecord(v)) {
    return undefined
  }
  const out: Record<string, number> = {}
  for (const [k, n] of Object.entries(v)) {
    out[k] = num(n)
  }
  return out
}

function optNum(v: unknown): number | undefined {
  return typeof v === 'number' && Number.isFinite(v) ? v : undefined
}

function parseUsedItem(v: unknown): UsageUsedItem | null {
  if (!isRecord(v)) {
    return null
  }
  const ref = str(v.ref)
  const evidence = str(v.evidence)
  if (ref === undefined || ref === '' || evidence === undefined || evidence === '') {
    return null
  }
  return {
    ref,
    ref_type: str(v.ref_type),
    title: str(v.title),
    url: str(v.url) || undefined,
    evidence,
    grade: str(v.grade) ?? '',
    detail: str(v.detail),
    seq: optNum(v.seq),
    commit: str(v.commit),
    why: str(v.why),
  }
}

// parseRelevant: the used-items slice, or undefined when there is no count.
export function parseRelevant(v: unknown): UsageRelevant | undefined {
  if (!isRecord(v) || typeof v.count !== 'number') {
    return undefined
  }
  const out: UsageRelevant = { count: num(v.count), surfaced_count: num(v.surfaced_count) }
  if (isRecord(v.by_evidence)) {
    const b = v.by_evidence
    const counts: UsageEvidenceCounts = { read: num(b.read), cited: num(b.cited), reported: num(b.reported), judged: num(b.judged) }
    out.by_evidence = counts
  }
  if (Array.isArray(v.items)) {
    out.items = v.items.map(parseUsedItem).filter((i): i is UsageUsedItem => i !== null)
  }
  if (v.reported_none === true) {
    out.reported_none = true
  }
  if (isRecord(v.derived) && typeof v.derived.category === 'string') {
    out.derived = { category: v.derived.category, reason: str(v.derived.reason) }
  }
  return out
}

// parseAttribution: the co-author verdict, or undefined when absent or unreadable.
export function parseAttribution(v: unknown): UsageAttribution | undefined {
  if (!isRecord(v) || typeof v.category !== 'string' || v.category === '') {
    return undefined
  }
  return {
    would_attribute: v.would_attribute === true,
    category: v.category,
    reason: str(v.reason) || undefined,
    source: str(v.source) ?? '',
    threshold: num(v.threshold),
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

// headerLines: the title's tail (after `● ox brief`) and the summary line,
// `calls N · ~Nk tokens · used N of M · co-author <category>`.
export function headerLines(report: UsageReport | null): { marker: string; summary: string } {
  if (report === null) {
    return { marker: 'loading…', summary: '' }
  }
  const failed = report.calls.failed > 0 ? ` (${report.calls.failed} failed)` : ''
  const parts = [
    `calls ${report.calls.total}${failed}`,
    `${formatTokens(report.context.tokens)} tokens`,
    report.relevant?.reported_none === true ? 'used none' : `used ${usedCount(report)} of ${surfacedCount(report)}`,
  ]
  const a = report.attribution
  if (a !== undefined && a.category !== 'none') {
    parts.push(`co-author ${a.category}`)
  }
  return { marker: report.session.recording ? '● recording' : '○ not recording', summary: parts.join(' · ') }
}

const MURMUR = 'murmur'
const SKILL = 'skill'
const MAX_BAR = 6
export const MAX_USED = 8

// usedCount: the report's relevant count, else the surfaced items with use evidence.
export function usedCount(report: UsageReport): number {
  return report.relevant?.count ?? report.surfaced.filter(s => s.used !== undefined).length
}

// surfacedCount: how many items ox put in front of the AI coworker.
export function surfacedCount(report: UsageReport): number {
  return report.relevant?.surfaced_count ?? report.surfaced.length
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

// usedItems: what the AI coworker used, strongest evidence per ref in seq
// order. An older ox has no relevant.items: fall back to the surfaced items
// that carry use evidence.
export function usedItems(report: UsageReport): UsageUsedItem[] {
  const items = report.relevant?.items
  if (items !== undefined) {
    return items
  }
  return report.surfaced
    .filter(s => s.used !== undefined)
    .map(s => ({
      ref: s.ref,
      ref_type: s.ref_type,
      title: s.title,
      url: s.url,
      evidence: s.used?.evidence ?? '',
      grade: s.used?.grade ?? '',
      seq: s.used?.seq,
    }))
}

// evidencePhrase: the few words that say how we know the item was used.
export function evidencePhrase(item: UsageUsedItem): string {
  switch (item.detail) {
    case 'requery':
      return 're-opened'
    case 'cited_commit':
      return item.commit ? `cited in commit ${item.commit}` : 'cited in a commit'
  }
  switch (item.evidence) {
    case 'cited':
      return 'cited'
    case 'reported':
      return item.why ? `reported: ${item.why}` : 'reported'
    case 'judged':
      return 'judged'
    default:
      return 'read'
  }
}

export type UsedRow = {
  key: string
  text: string
  /** The evidence phrase; empty on the `+N more` row. */
  phrase: string
  /** Whether the phrase fits on the item's row; else it gets its own. */
  inline: boolean
  url?: string
}

// usedRows: one `kind  title` row per used item (murmurs: `kind  who  [topic]`),
// its evidence phrase beside it when it fits, then its link. At most MAX_USED,
// then `+N more`.
export function usedRows(report: UsageReport, columns: number): UsedRow[] {
  const items = usedItems(report)
  const byRef = new Map(report.surfaced.map(s => [s.ref, s] as const))
  const kindOf = (i: UsageUsedItem) => i.ref_type || byRef.get(i.ref)?.ref_type || 'ref'
  const shown = items.slice(0, MAX_USED)
  const width = Math.max(0, ...shown.map(i => [...kindOf(i)].length))
  const rows: UsedRow[] = shown.map((item, i) => {
    const s = byRef.get(item.ref)
    const kind = kindOf(item)
    const name =
      kind === MURMUR
        ? [s?.from_name || 'a teammate', s?.topic ? `[${s.topic}]` : ''].filter(Boolean).join('  ')
        : item.title || s?.title || item.ref
    const text = `${kind.padEnd(width)}  ${name}`
    const full = evidencePhrase(item)
    const inline = [...text].length + 2 + [...full].length <= columns
    return {
      key: `used:${i}`,
      text,
      phrase: inline ? full : truncate(full, columns - 2),
      inline,
      url: item.url || s?.url || undefined,
    }
  })
  if (items.length > MAX_USED) {
    rows.push({ key: 'used:more', text: `+${items.length - MAX_USED} more`, phrase: '', inline: true })
  }
  return rows
}

const FOLDED: ReadonlyArray<[string, string]> = [
  ['discussion', 'discussions'],
  ['plan', 'plans'],
  ['session', 'sessions'],
  [MURMUR, 'murmurs'],
]

export type AvailableRow = { key: string; text: string }

// availableRows: what ox surfaced that went unused, without links. Docs stay
// by name; discussions, plans, sessions and murmurs fold into one count row;
// skills into `N team skills`. surfaced[] is capped for display, so the counts
// come from surfaced_by_type less the used items of each kind (used items are
// never cut); an older ox without it is counted from the list.
export function availableRows(report: UsageReport): AvailableRow[] {
  const used = new Set(usedItems(report).map(i => i.ref))
  const isUsed = (s: UsageSurfaced) => s.used !== undefined || used.has(s.ref)
  const unused = report.surfaced.filter(s => !isUsed(s))
  const totals = report.surfaced_by_type
  const unusedOf = (kind: string) =>
    totals === undefined
      ? unused.filter(s => s.ref_type === kind).length
      : Math.max(0, (totals[kind] ?? 0) - report.surfaced.filter(s => s.ref_type === kind && isUsed(s)).length)
  const folded = new Set(FOLDED.map(([k]) => k))
  const named = unused.filter(s => s.ref_type !== SKILL && !folded.has(s.ref_type))
  const width = Math.max(0, ...named.map(s => [...(s.ref_type || 'ref')].length))
  const rows: AvailableRow[] = named.map((s, i) => ({
    key: `available:${i}`,
    text: `${(s.ref_type || 'ref').padEnd(width)}  ${s.title || s.ref}`,
  }))
  const counts = FOLDED.map(([kind, plural]) => {
    const n = unusedOf(kind)
    return n === 0 ? '' : `${n} ${n === 1 ? kind : plural}`
  }).filter(Boolean)
  if (counts.length > 0) {
    rows.push({ key: 'available:counts', text: counts.join(' · ') })
  }
  const skills = unusedOf(SKILL)
  if (skills > 0) {
    rows.push({ key: 'available:skills', text: `${skills} team skill${skills === 1 ? '' : 's'}` })
  }
  return rows
}

export type CoauthorRows = { key: string; category: string; verdict: string; reason: string }

const SOURCE_WORDS: Record<string, string> = {
  'self-reported': 'reported by the AI coworker',
  derived: 'derived from use',
}

const DERIVED_PREFIX = /^derived from\b/i

// coauthorRows: whether SageOx would be credited as a co-author, and why.
// Absent attribution draws nothing.
export function coauthorRows(report: UsageReport, columns: number): CoauthorRows | null {
  const a = report.attribution
  if (a === undefined) {
    return null
  }
  const verdict = a.would_attribute ? '· would attribute' : `· below threshold ${a.threshold}`
  // ox's derived reason already opens with "derived from …": say it once.
  const source = DERIVED_PREFIX.test(a.reason ?? '') ? '' : (SOURCE_WORDS[a.source] ?? '')
  const reason = [source, a.reason ?? ''].filter(Boolean).join(': ')
  return { key: 'coauthor', category: a.category, verdict, reason: truncate(reason, columns) }
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

// INITIAL_STATUS: the pane opens at the top. The brief reads top down (header,
// used, context, available, then calls), so following the end starts only once the
// person scrolls there (the ui.scroll hook sets follow).
export const INITIAL_STATUS: UsageStatus = { follow: false }

// shouldScrollToEnd: a refresh that added calls pulls the window to the end
// while the person follows it (scrolled to the end themselves). The first
// load (no earlier report) never moves it: that would scroll the header away.
export function shouldScrollToEnd(prevRows: number | undefined, nextRows: number, follow: boolean): boolean {
  return follow && prevRows !== undefined && nextRows > prevRows
}
