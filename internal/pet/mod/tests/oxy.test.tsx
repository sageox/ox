import { describe, expect, mock, test } from 'claude-code/testing'

import { FRAMES } from '../hooks/frames'
import {
  ANIMS,
  CLIP_IMAGE_MARKUP_MAX,
  CLIP_WRAPPER_MAX,
  IDLE_AFTER_MS,
  SIZES,
  SLEEP_AFTER_MS,
  SVG_SOURCE_CAP,
  cellsFile,
  classifyToolCall,
  clipSvg,
  clipTimings,
  formatDuration,
  frameAt,
  frameFile,
  hiresFrameFile,
  hiresLayout,
  packClips,
  MIN_CLIP_MS,
  passMs,
  pickAnim,
  PIXEL_HEIGHTS,
  pixelBox,
  planAnim,
  ROW_PX,
  type Anim,
  type HiresFrames,
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

test('packClips covers every frame in order and keeps each clip under the cap', () => {
  const cases: Array<[string, number[]]> = [
    ['today\'s frames', Array.from({ length: 34 }, (_, i) => 24_000 + (i % 7) * 1_100)],
    ['tiny frames all fit one clip', [10, 20, 30]],
    ['mixed sizes', [60_000, 10_000, 70_000, 5_000, 5_000, 5_000, 50_000]],
    ['one frame', [24_000]],
    ['no frames', []],
  ]
  for (const [, lengths] of cases) {
    const clips = packClips(lengths.map(() => 40), lengths, SVG_SOURCE_CAP)
    expect(clips.flatMap(([start, end]) => Array.from({ length: end - start }, (_, i) => start + i))).toEqual(lengths.map((_, i) => i))
    for (const [start, end] of clips) {
      expect(end).toBeGreaterThan(start)
      const cost = CLIP_WRAPPER_MAX + lengths.slice(start, end).reduce((n, len) => n + len + CLIP_IMAGE_MARKUP_MAX, 0)
      expect(cost).toBeLessThanOrEqual(SVG_SOURCE_CAP)
    }
  }
  // a frame too big on its own still gets a clip, alone
  expect(packClips([40, 40, 40], [10, SVG_SOURCE_CAP, 10], SVG_SOURCE_CAP)).toEqual([[0, 1], [1, 2], [2, 3]])
  expect(() => packClips([40], [1, 2], SVG_SOURCE_CAP)).toThrow()
})

test('clipSvg stays inside the markup bounds packClips packs by', () => {
  // the widest numbers a frame's timing can print
  const frames = ['QUJD', 'REVG', 'R0hJ', 'SktM']
  for (const ms of [[40, 40, 40, 40], [99_999, 1, 7, 123_456], [3, 1_000_000, 3, 3]]) {
    for (const loop of [true, false]) {
      const svg = clipSvg(frames, ms, loop)
      expect(svg.length).toBeLessThanOrEqual(CLIP_WRAPPER_MAX + frames.reduce((n, f) => n + f.length + CLIP_IMAGE_MARKUP_MAX, 0))
    }
  }
})

test('clipSvg shows each frame for its own time, then loops or freezes', () => {
  const svg = clipSvg(['QUJD', 'REVG', 'R0hJ'], [100, 300, 100], false)
  expect(svg.startsWith('<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 160 207">')).toBe(true)
  expect(svg).toContain('visibility="visible" href="data:image/png;base64,QUJD"')
  expect(svg).toContain('values="visible;hidden" keyTimes="0;0.2" dur="500ms" fill="freeze"')
  expect(svg).toContain('values="hidden;visible;hidden" keyTimes="0;0.2;0.8"')
  // the last frame stays up once the clip has played
  expect(svg).toContain('values="hidden;visible" keyTimes="0;0.8"')
  expect(clipSvg(['QUJD', 'REVG'], [100, 100], true)).toContain('repeatCount="indefinite"')
  // a single frame needs no animation at all
  expect(clipSvg(['QUJD'], [100], true)).not.toContain('<animate')
})

// A frame PNG's bytes as the mod reads them: a stand-in base64 that names
// the animation and frame, padded to `length` so clips pack as real ones do.
function fakePng(name: Anim, index: number, length: number): string {
  const tag = `${name.replace(/-/g, '')}${index}x`
  return tag + 'A'.repeat(Math.max(0, length - tag.length))
}

// Answers frame reads from both sets; a hires frame's stand-in names its set
// so a test can tell which art reached the SVG. `hires: false` denies the
// hires reads, as an install whose hires folder went missing would.
function mockPngFiles(on: Parameters<Parameters<typeof test>[1]>[1], length = 24_000, { hires = true } = {}) {
  const reads: string[] = []
  on('fs.read', (_, e) => {
    const m = /\/hooks\/(frames|hires)\/([^/]+)\/(\d+)\.png$/.exec(e.path)
    if (!m || e.as !== 'bytes' || (m[1] === 'hires' && !hires)) {
      return { deny: `no such file: ${e.path}` }
    }
    reads.push(e.path)
    const tag = m[1] === 'hires' ? 'hi' : ''
    return { value: { base64: tag + fakePng(m[2] as Anim, Number(m[3]), length - tag.length) } }
  })
  return reads
}

// A hires block shaped like the generator's, with numbers chosen so each
// rule shows: Oxy's figure ends on the cell's bottom edge; the laptop widens
// the crop; float's crop stops 100 px above the ground; confetti fills the
// cell. The cell is not the real one on purpose: nothing may assume its size.
const FIGURE = { x: 120, y: 141, width: 240, height: 480 }
const HIRES: HiresFrames = {
  cell: { width: 480, height: 621 },
  scale: 3,
  figure: FIGURE,
  animations: {
    ...Object.fromEntries(ANIMS.map(name => [name, FIGURE])),
    // the hires set's own frame count and timing, unlike the pixel set's
    laptop: { x: 30, y: 141, width: 420, height: 480, ms: Array.from({ length: 45 }, () => 33) },
    float: { x: 120, y: 41, width: 240, height: 480 },
    confetti: { x: 60, y: 0, width: 360, height: 621 },
  },
}

// What the desktop should draw for `anim` at `box`: the hires layout when
// the generated frames.ts carries one for it, else the 160x207 fallback. The
// engine under test loads the mod's own copy of frames.ts, so a test can't
// swap FRAMES.hires; these render tests follow whichever frames.ts is laid.
function desktopLayout(anim: Anim, box: SizeName) {
  const l = FRAMES.hires ? hiresLayout(FRAMES.hires, anim, box) : null
  if (l) {
    const v = l.viewBox
    return { hires: true, viewBox: `${v.x} ${v.y} ${v.width} ${v.height}`, image: l.image, size: { width: l.width, height: l.height }, lift: l.wordsLift }
  }
  return { hires: false, viewBox: '0 0 160 207', image: { x: 0, y: 0, width: 160, height: 207 }, size: pixelBox(box), lift: 0 }
}

const WORKING = { word: 'Working', message: null, suffix: '…', mode: 'thinking' } as const

for (const surface of ['vscode'] as const) {
  test(`${surface}: Oxy is a self-animating SVG clip beside the words, at each size`, async ($, on) => {
    mock.clock(on, { now: 1_000 })
    mock.store(on)
    mockPngFiles(on)
    on('command.run', () => ({ text: '' }))
    // a pinned animation of its own, so no other test's frames are cached
    await $.command.run({ ...COMMAND, surface, args: 'chatter' } as never)
    for (const box of SIZES) {
      await $.command.run({ ...COMMAND, surface, args: box } as never)
      const ui = await $.ui.mount({ ...SPINNER, surface, props: { word: 'Sautéing', message: null, suffix: '…', mode: 'thinking' } })
      const svg = await ui.find({ type: 'Svg' })
      const source = String(svg?.props.source)
      expect(source.startsWith('<svg')).toBe(true)
      const want = desktopLayout('chatter', box)
      expect(source).toContain(`viewBox="${want.viewBox}"`)
      expect(source).toContain(`width="${want.image.width}" height="${want.image.height}"`)
      expect(source).toContain(`data:image/png;base64,${want.hires ? 'hi' : ''}chatter0x`)
      expect((await ui.find({ type: 'Box', key: 'words' }))?.props.marginBottom).toBe(want.lift)
      expect(source).toContain('<animate attributeName="visibility" calcMode="discrete"')
      expect(source.length).toBeLessThanOrEqual(SVG_SOURCE_CAP)
      // A plain image: the desktop's interactive scrub forbids <image> and data: hrefs.
      expect(svg?.props.isInteractive).toBeFalsy()
      expect(svg?.props.alt).toBe('Oxy')
      expect({ width: svg?.props.width, height: svg?.props.height }).toEqual(want.size)
      expect(await ui.find({ type: 'Raster' })).toBeUndefined()
      expect((await ui.find({ type: 'Text', text: /Sautéing/ }))?.text).toBe('Sautéing…')
      await ui.unmount()
    }
    expect([pixelBox('small').height, pixelBox('medium').height, pixelBox('large').height]).toEqual([48, 80, 120])
    await $.command.run({ ...COMMAND, args: 'medium' } as never)
    await $.command.run({ ...COMMAND, args: 'auto' } as never)
  })
}

test('desktop: Oxy lives in the band above the prompt, resting between turns and working during them', async ($, on) => {
  mock.clock(on, { now: 1_000 })
  mock.store(on)
  mockPngFiles(on)
  on('ui.render', ($, e) => {
    const { Text } = $.ui.resolve(e)
    return <Text>engine band</Text>
  })
  const band = (isWorking: boolean) => ({
    plugin: 'ox-cli-oxy',
    surface: 'desktop',
    component: 'AbovePrompt',
    requestId: 'band',
    props: { hasSurvey: false, isWorking, maxRows: 20, bodyColumns: 80, scroll: { offset: 0, bodyRows: 19 }, view: {} },
  })
  const idle = await $.ui.mount(band(false) as never)
  const svg = await idle.find({ type: 'Svg' })
  expect(String(svg?.props.source)).toContain('data:image/png;base64,')
  expect(svg?.props.isInteractive).toBeFalsy()
  // The band follows /oxy's size: medium by default.
  expect(svg?.props.height).toBeGreaterThanOrEqual(PIXEL_HEIGHTS.medium)
  expect(svg?.props.height).toBeLessThan(PIXEL_HEIGHTS.large)
  await idle.unmount()
  const working = await $.ui.mount(band(true) as never)
  expect(String((await working.find({ type: 'Svg' }))?.props.source)).toContain('data:image/png;base64,')
  await working.unmount()
  // The desktop's thinking row is one line tall and clips him: it stays the engine's.
  const row = await $.ui.mount({ ...SPINNER, surface: 'desktop', props: WORKING })
  expect(await row.find({ type: 'Svg' })).toBeUndefined()
  expect(await row.find({ type: 'Text', text: 'engine band' })).toBeDefined()
  await row.unmount()
  await $.command.run({ ...COMMAND, surface: 'desktop', args: 'off' } as never)
  const hidden = await $.ui.mount(band(false) as never)
  expect(await hidden.find({ type: 'Svg' })).toBeUndefined()
  await hidden.unmount()
  await $.command.run({ ...COMMAND, surface: 'desktop', args: 'on' } as never)
})

test('terminal: the band above the prompt stays the engine\'s', async ($, on) => {
  mock.clock(on, { now: 1_000 })
  mock.store(on)
  mockPngFiles(on)
  on('ui.render', ($, e) => {
    const { Text } = $.ui.resolve(e)
    return <Text>engine band</Text>
  })
  const ui = await $.ui.mount({
    plugin: 'ox-cli-oxy',
    surface: 'terminal',
    component: 'AbovePrompt',
    requestId: 'band',
    props: { hasSurvey: false, isWorking: false, maxRows: 20, bodyColumns: 80, scroll: { offset: 0, bodyRows: 19 }, view: {} },
  } as never)
  expect(await ui.find({ type: 'Text', text: 'engine band' })).toBeDefined()
  await ui.unmount()
})

test('desktop: /oxy off keeps the engine spinner', async ($, on) => {
  mock.clock(on, { now: 1_000 })
  mock.store(on)
  mockPngFiles(on)
  on('command.run', () => ({ text: '' }))
  on('ui.render', ($, e) => {
    const { Text } = $.ui.resolve(e)
    return <Text>engine spinner</Text>
  })
  await $.command.run({ ...COMMAND, surface: 'desktop', args: 'off' } as never)
  const ui = await $.ui.mount({ ...SPINNER, surface: 'desktop', props: WORKING })
  expect(await ui.find({ type: 'Svg' })).toBeUndefined()
  expect(await ui.find({ type: 'Text', text: 'engine spinner' })).toBeDefined()
  await ui.unmount()
  await $.command.run({ ...COMMAND, surface: 'desktop', args: 'on' } as never)
})

test('vscode: a frame that cannot be read keeps the engine spinner', async ($, on) => {
  mock.clock(on, { now: 1_000 })
  mock.store(on)
  on('command.run', () => ({ text: '' }))
  on('fs.read', (_, e) => ({ deny: `gone: ${e.path}` }))
  on('ui.render', ($, e) => {
    const { Text } = $.ui.resolve(e)
    return <Text>engine spinner</Text>
  })
  await $.command.run({ ...COMMAND, surface: 'vscode', args: 'doctor' } as never)
  const ui = await $.ui.mount({ ...SPINNER, surface: 'vscode', props: WORKING })
  expect(await ui.find({ type: 'Svg' })).toBeUndefined()
  expect(await ui.find({ type: 'Text', text: 'engine spinner' })).toBeDefined()
  await ui.unmount()
  await $.command.run({ ...COMMAND, args: 'auto' } as never)
})

test('vscode: the ticker redraws once per clip, never per frame, and never blits', async ($, on) => {
  const clock = mock.clock(on, { now: 1_000 })
  mock.store(on)
  // big enough frames that the animation spans several clips
  // Small enough that a clip holds six or seven 33 ms frames: 40,000 packed
  // the 30 fps set three to a clip and left the first clip one frame long,
  // shorter than the 60 ms the test rewinds before the first boundary.
  const length = 18_000
  mockPngFiles(on, length)
  on('command.run', () => ({ text: '' }))
  let invalidates = 0
  let blits = 0
  on('ui.invalidate', (_, e, next) => {
    invalidates++
    return next(e)
  })
  on('ui.blit', () => {
    blits++
    return {}
  })
  const anim: Anim = 'game'
  await $.command.run({ ...COMMAND, surface: 'vscode', args: anim } as never)
  // the timings of whichever art the desktop draws
  const ms = clipTimings(FRAMES, anim, desktopLayout(anim, 'medium').hires)
  const clips = packClips(ms, ms.map(() => length), SVG_SOURCE_CAP)
  expect(clips.length).toBeGreaterThan(1)
  const ui = await $.ui.mount({ ...SPINNER, surface: 'vscode', props: WORKING })
  expect(String((await ui.find({ type: 'Svg' }))?.props.source)).toContain(`${anim}0x`)
  const firstClipMs = ms.slice(clips[0][0], clips[0][1]).reduce((a, b) => a + b, 0)
  // every frame of the first clip changes while it plays: none redraws
  await clock.advance(firstClipMs - 60)
  expect(invalidates).toBe(0)
  await clock.advance(120)
  expect(invalidates).toBe(1)
  expect(String((await ui.find({ type: 'Svg' }))?.props.source)).toContain(`${anim}${clips[1][0]}x`)
  // a whole pass more, watched in 10 ms steps: each redraw comes once the
  // clip on show has played out (within a tick), the loop back included
  const at: number[] = [firstClipMs + 60]
  let seen = invalidates
  for (let t = firstClipMs + 70; t <= firstClipMs + 60 + passMs({ ms }) + clips.length * 60; t += 10) {
    await clock.advance(10)
    if (invalidates !== seen) {
      seen = invalidates
      at.push(t)
    }
  }
  expect(at.length).toBeGreaterThan(clips.length)
  for (let i = 1; i < at.length; i++) {
    const [start, end] = clips[i % clips.length]
    const played = ms.slice(start, end).reduce((a, b) => a + b, 0)
    expect(at[i] - at[i - 1]).toBeGreaterThanOrEqual(played - 60)
    expect(at[i] - at[i - 1]).toBeLessThanOrEqual(played + 60)
  }
  expect(blits).toBe(0)
  await ui.unmount()
  await $.command.run({ ...COMMAND, args: 'auto' } as never)
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

test('hires frame files sit under hooks/hires, per animation', () => {
  expect(hiresFrameFile('/mod', 'laptop', 7)).toBe('/mod/hooks/hires/laptop/7.png')
})

describe('hiresLayout', () => {
  // [anim, size, viewBox, Svg width x height, ground pad px, words lift rows]
  const cases: Array<[Anim, SizeName, string, [number, number], number, number]> = [
    // Oxy alone: his figure is the crop, 80 px tall at medium
    ['blink', 'medium', '120 141 240 480', [40, 80], 0, 2],
    ['blink', 'small', '120 141 240 480', [24, 48], 0, 1],
    ['blink', 'large', '120 141 240 480', [60, 120], 0, 3],
    // the laptop widens the element; Oxy stays 80 px
    ['laptop', 'medium', '30 141 420 480', [70, 80], 0, 2],
    // float's crop ends 100 px short of the ground: the viewBox keeps it
    ['float', 'medium', '120 41 240 580', [40, 97], 16.667, 2],
    // confetti fills the cell: taller than Oxy, same ground
    ['confetti', 'medium', '60 0 360 621', [60, 104], 0, 2],
  ]
  for (const [anim, size, viewBox, [width, height], pad, lift] of cases) {
    test(`${anim} at ${size}`, () => {
      const l = hiresLayout(HIRES, anim, size)
      expect(l).not.toBeNull()
      const v = l!.viewBox
      expect(`${v.x} ${v.y} ${v.width} ${v.height}`).toBe(viewBox)
      // every viewBox ends on the cell's bottom edge, the ground line
      expect(v.y + v.height).toBe(HIRES.cell.height)
      const { x, y, width: w, height: h } = HIRES.animations[anim]!
      expect(l!.image).toEqual({ x, y, width: w, height: h })
      expect([l!.width, l!.height]).toEqual([width, height])
      expect(Math.round(l!.groundPad * 1000) / 1000).toBe(pad)
      expect(l!.wordsLift).toBe(lift)
    })
  }
  test('words lift follows the figure, never a tall prop', () => {
    const medium = hiresLayout(HIRES, 'confetti', 'medium')!
    const middle = (HIRES.cell.height - (FIGURE.y + FIGURE.height / 2)) * (PIXEL_HEIGHTS.medium / FIGURE.height)
    expect(medium.wordsLift).toBe(Math.round((middle - ROW_PX / 2) / ROW_PX))
    expect(hiresLayout(HIRES, 'blink', 'medium')!.wordsLift).toBe(medium.wordsLift)
  })
  test('an animation with no box, or an empty figure, has no layout', () => {
    const { wave: _, ...rest } = HIRES.animations
    expect(hiresLayout({ ...HIRES, animations: rest }, 'wave', 'medium')).toBeNull()
    expect(hiresLayout({ ...HIRES, figure: { ...FIGURE, height: 0 } }, 'blink', 'medium')).toBeNull()
  })
})

test('packClips packs hires-sized frames under the cap', () => {
  // hires frames run to ~31K base64 each: about four to a clip
  for (const n of [1, 4, 5, 34, 60]) {
    const lengths = Array.from({ length: n }, (_, i) => 18_000 + ((i * 2_731) % 13_000))
    const clips = packClips(lengths.map(() => 40), lengths, SVG_SOURCE_CAP)
    expect(clips[0][0]).toBe(0)
    expect(clips.at(-1)![1]).toBe(n)
    for (const [start, end] of clips) {
      expect(end - start).toBeGreaterThanOrEqual(1)
      expect(end - start).toBeLessThanOrEqual(5)
      const cost = CLIP_WRAPPER_MAX + lengths.slice(start, end).reduce((t, len) => t + len + CLIP_IMAGE_MARKUP_MAX, 0)
      expect(cost).toBeLessThanOrEqual(SVG_SOURCE_CAP)
    }
  }
})

test('clipSvg places hires frames at their crop inside a ground-anchored viewBox', () => {
  const l = hiresLayout(HIRES, 'float', 'medium')!
  const frames = ['QUJD', 'REVG']
  const svg = clipSvg(frames, [99_999, 123_456], false, l)
  expect(svg.startsWith('<svg xmlns="http://www.w3.org/2000/svg" viewBox="120 41 240 580">')).toBe(true)
  expect(svg).toContain('<image x="120" y="41" width="240" height="480" visibility="visible" href="data:image/png;base64,QUJD"')
  expect(svg.length).toBeLessThanOrEqual(CLIP_WRAPPER_MAX + frames.reduce((n, f) => n + f.length + CLIP_IMAGE_MARKUP_MAX, 0))
})


for (const surface of ['vscode'] as const) {
  test(`${surface}: hires Oxy keeps his figure's size and his ground line whatever he holds`, async ($, on) => {
    mock.clock(on, { now: 1_000 })
    mock.store(on)
    const reads = mockPngFiles(on, 30_000)
    on('command.run', () => ({ text: '' }))
    const anims = ['laptop', 'float', 'plan-kanban'] as const
    for (const anim of anims) {
      await $.command.run({ ...COMMAND, surface, args: anim } as never)
      for (const box of SIZES) {
        await $.command.run({ ...COMMAND, surface, args: box } as never)
        const ui = await $.ui.mount({ ...SPINNER, surface, props: WORKING })
        const svg = await ui.find({ type: 'Svg' })
        const source = String(svg?.props.source)
        const want = desktopLayout(anim, box)
        expect(source).toContain(`viewBox="${want.viewBox}"`)
        expect(source.length).toBeLessThanOrEqual(SVG_SOURCE_CAP)
        expect({ width: svg?.props.width, height: svg?.props.height }).toEqual(want.size)
        expect((await ui.find({ type: 'Box', key: 'words' }))?.props.marginBottom).toBe(want.lift)
        if (want.hires && FRAMES.hires) {
          const h = FRAMES.hires
          const [, vy, , vh] = want.viewBox.split(' ').map(Number)
          // his figure is the size's height in every animation, to a pixel
          expect(Math.abs((h.figure.height * want.size.height) / vh - { small: 48, medium: 80, large: 120 }[box])).toBeLessThanOrEqual(1)
          // and every viewBox ends on the cell's bottom edge, the ground
          expect(vy + vh).toBe(h.cell.height)
        }
        await ui.unmount()
      }
    }
    const hiresDrawn = anims.some(a => desktopLayout(a, 'medium').hires)
    expect(reads.some(path => path.includes('/hooks/hires/'))).toBe(hiresDrawn)
    // each animation reads exactly the frames its own art's timings name
    for (const anim of anims) {
      const art = desktopLayout(anim, 'medium').hires ? 'hires' : 'frames'
      const read = new Set(reads.filter(path => path.includes(`/hooks/${art}/${anim}/`)))
      expect(read.size).toBe(clipTimings(FRAMES, anim, art === 'hires').length)
    }
    await $.command.run({ ...COMMAND, args: 'medium' } as never)
    await $.command.run({ ...COMMAND, args: 'auto' } as never)
  })
}

// Covers both fallbacks: an older frames.ts with no hires block, and a hires
// block whose PNGs can't be read. Either way the 160x207 clip draws.
test('vscode: hires frames missing or unreadable fall back to the 160x207 clip', async ($, on) => {
  mock.clock(on, { now: 1_000 })
  mock.store(on)
  const reads = mockPngFiles(on, 24_000, { hires: false })
  on('command.run', () => ({ text: '' }))
  await $.command.run({ ...COMMAND, surface: 'vscode', args: 'murmur' } as never)
  const ui = await $.ui.mount({ ...SPINNER, surface: 'vscode', props: WORKING })
  const svg = await ui.find({ type: 'Svg' })
  expect(String(svg?.props.source)).toContain('viewBox="0 0 160 207"')
  expect(String(svg?.props.source)).toContain('base64,murmur0x')
  expect({ width: svg?.props.width, height: svg?.props.height }).toEqual(pixelBox('medium'))
  expect(reads.length).toBeGreaterThan(0)
  expect(reads.every(path => path.includes('/hooks/frames/'))).toBe(true)
  await ui.unmount()
  await $.command.run({ ...COMMAND, args: 'auto' } as never)
})

test('hires clips use the hires timings; a missing or broken ms falls back to the pixel set', () => {
  const frames = { ...FRAMES, hires: HIRES }
  expect(clipTimings(frames, 'laptop', true)).toHaveLength(45)
  expect(clipTimings(frames, 'laptop', false)).toEqual(FRAMES.animations.laptop.ms)
  // float's entry has no ms: an older frames.ts, frame for frame the pixel set
  expect(clipTimings(frames, 'float', true)).toEqual(FRAMES.animations.float.ms)
  const broken = { ...HIRES, animations: { ...HIRES.animations, laptop: { ...HIRES.animations.laptop!, ms: [33, 0] } } }
  expect(clipTimings({ ...FRAMES, hires: broken }, 'laptop', true)).toEqual(FRAMES.animations.laptop.ms)
  expect(clipTimings({ ...FRAMES, hires: undefined }, 'laptop', true)).toEqual(FRAMES.animations.laptop.ms)
})

describe('packClips keeps clips at least MIN_CLIP_MS when frames allow', () => {
  const fps30 = (n: number) => Array.from({ length: n }, () => 33)
  const playMs = (ms: number[], [a, b]: [number, number]) => ms.slice(a, b).reduce((t, n) => t + n, 0)
  test('a short tail takes frames from the clip before', () => {
    // six 20K frames fit a clip; greedy leaves a 66 ms tail of two
    const ms = fps30(8)
    const lengths = ms.map(() => 20_000)
    expect(packClips(ms, lengths, SVG_SOURCE_CAP, 0)).toEqual([[0, 6], [6, 8]])
    const clips = packClips(ms, lengths, SVG_SOURCE_CAP)
    expect(clips).toEqual([[0, 4], [4, 8]])
    expect(clips.every(c => playMs(ms, c) >= MIN_CLIP_MS)).toBe(true)
  })
  test('hires-sized frames at 30 fps never make a clip under 100 ms', () => {
    for (const [n, base] of [7, 9, 13, 34, 61].flatMap(n => [20_000, 24_000, 28_000, 31_000].map(b => [n, b] as const))) {
      // 30 fps rounds to 33, 33, 34 ms
      const ms = Array.from({ length: n }, (_, i) => [33, 33, 34][i % 3]!)
      const lengths = ms.map((_, i) => base + ((i * 1_733) % 2_000))
      const clips = packClips(ms, lengths, SVG_SOURCE_CAP)
      expect(clips.flatMap(([a, b]) => Array.from({ length: b - a }, (_, i) => a + i))).toEqual(ms.map((_, i) => i))
      for (const c of clips) {
        expect(playMs(ms, c)).toBeGreaterThanOrEqual(MIN_CLIP_MS)
        const cost = CLIP_WRAPPER_MAX + lengths.slice(c[0], c[1]).reduce((t, len) => t + len + CLIP_IMAGE_MARKUP_MAX, 0)
        expect(cost).toBeLessThanOrEqual(SVG_SOURCE_CAP)
      }
    }
  })
  test('short clips cascade: each takes from the one before', () => {
    // four 28K frames to a clip; greedy leaves [0,4) [4,8) [8,9)
    const ms = Array.from({ length: 9 }, (_, i) => [33, 33, 34][i % 3]!)
    expect(packClips(ms, ms.map(() => 28_000), SVG_SOURCE_CAP)).toEqual([[0, 3], [3, 6], [6, 9]])
  })
  test('the cap still wins over the minimum', () => {
    const half = SVG_SOURCE_CAP / 2
    expect(packClips([33, 33, 33], [half, half, half], SVG_SOURCE_CAP)).toEqual([[0, 1], [1, 2], [2, 3]])
  })
  test('a clip already long enough is left alone', () => {
    expect(packClips([200, 200, 200], [60_000, 60_000, 60_000], SVG_SOURCE_CAP)).toEqual([[0, 2], [2, 3]])
  })
})
