// The ox usage pane: what ox did this session (each ox call, the context it
// put in front of the model, what it surfaced) in a scrollable pane beside
// the chat. Installed and kept up to date by ox; the data is
// `ox usage --json --recent 0`, re-read when the pane opens, after each ox
// command the AI coworker runs through Bash, and every 15 seconds while open.
//
// Every hook falls back to `next(e)`: a pane bug must never cost a turn.

import { atom, read, update } from 'claude-code'
import type { EngineInterface, Register } from 'claude-code'

import type { UsageReport, UsageStatus } from '../types'
import {
  PANE,
  REFRESH_MS,
  RUN_TIMEOUT_MS,
  USAGE_ARGV,
  errorLine,
  formatRow,
  headerLines,
  isAtEnd,
  isOxCommand,
  oldestFirst,
  parseReport,
  shouldScrollToEnd,
  truncate,
} from './logic'

// Change a tag when its value's fields change: an atom declines what an
// earlier version stored under another tag, which outlives a reload.
const report = atom({ plugin: 'ox-cli-usage-pane', key: 'report' } as const, null as UsageReport | null, { shape: 'report-1' })
const status = atom({ plugin: 'ox-cli-usage-pane', key: 'status' } as const, { follow: true } as UsageStatus, { shape: 'status-1' })

const TITLE = 'ox usage'
const USAGE = '/ox-usage [off|refresh]'

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
    ran = await $.process.run(USAGE_ARGV, { timeoutMs: RUN_TIMEOUT_MS })
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
    await setError($, 'ox usage returned unreadable JSON')
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

// refresh re-reads `ox usage`; a call while one runs folds into one more run
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

export const register: Register = on => {
  on('session.start', async ($, e, next) => {
    await $.command.register({
      name: 'ox-usage',
      description: 'Show what ox did this session in a pane',
      argumentHint: '[off|refresh]',
    })
    return next(e)
  }).catch(($, e, next) => next(e))

  on('command.run', { command: 'ox-usage' }, async ($, e) => {
    const arg = e.args.trim().toLowerCase()
    if (arg === 'off') {
      await $.ui.close({ id: PANE })
      stopTimer()
      return { text: 'ox usage pane closed.' }
    }
    if (arg === 'refresh') {
      await refresh($)
      const err = (await read($, status)).error
      return { text: err === undefined ? 'ox usage refreshed.' : err }
    }
    if (arg !== '') {
      return { text: `Usage: ${USAGE}` }
    }
    const opened = await $.ui.open({ id: PANE, title: TITLE })
    startTimer($)
    await refresh($)
    return { text: opened.isPlaced ? 'ox usage pane opened.' : `ox usage pane is open but not shown: ${opened.reason}` }
  }).catch(() => ({ text: 'ox usage pane hit an error; try /ox-usage again.' }))

  on('tool.call', { tool: 'Bash' }, async ($, e, next) => {
    const ran = await next(e)
    try {
      if (isOxCommand(e.command) && (await isOpen($))) {
        void refresh($)
      }
    } catch {
      // The tool already ran; a pane that can't refresh changes nothing.
    }
    return ran
  }).catch(($, e, next) => next(e))

  on('ui.close', { id: 'ox-usage' }, async ($, e, next) => {
    const done = await next(e)
    stopTimer()
    return done
  }).catch(($, e, next) => next(e))

  on('ui.scroll', { component: 'Pane', requestId: 'ox-usage' }, async ($, e, next) => {
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

  on('ui.render', { component: 'Pane', requestId: 'ox-usage' }, async ($, e) => {
    // A reload drops the timer while the pane stays up: start it again here.
    startTimer($)
    const { Box, Text } = $.ui.resolve(e)
    const cols = Math.max(10, e.props.bodyColumns)
    const r = await read($, report)
    const st = await read($, status)
    const { identity, summary } = headerLines(r)
    const offsetMin = -new Date().getTimezoneOffset()
    const title = '● ox usage'
    const recent = r === null ? [] : oldestFirst(r.calls.recent)

    let body
    if (r === null) {
      body = st.error === undefined ? <Text dimColor>Loading…</Text> : null
    } else if (!r.trace.available) {
      body = (
        <Box flexDirection="column">
          <Text dimColor wrap="wrap">
            {r.trace.reason ?? 'No usage trace for this session.'}
          </Text>
          <Text wrap="wrap">Run ox session start to record the next session.</Text>
        </Box>
      )
    } else if (recent.length === 0) {
      body = <Text dimColor>No ox calls yet.</Text>
    } else {
      body = (
        <Box flexDirection="column">
          {recent.map((call, i) => {
            const row = formatRow(call, cols, offsetMin)
            return (
              <Box key={`row:${i}`}>
                <Text color={row.failed ? 'error' : undefined} wrap="truncate-end">
                  {row.text}
                </Text>
              </Box>
            )
          })}
        </Box>
      )
    }

    return (
      <Box flexDirection="column">
        <Text wrap="truncate-end">
          <Text color="success" weight="bold">
            {title}
          </Text>
          <Text dimColor>{truncate(`  ${identity}`, Math.max(0, cols - title.length))}</Text>
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
        <Text dimColor>{'─'.repeat(Math.min(cols, 48))}</Text>
        {body}
      </Box>
    )
  }).catch(($, e, next) => next(e))
}
