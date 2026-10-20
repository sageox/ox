// Draw around, in the plan document: the gesture, the capture a stroke makes,
// and the placement and ink of the drawings the host paints. The capture is a
// proposal: the host checks it, mints its id and stamps the revision, and
// keeps it. docs/specs/plan-review-drawn-context.md is the format, and
// pkg/planreview/testdata/drawn-context/proto/draw.js the prototype this
// follows.

import { anchorFor, fnv1a, headingOf, norm } from './anchor.js';
import type { DrawRefusal, DrawingPaint, Outcome } from './protocol.js';
import { bboxOf, ellipsePoints, fitEllipse, inkLine, inkPaths, LIMITS, r1, thin, utf8Bytes, type Box, type Capture, type ContextItem, type ContextLabel, type Layout, type Point, type RegionTarget } from './region.js';

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
export function drawLayer(selector: string, exclude: string, capture: (c: Capture, rect: { left: number; bottom: number }) => void, refuse: (r: DrawRefusal) => void, changed: () => void): DrawLayer {
  const layer = svg('svg', { class: 'rev-ink', 'aria-hidden': 'true' }) as SVGSVGElement;
  const ink = svg('g', {}); // translated so the layer's coordinates are the document's
  layer.appendChild(ink);
  document.documentElement.appendChild(layer);
  let tool = false;
  // the stroke being drawn: the pointer drawing it (a second finger is
  // ignored), its points, and the faint line that shows them
  let stroke: { pointer: number; pts: Point[]; line: SVGPolylineElement } | null = null;
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
  const overlaps = (a: Box, b: Box) => a[0] < b[0] + b[2] && b[0] < a[0] + a[2] && a[1] < b[1] + b[3] && b[1] < a[1] + a[3];
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

  // capturePath turns a stroke into a capture, or the reason it makes none.
  // What it encloses is decided by the fitted ellipse: the shape the reviewer
  // sees once the pen lifts.
  function capturePath(pts: Point[]): Capture | DrawRefusal {
    const ellipse = fitEllipse(pts), poly = ellipsePoints(ellipse, 0), bb = bboxOf(poly);
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
      path: pts, ellipse, bbox: bb, layout: layoutContext(),
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
  // truncated says when anything was cut.
  function contextOf(shown: Shown[], bb: Box, targets: Shown[]): Capture['context'] {
    const m = 16, zone: Box = [bb[0] - m, bb[1] - m, bb[2] + 2 * m, bb[3] + 2 * m];
    const cx = bb[0] + bb[2] / 2, cy = bb[1] + bb[3] / 2;
    const dist = (b: Box) => Math.abs(b[0] + b[2] / 2 - cx) + Math.abs(b[1] + b[3] / 2 - cy);
    const near = leafMost(shown.filter((c) => overlaps(c.box, zone))).sort((a, b) => dist(a.box) - dist(b.box));
    const ctx: Capture['context'] = { items: [], truncated: near.length > LIMITS.contextItems };
    const cut = (s: string, n: number) => {
      if (s.length <= n) return s;
      ctx.truncated = true;
      return s.slice(0, n - 1) + '…';
    };
    // innerText is the text the reader saw, cells and lines apart; the
    // glyphs review marks paint into elements are hidden while it is read
    const root = document.documentElement;
    root.classList.add('rev-reading');
    try {
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
    return ctx;
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
    const out = drawings.map((d) => {
      const o = place(d, index);
      const cls = d.kind === 'unsent' ? 'rev-ink-unsent' : '';
      if (o.offset) inkInto(inkLine(d.ellipse), o.offset, d.tint, cls).setAttribute('data-anchor', d.anchor);
      else if (o.boxes) {
        // a thin dashed ellipse around the targets as they now are, never the drawing
        const u = o.boxes.reduce((m, b) => [Math.min(m[0], b[0]), Math.min(m[1], b[1]), Math.max(m[2], b[0] + b[2]), Math.max(m[3], b[1] + b[3])], [Infinity, Infinity, -Infinity, -Infinity]);
        ink.appendChild(svg('ellipse', {
          class: 'rev-ink-' + d.tint + ' rev-ink-reflowed', 'data-anchor': d.anchor, cx: (u[0] + u[2]) / 2, cy: (u[1] + u[3]) / 2,
          rx: (u[2] - u[0]) / Math.SQRT2 + 6, ry: (u[3] - u[1]) / Math.SQRT2 + 6,
        }));
      }
      return { anchor: d.anchor, outcome: o.outcome, el: o.el || null };
    });
    if (stroke) ink.appendChild(stroke.line);
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

  // --- the gesture ---
  // addPoint records where the pointer is in the document, as thin() would
  // keep it, and extends the faint line by that one point: a long stroke
  // never re-draws its whole length on each move.
  function addPoint(cx: number, cy: number) {
    if (!stroke) return;
    lastClient = [cx, cy];
    const p: Point = [r1(cx + scrollX), r1(cy + scrollY)], q = stroke.pts[stroke.pts.length - 1];
    if (q && Math.abs(p[0] - q[0]) + Math.abs(p[1] - q[1]) < 2) return;
    stroke.pts.push(p);
    const sp = layer.createSVGPoint();
    sp.x = p[0];
    sp.y = p[1];
    stroke.line.points.appendItem(sp);
  }
  function abort(reason: DrawRefusal) {
    if (!stroke) return;
    stroke.line.remove();
    stroke = null;
    refuse(reason);
  }
  layer.addEventListener('pointerdown', (e) => {
    if (!tool || e.button !== 0 || stroke) return;
    e.preventDefault();
    fitLayer();
    stroke = { pointer: e.pointerId, pts: [], line: svg('polyline', { class: 'rev-ink-live' }) as SVGPolylineElement };
    ink.appendChild(stroke.line);
    layer.setPointerCapture(e.pointerId);
    addPoint(e.clientX, e.clientY);
  });
  layer.addEventListener('pointermove', (e) => { if (stroke && e.pointerId === stroke.pointer) addPoint(e.clientX, e.clientY); });
  layer.addEventListener('pointerup', (e) => {
    if (!stroke || e.pointerId !== stroke.pointer) return;
    stroke.line.remove();
    const pts = thin(stroke.pts), bb = bboxOf(pts);
    stroke = null;
    if (pts.length < 3 || bb[2] < 8 || bb[3] < 8) { refuse('too-small'); return; }
    const c = capturePath(pts);
    if (typeof c === 'string') { refuse(c); return; }
    // the composer opens below the drawing, in viewport pixels
    capture(c, { left: c.bbox[0] - scrollX, bottom: c.bbox[1] + c.bbox[3] - scrollY });
  });
  layer.addEventListener('pointercancel', (e) => { if (stroke && e.pointerId === stroke.pointer) abort('cancelled'); });
  // Scrolling mid-stroke moves the page under a still pointer: record where
  // the pointer now is in the document, so the stroke follows the content.
  window.addEventListener('scroll', () => { if (stroke && lastClient) addPoint(lastClient[0], lastClient[1]); });
  document.addEventListener('scroll', (e) => { if (e.target !== document) relayout(); }, true);
  // A resize mid-stroke reflows what it was drawn over, so the stroke is
  // abandoned rather than captured against a layout that is gone.
  window.addEventListener('resize', () => { abort('layout-changed'); relayout(); });
  document.addEventListener('toggle', relayout, true);
  document.addEventListener('change', relayout, true);
  if (window.ResizeObserver) new ResizeObserver(relayout).observe(document.documentElement);
  // Esc ends a stroke in progress, before the host's own Esc (an open note,
  // then the tool, then review mode) sees it.
  window.addEventListener('keydown', (e) => {
    if (e.key !== 'Escape' || !stroke || e.defaultPrevented) return;
    e.preventDefault();
    abort('cancelled');
  }, true);

  return {
    setTool(on) {
      tool = on;
      layer.classList.toggle('rev-drawing', on);
      if (!on) abort('cancelled');
    },
    paint(next, all) {
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
