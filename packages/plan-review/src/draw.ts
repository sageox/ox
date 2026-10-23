// Draw around, in the plan document: a dragged oval becomes a capture, and the
// drawings the host paints are placed and inked. A capture is a proposal: the
// host checks it, mints its id and stamps its revision. The format is
// docs/specs/plan-review-drawn-context.md; this follows the prototype in
// pkg/planreview/testdata/drawn-context/proto/draw.js.

import { anchorFor, fnv1a, headingOf, norm } from './anchor.js';
import type { DrawRefusal, DrawingPaint, Outcome } from './protocol.js';
import { bboxOf, ellipsePoints, inkLine, inkPaths, LIMITS, overlaps, r1, thin, utf8Bytes, type Box, type Capture, type ContextItem, type ContextLabel, type Ellipse, type Layout, type Point, type RegionTarget } from './region.js';

const SVGNS = 'http://www.w3.org/2000/svg';
const TOL = 1; // css px: geometry within this of the capture is unchanged
const collapse = (s: string | null | undefined) => (s || '').replace(/\s+/g, ' ').trim();

/** Where a drawing landed on this render, with its first target when that is on the page. */
export interface Placement {
  anchor: string;
  outcome: Outcome;
  el: Element | null;
}

export interface DrawLayer {
  /** The Draw tool is selected: a stroke draws instead of reaching the plan. */
  setTool(on: boolean): void;
  /** Places and inks drawings among all, the page's markables as just listed, and reports where each landed. */
  paint(drawings: DrawingPaint[], all: Markable[]): Placement[];
  /** Restores what a drawing's capture recorded as open or checked, and opens any <details> around its targets. */
  show(d: DrawingPaint): void;
}

/** An element that can take a mark or be drawn around, with its content anchor. */
export type Markable = { el: Element; anchor: string };
type Shown = Markable & { box: Box; cover: number };

/**
 * markables lists the elements selector names, minus any inside exclude: the
 * host's own controls, on a page that shares their window, are never part of
 * the plan.
 */
export function markables(selector: string, exclude: string): Markable[] {
  const out: Markable[] = [];
  document.querySelectorAll(selector).forEach((el) => { if (!el.closest(exclude)) out.push({ el, anchor: anchorFor(el) }); });
  return out;
}

/**
 * drawLayer adds the ink layer to the document. A drawing encloses what
 * markables(selector, exclude) lists. capture hears each stroke's capture,
 * with where the composer opens below it in viewport pixels; refuse hears
 * why a stroke made none; changed hears that the layout moved after a paint,
 * which is placed again then.
 */
export function drawLayer(selector: string, exclude: string, capture: (c: Capture, rect: { left: number; bottom: number }, anchor?: string) => void, refuse: (r: DrawRefusal) => void, changed: () => void): DrawLayer {
  const layer = svg('svg', { class: 'rev-ink', 'aria-hidden': 'true', tabindex: -1 }) as SVGSVGElement;
  const ink = svg('g', {}); // translated so the layer's coordinates are the document's
  layer.appendChild(ink);
  document.documentElement.appendChild(layer);
  let tool = false;
  // One pointer creates, moves, or resizes an oval. A second finger is ignored.
  let gesture: { pointer: number; start: Point; original: Ellipse; ellipse: Ellipse; anchor?: string; handle?: Point; preview: SVGGElement; moved: boolean } | null = null;
  let selected: string | null = null;
  const editable = new Map<string, Ellipse>();
  let lastClient: Point | null = null;

  function svg(tag: string, attrs: Record<string, string | number>): SVGElement {
    const n = document.createElementNS(SVGNS, tag);
    for (const k in attrs) n.setAttribute(k, String(attrs[k]));
    return n;
  }
  // inkInto paints a line of pen ink through pts, shifted by off.
  function inkInto(pts: Point[], off: Point, tint: string, cls: string): SVGGElement {
    const p = inkPaths(pts, off);
    const g = svg('g', { class: 'rev-ink-' + tint + ' ' + cls }) as SVGGElement;
    g.appendChild(svg('path', { d: p.sweep, fill: 'currentColor', stroke: 'none' }));
    g.appendChild(svg('path', { d: p.hair, fill: 'none', stroke: 'currentColor', 'stroke-width': 1, 'stroke-linecap': 'round', 'stroke-linejoin': 'round' }));
    ink.appendChild(g);
    return g;
  }
  function oval(e: Ellipse, tint: string, anchor?: string): SVGGElement {
    const g = inkInto(inkLine(e), [0, 0], tint, anchor ? 'rev-ink-unsent' : 'rev-ink-live');
    if (!anchor) return g;
    g.setAttribute('data-anchor', anchor);
    const [cx, cy] = e.center, [rx, ry] = e.radii;
    const controls = svg('g', { transform: `translate(${cx} ${cy}) rotate(${e.rotation})` });
    controls.appendChild(svg('ellipse', { class: 'rev-ink-hit', 'data-edit': anchor, cx: 0, cy: 0, rx, ry }));
    if (selected === anchor) {
      controls.appendChild(svg('rect', { class: 'rev-ink-bounds', x: -rx, y: -ry, width: rx * 2, height: ry * 2 }));
      for (const [hx, hy, cursor] of [[-1, -1, 'nwse'], [0, -1, 'ns'], [1, -1, 'nesw'], [1, 0, 'ew'], [1, 1, 'nwse'], [0, 1, 'ns'], [-1, 1, 'nesw'], [-1, 0, 'ew']] as const) {
        controls.appendChild(svg('rect', { class: 'rev-ink-handle', 'data-edit': anchor, 'data-hx': hx, 'data-hy': hy, x: hx * rx - 5, y: hy * ry - 5, width: 10, height: 10, style: `cursor:${cursor}-resize` }));
      }
    }
    g.appendChild(controls);
    return g;
  }
  // fitLayer sizes the layer to the page. It zeroes the layer first: an
  // absolutely positioned child of <html> counts toward the root's scroll
  // size, so measuring with the old size in place would never let it shrink.
  function fitLayer() {
    const root = document.documentElement;
    layer.setAttribute('width', '0');
    layer.setAttribute('height', '0');
    const o = layer.getBoundingClientRect();
    layer.setAttribute('width', String(root.scrollWidth));
    layer.setAttribute('height', String(root.scrollHeight));
    ink.setAttribute('transform', 'translate(' + r1(-(o.left + scrollX)) + ' ' + r1(-(o.top + scrollY)) + ')');
  }

  // --- what is on the page, in the document frame: CSS px from its top-left ---
  function byAnchor(all: Markable[]): Record<string, Markable[]> {
    const m: Record<string, Markable[]> = Object.create(null);
    all.forEach((c) => { (m[c.anchor] = m[c.anchor] || []).push(c); });
    return m;
  }
  const rectOf = (r: DOMRect): Box => [r1(r.left + scrollX), r1(r.top + scrollY), r1(r.width), r1(r.height)];
  // shownBox is el's document box, or null when the reader cannot see it: not
  // rendered, invisible, inside a closed <details>, empty, or scrolled out of
  // an ancestor that clips. A closed <details> hides its content with
  // content-visibility in current Chrome, which still lays it out, so only
  // checkVisibility (or the <details> walk where that is missing) tells.
  function shownBox(el: Element): Box | null {
    const e = el as Element & { checkVisibility?: (o: object) => boolean };
    if (e.checkVisibility ? !e.checkVisibility({ visibilityProperty: true }) : (!el.getClientRects().length || getComputedStyle(el).visibility === 'hidden' || inClosedDetails(el))) return null;
    const r = el.getBoundingClientRect();
    if (r.width < 1 || r.height < 1) return null;
    for (let a = el.parentElement; a && a !== document.body && a !== document.documentElement; a = a.parentElement) {
      const cs = getComputedStyle(a);
      if (cs.overflowX === 'visible' && cs.overflowY === 'visible') continue;
      const ar = a.getBoundingClientRect();
      if (r.right <= ar.left || r.left >= ar.right || r.bottom <= ar.top || r.top >= ar.bottom) return null;
    }
    return rectOf(r);
  }
  function inClosedDetails(el: Element): boolean {
    for (let d = el.parentElement && el.parentElement.closest('details:not([open])'); d; d = d.parentElement && d.parentElement.closest('details:not([open])')) {
      const sum = d.querySelector(':scope > summary');
      if (!sum || !sum.contains(el)) return true;
    }
    return false;
  }

  // --- capture: geometry plus the bounded context a later reader needs ---
  // inside: the closed polygon winds around pt (nonzero winding).
  function inside(pt: Point, poly: Point[]): boolean {
    let w = 0;
    for (let i = 0, j = poly.length - 1; i < poly.length; j = i++) {
      const a = poly[j], b = poly[i], cross = (b[0] - a[0]) * (pt[1] - a[1]) - (pt[0] - a[0]) * (b[1] - a[1]);
      if (a[1] <= pt[1]) { if (b[1] > pt[1] && cross > 0) w++; } else if (b[1] <= pt[1] && cross < 0) w--;
    }
    return w !== 0;
  }
  // coverage: the share of a 5x5 grid over b that poly encloses.
  function coverage(b: Box, poly: Point[], bb: Box): number {
    if (!overlaps(b, bb)) return 0;
    let n = 0;
    for (let i = 0; i < 5; i++) for (let j = 0; j < 5; j++) if (inside([b[0] + (b[2] * (i + 0.5)) / 5, b[1] + (b[3] * (j + 0.5)) / 5], poly)) n++;
    return n / 25;
  }
  const leafMost = <T extends Markable>(l: T[]) => l.filter((c) => !l.some((d) => d !== c && c.el.contains(d.el)));
  const outerMost = <T extends Markable>(l: T[]) => l.filter((c) => !l.some((d) => d !== c && d.el.contains(c.el)));

  // Capture the same ellipse shown throughout the drag, with fresh targets
  // and context after every move or resize.
  function captureEllipse(ellipse: Ellipse): Capture | DrawRefusal {
    const poly = ellipsePoints(ellipse, 0), bb = bboxOf(poly);
    const all = markables(selector, exclude), index = byAnchor(all);
    const shown: Shown[] = [];
    all.forEach((c) => { const b = shownBox(c.el); if (b) shown.push({ ...c, box: b, cover: coverage(b, poly, bb) }); });
    // the innermost elements the ellipse mostly encloses; for a large region,
    // the outermost (a section rather than its every row)
    const covered = shown.filter((c) => c.cover >= 0.5);
    let targets = leafMost(covered);
    if (targets.length > LIMITS.targets) targets = outerMost(covered);
    if (!targets.length) {
      // an ellipse inside one element (a node of a diagram): the smallest
      // element holding its center, covered only in part
      const cx = bb[0] + bb[2] / 2, cy = bb[1] + bb[3] / 2;
      let best: Shown | null = null;
      shown.forEach((c) => {
        const b = c.box;
        if (cx >= b[0] && cx <= b[0] + b[2] && cy >= b[1] && cy <= b[1] + b[3] && (!best || b[2] * b[3] < best.box[2] * best.box[3])) best = c;
      });
      if (best) targets = [best];
    }
    if (!targets.length) return 'no-target';
    if (targets.length > LIMITS.targets) return 'too-large';
    const cap: Capture = {
      v: 1, kind: 'region', frame: 'document',
      path: thin(poly.map((p) => [r1(p[0]), r1(p[1])])), ellipse, bbox: bb, layout: layoutContext(),
      targets: targets.map((c) => {
        const same = index[c.anchor];
        const t: RegionTarget = { anchor: c.anchor, tag: c.el.tagName.toLowerCase(), section: collapse(headingOf(c.el)), box: c.box, cover: c.cover };
        if (same.length > 1) { t.nth = same.findIndex((s) => s.el === c.el); t.of = same.length; }
        return t;
      }),
      context: contextOf(shown, bb, targets),
    };
    // the host's id, revision and time add under 256 bytes; past the limit
    // the host would drop the capture without a word
    return utf8Bytes(cap) > LIMITS.regionBytes - 256 ? 'too-large' : cap;
  }
  // contextOf keeps what lay under and around the drawing: the innermost
  // markable elements meeting its box (plus a 16 px margin), nearest first,
  // with the text the reader saw and any diagram labels, bounded by LIMITS;
  // then, in the room those leave, the runs of text across the drawing,
  // nearest first. truncated says when anything was cut.
  function contextOf(shown: Shown[], bb: Box, targets: Shown[]): Capture['context'] {
    const m = 16, zone: Box = [bb[0] - m, bb[1] - m, bb[2] + 2 * m, bb[3] + 2 * m];
    const cx = bb[0] + bb[2] / 2, cy = bb[1] + bb[3] / 2;
    const dist = (b: Box) => Math.abs(b[0] + b[2] / 2 - cx) + Math.abs(b[1] + b[3] / 2 - cy);
    const near = leafMost(shown.filter((c) => overlaps(c.box, zone))).sort((a, b) => dist(a.box) - dist(b.box));
    // runs is in place before the items are trimmed, so the trim counts its key
    const kept: ContextLabel[] = [];
    const ctx: Capture['context'] = { items: [], truncated: near.length > LIMITS.contextItems, runs: kept };
    const cut = (s: string, n: number) => {
      if (s.length <= n) return s;
      ctx.truncated = true;
      return s.slice(0, n - 1) + '…';
    };
    // the band the runs come from: the zone, as wide as the elements in it,
    // so a row or a line is kept whole rather than cut at the drawing's edge
    const band: Box = [...zone];
    near.forEach((c) => {
      const x1 = Math.max(band[0] + band[2], c.box[0] + c.box[2]);
      band[0] = Math.min(band[0], c.box[0]);
      band[2] = x1 - band[0];
    });
    let runs: ContextLabel[] = [];
    // innerText is the text the reader saw, cells and lines apart; the
    // glyphs review marks paint into elements are hidden while it is read
    const root = document.documentElement;
    root.classList.add('rev-reading');
    try {
      runs = runsIn(band).sort((a, b) => dist(a.box) - dist(b.box));
      near.slice(0, LIMITS.contextItems).forEach((c) => {
        const h = c.el as HTMLElement;
        let text = collapse(h.innerText === undefined ? h.textContent : h.innerText);
        if (!text) { const img = c.el.querySelector('img[alt]'); if (img) text = '[image: ' + collapse(img.getAttribute('alt')) + ']'; }
        // an enclosed element keeps its words whole; a neighbour is only context
        const it: ContextItem = { anchor: c.anchor, tag: c.el.tagName.toLowerCase(), box: c.box, text: cut(text, targets.indexOf(c) >= 0 ? LIMITS.targetText : LIMITS.neighborText) };
        let labels = labelsIn(c.el, zone);
        if (labels.length > LIMITS.itemLabels) { ctx.truncated = true; labels = labels.slice(0, LIMITS.itemLabels); }
        labels.forEach((l) => { l.text = cut(l.text, LIMITS.labelText); });
        if (labels.length) it.labels = labels;
        ctx.items.push(it);
      });
    } finally {
      root.classList.remove('rev-reading');
    }
    while (utf8Bytes(ctx) > LIMITS.contextBytes && ctx.items.length > 1) { ctx.items.pop(); ctx.truncated = true; }
    let room = LIMITS.contextBytes - utf8Bytes(ctx);
    for (const r of runs) {
      const n = utf8Bytes(r) + 1; // and its comma
      if (n > room) ctx.truncated = true;
      else { kept.push(r); room -= n; }
    }
    return ctx;
  }
  // runsIn: the words the reader saw in band, where the page drew them, so
  // View original can draw them there again: one run per line of each text
  // node, with its box. A diagram's SVG text counts, as it has boxes too.
  function runsIn(band: Box): ContextLabel[] {
    const out: ContextLabel[] = [];
    const range = document.createRange();
    const walk = document.createTreeWalker(document.body, NodeFilter.SHOW_ELEMENT | NodeFilter.SHOW_TEXT, {
      acceptNode(n) {
        if (n.nodeType === Node.TEXT_NODE) return NodeFilter.FILTER_ACCEPT;
        const el = n as Element & { checkVisibility?: (o: object) => boolean };
        if (el.matches(exclude + ', script, style, noscript, template, textarea, select')) return NodeFilter.FILTER_REJECT;
        // most of the page lies outside the band: an element boxed outside it
        // is skipped whole, missing only words that overflow it there; one
        // with no box of its own (display: contents) still holds boxed words
        const r = el.getBoundingClientRect();
        if ((r.width || r.height) && !overlaps(rectOf(r), band)) return NodeFilter.FILTER_REJECT;
        if (el.checkVisibility && !el.checkVisibility({ visibilityProperty: true, opacityProperty: true })) return NodeFilter.FILTER_REJECT;
        return NodeFilter.FILTER_SKIP;
      },
    });
    for (let t = walk.nextNode() as Text | null; t; t = walk.nextNode() as Text | null) {
      // a word's box says which line it is on; a word broken over two lines
      // counts on its first. A gap wider than the line is tall (aligned columns
      // in a <pre>) starts a new run, so each side keeps its place
      let run: ContextLabel | null = null;
      const keep = () => { if (run && overlaps(run.box, band)) out.push(run); };
      const words = /\S+/g;
      for (let w = words.exec(t.data); w; w = words.exec(t.data)) {
        range.setStart(t, w.index);
        range.setEnd(t, w.index + w[0].length);
        const r = range.getClientRects()[0];
        if (!r || r.width < 1) continue;
        const b = rectOf(r);
        if (run && Math.abs(b[1] - run.box[1]) < 1 && b[0] - (run.box[0] + run.box[2]) < b[3]) {
          const x1 = Math.max(run.box[0] + run.box[2], b[0] + b[2]);
          run.box[0] = Math.min(run.box[0], b[0]);
          run.box[2] = r1(x1 - run.box[0]);
          run.box[3] = Math.max(run.box[3], b[3]);
          run.text += ' ' + w[0];
        } else {
          keep();
          run = { text: w[0], box: b };
        }
      }
      keep();
    }
    return out;
  }
  // labelsIn: the text a diagram draws inside el (Mermaid node, edge and
  // cluster labels, or any SVG <text>), each with its own box.
  function labelsIn(el: Element, zone: Box): ContextLabel[] {
    const out: ContextLabel[] = [];
    el.querySelectorAll('svg g.node, svg g.edgeLabel, svg g.cluster, svg text').forEach((n) => {
      if (n.tagName.toLowerCase() === 'text' && n.closest('g.node, g.edgeLabel, g.cluster')) return;
      const t = collapse(n.textContent), b = t ? shownBox(n) : null;
      if (b && overlaps(b, zone)) out.push({ text: t, box: b });
    });
    return out;
  }
  // the conditions the geometry was captured under
  const detailsKey = (d: Element) => { const s = d.querySelector('summary'); return 'd' + fnv1a(norm(s ? s.textContent : '')); };
  const inputKey = (i: HTMLInputElement) => (i.id ? '#' + i.id : i.name + '=' + i.value);
  function layoutContext(): Layout {
    fitLayer(); // so document below is the page's size, not a stale layer's
    const open: string[] = [], checked: string[] = [];
    document.querySelectorAll('details[open]').forEach((d) => open.push(detailsKey(d)));
    document.querySelectorAll('input[type=radio]:checked, input[type=checkbox]:checked').forEach((i) => checked.push(inputKey(i as HTMLInputElement)));
    const root = document.documentElement, vv = window.visualViewport;
    return {
      viewport: [innerWidth, innerHeight],
      document: [root.scrollWidth, root.scrollHeight],
      dpr: devicePixelRatio,
      scale: vv ? r1(vv.scale) : 1,
      scroll: [r1(scrollX), r1(scrollY)],
      theme: root.getAttribute('data-theme') || (matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark'),
      fonts: document.fonts ? document.fonts.status : '',
      open: open.slice(0, LIMITS.layoutKeys),
      checked: checked.slice(0, LIMITS.layoutKeys),
    };
  }

  // --- placement: derived on every render, never stored. Each target must
  // be found by its exact anchor (never by similar text), visible, and laid
  // out as captured; then the drawing moves rigidly with them. ---
  type Place = { outcome: Placement['outcome']; offset?: Point; boxes?: Box[]; el?: Element };
  function place(d: DrawingPaint, index: Record<string, Markable[]>): Place {
    const found: Array<{ box: Box; was: Box }> = [];
    let hidden: Place | null = null, first: Element | undefined;
    for (const t of d.targets) {
      const same = index[t.anchor] || [];
      if (!same.length) return { outcome: 'missing' };
      if (same.length !== (t.of || 1)) return { outcome: 'ambiguous' };
      const el = same[t.nth || 0].el, b = shownBox(el);
      first = first || el;
      if (!b) { hidden = hidden || { outcome: 'hidden' }; continue; }
      found.push({ box: b, was: t.box });
    }
    if (hidden) return { ...hidden, el: first };
    const dx = r1(found[0].box[0] - found[0].was[0]), dy = r1(found[0].box[1] - found[0].was[1]);
    const rigid = found.every((f) => Math.abs(f.box[2] - f.was[2]) <= TOL && Math.abs(f.box[3] - f.was[3]) <= TOL &&
      Math.abs(f.box[0] - f.was[0] - dx) <= TOL && Math.abs(f.box[1] - f.was[1] - dy) <= TOL);
    if (!rigid) return { outcome: 'reflowed', boxes: found.map((f) => f.box), el: first };
    return { outcome: Math.abs(dx) <= TOL && Math.abs(dy) <= TOL ? 'exact' : 'moved', offset: [dx, dy], el: first };
  }

  let drawings: DrawingPaint[] = [];
  let index: Record<string, Markable[]> = Object.create(null);
  let last = '';
  function placeAll(): Placement[] {
    fitLayer();
    while (ink.firstChild) ink.removeChild(ink.firstChild);
    editable.clear();
    const out = drawings.map((d) => {
      const o = place(d, index);
      const cls = d.kind === 'unsent' ? 'rev-ink-unsent' : '';
      if (o.offset) {
        if (tool && d.kind === 'unsent') {
          const e: Ellipse = { ...d.ellipse, center: [r1(d.ellipse.center[0] + o.offset[0]), r1(d.ellipse.center[1] + o.offset[1])] };
          editable.set(d.anchor, e);
          if (gesture?.anchor !== d.anchor) oval(e, d.tint, d.anchor);
        } else inkInto(inkLine(d.ellipse), o.offset, d.tint, cls).setAttribute('data-anchor', d.anchor);
      } else if (o.boxes) {
        // a thin dashed ellipse around the targets as they now are, never the drawing
        const u = o.boxes.reduce((m, b) => [Math.min(m[0], b[0]), Math.min(m[1], b[1]), Math.max(m[2], b[0] + b[2]), Math.max(m[3], b[1] + b[3])], [Infinity, Infinity, -Infinity, -Infinity]);
        ink.appendChild(svg('ellipse', {
          class: 'rev-ink-' + d.tint + ' rev-ink-reflowed', 'data-anchor': d.anchor, cx: (u[0] + u[2]) / 2, cy: (u[1] + u[3]) / 2,
          rx: (u[2] - u[0]) / Math.SQRT2 + 6, ry: (u[3] - u[1]) / Math.SQRT2 + 6,
        }));
      }
      return { anchor: d.anchor, outcome: o.outcome, el: o.el || null };
    });
    if (gesture) ink.appendChild(gesture.preview);
    last = out.map((p) => p.outcome).join();
    return out;
  }
  // The layout moved: a resize, a toggle, a tab change, an element scrolled.
  // Placement is derived again, and the document told when an outcome changed.
  let timer: ReturnType<typeof setTimeout> | undefined;
  function relayout() {
    clearTimeout(timer);
    timer = setTimeout(() => {
      const was = last;
      placeAll();
      if (last !== was) changed();
    }, 100);
  }

  // --- direct oval creation and editing ---
  function drag(cx: number, cy: number) {
    if (!gesture) return;
    lastClient = [cx, cy];
    const p: Point = [r1(cx + scrollX), r1(cy + scrollY)], s = gesture.start, e = gesture.original;
    const dx = p[0] - s[0], dy = p[1] - s[1];
    if (Math.abs(dx) + Math.abs(dy) >= 2) gesture.moved = true;
    if (!gesture.anchor) {
      gesture.ellipse = { center: [r1((s[0] + p[0]) / 2), r1((s[1] + p[1]) / 2)], radii: [r1(Math.abs(dx) / 2), r1(Math.abs(dy) / 2)], rotation: 0, start: 0, dir: 1 };
    } else if (!gesture.handle) {
      gesture.ellipse = { ...e, center: [r1(e.center[0] + dx), r1(e.center[1] + dy)] };
    } else {
      // Resize in the ellipse's own axes, keeping the opposite edge fixed.
      const a = e.rotation * Math.PI / 180, cos = Math.cos(a), sin = Math.sin(a);
      const delta = [dx * cos + dy * sin, -dx * sin + dy * cos];
      const radii: Point = [...e.radii], shift: Point = [0, 0];
      gesture.handle.forEach((h, i) => {
        if (!h) return;
        radii[i] = r1(Math.max(4, e.radii[i] + h * delta[i] / 2));
        shift[i] = h * (radii[i] - e.radii[i]);
      });
      gesture.ellipse = { ...e, radii, center: [r1(e.center[0] + shift[0] * cos - shift[1] * sin), r1(e.center[1] + shift[0] * sin + shift[1] * cos)] };
    }
    gesture.preview.remove();
    gesture.preview = oval(gesture.ellipse, 'gold', gesture.anchor);
  }
  function endGesture(reason?: DrawRefusal) {
    if (!gesture) return;
    const pointer = gesture.pointer;
    gesture = null;
    layer.classList.remove('rev-dragging');
    if (layer.hasPointerCapture(pointer)) layer.releasePointerCapture(pointer);
    placeAll();
    if (reason) refuse(reason);
  }
  layer.addEventListener('pointerdown', (e) => {
    if (!tool || e.button !== 0 || gesture) return;
    e.preventDefault();
    layer.focus({ preventScroll: true });
    fitLayer();
    const hit = (e.target as Element).closest('[data-edit]'), anchor = hit?.getAttribute('data-edit') || undefined;
    const original = anchor ? editable.get(anchor) : undefined;
    if (anchor && !original) return;
    selected = anchor || null;
    const start: Point = [r1(e.clientX + scrollX), r1(e.clientY + scrollY)];
    const ellipse: Ellipse = original || { center: start, radii: [0, 0], rotation: 0, start: 0, dir: 1 };
    const handle: Point | undefined = hit?.hasAttribute('data-hx') ? [Number(hit.getAttribute('data-hx')), Number(hit.getAttribute('data-hy'))] : undefined;
    gesture = { pointer: e.pointerId, start, original: ellipse, ellipse, anchor, handle, preview: oval(ellipse, 'gold', anchor), moved: false };
    lastClient = [e.clientX, e.clientY];
    layer.classList.toggle('rev-dragging', !!anchor && !handle);
    placeAll();
    layer.setPointerCapture(e.pointerId);
  });
  layer.addEventListener('pointermove', (e) => { if (gesture && e.pointerId === gesture.pointer) drag(e.clientX, e.clientY); });
  layer.addEventListener('pointerup', (e) => {
    if (!gesture || e.pointerId !== gesture.pointer) return;
    drag(e.clientX, e.clientY);
    const g = gesture;
    endGesture();
    if (g.anchor && !g.moved) return;
    if (g.ellipse.radii.some((r) => r < 4)) { refuse('too-small'); return; }
    const c = captureEllipse(g.ellipse);
    if (typeof c === 'string') { refuse(c); return; }
    // the composer opens below the drawing, in viewport pixels
    capture(c, { left: c.bbox[0] - scrollX, bottom: c.bbox[1] + c.bbox[3] - scrollY }, g.anchor);
  });
  layer.addEventListener('pointercancel', (e) => { if (gesture && e.pointerId === gesture.pointer) endGesture('cancelled'); });
  layer.addEventListener('lostpointercapture', (e) => { if (gesture && e.pointerId === gesture.pointer && !layer.hasPointerCapture(e.pointerId)) endGesture('cancelled'); });
  // Scrolling moves the page under a still pointer; keep document coordinates.
  window.addEventListener('scroll', () => { if (gesture && lastClient) drag(lastClient[0], lastClient[1]); });
  document.addEventListener('scroll', (e) => { if (e.target !== document) relayout(); }, true);
  // A resize mid-stroke reflows what it was drawn over, so the stroke is
  // abandoned rather than captured against a layout that is gone.
  window.addEventListener('resize', () => { endGesture('layout-changed'); relayout(); });
  document.addEventListener('toggle', relayout, true);
  document.addEventListener('change', relayout, true);
  if (window.ResizeObserver) new ResizeObserver(relayout).observe(document.documentElement);
  // Esc ends a stroke in progress, before the host's own Esc (an open note,
  // then the tool, then review mode) sees it.
  window.addEventListener('keydown', (e) => {
    if (e.key !== 'Escape' || !gesture || e.defaultPrevented) return;
    e.preventDefault();
    endGesture('cancelled');
  }, true);

  return {
    setTool(on) {
      if (tool === on) return;
      tool = on;
      layer.classList.toggle('rev-drawing', on);
      if (!on) endGesture('cancelled');
      placeAll();
    },
    paint(next, all) {
      for (const d of next) if (d.kind === 'unsent' && !drawings.some((old) => old.anchor === d.anchor)) selected = d.anchor;
      drawings = next;
      index = byAnchor(all);
      return placeAll();
    },
    show(d) {
      document.querySelectorAll('details').forEach((x) => { const el = x as HTMLDetailsElement; if (!el.open && d.open.indexOf(detailsKey(el)) >= 0) el.open = true; });
      document.querySelectorAll('input[type=radio], input[type=checkbox]').forEach((x) => {
        const i = x as HTMLInputElement;
        if (!i.checked && d.checked.indexOf(inputKey(i)) >= 0) { i.checked = true; i.dispatchEvent(new Event('change', { bubbles: true })); }
      });
      d.targets.forEach((t) => {
        const c = (index[t.anchor] || [])[t.nth || 0];
        for (let a = c && c.el.parentElement; a; a = a.parentElement) if (a.tagName === 'DETAILS') (a as HTMLDetailsElement).open = true;
      });
    },
  };
}
