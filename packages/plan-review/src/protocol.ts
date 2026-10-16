// The messages between a trusted host and the document tools running inside
// a plan document. The host owns review mode, the composer, drafts and
// submission; the document tools find what the reviewer points at and paint
// what the host tells them to. Nothing here can submit, approve, resolve or
// name a reviewer: those never leave the host.
//
// A framed document is untrusted, so every message from it goes through
// parseDocumentMessage, which returns a fresh object holding only the fields
// below, or null. Nothing the host sends may be secret from the plan: a
// plan's own script reads it and can carry it off by navigating its frame.
// So a paint names what to mark and how, never who marked it or what they
// wrote.

/** Version of these messages and of the review requests the package freezes. */
export const PROTOCOL = 2;

export type Status = 'approve' | 'request-change' | 'flag' | 'comment';
export type State = 'open' | 'addressed' | 'verified' | 'wontfix';
export type Tint = 'sage' | 'amber' | 'red' | 'gold' | 'faint';

/** One mark to paint: on an element ("h" anchor) or on a highlight's words ("q" anchor). */
export interface PaintItem {
  anchor: string;
  /** "unsent": the reviewer's draft; "committed": already in the Ledger. */
  kind: 'unsent' | 'committed';
  status: Status;
  state?: State;
  /** Margin glyph, inserted as text. */
  glyph: string;
  /** Highlight color. */
  tint: Tint;
  /** A highlight's words, its section heading and its label; absent for element marks. */
  quote?: string;
  section?: string;
  label?: string;
}

/** What the reviewer pointed at: an element, or the words they selected. */
export interface Target {
  anchor: string;
  section: string;
  label: string;
  quote?: string;
  /** Where the composer opens below, in the document's viewport pixels. */
  rect: { left: number; bottom: number };
}

/** A painted mark, in document order. */
export interface Placed {
  anchor: string;
  section: string;
  label: string;
  quote?: string;
}

export type HostMessage =
  | { type: 'mode'; on: boolean; open: boolean }
  | { type: 'paint'; items: PaintItem[] }
  | { type: 'focus'; anchor: string | null }
  | { type: 'reveal'; anchor: string; nth: number };

export type DocumentMessage =
  | { type: 'ready' }
  | { type: 'target'; target: Target }
  | { type: 'refused'; reason: 'cross-section' }
  | { type: 'refused'; reason: 'repeated'; quote: string; count: number }
  | { type: 'dismiss' }
  | { type: 'placed'; items: Placed[] }
  | { type: 'key'; key: 'Escape' | 'r' };

/** Every message travels inside this envelope; instance names one load of one framed document. */
export interface Envelope {
  ox: 'review';
  v: number;
  instance: string;
}

const MAX_TEXT = 65536; // UTF-16 units in any one string
const MAX_ITEMS = 10000;
// what the document tools compute and can place: "h" or "q" plus eight hex digits
const ANCHOR = /^[hq][0-9a-f]{8}$/;
/** Every Status, for validating one read back from storage or a message. */
export const STATUSES: readonly Status[] = ['approve', 'request-change', 'flag', 'comment'];
const STATES = ['open', 'addressed', 'verified', 'wontfix'];
const TINTS = ['sage', 'amber', 'red', 'gold', 'faint'];

type Obj = Record<string, unknown>;

function obj(v: unknown): v is Obj {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}
function text(v: unknown): v is string {
  return typeof v === 'string' && v.length <= MAX_TEXT;
}
function optText(v: unknown): boolean {
  return v === undefined || text(v);
}
function num(v: unknown): v is number {
  return typeof v === 'number' && isFinite(v);
}
function oneOf(v: unknown, list: readonly string[]): boolean {
  return typeof v === 'string' && list.indexOf(v) >= 0;
}
function list(v: unknown): v is unknown[] {
  return Array.isArray(v) && v.length <= MAX_ITEMS;
}

// open checks the envelope and returns the message body, or null.
function open(data: unknown, instance: string): Obj | null {
  if (!obj(data) || data.ox !== 'review' || data.v !== PROTOCOL || data.instance !== instance) return null;
  return typeof data.type === 'string' ? data : null;
}

/** envelope wraps a message for postMessage. */
export function envelope<M extends { type: string }>(msg: M, instance: string): M & Envelope {
  return Object.assign({ ox: 'review' as const, v: PROTOCOL, instance }, msg);
}

/** parseDocumentMessage validates a message from the document tools; null when it is not one. */
export function parseDocumentMessage(data: unknown, instance: string): DocumentMessage | null {
  const d = open(data, instance);
  if (!d) return null;
  switch (d.type) {
    case 'ready':
    case 'dismiss':
      return { type: d.type };
    case 'target': {
      const t = d.target;
      if (!obj(t) || typeof t.anchor !== 'string' || !ANCHOR.test(t.anchor)) return null;
      if (!text(t.section) || !text(t.label) || !optText(t.quote)) return null;
      // a highlight is its words; an element mark has none
      if ((t.anchor.charAt(0) === 'q') !== (t.quote !== undefined)) return null;
      const r = t.rect;
      if (!obj(r) || !num(r.left) || !num(r.bottom)) return null;
      const target: Target = { anchor: t.anchor, section: t.section, label: t.label, rect: { left: r.left, bottom: r.bottom } };
      if (t.quote !== undefined) target.quote = t.quote as string;
      return { type: 'target', target };
    }
    case 'refused':
      if (d.reason === 'cross-section') return { type: 'refused', reason: 'cross-section' };
      if (d.reason === 'repeated' && text(d.quote) && num(d.count) && d.count >= 2 && Math.floor(d.count) === d.count) {
        return { type: 'refused', reason: 'repeated', quote: d.quote, count: d.count };
      }
      return null;
    case 'placed': {
      if (!list(d.items)) return null;
      const items: Placed[] = [];
      for (const it of d.items) {
        if (!obj(it) || typeof it.anchor !== 'string' || !ANCHOR.test(it.anchor)) return null;
        if (!text(it.section) || !text(it.label) || !optText(it.quote)) return null;
        const p: Placed = { anchor: it.anchor, section: it.section, label: it.label };
        if (it.quote !== undefined) p.quote = it.quote as string;
        items.push(p);
      }
      return { type: 'placed', items };
    }
    case 'key':
      return d.key === 'Escape' || d.key === 'r' ? { type: 'key', key: d.key } : null;
  }
  return null;
}

/** parseHostMessage validates a message from the host; null when it is not one. */
export function parseHostMessage(data: unknown, instance: string): HostMessage | null {
  const d = open(data, instance);
  if (!d) return null;
  switch (d.type) {
    case 'mode':
      return typeof d.on === 'boolean' && typeof d.open === 'boolean' ? { type: 'mode', on: d.on, open: d.open } : null;
    case 'focus':
      if (d.anchor === null) return { type: 'focus', anchor: null };
      return typeof d.anchor === 'string' && ANCHOR.test(d.anchor) ? { type: 'focus', anchor: d.anchor } : null;
    case 'reveal':
      if (typeof d.anchor !== 'string' || !ANCHOR.test(d.anchor)) return null;
      return num(d.nth) && d.nth >= 0 && Math.floor(d.nth) === d.nth ? { type: 'reveal', anchor: d.anchor, nth: d.nth } : null;
    case 'paint': {
      if (!list(d.items)) return null;
      const items: PaintItem[] = [];
      for (const it of d.items) {
        if (!obj(it) || typeof it.anchor !== 'string' || !ANCHOR.test(it.anchor)) return null;
        if ((it.kind !== 'unsent' && it.kind !== 'committed') || !oneOf(it.status, STATUSES) || !oneOf(it.tint, TINTS)) return null;
        if ((it.state !== undefined && !oneOf(it.state, STATES)) || !text(it.glyph)) return null;
        if (!optText(it.quote) || !optText(it.section) || !optText(it.label)) return null;
        const p: PaintItem = { anchor: it.anchor, kind: it.kind, status: it.status as Status, glyph: it.glyph, tint: it.tint as Tint };
        if (it.state !== undefined) p.state = it.state as State;
        if (it.quote !== undefined) p.quote = it.quote as string;
        if (it.section !== undefined) p.section = it.section as string;
        if (it.label !== undefined) p.label = it.label as string;
        items.push(p);
      }
      return { type: 'paint', items };
    }
  }
  return null;
}
