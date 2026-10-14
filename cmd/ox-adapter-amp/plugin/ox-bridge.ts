/**
 * ox-bridge — minimal SageOx ↔ Amp session-recording plugin.
 *
 * Amp does not persist conversation transcripts. This plugin tails Amp's
 * Plugin API events and appends one JSONL line per event to
 * `~/.cache/amp/ox-sessions/<thread-id>.jsonl`, in the schema the
 * `ox-adapter-amp` Go binary already understands (`session_meta`,
 * `user`, `assistant`, `tool_use`, `tool_result`).
 *
 * Installed by `ox-adapter-amp install-hooks` into Amp's system plugin dir
 * (`$XDG_CONFIG_HOME/amp/plugins/`, else `~/.config/amp/plugins/`) so one
 * plugin serves every project.
 *
 * Orbs (sageox/ox#1314). Amp has no thread-end event, and an orb pauses
 * once its thread goes idle, freezing the ox daemon that would otherwise
 * finish the session. Inside an orb (`AMP_ORB` set), the plugin ends a
 * thread's ox session shortly before the orb's earliest possible pause by
 * running `ox agent hook SessionEnd`, which uploads the session to the
 * Ledger before it returns. Elsewhere the daemon outlives the agent and
 * finishes sessions on its own, so nothing changes there.
 */

import type { PluginAPI } from '@ampcode/plugin';
import { spawn } from 'node:child_process';
import { appendFileSync, mkdirSync } from 'node:fs';
import { homedir } from 'node:os';
import { join } from 'node:path';

const DIR = join(homedir(), '.cache', 'amp', 'ox-sessions');
mkdirSync(DIR, { recursive: true });

// Amp pauses an orb at the later of "agent idle 5 min" and "no interaction
// for 20 min" (https://ampcode.com/docs/orbs/sizes-and-costs). The session
// ends 3 min before the earliest pause, so the upload finishes first.
const ORB_AGENT_IDLE_MS = 5 * 60_000;
const ORB_INTERACTION_IDLE_MS = 20 * 60_000;
const UPLOAD_MARGIN_MS = 3 * 60_000;
// ox waits up to 3 min for the upload (sessionEndStopWait in
// cmd/ox/session_end_sync.go); wait a minute longer so ox returns first.
const STOP_TIMEOUT_MS = 4 * 60_000;

const write = (tid: string, obj: Record<string, unknown>): void =>
  appendFileSync(
    join(DIR, `${tid}.jsonl`),
    JSON.stringify({ ...obj, timestamp: new Date().toISOString() }) + '\n'
  );

const str = (v: unknown): string =>
  v == null ? '' : typeof v === 'string' ? v : JSON.stringify(v);

const assistantText = (m: unknown): string => {
  const c = (m as { content?: unknown })?.content;
  if (typeof c === 'string') return c;
  if (Array.isArray(c))
    return c
      .map((b) => (typeof b === 'string' ? b : (b as { text?: string })?.text ?? ''))
      .join('\n');
  return '';
};

/**
 * How long after a turn ends to end the thread's session: the orb's
 * earliest possible pause, less the upload margin. A prompt counts as the
 * coworker's last known interaction; the plugin can't see the others
 * (Files, Terminal, portals), and they only push the pause later.
 */
export function sessionEndDelay(turnEndedAt: number, lastPromptAt?: number): number {
  const byAgentIdle = turnEndedAt + ORB_AGENT_IDLE_MS;
  const byInteraction =
    lastPromptAt === undefined ? byAgentIdle : lastPromptAt + ORB_INTERACTION_IDLE_MS;
  return Math.max(byAgentIdle, byInteraction) - UPLOAD_MARGIN_MS - turnEndedAt;
}

type Timer = ReturnType<typeof setTimeout>;

export interface Clock {
  now(): number;
  setTimer(fn: () => void, ms: number): Timer;
  clearTimer(timer: Timer): void;
}

const realClock: Clock = {
  now: () => Date.now(),
  setTimer: (fn, ms) => setTimeout(fn, ms),
  clearTimer: (timer) => clearTimeout(timer),
};

/**
 * Schedules one session end per thread: a turn ending (re)arms it, and the
 * thread's next prompt cancels it.
 */
export function createSessionEnder(clock: Clock, end: (threadID: string) => Promise<void>) {
  const lastPrompt = new Map<string, number>();
  const pending = new Map<string, Timer>();
  const cancel = (threadID: string): void => {
    const timer = pending.get(threadID);
    if (timer === undefined) return;
    clock.clearTimer(timer);
    pending.delete(threadID);
  };
  return {
    promptStarted(threadID: string): void {
      cancel(threadID);
      lastPrompt.set(threadID, clock.now());
    },
    turnEnded(threadID: string): void {
      cancel(threadID);
      const delay = sessionEndDelay(clock.now(), lastPrompt.get(threadID));
      pending.set(
        threadID,
        clock.setTimer(() => {
          pending.delete(threadID);
          lastPrompt.delete(threadID);
          void end(threadID);
        }, delay)
      );
    },
  };
}

/**
 * Runs `ox agent hook SessionEnd` for the thread, holding the orb awake while
 * it uploads. The thread URL is the session id ox keyed the recording by
 * (Amp exports it to the agent's shell as AMP_THREAD_URL).
 */
export async function endSession(amp: PluginAPI, threadID: string): Promise<void> {
  const lease = await amp.system.executor.keepAlive().catch(() => undefined);
  try {
    const code = await runOx(['agent', 'hook', 'SessionEnd'], {
      cwd: amp.system.workspaceRoot
        ? amp.helpers.filePathFromURI(amp.system.workspaceRoot)
        : process.cwd(),
      input: JSON.stringify({
        session_id: new URL(`/threads/${threadID}`, amp.system.ampURL).toString(),
        hook_event_name: 'SessionEnd',
      }),
    });
    if (code !== 0)
      amp.logger.log(
        `ox-bridge event=session_end_exit thread_id=${threadID} exit_code=${code ?? 'still_running'}`
      );
  } catch (err) {
    amp.logger.log(
      `ox-bridge event=session_end_error thread_id=${threadID} error=${JSON.stringify(String(err))}`
    );
  } finally {
    lease?.unsubscribe();
  }
}

// Resolves with ox's exit code, or null when ox outlives STOP_TIMEOUT_MS; ox
// keeps uploading in its own process either way.
function runOx(args: string[], opts: { cwd: string; input: string }): Promise<number | null> {
  return new Promise((resolve, reject) => {
    const child = spawn('ox', args, {
      cwd: opts.cwd,
      env: { ...process.env, AGENT_ENV: 'amp' },
      stdio: ['pipe', 'ignore', 'ignore'],
    });
    const timer = setTimeout(() => resolve(null), STOP_TIMEOUT_MS);
    child.on('error', (err) => {
      clearTimeout(timer);
      reject(err);
    });
    child.on('exit', (code) => {
      clearTimeout(timer);
      resolve(code);
    });
    child.stdin?.on('error', () => {}); // ox missing: 'error' above reports it
    child.stdin?.end(opts.input);
  });
}

export function bridge(amp: PluginAPI, clock: Clock = realClock): void {
  const ender = process.env.AMP_ORB
    ? createSessionEnder(clock, (threadID) => endSession(amp, threadID))
    : undefined;

  amp.on('session.start', (e) => {
    if (e.thread?.id) write(e.thread.id, { type: 'session_meta', session_id: e.thread.id });
  });
  amp.on('agent.start', (e) => {
    ender?.promptStarted(e.thread.id);
    write(e.thread.id, { type: 'user', content: e.message });
    return {};
  });
  amp.on('tool.call', (e) => {
    write(e.thread.id, {
      type: 'tool_use',
      tool_name: e.tool,
      tool_input: str(e.input),
      call_id: e.toolUseID,
    });
    return { action: 'allow' as const };
  });
  amp.on('tool.result', (e) =>
    write(e.thread.id, {
      type: 'tool_result',
      content: str(e.output ?? e.error ?? ''),
      is_error: e.status === 'error',
      call_id: e.toolUseID,
    })
  );
  amp.on('agent.end', (e) => {
    for (const m of e.messages ?? []) {
      if ((m as { role?: string }).role !== 'assistant') continue;
      const text = assistantText(m).trim();
      if (text) write(e.thread.id, { type: 'assistant', content: text });
    }
    ender?.turnEnded(e.thread.id);
  });
}

export default function (amp: PluginAPI) {
  bridge(amp);
}
