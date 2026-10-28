// The brief pane in the Claude desktop app: it opens by itself, docked beside
// the chat, at session start and when the desktop app attaches, and stays
// closed across sessions once a person closes it. Failure prevented: desktop
// users never find the pane (there is no status bar pointing at /ox-session-report),
// terminal users get a pane they never asked for, or a pane the person closed
// keeps coming back every session.

import { describe, expect, mock, test } from 'claude-code/testing'

import { BRIEF_ARGV, DESKTOP_COLUMNS, REFRESH_MS, STATUSLINE_ARGV, STATUSLINE_REFRESH_MS, shouldAutoOpen } from '../hooks/logic'
import { register } from '../hooks/register'

const PLUGIN = 'ox-cli-brief-pane'

const COMMAND = {
  command: 'ox-session-report',
  origin: { kind: 'composer' },
  presentation: { isFullscreen: true, columns: 160 },
} as const

// The fixture the brief pane is designed around: three used items (two with
// links), metered context, unused items, a co-author verdict and three calls.
const REPORT = {
  view: 'session',
  session: { recording: true },
  trace: { available: true, source: 'session' },
  calls: {
    total: 3,
    failed: 1,
    recent: [
      { label: 'murmur', command: 'ox murmur list', seq: 3, tokens: 80, failed: true, ts: '2026-10-24T10:07:00Z' },
      { label: 'query', command: 'ox query "how do we version releases and keep the changelog honest"', seq: 2, tokens: 1234, ts: '2026-10-24T10:05:00Z' },
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
  },
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

type On = Parameters<Parameters<typeof test>[1]>[1]

function ok(stdout: string) {
  return { value: { exitCode: 0, stdout, stderr: '', isStdoutTruncated: false, isStderrTruncated: false } }
}

// The engine side of one desktop (or terminal) session: the roster of
// surfaces, the plugin's store, every pane opened and closed, every ox run.
// `placed: false` answers each open as a surface too narrow to place an
// unasked pane does.
function mockSession(on: On, surfaces: string[], stored: Record<string, unknown> = {}, { placed = true, storeFails = false } = {}) {
  const store: Record<string, unknown> = { ...stored }
  const opened: Array<Record<string, unknown>> = []
  const closed: string[] = []
  const runs: string[][] = []
  const open = new Set<string>()
  // Set `ox.fails` to make every ox run exit 1.
  const ox = { fails: false }
  const clock = mock.clock(on, { now: 1_000 })
  // The roster as it stands at each call: a test may push to it to attach.
  on('session.surfaces', () => ({ value: [...surfaces] }))
  on('store.get', (_, e) => ({ value: store[e.key] }))
  on('store.set', (_, e) => {
    if (storeFails) {
      return { deny: 'the store is unavailable' } as never
    }
    store[e.key] = e.value
    return { value: undefined }
  })
  on('ui.open', (_, e) => {
    opened.push({ ...e })
    open.add(e.id)
    return { value: placed ? { isPlaced: true } : { isPlaced: false, reason: 'the window is too narrow' } }
  })
  on('ui.close', (_, e) => {
    closed.push(e.id)
    open.delete(e.id)
    return { value: undefined }
  })
  on('ui.panes', () => ({ value: [...open].map(id => ({ id, title: 'ox session report', isShown: true, isFocused: false, isPlaced: true })) }))
  on('process.run', (_, e) => {
    runs.push([...e.argv])
    if (ox.fails) {
      return { value: { exitCode: 1, stdout: '', stderr: 'ox failed', isStdoutTruncated: false, isStderrTruncated: false } }
    }
    return ok(JSON.stringify(REPORT))
  })
  on('session.start', (_, e) => ({ cwd: e.cwd }))
  on('session.attach', (_, e) => ({ clientId: e.clientId }))
  on('session.detach', (_, e) => ({ clientId: e.clientId }))
  on('command.register', (_, e) => ({ value: { command: e.name } }))
  return { store, opened, closed, runs, clock, ox }
}

const briefRuns = (runs: string[][]) => runs.filter(r => r.join(' ') === BRIEF_ARGV.join(' '))
const statuslineRuns = (runs: string[][]) => runs.filter(r => r.join(' ') === STATUSLINE_ARGV.join(' '))

describe('shouldAutoOpen', () => {
  test('opens only on desktop, and only until turned off', () => {
    const cases: Array<[string, readonly string[], unknown, boolean]> = [
      ['desktop, unset', ['desktop'], undefined, true],
      ['desktop, true', ['desktop'], true, true],
      ['desktop, false', ['desktop'], false, false],
      ['desktop, the string false', ['desktop'], 'false', true],
      ['desktop, null', ['desktop'], null, true],
      ['terminal only', ['terminal'], undefined, false],
      ['terminal only, true', ['terminal'], true, false],
      ['no surfaces', [], undefined, false],
      ['desktop among several', ['terminal', 'mobile', 'desktop'], undefined, true],
      ['vscode', ['vscode'], undefined, false],
    ]
    for (const [name, surfaces, autoOpen, want] of cases) {
      expect([name, shouldAutoOpen({ surfaces, autoOpen })]).toEqual([name, want])
    }
  })
})

test('desktop: the session start opens the brief pane docked beside the chat and runs ox session report', async ($, on) => {
  const s = mockSession(on, ['desktop'])
  await $.session.start({ cwd: '/', surface: 'desktop', isInteractive: true })
  expect(s.opened).toHaveLength(1)
  expect(s.opened[0]).toMatchObject({ id: 'ox-session-report', title: 'ox session report', columns: DESKTOP_COLUMNS })
  expect(DESKTOP_COLUMNS).toBe(48)
  // It sits beside the chat: it never takes the keyboard or closes on Escape.
  expect(s.opened[0].focus).toBeUndefined()
  expect(s.opened[0].closeOnEscape).toBeUndefined()
  expect(briefRuns(s.runs).length).toBeGreaterThanOrEqual(1)
  expect(briefRuns(s.runs)[0]).toEqual(['ox', 'session', 'report', '--json', '--recent', '0'])
})

test('terminal: the session start opens nothing and runs no ox session report', async ($, on) => {
  const s = mockSession(on, ['terminal'])
  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  expect(s.opened).toEqual([])
  expect(briefRuns(s.runs)).toEqual([])
})

test('the desktop app attaching after the start opens the pane; vscode and mobile do not', async ($, on) => {
  const surfaces = ['terminal']
  const s = mockSession(on, surfaces)
  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  surfaces.push('vscode')
  await $.session.attach({ surface: 'vscode', clientId: 'vscode:default' })
  surfaces.push('mobile')
  await $.session.attach({ surface: 'mobile', clientId: 'mobile:default' })
  expect(s.opened).toEqual([])
  expect(briefRuns(s.runs)).toEqual([])
  surfaces.push('desktop')
  await $.session.attach({ surface: 'desktop', clientId: 'desktop:default' })
  expect(s.opened).toHaveLength(1)
  expect(s.opened[0]).toMatchObject({ id: 'ox-session-report', columns: DESKTOP_COLUMNS })
})

test('a pane the person closed stays closed next session; /ox-session-report reopens it and turns it back on', async ($, on) => {
  // autoOpen false is what a person's close stores (see the next test).
  const s = mockSession(on, ['desktop'], { autoOpen: false })
  await $.session.start({ cwd: '/', surface: 'desktop', isInteractive: true })
  await $.session.attach({ surface: 'desktop', clientId: 'desktop:default' })
  expect(s.opened).toEqual([])

  expect((await $.command.run({ ...COMMAND, surface: 'desktop', args: '' } as never)).text).toBe('ox session report pane opened.')
  expect(s.opened.map(o => o.id)).toEqual(['ox-session-report'])
  expect(s.store.autoOpen).toBe(true)

  await $.session.start({ cwd: '/', surface: 'desktop', isInteractive: true })
  expect(s.opened.map(o => o.id)).toEqual(['ox-session-report', 'ox-session-report'])
})

test('/ox-session-report off closes the pane and stops it opening by itself; a plugin close alone stores nothing', async ($, on) => {
  const s = mockSession(on, ['desktop'])
  await $.session.start({ cwd: '/', surface: 'desktop', isInteractive: true })
  expect(s.opened).toHaveLength(1)
  expect(s.store.autoOpen).toBeUndefined()

  expect((await $.command.run({ ...COMMAND, surface: 'desktop', args: 'off' } as never)).text).toBe('ox session report pane closed.')
  expect(s.closed).toEqual(['ox-session-report'])
  expect(s.store.autoOpen).toBe(false)

  await $.session.start({ cwd: '/', surface: 'desktop', isInteractive: true })
  expect(s.opened).toHaveLength(1)
})

// The test engine cannot raise a person's close (its `$` has no `ui.close`),
// so the pane's own ui.close hook is driven directly with each origin.
describe('the ui.close hook', () => {
  type Hook = ($: unknown, e: unknown, next: (e: unknown) => Promise<unknown>) => Promise<unknown>

  function closeHook(): Hook {
    const hooks: Array<{ name: string; filter: unknown; fn: Hook }> = []
    const on = (name: string, a: unknown, b?: unknown) => {
      hooks.push({ name, filter: b === undefined ? undefined : a, fn: (b ?? a) as Hook })
      return { catch: () => undefined }
    }
    register(on as never)
    const found = hooks.find(h => h.name === 'ui.close')
    if (found === undefined) {
      throw new Error('no ui.close hook registered')
    }
    expect(found.filter).toEqual({ id: 'ox-session-report' })
    return found.fn
  }

  async function close(kind: 'person' | 'plugin' | 'unload', surfaces: string[] = ['desktop']) {
    const sets: Array<[string, unknown]> = []
    const $ = {
      session: { surfaces: async () => surfaces },
      store: {
        set: async (key: string, value: unknown) => {
          sets.push([key, value])
        },
      },
    }
    let passed = 0
    await closeHook()($, { id: 'ox-session-report', origin: { kind } }, async () => {
      passed++
      return undefined
    })
    return { sets, passed }
  }

  test('a person closing the pane turns off opening it by itself', async () => {
    expect(await close('person')).toEqual({ sets: [['autoOpen', false]], passed: 1 })
  })
  test('a person closing it in a terminal-only session leaves desktop opening untouched', async () => {
    expect(await close('person', ['terminal'])).toEqual({ sets: [], passed: 1 })
    expect(await close('person', ['terminal', 'desktop'])).toEqual({ sets: [['autoOpen', false]], passed: 1 })
  })
  test('a plugin close or an unload leaves autoOpen untouched', async () => {
    expect(await close('plugin')).toEqual({ sets: [], passed: 1 })
    expect(await close('unload')).toEqual({ sets: [], passed: 1 })
  })
})

test('desktop: the pane draws every section from the fixture within its width', async ($, on) => {
  mockSession(on, ['desktop'])
  await $.session.start({ cwd: '/', surface: 'desktop', isInteractive: true })
  const bodyColumns = DESKTOP_COLUMNS - 2
  const ui = await $.ui.mount({
    plugin: PLUGIN,
    surface: 'desktop',
    component: 'Pane',
    requestId: 'ox-session-report',
    props: { title: 'ox session report', isFocused: false, bodyColumns, placement: 'dock', scroll: { offset: 0, bodyRows: 40 }, view: {} },
  } as never)

  expect((await ui.find({ type: 'Text', text: /^calls \d/ }))?.text).toBe('calls 3 (1 failed) · ~16k tokens · used 3 of 7 · co-author moderate'.slice(0, bodyColumns - 1) + '…')

  // used, with its links
  expect((await ui.find({ key: 'used:0' }))?.text).toBe('discussion  Release versioning sync')
  expect((await ui.find({ key: 'used:0:why' }))?.text).toBe('cited in commit abc1234')
  expect((await ui.find({ type: 'Link', text: 'https://sageox.ai/c/disc_9' }))?.props.href).toBe('https://sageox.ai/c/disc_9')
  expect((await ui.find({ type: 'Link', text: 'https://sageox.ai/p/plan_4' }))?.props.href).toBe('https://sageox.ai/p/plan_4')
  expect((await ui.find({ key: 'used:2' }))?.text).toBe('murmur      Avery  [wip]  read')
  // context, available, co-author, calls
  expect((await ui.find({ key: 'context:prime' }))?.text).toMatch(/^prime\s+~15k tokens\s+▇+$/)
  expect((await ui.find({ key: 'available:counts' }))?.text).toBe('2 discussions')
  expect((await ui.find({ key: 'coauthor' }))?.text).toBe('co-author  moderate · would attribute')
  expect(await ui.find({ key: 'coauthor:reason' })).toBeDefined()
  expect(await ui.find({ key: 'row:2' })).toBeDefined()

  const sectionKeys = new Set(['h:used', 'h:context', 'h:available', 'coauthor', 'h:calls', 'legend'])
  const order = (await ui.findAll({ type: 'Box' })).map(b => b.key).filter(k => k !== undefined && sectionKeys.has(k))
  expect(order).toEqual(['h:used', 'h:context', 'h:available', 'coauthor', 'h:calls', 'legend'])

  // Dense: no rule under the header and no blank spacer row before a
  // section, so a docked pane spends its rows on content.
  expect(await ui.find({ type: 'Text', text: /^─+$/ })).toBeUndefined()
  for (const b of await ui.findAll({ type: 'Box' })) {
    expect([b.key, b.props.marginTop ?? 0]).toEqual([b.key, 0])
  }

  // Every line that does not wrap fits the pane's body: nothing is cut by
  // the surface. Lines drawn to wrap (links, the legend) wrap instead.
  const keyed = (await ui.findAll({ type: 'Box' })).filter(b => b.key !== undefined && b.key !== 'legend' && !b.key.endsWith(':url') && !b.key.startsWith('available'))
  expect(keyed.length).toBeGreaterThan(10)
  for (const b of keyed) {
    expect([b.key, [...(b.text ?? '')].length <= bodyColumns]).toEqual([b.key, true])
  }
  for (const t of await ui.findAll({ type: 'Text' })) {
    if (t.props.wrap !== 'wrap') {
      expect([t.text, [...(t.text ?? '')].length <= bodyColumns]).toEqual([t.text, true])
    }
  }
  await ui.unmount()
})

const PANE_PROPS = { title: 'ox session report', isFocused: false, bodyColumns: 46, placement: 'dock', scroll: { offset: 0, bodyRows: 40 }, view: {} }

test('desktop: an open the window is too narrow to place polls nothing until the pane is drawn', async ($, on) => {
  const s = mockSession(on, ['desktop'], {}, { placed: false })
  await $.session.start({ cwd: '/', surface: 'desktop', isInteractive: true })
  expect(s.opened).toHaveLength(1)
  await s.clock.advance(REFRESH_MS * 3 + 1_000)
  expect(briefRuns(s.runs)).toEqual([])

  // The person widens the window: the surface places the pane and draws it.
  const ui = await $.ui.mount({ plugin: PLUGIN, surface: 'desktop', component: 'Pane', requestId: 'ox-session-report', props: PANE_PROPS } as never)
  await s.clock.advance(0)
  expect(briefRuns(s.runs)).toHaveLength(1)
  // A drawn pane keeps polling, but redraws with a report do not re-run it.
  await s.clock.advance(REFRESH_MS)
  expect(briefRuns(s.runs)).toHaveLength(2)
  await ui.unmount()
})

test('desktop: the desktop app detaching stops the pane polling; another surface leaving does not', async ($, on) => {
  const surfaces = ['terminal', 'desktop']
  const s = mockSession(on, surfaces)
  await $.session.start({ cwd: '/', surface: 'desktop', isInteractive: true })
  await s.clock.advance(0)
  const first = briefRuns(s.runs).length
  expect(first).toBe(1)
  await $.session.detach({ surface: 'vscode', clientId: 'vscode:default', reason: 'detach' } as never)
  await s.clock.advance(REFRESH_MS)
  expect(briefRuns(s.runs)).toHaveLength(2)

  surfaces.splice(surfaces.indexOf('desktop'), 1)
  await $.session.detach({ surface: 'desktop', clientId: 'desktop:default', reason: 'detach' } as never)
  await s.clock.advance(REFRESH_MS * 3)
  expect(briefRuns(s.runs)).toHaveLength(2)
})

test('desktop: the session ending stops the pane polling', async ($, on) => {
  const s = mockSession(on, ['desktop'])
  on('session.end', () => ({ sessionId: 's1' }) as never)
  await $.session.start({ cwd: '/', surface: 'desktop', isInteractive: true })
  await s.clock.advance(0)
  expect(briefRuns(s.runs)).toHaveLength(1)
  await $.session.end({ reason: 'other' } as never)
  await s.clock.advance(REFRESH_MS * 3)
  expect(briefRuns(s.runs)).toHaveLength(1)
})

test('/ox-session-report opens at the desktop width where the desktop app is attached, the default width in the terminal', async ($, on) => {
  const surfaces = ['terminal']
  const s = mockSession(on, surfaces)
  await $.command.run({ ...COMMAND, surface: 'terminal', args: '' } as never)
  expect(s.opened[0]).toEqual({ id: 'ox-session-report', title: 'ox session report' })
  surfaces.push('desktop')
  await $.command.run({ ...COMMAND, surface: 'desktop', args: '' } as never)
  expect(s.opened[1]).toEqual({ id: 'ox-session-report', title: 'ox session report', columns: DESKTOP_COLUMNS })
})

// Failure prevented: `/ox-session-report off` says the pane will stay closed when the
// setting never saved, and it pops open again next session.
test('/ox-session-report off says so when the setting did not save', async ($, on) => {
  const s = mockSession(on, ['desktop'], {}, { storeFails: true })
  const reply = await $.command.run({ ...COMMAND, surface: 'desktop', args: 'off' } as never)
  expect(s.closed).toEqual(['ox-session-report'])
  expect(reply.text).toBe('ox session report pane closed, but the setting did not save; it may open again next session.')
})

// Failure prevented: a `/ox-session-report` the window is too narrow to place runs
// `ox session report` every 15 seconds for a pane nobody can see.
test('/ox-session-report on a window too narrow to place it polls nothing until the pane is drawn', async ($, on) => {
  const s = mockSession(on, ['desktop'], {}, { placed: false })
  const reply = await $.command.run({ ...COMMAND, surface: 'desktop', args: '' } as never)
  expect(reply.text).toMatch(/not shown/)
  const once = briefRuns(s.runs).length
  await s.clock.advance(REFRESH_MS * 3 + 1_000)
  expect(briefRuns(s.runs)).toHaveLength(once)
})

// Failure prevented: after the desktop app leaves, a terminal-only session
// keeps running `ox statusline` every 15 seconds and after each ox command,
// for a band nothing draws.
test('desktop: the desktop app detaching stops the status band polling too', async ($, on) => {
  const surfaces = ['terminal', 'desktop']
  const s = mockSession(on, surfaces)
  on('ui.render', ($, e) => {
    const { Text } = $.ui.resolve(e)
    return <Text>engine</Text>
  })
  on('tool.call', () => ({ result: {} }))
  const band = await $.ui.mount({
    plugin: PLUGIN,
    surface: 'desktop',
    component: 'AbovePrompt',
    props: { hasSurvey: false, isWorking: false, maxRows: 10, bodyColumns: 80, scroll: { offset: 0, bodyRows: 10 }, view: {} },
  } as never)
  await s.clock.settle()
  await band.unmount()
  expect(statuslineRuns(s.runs).length).toBeGreaterThan(0)

  surfaces.splice(surfaces.indexOf('desktop'), 1)
  await $.session.detach({ surface: 'desktop', clientId: 'desktop:default', reason: 'detach' } as never)
  const before = statuslineRuns(s.runs).length
  await s.clock.advance(STATUSLINE_REFRESH_MS * 3)
  await $.tool.call({ tool: 'Bash', command: 'ox status' })
  await s.clock.settle()
  expect(statuslineRuns(s.runs)).toHaveLength(before)
})

// Failure prevented: a pane closed after a failed first read, then opened
// again, sits on "Loading…" until the timer's first tick instead of reading
// at once.
test('a pane drawn again after a close reads ox session report at once', async ($, on) => {
  const s = mockSession(on, ['desktop'])
  // A first pane whose first read fails: it stays on no report.
  s.ox.fails = true
  const first = await $.ui.mount({ plugin: PLUGIN, surface: 'desktop', component: 'Pane', requestId: 'ox-session-report', props: PANE_PROPS } as never)
  await s.clock.advance(0)
  await first.unmount()
  s.ox.fails = false
  await $.command.run({ ...COMMAND, surface: 'desktop', args: 'off' } as never)
  const failed = briefRuns(s.runs).length

  const ui = await $.ui.mount({ plugin: PLUGIN, surface: 'desktop', component: 'Pane', requestId: 'ox-session-report', props: PANE_PROPS } as never)
  await s.clock.advance(0)
  expect(briefRuns(s.runs)).toHaveLength(failed + 1)
  await ui.unmount()
})
