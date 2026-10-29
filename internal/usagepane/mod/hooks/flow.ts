// Pure classification and state for the flow lanes: which ox calls and which
// hook-injected blocks moved team data into or out of this session. The hooks
// module feeds observations in; the scene module draws the FlowState. No `$`
// here, so every function is unit-testable under `claude plugin test`.
//
// The taxonomy is a port of internal/oxactivity/classify.go (cliRules,
// mcpTools): labels keep its wording, past tense and lower case. Unlike
// oxactivity, a call that moves no team data (doctor, status, heartbeat, the
// session report) is not a flow event at all: a lane must mean data moved.

import type { FlowDirection, FlowEvent, FlowLane, FlowState } from '../types'
import { utf8Bytes } from './logic'

/** What a call is, decided when it starts. `command` holds fixed words only, never arguments. */
export type FlowStart = {
  lane: FlowLane
  direction: FlowDirection
  label: string
  step: string
  command: string
  /** For an `out` call: tokens of the payload the AI coworker sent, measured when it starts. */
  sent?: number
}

/** One block of hook-injected team context; always direction `in`. */
export type FlowAttachment = {
  lane: FlowLane
  label: string
  step: string
  tokens: number
  items?: number
  itemsNoun?: string
}

export type FlowItems = { items: number; itemsNoun: string }

// --- Bash: finding the ox words in a shell line -----------------------------

// splitCommands tokenizes a shell line into simple commands, like
// oxactivity's splitCommands: quotes and escapes are honored; `&&`, `||`,
// `;`, `&` and newlines separate commands; a pipe tail is dropped; and
// redirections go with their targets. It also opens a new command at `(`,
// `$(` and backticks (even inside double quotes), so `X=$(ox query "a")`
// and `echo "r: `ox query a`"` are found. A heredoc body is skipped: its
// lines are data, not commands. A classifier, not a shell.
function splitCommands(line: string): string[][] {
  const cmds: string[][] = []
  let cur: string[] = []
  let word = ''
  let inWord = false
  let piped = false
  let skipNext = false
  // 'dq' while inside double quotes, 'sub' for each open `$(` or `(`, 'bt'
  // for an open backtick substitution.
  const stack: ('dq' | 'sub' | 'bt')[] = []
  // Heredoc delimiters whose bodies start after the next newline.
  const heredocs: { delim: string; tabs: boolean }[] = []

  const emit = () => {
    if (!inWord) {
      return
    }
    const w = word
    word = ''
    inWord = false
    if (skipNext) {
      skipNext = false
    } else if (!piped) {
      cur.push(w)
    }
  }
  const end = () => {
    emit()
    if (cur.length > 0) {
      cmds.push(cur)
    }
    cur = []
    piped = false
    skipNext = false
  }
  const redirect = (i: number): number => {
    // A pending all-digit word is a file descriptor ("2>"), not an argument.
    if (inWord && /^\d+$/.test(word)) {
      word = ''
      inWord = false
    }
    emit()
    let j = i + 1
    if (j < line.length && line[j] === line[i]) {
      j++
    }
    if (j < line.length && line[j] === '&') {
      j++
      while (j < line.length && line[j] >= '0' && line[j] <= '9') {
        j++
      }
      return j - 1
    }
    skipNext = true
    return j - 1
  }
  // heredoc: `<<` (or `<<-`) at i; records the delimiter word and returns
  // the index of its last character. The body is skipped at the next newline.
  const heredoc = (i: number): number => {
    emit()
    let j = i + 2
    const tabs = line[j] === '-'
    if (tabs) {
      j++
    }
    while (j < line.length && (line[j] === ' ' || line[j] === '\t')) {
      j++
    }
    let delim = ''
    while (j < line.length && !' \t\n;&|<>()'.includes(line[j])) {
      const c = line[j]
      if (c === "'" || c === '"') {
        for (j++; j < line.length && line[j] !== c; j++) {
          delim += line[j]
        }
      } else if (c === '\\' && j + 1 < line.length) {
        delim += line[++j]
      } else {
        delim += c
      }
      j++
    }
    if (delim !== '') {
      heredocs.push({ delim, tabs })
    }
    return j - 1
  }
  // skipBodies: from the newline at i, skips every pending heredoc body and
  // returns the index of the last body character consumed.
  const skipBodies = (i: number): number => {
    let pos = i + 1
    for (const h of heredocs) {
      while (pos < line.length) {
        const nl = line.indexOf('\n', pos)
        const stop = nl < 0 ? line.length : nl
        const text = line.slice(pos, stop)
        pos = stop + 1
        if ((h.tabs ? text.replace(/^\t+/, '') : text) === h.delim) {
          break
        }
      }
    }
    heredocs.length = 0
    return Math.min(pos, line.length) - 1
  }

  for (let i = 0; i < line.length; i++) {
    const c = line[i]
    if (stack[stack.length - 1] === 'dq') {
      if (c === '"') {
        stack.pop()
      } else if (c === '\\' && i + 1 < line.length && '"\\$`'.includes(line[i + 1])) {
        word += line[++i]
      } else if (c === '$' && line[i + 1] === '(') {
        end()
        stack.push('sub')
        i++
      } else if (c === '`') {
        end()
        stack.push('bt')
      } else {
        word += c
      }
      continue
    }
    switch (c) {
      case "'":
        inWord = true
        for (i++; i < line.length && line[i] !== "'"; i++) {
          word += line[i]
        }
        break
      case '"':
        inWord = true
        stack.push('dq')
        break
      case '\\':
        if (i + 1 < line.length) {
          i++
          if (line[i] !== '\n') {
            inWord = true
            word += line[i]
          }
        }
        break
      case '$':
        if (line[i + 1] === '(') {
          end()
          stack.push('sub')
          i++
        } else {
          inWord = true
          word += c
        }
        break
      case '(':
        end()
        stack.push('sub')
        break
      case ')':
        end()
        if (stack[stack.length - 1] === 'sub') {
          stack.pop()
        }
        if (stack[stack.length - 1] === 'dq') {
          inWord = true
        }
        break
      case ';':
        end()
        break
      case '\n':
        end()
        if (heredocs.length > 0) {
          i = skipBodies(i)
        }
        break
      case '`':
        end()
        if (stack[stack.length - 1] === 'bt') {
          stack.pop()
          if (stack[stack.length - 1] === 'dq') {
            inWord = true
          }
        } else {
          stack.push('bt')
        }
        break
      case '&':
        if (line[i + 1] === '>') {
          emit()
          i = redirect(i + 1)
          break
        }
        if (line[i + 1] === '&') {
          i++
        }
        end()
        break
      case '|':
        if (line[i + 1] === '|') {
          i++
          end()
          break
        }
        emit()
        piped = true
        break
      case '<':
        i = line[i + 1] === '<' && line[i + 2] !== '<' ? heredoc(i) : redirect(i)
        break
      case '>':
        i = redirect(i)
        break
      case ' ':
      case '\t':
      case '\r':
        emit()
        break
      default:
        inWord = true
        word += c
    }
  }
  end()
  return cmds
}

const ASSIGNMENT = /^[A-Za-z_][A-Za-z0-9_]*=/
// Words that may lead a simple command without being the command itself.
const PREFIX_WORDS = new Set(['env', 'do', 'then', 'else', 'time', 'exec', 'command', 'nohup', '!', '{'])

// oxInvocations: the words after `ox` for every simple command in the line
// that runs ox (behind env assignments or a path such as ./ox), in order.
function oxInvocations(command: string): string[][] {
  const out: string[][] = []
  for (const seg of splitCommands(command)) {
    let k = 0
    while (k < seg.length && (ASSIGNMENT.test(seg[k]) || PREFIX_WORDS.has(seg[k]))) {
      k++
    }
    const head = seg[k]
    if (head !== undefined && head.slice(head.lastIndexOf('/') + 1) === 'ox') {
      out.push(seg.slice(k + 1))
    }
  }
  return out
}

// --- Bash: ox subcommands ---------------------------------------------------

type Rule = {
  words: string[]
  lane: FlowLane
  direction: FlowDirection
  label: string
  step: string
}

function rule(words: string, lane: FlowLane, direction: FlowDirection, label: string, step: string): Rule {
  return { words: words.split(' '), lane, direction, label, step }
}

const READ_SESSIONS = ['ledger', 'in', 'read sessions', 'reading sessions'] as const
const PULL_PLAN = ['ledger', 'in', 'pulled plan', 'pulling plan'] as const
const READ_DISCUSSION = ['team-context', 'in', 'read discussion', 'reading discussion'] as const
const SEARCH_CODE = ['code', 'in', 'searched code index', 'searching code'] as const

// RULES: the first whose words prefix the command's positional words wins.
// Anything ox that matches no rule (and none of the cases in classifyOxArgs)
// moved no team data and is not a flow event.
const RULES: Rule[] = [
  rule('agent prime', 'team-context', 'in', 'loaded team context', 'loading team context'),
  rule('agent team-ctx', 'team-context', 'in', 'loaded team context', 'loading team context'),
  rule('agent session stop', 'ledger', 'out', 'stopped recording', 'uploading session'),
  rule('agent session context-trace', 'ledger', 'out', 'reported context use', 'recording context use'),
  rule('agent context used', 'ledger', 'out', 'reported context use', 'recording context use'),
  rule('agent session score', 'ledger', 'out', 'scored session', 'scoring session'),

  rule('murmur list', 'murmurs', 'in', 'checked murmurs', 'reading murmurs'),

  rule('plan save', 'ledger', 'out', 'saved plan', 'saving plan'),
  rule('plan render', 'ledger', 'out', 'rendered plan', 'rendering plan'),
  rule('plan view', ...PULL_PLAN),
  rule('plan show', ...PULL_PLAN),
  rule('plan get', ...PULL_PLAN),
  rule('plan list', 'ledger', 'in', 'listed plans', 'listing plans'),
  rule('plan enrich', 'team-context', 'in', 'enriched plan', 'enriching plan'),

  rule('code search', ...SEARCH_CODE),
  rule('code defs', ...SEARCH_CODE),
  rule('code callers', ...SEARCH_CODE),
  rule('code callees', ...SEARCH_CODE),
  rule('code refs', ...SEARCH_CODE),
  rule('code log', ...SEARCH_CODE),
  rule('code insights', 'code', 'in', 'checked code hotspots', 'checking code hotspots'),
  rule('code prs', 'code', 'in', 'checked pull requests', 'checking pull requests'),
  rule('code activity', 'code', 'in', 'checked repo activity', 'checking repo activity'),

  rule('session list', ...READ_SESSIONS),
  rule('session view', ...READ_SESSIONS),
  rule('session show', ...READ_SESSIONS),
  rule('session score', 'ledger', 'out', 'scored session', 'scoring session'),

  rule('conversation show', ...READ_DISCUSSION),
  rule('conversation search', ...READ_DISCUSSION),
  rule('conversation transcript', ...READ_DISCUSSION),
  rule('conversation topics', ...READ_DISCUSSION),
  rule('conversation topic', ...READ_DISCUSSION),
  rule('conversation walkthrough', ...READ_DISCUSSION),
  rule('conversation list', 'team-context', 'in', 'listed discussions', 'listing discussions'),

  rule('decision enrich', 'team-context', 'in', 'checked decisions', 'checking decisions'),
  rule('coworker load', 'team-context', 'in', 'loaded coworker', 'loading coworker'),
  rule('kb', 'team-context', 'in', 'read knowledge base', 'reading knowledge base'),
  rule('recap', 'ledger', 'in', 'read recap', 'reading recap'),
]

// Top-level commands an AI coworker may address as `ox agent <id> <cmd>`.
const TOP_LEVEL_UNDER_AGENT = new Set(['murmur', 'query', 'plan', 'code', 'conversation', 'decision', 'kb', 'recap'])
const AGENT_ID = /^Ox[A-Za-z0-9]{4}$/
const MURMUR_SUBCOMMANDS = new Set(['list', 'status', 'delete', 'pause', 'resume', 'help'])
const MURMUR_POST_FLAG = /^--(topic|scope|importance)(=|$)/

function classifyOxArgs(args: string[]): FlowStart | null {
  let words = args.filter(a => !a.startsWith('-'))
  if (words.length === 0) {
    return null
  }
  if (words[0] === 'agent') {
    let rest = words.slice(1)
    // `ox agent <id> <cmd>` addresses one AI coworker; classify <cmd>.
    if (rest.length > 0 && AGENT_ID.test(rest[0])) {
      rest = rest.slice(1)
    }
    words = rest.length > 0 && TOP_LEVEL_UNDER_AGENT.has(rest[0]) ? rest : ['agent', ...rest]
  }

  if (words[0] === 'query') {
    const local = args.some(a => a === '--local' || a === '--local=true')
    return local
      ? { lane: 'ledger', direction: 'in', label: 'searched the ledger', step: 'searching the ledger', command: 'query' }
      : { lane: 'cloud', direction: 'in', label: 'searched team memory', step: 'searching team discussions', command: 'query' }
  }
  if (words[0] === 'murmur' && !(words.length > 1 && MURMUR_SUBCOMMANDS.has(words[1]))) {
    // A post: a positional body or a post-only flag. The body is never read.
    if (words.length > 1 || args.some(a => MURMUR_POST_FLAG.test(a))) {
      return { lane: 'murmurs', direction: 'out', label: 'posted murmur', step: 'posting murmur', command: 'murmur' }
    }
    return null
  }
  for (const r of RULES) {
    if (r.words.every((w, i) => words[i] === w)) {
      return { lane: r.lane, direction: r.direction, label: r.label, step: r.step, command: r.words.join(' ') }
    }
  }
  return null
}

/**
 * classifyBash: the flow a Bash command starts, or null when it runs no ox
 * command that moves team data. A line running several ox commands is
 * classified by the first one that does.
 */
export function classifyBash(command: string): FlowStart | null {
  if (typeof command !== 'string' || command === '') {
    return null
  }
  for (const args of oxInvocations(command)) {
    const start = classifyOxArgs(args)
    if (start) {
      return start
    }
  }
  return null
}

// --- SageOx MCP tools -------------------------------------------------------

type McpEntry = { lane: FlowLane; direction: FlowDirection; label: string; step: string }

function mcpIn(label: string, step: string, lane: FlowLane = 'cloud'): McpEntry {
  return { lane, direction: 'in', label, step }
}

function mcpOut(label: string, step: string, lane: FlowLane = 'cloud'): McpEntry {
  return { lane, direction: 'out', label, step }
}

// MCP_TOOLS: keyed by the tool name after the last `__`. Every SageOx MCP tool
// talks to the SageOx API, so its lane is `cloud` except murmurs.
const MCP_TOOLS: Record<string, McpEntry> = {
  Prime: mcpIn('loaded team context', 'loading team context'),
  Search: mcpIn('searched team memory', 'searching team discussions'),
  ListMurmurs: mcpIn('checked murmurs', 'reading murmurs', 'murmurs'),
  AttentionTray: mcpIn('checked attention tray', 'checking attention tray'),
  SessionContext: mcpIn('read session context', 'reading session context'),
  GetPlan: mcpIn('pulled plan', 'pulling plan'),
  Conversation: mcpIn('read discussion', 'reading discussion'),
  ConversationDigest: mcpIn('read discussion', 'reading discussion'),
  ConversationTranscript: mcpIn('read discussion', 'reading discussion'),
  ExpertiseMap: mcpIn('read expertise map', 'reading expertise map'),
  OrgMemory: mcpIn('read org memory', 'reading org memory'),
  Provenance: mcpIn('traced provenance', 'tracing provenance'),
  ListBubbles: mcpIn('listed knowledge bubbles', 'listing knowledge bubbles'),
  ListBubbleFiles: mcpIn('listed bubble files', 'listing bubble files'),
  ReadBubbleFile: mcpIn('read bubble file', 'reading bubble file'),
  ListRecordings: mcpIn('listed recordings', 'listing recordings'),
  RecordingChapters: mcpIn('read recording', 'reading recording'),
  RecordingDecisions: mcpIn('read recording', 'reading recording'),
  RecordingInsights: mcpIn('read recording', 'reading recording'),
  ListTeams: mcpIn('listed teams', 'listing teams'),
  ListPlanTargets: mcpIn('listed plan targets', 'listing plan targets'),
  GetTeamSkill: mcpIn('loaded team skill', 'loading team skill'),
  GetStarted: mcpIn('read getting started', 'reading getting started'),
  Help: mcpIn('read SageOx help', 'reading SageOx help'),
  // Named like a write, but it returns a context bundle.
  EnrichPlan: mcpIn('enriched plan', 'enriching plan'),

  PublishMurmur: mcpOut('posted murmur', 'posting murmur', 'murmurs'),
  SavePlan: mcpOut('saved plan', 'saving plan'),
  RevisePlan: mcpOut('saved plan', 'saving plan'),
  SaveSessionSummary: mcpOut('saved session summary', 'saving session summary'),
  CaptureTurns: mcpOut('uploaded session', 'uploading session'),
  KeepDecision: mcpOut('kept decision', 'keeping decision'),
  UndoKeptDecision: mcpOut('undid kept decision', 'undoing kept decision'),
  ReportBulletinUsefulness: mcpOut('reported bulletin usefulness', 'reporting bulletin usefulness'),
}

// SAGEOX_SERVER: the server segments a SageOx MCP tool comes under: a name
// carrying `sageox` (`claude_ai_sageox`), or the opaque connector id a
// claude.ai connector gets. A named server that is not SageOx's (`github`)
// never matches, whatever its tools are called.
const SAGEOX_SERVER = /sageox|^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i

/**
 * classifyMcp: the flow an MCP tool call starts, matched on the tool name
 * after the last `__`, for a server segment SAGEOX_SERVER accepts. The
 * server's identity is not visible to a hook, so an opaque connector id
 * offering a tool of the same name is taken for SageOx's.
 */
export function classifyMcp(tool: string): FlowStart | null {
  if (typeof tool !== 'string' || !tool.startsWith('mcp__')) {
    return null
  }
  const rest = tool.slice('mcp__'.length)
  const cut = rest.lastIndexOf('__')
  if (cut <= 0) {
    return null
  }
  const name = rest.slice(cut + 2)
  if (!SAGEOX_SERVER.test(rest.slice(0, cut))) {
    return null
  }
  const entry = Object.prototype.hasOwnProperty.call(MCP_TOOLS, name) ? MCP_TOOLS[name] : undefined
  return entry ? { ...entry, command: `mcp ${name}` } : null
}

// --- Finishing: tokens and items --------------------------------------------

/** byteTokens: bytes/4 rounded; nonempty text is at least one token. */
export function byteTokens(s: string): number {
  const bytes = utf8Bytes(s)
  return bytes === 0 ? 0 : Math.max(1, Math.round(bytes / 4))
}

const BUDGET_TOTAL = /<context-budget\b[^>]*\stotal="(\d+)"/

/**
 * finishTokens: tokens a call's result put in front of the model, bytes/4
 * rounded; a prime document's own `<context-budget total>` wins when present.
 * 0 for undefined.
 */
export function finishTokens(text: string | undefined): number {
  if (typeof text !== 'string') {
    return 0
  }
  if (text.includes('<ox-prime')) {
    const m = BUDGET_TOTAL.exec(text)
    if (m) {
      return Number(m[1])
    }
  }
  return byteTokens(text)
}

// itemsNoun: what a counted result holds, by the call that returned it.
function itemsNoun(start: FlowStart): string | undefined {
  const c = start.command
  if (c === 'query') {
    return start.lane === 'ledger' ? 'ledger match' : 'discussion'
  }
  if (c === 'mcp Search' || c === 'conversation search' || c === 'conversation list') {
    return 'discussion'
  }
  if (c.startsWith('session ')) {
    return 'session'
  }
  if (c.startsWith('code ')) {
    return 'match'
  }
  if (c === 'murmur list' || c === 'mcp ListMurmurs') {
    return 'murmur'
  }
  return undefined
}

/** plural: `1 murmur`, `3 murmurs`, `2 ledger matches`. */
export function plural(noun: string, n: number): string {
  if (n === 1) {
    return noun
  }
  return /(ch|sh|s|x)$/.test(noun) ? `${noun}es` : `${noun}s`
}

function parseJSONLoose(text: string): unknown {
  const t = text.trim()
  if (!t.startsWith('{') && !t.startsWith('[')) {
    return undefined
  }
  try {
    return JSON.parse(t)
  } catch {
    return undefined
  }
}

function asRecord(v: unknown): Record<string, unknown> | undefined {
  return v !== null && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>) : undefined
}

// countResult: the item count a JSON result states, preferring the list it
// returned over a `total` that may count beyond it.
function countResult(v: unknown): number | undefined {
  if (Array.isArray(v)) {
    return v.length
  }
  const o = asRecord(v)
  if (!o) {
    return undefined
  }
  for (const key of ['results', 'sessions', 'murmurs']) {
    if (Array.isArray(o[key])) {
      return (o[key] as unknown[]).length
    }
  }
  const tc = asRecord(o.team_context)
  if (tc && Array.isArray(tc.results)) {
    return tc.results.length
  }
  if (typeof o.total === 'number' && Number.isFinite(o.total) && o.total >= 0) {
    return Math.floor(o.total)
  }
  return undefined
}

/**
 * finishItems: how many items a call's JSON result moved, with the noun the
 * call's kind gives them; undefined when the result is not JSON, states no
 * count, or the call has no known noun.
 */
export function finishItems(text: string | undefined, start: FlowStart): FlowItems | undefined {
  if (typeof text !== 'string' || !start) {
    return undefined
  }
  const noun = itemsNoun(start)
  if (!noun) {
    return undefined
  }
  const n = countResult(parseJSONLoose(text))
  return n === undefined ? undefined : { items: n, itemsNoun: plural(noun, n) }
}

// --- Hook-injected context --------------------------------------------------

const ATTACHMENT_EVENTS = new Set(['SessionStart', 'UserPromptSubmit', 'PreCompact'])
const REMINDER_OPEN = '<system-reminder>'
const REMINDER_CLOSE = '</system-reminder>'

type Found = { pos: number; att: FlowAttachment }

function endOf(text: string, from: number, ...markers: string[]): number {
  let end = text.length
  for (const m of markers) {
    const i = text.indexOf(m, from)
    if (i >= 0) {
      end = Math.min(end, m === REMINDER_CLOSE || m === '</ox-prime>' ? i + m.length : i)
    }
  }
  return end
}

function primeBlocks(text: string, found: Found[]): [number, number][] {
  const spans: [number, number][] = []
  for (let i = text.indexOf('<ox-prime'); i >= 0; i = text.indexOf('<ox-prime', i + 1)) {
    const next = text[i + '<ox-prime'.length]
    if (next !== ' ' && next !== '>' && next !== '\n') {
      continue
    }
    const end = endOf(text, i, '</ox-prime>')
    const block = text.slice(i, end)
    const budget = BUDGET_TOTAL.exec(block)
    const compact = /^<ox-prime\b[^>]*\bmode="compact"/.test(block)
    found.push({
      pos: i,
      att: {
        lane: 'team-context',
        label: compact ? 'reloaded team context' : 'loaded team context',
        step: compact ? 'reloading team context' : 'loading team context',
        tokens: budget ? Number(budget[1]) : byteTokens(block),
      },
    })
    spans.push([i, end])
    i = end - 1
  }
  return spans
}

function inside(spans: [number, number][], pos: number): boolean {
  return spans.some(([a, b]) => pos >= a && pos < b)
}

/**
 * classifyAttachment: the team-context blocks one hook attachment brought in,
 * in text order: an `<ox-prime>` document (team-context), a murmur block
 * (murmurs, its `<entry>` elements counted, never read) and `[ox-recall]`
 * ledger matches (ledger). Local nudges (`<agent-tasks>`, `[ox] …`) and
 * hooks other than SessionStart, UserPromptSubmit and PreCompact yield none.
 */
export function classifyAttachment(text: string, event: string): FlowAttachment[] {
  if (typeof text !== 'string' || !ATTACHMENT_EVENTS.has(event)) {
    return []
  }
  const found: Found[] = []
  const prime = primeBlocks(text, found)

  for (let i = text.indexOf('<murmur-context>'); i >= 0; i = text.indexOf('<murmur-context>', i + 1)) {
    if (inside(prime, i)) {
      continue
    }
    const open = text.lastIndexOf(REMINDER_OPEN, i)
    const start = open >= 0 && !inside(prime, open) ? open : i
    const end = endOf(text, i, REMINDER_CLOSE)
    const block = text.slice(start, end)
    const n = (block.match(/<entry[\s>]/g) ?? []).length
    found.push({
      pos: start,
      att: {
        lane: 'murmurs',
        label: 'received murmurs',
        step: 'reading murmurs',
        tokens: byteTokens(block),
        ...(n > 0 ? { items: n, itemsNoun: plural('murmur', n) } : {}),
      },
    })
    i = end - 1
  }

  for (let i = text.indexOf('[ox-recall'); i >= 0; i = text.indexOf('[ox-recall', i + 1)) {
    if (inside(prime, i)) {
      continue
    }
    const end = endOf(text, i + 1, REMINDER_CLOSE, '[ox-recall')
    const block = text.slice(i, end)
    const n = block.split('\n').filter(l => l.startsWith('- ')).length
    if (n === 0) {
      continue
    }
    found.push({
      pos: i,
      att: {
        lane: 'ledger',
        label: 'recalled ledger matches',
        step: 'searching the ledger',
        tokens: byteTokens(block),
        items: n,
        itemsNoun: plural('ledger match', n),
      },
    })
  }

  return found.sort((a, b) => a.pos - b.pos).map(f => f.att)
}

// --- State ------------------------------------------------------------------

export const MAX_EVENTS = 60

export const EMPTY_FLOW: FlowState = { byId: {}, order: [] }

// capped: drops the oldest events beyond MAX_EVENTS.
function capped(byId: Record<string, FlowEvent>, order: string[]): FlowState {
  if (order.length <= MAX_EVENTS) {
    return { byId, order }
  }
  const drop = order.slice(0, order.length - MAX_EVENTS)
  const kept = { ...byId }
  for (const id of drop) {
    delete kept[id]
  }
  return { byId: kept, order: order.slice(order.length - MAX_EVENTS) }
}

/** startEvent: opens an event for a call; a known id is a no-op. */
export function startEvent(state: FlowState, { id, start, now }: { id: string; start: FlowStart; now: number }): FlowState {
  if (!id || Object.prototype.hasOwnProperty.call(state.byId, id)) {
    return state
  }
  const ev: FlowEvent = {
    id,
    lane: start.lane,
    direction: start.direction,
    label: start.label,
    step: start.step,
    tokens: 0,
    startedAt: now,
  }
  return capped({ ...state.byId, [id]: ev }, [...state.order, id])
}

export type FlowFinish = {
  id: string
  now: number
  tokens: number
  items?: number
  itemsNoun?: string
  failed?: boolean
  interrupted?: boolean
}

/**
 * finishEvent: closes an open event. Finishing an unknown or already
 * finished id returns the same state object.
 */
export function finishEvent(state: FlowState, f: FlowFinish): FlowState {
  const ev = Object.prototype.hasOwnProperty.call(state.byId, f.id) ? state.byId[f.id] : undefined
  if (!ev || ev.endedAt !== undefined) {
    return state
  }
  const done: FlowEvent = {
    ...ev,
    endedAt: f.now,
    tokens: Number.isFinite(f.tokens) ? Math.max(0, Math.round(f.tokens)) : 0,
  }
  if (typeof f.items === 'number' && f.items >= 0 && f.itemsNoun) {
    done.items = f.items
    done.itemsNoun = f.itemsNoun
  }
  if (f.failed) {
    done.failed = true
  }
  if (f.interrupted) {
    done.interrupted = true
  }
  return { byId: { ...state.byId, [f.id]: done }, order: state.order }
}

/** attachmentId: the event id of a prompt attachment's nth block. */
export function attachmentId(id: string, index: number): string {
  return `${id}#${index}`
}

/**
 * attachEvents: records a prompt attachment's blocks as finished `in` events,
 * ids `<id>#0`, `<id>#1`, … (id is `att:<message id>`). Recording the same id
 * twice is a no-op, so call it once per attachment message.
 */
export function attachEvents(
  state: FlowState,
  { id, now, attachments }: { id: string; now: number; attachments: readonly FlowAttachment[] },
): FlowState {
  if (!id || attachments.length === 0 || Object.prototype.hasOwnProperty.call(state.byId, attachmentId(id, 0))) {
    return state
  }
  const byId = { ...state.byId }
  const order = [...state.order]
  attachments.forEach((a, i) => {
    const eid = attachmentId(id, i)
    const ev: FlowEvent = {
      id: eid,
      lane: a.lane,
      direction: 'in',
      label: a.label,
      step: a.step,
      tokens: a.tokens,
      startedAt: now,
      endedAt: now,
    }
    if (a.items !== undefined && a.itemsNoun) {
      ev.items = a.items
      ev.itemsNoun = a.itemsNoun
    }
    byId[eid] = ev
    order.push(eid)
  })
  return capped(byId, order)
}

/** eventsFor: the events under one id (a tool_use_id, or an attachment id and its `#n` blocks), oldest first. */
export function eventsFor(state: FlowState, id: string): FlowEvent[] {
  return state.order.filter(e => e === id || e.startsWith(`${id}#`)).map(e => state.byId[e])
}

/** promptEvents: the attachment events of one prompt, by message id (`att:` prefix optional). */
export function promptEvents(state: FlowState, id: string): FlowEvent[] {
  return eventsFor(state, id.startsWith('att:') ? id : `att:${id}`)
}
