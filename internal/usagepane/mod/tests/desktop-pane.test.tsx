// The brief pane in the Claude desktop app: it opens by itself, docked beside
// the chat, at session start and when the desktop app attaches, and stays
// closed across sessions once a person closes it. Failure prevented: desktop
// users never find the pane (there is no status bar pointing at /ox-brief),
// terminal users get a pane they never asked for, or a pane the person closed
// keeps coming back every session.

import { describe, expect, mock, test } from 'claude-code/testing'

import { BRIEF_ARGV, DESKTOP_COLUMNS, REFRESH_MS, shouldAutoOpen } from '../hooks/logic'
import { register } from '../hooks/register'

const PLUGIN = 'ox-cli-brief-pane'

const COMMAND = {
  command: 'ox-brief',
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
function mockSession(on: On, surfaces: string[], stored: Record<string, unknown> = {}, { placed = true } = {}) {
  const store: Record<string, unknown> = { ...stored }
  const opened: Array<Record<string, unknown>> = []
  const closed: string[] = []
  const runs: string[][] = []
  const open = new Set<string>()
  const clock = mock.clock(on, { now: 1_000 })
  // The roster as it stands at each call: a test may push to it to attach.
  on('session.surfaces', () => ({ value: [...surfaces] }))
  on('store.get', (_, e) => ({ value: store[e.key] }))
  on('store.set', (_, e) => {
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
  on('ui.panes', () => ({ value: [...open].map(id => ({ id, title: 'ox brief', isShown: true, isFocused: false, isPlaced: true })) }))
  on('process.run', (_, e) => {
    runs.push([...e.argv])
    return ok(JSON.stringify(REPORT))
  })
  on('session.start', (_, e) => ({ cwd: e.cwd }))
  on('session.attach', (_, e) => ({ clientId: e.clientId }))
  on('session.detach', (_, e) => ({ clientId: e.clientId }))
  on('command.register', (_, e) => ({ value: { command: e.name } }))
  return { store, opened, closed, runs, clock }
}

const briefRuns = (runs: string[][]) => runs.filter(r => r.join(' ') === BRIEF_ARGV.join(' '))

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

test('desktop: the session start opens the brief pane docked beside the chat and runs ox brief', async ($, on) => {
  const s = mockSession(on, ['desktop'])
  await $.session.start({ cwd: '/', surface: 'desktop', isInteractive: true })
  expect(s.opened).toHaveLength(1)
  expect(s.opened[0]).toMatchObject({ id: 'ox-brief', title: 'ox brief', columns: DESKTOP_COLUMNS })
  expect(DESKTOP_COLUMNS).toBe(48)
  // It sits beside the chat: it never takes the keyboard or closes on Escape.
  expect(s.opened[0].focus).toBeUndefined()
  expect(s.opened[0].closeOnEscape).toBeUndefined()
  expect(briefRuns(s.runs).length).toBeGreaterThanOrEqual(1)
  expect(briefRuns(s.runs)[0]).toEqual(['ox', 'brief', '--json', '--recent', '0'])
})

test('terminal: the session start opens nothing and runs no ox brief', async ($, on) => {
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
  expect(s.opened[0]).toMatchObject({ id: 'ox-brief', columns: DESKTOP_COLUMNS })
})

test('a pane the person closed stays closed next session; /ox-brief reopens it and turns it back on', async ($, on) => {
  // autoOpen false is what a person's close stores (see the next test).
  const s = mockSession(on, ['desktop'], { autoOpen: false })
  await $.session.start({ cwd: '/', surface: 'desktop', isInteractive: true })
  await $.session.attach({ surface: 'desktop', clientId: 'desktop:default' })
  expect(s.opened).toEqual([])

  expect((await $.command.run({ ...COMMAND, surface: 'desktop', args: '' } as never)).text).toBe('ox brief pane opened.')
  expect(s.opened.map(o => o.id)).toEqual(['ox-brief'])
  expect(s.store.autoOpen).toBe(true)

  await $.session.start({ cwd: '/', surface: 'desktop', isInteractive: true })
  expect(s.opened.map(o => o.id)).toEqual(['ox-brief', 'ox-brief'])
})

test('/ox-brief off closes the pane and stops it opening by itself; a plugin close alone stores nothing', async ($, on) => {
  const s = mockSession(on, ['desktop'])
  await $.session.start({ cwd: '/', surface: 'desktop', isInteractive: true })
  expect(s.opened).toHaveLength(1)
  expect(s.store.autoOpen).toBeUndefined()

  expect((await $.command.run({ ...COMMAND, surface: 'desktop', args: 'off' } as never)).text).toBe('ox brief pane closed.')
  expect(s.closed).toEqual(['ox-brief'])
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
    expect(found.filter).toEqual({ id: 'ox-brief' })
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
    await closeHook()($, { id: 'ox-brief', origin: { kind } }, async () => {
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
    requestId: 'ox-brief',
    props: { title: 'ox brief', isFocused: false, bodyColumns, placement: 'dock', scroll: { offset: 0, bodyRows: 40 }, view: {} },
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

const PANE_PROPS = { title: 'ox brief', isFocused: false, bodyColumns: 46, placement: 'dock', scroll: { offset: 0, bodyRows: 40 }, view: {} }

test('desktop: an open the window is too narrow to place polls nothing until the pane is drawn', async ($, on) => {
  const s = mockSession(on, ['desktop'], {}, { placed: false })
  await $.session.start({ cwd: '/', surface: 'desktop', isInteractive: true })
  expect(s.opened).toHaveLength(1)
  await s.clock.advance(REFRESH_MS * 3 + 1_000)
  expect(briefRuns(s.runs)).toEqual([])

  // The person widens the window: the surface places the pane and draws it.
  const ui = await $.ui.mount({ plugin: PLUGIN, surface: 'desktop', component: 'Pane', requestId: 'ox-brief', props: PANE_PROPS } as never)
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

test('/ox-brief opens at the desktop width where the desktop app is attached, the default width in the terminal', async ($, on) => {
  const surfaces = ['terminal']
  const s = mockSession(on, surfaces)
  await $.command.run({ ...COMMAND, surface: 'terminal', args: '' } as never)
  expect(s.opened[0]).toEqual({ id: 'ox-brief', title: 'ox brief' })
  surfaces.push('desktop')
  await $.command.run({ ...COMMAND, surface: 'desktop', args: '' } as never)
  expect(s.opened[1]).toEqual({ id: 'ox-brief', title: 'ox brief', columns: DESKTOP_COLUMNS })
})
