// Draft state the host controls keep per plan, and how a send result is
// worded. Pure: no DOM, no storage; test/state.test.mjs runs it in node.

import type { Status } from './protocol.js';

/** A reviewer's unsent mark, stamped with updated_at (ms) when saved. */
export interface Mark {
  anchor: string;
  section?: string;
  label?: string;
  status: Status;
  note?: string;
  quote?: string;
  updated_at?: number;
}

/** anchor -> unsent mark, and anchor -> ms the mark was deleted or sent. */
export interface Drafts {
  marks: Record<string, Mark>;
  tombs: Record<string, number>;
}

/** The one submission in flight, resent with the same id until acknowledged. */
export interface Outbox {
  id: string;
  items: Array<Omit<Mark, 'updated_at'>>;
  reviewer: string;
  created_at: string;
  stamps: Record<string, number>;
}

export interface SyncResult {
  round_id?: string;
  saved?: boolean;
  pushed?: boolean;
  notified?: boolean;
  duplicate?: boolean;
}

export interface SyncMessage {
  kind: 'ok' | 'warn' | 'err';
  text: string;
}

export function stampOf(m: Mark | undefined): number {
  return (m && +(m.updated_at || 0)) || 0;
}

// mergeState joins two views of the same plan per anchor: the newest write
// wins, and a tombstone beats a mark stamped at or before it, so a delete (or
// a send) in one tab is not undone by another tab's stale copy.
export function mergeState(x: Partial<Drafts>, y: Partial<Drafts>): Drafts {
  const out: Drafts = { marks: {}, tombs: {} };
  const keys: Record<string, 1> = {};
  [x, y].forEach((s) => {
    Object.keys(s.marks || {}).forEach((k) => { keys[k] = 1; });
    Object.keys(s.tombs || {}).forEach((k) => { keys[k] = 1; });
  });
  Object.keys(keys).forEach((k) => {
    let best: Mark | null = null;
    [x, y].forEach((s) => {
      const m = (s.marks || {})[k];
      if (m && (!best || stampOf(m) > stampOf(best))) best = m;
    });
    const tomb = Math.max(+((x.tombs || {})[k]) || 0, +((y.tombs || {})[k]) || 0);
    // tomb 0 = never deleted, so a mark saved before stamps existed (0) stays
    if (best && (!tomb || stampOf(best) > tomb)) out.marks[k] = best;
    else if (tomb) out.tombs[k] = tomb;
  });
  return out;
}

export function pruneTombs(t: Record<string, number>, now: number, ttl: number): Record<string, number> {
  const out: Record<string, number> = {};
  Object.keys(t).forEach((k) => { if (now - t[k] < ttl) out[k] = t[k]; });
  return out;
}

// ackState clears what a round carried once the server acknowledged it: each
// sent anchor is tombstoned at the stamp it was sent with, so an edit made
// after Submit (a newer stamp, in any tab) survives as still unsent.
export function ackState(s: Drafts, box: Pick<Outbox, 'items'> & Partial<Pick<Outbox, 'stamps'>>): Drafts {
  const out: Drafts = { marks: { ...s.marks }, tombs: { ...s.tombs } };
  (box.items || []).forEach((it) => {
    // a mark from before stamps existed is sent at 0; tomb it at 1 so it still beats a stale 0
    const at = Math.max(+((box.stamps || {})[it.anchor]) || 0, 1);
    if (out.marks[it.anchor] && stampOf(out.marks[it.anchor]) <= at) delete out.marks[it.anchor];
    if (!out.marks[it.anchor]) out.tombs[it.anchor] = Math.max(out.tombs[it.anchor] || 0, at);
  });
  return out;
}

// roundAcked: whether a 2xx acknowledges this outbox. A server from before
// round ids answers without round_id — saved, but which round is unknown.
export function roundAcked(box: Pick<Outbox, 'id'>, data: SyncResult | null | undefined): boolean {
  return !data || data.round_id === undefined || data.round_id === box.id;
}

// syncMessage words a 2xx truthfully: "synced" only when the server says the
// ledger was pushed. An older server reports nothing about sync, so the page
// claims no more than that the author's machine has it.
export function syncMessage(verb: string, data: SyncResult | null | undefined): SyncMessage {
  data = data || {};
  let m: SyncMessage;
  if (data.saved === undefined) m = { kind: 'ok', text: verb + ' · received by the author’s machine' };
  else if (data.saved === false) m = { kind: 'err', text: 'Not saved — the author’s machine could not store it' };
  else if (data.pushed) m = { kind: 'ok', text: verb + ' · synced to your team' };
  else m = { kind: 'warn', text: 'Saved on the author’s machine · not yet synced (will retry)' };
  // a duplicate is a resend of a round whose first save already notified (or tried to)
  if (data.notified === false && !data.duplicate) m.text += ' · the plan’s authoring coworker was not notified automatically — tell them directly';
  return m;
}

export function sameItems(a: unknown, b: unknown): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}

// newRoundId: the server accepts ^[A-Za-z0-9_-]{8,64}$.
export function newRoundId(): string {
  const c = typeof crypto !== 'undefined' ? crypto : null;
  if (c && c.randomUUID) return c.randomUUID();
  const b = new Uint8Array(16);
  let s = '';
  if (c && c.getRandomValues) c.getRandomValues(b);
  else for (let i = 0; i < 16; i++) b[i] = Math.floor(Math.random() * 256); // no Web Crypto: still unique enough to dedupe one reviewer's retries
  for (let j = 0; j < 16; j++) s += ('0' + b[j].toString(16)).slice(-2);
  return s;
}
