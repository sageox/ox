// Tests for ox-bridge.ts. Run through TestOxBridgePlugin_BunTests in
// cmd/ox-adapter-amp, or by hand with a throwaway home:
//
//	HOME=$(mktemp -d) bun test
//
// Customer promise (sageox/ox#1314): in an Amp orb, a thread's session is
// ended, and so uploaded, before the orb's earliest pause; a coworker who
// keeps working keeps one session; laptops are unchanged. And (sageox/ox#1318)
// a coworker who replies after that end is recorded again.

import { describe, expect, test } from 'bun:test';
import type { PluginAPI } from '@ampcode/plugin';
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, writeFileSync } from 'node:fs';
import { homedir, tmpdir } from 'node:os';
import { join } from 'node:path';

// The plugin writes sidecars under the home directory, which Bun fixes at
// startup: setting HOME here would not move them. Refuse to run against a
// real home.
const home = realpathSync(homedir());
if (!home.startsWith(realpathSync(tmpdir()))) {
  throw new Error(`refusing to write Amp sidecars into ${home}: run with HOME=$(mktemp -d) bun test`);
}
const { bridge, createSessionEnder, endSession, resumeSession, sessionEndDelay } = await import('./ox-bridge.ts');
type Clock = Parameters<typeof createSessionEnder>[0];

const MIN = 60_000;

interface FakeTimer {
  fn: () => void;
  at: number;
  cleared: boolean;
  fired: boolean;
}

function fakeClock() {
  let now = 0;
  const timers: FakeTimer[] = [];
  const clock = {
    now: () => now,
    setTimer: (fn: () => void, ms: number) => {
      const timer: FakeTimer = { fn, at: now + ms, cleared: false, fired: false };
      timers.push(timer);
      return timer as unknown as ReturnType<typeof setTimeout>;
    },
    clearTimer: (timer: ReturnType<typeof setTimeout>) => {
      (timer as unknown as FakeTimer).cleared = true;
    },
  } satisfies Clock;
  return {
    clock,
    advanceTo(ms: number) {
      now = ms;
      for (const t of timers) {
        if (!t.cleared && !t.fired && t.at <= now) {
          t.fired = true;
          t.fn();
        }
      }
    },
    pending: () => timers.filter((t) => !t.cleared && !t.fired),
  };
}

function fakeAmp(workspace: string, keepAlive: 'grants' | 'rejects' = 'grants') {
  const handlers = new Map<string, (e: unknown) => unknown>();
  const leases = { taken: 0, released: 0 };
  const logs: string[] = [];
  const amp = {
    on: (event: string, fn: (e: unknown) => unknown) => {
      handlers.set(event, fn);
      return { unsubscribe() {} };
    },
    logger: { log: (...args: unknown[]) => logs.push(args.join(' ')) },
    helpers: { filePathFromURI: (uri: { toString(): string }) => uri.toString().replace('file://', '') },
    system: {
      workspaceRoot: { toString: () => `file://${workspace}` },
      ampURL: new URL('https://ampcode.com/'),
      executor: {
        kind: 'remote',
        keepAlive: async () => {
          if (keepAlive === 'rejects') throw new Error('not running inside an Amp-managed orb');
          leases.taken++;
          return { unsubscribe: () => leases.released++ };
        },
      },
    },
  } as unknown as PluginAPI;
  const fire = (event: string, payload: unknown) => handlers.get(event)?.(payload);
  return { amp, fire, leases, logs };
}

// A fake `ox` on PATH that records how it was called, one log per command
// (`ox agent hook ...` to hook.log, `ox agent prime` to prime.log), written
// whole once the call is done. It also records the thread's last sidecar
// line as the call saw it.
function fakeOx(): { logOf: (command: 'hook' | 'prime') => string; restore: () => void } {
  const bin = mkdtempSync(join(tmpdir(), 'ox-bridge-bin-'));
  writeFileSync(
    join(bin, 'ox'),
    `#!/bin/sh
log="${bin}/$2.log"
{
  echo "args=$*"; echo "cwd=$(pwd -P)"; echo "agent_env=$AGENT_ENV"; echo "amp_thread_url=$AMP_THREAD_URL"
  echo "sidecar_tail=$(tail -n 1 "$HOME/.cache/amp/ox-sessions/\${AMP_THREAD_URL##*/}.jsonl" 2>/dev/null)"
  printf 'stdin='; cat; echo
} > "$log.tmp"
mv "$log.tmp" "$log"
`
  );
  chmodSync(join(bin, 'ox'), 0o755);
  const priorPath = process.env.PATH;
  process.env.PATH = `${bin}:${priorPath}`;
  return { logOf: (command) => join(bin, `${command}.log`), restore: () => (process.env.PATH = priorPath) };
}

async function waitFor(check: () => boolean, what: string): Promise<void> {
  for (let i = 0; i < 200; i++) {
    if (check()) return;
    await new Promise((r) => setTimeout(r, 10));
  }
  throw new Error(`timed out waiting for ${what}`);
}

describe('sessionEndDelay', () => {
  test('a long turn ends 2 minutes after it finishes (orb pauses at agent idle + 5 min)', () => {
    expect(sessionEndDelay(30 * MIN, 0)).toBe(2 * MIN);
  });
  test('a quick turn ends inside the 20-minute interaction window, less the orb start allowance and the upload margin', () => {
    expect(sessionEndDelay(1 * MIN, 0)).toBe(13 * MIN);
  });
  test('a turn that starts 1m40s after its message (a snapshot build) still ends 3+ minutes before the pause', () => {
    const sentAt = 0; // Amp's interaction clock; the plugin never sees it
    const startedAt = 100_000; // agent.start once the orb is up
    const endedAt = 130_000;
    const sessionEndAt = endedAt + sessionEndDelay(endedAt, startedAt);
    const pauseAt = Math.max(endedAt + 5 * MIN, sentAt + 20 * MIN);
    expect(pauseAt - sessionEndAt).toBeGreaterThanOrEqual(3 * MIN);
  });
  test('with no prompt seen, only agent idle counts', () => {
    expect(sessionEndDelay(10 * MIN)).toBe(2 * MIN);
  });
});

// lets a test finish a session end when it chooses
function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>((r) => (resolve = r));
  return { promise, resolve };
}

const flush = () => new Promise((r) => setTimeout(r, 0));

describe('createSessionEnder', () => {
  const noResume = async () => true;

  test('a coworker who keeps prompting keeps one session; idle ends it once', async () => {
    const { clock, advanceTo } = fakeClock();
    const ended: string[] = [];
    const ender = createSessionEnder(clock, async (tid) => void ended.push(tid), noResume);

    ender.promptStarted('T-1');
    advanceTo(1 * MIN);
    ender.turnEnded('T-1'); // ends at 14 min unless Devon replies
    advanceTo(10 * MIN);
    ender.promptStarted('T-1'); // Devon replies: cancelled
    advanceTo(11 * MIN);
    ender.turnEnded('T-1'); // now ends at max(13, 24) = 24 min
    advanceTo(23 * MIN);
    await flush();
    expect(ended).toEqual([]);
    advanceTo(24 * MIN);
    await flush();
    expect(ended).toEqual(['T-1']);
  });

  test('threads end independently', async () => {
    const { clock, advanceTo } = fakeClock();
    const ended: string[] = [];
    const ender = createSessionEnder(clock, async (tid) => void ended.push(tid), noResume);

    ender.turnEnded('T-a'); // 2 min
    advanceTo(1 * MIN);
    ender.turnEnded('T-b'); // 3 min
    advanceTo(2 * MIN);
    await flush();
    expect(ended).toEqual(['T-a']);
    advanceTo(3 * MIN);
    await flush();
    expect(ended).toEqual(['T-a', 'T-b']);
  });

  test('a reply after the idle end resumes recording once, after the end has finished', async () => {
    const { clock, advanceTo } = fakeClock();
    const stop = deferred();
    const resumed: string[] = [];
    const ender = createSessionEnder(clock, () => stop.promise, async (tid) => (resumed.push(tid), true));

    ender.turnEnded('T-1');
    advanceTo(2 * MIN); // idle: the session end starts uploading
    ender.promptStarted('T-1'); // Devon replies while it uploads
    await flush();
    expect(resumed).toEqual([]); // the stop still owns the recording

    stop.resolve();
    await flush();
    expect(resumed).toEqual(['T-1']);

    ender.turnEnded('T-1');
    ender.promptStarted('T-1'); // a reply before the next idle end keeps the session
    await flush();
    expect(resumed).toEqual(['T-1']);
  });

  test('a prompt before any idle end never resumes', async () => {
    const { clock, advanceTo } = fakeClock();
    const resumed: string[] = [];
    const ender = createSessionEnder(clock, async () => {}, async (tid) => (resumed.push(tid), true));

    ender.promptStarted('T-1');
    advanceTo(1 * MIN);
    ender.turnEnded('T-1');
    advanceTo(10 * MIN);
    ender.promptStarted('T-1');
    await flush();
    expect(resumed).toEqual([]);
  });

  test('a failed end still resumes: the next prime records into whatever it left', async () => {
    const { clock, advanceTo } = fakeClock();
    const resumed: string[] = [];
    const ender = createSessionEnder(
      clock,
      () => Promise.reject(new Error('ox crashed')),
      async (tid) => (resumed.push(tid), true)
    );

    ender.turnEnded('T-1');
    advanceTo(2 * MIN);
    ender.promptStarted('T-1');
    await flush();
    expect(resumed).toEqual(['T-1']);
  });

  test("an end that fires while the last end still uploads waits for the reply's resume", async () => {
    const { clock, advanceTo } = fakeClock();
    const firstStop = deferred();
    const calls: string[] = [];
    let ends = 0;
    const ender = createSessionEnder(
      clock,
      (tid) => {
        calls.push(`end ${tid}`);
        return ends++ === 0 ? firstStop.promise : Promise.resolve();
      },
      async (tid) => (calls.push(`resume ${tid}`), true)
    );

    ender.turnEnded('T-1');
    advanceTo(2 * MIN); // end #1 starts uploading
    ender.promptStarted('T-1'); // Devon replies
    ender.turnEnded('T-1');
    advanceTo(16 * MIN); // end #2 is due before end #1 has returned
    await flush();
    expect(calls).toEqual(['end T-1']);

    firstStop.resolve();
    await flush();
    expect(calls).toEqual(['end T-1', 'resume T-1', 'end T-1']);
  });

  test('a prime that fails is tried again on the next prompt, and only until it works', async () => {
    const { clock, advanceTo } = fakeClock();
    const results = [false, true];
    const resumed: string[] = [];
    const ender = createSessionEnder(clock, async () => {}, async (tid) => (resumed.push(tid), results.shift()!));

    ender.turnEnded('T-1');
    advanceTo(2 * MIN);
    ender.promptStarted('T-1'); // the prime fails
    await flush();
    ender.promptStarted('T-1'); // tried again
    await flush();
    ender.promptStarted('T-1'); // recording: nothing to resume
    await flush();
    expect(resumed).toEqual(['T-1', 'T-1']);
  });
});

describe('bridge', () => {
  const workspace = mkdtempSync(join(tmpdir(), 'ox-bridge-ws-'));

  test('on a laptop nothing is scheduled or primed; the daemon finishes sessions', async () => {
    delete process.env.AMP_ORB;
    const ox = fakeOx();
    try {
      const { clock, pending } = fakeClock();
      const { amp, fire } = fakeAmp(workspace);
      bridge(amp, clock);

      fire('agent.start', { thread: { id: 'T-laptop' }, message: 'fix the flaky test' });
      fire('agent.end', { thread: { id: 'T-laptop' }, messages: [{ role: 'assistant', content: 'Fixed.' }] });
      fire('agent.start', { thread: { id: 'T-laptop' }, message: 'and the other one' });
      await new Promise((r) => setTimeout(r, 100));

      expect(pending()).toHaveLength(0);
      expect(existsSync(ox.logOf('prime'))).toBe(false);
      const sidecar = readFileSync(join(home, '.cache', 'amp', 'ox-sessions', 'T-laptop.jsonl'), 'utf8');
      expect(sidecar).toContain('fix the flaky test');
      expect(sidecar).toContain('Fixed.');
    } finally {
      ox.restore();
    }
  });

  test('in an orb an idle thread runs `ox agent hook SessionEnd` with the orb held awake', async () => {
    process.env.AMP_ORB = '1';
    const ox = fakeOx();
    try {
      const { clock, advanceTo } = fakeClock();
      const { amp, fire, leases } = fakeAmp(workspace);
      bridge(amp, clock);

      fire('agent.start', { thread: { id: 'T-orb' }, message: 'rename the retry helper' });
      advanceTo(30 * MIN);
      fire('agent.end', { thread: { id: 'T-orb' }, messages: [] });
      advanceTo(31 * MIN);
      expect(existsSync(ox.logOf('hook'))).toBe(false);
      advanceTo(32 * MIN);

      await waitFor(() => leases.released === 1, 'the keepAlive lease to be released');
      const call = readFileSync(ox.logOf('hook'), 'utf8');
      expect(call).toContain('args=agent hook SessionEnd');
      expect(call).toContain(`cwd=${realpathSync(workspace)}`);
      expect(call).toContain('agent_env=amp');
      const stdin = JSON.parse(call.split('stdin=')[1]);
      expect(stdin).toEqual({ session_id: 'https://ampcode.com/threads/T-orb', hook_event_name: 'SessionEnd' });
      expect(leases.taken).toBe(1);
    } finally {
      ox.restore();
      delete process.env.AMP_ORB;
    }
  });

  test('in an orb a reply after the idle end runs `ox agent prime` for the thread, once its prompt is on disk', async () => {
    process.env.AMP_ORB = '1';
    const ox = fakeOx();
    try {
      const { clock, advanceTo } = fakeClock();
      const { amp, fire, leases } = fakeAmp(workspace);
      bridge(amp, clock);

      fire('agent.start', { thread: { id: 'T-reply' }, message: 'rename the retry helper' });
      advanceTo(30 * MIN);
      fire('agent.end', { thread: { id: 'T-reply' }, messages: [] });
      advanceTo(32 * MIN); // idle: the session ends
      await waitFor(() => leases.released === 1, 'the session end');
      expect(existsSync(ox.logOf('prime'))).toBe(false);

      fire('agent.start', { thread: { id: 'T-reply' }, message: 'now add a unit test' });

      await waitFor(() => existsSync(ox.logOf('prime')), 'the quiet prime');
      const call = readFileSync(ox.logOf('prime'), 'utf8');
      expect(call).toContain('args=agent prime\n');
      expect(call).toContain(`cwd=${realpathSync(workspace)}`);
      expect(call).toContain('agent_env=amp');
      expect(call).toContain('amp_thread_url=https://ampcode.com/threads/T-reply\n');
      // ox starts the new recording at the prompt's line, so it must be there
      const tail = JSON.parse(call.split('sidecar_tail=')[1].split('\n')[0]);
      expect(tail).toMatchObject({ type: 'user', content: 'now add a unit test' });
      expect(call).toEndWith('stdin=\n');
    } finally {
      ox.restore();
      delete process.env.AMP_ORB;
    }
  });
});

describe('endSession', () => {
  const workspace = mkdtempSync(join(tmpdir(), 'ox-bridge-ws-'));

  test('ends the session even when keepAlive is unavailable', async () => {
    const ox = fakeOx();
    try {
      const { amp, leases } = fakeAmp(workspace, 'rejects');
      await endSession(amp, 'T-nolease');
      expect(readFileSync(ox.logOf('hook'), 'utf8')).toContain('args=agent hook SessionEnd');
      expect(leases.taken).toBe(0);
    } finally {
      ox.restore();
    }
  });

  test('a prime without ox on PATH logs and reports that it failed', async () => {
    const priorPath = process.env.PATH;
    process.env.PATH = mkdtempSync(join(tmpdir(), 'ox-bridge-empty-'));
    try {
      const { amp, logs } = fakeAmp(workspace);
      expect(await resumeSession(amp, 'T-noox')).toBe(false);
      expect(logs.join('\n')).toMatch(/^ox-bridge event=session_resume_error thread_id=T-noox error=".*"$/m);
    } finally {
      process.env.PATH = priorPath;
    }
  });

  test('without ox on PATH it logs, releases the lease, and does not throw', async () => {
    const priorPath = process.env.PATH;
    const empty = mkdtempSync(join(tmpdir(), 'ox-bridge-empty-'));
    mkdirSync(empty, { recursive: true });
    process.env.PATH = empty;
    try {
      const { amp, leases, logs } = fakeAmp(workspace);
      await endSession(amp, 'T-noox');
      expect(leases.released).toBe(1);
      expect(logs.join('\n')).toMatch(/^ox-bridge event=session_end_error thread_id=T-noox error=".*"$/m);
    } finally {
      process.env.PATH = priorPath;
    }
  });
});
