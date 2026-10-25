// Live pointers, inside the plan document. The reviewer's pointer is shared
// as the content under it (PointerAt in protocol.ts), never as screen
// coordinates, and another reviewer's location is turned back into a point in
// this document's viewport, unless this render cannot show that content. The
// host draws the cursors and the names; like the rest of the document tools,
// this reads no storage and makes no request.

import { markables, shownBox } from './draw.js';
import { MAX_PATH, type Located, type PointerAt } from './protocol.js';
import { r1 } from './region.js';

export interface PointerLayer {
  /** Places these pointers: answered now, and again whenever the layout moves one. A pointer this render cannot show is left out. */
  locate(points: Array<{ key: string; at: PointerAt }>): void;
  /** The page is ready to anchor on: its diagrams are drawn. Nothing is shared or placed before. */
  start(): void;
}

// What is not the plan's own: marks the review layer paints in, and markup
// that never renders. anchorText leaves out the same.
const SKIP = '.rev-glyph, script, style, noscript, template';
const STEP = 50; // ms between captures of a moving pointer

const kids = (el: Element): Element[] => Array.prototype.filter.call(el.children, (c: Element) => !c.matches(SKIP)) as Element[];
const nonSpace = (s: string) => s.replace(/\s+/g, '').length;
const frac = (d: number, size: number) => (size >= 1 ? Math.round(Math.min(1, Math.max(0, d / size)) * 1000) / 1000 : 0.5);
const words = (root: Element) => document.createTreeWalker(root, NodeFilter.SHOW_ELEMENT | NodeFilter.SHOW_TEXT, {
  acceptNode: (n) => (n.nodeType === 3 ? NodeFilter.FILTER_ACCEPT : (n as Element).matches(SKIP) ? NodeFilter.FILTER_REJECT : NodeFilter.FILTER_SKIP),
});

// caretAt is the text position under a point: caretPositionFromPoint where
// the browser has it, else WebKit's caretRangeFromPoint.
function caretAt(x: number, y: number): [Node, number] | null {
  const d = document as unknown as {
    caretPositionFromPoint?: (x: number, y: number) => { offsetNode: Node; offset: number } | null;
    caretRangeFromPoint?: (x: number, y: number) => Range | null;
  };
  if (d.caretPositionFromPoint) {
    const p = d.caretPositionFromPoint(x, y);
    return p ? [p.offsetNode, p.offset] : null;
  }
  const r = d.caretRangeFromPoint ? d.caretRangeFromPoint(x, y) : null;
  return r ? [r.startContainer, r.startOffset] : null;
}
function charBox(node: Text, i: number): DOMRect | null {
  if (i < 0 || i >= node.data.length) return null;
  const range = document.createRange();
  range.setStart(node, i);
  range.setEnd(node, i + 1);
  const r = range.getBoundingClientRect();
  return r.height ? r : null;
}
// pathTo is the child path from host down to el, or null when el sits in
// markup the review layer added; follow walks it back.
function pathTo(host: Element, el: Element): number[] | null {
  const path: number[] = [];
  for (let n = el; n !== host; ) {
    const p = n.parentElement;
    const i = p ? kids(p).indexOf(n) : -1;
    if (!p || i < 0 || path.length === MAX_PATH) return null;
    path.unshift(i);
    n = p;
  }
  return path;
}
function follow(host: Element, path: number[]): Element | null {
  let n: Element | undefined = host;
  for (let i = 0; n && i < path.length; i++) n = kids(n)[path[i]];
  return n || null;
}

/**
 * pointerLayer shares this document's pointer through moved, and places other
 * reviewers' pointers through located, among the elements
 * markables(selector, exclude) lists.
 */
export function pointerLayer(selector: string, exclude: string, moved: (at: PointerAt | null) => void, located: (points: Located[]) => void): PointerLayer {
  let started = false;
  // what can be pointed at, by element and by anchor, until the plan changes
  let index: { of: Map<Element, string>; by: Record<string, Element[]> } | null = null;
  function idx() {
    if (index) return index;
    const of = new Map<Element, string>(), by: Record<string, Element[]> = Object.create(null);
    markables(selector, exclude).forEach(({ el, anchor }) => { of.set(el, anchor); (by[anchor] = by[anchor] || []).push(el); });
    return (index = { of, by });
  }

  // --- sharing: the content under this reviewer's pointer ---
  let point: [number, number] | null = null; // the pointer, in viewport pixels, while it is over this document
  let sent = 'null'; // the location last shared, as JSON
  let timer: ReturnType<typeof setTimeout> | undefined;
  let last = 0;
  function share() {
    if (timer !== undefined || !started) return;
    timer = setTimeout(() => {
      timer = undefined;
      last = Date.now();
      const at = point ? locationAt(point[0], point[1]) : null;
      const s = JSON.stringify(at);
      if (s !== sent) { sent = s; moved(at); }
    }, Math.max(0, last + STEP - Date.now()));
  }
  function locationAt(cx: number, cy: number): PointerAt | null {
    // what the reader sees there, looking through the ink layer
    let hit: Element | null = null;
    for (const e of document.elementsFromPoint(cx, cy)) if (!e.closest('.rev-ink')) { hit = e; break; }
    // off the plan, or on the host's controls
    if (!hit || hit.closest(exclude)) return null;
    const host = hit.closest(selector), i = idx();
    const anchor = host && i.of.get(host);
    if (!host || !anchor) return null;
    const at: PointerAt = { anchor, x: 0.5, y: 0.5 };
    const same = i.by[anchor];
    if (same.length > 1) { at.nth = same.indexOf(host); at.of = same.length; }
    const path = pathTo(host, hit);
    if (path && path.length) at.path = path;
    const r = (path ? hit : host).getBoundingClientRect();
    at.x = frac(cx - r.left, r.width);
    at.y = frac(cy - r.top, r.height);
    const t = textAt(host, cx, cy);
    if (t !== undefined) at.text = t;
    return at;
  }
  // textAt is how many non-space characters of host precede the pointer when
  // it rests on host's words; undefined beside or between them, where a caret
  // still snaps to the nearest words.
  function textAt(host: Element, cx: number, cy: number): number | undefined {
    const c = caretAt(cx, cy);
    if (!c || c[0].nodeType !== 3 || !host.contains(c[0])) return undefined;
    const node = c[0] as Text, off = c[1];
    const r = charBox(node, off < node.data.length ? off : off - 1);
    if (!r || cy < r.top || cy > r.bottom || cx < r.left - r.height || cx > r.right + r.height) return undefined;
    let n = 0;
    const w = words(host);
    for (let t = w.nextNode(); t; t = w.nextNode()) {
      if (t === node) return n + nonSpace(node.data.slice(0, off));
      n += nonSpace((t as Text).data);
    }
    return undefined; // in a mark the review layer painted
  }

  // --- placing: where another reviewer's pointer lands on this render ---
  let wanted: Array<{ key: string; at: PointerAt }> = [];
  let told = '[]'; // the answer last sent, as JSON
  let frame = 0;
  function answer() {
    frame = 0;
    if (!started) return;
    const out: Located[] = [];
    wanted.forEach((p) => { const pt = place(p.at); if (pt) out.push({ key: p.key, x: pt[0], y: pt[1] }); });
    const s = JSON.stringify(out);
    if (s !== told) { told = s; located(out); }
  }
  // place is where at lands on this render, in viewport pixels; null when its
  // element is gone, repeats a different number of times than for the
  // sender, is collapsed or not shown, or the point is off screen.
  function place(at: PointerAt): [number, number] | null {
    const same = idx().by[at.anchor] || [];
    if (same.length !== (at.of || 1)) return null;
    const host = same[at.nth || 0];
    if (!shownBox(host)) return null;
    let pt = at.text === undefined ? null : textPoint(host, at.text);
    if (!pt) {
      const el = at.path ? follow(host, at.path) : host;
      if (!el || (el !== host && !shownBox(el))) return null;
      const r = el.getBoundingClientRect();
      pt = [r.left + at.x * r.width, r.top + at.y * r.height];
    }
    if (pt[0] < 0 || pt[1] < 0 || pt[0] > innerWidth || pt[1] > innerHeight) return null;
    return [r1(pt[0]), r1(pt[1])];
  }
  // textPoint is where this render draws host's character k non-space
  // characters in, or the end of its last one when k is past them.
  function textPoint(host: Element, k: number): [number, number] | null {
    let n = 0, end: [Text, number] | null = null;
    const w = words(host);
    for (let t = w.nextNode() as Text | null; t; t = w.nextNode() as Text | null) {
      const s = t.data;
      for (let i = 0; i < s.length; i++) {
        if (/\s/.test(s.charAt(i))) continue;
        if (n++ < k) { end = [t, i]; continue; }
        const r = t.parentElement && shownBox(t.parentElement) ? charBox(t, i) : null;
        return r ? [r.left, r.top + r.height / 2] : null;
      }
    }
    const r = n === k && end && end[0].parentElement && shownBox(end[0].parentElement) ? charBox(end[0], end[1]) : null;
    return r ? [r.right, r.top + r.height / 2] : null;
  }

  // The layout moved under the pointers: a scroll, a resize, a toggle, new
  // content. Placed again once a frame, and what lies under a still pointer
  // is shared again.
  function relayout() {
    if (wanted.length && !frame) frame = requestAnimationFrame(answer);
    if (point) share();
  }

  document.addEventListener('pointermove', (e) => {
    if (e.pointerType === 'touch') return; // a finger scrolls and taps; it does not point
    point = [e.clientX, e.clientY];
    share();
  }, { capture: true, passive: true });
  // the pointer left the window, or the frame
  document.addEventListener('pointerout', (e) => { if (!e.relatedTarget) { point = null; share(); } }, true);
  document.addEventListener('visibilitychange', () => { if (document.hidden) { point = null; share(); } });
  document.addEventListener('scroll', relayout, { capture: true, passive: true });
  window.addEventListener('resize', relayout);
  document.addEventListener('toggle', relayout, true);
  document.addEventListener('change', relayout, true);
  if (window.ResizeObserver) new ResizeObserver(relayout).observe(document.documentElement);
  // the plan's own content changed; the host's controls and the ink layer
  // drawn into this document are not the plan
  new MutationObserver((records) => {
    for (const r of records) {
      const el = r.target.nodeType === 1 ? (r.target as Element) : r.target.parentElement;
      if (el && el.closest(exclude + ', .rev-ink')) continue;
      index = null;
      relayout();
      return;
    }
  }).observe(document.documentElement, { childList: true, characterData: true, subtree: true });

  return {
    locate(points) {
      wanted = points;
      if (frame) cancelAnimationFrame(frame);
      told = ''; // always answered, so the host knows which placements are current
      answer();
    },
    start() {
      started = true;
      index = null;
      answer();
      share();
    },
  };
}
