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
 *
 * A coworker can reply in that thread after its session ended
 * (sageox/ox#1318). Nothing else starts recording again then: the model
 * primed once, at the thread's start. So the plugin runs `ox agent prime`
 * itself, with the thread's URL and its output discarded, once the reply's
 * prompt is in the sidecar. ox starts that recording at the prompt's line and
 * links it as continued from the session that ended.
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
// Amp starts the 20-minute interaction clock when the coworker sends the
// message, but the plugin first sees the turn at agent.start, once the orb is
// up. A project orb that built its setup snapshot started its turn about
// 1m40s after the message, and the upload landed only 70 s before the pause
// (sageox/ox#1425). Assume this much of the window is gone by agent.start.
// The agent-idle clock needs no allowance: it starts at agent.end, which the
// plugin sees as it happens.
const ORB_START_ALLOWANCE_MS = 3 * 60_000;
// ox waits up to 3 min for the upload (sessionEndStopWait in
// cmd/ox/session_end_sync.go); wait a minute longer so ox returns first. A
// prime takes seconds; the same bound only stops the plugin waiting on one.
const STOP_TIMEOUT_MS = 4 * 60_000;
// A resume fails while the last stop is still uploading: SessionEnd returns
// after 3 min and leaves a slow stop running. It is tried again when the turn
// ends and on the next prompt, at most this many times per idle end, so a
// prime that cannot record (ox missing, repo not set up) is not rerun on
// every prompt.
const RESUME_ATTEMPTS = 3;
// prime's JSON is a few KB; this only bounds a misbehaving child
const PRIME_OUTPUT_MAX_BYTES = 1 << 20;
// The end of ox's output can arrive after it exits. A daemon that prime
// starts may hold the pipe open, so wait this long for it at most.
const STDOUT_DRAIN_MS = 1_000;

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
    lastPromptAt === undefined
      ? byAgentIdle
      : lastPromptAt + ORB_INTERACTION_IDLE_MS - ORB_START_ALLOWANCE_MS;
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
 * thread's next prompt cancels it. The first prompt after an end resumes
 * recording. A thread's ends and resumes run one at a time, in order: a
 * prime while the stop still owns the recording would find it live and start
 * nothing, and an end before the resume it follows would end nothing. A
 * resume that fails leaves the thread ended, and is tried again when the turn
 * ends and on the next prompt (RESUME_ATTEMPTS).
 */
export function createSessionEnder(
  clock: Clock,
  end: (threadID: string) => Promise<void>,
  resume: (threadID: string) => Promise<boolean>
) {
  const lastPrompt = new Map<string, number>();
  const pending = new Map<string, Timer>();
  // threads whose session ended and has not been resumed
  const ended = new Set<string>();
  const attempts = new Map<string, number>();
  const queue = new Map<string, Promise<void>>();
  const settled = (): void => {};
  const enqueue = (threadID: string, step: () => Promise<unknown>): void => {
    const prior = queue.get(threadID) ?? Promise.resolve();
    queue.set(threadID, prior.then(step).then(settled, settled));
  };
  const cancel = (threadID: string): void => {
    const timer = pending.get(threadID);
    if (timer === undefined) return;
    clock.clearTimer(timer);
    pending.delete(threadID);
  };
  const tryResume = (threadID: string): void => {
    const tried = attempts.get(threadID) ?? 0;
    if (tried >= RESUME_ATTEMPTS || !ended.delete(threadID)) return;
    attempts.set(threadID, tried + 1);
    enqueue(threadID, async () => {
      let resumed = false;
      try {
        resumed = await resume(threadID);
      } finally {
        if (!resumed) ended.add(threadID);
      }
    });
  };
  return {
    promptStarted(threadID: string): void {
      cancel(threadID);
      lastPrompt.set(threadID, clock.now());
      tryResume(threadID);
    },
    turnEnded(threadID: string): void {
      // A resume that failed starts at the same prompt's line when tried
      // here, so the turn still lands whole.
      tryResume(threadID);
      cancel(threadID);
      const delay = sessionEndDelay(clock.now(), lastPrompt.get(threadID));
      pending.set(
        threadID,
        clock.setTimer(() => {
          pending.delete(threadID);
          lastPrompt.delete(threadID);
          ended.add(threadID);
          attempts.delete(threadID);
          enqueue(threadID, () => end(threadID));
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
    const { code } = await runOx(['agent', 'hook', 'SessionEnd'], {
      cwd: workspaceDir(amp),
      input: JSON.stringify({ session_id: threadURL(amp, threadID), hook_event_name: 'SessionEnd' }),
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

/**
 * Starts recording the thread again after its session ended, the way the
 * model's own prime does: `ox agent prime` with the thread's URL in
 * AMP_THREAD_URL, which keys the AI coworker and the session it continues.
 * Its team context is for the model, which already has it, so only the
 * recording status is read. Resolves whether the thread is recording again.
 */
export async function resumeSession(amp: PluginAPI, threadID: string): Promise<boolean> {
  try {
    const { code, stdout } = await runOx(['agent', 'prime', '--format', 'json'], {
      cwd: workspaceDir(amp),
      env: { AMP_THREAD_URL: threadURL(amp, threadID) },
      captureStdout: true,
    });
    if (primeIsRecording(stdout)) return true;
    amp.logger.log(
      `ox-bridge event=session_resume_not_recording thread_id=${threadID} exit_code=${code ?? 'still_running'}`
    );
    return false;
  } catch (err) {
    amp.logger.log(
      `ox-bridge event=session_resume_error thread_id=${threadID} error=${JSON.stringify(String(err))}`
    );
    return false;
  }
}

/**
 * Whether `ox agent prime --format json` reports the thread recording. Its
 * exit code cannot say: a prime that finds the last stop still uploading
 * starts nothing and still exits 0.
 */
export function primeIsRecording(stdout: string): boolean {
  const start = stdout.search(/^\{/m);
  if (start < 0) return false;
  try {
    const output = JSON.parse(stdout.slice(start)) as { session?: { recording?: unknown } };
    return output.session?.recording === true;
  } catch {
    return false;
  }
}

// The URL ox keys a thread's recording by: Amp exports it to the agent's
// shell as AMP_THREAD_URL, and agentx rebuilds it in this shape in an orb.
const threadURL = (amp: PluginAPI, threadID: string): string =>
  new URL(`/threads/${threadID}`, amp.system.ampURL).toString();

const workspaceDir = (amp: PluginAPI): string =>
  amp.system.workspaceRoot ? amp.helpers.filePathFromURI(amp.system.workspaceRoot) : process.cwd();

// Resolves with ox's exit code, or null when ox outlives STOP_TIMEOUT_MS (ox
// keeps running in its own process either way), and what it printed when
// captureStdout is set. Without input, ox's stdin is empty, so a prime reads
// no hook payload.
function runOx(
  args: string[],
  opts: { cwd: string; input?: string; env?: Record<string, string>; captureStdout?: boolean }
): Promise<{ code: number | null; stdout: string }> {
  return new Promise((resolve, reject) => {
    const child = spawn('ox', args, {
      cwd: opts.cwd,
      env: { ...process.env, AGENT_ENV: 'amp', ...opts.env },
      stdio: [opts.input === undefined ? 'ignore' : 'pipe', opts.captureStdout ? 'pipe' : 'ignore', 'ignore'],
    });
    let stdout = '';
    child.stdout?.setEncoding('utf8');
    child.stdout?.on('data', (chunk: string) => {
      if (stdout.length < PRIME_OUTPUT_MAX_BYTES) stdout += chunk;
    });
    const timer = setTimeout(() => resolve({ code: null, stdout }), STOP_TIMEOUT_MS);
    child.on('error', (err) => {
      clearTimeout(timer);
      reject(err);
    });
    child.on('exit', (code) => {
      clearTimeout(timer);
      const out = child.stdout;
      if (!out || out.readableEnded) return resolve({ code, stdout });
      const done = (): void => {
        clearTimeout(drain);
        out.destroy();
        resolve({ code, stdout });
      };
      const drain = setTimeout(done, STDOUT_DRAIN_MS);
      out.once('end', done);
    });
    child.stdin?.on('error', () => {}); // ox missing: 'error' above reports it
    child.stdin?.end(opts.input);
  });
}

export function bridge(amp: PluginAPI, clock: Clock = realClock): void {
  const ender = process.env.AMP_ORB
    ? createSessionEnder(
        clock,
        (threadID) => endSession(amp, threadID),
        (threadID) => resumeSession(amp, threadID)
      )
    : undefined;

  amp.on('session.start', (e) => {
    if (e.thread?.id) write(e.thread.id, { type: 'session_meta', session_id: e.thread.id });
  });
  amp.on('agent.start', (e) => {
    // The prompt goes in first: a resumed recording starts at its line. The
    // pending end is cancelled even if the write fails.
    try {
      write(e.thread.id, { type: 'user', content: e.message });
    } finally {
      ender?.promptStarted(e.thread.id);
    }
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
