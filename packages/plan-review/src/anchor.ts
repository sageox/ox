// Content anchors: how the review page names what a mark is on. An element's
// anchor is "h" + FNV-1a over its normalized section heading and text; a
// highlight's is "q" + the same hash over the heading and the quoted words.
// Every anchor in every Ledger was computed this way, and pkg/planreview
// (AnchorFor) recomputes it in Go, so the arithmetic is a wire format:
// `(h * 0x01000193) >>> 0` rounds through a float64 once the product passes
// 2^53, and Math.imul would orphan existing marks. The vectors in
// pkg/planreview/testdata/anchors.json pin it.

export function fnv1a(s: string): string {
  let h = 0x811c9dc5;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = (h * 0x01000193) >>> 0;
  }
  return ('0000000' + h.toString(16)).slice(-8);
}

export function norm(s: string | null | undefined): string {
  return (s || '').replace(/\s+/g, ' ').trim().toLowerCase();
}

export function clip(t: string, n: number): string {
  return t.length > n ? t.slice(0, n - 1) + '…' : t;
}

/** contentAnchor is an element's anchor from its section heading and text. */
export function contentAnchor(heading: string, text: string): string {
  return 'h' + fnv1a(norm(heading) + '\u0000' + norm(text));
}

export function quoteAnchor(section: string, quote: string): string {
  return 'q' + fnv1a(norm(section) + '\u0000' + norm(quote));
}

/** labelOf is the ≤70-unit display label for an element's text. */
export function labelOf(text: string | null): string {
  return clip((text || '').replace(/\s+/g, ' ').trim(), 70);
}

// Markable elements. Generated pages mark sections, items, rows and ox
// widgets; authored pages, which follow no such convention, mark any block.
// window.OX_REVIEW_SELECTOR (chrome.js sets it on authored pages the CLI
// serves) wins. A framed page has no chrome.js, so the page kind is read the
// way the CLI reads it (IsGeneratedMarkdownRender in internal/plan).
const GENERATED = 'section[id], li, tr, .ox-chip, .stat, .bar-row';
const AUTHORED = 'h1,h2,h3,h4,p,li,tr,pre,blockquote,figure,[data-ox-section]';

export function markableSelector(doc: Document): string {
  const set = (window as { OX_REVIEW_SELECTOR?: string }).OX_REVIEW_SELECTOR;
  if (set) return set;
  const meta = doc.querySelector('meta[name="generator"]');
  if (meta && meta.getAttribute('content') === 'ox plan markdown renderer') return GENERATED;
  // pages rendered before the generator meta existed
  const brand = doc.querySelector('div.brand'), eyebrow = doc.querySelector('div.eyebrow');
  if (brand && eyebrow && brand.textContent === 'OX · PLAN' && eyebrow.textContent === 'SageOx · enriched plan') return GENERATED;
  return AUTHORED;
}

export function headingOf(el: Element): string {
  const sec = el.closest('section[id], [data-ox-section]');
  if (!sec) return '';
  const ds = sec.getAttribute('data-ox-section');
  if (ds) return ds;
  const h = sec.querySelector('h2, h3');
  return h ? anchorText(h) : sec.id || '';
}

// anchorText is the element's text without the glyphs the review layer
// painted into it, so a marked element keeps its anchor and its label, and
// without <style> and <script>: Mermaid writes a per-render id into each
// diagram's <style>, which would give a drawn diagram a new anchor every load.
export function anchorText(el: Element): string {
  const clone = el.cloneNode(true) as Element;
  clone.querySelectorAll('.rev-glyph, style, script').forEach((g) => g.remove());
  return clone.textContent || '';
}

export function anchorFor(el: Element): string {
  return contentAnchor(headingOf(el), anchorText(el));
}

export function labelFor(el: Element): string {
  return labelOf(anchorText(el));
}
