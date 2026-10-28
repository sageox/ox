// Generated from packages/plan-review by `make plan-review`. Edit the source there, not this file.
"use strict";
(() => {
  // src/anchor.ts
  function fnv1a(s) {
    let h = 2166136261;
    for (let i = 0; i < s.length; i++) {
      h ^= s.charCodeAt(i);
      h = h * 16777619 >>> 0;
    }
    return ("0000000" + h.toString(16)).slice(-8);
  }
  function norm(s) {
    return (s || "").replace(/\s+/g, " ").trim().toLowerCase();
  }
  function clip(t, n) {
    return t.length > n ? t.slice(0, n - 1) + "…" : t;
  }
  function contentAnchor(heading, text2) {
    return "h" + fnv1a(norm(heading) + "\0" + norm(text2));
  }
  function quoteAnchor(section, quote) {
    return "q" + fnv1a(norm(section) + "\0" + norm(quote));
  }
  function labelOf(text2) {
    return clip((text2 || "").replace(/\s+/g, " ").trim(), 70);
  }
  var GENERATED = "section[id], li, tr, .ox-chip, .stat, .bar-row";
  var AUTHORED = "h1,h2,h3,h4,p,li,tr,pre,blockquote,figure,[data-ox-section]";
  function markableSelector(doc) {
    const set = window.OX_REVIEW_SELECTOR;
    if (set) return set;
    const meta = doc.querySelector('meta[name="generator"]');
    if (meta && meta.getAttribute("content") === "ox plan markdown renderer") return GENERATED;
    const brand = doc.querySelector("div.brand"), eyebrow = doc.querySelector("div.eyebrow");
    if (brand && eyebrow && brand.textContent === "OX · PLAN" && eyebrow.textContent === "SageOx · enriched plan") return GENERATED;
    return AUTHORED;
  }
  function headingOf(el) {
    const sec = el.closest("section[id], [data-ox-section]");
    if (!sec) return "";
    const ds = sec.getAttribute("data-ox-section");
    if (ds) return ds;
    const h = sec.querySelector("h2, h3");
    return h ? anchorText(h) : sec.id || "";
  }
  function anchorText(el) {
    const clone = el.cloneNode(true);
    clone.querySelectorAll(".rev-glyph, style, script").forEach((g) => g.remove());
    return clone.textContent || "";
  }
  function anchorFor(el) {
    return contentAnchor(headingOf(el), anchorText(el));
  }
  function labelFor(el) {
    return labelOf(anchorText(el));
  }

  // src/region.ts
  var LIMITS = {
    regionBytes: 65536,
    contextBytes: 16384,
    pathPoints: 512,
    targets: 16,
    contextItems: 48,
    itemLabels: 32,
    targetText: 2e3,
    neighborText: 600,
    labelText: 80,
    layoutKeys: 128
  };
  var DRAWING = /^q-[A-Za-z0-9_-]{8,64}$/;
  var ID = /^[A-Za-z0-9_-]{8,64}$/;
  var ELEMENT = /^h[0-9a-f]{8}$/;
  var REVISION = /^sha256:[0-9a-f]{64}$/;
  var TIME = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?Z$/;
  var obj = (v) => typeof v === "object" && v !== null && !Array.isArray(v);
  var str = (v) => typeof v === "string";
  var num = (v) => typeof v === "number" && isFinite(v);
  var all = (a, ok) => {
    for (let i = 0; i < a.length; i++) if (!ok(a[i])) return false;
    return true;
  };
  var nums = (v, n) => Array.isArray(v) && v.length === n && all(v, num);
  var box = (v) => nums(v, 4) && v[2] >= 0 && v[3] >= 0;
  var strs = (v, n) => Array.isArray(v) && v.length <= n && all(v, str);
  var count = (v) => num(v) && v >= 0 && Math.floor(v) === v;
  var utf8Bytes = (v) => new TextEncoder().encode(JSON.stringify(v)).length;
  function problem(v, stamped) {
    if (!obj(v)) return "not an object";
    if (stamped) {
      if (!str(v.id) || !ID.test(v.id)) return "id";
      if (!str(v.plan_revision) || !REVISION.test(v.plan_revision)) return "plan_revision";
      if (!str(v.created_at) || !TIME.test(v.created_at)) return "created_at";
    }
    if (v.v !== 1 || v.kind !== "region" || v.frame !== "document") return "v, kind or frame";
    const p = v.path;
    if (!Array.isArray(p) || p.length < 3 || p.length > LIMITS.pathPoints || !all(p, (x) => nums(x, 2))) return "path";
    const e = v.ellipse;
    if (!obj(e) || !nums(e.center, 2) || !nums(e.radii, 2) || !(e.radii[0] > 0 && e.radii[1] > 0) || !num(e.rotation) || !num(e.start) || e.dir !== 1 && e.dir !== -1) return "ellipse";
    if (!box(v.bbox)) return "bbox";
    const l = v.layout;
    if (!obj(l) || !nums(l.viewport, 2) || !nums(l.document, 2) || !nums(l.scroll, 2) || !num(l.dpr) || !num(l.scale) || !str(l.theme) || !str(l.fonts) || !strs(l.open, LIMITS.layoutKeys) || !strs(l.checked, LIMITS.layoutKeys)) return "layout";
    const ts = v.targets;
    if (!Array.isArray(ts) || !ts.length || ts.length > LIMITS.targets) return "targets";
    for (const t of ts) {
      if (!obj(t) || !str(t.anchor) || !ELEMENT.test(t.anchor) || !str(t.tag) || !str(t.section) || !box(t.box)) return "targets";
      if (!num(t.cover) || t.cover < 0 || t.cover > 1) return "targets";
      if ("nth" in t !== "of" in t || "nth" in t && !(count(t.nth) && count(t.of) && t.of >= 2 && t.nth < t.of)) return "targets";
    }
    const c = v.context;
    if (!obj(c) || !Array.isArray(c.items) || c.items.length > LIMITS.contextItems || typeof c.truncated !== "boolean") return "context";
    const enclosed = ts.map((t) => t.anchor);
    for (const it of c.items) {
      if (!obj(it) || !str(it.anchor) || !ELEMENT.test(it.anchor) || !str(it.tag) || !box(it.box) || !str(it.text)) return "context";
      if (it.text.length > (enclosed.indexOf(it.anchor) >= 0 ? LIMITS.targetText : LIMITS.neighborText)) return "context";
      if (!("labels" in it)) continue;
      const ls = it.labels;
      if (!Array.isArray(ls) || !ls.length || ls.length > LIMITS.itemLabels) return "context";
      if (!all(ls, (x) => obj(x) && box(x.box) && str(x.text) && x.text.length <= LIMITS.labelText)) return "context";
    }
    if (utf8Bytes(c) > LIMITS.contextBytes) return "context size";
    if (utf8Bytes(v) > LIMITS.regionBytes) return "size";
    return "";
  }
  function checkRegion(v) {
    return problem(v, true);
  }
  function runsOf(c) {
    return Array.isArray(c.runs) ? c.runs.filter((x) => obj(x) && str(x.text) && box(x.box)) : null;
  }
  function drawingLabel(r) {
    const first = r.targets[0];
    const item = r.context.items.filter((it) => it.anchor === first.anchor)[0];
    const text2 = (item && item.text || first.section).replace(/\s+/g, " ").trim();
    const label = "Drawing: " + text2;
    return label.length > 70 ? label.slice(0, 69) + "…" : label;
  }
  var overlaps = (a, b) => a[0] < b[0] + b[2] && b[0] < a[0] + a[2] && a[1] < b[1] + b[3] && b[1] < a[1] + a[3];
  var r1 = (n) => Math.round(n * 10) / 10;
  function bboxOf(pts) {
    let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
    pts.forEach((p) => {
      x0 = Math.min(x0, p[0]);
      y0 = Math.min(y0, p[1]);
      x1 = Math.max(x1, p[0]);
      y1 = Math.max(y1, p[1]);
    });
    return [r1(x0), r1(y0), r1(x1 - x0), r1(y1 - y0)];
  }
  function thin(pts) {
    const out = [];
    pts.forEach((p) => {
      const q = out[out.length - 1];
      if (!q || Math.abs(p[0] - q[0]) + Math.abs(p[1] - q[1]) >= 2) out.push(p);
    });
    if (out.length <= LIMITS.pathPoints) return out;
    const step = out.length / LIMITS.pathPoints, s = [];
    for (let i = 0; i < LIMITS.pathPoints; i++) s.push(out[Math.floor(i * step)]);
    return s;
  }
  var NIB = { width: 12, angle: -12 * Math.PI / 180 };
  function ellipsePoints(e, extra) {
    const rx = e.radii[0], ry = e.radii[1], rot = e.rotation * Math.PI / 180, t0 = e.start * Math.PI / 180;
    const around = Math.PI * (3 * (rx + ry) - Math.sqrt((3 * rx + ry) * (rx + 3 * ry)));
    const sweep = 2 * Math.PI + extra;
    const n = Math.max(48, Math.min(512, Math.ceil(around * sweep / (2 * Math.PI) / 3)));
    const cos = Math.cos(rot), sin = Math.sin(rot), out = [];
    for (let i = 0; i <= n; i++) {
      const t = t0 + e.dir * sweep * i / n, x = rx * Math.cos(t), y = ry * Math.sin(t);
      out.push([e.center[0] + x * cos - y * sin, e.center[1] + x * sin + y * cos]);
    }
    return out;
  }
  function smooth(pts) {
    for (let k = 0; k < 2 && pts.length > 2; k++) {
      const out = [pts[0]];
      for (let i = 0; i < pts.length - 1; i++) {
        const a = pts[i], b = pts[i + 1];
        out.push([0.75 * a[0] + 0.25 * b[0], 0.75 * a[1] + 0.25 * b[1]], [0.25 * a[0] + 0.75 * b[0], 0.25 * a[1] + 0.75 * b[1]]);
      }
      out.push(pts[pts.length - 1]);
      pts = out;
    }
    return pts;
  }
  function inkPaths(pts, off) {
    pts = smooth(pts.map((p) => [p[0] + off[0], p[1] + off[1]]));
    if (pts.length < 2) return { sweep: "", hair: "" };
    const at = [0];
    for (let i = 1; i < pts.length; i++) at.push(at[i - 1] + Math.hypot(pts[i][0] - pts[i - 1][0], pts[i][1] - pts[i - 1][1]));
    const total = at[at.length - 1];
    const width = (d) => Math.max(0.1, Math.min(1, d / 24, (total - d) / 56));
    const nx = Math.cos(NIB.angle) * NIB.width / 2, ny = Math.sin(NIB.angle) * NIB.width / 2;
    const xy = (p, s) => r1(p[0] + nx * s) + " " + r1(p[1] + ny * s);
    let sweep = "", hair = "M" + xy(pts[0], 0);
    for (let j = 0; j < pts.length - 1; j++) {
      const a = pts[j], b = pts[j + 1], wa = width(at[j]), wb = width(at[j + 1]);
      sweep += "M" + xy(a, wa) + "L" + xy(b, wb) + "L" + xy(b, -wb) + "L" + xy(a, -wa) + "Z";
      hair += "L" + xy(b, 0);
    }
    return { sweep, hair };
  }
  function inkLine(e) {
    return ellipsePoints(e, 0.3);
  }

  // src/draw.ts
  var SVGNS = "http://www.w3.org/2000/svg";
  var TOL = 1;
  var collapse = (s) => (s || "").replace(/\s+/g, " ").trim();
  function markables(selector, exclude) {
    const out = [];
    document.querySelectorAll(selector).forEach((el) => {
      if (!el.closest(exclude)) out.push({ el, anchor: anchorFor(el) });
    });
    return out;
  }
  var rectOf = (r) => [r1(r.left + scrollX), r1(r.top + scrollY), r1(r.width), r1(r.height)];
  function shownBox(el) {
    const e = el;
    if (e.checkVisibility ? !e.checkVisibility({ visibilityProperty: true }) : !el.getClientRects().length || getComputedStyle(el).visibility === "hidden" || inClosedDetails(el)) return null;
    const r = el.getBoundingClientRect();
    if (r.width < 1 || r.height < 1) return null;
    for (let a = el.parentElement; a && a !== document.body && a !== document.documentElement; a = a.parentElement) {
      const cs = getComputedStyle(a);
      if (cs.overflowX === "visible" && cs.overflowY === "visible") continue;
      const ar = a.getBoundingClientRect();
      if (r.right <= ar.left || r.left >= ar.right || r.bottom <= ar.top || r.top >= ar.bottom) return null;
    }
    return rectOf(r);
  }
  function inClosedDetails(el) {
    for (let d = el.parentElement && el.parentElement.closest("details:not([open])"); d; d = d.parentElement && d.parentElement.closest("details:not([open])")) {
      const sum = d.querySelector(":scope > summary");
      if (!sum || !sum.contains(el)) return true;
    }
    return false;
  }
  function drawLayer(selector, exclude, capture, refuse, changed) {
    const layer = svg("svg", { class: "rev-ink", "aria-hidden": "true", tabindex: -1 });
    const ink = svg("g", {});
    layer.appendChild(ink);
    document.documentElement.appendChild(layer);
    let tool = false;
    let gesture = null;
    let selected = null;
    const editable = /* @__PURE__ */ new Map();
    let lastClient = null;
    function svg(tag, attrs) {
      const n = document.createElementNS(SVGNS, tag);
      for (const k in attrs) n.setAttribute(k, String(attrs[k]));
      return n;
    }
    function inkInto(pts, off, tint, cls) {
      const p = inkPaths(pts, off);
      const g = svg("g", { class: "rev-ink-" + tint + " " + cls });
      g.appendChild(svg("path", { d: p.sweep, fill: "currentColor", stroke: "none" }));
      g.appendChild(svg("path", { d: p.hair, fill: "none", stroke: "currentColor", "stroke-width": 1, "stroke-linecap": "round", "stroke-linejoin": "round" }));
      ink.appendChild(g);
      return g;
    }
    function oval(e, tint, anchor) {
      const g = inkInto(inkLine(e), [0, 0], tint, anchor ? "rev-ink-unsent" : "rev-ink-live");
      if (!anchor) return g;
      g.setAttribute("data-anchor", anchor);
      const [cx, cy] = e.center, [rx, ry] = e.radii;
      const controls = svg("g", { transform: `translate(${cx} ${cy}) rotate(${e.rotation})` });
      controls.appendChild(svg("ellipse", { class: "rev-ink-hit", "data-edit": anchor, cx: 0, cy: 0, rx, ry }));
      if (selected === anchor) {
        controls.appendChild(svg("rect", { class: "rev-ink-bounds", x: -rx, y: -ry, width: rx * 2, height: ry * 2 }));
        for (const [hx, hy, cursor] of [[-1, -1, "nwse"], [0, -1, "ns"], [1, -1, "nesw"], [1, 0, "ew"], [1, 1, "nwse"], [0, 1, "ns"], [-1, 1, "nesw"], [-1, 0, "ew"]]) {
          controls.appendChild(svg("rect", { class: "rev-ink-handle", "data-edit": anchor, "data-hx": hx, "data-hy": hy, x: hx * rx - 5, y: hy * ry - 5, width: 10, height: 10, style: `cursor:${cursor}-resize` }));
        }
      }
      g.appendChild(controls);
      return g;
    }
    function fitLayer() {
      const root = document.documentElement;
      layer.setAttribute("width", "0");
      layer.setAttribute("height", "0");
      const o = layer.getBoundingClientRect();
      layer.setAttribute("width", String(root.scrollWidth));
      layer.setAttribute("height", String(root.scrollHeight));
      ink.setAttribute("transform", "translate(" + r1(-(o.left + scrollX)) + " " + r1(-(o.top + scrollY)) + ")");
    }
    function byAnchor(all2) {
      const m = /* @__PURE__ */ Object.create(null);
      all2.forEach((c) => {
        (m[c.anchor] = m[c.anchor] || []).push(c);
      });
      return m;
    }
    function inside(pt, poly) {
      let w = 0;
      for (let i = 0, j = poly.length - 1; i < poly.length; j = i++) {
        const a = poly[j], b = poly[i], cross = (b[0] - a[0]) * (pt[1] - a[1]) - (pt[0] - a[0]) * (b[1] - a[1]);
        if (a[1] <= pt[1]) {
          if (b[1] > pt[1] && cross > 0) w++;
        } else if (b[1] <= pt[1] && cross < 0) w--;
      }
      return w !== 0;
    }
    function coverage(b, poly, bb) {
      if (!overlaps(b, bb)) return 0;
      let n = 0;
      for (let i = 0; i < 5; i++) for (let j = 0; j < 5; j++) if (inside([b[0] + b[2] * (i + 0.5) / 5, b[1] + b[3] * (j + 0.5) / 5], poly)) n++;
      return n / 25;
    }
    const leafMost = (l) => l.filter((c) => !l.some((d) => d !== c && c.el.contains(d.el)));
    const outerMost = (l) => l.filter((c) => !l.some((d) => d !== c && d.el.contains(c.el)));
    function captureEllipse(ellipse) {
      const poly = ellipsePoints(ellipse, 0), bb = bboxOf(poly);
      const all2 = markables(selector, exclude), index2 = byAnchor(all2);
      const shown = [];
      all2.forEach((c) => {
        const b = shownBox(c.el);
        if (b) shown.push({ ...c, box: b, cover: coverage(b, poly, bb) });
      });
      const covered = shown.filter((c) => c.cover >= 0.5);
      let targets = leafMost(covered);
      if (targets.length > LIMITS.targets) targets = outerMost(covered);
      if (!targets.length) {
        const cx = bb[0] + bb[2] / 2, cy = bb[1] + bb[3] / 2;
        let best = null;
        shown.forEach((c) => {
          const b = c.box;
          if (cx >= b[0] && cx <= b[0] + b[2] && cy >= b[1] && cy <= b[1] + b[3] && (!best || b[2] * b[3] < best.box[2] * best.box[3])) best = c;
        });
        if (best) targets = [best];
      }
      if (!targets.length) return "no-target";
      if (targets.length > LIMITS.targets) return "too-large";
      const cap = {
        v: 1,
        kind: "region",
        frame: "document",
        path: thin(poly.map((p) => [r1(p[0]), r1(p[1])])),
        ellipse,
        bbox: bb,
        layout: layoutContext(),
        targets: targets.map((c) => {
          const same = index2[c.anchor];
          const t = { anchor: c.anchor, tag: c.el.tagName.toLowerCase(), section: collapse(headingOf(c.el)), box: c.box, cover: c.cover };
          if (same.length > 1) {
            t.nth = same.findIndex((s) => s.el === c.el);
            t.of = same.length;
          }
          return t;
        }),
        context: contextOf(shown, bb, targets)
      };
      return utf8Bytes(cap) > LIMITS.regionBytes - 256 ? "too-large" : cap;
    }
    function contextOf(shown, bb, targets) {
      const m = 16, zone = [bb[0] - m, bb[1] - m, bb[2] + 2 * m, bb[3] + 2 * m];
      const cx = bb[0] + bb[2] / 2, cy = bb[1] + bb[3] / 2;
      const dist = (b) => Math.abs(b[0] + b[2] / 2 - cx) + Math.abs(b[1] + b[3] / 2 - cy);
      const near = leafMost(shown.filter((c) => overlaps(c.box, zone))).sort((a, b) => dist(a.box) - dist(b.box));
      const kept = [];
      const ctx = { items: [], truncated: near.length > LIMITS.contextItems, runs: kept };
      const cut = (s, n) => {
        if (s.length <= n) return s;
        ctx.truncated = true;
        return s.slice(0, n - 1) + "…";
      };
      const band = [...zone];
      near.forEach((c) => {
        const x1 = Math.max(band[0] + band[2], c.box[0] + c.box[2]);
        band[0] = Math.min(band[0], c.box[0]);
        band[2] = x1 - band[0];
      });
      let runs = [];
      const root = document.documentElement;
      root.classList.add("rev-reading");
      try {
        runs = runsIn(band).sort((a, b) => dist(a.box) - dist(b.box));
        near.slice(0, LIMITS.contextItems).forEach((c) => {
          const h = c.el;
          let text2 = collapse(h.innerText === void 0 ? h.textContent : h.innerText);
          if (!text2) {
            const img = c.el.querySelector("img[alt]");
            if (img) text2 = "[image: " + collapse(img.getAttribute("alt")) + "]";
          }
          const it = { anchor: c.anchor, tag: c.el.tagName.toLowerCase(), box: c.box, text: cut(text2, targets.indexOf(c) >= 0 ? LIMITS.targetText : LIMITS.neighborText) };
          let labels = labelsIn(c.el, zone);
          if (labels.length > LIMITS.itemLabels) {
            ctx.truncated = true;
            labels = labels.slice(0, LIMITS.itemLabels);
          }
          labels.forEach((l) => {
            l.text = cut(l.text, LIMITS.labelText);
          });
          if (labels.length) it.labels = labels;
          ctx.items.push(it);
        });
      } finally {
        root.classList.remove("rev-reading");
      }
      while (utf8Bytes(ctx) > LIMITS.contextBytes && ctx.items.length > 1) {
        ctx.items.pop();
        ctx.truncated = true;
      }
      let room = LIMITS.contextBytes - utf8Bytes(ctx);
      for (const r of runs) {
        const n = utf8Bytes(r) + 1;
        if (n > room) ctx.truncated = true;
        else {
          kept.push(r);
          room -= n;
        }
      }
      return ctx;
    }
    function runsIn(band) {
      const out = [];
      const range = document.createRange();
      const walk = document.createTreeWalker(document.body, NodeFilter.SHOW_ELEMENT | NodeFilter.SHOW_TEXT, {
        acceptNode(n) {
          if (n.nodeType === Node.TEXT_NODE) return NodeFilter.FILTER_ACCEPT;
          const el = n;
          if (el.matches(exclude + ", script, style, noscript, template, textarea, select")) return NodeFilter.FILTER_REJECT;
          const r = el.getBoundingClientRect();
          if ((r.width || r.height) && !overlaps(rectOf(r), band)) return NodeFilter.FILTER_REJECT;
          if (el.checkVisibility && !el.checkVisibility({ visibilityProperty: true, opacityProperty: true })) return NodeFilter.FILTER_REJECT;
          return NodeFilter.FILTER_SKIP;
        }
      });
      for (let t = walk.nextNode(); t; t = walk.nextNode()) {
        let run = null;
        const keep = () => {
          if (run && overlaps(run.box, band)) out.push(run);
        };
        const words2 = /\S+/g;
        for (let w = words2.exec(t.data); w; w = words2.exec(t.data)) {
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
            run.text += " " + w[0];
          } else {
            keep();
            run = { text: w[0], box: b };
          }
        }
        keep();
      }
      return out;
    }
    function labelsIn(el, zone) {
      const out = [];
      el.querySelectorAll("svg g.node, svg g.edgeLabel, svg g.cluster, svg text").forEach((n) => {
        if (n.tagName.toLowerCase() === "text" && n.closest("g.node, g.edgeLabel, g.cluster")) return;
        const t = collapse(n.textContent), b = t ? shownBox(n) : null;
        if (b && overlaps(b, zone)) out.push({ text: t, box: b });
      });
      return out;
    }
    const detailsKey = (d) => {
      const s = d.querySelector("summary");
      return "d" + fnv1a(norm(s ? s.textContent : ""));
    };
    const inputKey = (i) => i.id ? "#" + i.id : i.name + "=" + i.value;
    function layoutContext() {
      fitLayer();
      const open = [], checked = [];
      document.querySelectorAll("details[open]").forEach((d) => open.push(detailsKey(d)));
      document.querySelectorAll("input[type=radio]:checked, input[type=checkbox]:checked").forEach((i) => checked.push(inputKey(i)));
      const root = document.documentElement, vv = window.visualViewport;
      return {
        viewport: [innerWidth, innerHeight],
        document: [root.scrollWidth, root.scrollHeight],
        dpr: devicePixelRatio,
        scale: vv ? r1(vv.scale) : 1,
        scroll: [r1(scrollX), r1(scrollY)],
        theme: root.getAttribute("data-theme") || (matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark"),
        fonts: document.fonts ? document.fonts.status : "",
        open: open.slice(0, LIMITS.layoutKeys),
        checked: checked.slice(0, LIMITS.layoutKeys)
      };
    }
    function place(d, index2) {
      const found = [];
      let hidden = null, first;
      for (const t of d.targets) {
        const same = index2[t.anchor] || [];
        if (!same.length) return { outcome: "missing" };
        if (same.length !== (t.of || 1)) return { outcome: "ambiguous" };
        const el = same[t.nth || 0].el, b = shownBox(el);
        first = first || el;
        if (!b) {
          hidden = hidden || { outcome: "hidden" };
          continue;
        }
        found.push({ box: b, was: t.box });
      }
      if (hidden) return { ...hidden, el: first };
      const dx = r1(found[0].box[0] - found[0].was[0]), dy = r1(found[0].box[1] - found[0].was[1]);
      const rigid = found.every((f) => Math.abs(f.box[2] - f.was[2]) <= TOL && Math.abs(f.box[3] - f.was[3]) <= TOL && Math.abs(f.box[0] - f.was[0] - dx) <= TOL && Math.abs(f.box[1] - f.was[1] - dy) <= TOL);
      if (!rigid) return { outcome: "reflowed", boxes: found.map((f) => f.box), el: first };
      return { outcome: Math.abs(dx) <= TOL && Math.abs(dy) <= TOL ? "exact" : "moved", offset: [dx, dy], el: first };
    }
    let drawings = [];
    let index = /* @__PURE__ */ Object.create(null);
    let last = "";
    function placeAll() {
      fitLayer();
      while (ink.firstChild) ink.removeChild(ink.firstChild);
      editable.clear();
      const out = drawings.map((d) => {
        const o = place(d, index);
        const cls = d.kind === "unsent" ? "rev-ink-unsent" : "";
        if (o.offset) {
          if (tool && d.kind === "unsent") {
            const e = { ...d.ellipse, center: [r1(d.ellipse.center[0] + o.offset[0]), r1(d.ellipse.center[1] + o.offset[1])] };
            editable.set(d.anchor, e);
            if (gesture?.anchor !== d.anchor) oval(e, d.tint, d.anchor);
          } else inkInto(inkLine(d.ellipse), o.offset, d.tint, cls).setAttribute("data-anchor", d.anchor);
        } else if (o.boxes) {
          const u = o.boxes.reduce((m, b) => [Math.min(m[0], b[0]), Math.min(m[1], b[1]), Math.max(m[2], b[0] + b[2]), Math.max(m[3], b[1] + b[3])], [Infinity, Infinity, -Infinity, -Infinity]);
          ink.appendChild(svg("ellipse", {
            class: "rev-ink-" + d.tint + " rev-ink-reflowed",
            "data-anchor": d.anchor,
            cx: (u[0] + u[2]) / 2,
            cy: (u[1] + u[3]) / 2,
            rx: (u[2] - u[0]) / Math.SQRT2 + 6,
            ry: (u[3] - u[1]) / Math.SQRT2 + 6
          }));
        }
        return { anchor: d.anchor, outcome: o.outcome, el: o.el || null };
      });
      if (gesture) ink.appendChild(gesture.preview);
      last = out.map((p) => p.outcome).join();
      return out;
    }
    let timer;
    function relayout() {
      clearTimeout(timer);
      timer = setTimeout(() => {
        const was = last;
        placeAll();
        if (last !== was) changed();
      }, 100);
    }
    function drag(cx, cy) {
      if (!gesture) return;
      lastClient = [cx, cy];
      const p = [r1(cx + scrollX), r1(cy + scrollY)], s = gesture.start, e = gesture.original;
      const dx = p[0] - s[0], dy = p[1] - s[1];
      if (Math.abs(dx) + Math.abs(dy) >= 2) gesture.moved = true;
      if (!gesture.anchor) {
        gesture.ellipse = { center: [r1((s[0] + p[0]) / 2), r1((s[1] + p[1]) / 2)], radii: [r1(Math.abs(dx) / 2), r1(Math.abs(dy) / 2)], rotation: 0, start: 0, dir: 1 };
      } else if (!gesture.handle) {
        gesture.ellipse = { ...e, center: [r1(e.center[0] + dx), r1(e.center[1] + dy)] };
      } else {
        const a = e.rotation * Math.PI / 180, cos = Math.cos(a), sin = Math.sin(a);
        const delta = [dx * cos + dy * sin, -dx * sin + dy * cos];
        const radii = [...e.radii], shift = [0, 0];
        gesture.handle.forEach((h, i) => {
          if (!h) return;
          radii[i] = r1(Math.max(4, e.radii[i] + h * delta[i] / 2));
          shift[i] = h * (radii[i] - e.radii[i]);
        });
        gesture.ellipse = { ...e, radii, center: [r1(e.center[0] + shift[0] * cos - shift[1] * sin), r1(e.center[1] + shift[0] * sin + shift[1] * cos)] };
      }
      gesture.preview.remove();
      gesture.preview = oval(gesture.ellipse, "gold", gesture.anchor);
    }
    function endGesture(reason) {
      if (!gesture) return;
      const pointer = gesture.pointer;
      gesture = null;
      layer.classList.remove("rev-dragging");
      if (layer.hasPointerCapture(pointer)) layer.releasePointerCapture(pointer);
      placeAll();
      if (reason) refuse(reason);
    }
    layer.addEventListener("pointerdown", (e) => {
      if (!tool || e.button !== 0 || gesture) return;
      e.preventDefault();
      layer.focus({ preventScroll: true });
      fitLayer();
      const hit = e.target.closest("[data-edit]"), anchor = hit?.getAttribute("data-edit") || void 0;
      const original = anchor ? editable.get(anchor) : void 0;
      if (anchor && !original) return;
      selected = anchor || null;
      const start = [r1(e.clientX + scrollX), r1(e.clientY + scrollY)];
      const ellipse = original || { center: start, radii: [0, 0], rotation: 0, start: 0, dir: 1 };
      const handle = hit?.hasAttribute("data-hx") ? [Number(hit.getAttribute("data-hx")), Number(hit.getAttribute("data-hy"))] : void 0;
      gesture = { pointer: e.pointerId, start, original: ellipse, ellipse, anchor, handle, preview: oval(ellipse, "gold", anchor), moved: false };
      lastClient = [e.clientX, e.clientY];
      layer.classList.toggle("rev-dragging", !!anchor && !handle);
      placeAll();
      layer.setPointerCapture(e.pointerId);
    });
    layer.addEventListener("pointermove", (e) => {
      if (gesture && e.pointerId === gesture.pointer) drag(e.clientX, e.clientY);
    });
    layer.addEventListener("pointerup", (e) => {
      if (!gesture || e.pointerId !== gesture.pointer) return;
      drag(e.clientX, e.clientY);
      const g = gesture;
      endGesture();
      if (g.anchor && !g.moved) return;
      if (g.ellipse.radii.some((r) => r < 4)) {
        refuse("too-small");
        return;
      }
      const c = captureEllipse(g.ellipse);
      if (typeof c === "string") {
        refuse(c);
        return;
      }
      capture(c, { left: c.bbox[0] - scrollX, bottom: c.bbox[1] + c.bbox[3] - scrollY }, g.anchor);
    });
    layer.addEventListener("pointercancel", (e) => {
      if (gesture && e.pointerId === gesture.pointer) endGesture("cancelled");
    });
    layer.addEventListener("lostpointercapture", (e) => {
      if (gesture && e.pointerId === gesture.pointer && !layer.hasPointerCapture(e.pointerId)) endGesture("cancelled");
    });
    window.addEventListener("scroll", () => {
      if (gesture && lastClient) drag(lastClient[0], lastClient[1]);
    });
    document.addEventListener("scroll", (e) => {
      if (e.target !== document) relayout();
    }, true);
    window.addEventListener("resize", () => {
      endGesture("layout-changed");
      relayout();
    });
    document.addEventListener("toggle", relayout, true);
    document.addEventListener("change", relayout, true);
    if (window.ResizeObserver) new ResizeObserver(relayout).observe(document.documentElement);
    window.addEventListener("keydown", (e) => {
      if (e.key !== "Escape" || !gesture || e.defaultPrevented) return;
      e.preventDefault();
      endGesture("cancelled");
    }, true);
    return {
      setTool(on) {
        if (tool === on) return;
        tool = on;
        layer.classList.toggle("rev-drawing", on);
        if (!on) endGesture("cancelled");
        placeAll();
      },
      paint(next, all2) {
        for (const d of next) if (d.kind === "unsent" && !drawings.some((old) => old.anchor === d.anchor)) selected = d.anchor;
        drawings = next;
        index = byAnchor(all2);
        return placeAll();
      },
      show(d) {
        document.querySelectorAll("details").forEach((x) => {
          const el = x;
          if (!el.open && d.open.indexOf(detailsKey(el)) >= 0) el.open = true;
        });
        document.querySelectorAll("input[type=radio], input[type=checkbox]").forEach((x) => {
          const i = x;
          if (!i.checked && d.checked.indexOf(inputKey(i)) >= 0) {
            i.checked = true;
            i.dispatchEvent(new Event("change", { bubbles: true }));
          }
        });
        d.targets.forEach((t) => {
          const c = (index[t.anchor] || [])[t.nth || 0];
          for (let a = c && c.el.parentElement; a; a = a.parentElement) if (a.tagName === "DETAILS") a.open = true;
        });
      }
    };
  }

  // src/protocol.ts
  var MAX_TEXT = 65536;
  var MAX_ITEMS = 1e4;
  var ANCHOR = /^[hq][0-9a-f]{8}$/;
  var STATUSES = ["approve", "request-change", "flag", "comment"];
  var STATES = ["open", "addressed", "verified", "wontfix"];
  var TINTS = ["sage", "amber", "red", "gold", "faint"];
  var MAX_POINTERS = 64;
  var MAX_PATH = 16;
  var MAX_TEXT_OFFSET = 1 << 20;
  function obj2(v) {
    return typeof v === "object" && v !== null && !Array.isArray(v);
  }
  function text(v) {
    return typeof v === "string" && v.length <= MAX_TEXT;
  }
  function optText(v) {
    return v === void 0 || text(v);
  }
  function num2(v) {
    return typeof v === "number" && isFinite(v);
  }
  function nums2(v, n) {
    return Array.isArray(v) && v.length === n && all(v, num2);
  }
  function oneOf(v, list) {
    return typeof v === "string" && list.indexOf(v) >= 0;
  }
  var whole = (v, max) => num2(v) && v >= 0 && v <= max && Math.floor(v) === v;
  var unit = (v) => num2(v) && v >= 0 && v <= 1;
  function pointerAt(v) {
    if (!obj2(v) || typeof v.anchor !== "string" || !ANCHOR.test(v.anchor) || v.anchor.charAt(0) !== "h" || !unit(v.x) || !unit(v.y)) return null;
    const at = { anchor: v.anchor, x: v.x, y: v.y };
    if (v.nth !== void 0 || v.of !== void 0) {
      if (!whole(v.nth, MAX_ITEMS) || !whole(v.of, MAX_ITEMS) || v.of < 2 || v.nth >= v.of) return null;
      at.nth = v.nth;
      at.of = v.of;
    }
    if (v.path !== void 0) {
      if (!Array.isArray(v.path) || !v.path.length || v.path.length > MAX_PATH || !all(v.path, (i) => whole(i, MAX_ITEMS))) return null;
      at.path = v.path.slice();
    }
    if (v.text !== void 0) {
      if (!whole(v.text, MAX_TEXT_OFFSET)) return null;
      at.text = v.text;
    }
    return at;
  }
  function paintItem(it) {
    if (!obj2(it) || typeof it.anchor !== "string" || !ANCHOR.test(it.anchor)) return null;
    if (it.kind !== "unsent" && it.kind !== "committed" || !oneOf(it.status, STATUSES) || !oneOf(it.tint, TINTS)) return null;
    if (it.state !== void 0 && !oneOf(it.state, STATES) || !text(it.glyph)) return null;
    if (!optText(it.quote) || !optText(it.section) || !optText(it.label)) return null;
    const p = { anchor: it.anchor, kind: it.kind, status: it.status, glyph: it.glyph, tint: it.tint };
    if (it.state !== void 0) p.state = it.state;
    if (it.quote !== void 0) p.quote = it.quote;
    if (it.section !== void 0) p.section = it.section;
    if (it.label !== void 0) p.label = it.label;
    return p;
  }
  function drawingPaint(it) {
    if (!obj2(it) || typeof it.anchor !== "string" || !DRAWING.test(it.anchor)) return null;
    if (it.kind !== "unsent" && it.kind !== "committed" || !oneOf(it.tint, TINTS)) return null;
    const e = it.ellipse;
    if (!obj2(e) || !nums2(e.center, 2) || !nums2(e.radii, 2) || !num2(e.rotation) || !num2(e.start) || e.dir !== 1 && e.dir !== -1) return null;
    const strs2 = (v) => Array.isArray(v) && v.length <= LIMITS.layoutKeys && all(v, text);
    if (!Array.isArray(it.targets) || !it.targets.length || it.targets.length > LIMITS.targets || !strs2(it.open) || !strs2(it.checked)) return null;
    const targets = [];
    for (const t of it.targets) {
      if (!obj2(t) || typeof t.anchor !== "string" || !ANCHOR.test(t.anchor) || t.anchor.charAt(0) !== "h" || !nums2(t.box, 4)) return null;
      const o = { anchor: t.anchor, box: [t.box[0], t.box[1], t.box[2], t.box[3]] };
      if (t.nth !== void 0 || t.of !== void 0) {
        if (!num2(t.nth) || !num2(t.of)) return null;
        o.nth = t.nth;
        o.of = t.of;
      }
      targets.push(o);
    }
    return {
      anchor: it.anchor,
      kind: it.kind,
      tint: it.tint,
      ellipse: { center: [e.center[0], e.center[1]], radii: [e.radii[0], e.radii[1]], rotation: e.rotation, start: e.start, dir: e.dir },
      targets,
      open: it.open.slice(),
      checked: it.checked.slice()
    };
  }

  // src/pointer.ts
  var SKIP = ".rev-glyph, script, style, noscript, template";
  var STEP = 50;
  var REHASH_EVERY = 250;
  var kids = (el) => Array.prototype.filter.call(el.children, (c) => !c.matches(SKIP));
  var nonSpace = (s) => s.replace(/\s+/g, "").length;
  var frac = (d, size) => size >= 1 ? Math.round(Math.min(1, Math.max(0, d / size)) * 1e3) / 1e3 : 0.5;
  var words = (root) => document.createTreeWalker(root, NodeFilter.SHOW_ELEMENT | NodeFilter.SHOW_TEXT, {
    acceptNode: (n) => n.nodeType === 3 ? NodeFilter.FILTER_ACCEPT : n.matches(SKIP) ? NodeFilter.FILTER_REJECT : NodeFilter.FILTER_SKIP
  });
  function caretAt(x, y) {
    const d = document;
    if (d.caretPositionFromPoint) {
      const p = d.caretPositionFromPoint(x, y);
      return p ? [p.offsetNode, p.offset] : null;
    }
    const r = d.caretRangeFromPoint ? d.caretRangeFromPoint(x, y) : null;
    return r ? [r.startContainer, r.startOffset] : null;
  }
  function charBox(node, i) {
    if (i < 0 || i >= node.data.length) return null;
    const range = document.createRange();
    range.setStart(node, i);
    range.setEnd(node, i + 1);
    const r = range.getBoundingClientRect();
    return r.height ? r : null;
  }
  function pathTo(host, el) {
    const path = [];
    for (let n = el; n !== host; ) {
      const p = n.parentElement;
      const i = p ? kids(p).indexOf(n) : -1;
      if (!p || i < 0 || path.length === MAX_PATH) return null;
      path.unshift(i);
      n = p;
    }
    return path;
  }
  function follow(host, path) {
    let n = host;
    for (let i = 0; n && i < path.length; i++) n = kids(n)[path[i]];
    return n || null;
  }
  function pointerLayer(selector, exclude, moved, located) {
    let started = false;
    let index = null;
    let stale = false, hashed = 0;
    let rehash;
    function idx() {
      const wait = hashed + REHASH_EVERY - Date.now();
      if (index && stale && wait > 0) {
        if (rehash === void 0) rehash = setTimeout(() => {
          rehash = void 0;
          relayout();
        }, wait);
        return index;
      }
      if (!index || stale) {
        const of = /* @__PURE__ */ new Map(), by = /* @__PURE__ */ Object.create(null);
        markables(selector, exclude).forEach(({ el, anchor }) => {
          of.set(el, anchor);
          (by[anchor] = by[anchor] || []).push(el);
        });
        index = { of, by };
        stale = false;
        hashed = Date.now();
      }
      return index;
    }
    let point = null;
    let sent = "null";
    let timer;
    let last = 0;
    function share() {
      if (timer !== void 0 || !started) return;
      timer = setTimeout(() => {
        timer = void 0;
        last = Date.now();
        const at = point ? locationAt(point[0], point[1]) : null;
        const s = JSON.stringify(at);
        if (s !== sent) {
          sent = s;
          moved(at);
        }
      }, Math.max(0, last + STEP - Date.now()));
    }
    function locationAt(cx, cy) {
      let hit = null;
      for (const e of document.elementsFromPoint(cx, cy)) if (!e.closest(".rev-ink")) {
        hit = e;
        break;
      }
      if (!hit || hit.closest(exclude)) return null;
      const host = hit.closest(selector), i = idx();
      const anchor = host && i.of.get(host);
      if (!host || !anchor) return null;
      const at = { anchor, x: 0.5, y: 0.5 };
      const same = i.by[anchor];
      if (same.length > 1) {
        at.nth = same.indexOf(host);
        at.of = same.length;
      }
      const path = pathTo(host, hit);
      if (path && path.length) at.path = path;
      const r = (path ? hit : host).getBoundingClientRect();
      at.x = frac(cx - r.left, r.width);
      at.y = frac(cy - r.top, r.height);
      const t = textAt(host, cx, cy);
      if (t !== void 0) at.text = t;
      return at;
    }
    function textAt(host, cx, cy) {
      const c = caretAt(cx, cy);
      if (!c || c[0].nodeType !== 3 || !host.contains(c[0])) return void 0;
      const node = c[0], off = c[1];
      const r = charBox(node, off < node.data.length ? off : off - 1);
      if (!r || cy < r.top || cy > r.bottom || cx < r.left - r.height || cx > r.right + r.height) return void 0;
      let n = 0;
      const w = words(host);
      for (let t = w.nextNode(); t; t = w.nextNode()) {
        if (t === node) return n + nonSpace(node.data.slice(0, off));
        n += nonSpace(t.data);
      }
      return void 0;
    }
    let wanted = [];
    let told = "[]";
    let frame = 0;
    function answer() {
      frame = 0;
      if (!started) return;
      const out = [];
      wanted.forEach((p) => {
        const pt = place(p.at);
        if (pt) out.push({ key: p.key, x: pt[0], y: pt[1] });
      });
      const s = JSON.stringify(out);
      if (s !== told) {
        told = s;
        located(out);
      }
    }
    function place(at) {
      const same = idx().by[at.anchor] || [];
      if (same.length !== (at.of || 1)) return null;
      const host = same[at.nth || 0];
      if (!shownBox(host)) return null;
      let pt = at.text === void 0 ? null : textPoint(host, at.text);
      if (!pt) {
        const el = at.path ? follow(host, at.path) : host;
        if (!el || el !== host && !shownBox(el)) return null;
        const r = el.getBoundingClientRect();
        pt = [r.left + at.x * r.width, r.top + at.y * r.height];
      }
      if (pt[0] < 0 || pt[1] < 0 || pt[0] > innerWidth || pt[1] > innerHeight) return null;
      return [r1(pt[0]), r1(pt[1])];
    }
    function textPoint(host, k) {
      let n = 0, end = null;
      const w = words(host);
      for (let t = w.nextNode(); t; t = w.nextNode()) {
        const s = t.data;
        for (let i = 0; i < s.length; i++) {
          if (/\s/.test(s.charAt(i))) continue;
          if (n++ < k) {
            end = [t, i];
            continue;
          }
          const r2 = t.parentElement && shownBox(t.parentElement) ? charBox(t, i) : null;
          return r2 ? [r2.left, r2.top + r2.height / 2] : null;
        }
      }
      const r = n === k && end && end[0].parentElement && shownBox(end[0].parentElement) ? charBox(end[0], end[1]) : null;
      return r ? [r.right, r.top + r.height / 2] : null;
    }
    function relayout() {
      if (wanted.length && !frame) frame = requestAnimationFrame(answer);
      if (point) share();
    }
    document.addEventListener("pointermove", (e) => {
      if (e.pointerType === "touch") return;
      point = [e.clientX, e.clientY];
      share();
    }, { capture: true, passive: true });
    document.addEventListener("pointerout", (e) => {
      if (!e.relatedTarget) {
        point = null;
        share();
      }
    }, true);
    document.addEventListener("visibilitychange", () => {
      if (document.hidden) {
        point = null;
        share();
      }
    });
    window.addEventListener("pagehide", () => {
      point = null;
      clearTimeout(timer);
      timer = void 0;
      if (sent !== "null") {
        sent = "null";
        moved(null);
      }
    });
    document.addEventListener("scroll", relayout, { capture: true, passive: true });
    window.addEventListener("resize", relayout);
    document.addEventListener("toggle", relayout, true);
    document.addEventListener("change", relayout, true);
    if (window.ResizeObserver) new ResizeObserver(relayout).observe(document.documentElement);
    new MutationObserver((records) => {
      for (const r of records) {
        const el = r.target.nodeType === 1 ? r.target : r.target.parentElement;
        if (el && el.closest(exclude + ", .rev-ink")) continue;
        stale = true;
        relayout();
        return;
      }
    }).observe(document.documentElement, { childList: true, characterData: true, subtree: true });
    return {
      locate(points) {
        wanted = points;
        if (frame) cancelAnimationFrame(frame);
        told = "";
        answer();
      },
      start() {
        started = true;
        index = null;
        answer();
        share();
      }
    };
  }

  // src/ports.ts
  function localPorts() {
    let toHost = null;
    let toDocument = null;
    return {
      host: { send: (m) => toDocument && toDocument(m), listen: (fn) => {
        toHost = fn;
      } },
      document: { send: (m) => toHost && toHost(m), listen: (fn) => {
        toDocument = fn;
      } }
    };
  }

  // src/document-tools.ts
  var CHROME = ".rev-bar, .rev-rail, .rev-orphans, .rev-toast, .rev-sync, .rev-offline-bar, .rev-original, .rev-cursors";
  function typing(e) {
    const t = e.target;
    return !!t && (t.tagName === "TEXTAREA" || t.tagName === "INPUT" || t.isContentEditable);
  }
  var INLINE = /^(A|ABBR|B|BDI|BDO|CITE|CODE|DATA|DEL|DFN|EM|I|INS|KBD|MARK|Q|S|SAMP|SMALL|SPAN|STRONG|SUB|SUP|TIME|U|VAR|WBR)$/;
  var TINTS2 = ["sage", "amber", "red", "gold", "faint"];
  var OWN = "input, select, textarea, label, summary";
  function startDocumentTools(port, opts = {}) {
    const body = document.body;
    const SELECTOR = markableSelector(document);
    const NOT_TEXT = CHROME + ", .rev-pop, .rev-glyph, .rev-hint, script, style, noscript, textarea";
    const HL = !!(window.CSS && CSS.highlights && window.Highlight);
    let on = false;
    let open = false;
    let items = [];
    let drawings = [];
    let qRanges = /* @__PURE__ */ Object.create(null);
    let spots = [];
    let picked = null;
    let focused = null;
    let started = false;
    let tool = false;
    function isRange(at) {
      return "startContainer" in at;
    }
    const NOT_PLAN = CHROME + ", .rev-pop";
    const draw = drawLayer(
      SELECTOR,
      NOT_PLAN,
      (capture, rect, anchor) => port.send({ type: "capture", capture, rect, anchor }),
      (reason) => port.send({ type: "refused", reason }),
      () => paint(items, drawings)
    );
    const pointers = pointerLayer(
      SELECTOR,
      NOT_PLAN,
      (at) => port.send({ type: "pointer", at }),
      (points) => port.send({ type: "located", points })
    );
    function paint(next, nextDrawings) {
      items = next;
      drawings = nextDrawings;
      document.querySelectorAll(".rev-marked,.rev-committed").forEach((el) => {
        el.classList.remove("rev-marked", "rev-committed");
        el.removeAttribute("data-rev");
        el.removeAttribute("data-revstate");
        const g = el.querySelector(":scope > .rev-glyph");
        if (g) g.remove();
      });
      const byAnchor = /* @__PURE__ */ Object.create(null);
      items.forEach((it) => {
        byAnchor[it.anchor] = it;
      });
      const marked = [];
      const all2 = markables(SELECTOR, NOT_PLAN);
      all2.forEach(({ el, anchor: a }) => {
        const it = byAnchor[a];
        if (!it) return;
        const glyph = document.createElement("span");
        glyph.className = "rev-glyph";
        glyph.textContent = it.glyph;
        if (it.kind === "committed") {
          el.classList.add("rev-committed");
          el.setAttribute("data-revstate", it.state || "");
        } else {
          el.classList.add("rev-marked");
          el.setAttribute("data-rev", it.status);
        }
        el.appendChild(glyph);
        marked.push({ el, a });
      });
      const found = marked.map((m) => ({ p: { anchor: m.a, section: headingOf(m.el), label: labelFor(m.el) }, at: m.el }));
      paintQuotes(found);
      const lost = [];
      draw.paint(drawings, all2).forEach((d) => {
        const p = { anchor: d.anchor, section: "", label: "", outcome: d.outcome };
        if (d.el && d.outcome !== "missing" && d.outcome !== "ambiguous") found.push({ p, at: d.el });
        else lost.push(p);
      });
      const pos = found.map((s) => {
        if (isRange(s.at)) return s.at;
        const r = document.createRange();
        r.selectNode(s.at);
        return r;
      });
      const order = found.map((_, i) => i).sort((x, y) => pos[x].compareBoundaryPoints(Range.START_TO_START, pos[y]));
      spots = order.map((i) => found[i]);
      port.send({ type: "placed", items: spots.map((s) => s.p).concat(lost) });
    }
    function paintQuotes(found) {
      qRanges = /* @__PURE__ */ Object.create(null);
      const tints = {};
      let idx = null;
      items.forEach((m) => {
        if (!m.quote || !norm(m.quote)) return;
        idx = idx || textIndex();
        let hit = quoteHits(idx, m.section || "", m.quote)[0];
        const anywhere = hit ? null : quoteHits(idx, null, m.quote);
        if (anywhere && anywhere.length === 1) hit = anywhere[0];
        if (!hit) return;
        const h = { section: m.section || "", label: m.label || clip(m.quote, 70), quote: m.quote, range: rangeAt(idx, hit[0], hit[1]) };
        qRanges[m.anchor] = h;
        found.push({ p: { anchor: m.anchor, section: h.section, label: h.label, quote: h.quote }, at: h.range });
        (tints[m.tint] = tints[m.tint] || []).push(h.range);
      });
      if (!HL) return;
      TINTS2.forEach((c) => {
        CSS.highlights.delete("rev-q-" + c);
        if (!tints[c]) return;
        const hl = new Highlight();
        tints[c].forEach((r) => hl.add(r));
        CSS.highlights.set("rev-q-" + c, hl);
      });
    }
    function focusRange(r) {
      focused = r;
      if (!HL) return;
      if (!r) {
        CSS.highlights.delete("rev-q-focus");
        return;
      }
      const hl = new Highlight();
      hl.add(r);
      CSS.highlights.set("rev-q-focus", hl);
    }
    function flashRange(r) {
      focusRange(r);
      setTimeout(() => {
        if (focused === r) focusRange(null);
      }, 1200);
    }
    function flashEl(el) {
      el.classList.add("rev-flash");
      setTimeout(() => el.classList.remove("rev-flash"), 1200);
    }
    function reveal(anchor, nth) {
      const d = DRAWING.test(anchor) ? drawings.filter((x) => x.anchor === anchor)[0] : null;
      if (d && spots.some((x) => x.p.anchor === anchor && x.p.outcome === "hidden")) {
        draw.show(d);
        paint(items, drawings);
      }
      const s = spots.filter((x) => x.p.anchor === anchor)[nth];
      if (!s) return;
      const range = isRange(s.at) ? s.at : null;
      const el = range ? range.startContainer.parentElement : s.at;
      if (!el) return;
      el.scrollIntoView({ behavior: "smooth", block: "center" });
      if (range) flashRange(range);
      else flashEl(el);
    }
    function textIndex() {
      const nodes = [];
      let text2 = "", prev = null, brk = false;
      const w = document.createTreeWalker(body, NodeFilter.SHOW_TEXT | NodeFilter.SHOW_ELEMENT);
      for (let n = w.nextNode(); n; n = w.nextNode()) {
        if (n.nodeType === 1) {
          if (!INLINE.test(n.tagName)) brk = true;
          continue;
        }
        const t = n;
        if (!t.parentElement || t.parentElement.closest(NOT_TEXT)) continue;
        if (prev && (brk || !inlineBetween(prev, t))) text2 += "\n";
        brk = false;
        nodes.push({ node: t, start: text2.length });
        text2 += t.data;
        prev = t;
      }
      return { nodes, text: text2 };
    }
    function inlineBetween(a, b) {
      const up = [];
      for (let e = a.parentElement; e; e = e.parentElement) up.push(e);
      for (let f = b.parentElement; f && up.indexOf(f) < 0; f = f.parentElement) if (!INLINE.test(f.tagName)) return false;
      for (let i = 0; i < up.length && !up[i].contains(b); i++) if (!INLINE.test(up[i].tagName)) return false;
      return true;
    }
    function nodeAt(idx, pos, isEnd) {
      let lo = 0, hi = idx.nodes.length - 1;
      while (lo < hi) {
        const mid = lo + hi + 1 >> 1, s = idx.nodes[mid].start;
        if (isEnd ? s < pos : s <= pos) lo = mid;
        else hi = mid - 1;
      }
      return idx.nodes[lo];
    }
    function rangeAt(idx, s, e) {
      const a = nodeAt(idx, s), b = nodeAt(idx, e, true), r = document.createRange();
      r.setStart(a.node, s - a.start);
      r.setEnd(b.node, e - b.start);
      return r;
    }
    function headingAt(idx, pos) {
      const x = nodeAt(idx, pos);
      if (x.h === void 0) x.h = norm(headingOf(x.node.parentElement));
      return x.h;
    }
    function quoteHits(idx, section, quote) {
      const q = quote.trim();
      const words2 = q.split(/\s+/).map((w) => w.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"));
      const re = new RegExp((/^\w/.test(q) ? "(^|\\W)" : "()") + words2.join("\\s+") + (/\w$/.test(q) ? "(?!\\w)" : ""), "gi");
      const want = section === null ? null : norm(section), hits = [];
      let m;
      while (m = re.exec(idx.text)) {
        const at = m.index + m[1].length;
        if (want === null || headingAt(idx, at) === want) hits.push([at, m.index + m[0].length]);
      }
      return hits;
    }
    function selectionTarget() {
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
      if (s < 0 || idx.text.slice(s, e).replace(/\s/g, "").length < 2) return null;
      while (s > 0 && /\w/.test(idx.text.charAt(s - 1)) && /\w/.test(idx.text.charAt(s))) s--;
      while (/\w/.test(idx.text.charAt(e)) && /\w/.test(idx.text.charAt(e - 1))) e++;
      const quote = idx.text.slice(s, e).replace(/\s+/g, " ");
      const first = nodeAt(idx, s).node.parentElement, last = nodeAt(idx, e, true).node.parentElement;
      if (first.closest("section[id], [data-ox-section]") !== last.closest("section[id], [data-ox-section]")) {
        return { refuse: "cross-section" };
      }
      const section = headingOf(first);
      const n = quoteHits(idx, section, quote).length;
      if (n > 1) return { refuse: "repeated", quote, count: n };
      const box2 = range.getBoundingClientRect();
      return { anchor: quoteAnchor(section, quote), section, label: clip(quote, 70), quote, range: rangeAt(idx, s, e), rect: { left: box2.left, bottom: box2.bottom } };
    }
    function highlightAt(ev) {
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
    let downAt = null;
    document.addEventListener("mousedown", (e) => {
      downAt = { x: e.clientX, y: e.clientY };
    }, true);
    function madeSelection(ev) {
      if (ev.detail > 1 || ev.shiftKey) return true;
      return ev.detail === 1 && !!downAt && Math.abs(ev.clientX - downAt.x) + Math.abs(ev.clientY - downAt.y) > 3;
    }
    function onClick(ev) {
      if (!started) return;
      const el0 = ev.target;
      if (el0.closest && el0.closest(CHROME + ", .rev-pop, .rev-ink")) return;
      if (!on) {
        port.send({ type: "dismiss" });
        return;
      }
      let t = madeSelection(ev) && selectionTarget() || highlightAt(ev);
      if (t && "refuse" in t) {
        ev.preventDefault();
        port.send(t.refuse === "repeated" ? { type: "refused", reason: "repeated", quote: t.quote, count: t.count } : { type: "refused", reason: "cross-section" });
        return;
      }
      if (!t) {
        const el = el0.closest(OWN) && !el0.closest("a[href]") ? null : el0.closest(SELECTOR);
        if (!el) {
          port.send({ type: "dismiss" });
          return;
        }
        t = { anchor: anchorFor(el), section: headingOf(el), label: labelFor(el) };
      }
      ev.preventDefault();
      picked = t.range ? { a: t.anchor, range: t.range } : null;
      const target = { anchor: t.anchor, section: t.section, label: t.label, rect: t.rect || { left: ev.clientX, bottom: ev.clientY } };
      if (t.quote !== void 0) target.quote = t.quote;
      port.send({ type: "target", target });
      ev.stopPropagation();
    }
    document.addEventListener("click", onClick, true);
    if (opts.forwardKeys) {
      document.addEventListener("keydown", (e) => {
        if (e.defaultPrevented) return;
        if (e.key === "Escape") {
          if (open || on) {
            e.preventDefault();
            port.send({ type: "key", key: "Escape" });
          }
          return;
        }
        if (e.key === "r" && !typing(e) && !e.metaKey && !e.ctrlKey && !e.altKey) {
          e.preventDefault();
          port.send({ type: "key", key: "r" });
        }
      });
    }
    port.listen((m) => {
      switch (m.type) {
        case "mode":
          on = m.on;
          open = m.open;
          body.classList.toggle("rev-on", on);
          tool = on && m.draw;
          draw.setTool(tool && started);
          break;
        case "paint":
          paint(m.items, m.drawings);
          break;
        case "focus": {
          const h = m.anchor === null ? null : picked && picked.a === m.anchor ? picked.range : qRanges[m.anchor] ? qRanges[m.anchor].range : null;
          focusRange(h);
          break;
        }
        case "reveal":
          reveal(m.anchor, m.nth);
          break;
        case "locate":
          pointers.locate(m.points);
          break;
      }
    });
    whenDiagramsDrawn(() => {
      started = true;
      draw.setTool(tool);
      port.send({ type: "ready" });
      pointers.start();
    });
  }
  function whenDiagramsDrawn(go) {
    const deadline = Date.now() + 5e3;
    const check = () => {
      const pending = Array.prototype.some.call(document.querySelectorAll(".mermaid"), (el) => !el.querySelector(":scope > svg"));
      if (!pending || Date.now() >= deadline) go();
      else setTimeout(check, 100);
    };
    check();
  }

  // src/original.ts
  var SVGNS2 = "http://www.w3.org/2000/svg";
  function originalView(r, doc) {
    const mk = (tag, attrs, text2) => {
      const n = doc.createElementNS(SVGNS2, tag);
      for (const k in attrs) n.setAttribute(k, String(attrs[k]));
      if (text2) n.textContent = text2;
      return n;
    };
    const items = r.context.items, runs = runsOf(r.context) || [];
    const labels = items.reduce((all2, it) => all2.concat(it.labels || []), []).filter((l) => !runs.some((t) => overlaps(t.box, l.box)));
    const placed = runs.concat(labels);
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
    const out = mk("svg", { viewBox: vb.join(" "), width: vb[2], height: vb[3], class: "rev-original-svg", role: "img" });
    out.appendChild(mk("title", {}, "What lay under this drawing on revision " + r.plan_revision));
    items.forEach((it) => out.appendChild(mk("rect", { x: it.box[0], y: it.box[1], width: it.box[2], height: it.box[3], class: "rev-orig-box" })));
    runs.forEach((t) => {
      const [x, y, w, h] = t.box;
      out.appendChild(mk("text", { x, y: r1(y + 0.8 * h), "font-size": r1(h / 1.2), textLength: w, lengthAdjust: "spacingAndGlyphs", class: "rev-orig-run" }, t.text));
    });
    labels.forEach((l) => {
      out.appendChild(mk("rect", { x: l.box[0], y: l.box[1], width: l.box[2], height: l.box[3], class: "rev-orig-label-box" }));
      out.appendChild(mk("text", { x: l.box[0] + 2, y: l.box[1] + Math.min(l.box[3], 14) - 3, class: "rev-orig-label" }, l.text));
    });
    const p = inkPaths(inkLine(r.ellipse), [0, 0]);
    const g = mk("g", { class: "rev-orig-ink" });
    g.appendChild(mk("path", { d: p.sweep, fill: "currentColor", stroke: "none" }));
    g.appendChild(mk("path", { d: p.hair, fill: "none", stroke: "currentColor", "stroke-width": 1, "stroke-linecap": "round", "stroke-linejoin": "round" }));
    out.appendChild(g);
    if (r.context.truncated) out.appendChild(mk("text", { x: vb[0] + 4, y: vb[1] + vb[3] - 6, class: "rev-orig-cut" }, "Context truncated: some of what lay here was not kept."));
    const view = doc.createDocumentFragment();
    view.appendChild(out);
    const enclosed = r.targets.map((t) => t.anchor);
    const unplaced = items.filter((it) => it.text && enclosed.indexOf(it.anchor) >= 0 && !placed.some((t) => overlaps(t.box, it.box)));
    if (unplaced.length) {
      const list = doc.createElement("div");
      list.className = "rev-orig-unplaced";
      const head = doc.createElement("div");
      head.className = "rev-orig-unplaced-h";
      head.textContent = "Drawn around (where these words sat was not recorded):";
      list.appendChild(head);
      unplaced.forEach((it) => {
        const q = doc.createElement("blockquote");
        q.textContent = it.text;
        list.appendChild(q);
      });
      view.appendChild(list);
    }
    return view;
  }

  // src/presence.ts
  var SEND_EVERY = 200;
  var COLORS = 8;
  var MAX_NAME = 200;
  var ARROW = '<svg width="16" height="20" viewBox="0 0 16 20" aria-hidden="true"><path d="M1 1l13.5 10.2-6.1.9-3.3 6.4z" fill="currentColor" stroke="#111411" stroke-width="1.2" stroke-linejoin="round"/></svg>';
  var color = (user) => "rev-c" + parseInt(fnv1a(user), 16) % COLORS;
  var short = (v, n) => typeof v === "string" && v.length > 0 && v.length <= n;
  function peerOf(v) {
    const p = v;
    if (!p || typeof p !== "object" || !short(p.id, 128) || !short(p.name, MAX_NAME) || !short(p.user, 128) || !short(p.revision, 128)) return null;
    const at = p.at == null ? null : pointerAt(p.at);
    if (p.at != null && !at) return null;
    return { id: p.id, name: p.name, user: p.user, revision: p.revision, at };
  }
  function startPresence(port, transport, revision, here, frame) {
    let self = "";
    let live = false;
    let gone = false;
    const peers = /* @__PURE__ */ new Map();
    const keys = /* @__PURE__ */ new Map(), ids = /* @__PURE__ */ new Map();
    let minted = 0;
    let placed = [];
    const glide = /* @__PURE__ */ new Set();
    const layer = document.createElement("div");
    layer.className = "rev-cursors";
    layer.setAttribute("aria-hidden", "true");
    document.body.appendChild(layer);
    const cursors = /* @__PURE__ */ new Map();
    let listed = "";
    let mine = null, sent = "null", last = 0, moving = false;
    let timer;
    const link = transport.join(revision, on);
    function on(e) {
      if (gone) return;
      switch (e.type) {
        case "welcome":
          self = typeof e.self === "string" ? e.self : "";
          peers.clear();
          (Array.isArray(e.peers) ? e.peers : []).forEach(add);
          live = true;
          sent = "";
          send(true);
          break;
        case "peer":
          add(e.peer);
          break;
        case "leave":
          peers.delete(e.id);
          break;
        case "lost":
          live = false;
          peers.clear();
          break;
      }
      update();
    }
    function add(v) {
      const p = peerOf(v);
      if (!p || p.id === self) return;
      const was = peers.get(p.id);
      if (was && JSON.stringify(was.at) !== JSON.stringify(p.at)) glide.add(keyOf(p.id));
      peers.set(p.id, p);
    }
    function keyOf(id) {
      let k = keys.get(id);
      if (!k) {
        k = "p" + ++minted;
        keys.set(id, k);
        ids.set(k, id);
      }
      return k;
    }
    function send(now) {
      if (!live || gone || timer !== void 0 && !now) return;
      clearTimeout(timer);
      timer = setTimeout(flush, now ? 0 : Math.max(0, last + SEND_EVERY - Date.now()));
    }
    function flush() {
      timer = void 0;
      const at = mine, s = JSON.stringify(at);
      if (!live || gone || moving || s === sent) return;
      sent = s;
      last = Date.now();
      moving = true;
      Promise.resolve().then(() => link.move(at)).catch(() => {
      }).then(() => {
        moving = false;
        send(false);
      });
    }
    function update() {
      if (gone) return;
      keys.forEach((k, id) => {
        if (!peers.has(id)) {
          keys.delete(id);
          ids.delete(k);
        }
      });
      renderHere();
      const points = [];
      peers.forEach((p) => {
        if (p.revision === revision && p.at && points.length < MAX_POINTERS) points.push({ key: keyOf(p.id), at: p.at });
      });
      port.send({ type: "locate", points });
      cursors.forEach((el, k) => {
        const id = ids.get(k), p = id ? peers.get(id) : void 0;
        if (!p || !p.at) {
          el.remove();
          cursors.delete(k);
        }
      });
    }
    function renderHere() {
      const people = /* @__PURE__ */ new Map();
      peers.forEach((p) => {
        const was = people.get(p.user);
        people.set(p.user, { name: p.name, same: !!was && was.same || p.revision === revision });
      });
      const now = JSON.stringify([...people]);
      if (now === listed) return;
      listed = now;
      here.textContent = "";
      here.hidden = !people.size;
      let other = 0, otherName = "";
      people.forEach((p, user) => {
        const chip = document.createElement("span");
        chip.className = "rev-peer " + color(user) + (p.same ? "" : " rev-peer-other");
        chip.textContent = (Array.from(p.name.trim())[0] || "?").toUpperCase();
        chip.title = p.same ? p.name + " is here" : p.name + " is viewing another revision of this plan";
        here.appendChild(chip);
        if (!p.same) {
          other++;
          otherName = p.name;
        }
      });
      if (other) {
        const note = document.createElement("span");
        note.className = "rev-here-note";
        note.textContent = (other === 1 ? otherName : other + " people") + " on another revision";
        here.appendChild(note);
      }
    }
    function draw() {
      let b = { left: 0, top: 0, right: innerWidth, bottom: innerHeight };
      if (frame) {
        const r = frame.getBoundingClientRect(), left = r.left + frame.clientLeft, top = r.top + frame.clientTop;
        b = { left, top, right: left + frame.clientWidth, bottom: top + frame.clientHeight };
      }
      const clip2 = { left: Math.max(b.left, 0), top: Math.max(b.top, 0), right: Math.min(b.right, innerWidth), bottom: Math.min(b.bottom, innerHeight) };
      const drawn = /* @__PURE__ */ new Set();
      placed.forEach((p) => {
        const id = ids.get(p.key), peer = id ? peers.get(id) : void 0;
        if (!peer || !peer.at) return;
        const x = b.left + p.x, y = b.top + p.y;
        if (x < clip2.left || y < clip2.top || x > clip2.right || y > clip2.bottom) return;
        let el = cursors.get(p.key);
        if (!el) {
          el = document.createElement("div");
          el.innerHTML = ARROW + '<span class="rev-cursor-name"></span>';
          layer.appendChild(el);
          cursors.set(p.key, el);
        }
        el.className = "rev-cursor " + color(peer.user) + (glide.has(p.key) ? " rev-glide" : "");
        el.lastChild.textContent = peer.name;
        el.style.transform = "translate(" + x + "px," + y + "px)";
        drawn.add(p.key);
      });
      glide.clear();
      cursors.forEach((el, k) => {
        if (!drawn.has(k)) {
          el.remove();
          cursors.delete(k);
        }
      });
    }
    return {
      pointer(at) {
        mine = at;
        send(false);
      },
      located(points) {
        placed = points;
        draw();
      },
      ready: update,
      reposition: draw,
      leave() {
        if (gone) return;
        gone = true;
        clearTimeout(timer);
        link.leave();
        port.send({ type: "locate", points: [] });
        layer.remove();
        here.textContent = "";
        here.hidden = true;
      }
    };
  }

  // src/state.ts
  var ACTS = ["submit", "reopen", "accept"];
  var DRAFT = "draft:";
  var OP = "op:";
  var EXPORT = "export";
  var enc = encodeURIComponent;
  var accountPrefix = (account) => "ox-review:" + enc(account) + ":";
  var has = (o, k) => Object.prototype.hasOwnProperty.call(o, k);
  function obj3(v) {
    return typeof v === "object" && v !== null && !Array.isArray(v);
  }
  function read(storage, key) {
    try {
      const s = storage.getItem(key);
      return s === null ? null : JSON.parse(s);
    } catch (e) {
      return null;
    }
  }
  function write(storage, key, v) {
    const s = JSON.stringify(v);
    try {
      storage.setItem(key, s);
      return storage.getItem(key) === s;
    } catch (e) {
      return false;
    }
  }
  function unset(storage, key) {
    try {
      storage.removeItem(key);
    } catch (e) {
    }
  }
  function keysUnder(storage, prefix) {
    const out = [];
    try {
      for (let i = 0; i < storage.length; i++) {
        const k = storage.key(i);
        if (k !== null && k.indexOf(prefix) === 0) out.push(k);
      }
    } catch (e) {
    }
    return out;
  }
  function asMark(v) {
    if (!obj3(v) || typeof v.anchor !== "string" || STATUSES.indexOf(v.status) < 0 || typeof v.at !== "number") return null;
    const m = { anchor: v.anchor, status: v.status, at: v.at };
    ["section", "label", "note", "quote"].forEach((k) => {
      if (typeof v[k] === "string") m[k] = v[k];
    });
    if (v.region !== void 0 && !checkRegion(v.region) && v.anchor === "q-" + v.region.id) m.region = v.region;
    return m;
  }
  function asEntry(v) {
    if (!obj3(v) || typeof v.id !== "string" || ACTS.indexOf(v.act) < 0 || typeof v.body !== "string" || typeof v.at !== "number" || !obj3(v.stamps)) return null;
    const stamps = /* @__PURE__ */ Object.create(null);
    Object.keys(v.stamps).forEach((a) => {
      const s = v.stamps[a];
      if (typeof s === "number") stamps[a] = s;
    });
    return { id: v.id, act: v.act, body: v.body, at: v.at, stamps };
  }
  function openStore(storage, account, plan) {
    const prefix = accountPrefix(account) + enc(plan) + ":";
    const memory = /* @__PURE__ */ Object.create(null);
    let last = 0;
    function stored(anchor) {
      const m = asMark(read(storage, prefix + DRAFT + enc(anchor)));
      return m && m.anchor === anchor ? m : null;
    }
    function entries() {
      const out = [];
      keysUnder(storage, prefix + OP).forEach((k) => {
        const e = asEntry(read(storage, k));
        if (e && prefix + OP + enc(e.id) === k) out.push(e);
      });
      return out.sort((x, y) => x.at - y.at || (x.id < y.id ? -1 : x.id > y.id ? 1 : 0));
    }
    function now() {
      last = Math.max(Date.now(), last + 1);
      return last;
    }
    return {
      account,
      prefix,
      drafts() {
        const out = /* @__PURE__ */ Object.create(null);
        keysUnder(storage, prefix + DRAFT).forEach((k) => {
          const m = asMark(read(storage, k));
          if (m && prefix + DRAFT + enc(m.anchor) === k) out[m.anchor] = m;
        });
        Object.keys(memory).forEach((a) => {
          if (!out[a] || memory[a].at > out[a].at) out[a] = memory[a];
        });
        return Object.keys(out).map((a) => out[a]).sort((x, y) => x.at - y.at);
      },
      save(m) {
        let at = now();
        const prev = stored(m.anchor);
        if (prev) at = Math.max(at, prev.at + 1);
        if (memory[m.anchor]) at = Math.max(at, memory[m.anchor].at + 1);
        entries().forEach((e) => {
          if (has(e.stamps, m.anchor)) at = Math.max(at, e.stamps[m.anchor] + 1);
        });
        last = at;
        const mark = { anchor: m.anchor, status: m.status, at };
        ["section", "label", "note", "quote"].forEach((k) => {
          if (m[k] !== void 0) mark[k] = m[k];
        });
        if (m.region) mark.region = m.region;
        if (write(storage, prefix + DRAFT + enc(m.anchor), mark)) {
          delete memory[m.anchor];
          return true;
        }
        memory[m.anchor] = mark;
        return false;
      },
      remove(anchor) {
        unset(storage, prefix + DRAFT + enc(anchor));
        delete memory[anchor];
      },
      unsaved(anchor) {
        if (!memory[anchor]) return false;
        const s = stored(anchor);
        return !s || memory[anchor].at > s.at;
      },
      entries,
      freeze(act, id, body, stamps) {
        const e = { id, act, body, at: now(), stamps };
        return write(storage, prefix + OP + enc(id), e) ? e : null;
      },
      owned(e) {
        Object.keys(e.stamps).forEach((a) => {
          const s = stored(a);
          if (s && s.at <= e.stamps[a]) unset(storage, prefix + DRAFT + enc(a));
          if (memory[a] && memory[a].at <= e.stamps[a]) delete memory[a];
        });
        unset(storage, prefix + OP + enc(e.id));
      },
      drop(e) {
        unset(storage, prefix + OP + enc(e.id));
      },
      exportId(items) {
        const prev = read(storage, prefix + EXPORT);
        const id = obj3(prev) && typeof prev.id === "string" && JSON.stringify(prev.items) === JSON.stringify(items) ? prev.id : newId();
        write(storage, prefix + EXPORT, { id, items });
        return id;
      }
    };
  }
  function queued(entries, m) {
    return entries.some((e) => has(e.stamps, m.anchor) && e.stamps[m.anchor] === m.at);
  }
  function wireItem(m) {
    const o = { anchor: m.anchor, section: m.section || "", label: m.label || "" };
    if (m.quote) o.quote = m.quote;
    o.status = m.status;
    o.note = m.note || "";
    if (m.region) o.region = m.region;
    return o;
  }
  function roundItems(items) {
    return items.some((it) => it.region) ? { items: [], marks: items } : { items };
  }
  function request(id, reviewer, rest) {
    const o = { id };
    if (reviewer) o.reviewer = reviewer;
    return JSON.stringify(Object.assign(o, rest));
  }
  function submitBody(id, reviewer, marks) {
    return request(id, reviewer, roundItems(marks.map(wireItem)));
  }
  function reopenBody(id, reviewer, item, note) {
    return request(id, reviewer, roundItems([wireItem({ ...item, status: "request-change", note: note || "reopened by reviewer" })]));
  }
  function acceptBody(id, reviewer, anchor) {
    return request(id, reviewer, { anchor, state: "verified", note: "accepted by reviewer" });
  }
  async function deliver(store, transport, report, stopped) {
    const tried = /* @__PURE__ */ Object.create(null);
    for (; ; ) {
      const e = store.entries().filter((x) => !tried[x.id])[0];
      if (!e || stopped()) return;
      tried[e.id] = true;
      let r;
      try {
        r = await transport.send({ account: store.account, id: e.id, kind: e.act === "accept" ? "resolution" : "round", body: e.body });
      } catch (err) {
        r = { state: "failed", message: err instanceof Error ? err.message : String(err) };
      }
      if (r.state === "owned" && r.id !== e.id) r = { state: "failed", message: "the server acknowledged a different operation" };
      if (r.state === "owned") store.owned(e);
      else if (r.state === "refused") store.drop(e);
      report(e, r);
      if (r.state === "failed" || r.state === "offline") return;
    }
  }
  function syncMessage(verb, r) {
    const m = r.synced ? { kind: "ok", text: verb + " · synced to your team" } : { kind: "warn", text: "Saved on the author’s machine · not yet synced (will retry)" };
    if (r.warning) m.text += " · " + r.warning;
    return m;
  }
  function newId() {
    const c = typeof crypto !== "undefined" ? crypto : null;
    if (c && c.randomUUID) return c.randomUUID();
    const b = new Uint8Array(16);
    let s = "";
    if (c && c.getRandomValues) c.getRandomValues(b);
    else for (let i = 0; i < 16; i++) b[i] = Math.floor(Math.random() * 256);
    for (let j = 0; j < 16; j++) s += ("0" + b[j].toString(16)).slice(-2);
    return s;
  }
  function migrateLegacy(storage, slug, store) {
    const KEY = "ox-plan-fb:" + slug;
    const marks = read(storage, KEY), box2 = read(storage, KEY + ":outbox"), exp = read(storage, KEY + ":export");
    if (marks === null && box2 === null && exp === null) return;
    let ok = true;
    const current = store.drafts();
    if (obj3(marks)) {
      Object.keys(marks).forEach((a) => {
        const v = marks[a];
        const m = asMark(obj3(v) ? { ...v, anchor: a, at: Math.max(+v.updated_at || 0, 1) } : null);
        if (!m || current.some((d) => d.anchor === a && d.at >= m.at)) return;
        ok = write(storage, store.prefix + DRAFT + enc(a), m) && ok;
      });
    }
    if (obj3(box2) && typeof box2.id === "string" && Array.isArray(box2.items)) {
      const stamps = /* @__PURE__ */ Object.create(null);
      const s = obj3(box2.stamps) ? box2.stamps : {};
      box2.items.forEach((it) => {
        if (obj3(it) && typeof it.anchor === "string") stamps[it.anchor] = Math.max(+s[it.anchor] || 0, 1);
      });
      const body = JSON.stringify({ id: box2.id, slug, reviewer: box2.reviewer, items: box2.items });
      const e = { id: box2.id, act: "submit", body, at: Date.parse(box2.created_at) || Date.now(), stamps };
      ok = write(storage, store.prefix + OP + enc(e.id), e) && ok;
    }
    if (obj3(exp)) ok = write(storage, store.prefix + EXPORT, exp) && ok;
    if (ok) ["", ":tombs", ":outbox", ":export"].forEach((k) => unset(storage, KEY + k));
  }

  // src/host-controls.ts
  var STATUS = [
    { id: "approve", glyph: "✓" },
    { id: "request-change", glyph: "✎" },
    { id: "flag", glyph: "⚑" },
    { id: "comment", glyph: "◌" }
  ];
  var HL_TINT = { "approve": "sage", "request-change": "amber", "flag": "red", "comment": "gold" };
  var stateTint = (state) => state === "addressed" || state === "verified" ? "sage" : state === "wontfix" ? "faint" : "amber";
  var DRAW_REFUSED = {
    "no-target": "Nothing in that oval can take a comment. Draw around a paragraph, an item, a row or part of a diagram.",
    "too-large": "That oval holds too much of the plan to keep. Draw around less of it.",
    "too-small": "Drag to draw a larger oval around what you mean.",
    "layout-changed": "The page changed while you drew. Draw the oval again."
  };
  var REVISION2 = /^sha256:[0-9a-f]{64}$/;
  var WHOLE = { a: "plan", section: "", label: "Whole plan" };
  var BUBBLE = '<svg width="14" height="14" viewBox="0 0 16 16" aria-hidden="true"><path d="M3 2.5h10A1.5 1.5 0 0 1 14.5 4v6a1.5 1.5 0 0 1-1.5 1.5H7.5L4 14.5v-3H3A1.5 1.5 0 0 1 1.5 10V4A1.5 1.5 0 0 1 3 2.5z" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round"/></svg>';
  function esc(s) {
    return (s || "").replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;").replace(/'/g, "&#39;");
  }
  var STATE_TAG = { addressed: "addressed", verified: "verified", wontfix: "wontfix" };
  var TEXT_FIELDS = ["section", "label", "quote", "note", "reviewer", "resolutionNote"];
  function glyphFor(id) {
    for (let i = 0; i < STATUS.length; i++) if (STATUS[i].id === id) return STATUS[i].glyph;
    return "◌";
  }
  function repOf(arr) {
    for (let i = 0; i < arr.length; i++) {
      if (arr[i].state === "open" && (arr[i].status === "request-change" || arr[i].status === "flag")) return arr[i];
    }
    for (let j = 0; j < arr.length; j++) {
      if (arr[j].state === "open") return arr[j];
    }
    for (let k = 0; k < arr.length; k++) {
      if (arr[k].state === "addressed" || arr[k].state === "verified") return arr[k];
    }
    return arr[0];
  }
  function mountHostControls(port, opts) {
    const body = document.body;
    const slug = opts.slug;
    const transport = opts.transport;
    const live = !!transport;
    const store = openStore(window.localStorage, opts.account, opts.plan);
    const tab = encodeURIComponent(opts.plan);
    const ON_KEY = "ox-plan-rev-on:" + tab;
    const MIN_KEY = "ox-plan-rail-min:" + tab;
    const SYNC_KEY = "ox-plan-fb-sync:" + tab;
    const off = new AbortController();
    const signal = off.signal;
    let problems = opts.problems || 0;
    let invalid = 0;
    let committed = /* @__PURE__ */ Object.create(null);
    function setCommitted(items) {
      committed = /* @__PURE__ */ Object.create(null);
      invalid = 0;
      items.forEach((x) => {
        if (!x || typeof x.anchor !== "string" || STATUSES.indexOf(x.status) < 0 || STATES.indexOf(x.state) < 0 || TEXT_FIELDS.some((k) => x[k] !== void 0 && typeof x[k] !== "string")) {
          invalid++;
          return;
        }
        if (x.region !== void 0 && (checkRegion(x.region) || x.anchor !== "q-" + x.region.id)) {
          invalid++;
          x = Object.assign({}, x);
          delete x.region;
        }
        (committed[x.anchor] = committed[x.anchor] || []).push(x);
      });
    }
    setCommitted(opts.committed);
    let named = "";
    if (!opts.reviewer) try {
      named = (localStorage.getItem("ox-plan-reviewer") || "").trim();
    } catch (e) {
    }
    function who() {
      return opts.reviewer || named;
    }
    let on = false;
    let offline = false;
    let destroyed = false;
    let inflight = 0;
    let flushing = false, again = false;
    let waiter = null;
    let ds = /* @__PURE__ */ Object.create(null);
    let placed = [];
    let unplaced = [];
    const revision = opts.revision && REVISION2.test(opts.revision) ? opts.revision : "";
    let tool = false;
    let pending = null;
    const ownedDrawings = /* @__PURE__ */ new Set();
    let presence = null;
    function regionOf(a) {
      if (pending && "q-" + pending.id === a) return pending;
      if (ds[a] && ds[a].region) return ds[a].region;
      return (committed[a] || []).map((x) => x.region).filter((r) => r)[0];
    }
    function committedState(a) {
      const arr = committed[a] || [];
      return arr.length ? repOf(arr).state : "";
    }
    function paintItems() {
      const out = [];
      Object.keys(committed).concat(Object.keys(ds)).forEach((a, i, all2) => {
        if (all2.indexOf(a) !== i || !/^[hq][0-9a-f]{8}$/.test(a)) return;
        const carr = committed[a] || [];
        const c = carr.length ? repOf(carr) : null, m = ds[a];
        let it;
        if (m) {
          it = { anchor: a, kind: "unsent", status: m.status, glyph: glyphFor(m.status), tint: HL_TINT[m.status] || "gold" };
        } else if (c) {
          it = {
            anchor: a,
            kind: "committed",
            status: c.status,
            state: c.state,
            glyph: c.state === "addressed" || c.state === "verified" ? "✓" : c.state === "wontfix" ? "—" : glyphFor(c.status),
            tint: stateTint(c.state)
          };
        } else return;
        const q = m && m.quote ? m : carr.filter((x) => x.quote)[0];
        if (q && q.quote) {
          it.quote = q.quote;
          it.section = q.section || "";
          if (q.label) it.label = q.label;
        }
        const ok = paintItem(it);
        if (ok) out.push(ok);
      });
      return out;
    }
    function paintDrawings() {
      const out = [];
      const waiting = store.entries();
      Object.keys(committed).concat(Object.keys(ds), pending ? ["q-" + pending.id] : []).forEach((a, i, all2) => {
        const r = all2.indexOf(a) === i && DRAWING.test(a) ? regionOf(a) : void 0;
        if (!r) return;
        const m = ds[a], carr = committed[a] || [];
        const d = drawingPaint({
          anchor: a,
          kind: carr.length || ownedDrawings.has(a) || waiting.some((e) => e.stamps[a] !== void 0) ? "committed" : "unsent",
          tint: m ? HL_TINT[m.status] || "gold" : carr.length ? stateTint(repOf(carr).state) : "gold",
          ellipse: r.ellipse,
          targets: r.targets.map((t) => ({ anchor: t.anchor, box: t.box, nth: t.nth, of: t.of })),
          open: r.layout.open,
          checked: r.layout.checked
        });
        if (d) out.push(d);
      });
      return out;
    }
    function paint() {
      if (destroyed) return;
      place();
      ds = /* @__PURE__ */ Object.create(null);
      store.drafts().forEach((m) => {
        ds[m.anchor] = m;
      });
      const n = Object.keys(ds).length;
      countEl.textContent = n ? n + " unsent" : "";
      port.send({ type: "paint", items: paintItems(), drawings: paintDrawings() });
    }
    let outcome = /* @__PURE__ */ Object.create(null);
    function onPlaced(items) {
      outcome = /* @__PURE__ */ Object.create(null);
      items.forEach((p) => {
        if (p.outcome) outcome[p.anchor] = p.outcome;
      });
      const seen = /* @__PURE__ */ Object.create(null);
      seen[WHOLE.a] = true;
      placed = items.filter((p) => p.outcome !== "missing" && p.outcome !== "ambiguous" && !seen[p.anchor] && (seen[p.anchor] = true));
      unplaced = Object.keys(committed).concat(Object.keys(ds)).filter((a, i, all2) => !seen[a] && all2.indexOf(a) === i);
      const orphans = [];
      Object.keys(committed).forEach((a) => {
        if (!seen[a]) committed[a].forEach((x) => orphans.push(x));
      });
      renderOrphans(orphans);
      renderRail();
    }
    let orphanBar = null;
    function renderOrphans(orphans) {
      if (orphanBar) {
        orphanBar.remove();
        orphanBar = null;
      }
      const open = orphans.filter((o) => o.state === "open");
      const n = problems + invalid;
      if (!open.length && !n) return;
      const bar2 = orphanBar = document.createElement("div");
      bar2.className = "rev-orphans";
      let html = n ? '<div class="rev-orphans-h">⚠ This review could not be fully read or holds conflicting copies (' + n + " problem(s)): notes may be missing or shown resolved.</div>" : "";
      if (open.length) {
        const items = open.map((o) => "<li><strong>" + esc(o.status) + "</strong> " + esc(o.label || o.anchor) + (o.note ? " — " + esc(o.note) : "") + "</li>").join("");
        html += '<div class="rev-orphans-h">⚠ ' + open.length + " review note(s) no longer anchored (the text changed) — still open:</div><ul>" + items + "</ul>";
      }
      bar2.innerHTML = html + '<button class="rev-orphans-x">dismiss</button>';
      body.appendChild(bar2);
      bar2.querySelector(".rev-orphans-x").onclick = () => {
        bar2.remove();
        if (orphanBar === bar2) orphanBar = null;
      };
    }
    let rail = null;
    let railMin = false;
    try {
      railMin = !!sessionStorage.getItem(MIN_KEY);
    } catch (e) {
    }
    function toggleRail() {
      railMin = !railMin;
      try {
        if (railMin) sessionStorage.setItem(MIN_KEY, "1");
        else sessionStorage.removeItem(MIN_KEY);
      } catch (e) {
      }
      renderRail();
      rail.querySelector(".rev-rail-show, .rev-rail-hide").focus();
    }
    function railRows() {
      const waiting = store.entries();
      const rows = [];
      function add(at, a, section, label, quote) {
        (committed[a] || []).forEach((c) => {
          rows.push({
            at,
            a,
            quote,
            status: c.status,
            note: c.note,
            section: c.section || section,
            label: c.label || label,
            state: c.state,
            reviewer: c.reviewer || "",
            tag: "",
            answer: c.state !== "open" && c.resolutionNote ? c.resolutionNote : "",
            region: c.region
          });
        });
        const m = ds[a];
        if (m) {
          rows.push({
            at,
            a,
            quote,
            status: m.status,
            note: m.note,
            section: m.section || section,
            label: m.label || label,
            state: "",
            reviewer: who() || "(you)",
            tag: store.unsaved(a) ? "unsaved" : queued(waiting, m) ? "queued" : "unsent",
            answer: "",
            region: m.region
          });
        }
      }
      add(null, WHOLE.a, WHOLE.section, WHOLE.label, "");
      placed.forEach((p) => add({ anchor: p.anchor, nth: 0 }, p.anchor, p.section, p.label, p.quote || ""));
      unplaced.forEach((a) => {
        const m = ds[a];
        const its = (committed[a] || []).concat(m ? [m] : []);
        const q = its.filter((x) => x.quote)[0];
        const any = q || its.filter((x) => x.label)[0] || its[0];
        add(null, a, any.section || "", any.label || "", q ? q.quote || "" : "");
      });
      return rows;
    }
    function renderRail() {
      if (!rail) {
        rail = document.createElement("aside");
        rail.className = "rev-rail";
        body.appendChild(rail);
        rail.addEventListener("click", (ev) => {
          const t = ev.target;
          if (t.closest(".rev-rail-show, .rev-rail-hide")) toggleRail();
          const plan2 = t.closest(".rev-rail-plan");
          if (plan2) {
            const b = plan2.getBoundingClientRect();
            openPop(WHOLE, { left: b.left, bottom: b.bottom }, true);
            popByHost = true;
          }
        });
      }
      const r = rail;
      const rows = railRows();
      if (!on && !rows.length) {
        r.style.display = "none";
        r.innerHTML = "";
        return;
      }
      r.style.display = "";
      r.classList.toggle("rev-rail-min", railMin);
      if (railMin) {
        r.innerHTML = '<button class="rev-rail-show" title="Show comments" aria-label="Show comments (' + rows.length + ')">' + BUBBLE + rows.length + "</button>";
        return;
      }
      const hide = '<button class="rev-rail-hide" title="Hide comments">Hide</button>';
      const plan = on ? '<button class="rev-rail-plan">+ Comment on the whole plan</button>' : "";
      if (!rows.length) {
        r.innerHTML = '<div class="rev-rail-h">Comments' + hide + "</div>" + plan + '<div class="rev-rail-empty">Click any section, risk, or row — or highlight text' + (revision ? ", or Draw around part of the plan" : "") + " — to leave a comment.</div>";
        return;
      }
      let html = '<div class="rev-rail-h">Comments <span class="rev-rail-n">' + rows.length + "</span>" + hide + "</div>" + plan + '<ul class="rev-rail-list">';
      rows.forEach((row, i) => {
        const whole2 = row.a === WHOLE.a;
        let tag = "";
        if (row.tag) tag = '<span class="rev-rail-tag ' + row.tag + '">' + row.tag + "</span>";
        else if (STATE_TAG[row.state]) tag = '<span class="rev-rail-tag ' + STATE_TAG[row.state] + '">' + STATE_TAG[row.state] + "</span>";
        if (!row.at && !whole2) tag += outcome[row.a] === "ambiguous" ? '<span class="rev-rail-tag" title="What it was drawn around now appears a different number of times">repeated</span>' : '<span class="rev-rail-tag">text changed</span>';
        else if (outcome[row.a] === "hidden") tag += '<span class="rev-rail-tag" title="Collapsed or on another tab: click to show it">hidden</span>';
        const content = (row.quote ? '<div class="rev-rail-quote">' + esc(row.label) + "</div>" : "") + (row.note ? '<div class="rev-rail-note">' + esc(row.note) + "</div>" : row.quote || whole2 ? "" : '<div class="rev-rail-note muted">' + esc(row.label) + "</div>") + (row.answer ? '<div class="rev-rail-answer">↳ ' + esc(row.answer) + "</div>" : "") + (row.region ? '<button class="rev-rail-orig" title="What lay under this drawing when it was drawn">View original</button>' : "");
        const whoTag = row.reviewer ? '<span class="rev-rail-who">' + esc(row.reviewer) + "</span>" : "";
        html += '<li class="rev-rail-item" data-i="' + i + '" data-status="' + esc(row.status) + '"><span class="rev-rail-glyph">' + glyphFor(row.status) + '</span><div class="rev-rail-body"><div class="rev-rail-sec"><span class="rev-rail-sec-t">' + esc(whole2 ? WHOLE.label : row.section || "(plan)") + "</span>" + whoTag + tag + "</div>" + content + "</div></li>";
      });
      html += "</ul>";
      r.innerHTML = html;
      r.querySelectorAll(".rev-rail-item").forEach((li) => {
        li.onclick = (ev) => {
          const row = rows[+li.getAttribute("data-i")];
          if (!row) return;
          if (row.region && ev.target.closest(".rev-rail-orig")) {
            showOriginal(row.region);
            ev.stopPropagation();
            return;
          }
          if (row.at) port.send({ type: "reveal", anchor: row.at.anchor, nth: row.at.nth });
          if (!row.at || DRAWING.test(row.a)) {
            openPop({ a: row.a, section: row.section, label: row.label, quote: row.quote }, { left: ev.clientX, bottom: ev.clientY });
            popByHost = true;
            ev.stopPropagation();
          }
        };
      });
    }
    let pop = null;
    let popAbout = null;
    let popNote = "";
    let popByHost = false;
    function sendMode() {
      port.send({ type: "mode", on, open: !!pop, draw: tool });
    }
    function held() {
      const n = pop ? pop.querySelector(".rev-note") : null;
      return !!pop && (popByHost || !!n && n.value.trim() !== popNote);
    }
    function closePop(replacing) {
      popByHost = false;
      if (pop) {
        pop.remove();
        pop = null;
        sendMode();
        popAbout = null;
        if (pending) {
          pending = null;
          paint();
        }
      }
      port.send({ type: "focus", anchor: null });
      if (!replacing) idle();
    }
    function openPop(t, rect, fresh) {
      closePop(true);
      popAbout = t;
      const a = t.a;
      const cstate = committedState(a);
      const p = pop = document.createElement("div");
      p.className = "rev-pop";
      const quote = '<div class="' + (t.quote ? "rev-quote" : "rev-about") + '">' + esc(clip(t.label || t.a, 280)) + "</div>";
      port.send({ type: "focus", anchor: a });
      if (live && opts.resolve && !fresh && (cstate === "addressed" || cstate === "verified")) {
        popNote = "";
        p.innerHTML = quote + '<div class="rev-cap">Agent marked this ' + cstate + '.</div><textarea class="rev-note" placeholder="reopen note (optional)"></textarea><div class="rev-row"><button class="rev-accept">Accept</button><button class="rev-reopen">Reopen</button></div>';
        placePop(rect);
        p.querySelector(".rev-accept").onclick = () => {
          accept(a);
        };
        p.querySelector(".rev-reopen").onclick = () => {
          reopen(a, p.querySelector(".rev-note").value.trim());
        };
        return;
      }
      const existing = ds[a] || { status: "request-change", note: "" };
      popNote = existing.note || "";
      const btns = STATUS.map((s) => '<button data-s="' + s.id + '" class="rev-s' + (existing.status === s.id ? " on" : "") + '">' + s.glyph + " " + s.id + "</button>").join("");
      p.innerHTML = quote + '<div class="rev-row">' + btns + '</div><textarea class="rev-note" placeholder="note (optional)">' + esc(existing.note || "") + '</textarea><div class="rev-row"><button class="rev-save">Save</button><button class="rev-del">Delete</button></div>';
      placePop(rect);
      let status = existing.status;
      p.querySelectorAll(".rev-s").forEach((b) => {
        b.onclick = () => {
          status = b.getAttribute("data-s");
          p.querySelectorAll(".rev-s").forEach((x) => x.classList.remove("on"));
          b.classList.add("on");
        };
      });
      p.querySelector(".rev-save").onclick = () => {
        const m = { anchor: a, section: t.section, label: t.label, status, note: p.querySelector(".rev-note").value.trim() };
        if (t.quote) m.quote = t.quote;
        const r = regionOf(a);
        if (r) m.region = r;
        const kept = store.save(m);
        pending = null;
        paint();
        closePop();
        if (!kept) showSync("err", "This browser could not save your note: it lives in this tab only and is lost when the tab closes. Free up browser storage, then save it again.");
      };
      p.querySelector(".rev-del").onclick = () => {
        store.remove(a);
        pending = null;
        paint();
        closePop();
      };
    }
    let original = null;
    function closeOriginal() {
      if (original) {
        original.remove();
        original = null;
      }
    }
    function showOriginal(r) {
      closeOriginal();
      const el = original = document.createElement("div");
      el.className = "rev-original";
      el.setAttribute("role", "dialog");
      el.setAttribute("aria-label", "The original drawing");
      const head = document.createElement("div");
      head.className = "rev-original-h";
      const l = r.layout;
      head.textContent = "Drawn on revision " + r.plan_revision.slice(7, 19) + " at " + l.viewport.join(" × ") + ", " + l.theme + (revision ? r.plan_revision === revision ? " · the revision shown" : " · not the revision shown" : "");
      const x = document.createElement("button");
      x.className = "rev-original-x";
      x.textContent = "Close";
      x.onclick = closeOriginal;
      head.appendChild(x);
      el.appendChild(head);
      el.appendChild(originalView(r, document));
      body.appendChild(el);
    }
    function box2() {
      return opts.frame ? opts.frame.getBoundingClientRect() : { left: 0, top: 0, right: window.innerWidth, bottom: window.innerHeight };
    }
    function toHost(r) {
      const b = box2();
      return {
        left: Math.min(Math.max(r.left + b.left, b.left), Math.max(b.left, b.right - 300)),
        bottom: Math.min(Math.max(r.bottom + b.top, b.top), Math.max(b.top, b.bottom - 200))
      };
    }
    function place() {
      if (!opts.frame || destroyed) return;
      const r = opts.frame.getBoundingClientRect();
      const top = Math.max(r.top, 0), bottom = Math.min(r.bottom, window.innerHeight);
      const v = {
        top,
        left: Math.max(r.left, 0),
        right: Math.max(window.innerWidth - r.right, 0),
        bottom: Math.max(window.innerHeight - bottom, 0),
        width: Math.max(r.width, 0),
        height: Math.max(bottom - top, 0)
      };
      Object.keys(v).forEach((k) => body.style.setProperty("--rev-" + k, v[k] + "px"));
      if (presence) presence.reposition();
    }
    function placePop(rect) {
      const p = pop;
      body.appendChild(p);
      p.style.top = window.scrollY + rect.bottom + 8 + "px";
      p.style.left = window.scrollX + Math.min(rect.left, window.innerWidth - 300) + "px";
      sendMode();
    }
    document.addEventListener("click", (ev) => {
      const t = ev.target;
      if (pop && pop.contains(t)) return;
      if (t.closest && t.closest(CHROME) && (on || pop)) closePop();
    }, { capture: true, signal });
    function queue(act, build, stamps, retry) {
      if (!who()) {
        askName("Your name, so teammates know whose feedback this is:", retry);
        return;
      }
      const id = newId();
      if (!store.freeze(act, id, build(id, named), stamps)) {
        showSync("err", "Not sent — this browser could not store it, so nothing was sent. Free up browser storage, then try again.");
        return;
      }
      closePop();
      paint();
      flush();
    }
    function submit() {
      if (!live) {
        exportJSON();
        return;
      }
      const waiting = store.entries();
      const fresh = store.drafts().filter((m) => !queued(waiting, m));
      if (!fresh.length) {
        if (waiting.length) flush();
        else showSync("info", "No marks yet. Toggle Review, click a section, leave a note.");
        return;
      }
      const stamps = /* @__PURE__ */ Object.create(null);
      fresh.forEach((m) => {
        stamps[m.anchor] = m.at;
      });
      queue("submit", (id, r) => submitBody(id, r, fresh), stamps, submit);
    }
    function accept(a) {
      queue("accept", (id, r) => acceptBody(id, r, a), {}, () => accept(a));
    }
    function reopen(a, note) {
      const c = (committed[a] || []).filter((x) => x.label || x.quote)[0];
      const item = c ? { anchor: a, section: c.section, label: c.label, quote: c.quote, region: regionOf(a) } : { anchor: a, region: regionOf(a) };
      queue("reopen", (id, r) => reopenBody(id, r, item, note), {}, () => reopen(a, note));
    }
    function flush() {
      if (!transport || destroyed) return;
      if (flushing) {
        again = true;
        return;
      }
      if (!store.entries().length) return;
      flushing = true;
      inflight++;
      paintSubmit();
      showSync("pending", "Sending…");
      deliver(store, transport, report, () => destroyed).finally(() => {
        flushing = false;
        inflight--;
        paintSubmit();
        if (again && !destroyed) {
          again = false;
          flush();
        }
        idle();
      });
    }
    function report(e, r) {
      if (destroyed) return;
      if (r.state === "owned") Object.keys(e.stamps).forEach((a) => {
        if (DRAWING.test(a)) ownedDrawings.add(a);
      });
      paint();
      setOffline(r.state === "offline");
      const verb = e.act === "accept" ? "Accepted" : e.act === "reopen" ? "Reopened" : "Sent";
      if (r.state === "owned") reportSync(syncMessage(verb, r));
      else if (r.state === "refused") showSync("err", "Not saved — the server refused it (" + r.message + ")." + (e.act === "submit" ? " Your marks are kept: Submit sends them as a new review." : ""));
      else if (r.state === "failed") showSync("err", "Not sent (" + r.message + "). " + (e.act === "submit" ? "Your marks are kept." : "It stays queued in this browser."), [{ label: "Retry", fn: flush }]);
      else showSync("warn", "Review server offline — your feedback is queued in this browser and sends when the server is back.");
    }
    function approve() {
      const send = opts.approve;
      showSync("ask", "Approve this plan? This stamps it approved and closes the review loop.", [
        { label: "Approve", fn: () => {
          showSync("pending", "Approving…");
          inflight++;
          Promise.resolve().then(send).catch((err) => ({ state: "failed", message: err instanceof Error ? err.message : String(err) })).then((r) => {
            inflight--;
            if (!destroyed) {
              setOffline(r.state === "offline");
              if (r.state === "owned") {
                const m = syncMessage("Approved", r);
                m.text += " — you can close this tab.";
                reportSync(m);
              } else if (r.state === "offline") showSync("err", "The review server is offline — Approve can’t be sent right now.");
              else showSync("err", "Not approved (" + r.message + ").", [{ label: "Retry", fn: approve }]);
            }
            idle();
          });
        } },
        { label: "Cancel", fn: hideSync }
      ]);
    }
    function exportJSON() {
      const items = store.drafts().map(wireItem);
      if (!items.length) {
        showSync("info", "No marks yet. Toggle Review, click a section, leave a note.");
        return;
      }
      if (!who()) {
        askName("Your name, so teammates know whose feedback this is:", exportJSON);
        return;
      }
      const json = JSON.stringify(Object.assign({ id: store.exportId(items), slug, reviewer: who() }, roundItems(items)), null, 2);
      const blob = new Blob([json], { type: "application/json" });
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = slug + "-feedback.json";
      a.click();
      URL.revokeObjectURL(url);
      if (navigator.clipboard) navigator.clipboard.writeText(json).catch(() => {
      });
      showSync("ok", "Saved " + slug + "-feedback.json (and copied to clipboard). Hand it to the agent, or run: ox plan feedback apply " + slug + " --from " + slug + "-feedback.json");
    }
    let syncEl = null, syncTimer = null;
    function hideSync() {
      if (syncTimer) {
        clearTimeout(syncTimer);
        syncTimer = null;
      }
      if (syncEl) {
        syncEl.remove();
        syncEl = null;
      }
    }
    function showSync(kind, text2, actions, input) {
      hideSync();
      const el = syncEl = document.createElement("div");
      el.className = "rev-sync " + kind;
      el.setAttribute("role", kind === "err" ? "alert" : "status");
      const msg = document.createElement("span");
      msg.className = "rev-sync-msg";
      msg.textContent = text2;
      el.appendChild(msg);
      let field = null;
      if (input !== void 0) {
        field = document.createElement("input");
        field.className = "rev-sync-input";
        field.value = input;
        el.appendChild(field);
      }
      (actions || []).forEach((a) => {
        const b = document.createElement("button");
        b.className = "rev-sync-act";
        b.textContent = a.label;
        b.onclick = () => {
          a.fn(field ? field.value.trim() : void 0);
        };
        el.appendChild(b);
      });
      if (kind !== "pending" && kind !== "ask") {
        const x = document.createElement("button");
        x.className = "rev-sync-x";
        x.textContent = "×";
        x.title = "Dismiss";
        x.onclick = hideSync;
        el.appendChild(x);
      }
      body.appendChild(el);
      if (field) {
        const f = field;
        f.focus();
        f.onkeydown = (e) => {
          if (e.key === "Enter" && actions && actions[0]) {
            e.preventDefault();
            actions[0].fn(f.value.trim());
          }
        };
      }
      const ttl = { ok: 1e4, info: 8e3, warn: 2e4 }[kind];
      if (ttl) syncTimer = setTimeout(() => {
        if (syncEl === el) hideSync();
      }, ttl);
    }
    function reportSync(m) {
      showSync(m.kind, m.text);
      try {
        sessionStorage.setItem(SYNC_KEY, JSON.stringify({ kind: m.kind, text: m.text, at: Date.now() }));
      } catch (e) {
      }
    }
    function restoreSync() {
      try {
        const s = JSON.parse(sessionStorage.getItem(SYNC_KEY) || "null");
        sessionStorage.removeItem(SYNC_KEY);
        if (s && Date.now() - s.at < 3e4) showSync(s.kind, s.text);
      } catch (e) {
      }
    }
    function askName(q, then) {
      showSync("ask", q, [
        { label: "Save", fn: (n) => {
          if (!n) return;
          named = n;
          try {
            localStorage.setItem("ox-plan-reviewer", named);
          } catch (e) {
          }
          hideSync();
          updateWho();
          paint();
          if (then) then();
        } },
        { label: "Cancel", fn: hideSync }
      ], named);
    }
    let connEl = null, offlineBar = null, toastEl = null;
    function paintConn() {
      if (!connEl) return;
      connEl.textContent = offline ? "● offline" : "● live";
      connEl.className = "rev-conn " + (offline ? "off" : "ok");
      connEl.title = offline ? "Review server unreachable — feedback is NOT reaching it" : "Connected to the review server";
    }
    function setOffline(down) {
      if (offline === down || destroyed) return;
      offline = down;
      body.classList.toggle("rev-offline", down);
      paintConn();
      if (offlineBar) {
        offlineBar.remove();
        offlineBar = null;
      }
      if (!down) {
        flush();
        return;
      }
      const n = Object.keys(ds).length;
      const cmd = opts.restartCommand;
      const bar2 = offlineBar = document.createElement("div");
      bar2.className = "rev-offline-bar";
      bar2.innerHTML = "<strong>⚠ Review server offline</strong> — feedback is NOT reaching it. " + (n ? n + " unsent mark(s) are" : "Your marks are") + " kept in this browser and sent on reconnect." + (cmd ? " Restart: <code>" + esc(cmd) + '</code> <button class="rev-offline-copy">copy</button>' : "");
      body.appendChild(bar2);
      const copy = bar2.querySelector(".rev-offline-copy");
      if (copy) copy.onclick = () => {
        if (navigator.clipboard) navigator.clipboard.writeText(cmd || "").then(() => {
          copy.textContent = "copied ✓";
        }).catch(() => {
        });
      };
    }
    function toast(msg) {
      if (toastEl) toastEl.remove();
      const el = toastEl = document.createElement("div");
      el.className = "rev-toast";
      el.textContent = msg;
      body.appendChild(el);
      setTimeout(() => {
        el.remove();
        if (toastEl === el) toastEl = null;
      }, 8e3);
    }
    function idle() {
      if (waiter && !inflight && !pop) {
        const fn = waiter;
        waiter = null;
        fn();
      }
    }
    if (opts.frame) {
      body.classList.add("rev-framed");
      place();
      window.addEventListener("resize", place, { signal });
      document.addEventListener("scroll", place, { capture: true, passive: true, signal });
    }
    const resized = opts.frame && typeof ResizeObserver !== "undefined" ? new ResizeObserver(place) : null;
    if (resized && opts.frame) resized.observe(opts.frame);
    const bar = document.createElement("div");
    bar.className = "rev-bar";
    bar.innerHTML = (live ? '<span class="rev-conn"></span>' : "") + (opts.presence && revision ? '<span class="rev-here" hidden></span>' : "") + '<button class="rev-toggle" title="Toggle review mode">Review</button>' + (revision ? '<button class="rev-draw" aria-pressed="false" title="Drag to draw an oval around part of the plan to comment on it">Draw</button>' : "") + '<span class="rev-count"></span>' + (live && !opts.reviewer ? '<button class="rev-who" title="Set the name teammates see on your comments"></button>' : "") + '<button class="rev-submit" title="Send feedback to the agent">' + (live ? "Submit" : "Export") + "</button>" + (opts.approve ? '<button class="rev-approve" title="Approve and close the loop">Approve</button>' : "");
    body.appendChild(bar);
    let drag = null;
    bar.addEventListener("pointerdown", (e) => {
      if (drag || !e.isPrimary || e.button !== 0 || e.target.closest("button")) return;
      const r = bar.getBoundingClientRect();
      drag = { x: e.clientX - r.left, y: e.clientY - r.top };
      bar.setPointerCapture(e.pointerId);
      bar.classList.add("rev-dragging");
      if (hintBubble) {
        hintBubble.remove();
        hintBubble = null;
      }
      e.preventDefault();
    }, { signal });
    bar.addEventListener("pointermove", (e) => {
      if (!drag || !e.isPrimary) return;
      const b = box2();
      bar.style.left = Math.max(0, b.left, Math.min(e.clientX - drag.x, Math.min(b.right, window.innerWidth) - bar.offsetWidth)) + "px";
      bar.style.top = Math.max(0, b.top, Math.min(e.clientY - drag.y, Math.min(b.bottom, window.innerHeight) - bar.offsetHeight)) + "px";
      bar.style.bottom = "auto";
    }, { signal });
    bar.addEventListener("lostpointercapture", () => {
      drag = null;
      bar.classList.remove("rev-dragging");
    }, { signal });
    const countEl = bar.querySelector(".rev-count");
    connEl = bar.querySelector(".rev-conn");
    paintConn();
    if (opts.presence && revision) presence = startPresence(port, opts.presence, revision, bar.querySelector(".rev-here"), opts.frame);
    const whoEl = bar.querySelector(".rev-who");
    function updateWho() {
      if (whoEl) whoEl.textContent = named ? "You: " + named : "Set name";
    }
    updateWho();
    if (whoEl) whoEl.onclick = () => {
      askName("Your name (shown to teammates on this plan):");
    };
    const submitEl = bar.querySelector(".rev-submit");
    function paintSubmit() {
      if (!live || destroyed) return;
      submitEl.disabled = flushing;
      submitEl.textContent = flushing ? "Sending…" : "Submit";
    }
    const toggleEl = bar.querySelector(".rev-toggle");
    const drawEl = bar.querySelector(".rev-draw");
    let hintBubble = null;
    let drawTaught = false;
    function setTool(next) {
      if (next && !on) setReview(true, true);
      tool = next;
      if (drawEl) {
        drawEl.classList.toggle("on", tool);
        drawEl.setAttribute("aria-pressed", String(tool));
      }
      sendMode();
      if (tool && !drawTaught) {
        drawTaught = true;
        toast("Drag to draw an oval. Drag its outline to move it, or its handles to resize it before submitting. Esc puts the pen down.");
      }
    }
    function setReview(next, silent) {
      on = next;
      toggleEl.classList.toggle("on", on);
      body.classList.toggle("rev-on", on);
      if (!on && tool) {
        tool = false;
        if (drawEl) {
          drawEl.classList.remove("on");
          drawEl.setAttribute("aria-pressed", "false");
        }
      }
      sendMode();
      toggleEl.textContent = on ? "Exit review" : "Review";
      toggleEl.title = on ? "Leave review mode (Esc)" : "Enter review mode (r)";
      if (on && !silent) toast("Review mode — click any section, item, or row to mark it up, or highlight text to comment on it. Esc or Exit review to leave.");
      if (!on) {
        closePop();
        if (toastEl) {
          toastEl.remove();
          toastEl = null;
        }
      }
      try {
        localStorage.setItem("ox-plan-rev-seen", "1");
      } catch (e) {
      }
      try {
        if (on) sessionStorage.setItem(ON_KEY, "1");
        else sessionStorage.removeItem(ON_KEY);
      } catch (e) {
      }
      if (hintBubble) {
        hintBubble.remove();
        hintBubble = null;
      }
      renderRail();
    }
    toggleEl.onclick = () => {
      setReview(!on);
    };
    if (drawEl) drawEl.onclick = () => {
      setTool(!tool);
    };
    submitEl.onclick = submit;
    if (opts.approve) bar.querySelector(".rev-approve").onclick = approve;
    function escape() {
      if (original) {
        closeOriginal();
        return true;
      }
      if (pop) {
        closePop();
        return true;
      }
      if (tool) {
        setTool(false);
        return true;
      }
      if (on) {
        setReview(false);
        return true;
      }
      return false;
    }
    document.addEventListener("keydown", (e) => {
      if (e.defaultPrevented) return;
      if (e.key === "Escape") {
        if (escape()) e.preventDefault();
        return;
      }
      if (e.key === "r" && !typing(e) && !e.metaKey && !e.ctrlKey && !e.altKey) {
        setReview(!on);
        e.preventDefault();
      }
    }, { signal });
    try {
      if (!opts.reviewing && !localStorage.getItem("ox-plan-rev-seen")) {
        const hint = hintBubble = document.createElement("div");
        hint.className = "rev-hint";
        hint.textContent = "↓ Click Review to mark up this plan";
        hint.style.left = toggleEl.getBoundingClientRect().left + "px";
        body.appendChild(hint);
        setTimeout(() => {
          if (hintBubble === hint) {
            hint.remove();
            hintBubble = null;
          }
        }, 9e3);
      }
    } catch (e) {
    }
    let repaint;
    window.addEventListener("storage", (e) => {
      if (e.key === null || e.key.indexOf(store.prefix) === 0) {
        clearTimeout(repaint);
        repaint = setTimeout(paint, 50);
      }
    }, { signal });
    window.addEventListener("online", flush, { signal });
    let started = false;
    port.listen((m) => {
      switch (m.type) {
        case "ready":
          paint();
          sendMode();
          if (presence) presence.ready();
          if (started) return;
          started = true;
          try {
            if (!on && sessionStorage.getItem(ON_KEY)) setReview(true, true);
          } catch (e) {
          }
          break;
        case "placed":
          onPlaced(m.items);
          break;
        case "target":
          if (!on || held()) break;
          openPop({ a: m.target.anchor, section: m.target.section, label: m.target.label, quote: m.target.quote }, toHost(m.target.rect));
          break;
        case "capture": {
          if (!on || !tool) break;
          const anchor = m.anchor;
          if (anchor) {
            if (committed[anchor]?.length || ownedDrawings.has(anchor) || store.entries().some((e) => e.stamps[anchor] !== void 0)) break;
            const old = pending && "q-" + pending.id === anchor ? pending : ds[anchor]?.region;
            if (!old) break;
            const r2 = { ...m.capture, id: old.id, plan_revision: revision, created_at: old.created_at };
            if (checkRegion(r2)) break;
            const label = drawingLabel(r2), section = r2.targets[0].section;
            if (pending && pending.id === r2.id) pending = r2;
            else if (!store.save({ ...ds[anchor], region: r2, label, section })) toast("This browser could not save the updated drawing. It is kept in this tab only.");
            if (pop && popAbout?.a === anchor) {
              popAbout.label = label;
              popAbout.section = section;
              const about = pop.querySelector(".rev-about");
              if (about) about.textContent = label;
              placePop(toHost(m.rect));
            }
            paint();
            break;
          }
          if (held()) {
            toast("Save or delete the open note, then draw again.");
            break;
          }
          const r = { ...m.capture, id: newId(), plan_revision: revision, created_at: (/* @__PURE__ */ new Date()).toISOString() };
          const why = checkRegion(r);
          if (why) {
            toast("That drawing cannot be kept: its " + why + " is out of bounds.");
            break;
          }
          closePop(true);
          pending = r;
          paint();
          openPop({ a: "q-" + r.id, section: r.targets[0].section, label: drawingLabel(r) }, toHost(m.rect), true);
          break;
        }
        case "refused":
          if (m.reason === "cancelled") break;
          if (!held() && !pending) closePop();
          if (m.reason === "repeated") toast("“" + clip(m.quote, 40) + "” appears " + m.count + " times in this section — highlight a longer phrase so the comment points at one place.");
          else toast(DRAW_REFUSED[m.reason] || "Highlight text within one section to comment on it.");
          break;
        case "dismiss":
          if ((on || pop) && !held()) closePop();
          break;
        case "key":
          if (held()) break;
          if (m.key === "Escape") escape();
          else setReview(!on);
          break;
        case "pointer":
          if (presence) presence.pointer(m.at);
          break;
        case "located":
          if (presence) presence.located(m.points);
          break;
      }
    });
    restoreSync();
    if (opts.reviewing) {
      let seen = true;
      try {
        seen = !!localStorage.getItem("ox-plan-rev-seen");
      } catch (e) {
      }
      setReview(true, seen);
    }
    paint();
    const restored = Object.keys(ds).length;
    if (live && restored && !store.entries().length) toast(restored + " unsent mark(s) restored — Submit to send them.");
    flush();
    return {
      update(items, n) {
        setCommitted(items);
        problems = n || 0;
        paint();
      },
      setOffline,
      setReviewing(next) {
        if (!destroyed && on !== next) setReview(next);
      },
      whenIdle(fn) {
        waiter = fn;
        idle();
      },
      destroy() {
        if (destroyed) return;
        waiter = null;
        closePop();
        destroyed = true;
        off.abort();
        clearTimeout(repaint);
        if (presence) presence.leave();
        port.listen(() => {
        });
        port.send({ type: "mode", on: false, open: false, draw: false });
        port.send({ type: "paint", items: [], drawings: [] });
        if (resized) resized.disconnect();
        [bar, rail, orphanBar, syncEl, toastEl, offlineBar, hintBubble, original].forEach((el) => {
          if (el) el.remove();
        });
        body.classList.remove("rev-on", "rev-offline", "rev-framed");
        ["top", "left", "right", "bottom", "width", "height"].forEach((k) => body.style.removeProperty("--rev-" + k));
      }
    };
  }

  // src/local.ts
  var ACCOUNT = "local";
  (function() {
    let storage;
    try {
      storage = window.localStorage;
      if (!storage) return;
    } catch (e) {
      return;
    }
    const body = document.body;
    const slug = (body.getAttribute("data-slug") || document.title || "plan").trim();
    const plan = body.getAttribute("data-review-plan") || slug;
    const base = body.getAttribute("data-review-endpoint") || "";
    const token = body.getAttribute("data-review-token") || "";
    let committed = [];
    try {
      const el = document.getElementById("ox-review-state");
      const v = el ? JSON.parse(el.textContent || "[]") : [];
      if (Array.isArray(v)) committed = v.filter((x) => x && typeof x.anchor === "string");
    } catch (e) {
      committed = [];
    }
    migrateLegacy(storage, slug, openStore(storage, ACCOUNT, plan));
    let probe = null;
    function reload() {
      controls.whenIdle(() => location.reload());
    }
    function watch() {
      if (probe) return;
      probe = setInterval(() => {
        fetch(base + "/healthz", { cache: "no-store" }).then((r) => r.ok ? r.json() : null).then((j) => {
          if (j && j.app === "ox-plan-review") back();
        }).catch(() => {
        });
      }, 3e3);
    }
    function back() {
      if (!probe) return;
      clearInterval(probe);
      probe = null;
      controls.setOffline(false);
      reload();
    }
    function post(path, payload) {
      return fetch(base + path, { method: "POST", headers: { "Content-Type": "application/json", "X-Review-Token": token }, body: payload }).catch(() => {
        watch();
        return null;
      });
    }
    async function failure(r) {
      const t = (await r.text().catch(() => "")).trim();
      return "HTTP " + r.status + (t ? " — " + clip(t, 120) : "");
    }
    async function send(op) {
      const r = await post(op.kind === "round" ? "/feedback" : "/accept", op.body);
      if (!r) return { state: "offline" };
      if (!r.ok) {
        const message = await failure(r);
        if (r.status === 400 || r.status === 409 || r.status === 413) return { state: "refused", message };
        return { state: "failed", message };
      }
      const d = await r.json().catch(() => ({}));
      if (d.saved !== true) return { state: "failed", message: "the server did not say it stored this" };
      return {
        state: "owned",
        id: op.kind === "round" ? d.round_id : d.resolution_id,
        synced: !!d.pushed,
        // a duplicate is a resend of an operation whose first save already notified (or tried to)
        warning: d.notified === false && !d.duplicate ? "the plan’s authoring coworker was not notified automatically — tell them directly" : void 0
      };
    }
    async function approve() {
      const r = await post("/approve", "{}");
      if (!r) return { state: "offline" };
      if (!r.ok) return { state: "failed", message: await failure(r) };
      const d = await r.json().catch(() => ({}));
      return { state: "owned", synced: !!d.pushed };
    }
    const revision = body.getAttribute("data-review-revision") || "";
    const key = newId();
    let es = null;
    if (base && window.EventSource) {
      try {
        es = new EventSource(base + "/events?t=" + encodeURIComponent(token) + (revision ? "&rev=" + encodeURIComponent(revision) + "&k=" + encodeURIComponent(key) : ""));
      } catch (e) {
      }
    }
    const stream = es;
    const presence = stream && revision ? {
      join(_shown, on) {
        stream.addEventListener("presence", (e) => {
          try {
            on(JSON.parse(e.data));
          } catch (err) {
          }
        });
        stream.addEventListener("error", () => on({ type: "lost" }));
        return {
          // a pointer that cannot reach the server is dropped: the stream's
          // own error already says the server is gone
          move(at) {
            return fetch(base + "/presence", { method: "POST", headers: { "Content-Type": "application/json", "X-Review-Token": token }, body: JSON.stringify({ key, at }) }).catch(() => {
            });
          },
          leave() {
          }
        };
      }
    } : void 0;
    const ports = localPorts();
    const controls = mountHostControls(ports.host, {
      account: ACCOUNT,
      plan,
      slug,
      committed,
      problems: Number(body.getAttribute("data-review-problems")) || 0,
      transport: base ? { send } : void 0,
      resolve: true,
      // any reviewer of a local review may Accept or Reopen
      approve: base ? approve : void 0,
      restartCommand: "ox plan review " + slug,
      // the revision ox rendered this page from; a page without one offers no Draw
      revision: revision || void 0,
      presence
    });
    startDocumentTools(ports.document);
    if (!base) return;
    if (es) {
      es.onopen = back;
      es.onerror = () => {
        controls.setOffline(true);
        watch();
      };
      es.onmessage = reload;
    }
    if ("serviceWorker" in navigator) {
      try {
        navigator.serviceWorker.register("/sw.js").catch(() => {
        });
      } catch (e) {
      }
    }
  })();
})();
