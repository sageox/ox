// draw.js — document-tool prototype for drawn-region review annotations
// (ox#1292). Not shipped: ox never embeds this file. The candidate format and
// the evidence it produced are in docs/specs/plan-review-drawn-context.md.
//
// It runs inside the plan document, either top-level on the local review
// origin or framed by a host under sandbox="allow-scripts" (opaque origin,
// no storage, no network). It needs neither: a capture leaves the document as
// data, through postMessage when framed and OxDraw.records when top-level.
// A framed document is untrusted, so the host mints the drawing's id and
// stamps the plan revision it framed; the document supplies only geometry
// and what lay under it.
(function () {
  'use strict';
  var V = 1;
  var SVGNS = 'http://www.w3.org/2000/svg';
  // review.js SELECTOR for generated pages plus chrome.js's for authored ones.
  var MARKABLE = 'section[id],[data-ox-section],h1,h2,h3,h4,p,li,tr,pre,blockquote,figure,.ox-chip,.stat,.bar-row';
  // Bounds on one capture. bytes is the serialized context in UTF-8; see the
  // spec for the measurements behind it.
  var LIMIT = { points: 512, targets: 16, items: 48, labels: 32, text: 600, targetText: 2000, label: 80, bytes: 16384, keys: 128 };
  var TOL = 1; // css px: geometry within this of the capture is unchanged

  function r1(n) { return Math.round(n * 10) / 10; }

  // --- ink: how a drawing is painted, from the record alone, so every host
  // paints it the same way. ---
  // A broad-nib pen held almost flat: the mark swells where the line turns
  // through the vertical and thins to a hairline along the horizontal runs, so
  // an ellipse around a line of text stays clear of the words above and below
  // it. The pen lands and lifts with tapered ends, and the line is never
  // closed into a polygon: where it runs past its start, the overlap shows.
  var NIB = { width: 12, angle: -12 * Math.PI / 180 };
  // smooth cuts corners twice (Chaikin), keeping both ends where the pen was.
  function smooth(pts) {
    for (var k = 0; k < 2 && pts.length > 2; k++) {
      var out = [pts[0]];
      for (var i = 0; i < pts.length - 1; i++) {
        var a = pts[i], b = pts[i + 1];
        out.push([0.75 * a[0] + 0.25 * b[0], 0.75 * a[1] + 0.25 * b[1]], [0.25 * a[0] + 0.75 * b[0], 0.25 * a[1] + 0.75 * b[1]]);
      }
      out.push(pts[pts.length - 1]);
      pts = out;
    }
    return pts;
  }
  // ellipsePoints samples a drawing's ellipse from where the pen landed, the
  // way it moved, for one turn plus extra radians, about every 3px.
  function ellipsePoints(e, extra) {
    var rx = e.radii[0], ry = e.radii[1], rot = e.rotation * Math.PI / 180, t0 = e.start * Math.PI / 180;
    var around = Math.PI * (3 * (rx + ry) - Math.sqrt((3 * rx + ry) * (rx + 3 * ry))); // Ramanujan's perimeter
    var sweep = 2 * Math.PI + extra, n = Math.max(48, Math.min(512, Math.ceil(around * sweep / (2 * Math.PI) / 3)));
    var cos = Math.cos(rot), sin = Math.sin(rot), out = [];
    for (var i = 0; i <= n; i++) {
      var t = t0 + e.dir * sweep * i / n, x = rx * Math.cos(t), y = ry * Math.sin(t);
      out.push([e.center[0] + x * cos - y * sin, e.center[1] + x * sin + y * cos]);
    }
    return out;
  }
  // inkLine is what gets inked for a record: its ellipse, run past its start
  // like a pen closing an O (0.3 rad, about 17°), or the raw path of a record
  // captured without one.
  function inkLine(rec) { return rec.ellipse ? ellipsePoints(rec.ellipse, 0.3) : rec.path; }
  // inkInto paints pts, shifted by off, into parent: the nib's sweep as one
  // filled path of quads, and a hairline along the centre so the stretches
  // that run along the nib never vanish. make(tag, attrs) creates an element.
  function inkInto(parent, make, pts, off, color, opacity) {
    pts = smooth(pts.map(function (p) { return [p[0] + off[0], p[1] + off[1]]; }));
    var g = make('g', { fill: color, stroke: color, opacity: opacity });
    parent.appendChild(g);
    if (pts.length < 2) return g;
    var at = [0];
    for (var i = 1; i < pts.length; i++) at.push(at[i - 1] + Math.hypot(pts[i][0] - pts[i - 1][0], pts[i][1] - pts[i - 1][1]));
    var total = at[at.length - 1];
    // the nib reaches full width 24px after landing and lifts over the last 56px
    function width(d) { return Math.max(0.1, Math.min(1, d / 24, (total - d) / 56)); }
    var nx = Math.cos(NIB.angle) * NIB.width / 2, ny = Math.sin(NIB.angle) * NIB.width / 2;
    function xy(p, s) { return r1(p[0] + nx * s) + ' ' + r1(p[1] + ny * s); }
    var sweep = '', hair = 'M' + xy(pts[0], 0);
    for (var j = 0; j < pts.length - 1; j++) {
      var a = pts[j], b = pts[j + 1], wa = width(at[j]), wb = width(at[j + 1]);
      sweep += 'M' + xy(a, wa) + 'L' + xy(b, wb) + 'L' + xy(b, -wb) + 'L' + xy(a, -wa) + 'Z';
      hair += 'L' + xy(b, 0);
    }
    g.appendChild(make('path', { d: sweep, stroke: 'none' }));
    g.appendChild(make('path', { d: hair, fill: 'none', 'stroke-width': 1, 'stroke-linecap': 'round', 'stroke-linejoin': 'round' }));
    return g;
  }

  // --- View original: the capture redrawn from its own context, no page needed ---
  // originalView builds an SVG of what lay under the drawing when it was made
  // (element boxes with their text, diagram labels, the inked ellipse), in doc —
  // the host's document when framed. Text goes in as text nodes, never markup.
  function originalView(rec, doc) {
    var mk = function (tag, attrs, text) {
      var n = doc.createElementNS(SVGNS, tag);
      for (var k in attrs) n.setAttribute(k, attrs[k]);
      if (text) n.textContent = text;
      return n;
    };
    var b = rec.bbox, pad = 24, vb = [b[0] - pad, b[1] - pad, b[2] + 2 * pad, b[3] + 2 * pad];
    var out = mk('svg', { viewBox: vb.join(' '), width: vb[2], height: vb[3], 'class': 'oxd-original', role: 'img' });
    out.appendChild(mk('title', {}, 'Drawn on plan revision ' + (rec.plan_revision || 'unknown')));
    ((rec.context && rec.context.items) || []).forEach(function (it) {
      var x = it.box[0], y = it.box[1], w = it.box[2], h = it.box[3];
      out.appendChild(mk('rect', { x: x, y: y, width: w, height: h, fill: 'none', stroke: '#8c8e7e', 'stroke-width': 0.5 }));
      if (it.labels) {
        it.labels.forEach(function (l) {
          out.appendChild(mk('rect', { x: l.box[0], y: l.box[1], width: l.box[2], height: l.box[3], fill: 'none', stroke: '#97aebd', 'stroke-width': 0.5 }));
          out.appendChild(mk('text', { x: l.box[0] + 2, y: l.box[1] + Math.min(l.box[3], 14) - 3, 'font-size': 11, fill: '#97aebd' }, l.text));
        });
        // whatever text the labels do not account for (a figure's caption)
        // goes along the bottom of the item's box
        var rest = it.labels.reduce(function (t, l) { return t.replace(l.text, ' '); }, it.text).replace(/\s+/g, ' ').trim();
        if (rest) out.appendChild(mk('text', { x: x + 2, y: y + h - 4, 'font-size': 11, fill: '#b7b6a3' }, rest));
        return;
      }
      // wrap the item's text at ~7px a character, as many lines as its box holds
      var per = Math.max(8, Math.floor(w / 7)), rows = Math.max(1, Math.floor(h / 15)), words = it.text.split(' '), line = '', n = 0;
      var text = mk('text', { 'font-size': 12, fill: '#b7b6a3' });
      function flush() { if (n < rows && line) text.appendChild(mk('tspan', { x: x + 2, y: y + 13 + n * 15 }, line)); n++; line = ''; }
      words.forEach(function (wd) { if (line && (line + ' ' + wd).length > per) flush(); line = line ? line + ' ' + wd : wd; });
      flush();
      out.appendChild(text);
    });
    inkInto(out, mk, inkLine(rec), [0, 0], '#d9b654', 0.95);
    if (rec.context && rec.context.truncated) {
      out.appendChild(mk('text', { x: vb[0] + 4, y: vb[1] + vb[3] - 6, 'font-size': 11, fill: '#c6a23c' }, 'context truncated: some of what lay here was not kept'));
    }
    return out;
  }

  window.OxDrawOriginal = originalView;
  // A host loads this file with data-role="host" for View original alone.
  var me = document.currentScript;
  if (me && me.getAttribute('data-role') === 'host') return;

  var embedded = window.parent !== window;
  var instance = (/[#&]i=([A-Za-z0-9_-]{8,64})/.exec(location.hash) || [])[1] || '';
  var config = readConfig();
  var records = []; // what to place: the host's list when framed, OxDraw.records top-level
  var outcomes = {};

  // --- anchors: review.js's content hash, minus text that changes per render ---
  function fnv1a(s) { var h = 0x811c9dc5; for (var i = 0; i < s.length; i++) { h ^= s.charCodeAt(i); h = (h * 0x01000193) >>> 0; } return ('0000000' + h.toString(16)).slice(-8); }
  function norm(s) { return (s || '').replace(/\s+/g, ' ').trim().toLowerCase(); }
  function collapse(s) { return (s || '').replace(/\s+/g, ' ').trim(); }
  function headingOf(el) { var sec = el.closest('section[id], [data-ox-section]'); if (!sec) return ''; var ds = sec.getAttribute('data-ox-section'); if (ds) return ds; var h = sec.querySelector('h2, h3'); return h ? h.textContent : (sec.id || ''); }
  // stableText is review.js anchorText without <style> and <script> text:
  // Mermaid names each SVG mermaid-<Date.now()> and repeats that id in the
  // SVG's <style>, so that text differs on every render of the same diagram.
  function stableText(el) {
    if (!el.querySelector('style, script, .oxd')) return el.textContent || '';
    var c = el.cloneNode(true);
    c.querySelectorAll('style, script, .oxd').forEach(function (n) { n.remove(); });
    return c.textContent || '';
  }
  function anchorOf(el) { return 'h' + fnv1a(norm(headingOf(el)) + '\u0000' + norm(stableText(el))); }
  function markables() {
    var out = [];
    document.querySelectorAll(MARKABLE).forEach(function (el) { if (!el.closest('.oxd')) out.push({ el: el, anchor: anchorOf(el) }); });
    return out;
  }
  function byAnchor(all) { var m = {}; all.forEach(function (c) { (m[c.anchor] = m[c.anchor] || []).push(c); }); return m; }

  // --- geometry, in the document frame: CSS px from the document's top-left ---
  function rectOf(r) { return [r1(r.left + scrollX), r1(r.top + scrollY), r1(r.width), r1(r.height)]; }
  // shownBox is el's document box, or null when the reader cannot see it:
  // not rendered (display:none), invisible, inside a closed <details>, empty,
  // or scrolled out of an ancestor that clips its overflow. A closed
  // <details> hides its content with content-visibility:hidden in current
  // Chrome, which still lays it out: getBoundingClientRect returns the box it
  // would have open, so only checkVisibility (or the <details> walk where that
  // is missing) tells it apart.
  function shownBox(el) {
    if (el.checkVisibility ? !el.checkVisibility({ visibilityProperty: true }) : (!el.getClientRects().length || getComputedStyle(el).visibility === 'hidden' || inClosedDetails(el))) return null;
    var r = el.getBoundingClientRect();
    if (r.width < 1 || r.height < 1) return null;
    for (var a = el.parentElement; a && a !== document.body && a !== document.documentElement; a = a.parentElement) {
      var cs = getComputedStyle(a);
      if (cs.overflowX === 'visible' && cs.overflowY === 'visible') continue;
      var ar = a.getBoundingClientRect();
      if (r.right <= ar.left || r.left >= ar.right || r.bottom <= ar.top || r.top >= ar.bottom) return null;
    }
    return rectOf(r);
  }
  function inClosedDetails(el) {
    for (var d = el.parentElement && el.parentElement.closest('details:not([open])'); d; d = d.parentElement && d.parentElement.closest('details:not([open])')) {
      var sum = d.querySelector(':scope > summary');
      if (!sum || !sum.contains(el)) return true;
    }
    return false;
  }
  function bboxOf(pts) {
    var x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
    pts.forEach(function (p) { x0 = Math.min(x0, p[0]); y0 = Math.min(y0, p[1]); x1 = Math.max(x1, p[0]); y1 = Math.max(y1, p[1]); });
    return [r1(x0), r1(y0), r1(x1 - x0), r1(y1 - y0)];
  }
  function overlaps(a, b) { return a[0] < b[0] + b[2] && b[0] < a[0] + a[2] && a[1] < b[1] + b[3] && b[1] < a[1] + a[3]; }
  // inside: the closed polygon poly winds around pt (nonzero winding).
  function inside(pt, poly) {
    var w = 0;
    for (var i = 0, j = poly.length - 1; i < poly.length; j = i++) {
      var a = poly[j], b = poly[i], cross = (b[0] - a[0]) * (pt[1] - a[1]) - (pt[0] - a[0]) * (b[1] - a[1]);
      if (a[1] <= pt[1]) { if (b[1] > pt[1] && cross > 0) w++; } else if (b[1] <= pt[1] && cross < 0) w--;
    }
    return w !== 0;
  }
  // coverage: the share of a 5x5 grid over box that poly encloses.
  function coverage(box, poly, bb) {
    if (!overlaps(box, bb)) return 0;
    var n = 0;
    for (var i = 0; i < 5; i++) for (var j = 0; j < 5; j++) if (inside([box[0] + box[2] * (i + 0.5) / 5, box[1] + box[3] * (j + 0.5) / 5], poly)) n++;
    return n / 25;
  }
  function leafMost(list) { return list.filter(function (c) { return !list.some(function (d) { return d !== c && c.el.contains(d.el); }); }); }
  function outerMost(list) { return list.filter(function (c) { return !list.some(function (d) { return d !== c && d.el.contains(c.el); }); }); }
  function clip(s, n) { return s.length > n ? s.slice(0, n - 1) + '…' : s; }
  // utf8Bytes is the size of v as stored: UTF-8 JSON, not JavaScript string units
  function utf8Bytes(v) { return new TextEncoder().encode(JSON.stringify(v)).length; }

  // --- layout context: the conditions the geometry was captured under ---
  function detailsKey(d) { var s = d.querySelector('summary'); return 'd' + fnv1a(norm(s ? s.textContent : '')); }
  function inputKey(i) { return i.id ? '#' + i.id : i.name + '=' + i.value; }
  function layoutContext() {
    fitLayer(); // so document below is the page's size, not a stale layer's
    var open = [], checked = [];
    document.querySelectorAll('details[open]').forEach(function (d) { if (!d.closest('.oxd')) open.push(detailsKey(d)); });
    document.querySelectorAll('input[type=radio]:checked, input[type=checkbox]:checked').forEach(function (i) { checked.push(inputKey(i)); });
    var root = document.documentElement, vv = window.visualViewport;
    return {
      viewport: [innerWidth, innerHeight],
      document: [root.scrollWidth, root.scrollHeight],
      dpr: devicePixelRatio,
      scale: vv ? r1(vv.scale) : 1,
      scroll: [r1(scrollX), r1(scrollY)],
      theme: root.getAttribute('data-theme') || (matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark'),
      fonts: document.fonts ? document.fonts.status : '',
      open: open.slice(0, LIMIT.keys),
      checked: checked.slice(0, LIMIT.keys)
    };
  }

  // --- capture: geometry plus the bounded context a later reader needs ---
  // fitEllipse is the ellipse a stroke means: the one with the stroke's area,
  // centre and second moments (closing the stroke back to its start), squared
  // to the page when within 10° of it, starting where the pen landed and
  // running the way it moved. A stroke that encloses almost nothing (a line)
  // gets the ellipse inscribed in its bounding box.
  function fitEllipse(pts) {
    var A = 0, sx = 0, sy = 0, sxx = 0, syy = 0, sxy = 0;
    for (var i = 0; i < pts.length; i++) {
      var p = pts[i], q = pts[(i + 1) % pts.length], c = p[0] * q[1] - q[0] * p[1];
      A += c / 2;
      sx += (p[0] + q[0]) * c;
      sy += (p[1] + q[1]) * c;
      sxx += (p[0] * p[0] + p[0] * q[0] + q[0] * q[0]) * c;
      syy += (p[1] * p[1] + p[1] * q[1] + q[1] * q[1]) * c;
      sxy += (p[0] * q[1] + 2 * p[0] * p[1] + 2 * q[0] * q[1] + q[0] * p[1]) * c;
    }
    var bb = bboxOf(pts), cx, cy, rx, ry, rot = 0;
    if (Math.abs(A) < 0.05 * bb[2] * bb[3]) {
      cx = bb[0] + bb[2] / 2; cy = bb[1] + bb[3] / 2; rx = bb[2] / 2; ry = bb[3] / 2;
    } else {
      // a uniform ellipse with semi-axes a, b has variance a²/4 and b²/4 along them
      cx = sx / (6 * A); cy = sy / (6 * A);
      var vxx = sxx / (12 * A) - cx * cx, vyy = syy / (12 * A) - cy * cy, vxy = sxy / (24 * A) - cx * cy;
      var mid = (vxx + vyy) / 2, d = Math.sqrt((vxx - vyy) * (vxx - vyy) / 4 + vxy * vxy);
      rot = 0.5 * Math.atan2(2 * vxy, vxx - vyy);
      rx = 2 * Math.sqrt(Math.max(mid + d, 0)); ry = 2 * Math.sqrt(Math.max(mid - d, 0));
      var snap = 10 * Math.PI / 180;
      if (Math.abs(rot) < snap) rot = 0;
      else if (Math.PI / 2 - Math.abs(rot) < snap) { rot = 0; var t = rx; rx = ry; ry = t; }
    }
    var dx = pts[0][0] - cx, dy = pts[0][1] - cy, cos = Math.cos(rot), sin = Math.sin(rot);
    var start = Math.atan2((-dx * sin + dy * cos) / (ry || 1), (dx * cos + dy * sin) / (rx || 1));
    return { center: [r1(cx), r1(cy)], radii: [r1(rx), r1(ry)], rotation: r1(rot * 180 / Math.PI), start: r1(start * 180 / Math.PI), dir: A < 0 ? -1 : 1 };
  }
  // capturePath turns a stroke (document-frame points) into a capture, or
  // {refused: 'no-target' | 'too-large'}. What it encloses is decided by the
  // fitted ellipse: that is the shape the reviewer sees once the pen lifts.
  function capturePath(pts) {
    pts = thin(pts);
    var ellipse = fitEllipse(pts), poly = ellipsePoints(ellipse, 0), bb = bboxOf(poly);
    var all = markables(), index = byAnchor(all);
    var shown = [];
    all.forEach(function (c) { var b = shownBox(c.el); if (b) { c.box = b; c.cover = coverage(b, poly, bb); shown.push(c); } });
    // targets: the innermost elements the ellipse mostly encloses; for a large
    // region, the outermost ones (a whole section rather than its every row)
    var covered = shown.filter(function (c) { return c.cover >= 0.5; });
    var targets = leafMost(covered);
    if (targets.length > LIMIT.targets) targets = outerMost(covered);
    if (!targets.length) {
      // an ellipse inside one element (a node of a diagram): the smallest
      // element holding its center, covered only in part
      var cx = bb[0] + bb[2] / 2, cy = bb[1] + bb[3] / 2, best = null;
      shown.forEach(function (c) {
        var b = c.box;
        if (cx >= b[0] && cx <= b[0] + b[2] && cy >= b[1] && cy <= b[1] + b[3] && (!best || b[2] * b[3] < best.box[2] * best.box[3])) best = c;
      });
      if (best) targets = [best];
    }
    if (!targets.length) return { refused: 'no-target' };
    if (targets.length > LIMIT.targets) return { refused: 'too-large' };
    return {
      v: V,
      kind: 'region',
      frame: 'document',
      path: pts,
      ellipse: ellipse,
      bbox: bb,
      layout: layoutContext(),
      targets: targets.map(function (c) {
        var same = index[c.anchor];
        var t = { anchor: c.anchor, tag: c.el.tagName.toLowerCase(), section: collapse(headingOf(c.el)), box: c.box, cover: c.cover };
        if (same.length > 1) { t.nth = same.indexOf(c); t.of = same.length; }
        return t;
      }),
      context: contextOf(shown, bb, targets)
    };
  }
  // thin drops points closer than 2px to the last kept one, then strides the
  // rest down to LIMIT.points.
  function thin(pts) {
    var out = [];
    pts.forEach(function (p) { var q = out[out.length - 1]; if (!q || Math.abs(p[0] - q[0]) + Math.abs(p[1] - q[1]) >= 2) out.push(p); });
    if (out.length <= LIMIT.points) return out;
    var step = out.length / LIMIT.points, s = [];
    for (var i = 0; i < LIMIT.points; i++) s.push(out[Math.floor(i * step)]);
    return s;
  }
  // contextOf keeps what lay under and around the drawing — the innermost
  // markable elements meeting its box (plus a 16px margin), nearest first, with
  // the text the reader saw (innerText: rendered text only, cells and lines
  // kept apart) and any diagram labels — so the drawing can be shown again
  // after the page has changed. Bounded by LIMIT; truncated says so.
  function contextOf(shown, bb, targets) {
    var m = 16, zone = [bb[0] - m, bb[1] - m, bb[2] + 2 * m, bb[3] + 2 * m];
    var cx = bb[0] + bb[2] / 2, cy = bb[1] + bb[3] / 2;
    var near = leafMost(shown.filter(function (c) { return overlaps(c.box, zone); }));
    near.sort(function (a, b) { return dist(a.box) - dist(b.box); });
    function dist(b) { return Math.abs(b[0] + b[2] / 2 - cx) + Math.abs(b[1] + b[3] / 2 - cy); }
    var ctx = { items: [], truncated: near.length > LIMIT.items };
    // cut clips s to n characters, and marks the context truncated when it does
    function cut(s, n) { if (s.length > n) ctx.truncated = true; return clip(s, n); }
    near.slice(0, LIMIT.items).forEach(function (c) {
      var text = collapse(c.el.innerText === undefined ? stableText(c.el) : c.el.innerText);
      if (!text) { var img = c.el.querySelector('img[alt]'); if (img) text = '[image: ' + collapse(img.alt) + ']'; }
      // an enclosed element keeps its words whole; a neighbour is only context
      var it = { anchor: c.anchor, tag: c.el.tagName.toLowerCase(), box: c.box, text: cut(text, targets.indexOf(c) >= 0 ? LIMIT.targetText : LIMIT.text) };
      var labels = labelsIn(c.el, zone);
      if (labels.length > LIMIT.labels) { ctx.truncated = true; labels = labels.slice(0, LIMIT.labels); }
      labels.forEach(function (l) { l.text = cut(l.text, LIMIT.label); });
      if (labels.length) it.labels = labels;
      ctx.items.push(it);
    });
    while (utf8Bytes(ctx) > LIMIT.bytes && ctx.items.length > 1) { ctx.items.pop(); ctx.truncated = true; }
    return ctx;
  }
  // labelsIn: the text a diagram draws inside el — Mermaid node, edge and
  // cluster labels, or any SVG <text> — each with its own box.
  function labelsIn(el, zone) {
    var out = [];
    el.querySelectorAll('svg g.node, svg g.edgeLabel, svg g.cluster, svg text').forEach(function (n) {
      if (n.tagName.toLowerCase() === 'text' && n.closest('g.node, g.edgeLabel, g.cluster')) return;
      var t = collapse(n.textContent), b = t && shownBox(n);
      if (b && overlaps(b, zone)) out.push({ text: t, box: b });
    });
    return out;
  }

  // --- placement: derived on every render, never stored ---
  // place answers where a captured drawing goes on this render. Each target
  // must be found by its exact anchor (never by similar text), visible, and
  // laid out as captured; then the drawing moves rigidly with them.
  //   exact | moved    paint the drawing (moved: offset by the targets' shift)
  //   reflowed         targets found, geometry changed: outline them only
  //   hidden           a target exists but is collapsed, on another tab, or clipped
  //   missing          a target's content is gone
  //   ambiguous        a target's content now occurs a different number of times
  function place(rec, index) {
    index = index || byAnchor(markables());
    var found = [], hidden = null;
    for (var i = 0; i < rec.targets.length; i++) {
      var t = rec.targets[i], same = index[t.anchor] || [];
      if (!same.length) return { outcome: 'missing', anchor: t.anchor };
      if (same.length !== (t.of || 1)) return { outcome: 'ambiguous', anchor: t.anchor };
      var el = same[t.nth || 0].el, box = shownBox(el);
      if (!box) { hidden = hidden || { outcome: 'hidden', anchor: t.anchor }; continue; }
      found.push({ t: t, box: box });
    }
    if (hidden) return hidden;
    var dx = r1(found[0].box[0] - found[0].t.box[0]), dy = r1(found[0].box[1] - found[0].t.box[1]);
    var rigid = found.every(function (f) {
      var b = f.box, c = f.t.box;
      return Math.abs(b[2] - c[2]) <= TOL && Math.abs(b[3] - c[3]) <= TOL && Math.abs(b[0] - c[0] - dx) <= TOL && Math.abs(b[1] - c[1] - dy) <= TOL;
    });
    if (!rigid) return { outcome: 'reflowed', boxes: found.map(function (f) { return f.box; }) };
    return Math.abs(dx) <= TOL && Math.abs(dy) <= TOL ? { outcome: 'exact', offset: [0, 0] } : { outcome: 'moved', offset: [dx, dy] };
  }
  // reveal restores what capture recorded as open or checked, and opens any
  // <details> closed around a target, so a hidden drawing can be placed.
  function reveal(rec) {
    var l = rec.layout || {}, open = l.open || [], checked = l.checked || [];
    document.querySelectorAll('details').forEach(function (d) { if (!d.open && open.indexOf(detailsKey(d)) >= 0) d.open = true; });
    document.querySelectorAll('input[type=radio], input[type=checkbox]').forEach(function (i) {
      if (!i.checked && checked.indexOf(inputKey(i)) >= 0) { i.checked = true; i.dispatchEvent(new Event('change', { bubbles: true })); }
    });
    var index = byAnchor(markables());
    rec.targets.forEach(function (t) {
      var same = index[t.anchor] || [], c = same[t.nth || 0];
      for (var a = c && c.el.parentElement; a; a = a.parentElement) if (a.tagName === 'DETAILS' && !a.open) a.open = true;
    });
    return place(rec);
  }

  // --- painting ---
  var layer = svg('svg', { 'class': 'oxd', 'aria-hidden': 'true' });
  layer.style.cssText = 'position:absolute;left:0;top:0;overflow:visible;pointer-events:none;z-index:2147483000';
  var ink = svg('g', {}); // translated so layer coordinates are document coordinates
  layer.appendChild(ink);
  document.documentElement.appendChild(layer);
  function svg(tag, attrs) { var n = document.createElementNS(SVGNS, tag); for (var k in attrs) n.setAttribute(k, attrs[k]); return n; }
  // the design register's gold: #d9b654 on dark pages, #836726 on light ones
  function inkColor() { return document.documentElement.getAttribute('data-theme') === 'light' ? '#836726' : '#d9b654'; }
  // fitLayer sizes the layer to the page. It zeroes the layer first: an
  // absolutely positioned child of <html> counts toward the root's scroll size,
  // so measuring with the old size in place would never let the page shrink.
  function fitLayer() {
    var root = document.documentElement;
    layer.setAttribute('width', 0);
    layer.setAttribute('height', 0);
    var o = layer.getBoundingClientRect();
    layer.setAttribute('width', root.scrollWidth);
    layer.setAttribute('height', root.scrollHeight);
    ink.setAttribute('transform', 'translate(' + r1(-(o.left + scrollX)) + ' ' + r1(-(o.top + scrollY)) + ')');
  }
  function paintAll() {
    fitLayer();
    while (ink.firstChild) ink.removeChild(ink.firstChild);
    outcomes = {};
    var index = byAnchor(markables());
    records.forEach(function (rec) {
      var o = outcomes[rec.id] = place(rec, index);
      if (o.outcome === 'exact' || o.outcome === 'moved') {
        inkInto(ink, svg, inkLine(rec), o.offset, inkColor(), 0.92).setAttribute('data-id', rec.id);
      } else if (o.outcome === 'reflowed') {
        // a thin dashed ellipse around the targets as they now are, never the drawing
        var u = o.boxes.reduce(function (m, b) { return [Math.min(m[0], b[0]), Math.min(m[1], b[1]), Math.max(m[2], b[0] + b[2]), Math.max(m[3], b[1] + b[3])]; }, [Infinity, Infinity, -Infinity, -Infinity]);
        ink.appendChild(svg('ellipse', { cx: (u[0] + u[2]) / 2, cy: (u[1] + u[3]) / 2, rx: (u[2] - u[0]) / Math.SQRT2 + 6, ry: (u[3] - u[1]) / Math.SQRT2 + 6, fill: 'none', stroke: inkColor(), 'stroke-width': 1.2, 'stroke-dasharray': '5 5', 'data-id': rec.id }));
      }
    });
    if (stroke) ink.appendChild(stroke.g);
    if (embedded) send({ type: 'placed', outcomes: outcomes });
  }
  var paintTimer = null;
  function schedulePaint() { clearTimeout(paintTimer); paintTimer = setTimeout(paintAll, 100); }

  // --- the gesture ---
  var drawing = false, stroke = null, lastClient = null;
  function setMode(on, fromHost) {
    if (embedded && !fromHost && on !== drawing) send({ type: 'mode', draw: on });
    drawing = on;
    layer.style.pointerEvents = on ? 'auto' : 'none';
    layer.style.cursor = on ? 'crosshair' : '';
    if (!on) abort('cancelled');
  }
  function addPoint(cx, cy) {
    lastClient = [cx, cy];
    stroke.pts.push([r1(cx + scrollX), r1(cy + scrollY)]);
    var g = inkInto(ink, svg, stroke.pts, [0, 0], inkColor(), 0.45);
    if (stroke.g) stroke.g.remove();
    stroke.g = g;
  }
  function abort(reason) {
    if (!stroke) return;
    stroke.g.remove();
    stroke = null;
    deliver({ refused: reason });
  }
  layer.addEventListener('pointerdown', function (e) {
    if (!drawing || e.button !== 0) return;
    e.preventDefault();
    fitLayer();
    stroke = { pts: [], g: null };
    layer.setPointerCapture(e.pointerId);
    addPoint(e.clientX, e.clientY);
  });
  layer.addEventListener('pointermove', function (e) { if (stroke) addPoint(e.clientX, e.clientY); });
  layer.addEventListener('pointerup', function () {
    if (!stroke) return;
    var pts = stroke.pts, bb = bboxOf(pts);
    stroke.g.remove();
    stroke = null;
    deliver(pts.length < 3 || bb[2] < 8 || bb[3] < 8 ? { refused: 'too-small' } : capturePath(pts));
  });
  layer.addEventListener('pointercancel', function () { abort('cancelled'); });
  // Scrolling mid-stroke moves the page under a still pointer: record where
  // the pointer now is in the document, so the stroke follows the content.
  window.addEventListener('scroll', function () { if (stroke && lastClient) addPoint(lastClient[0], lastClient[1]); });
  // Scrolling an element (an overflowing table or panel) moves its content
  // within the page, so placement is derived again: the drawing follows its
  // target, or is hidden once the target scrolls out of view.
  document.addEventListener('scroll', function (e) { if (e.target !== document) schedulePaint(); }, true);
  // A resize mid-stroke reflows the content the stroke was drawn over, so the
  // stroke is abandoned rather than captured against a layout that is gone.
  window.addEventListener('resize', function () { if (stroke) abort('layout-changed'); schedulePaint(); });
  document.addEventListener('toggle', schedulePaint, true);
  document.addEventListener('change', schedulePaint, true);
  if (window.ResizeObserver) new ResizeObserver(schedulePaint).observe(document.documentElement);
  document.addEventListener('keydown', function (e) {
    if (e.defaultPrevented) return;
    if (e.key === 'Escape' && (stroke || drawing)) { e.preventDefault(); if (stroke) abort('cancelled'); else setMode(false); }
  });

  // --- delivery ---
  function send(msg) { msg.ox = 'draw'; msg.v = V; msg.instance = instance; parent.postMessage(msg, '*'); }
  function deliver(result) {
    if (embedded) { send(result.refused ? { type: 'refused', reason: result.refused } : { type: 'capture', capture: result }); return; }
    OxDraw.events.push(result);
    if (result.refused) return;
    // top-level, this page is the trusted local review chrome: it mints the
    // id and stamps the revision its server rendered (#ox-draw-config)
    result.id = newId();
    result.plan_revision = config.plan_revision || '';
    records.push(result);
    paintAll();
  }
  function newId() { return crypto.randomUUID ? crypto.randomUUID() : String(Date.now()) + Math.random().toString(16).slice(2); }
  function readConfig() { try { return JSON.parse(document.getElementById('ox-draw-config').textContent) || {}; } catch (e) { return {}; } }

  // --- host messages (framed only): accepted from the parent window alone ---
  if (embedded) {
    window.addEventListener('message', function (e) {
      var d = e.data;
      if (e.source !== window.parent || !d || d.ox !== 'draw') return;
      if (d.type === 'mode') setMode(!!d.draw, true);
      else if (d.type === 'place') { records = Array.isArray(d.records) ? d.records : []; paintAll(); }
      else if (d.type === 'reveal') { var r = records.filter(function (x) { return x.id === d.id; })[0]; if (r) { reveal(r); paintAll(); } }
    });
  }

  window.OxDraw = {
    records: records, events: [],
    setMode: setMode, capturePath: capturePath, place: place, reveal: reveal, anchorOf: anchorOf,
    outcomes: function () { return outcomes; },
    load: function (recs) { records = recs.slice(); OxDraw.records = records; paintAll(); return outcomes; }
  };

  // probe reports what the document can reach (storage, cookies, parent,
  // network) from which origin, and whether its diagrams rendered: what a host
  // checks when it qualifies its viewer.
  function probe() {
    function attempt(fn) { try { fn(); return 'allowed'; } catch (e) { return 'denied'; } }
    var mermaid = [].slice.call(document.querySelectorAll('pre.mermaid'));
    return {
      origin: self.origin,
      framed: embedded,
      storage: attempt(function () { localStorage.getItem('x'); }),
      cookie: attempt(function () { void document.cookie; }),
      parent_dom: embedded ? attempt(function () { void window.parent.document; }) : 'n/a',
      mermaid: mermaid.length + ' diagram(s), ' + mermaid.filter(function (p) { return p.querySelector('svg'); }).length + ' rendered'
    };
  }
  // ready once diagrams have had their render (Mermaid runs async) and fonts
  // have settled, so the first capture measures the page the reader sees.
  function whenSettled(fn) {
    var t0 = Date.now();
    (function poll() {
      var pending = [].slice.call(document.querySelectorAll('pre.mermaid')).some(function (p) { return !p.querySelector('svg'); });
      if (pending && Date.now() - t0 < 5000) { setTimeout(poll, 50); return; }
      (document.fonts ? document.fonts.ready : Promise.resolve()).then(fn, fn);
    })();
  }
  whenSettled(function () {
    var p = probe();
    var net = function (result) { p.network = result; OxDraw.ready = p; if (embedded) send({ type: 'ready', probe: p }); paintAll(); };
    // network is whether the page's policy lets a request leave. A failed fetch
    // cannot say: from an opaque origin every request is cross-origin and fails
    // CORS whatever connect-src allows. A no-cors request the policy blocks
    // raises securitypolicyviolation, enforced and naming the request (as its
    // URL, or only its origin); one it allows does not.
    var target = 'https://network-probe.invalid/', blocked = false;
    document.addEventListener('securitypolicyviolation', function (e) {
      if (e.disposition === 'enforce' && (e.blockedURI === target || e.blockedURI === 'https://network-probe.invalid') && /^(connect|default)-src$/.test(e.effectiveDirective)) blocked = true;
    });
    var settled = function () { setTimeout(function () { net(blocked ? 'denied' : 'allowed'); }, 100); };
    try { fetch(target, { mode: 'no-cors', cache: 'no-store' }).then(settled, settled); } catch (e) { settled(); }
  });
})();
