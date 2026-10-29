// The ox session report pane: what ox gave this session (what the AI coworker used,
// with links, the context ox put in front of the model, what else was
// available, whether SageOx would co-author the work, each ox call) in a
// scrollable pane beside the chat. Installed and kept up to date by ox; the data is
// `ox session report --json --recent 0`, re-read when the pane opens, after each ox
// command the AI coworker runs through Bash, and every 15 seconds while open.
//
// In the terminal it opens only on `/ox-session-report`. In the Claude desktop app it
// opens by itself, docked beside the chat, at session start and whenever the
// desktop app attaches. A person closing it by hand turns that off across
// sessions (the `autoOpen` store key); `/ox-session-report` turns it back on and
// `/ox-session-report off` turns it off.
//
// On the Claude desktop app (whose Code tab has no status bar) it also draws
// ox's status line as a band directly above the prompt:
// `● ox  3 discussions · 1 plan · 2 used  ● rec`, from `ox statusline --json`,
// re-read at each turn's start and end, after each ox command, and every 15
// seconds. Terminal sessions keep their own status line and never run it.
//
// On the desktop app, VS Code and mobile it also draws SageOx's data flow
// into the transcript: a one-line lane under each ox tool row (a Bash `ox …`
// command or a SageOx MCP tool) and under each prompt whose hooks brought
// team context in (prime, murmurs, ledger recall). Context arriving travels
// the lane as filled dots, anything sent back as hollow rings, once; then the
// lane settles to a still line that says what moved. Folded tool groups
// holding an ox call unfold so the lane shows. `/ox-session-report flow off`
// turns the lanes off, `flow still` draws them without motion and `flow on`
// brings the motion back. Terminal sessions draw none.
//
// Every hook falls back to `next(e)`: a pane bug must never cost a turn.

import { atom, read, update } from 'claude-code'
import type { EngineInterface, Register } from 'claude-code'

import type { FlowEvent, FlowState, Statusline, UsageReport } from '../types'
import { attachEvents, classifyAttachment, classifyBash, classifyMcp, EMPTY_FLOW, eventsFor, finishEvent, promptEvents, startEvent } from './flow'
import type { FlowAttachment, FlowStart } from './flow'
import {
  arrived,
  drawn,
  finishOf,
  FLOW_KEY,
  flowMode,
  flowStored,
  isMcpName,
  laneWidth,
  newBinder,
  parseFlowArg,
  settleDelay,
  startOf,
  submitted,
  withSent,
  type FlowMode,
  type RanLike,
} from './flowwire'
import {
  PANE,
  DESKTOP_COLUMNS,
  REFRESH_MS,
  BRIEF_ARGV,
  STATUSLINE_ARGV,
  STATUSLINE_REFRESH_MS,
  parseStatusline,
  sameStatusline,
  statuslinePieces,
  statuslineRunInit,
  availableRows,
  coauthorRows,
  briefRunInit,
  contextRows,
  errorLine,
  formatRow,
  headerLines,
  INITIAL_STATUS,
  isAtEnd,
  isOxCommand,
  oldestFirst,
  parseReport,
  shouldAutoOpen,
  shouldScrollToEnd,
  truncate,
  usedRows,
} from './logic'
import { caption, laneSvg, promptSvg } from './scene'
import { TOKENS } from './tokens'

// Change a tag when its value's fields change: an atom declines what an
// earlier version stored under another tag, which outlives a reload.
// report-3: relevant.items and attribution joined the report.
const report = atom({ plugin: 'ox-cli-brief-pane', key: 'report' } as const, null as UsageReport | null, { shape: 'report-3' })
// status-2: follow now starts false (the pane opens at the top); a stored
// status-1 value, follow true, is declined so it cannot pull the pane down.
const status = atom({ plugin: 'ox-cli-brief-pane', key: 'status' } as const, INITIAL_STATUS, { shape: 'status-2' })
// flow-1: FlowState, the session's flow events (hooks/flow.ts).
const flow = atom({ plugin: 'ox-cli-brief-pane', key: 'flow' } as const, EMPTY_FLOW, { shape: 'flow-1' })

const TITLE = 'ox session report'
const USAGE = '/ox-session-report [off|refresh|flow on|off|still]'
// The store key remembering whether the pane opens by itself on desktop:
// unset or true opens it, false (a person's close, `/ox-session-report off`) does not.
const AUTO_OPEN = 'autoOpen'

// Module state: the refresh timer and the run in flight. A reload drops the
// timer; the next draw of an open pane starts it again.
let timer: { cancel: () => void } | null = null
let inflight: Promise<void> | null = null
let again = false

function startTimer($: EngineInterface) {
  timer ??= $.clock.every(REFRESH_MS, () => void refresh($).catch(() => undefined))
}

function stopTimer() {
  timer?.cancel()
  timer = null
}

async function isOpen($: EngineInterface): Promise<boolean> {
  return (await $.ui.panes()).some(p => p.id === PANE)
}

async function setError($: EngineInterface, error: string | undefined) {
  await update($, status, s => ({ ...s, error }))
}

async function refreshOnce($: EngineInterface) {
  let ran
  try {
    const sessionId = await $.session.id().catch(() => '')
    ran = await $.process.run(BRIEF_ARGV, briefRunInit(sessionId))
  } catch (thrown) {
    await setError($, errorLine({ thrown }))
    return
  }
  if (ran.exitCode !== 0) {
    await setError($, errorLine({ exitCode: ran.exitCode, stderr: ran.stderr }))
    return
  }
  const next = parseReport(ran.stdout)
  if (next === null) {
    await setError($, 'ox session report returned unreadable JSON')
    return
  }
  const prev = await read($, report)
  await update($, report, () => next)
  const now = await $.clock.now()
  const st = await update($, status, s => ({ ...s, error: undefined, refreshedAt: now }))
  if (shouldScrollToEnd(prev?.calls.recent.length, next.calls.recent.length, st.follow)) {
    // `end` keeps up with the rows just added until something else moves it.
    await $.ui.scroll({ in: PANE, to: 'end' }).catch(() => undefined)
  }
}

// refresh re-reads `ox session report`; a call while one runs folds into one more run
// after it, so a burst of ox commands costs at most two. Never throws.
function refresh($: EngineInterface): Promise<void> {
  if (inflight !== null) {
    again = true
    return inflight
  }
  inflight = (async () => {
    do {
      again = false
      await refreshOnce($).catch(() => undefined)
    } while (again)
  })().finally(() => {
    inflight = null
  })
  return inflight
}

async function surfacesOf($: EngineInterface): Promise<readonly string[]> {
  try {
    return await $.session.surfaces()
  } catch {
    return []
  }
}

async function hasDesktop($: EngineInterface): Promise<boolean> {
  return (await surfacesOf($)).includes('desktop')
}

// Whether a Pane draw has kicked a first refresh: a pane placed late (an
// unasked open on a narrow window) would otherwise sit on "Loading…" until
// the timer's first tick. Once per open pane, so a failing ox does not loop;
// a close resets it.
let kicked = false

// openDesktopPane opens the pane by itself where the desktop app is attached,
// unless the person turned that off. No focus, no Escape-to-close: it sits
// beside the chat until closed. An open the surface does not place yet (too
// narrow) polls nothing: the Pane draw starts the timer once it is placed.
// Never throws.
async function openDesktopPane($: EngineInterface): Promise<void> {
  try {
    let autoOpen: unknown
    try {
      autoOpen = await $.store.get(AUTO_OPEN)
    } catch {
      autoOpen = undefined
    }
    const surfaces = await $.session.surfaces()
    if (!shouldAutoOpen({ surfaces, autoOpen })) {
      return
    }
    const opened = await $.ui.open({ id: PANE, title: TITLE, columns: DESKTOP_COLUMNS })
    if (opened.isPlaced) {
      void refresh($)
      startTimer($)
    }
  } catch {
    // A pane that can't open costs nothing; /ox-session-report still opens it.
  }
}

// setAutoOpen stores the auto-open choice and reports whether it saved.
async function setAutoOpen($: EngineInterface, on: boolean): Promise<boolean> {
  return $.store.set(AUTO_OPEN, on).then(
    () => true,
    () => false,
  )
}

// Module state for the desktop band: the last status line read (null draws
// nothing), whether a desktop band draw has been seen (until then nothing
// runs, so terminal sessions pay nothing), its timer and its run in flight.
// A reload drops all of it; the next band draw starts it again.
let statusline: Statusline | null = null
let bandSeen = false
let statuslineTimer: { cancel: () => void } | null = null
let statuslineInflight: Promise<void> | null = null
let statuslineAgain = false
// Bumped when a session ends or the desktop app leaves: a read started before
// then finishes into a session it no longer belongs to, so it is dropped.
let statuslineGen = 0

// resetBand stops the band's polling and forgets its line. The next desktop
// band draw starts it again from nothing.
function resetBand() {
  statuslineTimer?.cancel()
  statuslineTimer = null
  bandSeen = false
  statusline = null
  statuslineGen++
}

async function refreshStatuslineOnce($: EngineInterface) {
  const gen = statuslineGen
  let next: Statusline | null = null
  try {
    const sessionId = await $.session.id().catch(() => '')
    const ran = await $.process.run(STATUSLINE_ARGV, statuslineRunInit(sessionId))
    next = ran.exitCode === 0 ? parseStatusline(ran.stdout) : null
  } catch {
    // ox missing or timed out: draw nothing.
    next = null
  }
  if (gen !== statuslineGen) {
    return
  }
  if (!sameStatusline(statusline, next)) {
    statusline = next
    $.ui.invalidate('ui.render')
  }
}

// refreshStatusline re-reads `ox statusline`; like `refresh`, a call while one
// runs folds into one more run after it. Never throws; does nothing until a
// desktop band has been drawn.
function refreshStatusline($: EngineInterface): Promise<void> {
  if (!bandSeen) {
    return Promise.resolve()
  }
  if (statuslineInflight !== null) {
    statuslineAgain = true
    return statuslineInflight
  }
  statuslineInflight = (async () => {
    do {
      statuslineAgain = false
      await refreshStatuslineOnce($).catch(() => undefined)
    } while (statuslineAgain)
  })().finally(() => {
    statuslineInflight = null
  })
  return statuslineInflight
}

// Module state for the flow lanes: the setting as last read (re-read after
// FLOW_CACHE_MS; the command sets it at once), one settle timer per row
// still animating, and the binder tying hook attachments to their prompt row
// (hooks/flowwire.ts states the rule). A reload drops all of it: the events
// themselves live in the `flow` atom, and the next draw settles what it finds.
const FLOW_CACHE_MS = 5_000
let flowCache: { mode: FlowMode; at: number } | null = null
const settling = new Map<string, { cancel: () => void }>()
let binder = newBinder<FlowAttachment[]>()

async function modeOf($: EngineInterface, now: number): Promise<FlowMode> {
  if (flowCache !== null && now - flowCache.at < FLOW_CACHE_MS) {
    return flowCache.mode
  }
  let stored: unknown
  try {
    stored = await $.store.get(FLOW_KEY)
  } catch {
    stored = undefined
  }
  flowCache = { mode: flowMode(stored), at: now }
  return flowCache.mode
}

function resetFlow() {
  for (const t of settling.values()) {
    t.cancel()
  }
  settling.clear()
  binder = newBinder<FlowAttachment[]>()
  flowCache = null
}

// beginFlow opens an event for a call that moves team data; the start, or
// null when there is nothing to draw. Never throws.
async function beginFlow($: EngineInterface, id: string, start: FlowStart | null): Promise<FlowStart | null> {
  if (start === null || !id) {
    return null
  }
  try {
    const now = await $.clock.now()
    if ((await modeOf($, now)) === 'off') {
      return null
    }
    await update($, flow, s => startEvent(s, { id, start, now }))
  } catch {
    return null
  }
  // The event is stored: a redraw that fails must not strand it open.
  try {
    $.ui.invalidate('ui.render')
  } catch {
    // The next draw shows it.
  }
  return start
}

// endFlow closes it with what the call moved. Never throws.
async function endFlow($: EngineInterface, id: string, start: FlowStart | null, ran: RanLike) {
  if (start === null) {
    return
  }
  try {
    const now = await $.clock.now()
    const f = finishOf(ran, start)
    await update($, flow, s => finishEvent(s, { id, now, ...f }))
    $.ui.invalidate('ui.render')
  } catch {
    // The call already ran; a lane that cannot close changes nothing.
  }
}

// withAttachments files one hook attachment's blocks under a prompt row.
// Each attachment gets its own batch id (`att:<msg>#b<n>`), n above every
// batch still retained, so a prompt holding both SessionStart and
// UserPromptSubmit context keeps both, even after the cap evicted some.
function withAttachments(s: FlowState, msg: string, attachments: FlowAttachment[], now: number): FlowState {
  let next = 0
  for (const e of promptEvents(s, msg)) {
    const m = /#b(\d+)#/.exec(e.id)
    if (m) {
      next = Math.max(next, Number(m[1]) + 1)
    }
  }
  return attachEvents(s, { id: `att:${msg}#b${next}`, now, attachments })
}

async function recordAttachments($: EngineInterface, msg: string, attachments: FlowAttachment[], now: number) {
  await update($, flow, s => withAttachments(s, msg, attachments, now))
}

// settleLater redraws a row once its animation has played, so it settles to
// the still frame. One timer per row: a redraw while it waits adds none.
function settleLater($: EngineInterface, id: string, delay: number | null) {
  if (delay === null || settling.has(id)) {
    return
  }
  settling.set(
    id,
    $.clock.after(delay, () => {
      settling.delete(id)
      $.ui.invalidate('ui.render')
    }),
  )
}

export const register: Register = on => {
  on('session.start', async ($, e, next) => {
    await $.command.register({
      name: 'ox-session-report',
      description: 'Show what SageOx gave this session in a pane (opens by itself in the desktop app; off stops that)',
      argumentHint: '[off|refresh|flow on|off|still]',
    })
    await openDesktopPane($)
    return next(e)
  }).catch(($, e, next) => next(e))

  // The desktop app may attach after the session starts.
  on('session.attach', async ($, e, next) => {
    const done = await next(e)
    if (e.surface === 'desktop') {
      await openDesktopPane($).catch(() => undefined)
    }
    return done
  }).catch(($, e, next) => next(e))

  // The desktop app leaving: with no desktop left, stop polling `ox session report`
  // and `ox statusline` (the terminal has its own status line). The pane's
  // and the band's next draws restart them.
  on('session.detach', async ($, e, next) => {
    const done = await next(e)
    if (e.surface === 'desktop' && !(await hasDesktop($))) {
      stopTimer()
      resetBand()
    }
    return done
  }).catch(($, e, next) => next(e))

  on('command.run', { command: 'ox-session-report' }, async ($, e) => {
    const arg = e.args.trim().toLowerCase()
    if (arg === 'off') {
      await $.ui.close({ id: PANE })
      stopTimer()
      const saved = await setAutoOpen($, false)
      return { text: saved ? 'ox session report pane closed.' : 'ox session report pane closed, but the setting did not save; it may open again next session.' }
    }
    if (arg === 'refresh') {
      await refresh($)
      const err = (await read($, status)).error
      return { text: err === undefined ? 'ox session report refreshed.' : err }
    }
    if (arg === 'flow' || arg.startsWith('flow ')) {
      return { text: await flowCommand($, arg.slice('flow'.length)) }
    }
    if (arg !== '') {
      return { text: `Usage: ${USAGE} (opens by itself in the desktop app)` }
    }
    await setAutoOpen($, true)
    // On desktop, open at the width the pane opens by itself at, so each
    // open does not flip the dock between the default share and that width.
    const opened = await $.ui.open((await hasDesktop($)) ? { id: PANE, title: TITLE, columns: DESKTOP_COLUMNS } : { id: PANE, title: TITLE })
    // An open not placed yet polls nothing: the Pane draw starts the timer.
    if (opened.isPlaced) {
      startTimer($)
    }
    await refresh($)
    return { text: opened.isPlaced ? 'ox session report pane opened.' : `ox session report pane is open but not shown: ${opened.reason}` }
  }).catch(() => ({ text: 'ox session report pane hit an error; try /ox-session-report again.' }))

  // One Bash hook: it draws the flow of an ox command that moves team data
  // and refreshes the pane and band after any ox command. It never blocks or
  // alters the call.
  on('tool.call', { tool: 'Bash' }, async ($, e, next) => {
    const start = await beginFlow($, e.tool_use_id, safely(() => withSent(classifyBash(e.command), e.command)))
    let ran
    try {
      ran = await next(e)
    } catch (thrown) {
      await endFlow($, e.tool_use_id, start, { isError: true })
      throw thrown
    }
    await endFlow($, e.tool_use_id, start, ran as RanLike)
    try {
      if (isOxCommand(e.command)) {
        void refreshStatusline($)
        if (await isOpen($)) {
          void refresh($)
        }
      }
    } catch {
      // The tool already ran; a pane that can't refresh changes nothing.
    }
    return ran
  }).catch(($, e, next) => next(e))

  // SageOx MCP tools: the same lane, classified by the tool's own name.
  on('tool.call', { tool: /^mcp__/ }, async ($, e, next) => {
    const tool = (e as { tool: unknown }).tool
    const id = (e as { tool_use_id?: unknown }).tool_use_id
    if (!isMcpName(tool) || typeof id !== 'string') {
      return next(e)
    }
    const { tool: _tool, tool_use_id: _id, ...args } = e as Record<string, unknown>
    const start = await beginFlow($, id, safely(() => withSent(classifyMcp(tool), args)))
    if (start === null) {
      return next(e)
    }
    let ran
    try {
      ran = await next(e)
    } catch (thrown) {
      await endFlow($, id, start, { isError: true })
      throw thrown
    }
    await endFlow($, id, start, ran as RanLike)
    return ran
  }).catch(($, e, next) => next(e))

  // A prompt submit opens the window its hook attachments belong to.
  on('prompt.submit', async ($, e, next) => {
    if (e.isMeta !== true) {
      submitted(binder)
    }
    return next(e)
  }).catch(($, e, next) => next(e))

  // Hook-injected team context (prime, murmurs, ledger recall): filed under
  // the prompt row it came in with, now or once that row draws. The text the
  // model reads passes through untouched.
  on('prompt.attachment', async ($, e, next) => {
    const out = await next(e)
    try {
      if (e.origin.kind !== 'hook' || e.agentId !== undefined || typeof out.text !== 'string') {
        return out
      }
      const attachments = classifyAttachment(out.text, e.origin.event)
      if (attachments.length === 0) {
        return out
      }
      const now = await $.clock.now()
      if ((await modeOf($, now)) === 'off') {
        return out
      }
      const target = arrived(binder, out.text, attachments)
      if (target !== null) {
        await recordAttachments($, target, attachments, now)
        $.ui.invalidate('ui.render')
      }
    } catch {
      // Context still reaches the model; only its lane is lost.
    }
    return out
  }).catch(($, e, next) => next(e))

  on('turn.start', async ($, e, next) => {
    void refreshStatusline($)
    return next(e)
  }).catch(($, e, next) => next(e))

  on('turn.complete', async ($, e, next) => {
    const done = await next(e)
    void refreshStatusline($)
    return done
  }).catch(($, e, next) => next(e))

  on('session.end', async ($, e, next) => {
    stopTimer()
    resetBand()
    resetFlow()
    return next(e)
  }).catch(($, e, next) => next(e))

  // The status line band: desktop only (the terminal has its own status line).
  on('ui.render', { component: 'AbovePrompt' }, async ($, e, next) => {
    if (e.surface !== 'desktop' || e.props.hasSurvey) {
      return next(e)
    }
    if (!bandSeen) {
      bandSeen = true
      void refreshStatusline($)
    }
    statuslineTimer ??= $.clock.every(STATUSLINE_REFRESH_MS, () => void refreshStatusline($).catch(() => undefined))
    const line = statusline
    if (line === null || !line.show) {
      return next(e)
    }
    // Whatever else draws in the band (the engine, another plugin) stays,
    // under the ox line.
    const below = await next(e)
    const { Box, Text } = $.ui.resolve(e)
    const pieces = statuslinePieces(line, e.props.bodyColumns)
    return (
      <Box flexDirection="column">
        <Box key="ox-statusline">
          <Text wrap="truncate-end">
            {pieces.map((p, i) => {
              switch (p.style) {
                case 'mark':
                  return (
                    <Text key={i} color="success" weight="bold">
                      {p.text}
                    </Text>
                  )
                case 'num':
                  return (
                    <Text key={i} color="success">
                      {p.text}
                    </Text>
                  )
                case 'recDot':
                  return (
                    <Text key={i} color="error">
                      {p.text}
                    </Text>
                  )
                case 'dim':
                case 'recLabel':
                  return (
                    <Text key={i} dimColor>
                      {p.text}
                    </Text>
                  )
                default:
                  return p.text
              }
            })}
          </Text>
        </Box>
        {below}
      </Box>
    )
  }).catch(($, e, next) => next(e))

  // The flow lane under an ox tool row (desktop, VS Code, mobile).
  on('ui.render', { component: 'ToolUse' }, async ($, e, next) => {
    const own = await next(e)
    if (e.surface === 'terminal') {
      return own
    }
    const now = await $.clock.now()
    const mode = await modeOf($, now)
    if (mode === 'off') {
      return own
    }
    const ev = eventsFor(await read($, flow), e.requestId).find(x => x.id === e.requestId)
    if (ev === undefined) {
      return own
    }
    // Off screen, or asked for no motion: the still frame, no timer.
    const still = mode === 'still' || e.props.onScreen === null
    if (!still) {
      settleLater($, e.requestId, settleDelay([ev], now))
    }
    const width = laneWidth(e.viewport)
    const { Box, Svg } = $.ui.resolve(e)
    // Never `isInteractive`: as a plain image the desktop keeps the markup
    // whole and Chromium still runs its SMIL (see the pet mod's Spinner hook).
    return (
      <Box flexDirection="column">
        {own}
        <Box key="ox-flow" flexDirection="row">
          <Svg key="ox-flow-lane" source={laneSvg(ev, now, { width, reducedMotion: still })} alt={caption(ev)} width={width} height={TOKENS.height} />
        </Box>
      </Box>
    )
  }).catch(($, e, next) => next(e))

  // The flow strip under a prompt that carried team context in. Every
  // UserMessage draw also lets the binder claim pending attachments.
  on('ui.render', { component: 'UserMessage' }, async ($, e, next) => {
    const own = await next(e)
    const composer = e.props.origin.kind === 'composer'
    const now = await $.clock.now()
    // Drawing is pure: the claim is written from a timer, and this draw folds
    // it in locally so the strip shows at once.
    const claimed = drawn(binder, e.requestId, composer)
    if (claimed.length > 0) {
      const id = e.requestId
      $.clock.after(0, () => {
        void (async () => {
          for (const attachments of claimed) {
            await recordAttachments($, id, attachments, now)
          }
          $.ui.invalidate('ui.render')
        })().catch(() => undefined)
      })
    }
    if (e.surface === 'terminal' || !composer) {
      return own
    }
    const mode = await modeOf($, now)
    if (mode === 'off') {
      return own
    }
    let state = await read($, flow)
    for (const attachments of claimed) {
      state = withAttachments(state, e.requestId, attachments, now)
    }
    const evs: FlowEvent[] = promptEvents(state, e.requestId)
    if (evs.length === 0) {
      return own
    }
    const still = mode === 'still' || e.props.onScreen === null
    if (!still) {
      settleLater($, `att:${e.requestId}`, settleDelay(evs, now))
    }
    const width = laneWidth(e.viewport)
    const { Box, Svg } = $.ui.resolve(e)
    return (
      <Box flexDirection="column">
        {own}
        <Box key="ox-flow" flexDirection="row">
          <Svg key="ox-flow-lane" source={promptSvg(evs, now, { width, reducedMotion: still })} alt={caption(evs)} width={width} height={TOKENS.height} />
        </Box>
      </Box>
    )
  }).catch(($, e, next) => next(e))

  // A folded group holding an ox call unfolds, so its rows (and lanes) show.
  on('ui.render', { component: 'ToolGroup' }, async ($, e, next) => {
    if (e.surface === 'terminal' || e.props.isExpanded) {
      return next(e)
    }
    if (!e.props.calls.some(c => safely(() => startOf(c.tool, c.input)) !== null)) {
      return next(e)
    }
    if ((await modeOf($, await $.clock.now())) === 'off') {
      return next(e)
    }
    return next({ ...e, props: { ...e.props, isExpanded: true } } as typeof e)
  }).catch(($, e, next) => next(e))

  // A pane the person closes by hand stays closed in later desktop sessions
  // too; /ox-session-report brings it back. A plugin's close (`/ox-session-report off`) or an
  // unload is not the person's choice and stores nothing here.
  on('ui.close', { id: PANE }, async ($, e, next) => {
    const done = await next(e)
    stopTimer()
    // The next pane gets its own first-draw refresh.
    kicked = false
    // Only a close where the desktop app is attached turns off its opening
    // by itself: a terminal close is about the terminal.
    if (e.origin.kind === 'person' && (await hasDesktop($))) {
      await setAutoOpen($, false)
    }
    return done
  }).catch(($, e, next) => next(e))

  on('ui.scroll', { component: 'Pane', requestId: PANE }, async ($, e, next) => {
    const moved = await next(e)
    if (e.origin.kind === 'person' && moved.deny === undefined) {
      const follow = isAtEnd(e.offset, e.bodyRows, e.contentRows)
      const st = await read($, status)
      if (st.follow !== follow) {
        await update($, status, s => ({ ...s, follow }))
      }
    }
    return moved
  }).catch(($, e, next) => next(e))

  on('ui.render', { component: 'Pane', requestId: PANE }, async ($, e) => {
    // A reload drops the timer while the pane stays up: start it again here.
    startTimer($)
    const { Box, Text, Link } = $.ui.resolve(e)
    const cols = Math.max(10, e.props.bodyColumns)
    const r = await read($, report)
    if (r === null && !kicked) {
      kicked = true
      void refresh($)
    }
    const st = await read($, status)
    const { marker, summary } = headerLines(r)
    const offsetMin = -new Date().getTimezoneOffset()
    const title = '● ox session report'

    const heading = (key: string, text: string) => (
      <Box key={key}>
        <Text dimColor weight="bold">
          {text}
        </Text>
      </Box>
    )

    let body
    if (r === null) {
      body = st.error === undefined ? <Text dimColor>Loading…</Text> : null
    } else if (!r.trace.available) {
      body = (
        <Box flexDirection="column">
          <Text dimColor wrap="wrap">
            {r.trace.reason ?? 'No ox trace for this session.'}
          </Text>
          <Text wrap="wrap">Run ox session start to record the next session.</Text>
        </Box>
      )
    } else {
      const used = usedRows(r, cols)
      const ctx = contextRows(r)
      const available = availableRows(r)
      const coauthor = coauthorRows(r, cols)
      const recent = oldestFirst(r.calls.recent)
      const sections = []
      if (used.length > 0) {
        sections.push(heading('h:used', 'used'))
        for (const row of used) {
          sections.push(
            <Box key={row.key}>
              <Text wrap="truncate-end">
                {row.text}
                {row.phrase !== '' && row.inline && <Text dimColor>{`  ${row.phrase}`}</Text>}
              </Text>
            </Box>,
          )
          if (row.phrase !== '' && !row.inline) {
            sections.push(
              <Box key={`${row.key}:why`} paddingLeft={2}>
                <Text dimColor wrap="truncate-end">
                  {row.phrase}
                </Text>
              </Box>,
            )
          }
          if (row.url !== undefined) {
            sections.push(
              <Box key={`${row.key}:url`} paddingLeft={2}>
                <Text dimColor wrap="wrap">
                  <Link href={row.url} />
                </Text>
              </Box>,
            )
          }
        }
      }
      if (ctx.length > 0) {
        sections.push(heading('h:context', 'context'))
        for (const row of ctx) {
          sections.push(
            <Box key={`context:${row.key}`}>
              <Text wrap="truncate-end">
                {row.text}
                {row.bar !== '' && <Text color="success">{`  ${row.bar}`}</Text>}
              </Text>
            </Box>,
          )
        }
        if (r.context.estimated) {
          sections.push(
            <Text key="context:note" dimColor>
              estimates, ~4 bytes/token
            </Text>,
          )
        }
      }
      if (available.length > 0) {
        sections.push(heading('h:available', 'available'))
        for (const row of available) {
          sections.push(
            <Box key={row.key}>
              <Text wrap="wrap">{row.text}</Text>
            </Box>,
          )
        }
      }
      if (coauthor !== null) {
        sections.push(
          <Box key={coauthor.key}>
            <Text wrap="truncate-end">
              <Text weight="bold">co-author</Text>
              {`  ${coauthor.category} `}
              <Text dimColor>{coauthor.verdict}</Text>
            </Text>
          </Box>,
        )
        if (coauthor.reason !== '') {
          sections.push(
            <Box key={`${coauthor.key}:reason`}>
              <Text dimColor wrap="truncate-end">
                {coauthor.reason}
              </Text>
            </Box>,
          )
        }
      }
      sections.push(heading('h:calls', 'calls'))
      if (recent.length === 0) {
        sections.push(
          <Text key="calls:none" dimColor>
            No ox calls yet.
          </Text>,
        )
      }
      recent.forEach((call, i) => {
        const row = formatRow(call, cols, offsetMin)
        sections.push(
          <Box key={`row:${i}`}>
            <Text color={row.failed ? 'error' : undefined} wrap="truncate-end">
              {row.text}
            </Text>
          </Box>,
        )
      })
      if (used.length > 0) {
        sections.push(
          <Box key="legend">
            <Text dimColor wrap="wrap">
              used = evidence from the transcript, a commit, or the AI coworker's report
            </Text>
          </Box>,
        )
      }
      body = <Box flexDirection="column">{sections}</Box>
    }

    return (
      <Box flexDirection="column">
        <Text wrap="truncate-end">
          <Text color="success" weight="bold">
            {title}
          </Text>
          {r !== null && r.session.recording ? (
            <Text>
              {'  '}
              <Text color="error">●</Text>
              {' recording'}
            </Text>
          ) : (
            <Text dimColor>{truncate(`  ${marker}`, Math.max(0, cols - title.length))}</Text>
          )}
        </Text>
        {summary !== '' && (
          <Text dimColor wrap="truncate-end">
            {truncate(summary, cols)}
          </Text>
        )}
        {st.error !== undefined && (
          <Text color="error" dimColor wrap="truncate-end">
            {truncate(st.error, cols)}
          </Text>
        )}
        {body}
      </Box>
    )
  }).catch(($, e, next) => next(e))
}

// safely runs a pure classifier; a bug in it draws no lane and costs nothing.
function safely<T>(fn: () => T | null): T | null {
  try {
    return fn()
  } catch {
    return null
  }
}

// flowCommand: `/ox-session-report flow [on|off|still]`.
async function flowCommand($: EngineInterface, rest: string): Promise<string> {
  const mode = parseFlowArg(rest)
  const now = await $.clock.now()
  if (mode === null) {
    return `Usage: ${USAGE}`
  }
  if (mode === undefined) {
    flowCache = null
    const cur = await modeOf($, now)
    return `ox flow lanes are ${cur === 'still' ? 'on, drawn still' : cur}. ${USAGE}`
  }
  const saved = await $.store.set(FLOW_KEY, flowStored(mode)).then(
    () => true,
    () => false,
  )
  // Unsaved, it holds for this session: the cache never goes stale.
  flowCache = { mode, at: saved ? now : Infinity }
  $.ui.invalidate('ui.render')
  const said =
    mode === 'off'
      ? 'ox flow lanes off.'
      : mode === 'still'
        ? 'ox flow lanes drawn still, without motion.'
        : 'ox flow lanes on: SageOx data flow draws under ox calls and prompts (desktop, VS Code, mobile).'
  return saved ? said : `${said} The setting did not save; it lasts until the session ends.`
}
