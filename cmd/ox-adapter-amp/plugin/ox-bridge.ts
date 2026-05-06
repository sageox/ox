/**
 * ox-bridge — minimal SageOx ↔ Amp session-recording plugin.
 *
 * Amp does not persist conversation transcripts. This plugin tails Amp's
 * Plugin API events and appends one JSONL line per event to
 * `~/.cache/amp/ox-sessions/<thread-id>.jsonl`, in the schema the
 * `ox-adapter-amp` Go binary already understands (`session_meta`,
 * `user`, `assistant`, `tool_use`, `tool_result`).
 *
 * Installed by `ox-adapter-amp install-hooks` into the user-scope plugin
 * dir (`~/.config/amp/plugins/`) so one plugin serves every project.
 */

import type { PluginAPI } from '@ampcode/plugin';
import { appendFileSync, mkdirSync } from 'node:fs';
import { homedir } from 'node:os';
import { join } from 'node:path';

const DIR = join(homedir(), '.cache', 'amp', 'ox-sessions');
mkdirSync(DIR, { recursive: true });

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

export default function (amp: PluginAPI) {
  amp.on('session.start', (e) => {
    if (e.thread?.id) write(e.thread.id, { type: 'session_meta', session_id: e.thread.id });
  });
  amp.on('agent.start', (e) => write(e.thread.id, { type: 'user', content: e.message }));
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
  });
}
