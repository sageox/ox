// The ox brief pane: what ox gave this session (what the AI coworker used,
// with links, the context ox put in front of the model, what else was
// available, whether SageOx would co-author the work, each ox call) in a
// scrollable pane beside the chat. Installed and kept up to date by ox; the data is
// `ox brief --json --recent 0`, re-read when the pane opens, after each ox
// command the AI coworker runs through Bash, and every 15 seconds while open.
//
// Every hook falls back to `next(e)`: a pane bug must never cost a turn.

import { atom, read, update } from 'claude-code'
import type { EngineInterface, Register } from 'claude-code'

import type { UsageReport } from '../types'
import {
  PANE,
  REFRESH_MS,
  RUN_TIMEOUT_MS,
  BRIEF_ARGV,
  availableRows,
  coauthorRows,
  contextRows,
  errorLine,
  formatRow,
  headerLines,
  INITIAL_STATUS,
  isAtEnd,
  isOxCommand,
  oldestFirst,
  parseReport,
  shouldScrollToEnd,
  truncate,
  usedRows,
} from './logic'

// Change a tag when its value's fields change: an atom declines what an
// earlier version stored under another tag, which outlives a reload.
// report-3: relevant.items and attribution joined the report.
const report = atom({ plugin: 'ox-cli-brief-pane', key: 'report' } as const, null as UsageReport | null, { shape: 'report-3' })
// status-2: follow now starts false (the pane opens at the top); a stored
// status-1 value, follow true, is declined so it cannot pull the pane down.
const status = atom({ plugin: 'ox-cli-brief-pane', key: 'status' } as const, INITIAL_STATUS, { shape: 'status-2' })

const TITLE = 'ox brief'
const USAGE = '/ox-brief [off|refresh]'

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
    ran = await $.process.run(BRIEF_ARGV, { timeoutMs: RUN_TIMEOUT_MS })
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
    await setError($, 'ox brief returned unreadable JSON')
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

// refresh re-reads `ox brief`; a call while one runs folds into one more run
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
      name: 'ox-brief',
      description: 'Show what SageOx gave this session in a pane',
      argumentHint: '[off|refresh]',
    })
    return next(e)
  }).catch(($, e, next) => next(e))

  on('command.run', { command: 'ox-brief' }, async ($, e) => {
    const arg = e.args.trim().toLowerCase()
    if (arg === 'off') {
      await $.ui.close({ id: PANE })
      stopTimer()
      return { text: 'ox brief pane closed.' }
    }
    if (arg === 'refresh') {
      await refresh($)
      const err = (await read($, status)).error
      return { text: err === undefined ? 'ox brief refreshed.' : err }
    }
    if (arg !== '') {
      return { text: `Usage: ${USAGE}` }
    }
    const opened = await $.ui.open({ id: PANE, title: TITLE })
    startTimer($)
    await refresh($)
    return { text: opened.isPlaced ? 'ox brief pane opened.' : `ox brief pane is open but not shown: ${opened.reason}` }
  }).catch(() => ({ text: 'ox brief pane hit an error; try /ox-brief again.' }))

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

  on('ui.close', { id: 'ox-brief' }, async ($, e, next) => {
    const done = await next(e)
    stopTimer()
    return done
  }).catch(($, e, next) => next(e))

  on('ui.scroll', { component: 'Pane', requestId: 'ox-brief' }, async ($, e, next) => {
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

  on('ui.render', { component: 'Pane', requestId: 'ox-brief' }, async ($, e) => {
    // A reload drops the timer while the pane stays up: start it again here.
    startTimer($)
    const { Box, Text, Link } = $.ui.resolve(e)
    const cols = Math.max(10, e.props.bodyColumns)
    const r = await read($, report)
    const st = await read($, status)
    const { marker, summary } = headerLines(r)
    const offsetMin = -new Date().getTimezoneOffset()
    const title = '● ox brief'

    const heading = (key: string, text: string) => (
      <Box key={key} marginTop={1}>
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
          <Box key={coauthor.key} marginTop={1}>
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
          <Box key="legend" marginTop={1}>
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
        <Text dimColor>{'─'.repeat(Math.min(cols, 48))}</Text>
        {body}
      </Box>
    )
  }).catch(($, e, next) => next(e))
}
