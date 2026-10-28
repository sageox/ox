// The desktop status line band: ox's `● ox  3 discussions · … ● rec` line,
// drawn above the prompt where the Claude desktop app's Code tab has no status
// bar. Failure prevented: desktop users never see what ox gave the session,
// or terminal users get the line twice (their status bar and the band).

import { describe, expect, mock, test } from 'claude-code/testing'

import type { Statusline } from '../types'
import {
  STATUSLINE_ARGV,
  STATUSLINE_REFRESH_MS,
  STATUSLINE_RUN_TIMEOUT_MS,
  parseStatusline,
  sameStatusline,
  statuslinePieces,
  statuslineRunInit,
} from '../hooks/logic'

const PLUGIN = 'ox-cli-brief-pane'

const BAND = {
  plugin: PLUGIN,
  component: 'AbovePrompt',
  props: {
    hasSurvey: false,
    isWorking: false,
    maxRows: 10,
    bodyColumns: 80,
    scroll: { offset: 0, bodyRows: 10 },
    view: {},
  },
} as const

const LINE = {
  show: true,
  counts: { discussions: 3, plans: 1, docs: 0, sessions: 0, murmurs: 1, used: 2 },
  recording: true,
}

function ok(stdout: string, exitCode = 0) {
  return { value: { exitCode, stdout, stderr: '', isStdoutTruncated: false, isStderrTruncated: false } }
}

// Nothing draws beneath the plugin in a test: this hook stands for the
// engine's own band, so a pass-through shows as "engine".
function engineDraws(on: Parameters<Parameters<typeof test>[1]>[1]) {
  on('ui.render', ($, e) => {
    const { Text } = $.ui.resolve(e)
    return <Text>engine</Text>
  })
}

const counts = (over: Partial<Statusline['counts']>): Statusline['counts'] => ({
  discussions: 0,
  plans: 0,
  docs: 0,
  sessions: 0,
  murmurs: 0,
  used: 0,
  ...over,
})

const text = (line: Statusline, cols: number) =>
  statuslinePieces(line, cols)
    .map(p => p.text)
    .join('')

describe('parseStatusline', () => {
  test('a full document', () => {
    expect(parseStatusline(JSON.stringify(LINE))).toEqual(LINE)
  })
  test('omitted zero counts read as 0', () => {
    expect(parseStatusline('{"show":true,"counts":{"plans":2},"recording":false}')).toEqual({
      show: true,
      counts: counts({ plans: 2 }),
      recording: false,
    })
    expect(parseStatusline('{"show":true}')).toEqual({ show: true, counts: counts({}), recording: false })
  })
  test('malformed input is null; malformed counts are 0', () => {
    expect(parseStatusline('not json')).toBeNull()
    expect(parseStatusline('[1,2]')).toBeNull()
    expect(parseStatusline('null')).toBeNull()
    expect(parseStatusline('{"show":true,"counts":{"plans":"3","docs":-2,"used":1.7}}')?.counts).toEqual(counts({ used: 1 }))
  })
  test('show must be boolean true; recording likewise', () => {
    expect(parseStatusline('{"show":"true"}')?.show).toBe(false)
    expect(parseStatusline('{"show":1}')?.show).toBe(false)
    expect(parseStatusline('{"show":false,"recording":"yes"}')).toEqual({ show: false, counts: counts({}), recording: false })
  })
  test('sameStatusline', () => {
    expect(sameStatusline(LINE, { ...LINE, counts: { ...LINE.counts } })).toBe(true)
    expect(sameStatusline(LINE, { ...LINE, counts: { ...LINE.counts, used: 3 } })).toBe(false)
    expect(sameStatusline(LINE, { ...LINE, recording: false })).toBe(false)
    expect(sameStatusline(null, null)).toBe(true)
    expect(sameStatusline(LINE, null)).toBe(false)
  })
  test('statuslineRunInit keeps the session id on a short leash', () => {
    expect(STATUSLINE_ARGV).toEqual(['ox', 'statusline', '--json'])
    expect(statuslineRunInit('s1')).toEqual({ timeoutMs: STATUSLINE_RUN_TIMEOUT_MS, env: { CLAUDE_CODE_SESSION_ID: 's1' } })
    expect(statuslineRunInit('')).toEqual({ timeoutMs: 2_000 })
  })
})

describe('statuslinePieces: same layout as the terminal status line', () => {
  test('display order, singular/plural, used last, rec at the end', () => {
    const all = { show: true, counts: counts({ discussions: 3, plans: 1, docs: 2, sessions: 1, murmurs: 4, used: 2 }), recording: true }
    expect(text(all, 0)).toBe('● ox  3 discussions · 1 plan · 2 docs · 1 session · 4 murmurs · 2 used  ● rec')
    expect(text({ ...all, counts: counts({ discussions: 1, murmurs: 1, used: 1 }), recording: false }, 0)).toBe('● ox  1 discussion · 1 murmur · 1 used')
  })
  test('styles: mark, green numbers, dim nouns and separators, red rec dot', () => {
    expect(statuslinePieces({ show: true, counts: counts({ plans: 1, used: 2 }), recording: true }, 0)).toEqual([
      { text: '● ox', style: 'mark' },
      { text: '  ', style: 'plain' },
      { text: '1', style: 'num' },
      { text: ' plan', style: 'dim' },
      { text: ' · ', style: 'dim' },
      { text: '2', style: 'num' },
      { text: ' used', style: 'dim' },
      { text: '  ', style: 'plain' },
      { text: '●', style: 'recDot' },
      { text: ' rec', style: 'recLabel' },
    ])
  })
  test('nothing counted reads "● ox" (and rec)', () => {
    expect(text({ show: true, counts: counts({}), recording: true }, 80)).toBe('● ox  ● rec')
    expect(text({ show: true, counts: counts({}), recording: false }, 80)).toBe('● ox')
  })
  test('narrow: rec is reserved first, the smallest kind count drops first, used last', () => {
    // The full line is 48 cells, 55 with rec.
    expect(text(LINE, 55)).toBe('● ox  3 discussions · 1 plan · 1 murmur · 2 used  ● rec')
    // Tie between plan (1) and murmur (1): the rightmost (murmur) goes first.
    expect(text(LINE, 54)).toBe('● ox  3 discussions · 1 plan · 2 used  ● rec')
    expect(text(LINE, 43)).toBe('● ox  3 discussions · 2 used  ● rec')
    // Every kind gone before used.
    expect(text(LINE, 34)).toBe('● ox  2 used  ● rec')
    expect(text(LINE, 18)).toBe('● ox  ● rec')
  })
  test('smallest drops first regardless of position', () => {
    const line = { show: true, counts: counts({ discussions: 1, plans: 5, murmurs: 2, used: 9 }), recording: false }
    expect(text(line, 0)).toBe('● ox  1 discussion · 5 plans · 2 murmurs · 9 used')
    expect(text(line, 40)).toBe('● ox  5 plans · 2 murmurs · 9 used')
    expect(text(line, 30)).toBe('● ox  5 plans · 9 used')
  })
  test('"● ox" is the floor; rec only when both fit', () => {
    expect(text(LINE, 11)).toBe('● ox  ● rec')
    expect(text(LINE, 10)).toBe('● ox')
    expect(text(LINE, 2)).toBe('● ox')
  })
})

test('the band draws on desktop only; the terminal keeps its own status line', async ($, on) => {
  const clock = mock.clock(on, { now: 1_000 })
  engineDraws(on)
  on('session.id', () => ({ value: 'sess-1' }))
  const runs: { argv: readonly string[]; env?: Record<string, string>; timeoutMs?: number }[] = []
  on('process.run', (_, e) => {
    runs.push({ argv: e.argv, env: e.init?.env, timeoutMs: e.init?.timeoutMs })
    return ok(JSON.stringify(LINE))
  })

  // The terminal: nothing drawn, ox never run.
  let ui = await $.ui.mount({ ...BAND, surface: 'terminal' })
  await clock.settle()
  expect(await ui.find({ type: 'Text', text: /● ox/ })).toBeUndefined()
  expect(await ui.find({ type: 'Text', text: 'engine' })).toBeDefined()
  await ui.unmount()
  await clock.advance(STATUSLINE_REFRESH_MS)
  expect(runs).toHaveLength(0)

  // The desktop: the first draw reads the line, then it is drawn.
  ui = await $.ui.mount({ ...BAND, surface: 'desktop' })
  await clock.settle()
  expect(runs[0]).toEqual({ argv: ['ox', 'statusline', '--json'], env: { CLAUDE_CODE_SESSION_ID: 'sess-1' }, timeoutMs: 2_000 })
  const row = (await ui.find({ type: 'Text', text: /● ox/ }))?.text
  expect(row).toBe('● ox  3 discussions · 1 plan · 1 murmur · 2 used  ● rec')
  const styled = (await ui.findAll({ type: 'Text' })).map(t => [t.text, t.props.color, t.props.dimColor === true])
  expect(styled).toContainEqual(['● ox', 'success', false])
  expect(styled).toContainEqual(['3', 'success', false])
  expect(styled).toContainEqual([' discussions', undefined, true])
  expect(styled).toContainEqual(['●', 'error', false])
  expect(styled).toContainEqual([' rec', undefined, true])
  await ui.unmount()
})

test('show:false, a failing ox, and a missing ox draw nothing and throw nothing', async ($, on) => {
  const clock = mock.clock(on, { now: 1_000 })
  engineDraws(on)
  let answer: () => unknown = () => ok(JSON.stringify({ ...LINE, show: false }))
  on('process.run', () => answer() as never)

  const ui = await $.ui.mount({ ...BAND, surface: 'desktop' })
  await clock.settle()
  expect(await ui.find({ type: 'Text', text: /● ox/ })).toBeUndefined()

  answer = () => ok(JSON.stringify(LINE))
  await clock.advance(STATUSLINE_REFRESH_MS)
  expect(await ui.find({ type: 'Text', text: /● ox/ })).toBeDefined()

  answer = () => ok('', 1)
  await clock.advance(STATUSLINE_REFRESH_MS)
  expect(await ui.find({ type: 'Text', text: /● ox/ })).toBeUndefined()

  answer = () => ok(JSON.stringify(LINE))
  await clock.advance(STATUSLINE_REFRESH_MS)
  answer = () => ({ deny: 'spawn ox ENOENT' })
  await clock.advance(STATUSLINE_REFRESH_MS)
  expect(await ui.find({ type: 'Text', text: /● ox/ })).toBeUndefined()
  await ui.unmount()
})

test('the engine\'s band stays, under the ox line', async ($, on) => {
  const clock = mock.clock(on, { now: 1_000 })
  engineDraws(on)
  on('process.run', () => ok(JSON.stringify(LINE)))
  const ui = await $.ui.mount({ ...BAND, surface: 'desktop' })
  await clock.settle()
  const rows = (await ui.findAll({ type: 'Text' })).map(t => t.text)
  const ox = rows.findIndex(t => /^● ox  /.test(t ?? ''))
  const engine = rows.indexOf('engine')
  expect(ox).toBeGreaterThanOrEqual(0)
  expect(engine).toBeGreaterThan(ox)
  await ui.unmount()
})

test('a survey holds the band', async ($, on) => {
  const clock = mock.clock(on, { now: 1_000 })
  engineDraws(on)
  on('process.run', () => ok(JSON.stringify(LINE)))
  let ui = await $.ui.mount({ ...BAND, surface: 'desktop' })
  await clock.settle()
  expect(await ui.find({ type: 'Text', text: /● ox/ })).toBeDefined()
  await ui.unmount()
  ui = await $.ui.mount({ ...BAND, surface: 'desktop', props: { ...BAND.props, hasSurvey: true } })
  expect(await ui.find({ type: 'Text', text: /● ox/ })).toBeUndefined()
  await ui.unmount()
})

test('an ox Bash call and the timer re-read the line; other commands do not', async ($, on) => {
  const clock = mock.clock(on, { now: 1_000 })
  engineDraws(on)
  let runs = 0
  let used = 2
  on('process.run', (_, e) => {
    if (e.argv[1] === 'statusline') runs++
    return ok(JSON.stringify({ ...LINE, counts: { ...LINE.counts, used } }))
  })
  on('ui.panes', () => ({ value: [] }))
  on('tool.call', () => ({ result: {} }))

  const ui = await $.ui.mount({ ...BAND, surface: 'desktop' })
  await clock.settle()
  expect(runs).toBe(1)

  used = 3
  await $.tool.call({ tool: 'Bash', command: 'ox murmur list' })
  await clock.settle()
  expect(runs).toBe(2)
  expect((await ui.find({ type: 'Text', text: /● ox/ }))?.text).toMatch(/3 used/)

  await $.tool.call({ tool: 'Bash', command: 'ls -la' })
  await clock.settle()
  expect(runs).toBe(2)

  // The timer re-reads and the drawing follows.
  used = 5
  await clock.advance(STATUSLINE_REFRESH_MS)
  expect(runs).toBe(3)
  expect((await ui.find({ type: 'Text', text: /● ox/ }))?.text).toMatch(/5 used/)
  await ui.unmount()
})

// Failure prevented: the next desktop session draws the previous session's
// counts, including one from a read that finished after the session ended,
// until its own first read lands.
test('a new session never draws the line of the session before it', async ($, on) => {
  const clock = mock.clock(on, { now: 1_000 })
  engineDraws(on)
  on('ui.panes', () => ({ value: [] }))
  on('tool.call', () => ({ result: {} }))
  on('session.end', () => ({ sessionId: 's1' }) as never)
  let release: (v: unknown) => void = () => undefined
  let answer: () => unknown = () => ok(JSON.stringify(LINE))
  on('process.run', () => answer() as never)

  let ui = await $.ui.mount({ ...BAND, surface: 'desktop' })
  await clock.settle()
  expect(await ui.find({ type: 'Text', text: /● ox/ })).toBeDefined()
  await ui.unmount()

  // A read in flight when the session ends, answering after it.
  answer = () => new Promise(r => (release = r))
  await $.tool.call({ tool: 'Bash', command: 'ox status' })
  await $.session.end({ reason: 'other' } as never)
  release(ok(JSON.stringify({ ...LINE, counts: { ...LINE.counts, used: 9 } })))
  await clock.settle()

  // The next session's own read has not answered yet: nothing is drawn.
  answer = () => new Promise(() => undefined)
  ui = await $.ui.mount({ ...BAND, surface: 'desktop' })
  await clock.settle()
  expect(await ui.find({ type: 'Text', text: /● ox/ })).toBeUndefined()
  await ui.unmount()
})
