// The host controls' drafts and outbox, how an operation is frozen and
// delivered, and how a result is worded. No DOM: test/state.test.mjs runs it
// in node over a fake Storage.
//
// Everything is kept per account and plan, one storage key per draft and per
// operation, so two tabs saving different marks or submitting different
// operations never rewrite each other's keys:
//
//   ox-review:<account>:<plan>:draft:<anchor>   an unsent mark
//   ox-review:<account>:<plan>:op:<id>          a frozen operation, kept until a
//                                               receipt for its id says the host owns it
//   ox-review:<account>:<plan>:export           the last export's id and items

import { STATUSES, type Status } from './protocol.js';

/** A reviewer's unsent mark. Each save of an anchor stamps a later `at` than any copy of it saved or frozen before. */
export interface Mark {
  anchor: string;
  section?: string;
  label?: string;
  status: Status;
  note?: string;
  quote?: string;
  at: number;
}

/** What the reviewer did: submit their marks, reopen an addressed item, or accept one. */
export type Act = 'submit' | 'reopen' | 'accept';

/** One frozen operation. */
export interface Entry {
  id: string;
  act: Act;
  /** The request: these exact bytes are sent on every attempt. */
  body: string;
  /** When it was frozen; entries are sent oldest first. */
  at: number;
  /** anchor -> stamp of each draft it carries. Once owned, it clears those drafts unless they were edited since. */
  stamps: Record<string, number>;
}

/** One operation, as a transport sends it. */
export interface Operation {
  /** The account it belongs to: a transport must never send it as anyone else. */
  account: string;
  /** Minted once when the operation was frozen, the same on every attempt. */
  id: string;
  /** round: a submission or a reopen; resolution: an accept. */
  kind: 'round' | 'resolution';
  /** The frozen request. */
  body: string;
}

/** What a host answered for one operation. */
export type Receipt =
  /**
   * The host durably owns the operation's record, so the outbox may let it go:
   * a hosted transport once the record is published, ox's local server once
   * the record and its recovery entry are on disk. id names the operation the
   * host stored; any other id acknowledges nothing.
   */
  | { state: 'owned'; id?: string; synced: boolean; warning?: string }
  /** The host refuses these exact bytes, and always will: the entry is dropped and its drafts stay. */
  | { state: 'refused'; message: string }
  /** Not stored this time: the entry stays and is sent again. */
  | { state: 'failed'; message: string }
  /** The host is unreachable: the entry stays and is sent when it is back. */
  | { state: 'offline' };

/** How a host stores operations: its server, with the reviewer's credentials. */
export interface Transport {
  send(op: Operation): Promise<Receipt>;
}

export interface SyncMessage {
  kind: 'ok' | 'warn' | 'err';
  text: string;
}

/** A plan's drafts and outbox for one account. */
export interface Store {
  account: string;
  /** Every key of this account's plan starts with it, for telling another tab's storage events apart. */
  prefix: string;
  /** The unsent marks, oldest edit first. */
  drafts(): Mark[];
  /** save stamps and stores a draft. false: storage refused it, and the draft lives in this tab only. */
  save(m: Omit<Mark, 'at'>): boolean;
  remove(anchor: string): void;
  /** Whether the anchor's draft lives in this tab only. */
  unsaved(anchor: string): boolean;
  /** The frozen operations, oldest first. */
  entries(): Entry[];
  /** freeze stores an operation before anything sends it. null: storage refused it, and it must not be sent. */
  freeze(act: Act, id: string, body: string, stamps: Record<string, number>): Entry | null;
  /** owned clears an entry the host owns, with each draft it carried that was not edited since. */
  owned(e: Entry): void;
  /** drop clears an entry the host refused; its drafts stay. */
  drop(e: Entry): void;
  /** exportId is the id an export of items carries: the last export's while its items are unchanged. */
  exportId(items: unknown[]): string;
}

const ACTS = ['submit', 'reopen', 'accept'];
const DRAFT = 'draft:', OP = 'op:', EXPORT = 'export';
const enc = encodeURIComponent;
const has = (o: object, k: string) => Object.prototype.hasOwnProperty.call(o, k);

function obj(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}
function read(storage: Storage, key: string): unknown {
  try {
    const s = storage.getItem(key);
    return s === null ? null : JSON.parse(s);
  } catch (e) {
    return null;
  }
}
// write reports whether storage now holds v: setItem throws when storage is
// full or denied, and reading it back catches one that drops writes silently.
function write(storage: Storage, key: string, v: unknown): boolean {
  const s = JSON.stringify(v);
  try {
    storage.setItem(key, s);
    return storage.getItem(key) === s;
  } catch (e) {
    return false;
  }
}
function unset(storage: Storage, key: string): void {
  try { storage.removeItem(key); } catch (e) { /* storage denied: nothing was stored either */ }
}
function keysUnder(storage: Storage, prefix: string): string[] {
  const out: string[] = [];
  try {
    for (let i = 0; i < storage.length; i++) {
      const k = storage.key(i);
      if (k !== null && k.indexOf(prefix) === 0) out.push(k);
    }
  } catch (e) { /* storage denied: nothing to list */ }
  return out;
}
function asMark(v: unknown): Mark | null {
  if (!obj(v) || typeof v.anchor !== 'string' || STATUSES.indexOf(v.status as Status) < 0 || typeof v.at !== 'number') return null;
  const m: Mark = { anchor: v.anchor, status: v.status as Status, at: v.at };
  (['section', 'label', 'note', 'quote'] as const).forEach((k) => { if (typeof v[k] === 'string') m[k] = v[k] as string; });
  return m;
}
function asEntry(v: unknown): Entry | null {
  if (!obj(v) || typeof v.id !== 'string' || ACTS.indexOf(v.act as string) < 0 || typeof v.body !== 'string' || typeof v.at !== 'number' || !obj(v.stamps)) return null;
  const stamps: Record<string, number> = Object.create(null);
  Object.keys(v.stamps).forEach((a) => { const s = (v.stamps as Record<string, unknown>)[a]; if (typeof s === 'number') stamps[a] = s; });
  return { id: v.id, act: v.act as Act, body: v.body, at: v.at, stamps };
}

/** openStore is the drafts and outbox of one account's review of one plan, kept in storage. */
export function openStore(storage: Storage, account: string, plan: string): Store {
  const prefix = 'ox-review:' + enc(account) + ':' + enc(plan) + ':';
  // drafts storage refused, kept for as long as this tab lives
  const memory: Record<string, Mark> = Object.create(null);
  let last = 0;

  function stored(anchor: string): Mark | null {
    const m = asMark(read(storage, prefix + DRAFT + enc(anchor)));
    return m && m.anchor === anchor ? m : null;
  }
  function entries(): Entry[] {
    const out: Entry[] = [];
    keysUnder(storage, prefix + OP).forEach((k) => { const e = asEntry(read(storage, k)); if (e && prefix + OP + enc(e.id) === k) out.push(e); });
    return out.sort((x, y) => x.at - y.at || (x.id < y.id ? -1 : x.id > y.id ? 1 : 0));
  }
  function now(): number {
    last = Math.max(Date.now(), last + 1);
    return last;
  }

  return {
    account,
    prefix,
    drafts() {
      const out: Record<string, Mark> = Object.create(null);
      keysUnder(storage, prefix + DRAFT).forEach((k) => { const m = asMark(read(storage, k)); if (m && prefix + DRAFT + enc(m.anchor) === k) out[m.anchor] = m; });
      Object.keys(memory).forEach((a) => { if (!out[a] || memory[a].at > out[a].at) out[a] = memory[a]; });
      return Object.keys(out).map((a) => out[a]).sort((x, y) => x.at - y.at);
    },
    save(m) {
      // later than every copy of this anchor, saved by any tab or frozen into
      // an entry, so an acknowledgment of an older copy never clears it
      let at = now();
      const prev = stored(m.anchor);
      if (prev) at = Math.max(at, prev.at + 1);
      if (memory[m.anchor]) at = Math.max(at, memory[m.anchor].at + 1);
      entries().forEach((e) => { if (has(e.stamps, m.anchor)) at = Math.max(at, e.stamps[m.anchor] + 1); });
      last = at;
      const mark: Mark = { anchor: m.anchor, status: m.status, at };
      (['section', 'label', 'note', 'quote'] as const).forEach((k) => { if (m[k] !== undefined) mark[k] = m[k]; });
      if (write(storage, prefix + DRAFT + enc(m.anchor), mark)) {
        delete memory[m.anchor];
        return true;
      }
      memory[m.anchor] = mark;
      return false;
    },
    remove(anchor) {
      unset(storage, prefix + DRAFT + enc(anchor));
      delete memory[anchor];
    },
    unsaved(anchor) {
      if (!memory[anchor]) return false;
      const s = stored(anchor);
      return !s || memory[anchor].at > s.at;
    },
    entries,
    freeze(act, id, body, stamps) {
      const e: Entry = { id, act, body, at: now(), stamps };
      return write(storage, prefix + OP + enc(id), e) ? e : null;
    },
    owned(e) {
      // drafts first: a crash before the entry is cleared leaves an entry
      // whose resend the host answers as already stored, never lost marks
      Object.keys(e.stamps).forEach((a) => {
        const s = stored(a);
        if (s && s.at <= e.stamps[a]) unset(storage, prefix + DRAFT + enc(a));
        if (memory[a] && memory[a].at <= e.stamps[a]) delete memory[a];
      });
      unset(storage, prefix + OP + enc(e.id));
    },
    drop(e) {
      unset(storage, prefix + OP + enc(e.id));
    },
    exportId(items) {
      const prev = read(storage, prefix + EXPORT);
      const id = obj(prev) && typeof prev.id === 'string' && JSON.stringify(prev.items) === JSON.stringify(items) ? prev.id : newId();
      write(storage, prefix + EXPORT, { id, items }); // unsaved: the next export mints a new id
      return id;
    },
  };
}

/** queued: whether an entry carries this draft as it is now. */
export function queued(entries: Entry[], m: Mark): boolean {
  return entries.some((e) => has(e.stamps, m.anchor) && e.stamps[m.anchor] === m.at);
}

/** The item a round carries for a mark, without its stamp. */
export function wireItem(m: Pick<Mark, 'anchor' | 'section' | 'label' | 'status' | 'note' | 'quote'>): Record<string, string> {
  const o: Record<string, string> = { anchor: m.anchor, section: m.section || '', label: m.label || '' };
  if (m.quote) o.quote = m.quote;
  o.status = m.status;
  o.note = m.note || '';
  return o;
}

// A request names its reviewer only for a host that has no sign-in (ox's
// local review); a host that authenticates passes '' and stamps the caller.
function request(id: string, reviewer: string, rest: Record<string, unknown>): string {
  const o: Record<string, unknown> = { id };
  if (reviewer) o.reviewer = reviewer;
  return JSON.stringify(Object.assign(o, rest));
}

/** submitBody freezes a round of marks. */
export function submitBody(id: string, reviewer: string, marks: Mark[]): string {
  return request(id, reviewer, { items: marks.map(wireItem) });
}

/** reopenBody freezes a reopen: a round raising one item again as a requested change. */
export function reopenBody(id: string, reviewer: string, item: { anchor: string; section?: string; label?: string; quote?: string }, note: string): string {
  return request(id, reviewer, { items: [wireItem({ ...item, status: 'request-change', note: note || 'reopened by reviewer' })] });
}

/** acceptBody freezes a reviewer's accept: a verified resolution. */
export function acceptBody(id: string, reviewer: string, anchor: string): string {
  return request(id, reviewer, { anchor, state: 'verified', note: 'accepted by reviewer' });
}

/**
 * deliver sends the entries one at a time, oldest first, so a host stores
 * them in the order they were frozen: a mark edited after Submit is stored
 * after the copy Submit froze, and so reads as the latest. It stops at the
 * first entry that fails or finds the host offline. One the host refuses is
 * dropped, and the rest still go. report hears every answer.
 */
export async function deliver(store: Store, transport: Transport, report: (e: Entry, r: Receipt) => void, stopped: () => boolean): Promise<void> {
  const tried: Record<string, true> = Object.create(null);
  for (;;) {
    const e = store.entries().filter((x) => !tried[x.id])[0];
    if (!e || stopped()) return;
    tried[e.id] = true; // an entry storage could not clear is not resent in a loop
    let r: Receipt;
    try {
      r = await transport.send({ account: store.account, id: e.id, kind: e.act === 'accept' ? 'resolution' : 'round', body: e.body });
    } catch (err) {
      r = { state: 'failed', message: err instanceof Error ? err.message : String(err) };
    }
    if (r.state === 'owned' && r.id !== e.id) r = { state: 'failed', message: 'the server acknowledged a different operation' };
    if (r.state === 'owned') store.owned(e);
    else if (r.state === 'refused') store.drop(e);
    report(e, r);
    if (r.state === 'failed' || r.state === 'offline') return;
  }
}

// syncMessage words what a host owns truthfully: "synced" only when the
// record reached the team's Ledger.
export function syncMessage(verb: string, r: { synced: boolean; warning?: string }): SyncMessage {
  const m: SyncMessage = r.synced
    ? { kind: 'ok', text: verb + ' · synced to your team' }
    : { kind: 'warn', text: 'Saved on the author’s machine · not yet synced (will retry)' };
  if (r.warning) m.text += ' · ' + r.warning;
  return m;
}

// newId mints an operation id; hosts accept ^[A-Za-z0-9_-]{8,64}$.
export function newId(): string {
  const c = typeof crypto !== 'undefined' ? crypto : null;
  if (c && c.randomUUID) return c.randomUUID();
  const b = new Uint8Array(16);
  let s = '';
  if (c && c.getRandomValues) c.getRandomValues(b);
  else for (let i = 0; i < 16; i++) b[i] = Math.floor(Math.random() * 256); // no Web Crypto: still unique enough to dedupe one reviewer's retries
  for (let j = 0; j < 16; j++) s += ('0' + b[j].toString(16)).slice(-2);
  return s;
}

/**
 * migrateLegacy moves what review pages before @sageox/plan-review 0.3.0 kept
 * under a plan's slug into store: unsent marks become drafts, and a
 * submission still waiting for its answer becomes an entry holding the bytes
 * those pages sent, so a resend is recognized as the same round.
 */
export function migrateLegacy(storage: Storage, slug: string, store: Store): void {
  const KEY = 'ox-plan-fb:' + slug;
  const marks = read(storage, KEY), box = read(storage, KEY + ':outbox'), exp = read(storage, KEY + ':export');
  if (marks === null && box === null && exp === null) return;
  let ok = true;
  const current = store.drafts();
  if (obj(marks)) {
    Object.keys(marks).forEach((a) => {
      const v = marks[a];
      // stamps kept, so the migrated entry still clears the drafts it carried
      const m = asMark(obj(v) ? { ...v, anchor: a, at: Math.max(+(v.updated_at as number) || 0, 1) } : null);
      if (!m || current.some((d) => d.anchor === a && d.at >= m.at)) return;
      ok = write(storage, store.prefix + DRAFT + enc(a), m) && ok;
    });
  }
  if (obj(box) && typeof box.id === 'string' && Array.isArray(box.items)) {
    const stamps: Record<string, number> = Object.create(null);
    const s = obj(box.stamps) ? box.stamps : {};
    box.items.forEach((it) => { if (obj(it) && typeof it.anchor === 'string') stamps[it.anchor] = Math.max(+(s[it.anchor] as number) || 0, 1); });
    // what those pages sent: JSON.stringify({ id, slug, reviewer, items })
    const body = JSON.stringify({ id: box.id, slug, reviewer: box.reviewer, items: box.items });
    const e: Entry = { id: box.id, act: 'submit', body, at: Date.parse(box.created_at as string) || Date.now(), stamps };
    ok = write(storage, store.prefix + OP + enc(e.id), e) && ok;
  }
  if (obj(exp)) ok = write(storage, store.prefix + EXPORT, exp) && ok;
  // kept until every copy is written, so a full storage loses nothing
  if (ok) ['', ':tombs', ':outbox', ':export'].forEach((k) => unset(storage, KEY + k));
}
