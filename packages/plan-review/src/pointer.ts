// Live pointers and selections, inside the plan document. The reviewer's
// pointer is shared as the content under it (PointerAt in protocol.ts), and
// the words they select as two places in the text (SelectionAt), never as
// screen coordinates. Another reviewer's location is turned back into a point
// in this document's viewport, unless this render cannot show that content,
// and their selection back into the same words, tinted in the color the host
// gave them. The host draws the cursors and the names; like the rest of the
// document tools, this reads no storage and makes no request.

import { markables, shownBox } from './draw.js';
import { COLORS, MAX_PATH, type Located, type PointerAt, type SelectionAt, type TextAt, type Tinted } from './protocol.js';
import { r1 } from './region.js';

export interface PointerLayer {
  /** Places these pointers: answered now, and again whenever the layout moves one. A pointer this render cannot show is left out. */
  locate(points: Array<{ key: string; at: PointerAt }>): void;
  /** Tints these selections, each in its color, until the next call. A selection this render cannot find is left out. */
  selections(items: Tinted[]): void;
  /** The page is ready to anchor on: its diagrams are drawn. Nothing is shared, placed or tinted before. */
  start(): void;
}

// What is not the plan's own: marks the review layer paints in, and markup
// that never renders. anchorText leaves out the same.
const SKIP = '.rev-glyph, script, style, noscript, template';
const STEP = 50; // ms between captures of a moving pointer
const REHASH_EVERY = 250; // ms: the most often a plan that keeps changing has its anchors hashed again

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
// charsBefore is how many non-space characters of host precede offset off of
// its text node; undefined when node sits in a mark the review layer painted.
function charsBefore(host: Element, node: Text, off: number): number | undefined {
  let n = 0;
  const w = words(host);
  for (let t = w.nextNode(); t; t = w.nextNode()) {
    if (t === node) return n + nonSpace(node.data.slice(0, off));
    n += nonSpace((t as Text).data);
  }
  return undefined;
}
// charOf finds host's non-space character k, counting from 0: its text node
// and index. Just past the last one, it names the last one, as past.
function charOf(host: Element, k: number): { t: Text; i: number; past: boolean } | null {
  let n = 0, last: { t: Text; i: number; past: boolean } | null = null;
  const w = words(host);
  for (let t = w.nextNode() as Text | null; t; t = w.nextNode() as Text | null) {
    for (let i = 0; i < t.data.length; i++) {
      if (/\s/.test(t.data.charAt(i))) continue;
      if (n++ === k) return { t, i, past: false };
      last = { t, i, past: true };
    }
  }
  return n === k ? last : null;
}

/**
 * pointerLayer shares this document's pointer through moved and its selection
 * through selected, places other reviewers' pointers through located, and
 * tints their selections, among the elements markables(selector, exclude)
 * lists.
 */
export function pointerLayer(selector: string, exclude: string, moved: (at: PointerAt | null) => void, located: (points: Located[]) => void, selected: (sel: SelectionAt | null) => void): PointerLayer {
  // the CSS Custom Highlight API tints ranges without touching the page's
  // DOM; without it, other reviewers' selections are not shown
  const HL = !!(window.CSS && CSS.highlights && window.Highlight);
  let started = false;
  // what can be pointed at, by element and by anchor. A plan that keeps
  // changing (a ticking clock) is hashed again at most every REHASH_EVERY ms,
  // not every frame; once it settles, its last change is placed too.
  let index: { of: Map<Element, string>; by: Record<string, Element[]> } | null = null;
  let stale = false, hashed = 0;
  let rehash: ReturnType<typeof setTimeout> | undefined;
  function idx() {
    const wait = hashed + REHASH_EVERY - Date.now();
    if (index && stale && wait > 0) {
      if (rehash === undefined) rehash = setTimeout(() => { rehash = undefined; relayout(); }, wait);
      return index;
    }
    if (!index || stale) {
      const of = new Map<Element, string>(), by: Record<string, Element[]> = Object.create(null);
      markables(selector, exclude).forEach(({ el, anchor }) => { of.set(el, anchor); (by[anchor] = by[anchor] || []).push(el); });
      index = { of, by };
      stale = false;
      hashed = Date.now();
    }
    return index;
  }

  // nameOf names the innermost markable element holding el: its anchor and,
  // when that anchor repeats, which occurrence of how many. Null off the plan.
  function nameOf(el: Element): { host: Element; anchor: string; nth?: number; of?: number } | null {
    const host = el.closest(selector), i = idx();
    const anchor = host && i.of.get(host);
    if (!host || !anchor) return null;
    const same = i.by[anchor];
    return same.length > 1 ? { host, anchor, nth: same.indexOf(host), of: same.length } : { host, anchor };
  }

  // --- sharing: the content under this reviewer's pointer, and the words they selected ---
  let point: [number, number] | null = null; // the pointer, in viewport pixels, while it is over this document
  let sent = 'null'; // the location last shared, as JSON
  let selDirty = false; // the selection may have changed since it was last shared
  let selSent = 'null'; // the selection last shared, as JSON
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
      if (!selDirty) return;
      const sel = selectionNow(), j = JSON.stringify(sel);
      // read through an anchor index still waiting to be rebuilt, the
      // selection may name an anchor its element no longer has: read it
      // again when the rebuild's relayout comes
      selDirty = stale;
      if (j !== selSent) { selSent = j; selected(sel); }
    }, Math.max(0, last + STEP - Date.now()));
  }
  function locationAt(cx: number, cy: number): PointerAt | null {
    // what the reader sees there, looking through the ink layer
    let hit: Element | null = null;
    for (const e of document.elementsFromPoint(cx, cy)) if (!e.closest('.rev-ink')) { hit = e; break; }
    // off the plan, or on the host's controls
    if (!hit || hit.closest(exclude)) return null;
    const m = nameOf(hit);
    if (!m) return null;
    const host = m.host, at: PointerAt = { anchor: m.anchor, x: 0.5, y: 0.5 };
    if (m.of) { at.nth = m.nth; at.of = m.of; }
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
    return charsBefore(host, node, off);
  }
  // selectionNow is this reviewer's selection as two places in the plan's
  // text: from the first selected words to the last, past whitespace and any
  // text outside a markable element at either edge. Null when nothing is
  // selected, or nothing selected is on the plan.
  function selectionNow(): SelectionAt | null {
    const s = getSelection();
    if (!s || s.isCollapsed || !s.rangeCount) return null;
    const r = s.getRangeAt(0), root = r.commonAncestorContainer;
    const texts: Node[] = [];
    if (root.nodeType === 3) texts.push(root);
    else for (let w = document.createTreeWalker(root, NodeFilter.SHOW_TEXT), t = w.nextNode(); t; t = w.nextNode()) texts.push(t);
    const parts: Array<[Text, number, number]> = []; // each text node's selected part
    for (const n of texts) {
      const t = n as Text;
      if (!r.intersectsNode(t) || !t.parentElement || t.parentElement.closest(SKIP + ', ' + exclude)) continue;
      const from = t === r.startContainer ? r.startOffset : 0, to = t === r.endContainer ? r.endOffset : t.data.length;
      if (/\S/.test(t.data.slice(from, to))) parts.push([t, from, to]);
    }
    let start: TextAt | null = null, end: TextAt | null = null;
    for (let i = 0; i < parts.length && !start; i++) start = placeOf(parts[i][0], parts[i][1]);
    for (let i = parts.length - 1; i >= 0 && !end; i--) end = placeOf(parts[i][0], parts[i][2]);
    return start && end ? { start, end } : null;
  }
  // placeOf names a place in a text node by its markable element and how many
  // of that element's non-space characters precede it; null off the plan.
  function placeOf(node: Text, off: number): TextAt | null {
    const m = node.parentElement && nameOf(node.parentElement);
    const text = m ? charsBefore(m.host, node, off) : undefined;
    if (!m || text === undefined) return null;
    const t: TextAt = { anchor: m.anchor, text };
    if (m.of) { t.nth = m.nth; t.of = m.of; }
    return t;
  }

  // --- placing: where another reviewer's pointer lands on this render ---
  let wanted: Array<{ key: string; at: PointerAt }> = [];
  let told = '[]'; // the answer last sent, as JSON
  let frame = 0;
  function answer() {
    frame = 0;
    if (!started) return;
    // the plan's content changed under the tints: find their words again
    if (tinted.length && idx() !== tintedOn) tint();
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
    const c = charOf(host, k);
    const r = c && c.t.parentElement && shownBox(c.t.parentElement) ? charBox(c.t, c.i) : null;
    return c && r ? [c.past ? r.right : r.left, r.top + r.height / 2] : null;
  }

  // --- tinting: the words other reviewers have selected ---
  let tinted: Tinted[] = [];
  let tintedOn: object | null = null; // the anchor index the tints were found with
  function tint() {
    tintedOn = idx();
    if (!HL) return;
    const by: Range[][] = [];
    tinted.forEach((s) => { const r = rangeOf(s.sel); if (r) (by[s.color] = by[s.color] || []).push(r); });
    for (let c = 0; c < COLORS; c++) {
      if (!by[c]) { CSS.highlights.delete('rev-sel-c' + c); continue; }
      const hl = new Highlight();
      by[c].forEach((r) => hl.add(r));
      CSS.highlights.set('rev-sel-c' + c, hl);
    }
  }
  // rangeOf is where a selection's words are on this render; null when either
  // end cannot be placed, or nothing lies between them.
  function rangeOf(sel: SelectionAt): Range | null {
    const a = placeIn(sel.start, false), b = placeIn(sel.end, true);
    if (!a || !b) return null;
    const r = document.createRange();
    r.setStart(a[0], a[1]);
    r.setEnd(b[0], b[1]);
    return r.collapsed ? null : r;
  }
  // placeIn is where a place in the text falls on this render: a start just
  // before the character after it, an end just past the one before it. Null
  // when its element is gone, repeats a different number of times than for
  // the sender, or has fewer characters.
  function placeIn(t: TextAt, isEnd: boolean): [Text, number] | null {
    const same = idx().by[t.anchor] || [];
    if (same.length !== (t.of || 1)) return null;
    const back = isEnd && t.text > 0;
    const c = charOf(same[t.nth || 0], back ? t.text - 1 : t.text);
    if (!c || (back && c.past)) return null;
    return [c.t, c.past || back ? c.i + 1 : c.i];
  }

  // The layout moved under the pointers: a scroll, a resize, a toggle, new
  // content. Placed again once a frame, and what lies under a still pointer,
  // or a selection the content may have moved, is shared again.
  function relayout() {
    if ((wanted.length || tinted.length) && !frame) frame = requestAnimationFrame(answer);
    if (point || selDirty) share();
  }

  document.addEventListener('pointermove', (e) => {
    if (e.pointerType === 'touch') return; // a finger scrolls and taps; it does not point
    point = [e.clientX, e.clientY];
    share();
  }, { capture: true, passive: true });
  // the pointer left the window, or the frame
  document.addEventListener('pointerout', (e) => { if (!e.relatedTarget) { point = null; share(); } }, true);
  document.addEventListener('visibilitychange', () => { if (document.hidden) { point = null; share(); } });
  // the reviewer selected words, or let go of them
  document.addEventListener('selectionchange', () => { selDirty = true; share(); });
  // the document is going away, a reload or a navigation: nothing it sends
  // afterwards is heard, so its pointer leaves the plan now, not on a timer
  window.addEventListener('pagehide', () => {
    point = null;
    clearTimeout(timer);
    timer = undefined;
    if (sent !== 'null') { sent = 'null'; moved(null); }
    if (selSent !== 'null') { selSent = 'null'; selected(null); }
  });
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
      stale = true;
      selDirty = true; // the selected words may have changed with it
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
    selections(items) {
      tinted = items;
      if (started) tint();
    },
    start() {
      started = true;
      index = null; // answer() then tints with the rebuilt index
      selDirty = true; // words selected before the page was ready
      answer();
      share();
    },
  };
}
