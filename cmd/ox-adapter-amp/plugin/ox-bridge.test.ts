// Tests for ox-bridge.ts. Run through TestOxBridgePlugin_BunTests in
// cmd/ox-adapter-amp, or by hand with a throwaway home:
//
//	HOME=$(mktemp -d) bun test
//
// Customer promise (sageox/ox#1314): in an Amp orb, a thread's session is
// ended, and so uploaded, before the orb's earliest pause; a coworker who
// keeps working keeps one session; laptops are unchanged.

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
const { bridge, createSessionEnder, endSession, sessionEndDelay } = await import('./ox-bridge.ts');
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

// a fake `ox` on PATH that records how it was called
function fakeOx(): { log: string; restore: () => void } {
  const bin = mkdtempSync(join(tmpdir(), 'ox-bridge-bin-'));
  const log = join(bin, 'calls.log');
  writeFileSync(
    join(bin, 'ox'),
    `#!/bin/sh\n{ echo "args=$*"; echo "cwd=$(pwd -P)"; echo "agent_env=$AGENT_ENV"; printf 'stdin='; cat; echo; } > "${log}"\n`
  );
  chmodSync(join(bin, 'ox'), 0o755);
  const priorPath = process.env.PATH;
  process.env.PATH = `${bin}:${priorPath}`;
  return { log, restore: () => (process.env.PATH = priorPath) };
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
  test('a quick turn waits for the 20-minute interaction window, less the upload margin', () => {
    expect(sessionEndDelay(1 * MIN, 0)).toBe(16 * MIN);
  });
  test('with no prompt seen, only agent idle counts', () => {
    expect(sessionEndDelay(10 * MIN)).toBe(2 * MIN);
  });
});

describe('createSessionEnder', () => {
  test('a coworker who keeps prompting keeps one session; idle ends it once', () => {
    const { clock, advanceTo } = fakeClock();
    const ended: string[] = [];
    const ender = createSessionEnder(clock, async (tid) => void ended.push(tid));

    ender.promptStarted('T-1');
    advanceTo(1 * MIN);
    ender.turnEnded('T-1'); // ends at 17 min unless Devon replies
    advanceTo(10 * MIN);
    ender.promptStarted('T-1'); // Devon replies: cancelled
    advanceTo(11 * MIN);
    ender.turnEnded('T-1'); // now ends at max(13, 27) = 27 min
    advanceTo(26 * MIN);
    expect(ended).toEqual([]);
    advanceTo(27 * MIN);
    expect(ended).toEqual(['T-1']);
  });

  test('threads end independently', () => {
    const { clock, advanceTo } = fakeClock();
    const ended: string[] = [];
    const ender = createSessionEnder(clock, async (tid) => void ended.push(tid));

    ender.turnEnded('T-a'); // 2 min
    advanceTo(1 * MIN);
    ender.turnEnded('T-b'); // 3 min
    advanceTo(2 * MIN);
    expect(ended).toEqual(['T-a']);
    advanceTo(3 * MIN);
    expect(ended).toEqual(['T-a', 'T-b']);
  });
});

describe('bridge', () => {
  const workspace = mkdtempSync(join(tmpdir(), 'ox-bridge-ws-'));

  test('on a laptop nothing is scheduled; the daemon finishes sessions', () => {
    delete process.env.AMP_ORB;
    const { clock, pending } = fakeClock();
    const { amp, fire } = fakeAmp(workspace);
    bridge(amp, clock);

    fire('agent.start', { thread: { id: 'T-laptop' }, message: 'fix the flaky test' });
    fire('agent.end', { thread: { id: 'T-laptop' }, messages: [{ role: 'assistant', content: 'Fixed.' }] });

    expect(pending()).toHaveLength(0);
    const sidecar = readFileSync(join(home, '.cache', 'amp', 'ox-sessions', 'T-laptop.jsonl'), 'utf8');
    expect(sidecar).toContain('fix the flaky test');
    expect(sidecar).toContain('Fixed.');
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
      expect(existsSync(ox.log)).toBe(false);
      advanceTo(32 * MIN);

      await waitFor(() => leases.released === 1, 'the keepAlive lease to be released');
      const call = readFileSync(ox.log, 'utf8');
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
});

describe('endSession', () => {
  const workspace = mkdtempSync(join(tmpdir(), 'ox-bridge-ws-'));

  test('ends the session even when keepAlive is unavailable', async () => {
    const ox = fakeOx();
    try {
      const { amp, leases } = fakeAmp(workspace, 'rejects');
      await endSession(amp, 'T-nolease');
      expect(readFileSync(ox.log, 'utf8')).toContain('args=agent hook SessionEnd');
      expect(leases.taken).toBe(0);
    } finally {
      ox.restore();
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
