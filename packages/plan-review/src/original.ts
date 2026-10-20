// View original: a drawing redrawn in the host from its record alone (spec
// section 4): each context item as a box with its text or diagram labels, the
// inked ellipse, and a note when the context was cut. Text goes in as text
// nodes, never markup.

import { inkLine, inkPaths, type Region } from './region.js';

const SVGNS = 'http://www.w3.org/2000/svg';

/** originalView builds the SVG of what lay under a drawing when it was made, in the record's own coordinates. */
export function originalView(r: Region, doc: Document): SVGSVGElement {
  const mk = (tag: string, attrs: Record<string, string | number>, text?: string) => {
    const n = doc.createElementNS(SVGNS, tag);
    for (const k in attrs) n.setAttribute(k, String(attrs[k]));
    if (text) n.textContent = text;
    return n;
  };
  const b = r.bbox, pad = 24, vb = [b[0] - pad, b[1] - pad, b[2] + 2 * pad, b[3] + 2 * pad];
  const out = mk('svg', { viewBox: vb.join(' '), width: vb[2], height: vb[3], class: 'rev-original-svg', role: 'img' }) as SVGSVGElement;
  out.appendChild(mk('title', {}, 'What lay under this drawing on revision ' + r.plan_revision));
  r.context.items.forEach((it) => {
    const [x, y, w, h] = it.box;
    out.appendChild(mk('rect', { x, y, width: w, height: h, class: 'rev-orig-box' }));
    if (it.labels) {
      it.labels.forEach((l) => {
        out.appendChild(mk('rect', { x: l.box[0], y: l.box[1], width: l.box[2], height: l.box[3], class: 'rev-orig-label-box' }));
        out.appendChild(mk('text', { x: l.box[0] + 2, y: l.box[1] + Math.min(l.box[3], 14) - 3, class: 'rev-orig-label' }, l.text));
      });
      // what the labels do not account for, such as a figure's caption
      const rest = it.labels.reduce((t, l) => t.replace(l.text, ' '), it.text).replace(/\s+/g, ' ').trim();
      if (rest) out.appendChild(mk('text', { x: x + 2, y: y + h - 4, class: 'rev-orig-text' }, rest));
      return;
    }
    // the item's text at about 7 px a character, as many lines as its box holds
    const per = Math.max(8, Math.floor(w / 7)), rows = Math.max(1, Math.floor(h / 15)), text = mk('text', { class: 'rev-orig-text' });
    let line = '', n = 0;
    const flush = () => { if (n < rows && line) text.appendChild(mk('tspan', { x: x + 2, y: y + 13 + n * 15 }, line)); n++; line = ''; };
    it.text.split(' ').forEach((wd) => { if (line && (line + ' ' + wd).length > per) flush(); line = line ? line + ' ' + wd : wd; });
    flush();
    out.appendChild(text);
  });
  const p = inkPaths(inkLine(r.ellipse), [0, 0]);
  const g = mk('g', { class: 'rev-orig-ink' });
  g.appendChild(mk('path', { d: p.sweep, fill: 'currentColor', stroke: 'none' }));
  g.appendChild(mk('path', { d: p.hair, fill: 'none', stroke: 'currentColor', 'stroke-width': 1, 'stroke-linecap': 'round', 'stroke-linejoin': 'round' }));
  out.appendChild(g);
  if (r.context.truncated) out.appendChild(mk('text', { x: vb[0] + 4, y: vb[1] + vb[3] - 6, class: 'rev-orig-cut' }, 'Context truncated: some of what lay here was not kept.'));
  return out;
}
