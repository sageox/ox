import { describe, expect, mock, test } from 'claude-code/testing'

import {
  availableRows,
  bar,
  briefRunInit,
  coauthorRows,
  contextRows,
  evidencePhrase,
  formatClock,
  formatRow,
  formatTokens,
  headerLines,
  INITIAL_STATUS,
  isAtEnd,
  isOxCommand,
  MAX_USED,
  parseReport,
  RUN_TIMEOUT_MS,
  shouldScrollToEnd,
  truncate,
  usedRows,
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

// The fixture the design is drawn for: a discussion cited in a commit, a plan
// the AI coworker reported using, a murmur it read, two discussions unused.
const USED_REPORT = {
  ...REPORT,
  surfaced: [
    { ref: 'disc_9', ref_type: 'discussion', via: 'query', title: 'Release versioning sync', url: 'https://sageox.ai/c/disc_9', seq: 2 },
    { ref: 'plan_4', ref_type: 'plan', via: 'prime', title: 'Brief pane redesign', url: 'https://sageox.ai/p/plan_4' },
    { ref: 'murmur_1', ref_type: 'murmur', from: 'Oxa1', from_name: 'Avery', topic: 'wip', via: 'murmur' },
    { ref: 'disc_10', ref_type: 'discussion', via: 'query', title: 'Pricing page review', url: 'https://sageox.ai/c/disc_10' },
    { ref: 'disc_11', ref_type: 'discussion', via: 'query', title: 'Onboarding retro', url: 'https://sageox.ai/c/disc_11' },
    { ref: 'docs/adr-031.md', ref_type: 'doc', via: 'prime', title: 'ADR-031 ox-owned names' },
    { ref: 'ox-cli-plan', ref_type: 'skill', via: 'prime' },
  ],
  relevant: {
    count: 3,
    surfaced_count: 7,
    by_evidence: { read: 1, cited: 1, reported: 1, judged: 0 },
    items: [
      { ref: 'disc_9', ref_type: 'discussion', title: 'Release versioning sync', url: 'https://sageox.ai/c/disc_9', evidence: 'cited', grade: 'provable', detail: 'cited_commit', seq: 3, commit: 'abc1234' },
      { ref: 'plan_4', ref_type: 'plan', title: 'Brief pane redesign', url: 'https://sageox.ai/p/plan_4', evidence: 'reported', grade: 'reported', detail: 'reported', why: 'followed its rollout order for the pane sections' },
      { ref: 'murmur_1', ref_type: 'murmur', evidence: 'read', grade: 'provable', detail: 'read', seq: 4 },
    ],
  },
  attribution: { would_attribute: true, category: 'moderate', reason: 'a discussion cited in a commit and a plan followed', source: 'derived', threshold: 2 },
}

const item = (over: Record<string, unknown>) => ({ ref: 'r', evidence: 'read', grade: 'provable', ...over })

describe('used rows', () => {
  test('each evidence phrase', () => {
    const cases: Array<[string, Record<string, unknown>, string]> = [
      ['read', { evidence: 'read', detail: 'read' }, 'read'],
      ['re-opened', { evidence: 'read', detail: 'requery' }, 're-opened'],
      ['cited', { evidence: 'cited', detail: 'cited' }, 'cited'],
      ['cited in commit', { evidence: 'cited', detail: 'cited_commit', commit: 'abc1234' }, 'cited in commit abc1234'],
      ['cited in commit, no sha', { evidence: 'cited', detail: 'cited_commit' }, 'cited in a commit'],
      ['reported with why', { evidence: 'reported', detail: 'reported', why: 'kept the rule' }, 'reported: kept the rule'],
      ['reported bare', { evidence: 'reported' }, 'reported'],
      ['judged', { evidence: 'judged', detail: 'judged' }, 'judged'],
    ]
    for (const [name, over, want] of cases) {
      expect([name, evidencePhrase(item(over) as never)]).toEqual([name, want])
    }
  })
  test('the fixture: kinds line up, murmurs join to who and topic, links only with a url', () => {
    const r = parseReport(JSON.stringify(USED_REPORT))!
    expect(usedRows(r, 80)).toEqual([
      { key: 'used:0', text: 'discussion  Release versioning sync', phrase: 'cited in commit abc1234', inline: true, url: 'https://sageox.ai/c/disc_9' },
      { key: 'used:1', text: 'plan        Brief pane redesign', phrase: 'reported: followed its rollout order for the pane sections', inline: false, url: 'https://sageox.ai/p/plan_4' },
      { key: 'used:2', text: 'murmur      Avery  [wip]', phrase: 'read', inline: true, url: undefined },
    ])
  })
  test('a phrase that does not fit gets its own row, cut to the pane', () => {
    const r = parseReport(JSON.stringify(USED_REPORT))!
    const plan = usedRows(r, 30)[1]!
    expect(plan.inline).toBe(false)
    expect(plan.phrase).toBe('reported: followed its roll…')
    expect([...plan.phrase].length).toBe(28)
  })
  test('a murmur with no sender reads as a teammate', () => {
    const r = parseReport(JSON.stringify({ ...REPORT, surfaced: [], relevant: { count: 1, surfaced_count: 1, items: [item({ ref: 'm', ref_type: 'murmur' })] } }))!
    expect(usedRows(r, 80)[0]?.text).toBe('murmur  a teammate')
  })
  test('caps at MAX_USED, then +N more', () => {
    const items = Array.from({ length: MAX_USED + 3 }, (_, i) => item({ ref: `doc_${i}`, ref_type: 'doc', title: `Doc ${i}` }))
    const rows = usedRows(parseReport(JSON.stringify({ ...REPORT, relevant: { count: items.length, surfaced_count: 20, items } }))!, 80)
    expect(rows).toHaveLength(MAX_USED + 1)
    expect(rows.at(-1)).toEqual({ key: 'used:more', text: '+3 more', phrase: '', inline: true })
  })
})

describe('available rows', () => {
  test('exclude what was used; fold discussions, plans, sessions, murmurs; docs by name', () => {
    const r = parseReport(JSON.stringify(USED_REPORT))!
    expect(availableRows(r)).toEqual([
      { key: 'available:0', text: 'doc  ADR-031 ox-owned names' },
      { key: 'available:counts', text: '2 discussions' },
      { key: 'available:skills', text: '1 team skill' },
    ])
  })
  test('every folded kind, pluralized', () => {
    const surfaced = ['discussion', 'plan', 'plan', 'session', 'murmur', 'murmur', 'murmur'].map((ref_type, i) => ({ ref: `r${i}`, ref_type }))
    expect(availableRows(parseReport(JSON.stringify({ ...REPORT, surfaced, relevant: { count: 0, surfaced_count: 7, items: [] } }))!)).toEqual([
      { key: 'available:counts', text: '1 discussion · 2 plans · 1 session · 3 murmurs' },
    ])
  })
  test('nothing unused, nothing to draw', () => {
    expect(availableRows(parseReport(JSON.stringify({ ...REPORT, surfaced: [] }))!)).toEqual([])
  })
  test('folded counts come from the full per-type totals when the list is capped', () => {
    // ox capped surfaced[] for display; surfaced_by_type counts everything.
    const surfaced = [
      { ref: 'AGENTS.md', ref_type: 'doc', via: 'prime' },
      { ref: 'mur_1', ref_type: 'murmur', via: 'whisper' },
      { ref: 'mur_2', ref_type: 'murmur', via: 'whisper', used: { evidence: 'cited' } },
      { ref: 'cnv_a', ref_type: 'discussion', via: 'retrieval' },
      { ref: 'ox-cli-plan', ref_type: 'skill', via: 'prime' },
    ]
    const surfaced_by_type = { doc: 4, murmur: 11, discussion: 2, plan: 5, skill: 3 }
    const r = parseReport(JSON.stringify({ ...REPORT, surfaced, surfaced_by_type, relevant: { count: 1, surfaced_count: 25 } }))!
    expect(availableRows(r)).toEqual([
      { key: 'available:0', text: 'doc  AGENTS.md' },
      { key: 'available:counts', text: '2 discussions · 5 plans · 10 murmurs' },
      { key: 'available:skills', text: '3 team skills' },
    ])
  })
})

describe('header and co-author', () => {
  test('used N of M with the co-author category', () => {
    expect(headerLines(parseReport(JSON.stringify(USED_REPORT))).summary).toBe('calls 3 (1 failed) · ~16k tokens · used 3 of 7 · co-author moderate')
  })
  test('category none, or no attribution, leaves co-author out', () => {
    const none = { ...USED_REPORT, attribution: { ...USED_REPORT.attribution, category: 'none', would_attribute: false } }
    expect(headerLines(parseReport(JSON.stringify(none))).summary).toBe('calls 3 (1 failed) · ~16k tokens · used 3 of 7')
  })
  test('reported_none says used none', () => {
    const r = { ...REPORT, relevant: { count: 0, surfaced_count: 6, reported_none: true, items: [] } }
    expect(headerLines(parseReport(JSON.stringify(r))).summary).toBe('calls 3 (1 failed) · ~16k tokens · used none')
  })
  test('co-author rows: verdict and source wording', () => {
    const derived = parseReport(JSON.stringify(USED_REPORT))!
    expect(coauthorRows(derived, 80)).toEqual({
      key: 'coauthor',
      category: 'moderate',
      verdict: '· would attribute',
      reason: 'derived from use: a discussion cited in a commit and a plan followed',
    })
    const below = parseReport(
      JSON.stringify({ ...USED_REPORT, attribution: { would_attribute: false, category: 'minor', source: 'self-reported', threshold: 2 } }),
    )!
    expect(coauthorRows(below, 80)).toEqual({ key: 'coauthor', category: 'minor', verdict: '· below threshold 2', reason: 'reported by the AI coworker' })
    expect(coauthorRows(derived, 20)?.reason).toBe('derived from use: a…')
    expect(coauthorRows(parseReport(JSON.stringify(REPORT))!, 80)).toBeNull()
  })
  test('a derived reason that already says where it came from renders once', () => {
    const r = parseReport(
      JSON.stringify({
        ...USED_REPORT,
        attribution: { would_attribute: false, category: 'none', reason: 'derived from context use: nothing used', source: 'derived', threshold: 0.5 },
      }),
    )!
    expect(coauthorRows(r, 80)?.reason).toBe('derived from context use: nothing used')
  })
})

test('the pane links what was used and folds the rest into counts', async ($, on) => {
  mock.clock(on, { now: 1_000 })
  on('process.run', () => ok(JSON.stringify(USED_REPORT)))
  on('ui.open', () => ({ value: { isPlaced: true } }))

  await $.command.run({ ...COMMAND, args: '' } as never)
  const ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
  expect((await ui.find({ type: 'Text', text: /^calls \d/ }))?.text).toBe('calls 3 (1 failed) · ~16k tokens · used 3 of 7 · co-author moderate')

  expect((await ui.find({ key: 'used:0' }))?.text).toBe('discussion  Release versioning sync  cited in commit abc1234')
  expect((await ui.find({ type: 'Link', text: 'https://sageox.ai/c/disc_9' }))?.props.href).toBe('https://sageox.ai/c/disc_9')
  expect((await ui.find({ key: 'used:1' }))?.text).toBe('plan        Brief pane redesign')
  expect((await ui.find({ key: 'used:1:why' }))?.text).toBe('reported: followed its rollout order for the pane sections')
  expect((await ui.find({ type: 'Link', text: 'https://sageox.ai/p/plan_4' }))?.props.href).toBe('https://sageox.ai/p/plan_4')
  expect((await ui.find({ key: 'used:2' }))?.text).toBe('murmur      Avery  [wip]  read')
  expect(await ui.find({ key: 'used:2:url' })).toBeUndefined()

  // The two unused discussions are a count: no title, no link.
  expect((await ui.find({ key: 'available:counts' }))?.text).toBe('2 discussions')
  expect(await ui.find({ type: 'Text', text: /Pricing page review|Onboarding retro/ })).toBeUndefined()
  expect(await ui.find({ type: 'Link', text: /disc_10|disc_11/ })).toBeUndefined()

  expect((await ui.find({ key: 'coauthor' }))?.text).toBe('co-author  moderate · would attribute')
  expect((await ui.find({ key: 'coauthor:reason' }))?.text).toBe('derived from use: a discussion cited in a commit and a plan followed')

  // The used section leads; co-author sits right above calls.
  const sectionKeys = new Set(['h:used', 'h:context', 'h:available', 'coauthor', 'h:calls', 'legend'])
  const order = (await ui.findAll({ type: 'Box' })).map(b => b.key).filter(k => k !== undefined && sectionKeys.has(k))
  expect(order).toEqual(['h:used', 'h:context', 'h:available', 'coauthor', 'h:calls', 'legend'])

  // Plumbing words never reach the person.
  expect(await ui.find({ type: 'Text', text: /injected|not yet reported|you reported|surfaced|grade|provable| tok\b|✓/ })).toBeUndefined()
  await ui.unmount()
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
    expect(h.summary).toBe('calls 3 (1 failed) · ~16k tokens · used 2 of 6')
  })
  test('header without relevant counts used items, and says not recording', () => {
    const { relevant: _, ...rest } = REPORT
    const h = headerLines(parseReport(JSON.stringify({ ...rest, session: { recording: false } })))
    expect(h.marker).toBe('○ not recording')
    expect(h.summary).toBe('calls 3 (1 failed) · ~16k tokens · used 2 of 6')
  })
  test('an old trace without context or surfaced still parses', () => {
    const h = headerLines(parseReport(JSON.stringify({ view: 'session', session: { recording: true }, calls: { total: 0, failed: 0 } })))
    expect(h.summary).toBe('calls 0 · ~0 tokens · used 0 of 0')
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
  test('an older report without items: used falls back to surfaced use evidence', () => {
    const r = parseReport(JSON.stringify(REPORT))!
    expect(usedRows(r, 80)).toEqual([
      { key: 'used:0', text: 'discussion  Release versioning sync', phrase: 'cited', inline: true, url: 'https://sageox.ai/c/disc_9' },
      { key: 'used:1', text: 'murmur      Avery  [wip]', phrase: 'read', inline: true, url: undefined },
    ])
    expect(availableRows(r)).toEqual([
      { key: 'available:counts', text: '1 plan · 1 murmur' },
      { key: 'available:skills', text: '2 team skills' },
    ])
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

test('the pane tells ox brief which Claude Code session it is reporting on', async ($, on) => {
  // Failure prevented: the pane runs ox from the host process, which has no
  // session variables, so ox reported a recording session as not recording
  // while the status line showed it recording.
  mock.clock(on, { now: 1_000 })
  on('session.id', () => ({ value: 'sess-7f2a' }))
  const envs: (Record<string, string> | undefined)[] = []
  on('process.run', (_, e) => {
    envs.push(e.init?.env)
    return ok(JSON.stringify(REPORT))
  })
  on('ui.open', () => ({ value: { isPlaced: true } }))
  on('ui.panes', () => ({ value: [{ id: 'ox-brief', title: 'ox brief', isShown: true, isFocused: false, isPlaced: true }] }))

  await $.command.run({ ...COMMAND, args: '' } as never)
  expect(envs.length).toBe(1)
  expect(envs[0]?.CLAUDE_CODE_SESSION_ID).toBe('sess-7f2a')
})

test('briefRunInit passes no environment when the session id is unknown', () => {
  expect(briefRunInit('')).toEqual({ timeoutMs: RUN_TIMEOUT_MS })
  expect(briefRunInit('abc').env).toEqual({ CLAUDE_CODE_SESSION_ID: 'abc' })
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
    expect((await ui.find({ type: 'Text', text: /^calls \d/ }))?.text).toBe('calls 3 (1 failed) · ~16k tokens · used 2 of 6')
    expect(await ui.find({ type: 'Text', text: /injected|not yet reported/ })).toBeUndefined()
    expect(await ui.find({ type: 'Text', text: /recording/ })).toBeDefined()

    for (const h of ['used', 'context', 'available', 'calls']) {
      expect((await ui.find({ key: `h:${h}` }))?.text).toBe(h)
    }
    expect((await ui.find({ key: 'context:prime' }))?.text).toBe('prime    ~15k tokens  ▇▇▇▇▇▇')
    expect(await ui.find({ type: 'Text', text: 'estimates, ~4 bytes/token' })).toBeDefined()
    expect((await ui.find({ key: 'used:0' }))?.text).toBe('discussion  Release versioning sync  cited')
    expect((await ui.find({ key: 'used:0:url' }))?.text).toBe('https://sageox.ai/c/disc_9')
    expect((await ui.find({ type: 'Link', text: 'https://sageox.ai/c/disc_9' }))?.props.href).toBe('https://sageox.ai/c/disc_9')
    expect((await ui.find({ key: 'used:1' }))?.text).toBe('murmur      Avery  [wip]  read')
    // Unused items are a count, never a link.
    expect((await ui.find({ key: 'available:counts' }))?.text).toBe('1 plan · 1 murmur')
    expect((await ui.find({ key: 'available:skills' }))?.text).toBe('2 team skills')
    expect(await ui.find({ type: 'Link', text: 'https://sageox.ai/p/plan_4' })).toBeUndefined()
    expect(await ui.find({ type: 'Text', text: /Brief pane redesign|Sam/ })).toBeUndefined()
    expect(await ui.find({ key: 'coauthor' })).toBeUndefined()
    expect(await ui.find({ type: 'Text', text: "used = evidence from the transcript, a commit, or the AI coworker's report" })).toBeDefined()

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

  // Nothing used: no used section, no legend, and unused items only as counts.
  stdout = JSON.stringify({
    ...EMPTY,
    surfaced: EMPTY.surfaced.map(({ used: _, ...item }) => item),
    relevant: undefined,
  })
  await $.command.run({ ...COMMAND, args: 'refresh' } as never)
  ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
  expect(await ui.find({ key: 'h:used' })).toBeUndefined()
  expect(await ui.find({ type: 'Text', text: /^used = / })).toBeUndefined()
  expect(await ui.find({ type: 'Link' })).toBeUndefined()
  expect((await ui.find({ key: 'available:counts' }))?.text).toBe('1 discussion · 1 plan · 2 murmurs')
  expect((await ui.find({ type: 'Text', text: /^calls \d/ }))?.text).toBe('calls 0 · ~16k tokens · used 0 of 6')
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

