import { describe, expect, mock, test } from 'claude-code/testing'

import {
  bar,
  contextRows,
  formatClock,
  formatRow,
  formatTokens,
  headerLines,
  INITIAL_STATUS,
  isAtEnd,
  isOxCommand,
  murmurRows,
  parseReport,
  readRows,
  shouldScrollToEnd,
  truncate,
} from '../hooks/logic'

const PLUGIN = 'ox-cli-brief-pane'

const PANE = {
  plugin: PLUGIN,
  component: 'Pane',
  requestId: 'ox-brief',
  props: {
    title: 'ox brief',
    isFocused: false,
    bodyColumns: 80,
    placement: 'dock',
    scroll: { offset: 0, bodyRows: 20 },
    view: {},
  },
} as const

const COMMAND = {
  command: 'ox-brief',
  origin: { kind: 'composer' },
  presentation: { isFullscreen: true, columns: 160 },
} as const

// Three ox calls, newest first as `ox brief --json` lists them.
const REPORT = {
  view: 'session',
  session: { name: 'brief-pane', agent_id: 'Oxa1b2', harness: 'claude-code', recording: true, turns: 4 },
  trace: { available: true, source: 'session' },
  calls: {
    total: 3,
    failed: 1,
    by_command: [],
    recent: [
      { label: 'murmur', command: 'ox murmur list', seq: 3, tokens: 80, failed: true, ts: '2026-10-24T10:07:00Z' },
      { label: 'query', command: 'ox query "how do we version releases"', seq: 2, tokens: 1234, ts: '2026-10-24T10:05:00Z' },
      { label: 'prime', command: 'ox agent prime', seq: 1, tokens: 15000, ts: '2026-10-24T10:01:00Z' },
    ],
  },
  context: {
    tokens: 16314,
    estimated: true,
    by_source: [
      { source: 'prime', tokens: 15000, count: 1 },
      { source: 'query', tokens: 1234, count: 1 },
      { source: 'murmurs', tokens: 80, count: 1 },
    ],
    unmetered: [],
  },
  surfaced: [
    {
      ref: 'disc_9',
      ref_type: 'discussion',
      via: 'query',
      title: 'Release versioning sync',
      url: 'https://sageox.ai/c/disc_9',
      seq: 2,
      used: { evidence: 'cited', seq: 3 },
    },
    { ref: 'plan_4', ref_type: 'plan', via: 'prime', title: 'Brief pane redesign', url: 'https://sageox.ai/p/plan_4' },
    { ref: 'murmur_1', ref_type: 'murmur', from: 'Oxa1', from_name: 'Avery', topic: 'wip', via: 'murmur', used: { evidence: 'read' } },
    { ref: 'murmur_2', ref_type: 'murmur', from: 'Oxb2', from_name: 'Sam', topic: 'release', via: 'murmur' },
    { ref: 'ox-cli-plan', ref_type: 'skill', via: 'prime' },
    { ref: 'ox-cli-recap', ref_type: 'skill', via: 'prime' },
  ],
  relevant: { count: 2, surfaced_count: 6 },
  used: { self_reported: false },
  display: '',
  guidance: '',
}

const EMPTY = { ...REPORT, calls: { total: 0, failed: 0, by_command: [], recent: [] } }

function ok(stdout: string) {
  return { value: { exitCode: 0, stdout, stderr: '', isStdoutTruncated: false, isStderrTruncated: false } }
}

describe('parseReport: used items and attribution', () => {
  const RELEVANT = {
    count: 3,
    surfaced_count: 6,
    by_evidence: { read: 1, cited: 1, reported: 1, judged: 0 },
    items: [
      { ref: 'disc_9', ref_type: 'discussion', title: 'Release versioning sync', url: 'https://sageox.ai/c/disc_9', evidence: 'cited', grade: 'provable', detail: 'cited_commit', seq: 3, commit: 'abc1234' },
      { ref: 'plan_4', evidence: 'reported', grade: 'reported', detail: 'reported', why: 'followed the rollout order' },
      { ref: 'murmur_1', ref_type: 'murmur', evidence: 'read', grade: 'provable', detail: 'read', seq: 4 },
    ],
    derived: { category: 'moderate', reason: 'two cited items' },
  }
  const ATTRIBUTION = { would_attribute: true, category: 'moderate', reason: 'cited in a commit', source: 'derived', threshold: 2 }

  test('reads items, evidence counts, derived and attribution', () => {
    const r = parseReport(JSON.stringify({ ...REPORT, relevant: RELEVANT, attribution: ATTRIBUTION }))!
    expect(r.relevant?.by_evidence).toEqual({ read: 1, cited: 1, reported: 1, judged: 0 })
    expect(r.relevant?.items?.map(i => [i.ref, i.evidence, i.detail, i.commit, i.why, i.url])).toEqual([
      ['disc_9', 'cited', 'cited_commit', 'abc1234', undefined, 'https://sageox.ai/c/disc_9'],
      ['plan_4', 'reported', 'reported', undefined, 'followed the rollout order', undefined],
      ['murmur_1', 'read', 'read', undefined, undefined, undefined],
    ])
    expect(r.relevant?.derived).toEqual({ category: 'moderate', reason: 'two cited items' })
    expect(r.relevant?.reported_none).toBeUndefined()
    expect(r.attribution).toEqual(ATTRIBUTION)
  })
  test('reported_none is kept only when true', () => {
    expect(parseReport(JSON.stringify({ ...REPORT, relevant: { count: 0, surfaced_count: 6, reported_none: true } }))!.relevant?.reported_none).toBe(true)
    expect(parseReport(JSON.stringify({ ...REPORT, relevant: { count: 0, surfaced_count: 6, reported_none: 'yes' } }))!.relevant?.reported_none).toBeUndefined()
  })
  test('malformed fields are tolerated, not fatal', () => {
    const r = parseReport(
      JSON.stringify({
        ...REPORT,
        relevant: { count: 1, surfaced_count: 'x', by_evidence: 'nope', items: [null, 7, { ref: 'x' }, { evidence: 'read' }, { ref: 'ok', evidence: 'judged', url: '' }], derived: { reason: 'r' } },
        attribution: { would_attribute: 'yes', category: 7 },
      }),
    )!
    expect(r.relevant).toEqual({ count: 1, surfaced_count: 0, items: [{ ref: 'ok', ref_type: undefined, title: undefined, url: undefined, evidence: 'judged', grade: '', detail: undefined, seq: undefined, commit: undefined, why: undefined }] })
    expect(r.attribution).toBeUndefined()
    expect(parseReport(JSON.stringify({ ...REPORT, relevant: { surfaced_count: 3 }, attribution: 'moderate' }))!.relevant).toBeUndefined()
  })
  test('an older report without items or attribution still parses', () => {
    const r = parseReport(JSON.stringify(REPORT))!
    expect(r.relevant).toEqual({ count: 2, surfaced_count: 6 })
    expect(r.attribution).toBeUndefined()
  })
})

describe('isOxCommand', () => {
  const cases: Array<[string, string, boolean]> = [
    ['plain', 'ox murmur list', true],
    ['bare', 'ox', true],
    ['cd prefix', 'cd repo && ox query "x"', true],
    ['env prefix', 'OX_DEBUG=1 ox plan enrich --json', true],
    ['path', './ox status', true],
    ['abs path', '/usr/local/bin/ox doctor', true],
    ['after pipe', 'echo x | ox session import', true],
    ['subshell', '(ox status)', true],
    ['not ox', 'ls -la', false],
    ['oxford', 'oxford query', false],
    ['in a string', 'echo "ox query"', false],
    ['flag value', 'grep -r ox .', false],
  ]
  for (const [name, command, want] of cases) {
    test(name, () => {
      expect(isOxCommand(command)).toBe(want)
    })
  }
})

describe('formatting', () => {
  test('tokens', () => {
    expect(formatTokens(80)).toBe('~80')
    expect(formatTokens(1234)).toBe('~1.2k')
    expect(formatTokens(1000)).toBe('~1k')
    expect(formatTokens(15000)).toBe('~15k')
  })
  test('clock', () => {
    expect(formatClock('2026-10-24T10:07:00Z')).toBe('10:07')
    expect(formatClock('2026-10-24T10:07:00Z', 120)).toBe('12:07')
    expect(formatClock('nope')).toBe('--:--')
    expect(formatClock(undefined)).toBe('--:--')
  })
  test('a row cuts the command, never the tokens', () => {
    const row = formatRow({ label: 'query', command: 'ox query "a very long question"', tokens: 1234, ts: '2026-10-24T10:05:00Z' }, 30)
    expect(row.text).toBe('10:05  query  ox query…  ~1.2k')
    expect([...row.text].length).toBe(30)
  })
  test('truncate', () => {
    expect(truncate('abcdef', 4)).toBe('abc…')
    expect(truncate('abc', 4)).toBe('abc')
  })
  test('header', () => {
    const h = headerLines(parseReport(JSON.stringify(REPORT)))
    expect(h.marker).toBe('● recording')
    expect(h.summary).toBe('calls 3 (1 failed) · ~16k tokens · read 4 · useful 2')
  })
  test('header without relevant counts used items, and says not recording', () => {
    const { relevant: _, ...rest } = REPORT
    const h = headerLines(parseReport(JSON.stringify({ ...rest, session: { recording: false } })))
    expect(h.marker).toBe('○ not recording')
    expect(h.summary).toBe('calls 3 (1 failed) · ~16k tokens · read 4 · useful 2')
  })
  test('an old trace without context or surfaced still parses', () => {
    const h = headerLines(parseReport(JSON.stringify({ view: 'session', session: { recording: true }, calls: { total: 0, failed: 0 } })))
    expect(h.summary).toBe('calls 0 · ~0 tokens · read 0 · useful 0')
  })
  test('context bars scale to the largest source', () => {
    const r = parseReport(JSON.stringify(REPORT))!
    expect(contextRows(r).map(row => [row.text, row.bar])).toEqual([
      ['prime    ~15k tokens', '▇▇▇▇▇▇'],
      ['query    ~1.2k tokens', '▇'],
      ['murmurs  ~80 tokens', '▇'],
    ])
    expect(bar(0, 10)).toBe('')
    expect(bar(5, 10)).toBe('▇▇▇')
  })
  test('read rows group skills; murmur rows keep only used ones', () => {
    const r = parseReport(JSON.stringify(REPORT))!
    expect(readRows(r)).toEqual([
      { key: 'read:0', text: 'discussion  Release versioning sync', used: true, url: 'https://sageox.ai/c/disc_9' },
      { key: 'read:1', text: 'plan        Brief pane redesign', used: false, url: 'https://sageox.ai/p/plan_4' },
      { key: 'read:skills', text: '2 team skills', used: false },
    ])
    expect(murmurRows(r)).toEqual([{ key: 'murmur:0', text: 'Avery  [wip]', used: true }])
  })
  test('parse', () => {
    expect(parseReport('not json')).toBeNull()
    expect(parseReport('{"view":"me"}')).toBeNull()
    expect(parseReport(JSON.stringify(REPORT))?.calls.recent).toHaveLength(3)
  })
  test('scroll end', () => {
    expect(isAtEnd(10, 10, 20)).toBe(true)
    expect(isAtEnd(4, 10, 20)).toBe(false)
    expect(isAtEnd(0, 10, 5)).toBe(true)
  })
})

test('/ox-brief opens the pane and lists each ox call, oldest first', async ($, on) => {
  mock.clock(on, { now: 1_000 })
  let runs = 0
  on('process.run', () => {
    runs++
    return ok(JSON.stringify(REPORT))
  })
  on('ui.open', () => ({ value: { isPlaced: true } }))
  on('ui.panes', () => ({ value: [{ id: 'ox-brief', title: 'ox brief', isShown: true, isFocused: false, isPlaced: true }] }))

  const ran = await $.command.run({ ...COMMAND, args: '' } as never)
  expect(ran.text).toBe('ox brief pane opened.')
  expect(runs).toBe(1)

  for (const surface of ['terminal', 'desktop'] as const) {
    const ui = await $.ui.mount({ ...PANE, surface })
    const titleRow = (await ui.find({ type: 'Text', text: /● ox brief/ }))?.text
    expect(titleRow).toBe('● ox brief  ● recording')
    expect(titleRow).not.toMatch(/brief-pane|claude-code|Oxa1b2/)
    expect((await ui.find({ type: 'Text', text: /^calls \d/ }))?.text).toBe('calls 3 (1 failed) · ~16k tokens · read 4 · useful 2')
    expect(await ui.find({ type: 'Text', text: /injected|not yet reported/ })).toBeUndefined()
    expect(await ui.find({ type: 'Text', text: /recording/ })).toBeDefined()

    for (const h of ['context', 'read', 'murmurs', 'calls']) {
      expect((await ui.find({ key: `h:${h}` }))?.text).toBe(h)
    }
    expect((await ui.find({ key: 'context:prime' }))?.text).toBe('prime    ~15k tokens  ▇▇▇▇▇▇')
    expect(await ui.find({ type: 'Text', text: 'estimates, ~4 bytes/token' })).toBeDefined()
    expect((await ui.find({ key: 'read:0' }))?.text).toBe('discussion  Release versioning sync ✓')
    expect((await ui.find({ key: 'read:0:url' }))?.text).toBe('https://sageox.ai/c/disc_9')
    expect((await ui.find({ type: 'Link', text: 'https://sageox.ai/c/disc_9' }))?.props.href).toBe('https://sageox.ai/c/disc_9')
    expect((await ui.find({ key: 'read:1' }))?.text).toBe('plan        Brief pane redesign')
    expect((await ui.find({ key: 'read:1:url' }))?.text).toBe('https://sageox.ai/p/plan_4')
    expect((await ui.find({ key: 'read:skills' }))?.text).toBe('2 team skills')
    expect((await ui.find({ key: 'murmur:0' }))?.text).toBe('Avery  [wip] ✓')
    expect(await ui.find({ type: 'Text', text: /Sam/ })).toBeUndefined()
    expect(await ui.find({ type: 'Text', text: '✓ used by the AI coworker' })).toBeDefined()

    const rows = [await ui.find({ key: 'row:0' }), await ui.find({ key: 'row:1' }), await ui.find({ key: 'row:2' })]
    expect(rows.map(r => r?.text.replace(/^\d\d:\d\d/, 'HH:MM'))).toEqual([
      'HH:MM  prime  ox agent prime  ~15k',
      'HH:MM  query  ox query "how do we version releases"  ~1.2k',
      'HH:MM  murmur  ox murmur list  ~80',
    ])
    const failed = await ui.find({ type: 'Text', text: /murmur list/ })
    expect(failed?.props.color).toBe('error')
    await ui.unmount()
  }
})

test('an ox Bash call refreshes the open pane once; other commands do not', async ($, on) => {
  const clock = mock.clock(on, { now: 1_000 })
  let runs = 0
  on('process.run', () => {
    runs++
    return ok(JSON.stringify(REPORT))
  })
  on('ui.open', () => ({ value: { isPlaced: true } }))
  on('ui.panes', () => ({ value: [{ id: 'ox-brief', title: 'ox brief', isShown: true, isFocused: false, isPlaced: true }] }))
  on('tool.call', () => ({ result: {} }))

  await $.command.run({ ...COMMAND, args: '' } as never)
  expect(runs).toBe(1)

  await $.tool.call({ tool: 'Bash', command: 'ox murmur list' })
  await clock.settle()
  expect(runs).toBe(2)

  await $.tool.call({ tool: 'Bash', command: 'ls -la' })
  await clock.settle()
  expect(runs).toBe(2)

  // The timer re-reads while the pane is open.
  await clock.advance(15_000)
  expect(runs).toBe(3)
})

test('a Bash ox call with the pane closed runs nothing', async ($, on) => {
  mock.clock(on, { now: 1_000 })
  let runs = 0
  on('process.run', () => {
    runs++
    return ok(JSON.stringify(REPORT))
  })
  on('ui.panes', () => ({ value: [] }))
  on('tool.call', () => ({ result: {} }))

  await $.tool.call({ tool: 'Bash', command: 'ox status' })
  expect(runs).toBe(0)
})

test('a failing ox shows one error line and throws nothing', async ($, on) => {
  mock.clock(on, { now: 1_000 })
  on('process.run', () => ({
    value: { exitCode: 2, stdout: '', stderr: 'Error: not logged in\nrun ox login', isStdoutTruncated: false, isStderrTruncated: false },
  }))
  on('ui.open', () => ({ value: { isPlaced: true } }))

  const ran = await $.command.run({ ...COMMAND, args: '' } as never)
  expect(ran.text).toBe('ox brief pane opened.')
  const ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
  expect((await ui.find({ type: 'Text', text: /failed/ }))?.text).toBe('ox brief failed: Error: not logged in')
  await ui.unmount()

  expect((await $.command.run({ ...COMMAND, args: 'refresh' } as never)).text).toBe('ox brief failed: Error: not logged in')
})

test('ox missing from PATH says so', async ($, on) => {
  mock.clock(on, { now: 1_000 })
  // A call on `$` the engine cannot make rejects; a deny beneath is how a test makes it.
  on('process.run', () => ({ deny: 'spawn ox ENOENT' }))
  on('ui.open', () => ({ value: { isPlaced: true } }))

  await $.command.run({ ...COMMAND, args: '' } as never)
  const ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
  expect((await ui.find({ type: 'Text', text: /PATH/ }))?.text).toBe('ox not found on PATH')
  await ui.unmount()
})

test('an empty session and a missing trace read plainly', async ($, on) => {
  mock.clock(on, { now: 1_000 })
  let stdout = JSON.stringify(EMPTY)
  on('process.run', () => ok(stdout))
  on('ui.open', () => ({ value: { isPlaced: true } }))

  await $.command.run({ ...COMMAND, args: '' } as never)
  let ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
  expect(await ui.find({ type: 'Text', text: 'No ox calls yet.' })).toBeDefined()
  await ui.unmount()

  // Nothing used: no murmurs section, no legend, and unused murmurs never show.
  stdout = JSON.stringify({
    ...EMPTY,
    surfaced: EMPTY.surfaced.map(({ used: _, ...item }) => item),
    relevant: undefined,
  })
  await $.command.run({ ...COMMAND, args: 'refresh' } as never)
  ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
  expect(await ui.find({ key: 'h:murmurs' })).toBeUndefined()
  expect(await ui.find({ type: 'Text', text: /used by the AI coworker/ })).toBeUndefined()
  expect(await ui.find({ type: 'Text', text: /✓/ })).toBeUndefined()
  expect((await ui.find({ type: 'Text', text: /^calls \d/ }))?.text).toBe('calls 0 · ~16k tokens · read 4 · useful 0')
  await ui.unmount()

  stdout = JSON.stringify({ ...EMPTY, trace: { available: false, source: 'none', reason: 'no usage trace found for this session' } })
  await $.command.run({ ...COMMAND, args: 'refresh' } as never)
  ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
  expect((await ui.find({ type: 'Text', text: /no usage trace/ }))?.text).toBe('no usage trace found for this session')
  expect(await ui.find({ type: 'Text', text: 'Run ox session start to record the next session.' })).toBeDefined()
  await ui.unmount()
})

test('/ox-brief off closes the pane', async ($, on) => {
  mock.clock(on, { now: 1_000 })
  const closed: string[] = []
  on('ui.close', (_, e) => {
    closed.push(e.id)
    return { value: undefined }
  })

  const ran = await $.command.run({ ...COMMAND, args: 'off' } as never)
  expect(ran.text).toBe('ox brief pane closed.')
  expect(closed).toEqual(['ox-brief'])
})

test('/ox-brief registers at session start without opening the pane', async ($, on) => {
  const registered: string[] = []
  let opened = 0
  on('command.register', (_, e) => {
    registered.push(e.name)
    return { value: { command: e.name } }
  })
  on('ui.open', () => {
    opened++
    return { value: { isPlaced: true } }
  })
  on('session.start', (_, e) => ({ cwd: e.cwd }))

  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  expect(registered).toEqual(['ox-brief'])
  expect(opened).toBe(0)
})

// The brief reads top down (header, context, read, then calls), so the pane
// opens at the top and keeps the end in view only once the person has
// scrolled there. Jumping to the end on open scrolled the header away.
describe('following the end', () => {
  test('the pane opens at the top: a first load never jumps to the end', () => {
    expect(INITIAL_STATUS.follow).toBe(false)
    expect(shouldScrollToEnd(undefined, 3, true)).toBe(false)
    expect(shouldScrollToEnd(undefined, 3, INITIAL_STATUS.follow)).toBe(false)
  })
  test('new rows pull the window down once the person follows the end', () => {
    expect(shouldScrollToEnd(3, 4, true)).toBe(true)
  })
  test('no new rows, or the person reading above the end, leaves the window', () => {
    expect(shouldScrollToEnd(3, 3, true)).toBe(false)
    expect(shouldScrollToEnd(3, 4, false)).toBe(false)
    expect(shouldScrollToEnd(3, 4, INITIAL_STATUS.follow)).toBe(false)
  })
})

test('a person scrolling the pane passes through untouched', async ($, on) => {
  mock.clock(on, { now: 1_000 })
  let seen = 0
  on('ui.scroll', () => {
    seen++
    return {}
  })
  const moved = await $.ui.scroll({
    component: 'Pane',
    requestId: 'ox-brief',
    offset: 3,
    by: -1,
    bodyRows: 2,
    contentRows: 8,
    origin: { kind: 'person' },
  } as never)
  expect(moved).toEqual({})
  expect(seen).toBe(1)
})

