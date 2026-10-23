// The messages between a trusted host and the document tools running inside
// a plan document. The host owns review mode, the composer, drafts and
// submission; the document tools find what the reviewer points at or draws
// around, and paint what the host tells them to. Nothing here can submit,
// approve, resolve or name a reviewer: those never leave the host.
//
// A framed document is untrusted, so every message from it goes through
// parseDocumentMessage, which returns a fresh object holding only the fields
// below, or null. Nothing the host sends may be secret from the plan: a
// plan's own script reads it and can carry it off by navigating its frame.
// So a paint names what to mark and how, never who marked it or what they
// wrote.

import { all, captureOf, DRAWING, LIMITS, type Box, type Capture, type Ellipse } from './region.js';

/** Version of these messages and of the review requests the package freezes. */
export const PROTOCOL = 3;

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

/**
 * One drawing to place and paint (anchor "q-<id>"): what placement needs from
 * its record, never who drew it or what they wrote.
 */
export interface DrawingPaint {
  anchor: string;
  kind: 'unsent' | 'committed';
  tint: Tint;
  ellipse: Ellipse;
  targets: Array<{ anchor: string; box: Box; nth?: number; of?: number }>;
  /** What capture recorded as open and checked, which Show restores. */
  open: string[];
  checked: string[];
}

/**
 * Where a drawing landed on this render (drawn-context spec, section 3):
 * exact or moved, painted where it was drawn; reflowed, its targets outlined;
 * hidden, a target collapsed or on another tab; missing, a target's text is
 * gone; ambiguous, a target's text occurs a different number of times than
 * when it was drawn. Missing and ambiguous drawings are not on the page.
 */
export type Outcome = 'exact' | 'moved' | 'reflowed' | 'hidden' | 'missing' | 'ambiguous';

/** Why a stroke made no capture. */
export type DrawRefusal = 'no-target' | 'too-large' | 'too-small' | 'layout-changed' | 'cancelled';

/** What the reviewer pointed at: an element, or the words they selected. */
export interface Target {
  anchor: string;
  section: string;
  label: string;
  quote?: string;
  /** Where the composer opens below, in the document's viewport pixels. */
  rect: { left: number; bottom: number };
}

/** A painted mark or placed drawing, in document order; drawings not on the page come last. */
export interface Placed {
  anchor: string;
  section: string;
  label: string;
  quote?: string;
  /** A drawing's outcome; absent for marks. */
  outcome?: Outcome;
}

export type HostMessage =
  | { type: 'mode'; on: boolean; open: boolean; draw: boolean }
  | { type: 'paint'; items: PaintItem[]; drawings: DrawingPaint[] }
  | { type: 'focus'; anchor: string | null }
  | { type: 'reveal'; anchor: string; nth: number };

export type DocumentMessage =
  | { type: 'ready' }
  | { type: 'target'; target: Target }
  | { type: 'capture'; capture: Capture; rect: { left: number; bottom: number }; anchor?: string }
  | { type: 'refused'; reason: 'cross-section' }
  | { type: 'refused'; reason: 'repeated'; quote: string; count: number }
  | { type: 'refused'; reason: DrawRefusal }
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
/** Every State, for validating one a host read back from its store. */
export const STATES: readonly State[] = ['open', 'addressed', 'verified', 'wontfix'];
const TINTS = ['sage', 'amber', 'red', 'gold', 'faint'];
const OUTCOMES = ['exact', 'moved', 'reflowed', 'hidden', 'missing', 'ambiguous'];
const DRAW_REFUSALS = ['no-target', 'too-large', 'too-small', 'layout-changed', 'cancelled'];

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
function nums(v: unknown, n: number): v is number[] {
  return Array.isArray(v) && v.length === n && all(v, num);
}
function oneOf(v: unknown, list: readonly string[]): boolean {
  return typeof v === 'string' && list.indexOf(v) >= 0;
}
function list(v: unknown): v is unknown[] {
  return Array.isArray(v) && v.length <= MAX_ITEMS;
}
function rect(r: unknown): { left: number; bottom: number } | null {
  return obj(r) && num(r.left) && num(r.bottom) ? { left: r.left, bottom: r.bottom } : null;
}
// a mark or a drawing: what reveal, focus and placed may name
function markAnchor(a: unknown): a is string {
  return typeof a === 'string' && (ANCHOR.test(a) || DRAWING.test(a));
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
      const r = rect(t.rect);
      if (!r) return null;
      const target: Target = { anchor: t.anchor, section: t.section, label: t.label, rect: r };
      if (t.quote !== undefined) target.quote = t.quote as string;
      return { type: 'target', target };
    }
    case 'capture': {
      const c = captureOf(d.capture), r = rect(d.rect);
      if (!c || !r || (d.anchor !== undefined && (typeof d.anchor !== 'string' || !DRAWING.test(d.anchor)))) return null;
      const message: DocumentMessage = { type: 'capture', capture: c, rect: r };
      if (typeof d.anchor === 'string') message.anchor = d.anchor;
      return message;
    }
    case 'refused':
      if (d.reason === 'cross-section') return { type: 'refused', reason: 'cross-section' };
      if (d.reason === 'repeated' && text(d.quote) && num(d.count) && d.count >= 2 && Math.floor(d.count) === d.count) {
        return { type: 'refused', reason: 'repeated', quote: d.quote, count: d.count };
      }
      if (oneOf(d.reason, DRAW_REFUSALS)) return { type: 'refused', reason: d.reason as DrawRefusal };
      return null;
    case 'placed': {
      if (!list(d.items)) return null;
      const items: Placed[] = [];
      for (const it of d.items) {
        if (!obj(it) || !markAnchor(it.anchor)) return null;
        if (!text(it.section) || !text(it.label) || !optText(it.quote)) return null;
        // a drawing comes with its outcome, and only a drawing
        if (DRAWING.test(it.anchor) !== oneOf(it.outcome, OUTCOMES)) return null;
        const p: Placed = { anchor: it.anchor, section: it.section, label: it.label };
        if (it.quote !== undefined) p.quote = it.quote as string;
        if (it.outcome !== undefined) p.outcome = it.outcome as Outcome;
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
      return typeof d.on === 'boolean' && typeof d.open === 'boolean' && typeof d.draw === 'boolean' ? { type: 'mode', on: d.on, open: d.open, draw: d.draw } : null;
    case 'focus':
      if (d.anchor === null) return { type: 'focus', anchor: null };
      return markAnchor(d.anchor) ? { type: 'focus', anchor: d.anchor } : null;
    case 'reveal':
      if (!markAnchor(d.anchor)) return null;
      return num(d.nth) && d.nth >= 0 && Math.floor(d.nth) === d.nth ? { type: 'reveal', anchor: d.anchor, nth: d.nth } : null;
    case 'paint': {
      if (!list(d.items) || !list(d.drawings)) return null;
      const items: PaintItem[] = [];
      for (const it of d.items) {
        const p = paintItem(it);
        if (!p) return null;
        items.push(p);
      }
      const drawings: DrawingPaint[] = [];
      for (const it of d.drawings) {
        const p = drawingPaint(it);
        if (!p) return null;
        drawings.push(p);
      }
      return { type: 'paint', items, drawings };
    }
  }
  return null;
}

/** paintItem validates one item a host paints; null when it is not one. A host checks each before it sends, since one bad item drops the whole paint. */
export function paintItem(it: unknown): PaintItem | null {
  if (!obj(it) || typeof it.anchor !== 'string' || !ANCHOR.test(it.anchor)) return null;
  if ((it.kind !== 'unsent' && it.kind !== 'committed') || !oneOf(it.status, STATUSES) || !oneOf(it.tint, TINTS)) return null;
  if ((it.state !== undefined && !oneOf(it.state, STATES)) || !text(it.glyph)) return null;
  if (!optText(it.quote) || !optText(it.section) || !optText(it.label)) return null;
  const p: PaintItem = { anchor: it.anchor, kind: it.kind, status: it.status as Status, glyph: it.glyph, tint: it.tint as Tint };
  if (it.state !== undefined) p.state = it.state as State;
  if (it.quote !== undefined) p.quote = it.quote as string;
  if (it.section !== undefined) p.section = it.section as string;
  if (it.label !== undefined) p.label = it.label as string;
  return p;
}

/** drawingPaint validates one drawing a host paints, as a fresh object; null when it is not one. */
export function drawingPaint(it: unknown): DrawingPaint | null {
  if (!obj(it) || typeof it.anchor !== 'string' || !DRAWING.test(it.anchor)) return null;
  if ((it.kind !== 'unsent' && it.kind !== 'committed') || !oneOf(it.tint, TINTS)) return null;
  const e = it.ellipse;
  if (!obj(e) || !nums(e.center, 2) || !nums(e.radii, 2) || !num(e.rotation) || !num(e.start) || (e.dir !== 1 && e.dir !== -1)) return null;
  const strs = (v: unknown) => Array.isArray(v) && v.length <= LIMITS.layoutKeys && all(v, text);
  if (!Array.isArray(it.targets) || !it.targets.length || it.targets.length > LIMITS.targets || !strs(it.open) || !strs(it.checked)) return null;
  const targets: DrawingPaint['targets'] = [];
  for (const t of it.targets) {
    if (!obj(t) || typeof t.anchor !== 'string' || !ANCHOR.test(t.anchor) || t.anchor.charAt(0) !== 'h' || !nums(t.box, 4)) return null;
    const o: DrawingPaint['targets'][number] = { anchor: t.anchor, box: [t.box[0], t.box[1], t.box[2], t.box[3]] };
    if (t.nth !== undefined || t.of !== undefined) {
      if (!num(t.nth) || !num(t.of)) return null;
      o.nth = t.nth;
      o.of = t.of;
    }
    targets.push(o);
  }
  return {
    anchor: it.anchor, kind: it.kind, tint: it.tint as Tint,
    ellipse: { center: [e.center[0], e.center[1]], radii: [e.radii[0], e.radii[1]], rotation: e.rotation, start: e.start, dir: e.dir },
    targets, open: (it.open as string[]).slice(), checked: (it.checked as string[]).slice(),
  };
}
