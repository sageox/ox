// The flow lanes in the transcript: on the desktop app (and VS Code, mobile)
// a one-line lane is drawn under each ox tool row and under each prompt that
// carried team context in; it animates once and settles. Failure prevented:
// a coworker cannot see what SageOx moved into or out of the session, a lane
// keeps animating (or never settles), the wrong prompt is credited with team
// context, or terminal sessions get drawings they cannot show.

import { describe, expect, mock, test } from 'claude-code/testing'

import { ANIM_MS } from '../hooks/scene'
import { arrived, drawn, finishOf, laneWidth, LANE_DEFAULT_PX, LANE_MAX_PX, LANE_MIN_PX, newBinder, settleDelay, startOf, submitted } from '../hooks/flowwire'

const PLUGIN = 'ox-cli-brief-pane'
const SURFACES = ['desktop', 'vscode'] as const
const MCP_SEARCH = 'mcp__3a5362ee-dfc4-43c0-a99a-d9d0d1864003__Search'
const MCP_PUBLISH = 'mcp__3a5362ee-dfc4-43c0-a99a-d9d0d1864003__PublishMurmur'

type On = Parameters<Parameters<typeof test>[1]>[1]
type Answer = { result?: unknown; text?: string; isError?: true }

const PRIME = [
  '<ox-prime>',
  '<team-context>Use OpenTofu, never Terraform.</team-context>',
  '<context-budget hint="who controls each token" sageox="1200" team="3617" total="4817">',
  '</context-budget>',
  '</ox-prime>',
].join('\n')

const MURMURS = [
  '<system-reminder>',
  '<murmur-context>Signals from coworkers delivered by SageOx.</murmur-context>',
  '<entry id="m1" importance="normal" topic="wip" source="murmur" from="Avery">editing scene.ts</entry>',
  '<entry id="m2" importance="critical" topic="release" source="murmur" from="Sam">cutting 0.12</entry>',
  '</system-reminder>',
].join('\n')

const SEARCH_OUT = JSON.stringify({ results: [{ id: 'a' }, { id: 'b' }, { id: 'c' }] }) + ' '.repeat(4000)

// The engine beneath the plugin: every row draws as "engine", every tool
// answers from `answers` by tool_use_id, attachments and submits pass as
// sent. `seen` records each render's props as the engine got them.
function mockEngine(on: On, stored: Record<string, unknown> = {}) {
  const store: Record<string, unknown> = { ...stored }
  const answers: Record<string, Answer> = {}
  const seen: Array<{ component: string; props: Record<string, unknown> }> = []
  const clock = mock.clock(on, { now: 1_000_000 })
  on('store.get', (_, e) => ({ value: store[e.key] }))
  on('store.set', (_, e) => {
    store[e.key] = e.value
    return { value: undefined }
  })
  on('session.surfaces', () => ({ value: ['desktop'] }))
  on('ui.panes', () => ({ value: [] }))
  on('process.run', () => ({ value: { exitCode: 1, stdout: '', stderr: '', isStdoutTruncated: false, isStderrTruncated: false } }))
  on('ui.render', ($, e) => {
    seen.push({ component: e.component, props: { ...(e.props as Record<string, unknown>) } })
    const { Text } = $.ui.resolve(e)
    return <Text>engine</Text>
  })
  on('tool.call', (_, e) => {
    const a = answers[(e as { tool_use_id: string }).tool_use_id]
    return (a ?? { result: { stdout: '', stderr: '', interrupted: false } }) as never
  })
  on('prompt.attachment', (_, e) => ({ text: e.text }))
  on('prompt.submit', (_, e) => ({ text: e.text }))
  on('session.end', () => ({ sessionId: 's1' }) as never)
  on('command.register', (_, e) => ({ value: { command: e.name } }))
  return { store, answers, seen, clock }
}

const toolRow = (tool: string, input: Record<string, unknown>, over: Record<string, unknown> = {}) => ({
  tool_use_id: '',
  tool,
  input,
  isRunning: false,
  isErrored: false,
  isInterrupted: false,
  ...over,
})

async function mountTool($: any, surface: string, id: string, tool: string, input: Record<string, unknown>, over: Record<string, unknown> = {}) {
  return $.ui.mount({
    plugin: PLUGIN,
    surface,
    component: 'ToolUse',
    requestId: id,
    props: { ...toolRow(tool, input, over), tool_use_id: id },
    viewport: { columns: 90, rows: 40 },
  } as never)
}

async function mountPrompt($: any, surface: string, id: string, origin = 'composer') {
  return $.ui.mount({
    plugin: PLUGIN,
    surface,
    component: 'UserMessage',
    requestId: id,
    props: { text: 'what changed?', origin: { kind: origin }, isExpanded: true },
    viewport: { columns: 90, rows: 40 },
  } as never)
}

async function svgOf(ui: any): Promise<string | undefined> {
  return (await ui.find({ type: 'Svg' }))?.props.source as string | undefined
}

const animates = (svg: string | undefined) => (svg ?? '').includes('<animate')

describe('the lane under an ox tool row', () => {
  for (const surface of SURFACES) {
    test(`${surface}: an ox Bash call animates while in flight, then the settle timer redraws it still`, async ($, on) => {
      const s = mockEngine(on)
      s.answers.tu1 = { result: { stdout: PRIME, stderr: '', interrupted: false }, text: PRIME }
      await $.tool.call({ tool: 'Bash', command: 'ox agent prime', tool_use_id: 'tu1' } as never)
      const ui = await mountTool($, surface, 'tu1', 'Bash', { command: 'ox agent prime' })
      const first = await svgOf(ui)
      expect(first).toBeDefined()
      expect(animates(first)).toBe(true)
      expect(first).toContain('class="dot-in"')
      const lane = await ui.find({ type: 'Svg' })
      expect(lane?.props.alt).toMatch(/^loaded team context: ~?4\.8k tokens/)
      expect(lane?.props.width).toBe(laneWidth({ columns: 90 }))
      // The engine's own row stays, above the lane.
      expect((await ui.find({ type: 'Text', text: 'engine' }))).toBeDefined()

      await s.clock.advance(ANIM_MS + 100)
      const settled = await svgOf(ui)
      expect(settled).toBeDefined()
      expect(animates(settled)).toBe(false)
      expect(settled).toContain('class="flow"')
      await ui.unmount()
    })

    test(`${surface}: a SageOx MCP Search call draws a lane with its items`, async ($, on) => {
      const s = mockEngine(on)
      s.answers.mc1 = { result: [{ type: 'text', text: SEARCH_OUT }], text: SEARCH_OUT }
      await $.tool.call({ tool: MCP_SEARCH, tool_use_id: 'mc1', query: 'release' } as never)
      const ui = await mountTool($, surface, 'mc1', MCP_SEARCH, { query: 'release' })
      const lane = await ui.find({ type: 'Svg' })
      expect(lane).toBeDefined()
      expect(String(lane?.props.alt)).toMatch(/^searched team memory: /)
      expect(animates(lane?.props.source as string)).toBe(true)
      await ui.unmount()
    })

    test(`${surface}: a failed ox call draws the stub and says failed`, async ($, on) => {
      const s = mockEngine(on)
      s.answers.f1 = { isError: true, result: 'boom', text: 'Exit code 1\nboom' }
      await $.tool.call({ tool: 'Bash', command: 'ox query "release"', tool_use_id: 'f1' } as never)
      const ui = await mountTool($, surface, 'f1', 'Bash', { command: 'ox query "release"' }, { isErrored: true })
      await s.clock.advance(ANIM_MS + 100)
      const lane = await ui.find({ type: 'Svg' })
      expect(String(lane?.props.alt)).toContain('failed')
      expect(lane?.props.source as string).not.toContain('<animate')
      expect(lane?.props.source as string).not.toContain('class="flow"')
      expect(lane?.props.source as string).toContain('<tspan class="bad">failed</tspan>')
      await ui.unmount()
    })

    test(`${surface}: an outbound murmur draws hollow rings travelling right to left`, async ($, on) => {
      const s = mockEngine(on)
      s.answers.o1 = { result: { stdout: 'posted murmur m9 to the team', stderr: '', interrupted: false }, text: 'posted murmur m9 to the team' }
      await $.tool.call({ tool: 'Bash', command: 'ox murmur --topic=wip "x"', tool_use_id: 'o1' } as never)
      const ui = await mountTool($, surface, 'o1', 'Bash', { command: 'ox murmur --topic=wip "x"' })
      const svg = (await svgOf(ui)) ?? ''
      expect(svg).toContain('.dot-out{fill:none')
      expect(svg).toContain('<circle class="dot-out"')
      expect(svg).not.toContain('class="dot-in"')
      const m = svg.match(/attributeName="cx" values="(\d+);(\d+)"/)
      expect(m).not.toBeNull()
      expect(Number(m![1])).toBeGreaterThan(Number(m![2]))
      await ui.unmount()
    })

    test(`${surface}: a non-ox Bash call and a row with no event draw the engine's row untouched`, async ($, on) => {
      mockEngine(on)
      await $.tool.call({ tool: 'Bash', command: 'ls -la', tool_use_id: 'n1' } as never)
      for (const id of ['n1', 'never-called']) {
        const ui = await mountTool($, surface, id, 'Bash', { command: 'ls -la' })
        expect(await ui.find({ type: 'Svg' })).toBeUndefined()
        expect(await ui.drawn()).toMatchObject({ type: 'Text' })
        await ui.unmount()
      }
    })

    test(`${surface}: a row drawn off screen is drawn still and starts no timer`, async ($, on) => {
      mockEngine(on)
      await $.tool.call({ tool: 'Bash', command: 'ox agent prime', tool_use_id: 'os1' } as never)
      const ui = await mountTool($, surface, 'os1', 'Bash', { command: 'ox agent prime' }, { onScreen: null })
      expect(await svgOf(ui)).toBeDefined()
      expect(animates(await svgOf(ui))).toBe(false)
      await ui.unmount()
    })
  }

  test('terminal: the same ox call draws no lane', async ($, on) => {
    mockEngine(on)
    await $.tool.call({ tool: 'Bash', command: 'ox agent prime', tool_use_id: 't1' } as never)
    const ui = await $.ui.mount({
      plugin: PLUGIN,
      surface: 'terminal',
      component: 'ToolUse',
      requestId: 't1',
      props: { ...toolRow('Bash', { command: 'ox agent prime' }), tool_use_id: 't1' },
    } as never)
    expect(await ui.drawn()).toMatchObject({ type: 'Text' })
    expect(await ui.find({ type: 'Box', key: 'ox-flow' })).toBeUndefined()
    await ui.unmount()
  })

  test('the call is never altered: what the tool answered comes back as answered', async ($, on) => {
    const s = mockEngine(on)
    s.answers.r1 = { result: { stdout: 'x', stderr: '', interrupted: false }, text: 'x' }
    const ran = await $.tool.call({ tool: 'Bash', command: 'ox query "x"', tool_use_id: 'r1' } as never)
    expect((ran as { result: unknown }).result).toEqual({ stdout: 'x', stderr: '', interrupted: false })
  })
})

describe('/ox-session-report flow', () => {
  const COMMAND = { command: 'ox-session-report', origin: { kind: 'composer' }, presentation: { isFullscreen: true, columns: 160 }, surface: 'desktop' } as const

  test('flow off: the row passes through untouched and nothing is recorded', async ($, on) => {
    const s = mockEngine(on)
    expect((await $.command.run({ ...COMMAND, args: 'flow off' } as never)).text).toBe('ox flow lanes off.')
    expect(s.store.flow).toBe(false)
    await $.tool.call({ tool: 'Bash', command: 'ox agent prime', tool_use_id: 'off1' } as never)
    const ui = await mountTool($, 'desktop', 'off1', 'Bash', { command: 'ox agent prime' })
    expect(await ui.drawn()).toMatchObject({ type: 'Text' })
    expect(s.seen.filter(x => x.component === 'ToolUse')).toHaveLength(1)
    await ui.unmount()

    // Back on: lanes draw again for the next call.
    expect((await $.command.run({ ...COMMAND, args: 'flow on' } as never)).text).toMatch(/^ox flow lanes on/)
    expect(s.store.flow).toBe(true)
    await $.tool.call({ tool: 'Bash', command: 'ox agent prime', tool_use_id: 'on1' } as never)
    const again = await mountTool($, 'desktop', 'on1', 'Bash', { command: 'ox agent prime' })
    expect(await svgOf(again)).toBeDefined()
    await again.unmount()
  })

  test('flow still: no animation even while in flight', async ($, on) => {
    const s = mockEngine(on)
    await $.command.run({ ...COMMAND, args: 'flow still' } as never)
    expect(s.store.flow).toBe('still')
    s.answers.st1 = { result: { stdout: PRIME, stderr: '', interrupted: false }, text: PRIME }
    await $.tool.call({ tool: 'Bash', command: 'ox agent prime', tool_use_id: 'st1' } as never)
    const ui = await mountTool($, 'desktop', 'st1', 'Bash', { command: 'ox agent prime' })
    const svg = await svgOf(ui)
    expect(svg).toContain('class="flow"')
    expect(animates(svg)).toBe(false)
    await ui.unmount()
  })

  test('a stored setting is read at the first draw; an unknown word prints usage', async ($, on) => {
    mockEngine(on, { flow: false })
    await $.tool.call({ tool: 'Bash', command: 'ox agent prime', tool_use_id: 'pre1' } as never)
    const ui = await mountTool($, 'desktop', 'pre1', 'Bash', { command: 'ox agent prime' })
    expect(await ui.find({ type: 'Svg' })).toBeUndefined()
    await ui.unmount()
    expect((await $.command.run({ ...COMMAND, args: 'flow sideways' } as never)).text).toMatch(/^Usage: /)
    expect((await $.command.run({ ...COMMAND, args: 'flow' } as never)).text).toMatch(/^ox flow lanes are off\./)
  })
})

describe('the strip under a prompt that carried team context in', () => {
  for (const surface of SURFACES) {
    test(`${surface}: SessionStart prime arriving before the first prompt row draws lands under that row`, async ($, on) => {
      mockEngine(on)
      await $.prompt.submit({ text: 'what changed?', wait: false } as never)
      await $.prompt.attachment({ type: 'hook_additional_context', text: PRIME, origin: { kind: 'hook', event: 'SessionStart' } } as never)
      const ui = await mountPrompt($, surface, 'msg1')
      const strip = await ui.find({ type: 'Svg' })
      expect(strip).toBeDefined()
      expect(String(strip?.props.alt)).toMatch(/^with this prompt: team context/)
      expect(animates(strip?.props.source as string)).toBe(true)
      await ui.unmount()
    })

    test(`${surface}: a UserPromptSubmit murmur block arriving after the row drew lands under it`, async ($, on) => {
      const s = mockEngine(on)
      await $.prompt.submit({ text: 'what changed?', wait: false } as never)
      const ui = await mountPrompt($, surface, 'msg2')
      expect(await ui.find({ type: 'Svg' })).toBeUndefined()
      await $.prompt.attachment({ type: 'hook_additional_context', text: MURMURS, origin: { kind: 'hook', event: 'UserPromptSubmit' } } as never)
      const strip = await ui.find({ type: 'Svg' })
      expect(String(strip?.props.alt)).toBe('with this prompt: 2 murmurs')
      await s.clock.advance(ANIM_MS + 100)
      expect(animates(await svgOf(ui))).toBe(false)
      await ui.unmount()
    })
  }

  test('each prompt keeps its own context; an older row and a non-composer row claim nothing', async ($, on) => {
    mockEngine(on)
    await $.prompt.submit({ text: 'one', wait: false } as never)
    await $.prompt.attachment({ type: 'hook_additional_context', text: PRIME, origin: { kind: 'hook', event: 'SessionStart' } } as never)
    const one = await mountPrompt($, 'desktop', 'p1')
    expect(String((await one.find({ type: 'Svg' }))?.props.alt)).toMatch(/team context/)

    await $.prompt.submit({ text: 'two', wait: false } as never)
    await $.prompt.attachment({ type: 'hook_additional_context', text: MURMURS, origin: { kind: 'hook', event: 'UserPromptSubmit' } } as never)
    // The old row redraws first: it keeps only its own context.
    await one.redraw()
    expect(String((await one.find({ type: 'Svg' }))?.props.alt)).not.toMatch(/murmur/)
    const note = await mountPrompt($, 'desktop', 'n1', 'task-notification')
    expect(await note.find({ type: 'Svg' })).toBeUndefined()
    const two = await mountPrompt($, 'desktop', 'p2')
    expect(String((await two.find({ type: 'Svg' }))?.props.alt)).toBe('with this prompt: 2 murmurs')
    for (const ui of [one, note, two]) {
      await ui.unmount()
    }
  })

  test('engine and plugin attachments, local nudges and the terminal draw nothing', async ($, on) => {
    mockEngine(on)
    await $.prompt.submit({ text: 'x', wait: false } as never)
    await $.prompt.attachment({ type: 'todo_reminder', text: PRIME, origin: { kind: 'engine' } } as never)
    await $.prompt.attachment({ type: 'hook_additional_context', text: '[ox] run ox agent prime', origin: { kind: 'hook', event: 'UserPromptSubmit' } } as never)
    const ui = await mountPrompt($, 'desktop', 'q1')
    expect(await ui.find({ type: 'Svg' })).toBeUndefined()
    await ui.unmount()

    await $.prompt.submit({ text: 'y', wait: false } as never)
    await $.prompt.attachment({ type: 'hook_additional_context', text: MURMURS, origin: { kind: 'hook', event: 'UserPromptSubmit' } } as never)
    const term = await $.ui.mount({
      plugin: PLUGIN,
      surface: 'terminal',
      component: 'UserMessage',
      requestId: 'q2',
      props: { text: 'y', origin: { kind: 'composer' }, isExpanded: true },
    } as never)
    expect(await term.drawn()).toMatchObject({ type: 'Text' })
    await term.unmount()
  })

  test('the attachment text the model reads passes through untouched', async ($, on) => {
    mockEngine(on)
    const out = await $.prompt.attachment({ type: 'hook_additional_context', text: MURMURS, origin: { kind: 'hook', event: 'UserPromptSubmit' } } as never)
    expect(out.text).toBe(MURMURS)
  })
})

describe('tool groups', () => {
  const group = (calls: Array<Record<string, unknown>>, isExpanded = false) => ({ calls, isActive: false, isExpanded, isCallsOnly: true })
  const call = (tool: string, input: Record<string, unknown>) => ({ tool, input, isRunning: false, isErrored: false, isInterrupted: false })

  for (const surface of SURFACES) {
    test(`${surface}: a folded group holding an ox call unfolds; one without does not`, async ($, on) => {
      const s = mockEngine(on)
      const withOx = await $.ui.mount({
        plugin: PLUGIN,
        surface,
        component: 'ToolGroup',
        props: group([call('Read', { file_path: '/a' }), call('Bash', { command: 'ox query "x"' })]),
      } as never)
      const withMcp = await $.ui.mount({ plugin: PLUGIN, surface, component: 'ToolGroup', props: group([call(MCP_SEARCH, { query: 'x' })]) } as never)
      const plain = await $.ui.mount({ plugin: PLUGIN, surface, component: 'ToolGroup', props: group([call('Bash', { command: 'ls' }), call('mcp__github__create_issue', {})]) } as never)
      const groups = s.seen.filter(x => x.component === 'ToolGroup').map(x => x.props.isExpanded)
      expect(groups).toEqual([true, true, false])
      for (const ui of [withOx, withMcp, plain]) {
        await ui.unmount()
      }
    })
  }

  test('terminal: groups stay folded', async ($, on) => {
    const s = mockEngine(on)
    const ui = await $.ui.mount({ plugin: PLUGIN, surface: 'terminal', component: 'ToolGroup', props: group([call('Bash', { command: 'ox query "x"' })]) } as never)
    expect(s.seen.filter(x => x.component === 'ToolGroup').map(x => x.props.isExpanded)).toEqual([false])
    await ui.unmount()
  })
})

describe('flowwire helpers', () => {
  test('laneWidth: cells to px, clamped; the default when unmeasured', () => {
    const cases: Array<[{ columns?: number } | undefined, number]> = [
      [undefined, LANE_DEFAULT_PX],
      [{}, LANE_DEFAULT_PX],
      [{ columns: 0 }, LANE_DEFAULT_PX],
      [{ columns: 10 }, LANE_MIN_PX],
      [{ columns: 80 }, 624],
      [{ columns: 400 }, LANE_MAX_PX],
    ]
    for (const [vp, want] of cases) {
      expect([vp, laneWidth(vp)]).toEqual([vp, want])
    }
  })

  test('settleDelay: until the latest finished event settles; none while running or settled', () => {
    const ev = (endedAt?: number) => ({ id: 'x', lane: 'cloud' as const, direction: 'in' as const, label: 'l', step: 's', tokens: 1, startedAt: 0, endedAt })
    expect(settleDelay([ev(undefined)], 1000)).toBeNull()
    expect(settleDelay([ev(0)], ANIM_MS + 1)).toBeNull()
    expect(settleDelay([ev(1000)], 1000)).toBe(ANIM_MS + 50)
    expect(settleDelay([ev(0), ev(1000)], 1500)).toBe(ANIM_MS - 500 + 50)
  })

  test('the binder: both arrival orders, redraws and repeats', () => {
    const b = newBinder<string>()
    submitted(b)
    // Before the row: pending, claimed by the first composer row drawn.
    expect(arrived(b, 'A', 'a')).toBeNull()
    expect(drawn(b, 'other', false)).toEqual([])
    expect(drawn(b, 'm1', true)).toEqual(['a'])
    // After the row: straight to it; a repeat of the same text is taken once.
    expect(arrived(b, 'B', 'b')).toBe('m1')
    expect(arrived(b, 'B', 'b')).toBeNull()
    // A redraw claims nothing; the next submit opens a new window.
    expect(drawn(b, 'm1', true)).toEqual([])
    submitted(b)
    expect(arrived(b, 'C', 'c')).toBeNull()
    expect(drawn(b, 'm1', true)).toEqual([])
    expect(drawn(b, 'm2', true)).toEqual(['c'])
    // The same text on a later prompt is that prompt's own.
    expect(arrived(b, 'B', 'b')).toBe('m2')
  })

  test('an outbound call counts what it sent, never the acknowledgement', () => {
    const body = 'x'.repeat(4000)
    const bash = startOf('Bash', { command: `ox murmur --topic=wip "${body}"` })!
    expect(finishOf({ text: 'ok' }, bash).tokens).toBeGreaterThan(1000)
    const mcp = startOf(MCP_PUBLISH, { body })!
    expect(finishOf({ text: 'ok' }, mcp).tokens).toBeGreaterThan(1000)
    // An inbound call still counts its result.
    const query = startOf('Bash', { command: 'ox query "x"' })!
    expect(query.sent).toBeUndefined()
    expect(finishOf({ text: 'abcdefgh' }, query).tokens).toBe(2)
    expect(finishOf({ text: 'ok', isError: true }, bash)).toEqual({ tokens: 0, failed: true })
  })
})
