// View original: a drawing redrawn in the host from its record alone (spec
// section 4): the words that lay across it where the page drew them, each
// context item as a box, the inked ellipse, and a note when the context was
// cut. A diagram's labels are drawn where no run covers them. An enclosed
// element none of whose words are placed has its text listed below the
// picture rather than drawn where it may not have been. Text goes in as text
// nodes, never markup.

import { inkLine, inkPaths, overlaps, r1, runsOf, type ContextLabel, type Region } from './region.js';

const SVGNS = 'http://www.w3.org/2000/svg';

/** originalView builds what lay under a drawing when it was made, in the record's own coordinates. */
export function originalView(r: Region, doc: Document): DocumentFragment {
  const mk = (tag: string, attrs: Record<string, string | number>, text?: string) => {
    const n = doc.createElementNS(SVGNS, tag);
    for (const k in attrs) n.setAttribute(k, String(attrs[k]));
    if (text) n.textContent = text;
    return n;
  };
  // the runs, and each diagram label no run covers: one whose run did not
  // fit, or any in a record captured before runs
  const items = r.context.items, runs = runsOf(r.context) || [];
  const labels = items.reduce((all: ContextLabel[], it) => all.concat(it.labels || []), []).filter((l) => !runs.some((t) => overlaps(t.box, l.box)));
  const placed = runs.concat(labels);
  // the drawing with a margin, widened to the whole of each text placed
  // beside it, with room below for the truncation note
  const b = r.bbox, pad = 24;
  let x0 = b[0] - pad, y0 = b[1] - pad, x1 = b[0] + b[2] + pad, y1 = b[1] + b[3] + pad;
  placed.forEach((t) => {
    x0 = Math.min(x0, t.box[0] - 4);
    y0 = Math.min(y0, t.box[1] - 4);
    x1 = Math.max(x1, t.box[0] + t.box[2] + 4);
    y1 = Math.max(y1, t.box[1] + t.box[3] + 4);
  });
  if (r.context.truncated) y1 += 18;
  const vb = [x0, y0, x1 - x0, y1 - y0];
  const out = mk('svg', { viewBox: vb.join(' '), width: vb[2], height: vb[3], class: 'rev-original-svg', role: 'img' });
  out.appendChild(mk('title', {}, 'What lay under this drawing on revision ' + r.plan_revision));
  items.forEach((it) => out.appendChild(mk('rect', { x: it.box[0], y: it.box[1], width: it.box[2], height: it.box[3], class: 'rev-orig-box' })));
  // a run's box is about 1.2 em tall with its baseline 0.8 of the way down;
  // stretched to the box's width, each word lands where the page drew it
  runs.forEach((t) => {
    const [x, y, w, h] = t.box;
    out.appendChild(mk('text', { x, y: r1(y + 0.8 * h), 'font-size': r1(h / 1.2), textLength: w, lengthAdjust: 'spacingAndGlyphs', class: 'rev-orig-run' }, t.text));
  });
  labels.forEach((l) => {
    out.appendChild(mk('rect', { x: l.box[0], y: l.box[1], width: l.box[2], height: l.box[3], class: 'rev-orig-label-box' }));
    out.appendChild(mk('text', { x: l.box[0] + 2, y: l.box[1] + Math.min(l.box[3], 14) - 3, class: 'rev-orig-label' }, l.text));
  });
  const p = inkPaths(inkLine(r.ellipse), [0, 0]);
  const g = mk('g', { class: 'rev-orig-ink' });
  g.appendChild(mk('path', { d: p.sweep, fill: 'currentColor', stroke: 'none' }));
  g.appendChild(mk('path', { d: p.hair, fill: 'none', stroke: 'currentColor', 'stroke-width': 1, 'stroke-linecap': 'round', 'stroke-linejoin': 'round' }));
  out.appendChild(g);
  if (r.context.truncated) out.appendChild(mk('text', { x: vb[0] + 4, y: vb[1] + vb[3] - 6, class: 'rev-orig-cut' }, 'Context truncated: some of what lay here was not kept.'));
  const view = doc.createDocumentFragment();
  view.appendChild(out);
  const enclosed = r.targets.map((t) => t.anchor);
  const unplaced = items.filter((it) => it.text && enclosed.indexOf(it.anchor) >= 0 && !placed.some((t) => overlaps(t.box, it.box)));
  if (unplaced.length) {
    const list = doc.createElement('div');
    list.className = 'rev-orig-unplaced';
    const head = doc.createElement('div');
    head.className = 'rev-orig-unplaced-h';
    head.textContent = 'Drawn around (where these words sat was not recorded):';
    list.appendChild(head);
    unplaced.forEach((it) => {
      const q = doc.createElement('blockquote');
      q.textContent = it.text;
      list.appendChild(q);
    });
    view.appendChild(list);
  }
  return view;
}
