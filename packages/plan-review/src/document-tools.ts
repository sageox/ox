// Document tools: run inside the plan document. They find what the reviewer
// points at (an element, or the words they selected) or draws around, and
// paint the marks and drawings the host sends. They read no storage and make
// no request, so they run in an opaque-origin frame as well as on the CLI's
// own review page; everything the reviewer decides lives in the host.

import { anchorFor, clip, headingOf, labelFor, markableSelector, norm, quoteAnchor } from './anchor.js';
import { drawLayer, markables } from './draw.js';
import { pointerLayer } from './pointer.js';
import type { DocumentPort } from './ports.js';
import type { DrawingPaint, PaintItem, Placed, Target } from './protocol.js';
import { DRAWING } from './region.js';

export { localPorts, connectParent } from './ports.js';
export type { DocumentPort, HostPort, Port } from './ports.js';

/**
 * Host controls drawn into this document (the CLI's review page): never a
 * mark-up target, and clicks on them are the host's own.
 */
export const CHROME = '.rev-bar, .rev-rail, .rev-orphans, .rev-toast, .rev-sync, .rev-offline-bar, .rev-original, .rev-cursors';

export interface DocumentToolsOptions {
  /** Send the review keys (r, Esc) to the host: set when its controls live outside this document. */
  forwardKeys?: boolean;
}

/** typing: the key went to a text field, where `r` is a letter, not a command. */
export function typing(e: KeyboardEvent): boolean {
  const t = e.target as HTMLElement | null;
  return !!t && (t.tagName === 'TEXTAREA' || t.tagName === 'INPUT' || t.isContentEditable);
}

type Highlight = { range: Range; section: string; label: string; quote: string };
type Spot = { p: Placed; at: Element | Range };

const INLINE = /^(A|ABBR|B|BDI|BDO|CITE|CODE|DATA|DEL|DFN|EM|I|INS|KBD|MARK|Q|S|SAMP|SMALL|SPAN|STRONG|SUB|SUP|TIME|U|VAR|WBR)$/;
const TINTS = ['sage', 'amber', 'red', 'gold', 'faint'];
// A plan's own controls, which a click in review mode leaves to do their job:
// a tab's label checks its radio, a summary opens its details. A link inside
// one stays a pick, since following it would navigate away from the review.
const OWN = 'input, select, textarea, label, summary';

/** startDocumentTools runs the document tools against a host on the other end of port. */
export function startDocumentTools(port: DocumentPort, opts: DocumentToolsOptions = {}): void {
  const body = document.body;
  const SELECTOR = markableSelector(document);
  const NOT_TEXT = CHROME + ', .rev-pop, .rev-glyph, .rev-hint, script, style, noscript, textarea';
  // The CSS Custom Highlight API tints ranges without touching the page's DOM.
  // Without it, highlights still anchor, list in the rail, and reopen on click.
  const HL = !!(window.CSS && CSS.highlights && window.Highlight);

  let on = false; // review mode, as the host last set it
  let open = false; // the host's composer is open
  let items: PaintItem[] = [];
  let drawings: DrawingPaint[] = [];
  // anchor-keyed maps have no prototype: an anchor named like an Object
  // member ("constructor", "__proto__") must be just a key
  let qRanges: Record<string, Highlight> = Object.create(null); // anchor -> highlight the last paint found
  let spots: Spot[] = []; // every painted mark, in document order
  let picked: { a: string; range: Range } | null = null; // the words the last target named
  let focused: Range | null = null;
  let started = false; // ready sent: before it, a target would anchor on a diagram's source
  let tool = false; // the host selected the Draw tool, which also waits on started

  function isRange(at: Element | Range): at is Range {
    return 'startContainer' in at;
  }

  // The host's controls drawn into this document never take a mark and are
  // never part of a drawing.
  const NOT_PLAN = CHROME + ', .rev-pop';
  // A stroke in the Draw tool becomes a capture the host checks and keeps; a
  // layout change that moves a drawing is placed again, and the host told.
  const draw = drawLayer(SELECTOR, NOT_PLAN,
    (capture, rect, anchor) => port.send({ type: 'capture', capture, rect, anchor }),
    (reason) => port.send({ type: 'refused', reason }),
    () => paint(items, drawings));
  // Live pointers: share this reviewer's as the content under it, and place
  // the ones the host sends, which the host names and draws.
  const pointers = pointerLayer(SELECTOR, NOT_PLAN,
    (at) => port.send({ type: 'pointer', at }),
    (points) => port.send({ type: 'located', points }));

  function paint(next: PaintItem[], nextDrawings: DrawingPaint[]) {
    items = next;
    drawings = nextDrawings;
    document.querySelectorAll('.rev-marked,.rev-committed').forEach((el) => {
      el.classList.remove('rev-marked', 'rev-committed');
      el.removeAttribute('data-rev');
      el.removeAttribute('data-revstate');
      const g = el.querySelector(':scope > .rev-glyph');
      if (g) g.remove();
    });
    const byAnchor: Record<string, PaintItem> = Object.create(null);
    items.forEach((it) => { byAnchor[it.anchor] = it; });
    const marked: Array<{ el: Element; a: string }> = [];
    // listed once, for the marks and the drawings both
    const all = markables(SELECTOR, NOT_PLAN);
    all.forEach(({ el, anchor: a }) => {
      const it = byAnchor[a];
      if (!it) return;
      const glyph = document.createElement('span');
      glyph.className = 'rev-glyph';
      glyph.textContent = it.glyph;
      if (it.kind === 'committed') {
        el.classList.add('rev-committed');
        el.setAttribute('data-revstate', it.state || '');
      } else {
        el.classList.add('rev-marked');
        el.setAttribute('data-rev', it.status);
      }
      el.appendChild(glyph);
      marked.push({ el, a });
    });
    const found: Spot[] = marked.map((m) => ({ p: { anchor: m.a, section: headingOf(m.el), label: labelFor(m.el) }, at: m.el }));
    paintQuotes(found);
    // a drawing whose targets are missing or ambiguous is not on the page: it
    // is reported after everything that is
    const lost: Placed[] = [];
    draw.paint(drawings, all).forEach((d) => {
      const p: Placed = { anchor: d.anchor, section: '', label: '', outcome: d.outcome };
      if (d.el && d.outcome !== 'missing' && d.outcome !== 'ambiguous') found.push({ p, at: d.el });
      else lost.push(p);
    });
    // reading order: highlights and drawings interleaved with elements; the sort is stable
    const pos = found.map((s) => {
      if (isRange(s.at)) return s.at;
      const r = document.createRange();
      r.selectNode(s.at);
      return r;
    });
    const order = found.map((_, i) => i).sort((x, y) => pos[x].compareBoundaryPoints(Range.START_TO_START, pos[y]));
    spots = order.map((i) => found[i]);
    port.send({ type: 'placed', items: spots.map((s) => s.p).concat(lost) });
  }

  // paintQuotes finds each highlight's words on the page, records it for
  // clicks and placement, and tints it.
  function paintQuotes(found: Spot[]) {
    qRanges = Object.create(null);
    const tints: Record<string, Range[]> = {};
    let idx: TextIndex | null = null;
    items.forEach((m) => {
      if (!m.quote || !norm(m.quote)) return; // a blank quote builds an empty pattern, which never advances
      idx = idx || textIndex();
      let hit = quoteHits(idx, m.section || '', m.quote)[0];
      // a renamed heading moves no words: accept them anywhere, if only once
      const anywhere = hit ? null : quoteHits(idx, null, m.quote);
      if (anywhere && anywhere.length === 1) hit = anywhere[0];
      if (!hit) return; // its words changed: the host lists it as unplaced
      const h: Highlight = { section: m.section || '', label: m.label || clip(m.quote, 70), quote: m.quote, range: rangeAt(idx, hit[0], hit[1]) };
      qRanges[m.anchor] = h;
      found.push({ p: { anchor: m.anchor, section: h.section, label: h.label, quote: h.quote }, at: h.range });
      (tints[m.tint] = tints[m.tint] || []).push(h.range);
    });
    if (!HL) return;
    TINTS.forEach((c) => {
      CSS.highlights.delete('rev-q-' + c);
      if (!tints[c]) return;
      const hl = new Highlight();
      tints[c].forEach((r) => hl.add(r));
      CSS.highlights.set('rev-q-' + c, hl);
    });
  }

  // focusRange tints one range in the focus color — the words an open note is
  // about, or the highlight a reveal jumped to; null clears it.
  function focusRange(r: Range | null) {
    focused = r;
    if (!HL) return;
    if (!r) { CSS.highlights.delete('rev-q-focus'); return; }
    const hl = new Highlight();
    hl.add(r);
    CSS.highlights.set('rev-q-focus', hl);
  }
  function flashRange(r: Range) {
    focusRange(r);
    setTimeout(() => { if (focused === r) focusRange(null); }, 1200); // a note opened since keeps its own focus
  }
  function flashEl(el: Element) {
    el.classList.add('rev-flash');
    setTimeout(() => el.classList.remove('rev-flash'), 1200);
  }
  function reveal(anchor: string, nth: number) {
    // a hidden drawing is shown first: what capture had open or checked, and
    // any <details> around its targets, then it is placed again
    const d = DRAWING.test(anchor) ? drawings.filter((x) => x.anchor === anchor)[0] : null;
    if (d && spots.some((x) => x.p.anchor === anchor && x.p.outcome === 'hidden')) {
      draw.show(d);
      paint(items, drawings);
    }
    const s = spots.filter((x) => x.p.anchor === anchor)[nth];
    if (!s) return;
    const range = isRange(s.at) ? s.at : null;
    const el = range ? range.startContainer.parentElement : (s.at as Element);
    if (!el) return;
    el.scrollIntoView({ behavior: 'smooth', block: 'center' });
    if (range) flashRange(range);
    else flashEl(el);
  }

  // --- highlights: a text selection comments on exactly the words selected.
  // The click that ends a drag or double-click carries a non-empty selection;
  // a plain click does not — that is what tells a highlight from an element
  // mark. A highlight anchors on (section heading, quoted text), not on its
  // element's full text, so an edit elsewhere in the section leaves it in place.

  // textIndex joins the page's text nodes — never review chrome, glyphs, or
  // scripts — into one string, remembering where each starts, so a quote is
  // found across inline markup and mapped back to a DOM Range. Text split by
  // anything but inline markup (cells, items, paragraphs, a <br> or an <img>)
  // is joined with a newline, even where the markup has no whitespace between
  // the elements, so the end of one cell and the start of the next never read
  // as one word.
  type TextIndex = { nodes: Array<{ node: Text; start: number; h?: string }>; text: string };
  function textIndex(): TextIndex {
    const nodes: TextIndex['nodes'] = [];
    let text = '', prev: Text | null = null, brk = false;
    const w = document.createTreeWalker(body, NodeFilter.SHOW_TEXT | NodeFilter.SHOW_ELEMENT);
    for (let n = w.nextNode(); n; n = w.nextNode()) {
      // entering one splits the text around it; inlineBetween catches the
      // elements the walk leaves, which it never reports
      if (n.nodeType === 1) { if (!INLINE.test((n as Element).tagName)) brk = true; continue; }
      const t = n as Text;
      if (!t.parentElement || t.parentElement.closest(NOT_TEXT)) continue;
      if (prev && (brk || !inlineBetween(prev, t))) text += '\n';
      brk = false;
      nodes.push({ node: t, start: text.length });
      text += t.data;
      prev = t;
    }
    return { nodes, text };
  }
  // inlineBetween: whether every element from each text node up to their
  // common ancestor is inline markup, i.e. the two run on as one line of text.
  function inlineBetween(a: Node, b: Node) {
    const up: Element[] = [];
    for (let e = a.parentElement; e; e = e.parentElement) up.push(e);
    for (let f = b.parentElement; f && up.indexOf(f) < 0; f = f.parentElement) if (!INLINE.test(f.tagName)) return false;
    for (let i = 0; i < up.length && !up[i].contains(b); i++) if (!INLINE.test(up[i].tagName)) return false;
    return true;
  }
  // nodeAt: the entry holding offset pos. An end offset resolves to the entry
  // it closes, so a range never ends at offset 0 of the next node.
  function nodeAt(idx: TextIndex, pos: number, isEnd?: boolean) {
    let lo = 0, hi = idx.nodes.length - 1;
    while (lo < hi) {
      const mid = (lo + hi + 1) >> 1, s = idx.nodes[mid].start;
      if (isEnd ? s < pos : s <= pos) lo = mid; else hi = mid - 1;
    }
    return idx.nodes[lo];
  }
  function rangeAt(idx: TextIndex, s: number, e: number) {
    const a = nodeAt(idx, s), b = nodeAt(idx, e, true), r = document.createRange();
    r.setStart(a.node, s - a.start);
    r.setEnd(b.node, e - b.start);
    return r;
  }
  function headingAt(idx: TextIndex, pos: number) {
    const x = nodeAt(idx, pos);
    if (x.h === undefined) x.h = norm(headingOf(x.node.parentElement as Element));
    return x.h;
  }
  // quoteHits: every [start, end) where quote occurs under the section heading
  // (anywhere, when section is null), matched the way norm() compares text —
  // any whitespace run, any case — and, where the quote starts or ends with a
  // word character, only at a word boundary, so "retry" is not found in
  // "retrying".
  function quoteHits(idx: TextIndex, section: string | null, quote: string) {
    const q = quote.trim();
    const words = q.split(/\s+/).map((w) => w.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'));
    const re = new RegExp((/^\w/.test(q) ? '(^|\\W)' : '()') + words.join('\\s+') + (/\w$/.test(q) ? '(?!\\w)' : ''), 'gi');
    const want = section === null ? null : norm(section), hits: Array<[number, number]> = [];
    let m: RegExpExecArray | null;
    while ((m = re.exec(idx.text))) {
      const at = m.index + m[1].length; // past the boundary character the pattern consumed
      if (want === null || headingAt(idx, at) === want) hits.push([at, m.index + m[0].length]);
    }
    return hits;
  }

  type Pick = Omit<Target, 'rect'> & { range?: Range; rect?: Target['rect'] };
  type Refusal = { refuse: 'cross-section' } | { refuse: 'repeated'; quote: string; count: number };

  // selectionTarget turns the page selection into a highlight target: null
  // when there is none (a plain click, or a stray drag across one character),
  // a refusal when the words cannot anchor to one place.
  function selectionTarget(): Pick | Refusal | null {
    const sel = window.getSelection();
    if (!sel || sel.isCollapsed || !sel.rangeCount) return null;
    const range = sel.getRangeAt(0), idx = textIndex();
    let s = -1, e = -1;
    idx.nodes.forEach((x) => {
      if (!range.intersectsNode(x.node)) return;
      const from = x.node === range.startContainer ? range.startOffset : 0;
      const to = x.node === range.endContainer ? range.endOffset : x.node.data.length;
      if (to <= from) return;
      if (s < 0) s = x.start + from;
      e = x.start + to;
    });
    while (s >= 0 && s < e && /\s/.test(idx.text.charAt(s))) s++;
    while (e > s && /\s/.test(idx.text.charAt(e - 1))) e--;
    if (s < 0 || idx.text.slice(s, e).replace(/\s/g, '').length < 2) return null;
    // a drag that starts or ends mid-word takes the whole word: quoteHits
    // finds a quote's word edges only at word boundaries
    while (s > 0 && /\w/.test(idx.text.charAt(s - 1)) && /\w/.test(idx.text.charAt(s))) s--;
    while (/\w/.test(idx.text.charAt(e)) && /\w/.test(idx.text.charAt(e - 1))) e++;
    const quote = idx.text.slice(s, e).replace(/\s+/g, ' ');
    // the agent reads a highlight as its words plus its section, so the words
    // must sit in one section (or all outside every section, like the TL;DR)
    // and name one place in it
    const first = nodeAt(idx, s).node.parentElement as Element, last = nodeAt(idx, e, true).node.parentElement as Element;
    if (first.closest('section[id], [data-ox-section]') !== last.closest('section[id], [data-ox-section]')) {
      return { refuse: 'cross-section' };
    }
    const section = headingOf(first);
    const n = quoteHits(idx, section, quote).length;
    if (n > 1) return { refuse: 'repeated', quote, count: n };
    const box = range.getBoundingClientRect();
    return { anchor: quoteAnchor(section, quote), section, label: clip(quote, 70), quote, range: rangeAt(idx, s, e), rect: { left: box.left, bottom: box.bottom } };
  }
  // highlightAt: the painted highlight under the click point, by its words' own
  // boxes. The caret can't tell: a click beside a highlight snaps it to the
  // highlight's edge, and a click on an image or a button leaves it where it was.
  function highlightAt(ev: MouseEvent): Pick | null {
    for (const a in qRanges) {
      const rects = qRanges[a].range.getClientRects();
      for (let i = 0; i < rects.length; i++) {
        const r = rects[i];
        if (ev.clientX >= r.left && ev.clientX <= r.right && ev.clientY >= r.top && ev.clientY <= r.bottom) {
          const h = qRanges[a];
          return { anchor: a, section: h.section, label: h.label, quote: h.quote, range: h.range };
        }
      }
    }
    return null;
  }
  // A selection counts only if this click made it: a drag, a double or triple
  // click, or a shift-click extending one. One left from before (Esc on its
  // note, text selected to copy) survives a click on a button or an image, and
  // that click marks the element, not the old words.
  let downAt: { x: number; y: number } | null = null;
  document.addEventListener('mousedown', (e) => { downAt = { x: e.clientX, y: e.clientY }; }, true);
  function madeSelection(ev: MouseEvent) {
    if (ev.detail > 1 || ev.shiftKey) return true;
    return ev.detail === 1 && !!downAt && Math.abs(ev.clientX - downAt.x) + Math.abs(ev.clientY - downAt.y) > 3;
  }

  function onClick(ev: MouseEvent) {
    if (!started) return;
    const el0 = ev.target as Element;
    // the host's own controls, drawn into this document, are the host's; a
    // click on the ink layer ends a stroke, which made a capture or a refusal
    if (el0.closest && el0.closest(CHROME + ', .rev-pop, .rev-ink')) return;
    if (!on) { port.send({ type: 'dismiss' }); return; }
    // a selection means a drag or double-click just ended: comment on those
    // words. A plain click on highlighted words reopens that highlight; any
    // other click marks the element under it, unless one of the plan's own
    // controls takes it.
    let t = (madeSelection(ev) && selectionTarget()) || highlightAt(ev);
    if (t && 'refuse' in t) {
      ev.preventDefault();
      port.send(t.refuse === 'repeated' ? { type: 'refused', reason: 'repeated', quote: t.quote, count: t.count } : { type: 'refused', reason: 'cross-section' });
      return;
    }
    if (!t) {
      const el = el0.closest(OWN) && !el0.closest('a[href]') ? null : el0.closest(SELECTOR);
      if (!el) { port.send({ type: 'dismiss' }); return; }
      t = { anchor: anchorFor(el), section: headingOf(el), label: labelFor(el) };
    }
    ev.preventDefault();
    picked = t.range ? { a: t.anchor, range: t.range } : null;
    // the composer opens just below where the reviewer acted — under a new
    // highlight's text, else under the click — never over it: the second press
    // of a double-click lands where the first one opened a note, and has to
    // reach the word to select it
    const target: Target = { anchor: t.anchor, section: t.section, label: t.label, rect: t.rect || { left: ev.clientX, bottom: ev.clientY } };
    if (t.quote !== undefined) target.quote = t.quote;
    port.send({ type: 'target', target });
    ev.stopPropagation();
  }
  document.addEventListener('click', onClick, true);

  // Keys reach the host by themselves when it shares this document. A framed
  // document forwards r and Esc, marked handled the way the host would: Esc
  // only when there is a note or a mode to close.
  if (opts.forwardKeys) {
    document.addEventListener('keydown', (e) => {
      if (e.defaultPrevented) return;
      if (e.key === 'Escape') {
        if (open || on) { e.preventDefault(); port.send({ type: 'key', key: 'Escape' }); }
        return;
      }
      if (e.key === 'r' && !typing(e) && !e.metaKey && !e.ctrlKey && !e.altKey) { e.preventDefault(); port.send({ type: 'key', key: 'r' }); }
    });
  }

  port.listen((m) => {
    switch (m.type) {
      case 'mode':
        on = m.on;
        open = m.open;
        body.classList.toggle('rev-on', on);
        tool = on && m.draw;
        draw.setTool(tool && started);
        break;
      case 'paint':
        paint(m.items, m.drawings);
        break;
      case 'focus': {
        // the words this anchor's note is about: the selection just picked,
        // or a painted highlight; nothing for an element
        const h = m.anchor === null ? null : picked && picked.a === m.anchor ? picked.range : qRanges[m.anchor] ? qRanges[m.anchor].range : null;
        focusRange(h);
        break;
      }
      case 'reveal':
        reveal(m.anchor, m.nth);
        break;
      case 'locate':
        pointers.locate(m.points);
        break;
    }
  });
  // An anchor hashes a diagram's drawn text, and Mermaid draws after load: a
  // host that painted, or a click that made a target, before then would use
  // the diagram's source. The CLI's host can turn review on before ready, so
  // clicks, strokes and pointers wait on `started` too.
  whenDiagramsDrawn(() => {
    started = true;
    draw.setTool(tool);
    port.send({ type: 'ready' });
    pointers.start();
  });
}

/**
 * whenDiagramsDrawn calls go once every Mermaid diagram holds its final <svg>,
 * or after 5 s for one that never draws. Mermaid renders into a temporary
 * <div> inside the element and only then swaps the finished <svg> in as the
 * element's child, so a nested <svg> is not yet a drawn diagram.
 */
function whenDiagramsDrawn(go: () => void): void {
  const deadline = Date.now() + 5000;
  const check = () => {
    const pending = Array.prototype.some.call(document.querySelectorAll('.mermaid'), (el: Element) => !el.querySelector(':scope > svg'));
    if (!pending || Date.now() >= deadline) go();
    else setTimeout(check, 100);
  };
  check();
}
