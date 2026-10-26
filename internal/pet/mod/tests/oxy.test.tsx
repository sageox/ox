import { describe, expect, mock, test } from 'claude-code/testing'

import { FRAMES } from '../hooks/frames'
import {
  ANIMS,
  IDLE_AFTER_MS,
  SIZES,
  SLEEP_AFTER_MS,
  cellsFile,
  classifyToolCall,
  formatDuration,
  frameAt,
  frameFile,
  passMs,
  pickAnim,
  planAnim,
  type Anim,
  type Cells,
  type SizeName,
  type SpinnerState,
} from '../hooks/logic'

const SPINNER = {
  plugin: 'ox-cli-oxy',
  surface: 'terminal',
  component: 'Spinner',
  requestId: 'main',
} as const

const COMMAND = { command: 'oxy', origin: 'user', presentation: 'inline', cwd: '/', surface: 'terminal', isInteractive: true } as const

describe('classifyToolCall', () => {
  const cases: Array<[string, string, Record<string, unknown>, string | null]> = [
    ['ox agent prime', 'Bash', { command: 'ox agent prime' }, 'prime'],
    ['ox prime', 'Bash', { command: 'ox prime --json' }, 'prime'],
    ['chained query', 'Bash', { command: 'cd a && ox query "what did we decide"' }, 'query'],
    ['conversation search', 'Bash', { command: 'ox conversation search --participant Sam' }, 'query'],
    ['plan save', 'Bash', { command: 'ox plan save --file plan.html' }, 'plan-checklist'],
    ['murmur', 'Bash', { command: 'ox murmur --topic=wip "a && ox plan"' }, 'murmur'],
    ['ox doctor', 'Bash', { command: 'ox doctor --fix' }, 'doctor'],
    ['claude doctor', 'Bash', { command: 'claude doctor' }, 'doctor'],
    ['built binary', 'Bash', { command: './ox query x' }, 'query'],
    ['env prefix', 'Bash', { command: 'OX_DEBUG=1 ox plan enrich --json' }, 'plan-checklist'],
    ['config flag value is not the subcommand', 'Bash', { command: 'ox --config /tmp/ox.json query auth' }, 'query'],
    ['short config flag', 'Bash', { command: 'ox -c ox.json murmur "hi"' }, 'murmur'],
    ['=-form flag', 'Bash', { command: 'ox --config=/tmp/ox.json prime' }, 'prime'],
    ['ox in a string only', 'Bash', { command: 'echo "ox query"' }, null],
    ['other ox command', 'Bash', { command: 'ox status' }, null],
    ['not ox', 'Bash', { command: 'ls -la' }, null],
    ['oxford is not ox', 'Bash', { command: 'oxford query' }, null],
    ['edit', 'Edit', { file_path: 'a.go' }, 'laptop'],
    ['write', 'Write', { file_path: 'a.go' }, 'laptop'],
    ['mcp prime', 'mcp__claude_ai_sageox__Prime', {}, 'prime'],
    ['mcp search', 'mcp__claude_ai_sageox__Search', {}, 'query'],
    ['mcp plan', 'mcp__claude_ai_sageox__EnrichPlan', {}, 'plan-checklist'],
    ['mcp murmur', 'mcp__claude_ai_sageox__PublishMurmur', {}, 'murmur'],
    ['other mcp server', 'mcp__github__Search', {}, null],
    ['read peers through the glass', 'Read', { file_path: 'a.go' }, 'query'],
    ['grep peers through the glass', 'Grep', { pattern: 'BoxFor' }, 'query'],
    ['glob peers through the glass', 'Glob', { pattern: '**/*.go' }, 'query'],
    ['an unknown tool is left to the spinner', 'TaskStop', {}, null],
    ['web search pops tabs', 'WebSearch', { query: 'kitty graphics' }, 'laptop-tabs'],
    ['web fetch pops tabs', 'WebFetch', { url: 'https://example.com' }, 'laptop-tabs'],
    ['go test swats a bug', 'Bash', { command: 'go test ./internal/pet/...' }, 'laptop-bug'],
    ['make test swats a bug', 'Bash', { command: 'cd ox && make test' }, 'laptop-bug'],
    ['make test-preflight swats a bug', 'Bash', { command: 'make test-preflight' }, 'laptop-bug'],
    ['python -m pytest is left to the spinner', 'Bash', { command: 'python3 -m pytest -q' }, null],
    ['pytest binary swats a bug', 'Bash', { command: 'pytest -q tests/' }, 'laptop-bug'],
    ['go build is not a test', 'Bash', { command: 'go build ./...' }, null],
    ['git push launches', 'Bash', { command: 'git pull --rebase && git push' }, 'laptop-deploy'],
    ['gh pr create launches', 'Bash', { command: 'gh pr create --fill' }, 'laptop-deploy'],
    ['gh release create launches', 'Bash', { command: 'gh release create v1.0.0' }, 'laptop-deploy'],
    ['git status is quiet', 'Bash', { command: 'git status' }, null],
    ['gh pr view is quiet', 'Bash', { command: 'gh pr view 12' }, null],
  ]
  for (const [name, tool, input, want] of cases) {
    test(name, () => {
      expect(classifyToolCall(tool, input, 0)).toBe(want)
    })
  }
})

test('a plan gets one of the three whiteboards', () => {
  expect(planAnim(0)).toBe('plan-checklist')
  expect(planAnim(0.5)).toBe('plan-flowchart')
  expect(planAnim(0.99)).toBe('plan-kanban')
  expect(classifyToolCall('Bash', { command: 'ox plan enrich' }, 0.99)).toBe('plan-kanban')
})

describe('pickAnim', () => {
  const base: SpinnerState = { now: 100_000, mode: 'thinking', message: null, oneShot: null, activities: [], pinned: null }
  const cases: Array<[string, Partial<SpinnerState>, string]> = [
    ['thinking', {}, 'think'],
    ['requesting', { mode: 'requesting' }, 'think'],
    ['responding', { mode: 'responding' }, 'chatter'],
    ['drafting a tool call wags', { mode: 'tool-input' }, 'wag'],
    ['an ordinary tool may use the laptop', { mode: 'tool-use', activities: [{ anim: null, since: 99_000 }] }, 'laptop'],
    ['or the robot', { mode: 'tool-use', activities: [{ anim: null, since: 99_001 }] }, 'robot'],
    ['or the soccer ball', { mode: 'tool-use', activities: [{ anim: null, since: 99_002 }] }, 'juggle'],
    ['or a big tail wag', { mode: 'tool-use', activities: [{ anim: null, since: 99_003 }] }, 'wag-big'],
    ['or the handheld', { mode: 'tool-use', activities: [{ anim: null, since: 99_004 }] }, 'game'],
    ['or the jump that bumps the ? block', { mode: 'tool-use', activities: [{ anim: null, since: 99_005 }] }, 'jump'],
    ['a tool in flight with no record yet keeps the laptop', { mode: 'tool-use' }, 'laptop'],
    ['ox query', { mode: 'tool-use', activities: [{ anim: 'query', since: 99_000 }] }, 'query'],
    ['edit', { mode: 'tool-use', activities: [{ anim: 'laptop', since: 99_000 }] }, 'laptop'],
    ['long tool brings the robot', { mode: 'tool-use', activities: [{ anim: null, since: 100_000 - IDLE_AFTER_MS - 4 }] }, 'robot'],
    ['a long tool may bring the handheld', { mode: 'tool-use', activities: [{ anim: null, since: 100_000 - IDLE_AFTER_MS - 3 }] }, 'game'],
    ['or the soccer ball while it waits', { mode: 'tool-use', activities: [{ anim: null, since: 100_000 - IDLE_AFTER_MS - 2 }] }, 'juggle'],
    ['or a stuck progress bar', { mode: 'tool-use', activities: [{ anim: null, since: 100_000 - IDLE_AFTER_MS - 1 }] }, 'laptop-loading'],
    ['a very long tool puts him to sleep', { mode: 'tool-use', activities: [{ anim: null, since: 100_000 - SLEEP_AFTER_MS - 2 }] }, 'sleep'],
    ['or dozing at his laptop', { mode: 'tool-use', activities: [{ anim: null, since: 100_000 - SLEEP_AFTER_MS - 1 }] }, 'laptop-doze'],
    ['long ox command keeps its animation', { mode: 'tool-use', activities: [{ anim: 'plan-kanban', since: 0 }] }, 'plan-kanban'],
    ['retrying floats', { message: 'Retrying in 5s' }, 'float'],
    ['a todo is not trouble', { message: 'Running tests' }, 'think'],
    ['wave at turn start', { oneShot: { anim: 'wave', start: 99_900, until: 100_500 } }, 'wave'],
    ['finished one-shot', { oneShot: { anim: 'fail', start: 0, until: 1 } }, 'think'],
    ['pinned wins', { pinned: 'murmur', message: 'Retrying' }, 'murmur'],
    ['unknown mode', { mode: 'something-new' }, 'blink'],
  ]
  for (const [name, patch, want] of cases) {
    test(name, () => {
      expect(pickAnim({ ...base, ...patch })).toBe(want)
    })
  }
})

test('every animation has a positive duration for each frame', () => {
  for (const name of ANIMS) {
    const a = FRAMES.animations[name]
    expect(a.ms.length).toBeGreaterThan(0)
    expect(a.ms.every(ms => ms > 0)).toBe(true)
  }
})

// A well-formed cells payload for one frame at one size (Raster wants
// columns * rows little-endian u32 triplets, base64), coloured so each
// animation, size and frame comes out distinct.
function fakeCells(name: Anim, size: SizeName, index: number): string {
  const { columns, rows } = FRAMES.sizes[size].cells
  const bytes = new Uint8Array(columns * rows * 12)
  const view = new DataView(bytes.buffer)
  const tint = (ANIMS.indexOf(name) + 1) * 0x010000 + SIZES.indexOf(size) * 0x0100 + (index % 256)
  for (let c = 0; c < columns * rows; c++) {
    view.setUint32(c * 12, 0x2580, true)
    view.setUint32(c * 12 + 4, tint, true)
    view.setUint32(c * 12 + 8, 0x01000000, true)
  }
  return btoa(String.fromCharCode(...bytes))
}

// The engine beneath the tests has no file system, so a test answers the
// mod's reads of its own cells.json files with fakeCells. (The Go tests check
// the real files `ox pet enable` writes.)
function mockFrameFiles(on: Parameters<Parameters<typeof test>[1]>[1]) {
  on('fs.read', (_, e) => {
    const m = /\/hooks\/frames\/([^/]+)\/cells\.json$/.exec(e.path)
    if (!m) {
      return { deny: `no such file: ${e.path}` }
    }
    const name = m[1] as Anim
    const n = FRAMES.animations[name].ms.length
    const cells = Object.fromEntries(SIZES.map(size => [size, Array.from({ length: n }, (_, i) => fakeCells(name, size, i))])) as Cells
    return { value: JSON.stringify(cells) }
  })
}

test('frame and cells files sit under the mod folder, per animation', () => {
  expect(frameFile('/mod', 'think', 3)).toBe('/mod/hooks/frames/think/3.png')
  expect(cellsFile('/mod', 'plan-kanban')).toBe('/mod/hooks/frames/plan-kanban/cells.json')
})

test('sizes grow with the art, in rows', () => {
  const { small, medium, large } = FRAMES.sizes
  expect([small.rows, medium.rows, large.rows]).toEqual([8, 12, 16])
  for (const box of [small, medium, large]) {
    // half-block cells: two pixels per cell, the art edge to edge
    expect(box.cells).toEqual({ columns: Math.max(1, Math.round(box.rows * 2 * FRAMES.aspect)), rows: box.rows })
    // the picture box fits a cell 2.25x as tall as wide, so the art fills its
    // height in Ghostty and no gap is left under Oxy
    expect(box.columns).toBe(Math.max(1, Math.ceil(box.rows * 2.25 * FRAMES.aspect)))
    expect(box.columns).toBeGreaterThanOrEqual(box.cells.columns)
  }
})

test('frameAt follows each frame\'s own duration, loops, and holds the last frame of a single pass', () => {
  const a = { ms: [100, 400, 100] }
  expect(passMs(a)).toBe(600)
  expect(frameAt(a, 0, 50, true)).toBe(0)
  expect(frameAt(a, 0, 150, true)).toBe(1)
  expect(frameAt(a, 0, 499, true)).toBe(1)
  expect(frameAt(a, 0, 550, true)).toBe(2)
  expect(frameAt(a, 0, 650, true)).toBe(0)
  expect(frameAt(a, 0, 5_000, false)).toBe(2)
  const still = { ms: [1000] }
  expect(frameAt(still, 0, 50_000, true)).toBe(0)
  expect(frameAt(still, 0, 50_000, false)).toBe(0)
})

test('formatDuration matches the closing line', () => {
  expect(formatDuration(3_000)).toBe('3s')
  expect(formatDuration(64_000)).toBe('1m 4s')
  expect(formatDuration(3_720_000)).toBe('1h 2m')
})

test('the thinking line draws Oxy beside the word, medium by default', async ($, on) => {
  mock.clock(on, { now: 1_000 })
  mockFrameFiles(on)
  const ui = await $.ui.mount({ ...SPINNER, props: { word: 'Sautéing', message: null, suffix: '…', mode: 'thinking' } })
  const raster = await ui.find({ type: 'Raster', key: 'oxy' })
  const grid = FRAMES.sizes.medium.cells
  expect(raster?.props.columns).toBe(grid.columns)
  expect(raster?.props.rows).toBe(grid.rows)
  // the loop started at 0 and it is now 1s in: whichever frame that is
  expect(raster?.props.cells).toBe(fakeCells('think', 'medium', frameAt(FRAMES.animations.think, 0, 1_000, true)))
  // the words sit on the bottom row of Oxy's box
  expect((await ui.find({ type: 'Box', key: 'words' }))?.props.marginTop).toBe(grid.rows - 1)
  expect((await ui.find({ type: 'Text', text: /Sautéing/ }))?.text).toBe('Sautéing…')
  await ui.unmount()
})

test('an ox query plays the query animation in the thinking line', async ($, on) => {
  mock.clock(on, { now: 1_000 })
  mockFrameFiles(on)
  let release = () => {}
  on('tool.call', async () => {
    await new Promise<void>(resolve => {
      release = resolve
    })
    return { result: 'ok' }
  })
  const call = $.tool.call({ tool: 'Bash', command: 'cd repo && ox query "auth decisions"' } as never)
  const ui = await $.ui.mount({ ...SPINNER, props: { word: 'Sautéing', message: null, suffix: '…', mode: 'tool-use' } })
  const raster = await ui.find({ type: 'Raster', key: 'oxy' })
  expect(raster?.props.cells).toBe(fakeCells('query', 'medium', frameAt(FRAMES.animations.query, 0, 1_000, true)))
  release()
  await call
  await ui.unmount()
})

test('one failed tool trips Oxy; a second in a row has him smash his laptop', async ($, on) => {
  mock.clock(on, { now: 1_000 })
  mockFrameFiles(on)
  on('turn.start', (_, e) => ({ turnId: e.turnId }))
  on('tool.call', () => ({ result: 'boom', isError: true }))
  await $.turn.start({ turnId: 't1' } as never)
  const spinner = { ...SPINNER, props: { word: 'Sautéing', message: null, suffix: '…', mode: 'tool-use' } } as const

  await $.tool.call({ tool: 'Bash', command: 'false' } as never)
  let ui = await $.ui.mount(spinner)
  expect((await ui.find({ type: 'Raster', key: 'oxy' }))?.props.cells).toBe(fakeCells('fail', 'medium', 0))
  await ui.unmount()

  await $.tool.call({ tool: 'Bash', command: 'false' } as never)
  ui = await $.ui.mount(spinner)
  expect((await ui.find({ type: 'Raster', key: 'oxy' }))?.props.cells).toBe(fakeCells('laptop-rage', 'medium', 0))
  await ui.unmount()
})

test('the turn closing line throws confetti', async ($, on) => {
  mock.clock(on, { now: 5_000 })
  mockFrameFiles(on)
  on('turn.complete', () => ({ text: '' }))
  await $.turn.complete({ answer: '', durationMs: 3_000, isAborted: false, turnId: 't1', reason: 'answer' })
  const ui = await $.ui.mount({
    plugin: 'ox-cli-oxy',
    surface: 'terminal',
    component: 'TurnDuration',
    requestId: 'turn-1',
    props: { word: 'Baked', durationMs: 3_000 },
  })
  const raster = await ui.find({ type: 'Raster', key: 'oxy' })
  expect(raster?.props.cells).toBe(fakeCells('confetti', 'medium', 0))
  expect((await ui.find({ type: 'Text', text: /Baked/ }))?.text).toBe('Baked for 3s')
  await ui.unmount()
})

test('/oxy large draws a bigger Oxy and remembers it', async ($, on) => {
  mock.clock(on, { now: 1_000 })
  mock.store(on)
  mockFrameFiles(on)
  on('command.run', () => ({ text: '' }))
  const ran = await $.command.run({ ...COMMAND, args: 'large' } as never)
  expect(ran.text).toContain('large')
  await $.command.run({ ...COMMAND, args: 'image' } as never)
  const ui = await $.ui.mount({ ...SPINNER, props: { word: 'Sautéing', message: null, suffix: '…', mode: 'thinking' } })
  const image = await ui.find({ type: 'Image', key: 'oxy' })
  const box = FRAMES.sizes.large
  expect(image?.props.columns).toBe(box.columns)
  expect(image?.props.rows).toBe(box.rows)
  // the picture is a file the terminal opens itself, not bytes the mod holds
  const source = image?.props.source as { file?: string; format?: string } | undefined
  expect(source?.format).toBe('png')
  expect(source?.file).toMatch(/\/hooks\/frames\/think\/\d+\.png$/)
  expect((await ui.find({ type: 'Box', key: 'words' }))?.props.marginTop).toBe(box.rows - 1)
  await ui.unmount()
  await $.command.run({ ...COMMAND, args: 'medium' } as never)
})

test('/oxy off hides Oxy until /oxy hatches him again', async ($, on) => {
  mock.clock(on, { now: 1_000 })
  mock.store(on)
  mockFrameFiles(on)
  on('command.run', () => ({ text: '' }))
  on('ui.render', ($, e) => {
    const { Text } = $.ui.resolve(e)
    return <Text>engine spinner</Text>
  })
  expect((await $.command.run({ ...COMMAND, args: 'off' } as never)).text).toContain('hidden')
  let ui = await $.ui.mount({ ...SPINNER, props: { word: 'Working', message: null, suffix: '…', mode: 'thinking' } })
  expect(await ui.find({ type: 'Raster' })).toBeUndefined()
  expect(await ui.find({ type: 'Text', text: 'engine spinner' })).toBeDefined()
  await ui.unmount()

  expect((await $.command.run({ ...COMMAND, args: '' } as never)).text).toContain('hatched')
  ui = await $.ui.mount({ ...SPINNER, props: { word: 'Working', message: null, suffix: '…', mode: 'thinking' } })
  expect(await ui.find({ type: 'Raster', key: 'oxy' })).toBeDefined()
  await ui.unmount()
  expect((await $.command.run({ ...COMMAND, args: '' } as never)).text).toContain('already here')
})

test('/oxy remembers a hidden Oxy across sessions', async ($, on) => {
  mock.clock(on, { now: 1_000 })
  mock.store(on, { hidden: true })
  mock.env(on, { TERM_PROGRAM: 'ghostty' })
  on('session.start', () => ({ cwd: '/' }))
  on('command.register', (_, e) => ({ value: { command: e.name } }))
  on('ui.render', ($, e) => {
    const { Text } = $.ui.resolve(e)
    return <Text>engine spinner</Text>
  })
  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  const ui = await $.ui.mount({ ...SPINNER, props: { word: 'Working', message: null, suffix: '…', mode: 'thinking' } })
  // Ghostty draws pictures, so a drawn Oxy would be an Image here, not a Raster.
  expect(await ui.find({ type: 'Image', key: 'oxy' })).toBeUndefined()
  expect(await ui.find({ type: 'Text', text: 'engine spinner' })).toBeDefined()
  await ui.unmount()
})

// A /clear or an in-app resume starts a session with fresh state and no
// session.start: Oxy must still be a picture in Ghostty, at the saved size.
test('a session with no session.start still draws a picture at the saved size', async ($, on) => {
  mock.clock(on, { now: 1_000 })
  mock.store(on, { size: 'large' })
  mock.env(on, { TERM_PROGRAM: 'ghostty' })
  mockFrameFiles(on)
  const ui = await $.ui.mount({ ...SPINNER, props: { word: 'Enchanting', message: null, suffix: '…', mode: 'thinking' } })
  expect(await ui.find({ type: 'Raster' })).toBeUndefined()
  const image = await ui.find({ type: 'Image', key: 'oxy' })
  expect(image?.props.rows).toBe(FRAMES.sizes.large.rows)
  await ui.unmount()
})

test('a non-terminal surface keeps the engine spinner', async ($, on) => {
  on('ui.render', ($, e) => {
    const { Text } = $.ui.resolve(e)
    return <Text>engine spinner</Text>
  })
  const ui = await $.ui.mount({ ...SPINNER, surface: 'desktop', props: { word: 'Working', message: null, suffix: '…', mode: 'thinking' } })
  expect(await ui.find({ type: 'Raster' })).toBeUndefined()
  expect(await ui.find({ type: 'Text', text: 'engine spinner' })).toBeDefined()
  await ui.unmount()
})

test('a pinned one-shot animation keeps looping after its first pass', async ($, on) => {
  const clock = mock.clock(on, { now: 1_000 })
  mock.store(on)
  mockFrameFiles(on)
  on('command.run', () => ({ text: '' }))
  on('turn.start', (_, e) => ({ turnId: e.turnId }))
  await $.turn.start({ turnId: 't1' } as never)
  await $.command.run({ ...COMMAND, args: 'wave' } as never)
  const wave = FRAMES.animations.wave
  // well past one pass: a frozen one-shot would hold the last frame
  const later = 1_000 + passMs(wave) * 3 + wave.ms[0] / 2
  await clock.set(later)
  const ui = await $.ui.mount({ ...SPINNER, props: { word: 'Working', message: null, suffix: '…', mode: 'thinking' } })
  const raster = await ui.find({ type: 'Raster', key: 'oxy' })
  expect(raster?.props.cells).toBe(fakeCells('wave', 'medium', frameAt(wave, 1_000, later, true)))
  expect(raster?.props.cells).not.toBe(fakeCells('wave', 'medium', wave.ms.length - 1))
  await ui.unmount()
  await $.command.run({ ...COMMAND, args: 'auto' } as never)
})

test('a frame that cannot load stops its site instead of retrying forever', async ($, on) => {
  const clock = mock.clock(on, { now: 1_000 })
  let reads = 0
  let broken = false
  on('fs.read', (_, e) => {
    reads++
    if (broken) {
      return { deny: `gone: ${e.path}` }
    }
    const m = /\/hooks\/frames\/([^/]+)\/cells\.json$/.exec(e.path)
    const name = (m?.[1] ?? 'think') as Anim
    const n = FRAMES.animations[name].ms.length
    return { value: JSON.stringify(Object.fromEntries(SIZES.map(size => [size, Array.from({ length: n }, (_, i) => fakeCells(name, size, i))]))) }
  })
  const ui = await $.ui.mount({ ...SPINNER, props: { word: 'Working', message: null, suffix: '…', mode: 'responding' } })
  expect(await ui.find({ type: 'Raster', key: 'oxy' })).toBeDefined()
  // the next animation's cells.json is missing from now on
  broken = true
  await $.command.run({ ...COMMAND, args: 'query' } as never).catch(() => undefined)
  await clock.advance(200)
  const after = reads
  await clock.advance(2_000)
  expect(reads).toBe(after)
  await ui.unmount()
})
