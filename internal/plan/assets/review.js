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

  // src/protocol.ts
  var MAX_TEXT = 65536;
  var ANCHOR = /^[hq][0-9a-f]{8}$/;
  var STATUSES = ["approve", "request-change", "flag", "comment"];
  var STATES = ["open", "addressed", "verified", "wontfix"];
  var TINTS = ["sage", "amber", "red", "gold", "faint"];
  function obj(v) {
    return typeof v === "object" && v !== null && !Array.isArray(v);
  }
  function text(v) {
    return typeof v === "string" && v.length <= MAX_TEXT;
  }
  function optText(v) {
    return v === void 0 || text(v);
  }
  function oneOf(v, list) {
    return typeof v === "string" && list.indexOf(v) >= 0;
  }
  function paintItem(it) {
    if (!obj(it) || typeof it.anchor !== "string" || !ANCHOR.test(it.anchor)) return null;
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
  var CHROME = ".rev-bar, .rev-rail, .rev-orphans, .rev-toast, .rev-sync, .rev-offline-bar";
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
    let qRanges = /* @__PURE__ */ Object.create(null);
    let spots = [];
    let picked = null;
    let focused = null;
    let started = false;
    function isRange(at) {
      return "startContainer" in at;
    }
    function paint(next) {
      items = next;
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
      document.querySelectorAll(SELECTOR).forEach((el) => {
        const a = anchorFor(el);
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
      const pos = found.map((s) => {
        if (isRange(s.at)) return s.at;
        const r = document.createRange();
        r.selectNode(s.at);
        return r;
      });
      const order = found.map((_, i) => i).sort((x, y) => pos[x].compareBoundaryPoints(Range.START_TO_START, pos[y]));
      spots = order.map((i) => found[i]);
      port.send({ type: "placed", items: spots.map((s) => s.p) });
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
      const words = q.split(/\s+/).map((w) => w.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"));
      const re = new RegExp((/^\w/.test(q) ? "(^|\\W)" : "()") + words.join("\\s+") + (/\w$/.test(q) ? "(?!\\w)" : ""), "gi");
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
      const box = range.getBoundingClientRect();
      return { anchor: quoteAnchor(section, quote), section, label: clip(quote, 70), quote, range: rangeAt(idx, s, e), rect: { left: box.left, bottom: box.bottom } };
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
      if (el0.closest && el0.closest(CHROME + ", .rev-pop")) return;
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
          break;
        case "paint":
          paint(m.items);
          break;
        case "focus": {
          const h = m.anchor === null ? null : picked && picked.a === m.anchor ? picked.range : qRanges[m.anchor] ? qRanges[m.anchor].range : null;
          focusRange(h);
          break;
        }
        case "reveal":
          reveal(m.anchor, m.nth);
          break;
      }
    });
    whenDiagramsDrawn(() => {
      started = true;
      port.send({ type: "ready" });
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

  // src/state.ts
  var ACTS = ["submit", "reopen", "accept"];
  var DRAFT = "draft:";
  var OP = "op:";
  var EXPORT = "export";
  var enc = encodeURIComponent;
  var has = (o, k) => Object.prototype.hasOwnProperty.call(o, k);
  function obj2(v) {
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
    if (!obj2(v) || typeof v.anchor !== "string" || STATUSES.indexOf(v.status) < 0 || typeof v.at !== "number") return null;
    const m = { anchor: v.anchor, status: v.status, at: v.at };
    ["section", "label", "note", "quote"].forEach((k) => {
      if (typeof v[k] === "string") m[k] = v[k];
    });
    return m;
  }
  function asEntry(v) {
    if (!obj2(v) || typeof v.id !== "string" || ACTS.indexOf(v.act) < 0 || typeof v.body !== "string" || typeof v.at !== "number" || !obj2(v.stamps)) return null;
    const stamps = /* @__PURE__ */ Object.create(null);
    Object.keys(v.stamps).forEach((a) => {
      const s = v.stamps[a];
      if (typeof s === "number") stamps[a] = s;
    });
    return { id: v.id, act: v.act, body: v.body, at: v.at, stamps };
  }
  function openStore(storage, account, plan) {
    const prefix = "ox-review:" + enc(account) + ":" + enc(plan) + ":";
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
        const id = obj2(prev) && typeof prev.id === "string" && JSON.stringify(prev.items) === JSON.stringify(items) ? prev.id : newId();
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
    return o;
  }
  function request(id, reviewer, rest) {
    const o = { id };
    if (reviewer) o.reviewer = reviewer;
    return JSON.stringify(Object.assign(o, rest));
  }
  function submitBody(id, reviewer, marks) {
    return request(id, reviewer, { items: marks.map(wireItem) });
  }
  function reopenBody(id, reviewer, item, note) {
    return request(id, reviewer, { items: [wireItem({ ...item, status: "request-change", note: note || "reopened by reviewer" })] });
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
    const marks = read(storage, KEY), box = read(storage, KEY + ":outbox"), exp = read(storage, KEY + ":export");
    if (marks === null && box === null && exp === null) return;
    let ok = true;
    const current = store.drafts();
    if (obj2(marks)) {
      Object.keys(marks).forEach((a) => {
        const v = marks[a];
        const m = asMark(obj2(v) ? { ...v, anchor: a, at: Math.max(+v.updated_at || 0, 1) } : null);
        if (!m || current.some((d) => d.anchor === a && d.at >= m.at)) return;
        ok = write(storage, store.prefix + DRAFT + enc(a), m) && ok;
      });
    }
    if (obj2(box) && typeof box.id === "string" && Array.isArray(box.items)) {
      const stamps = /* @__PURE__ */ Object.create(null);
      const s = obj2(box.stamps) ? box.stamps : {};
      box.items.forEach((it) => {
        if (obj2(it) && typeof it.anchor === "string") stamps[it.anchor] = Math.max(+s[it.anchor] || 0, 1);
      });
      const body = JSON.stringify({ id: box.id, slug, reviewer: box.reviewer, items: box.items });
      const e = { id: box.id, act: "submit", body, at: Date.parse(box.created_at) || Date.now(), stamps };
      ok = write(storage, store.prefix + OP + enc(e.id), e) && ok;
    }
    if (obj2(exp)) ok = write(storage, store.prefix + EXPORT, exp) && ok;
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
  var WHOLE = { a: "plan", section: "", label: "Whole plan" };
  var BUBBLE = '<svg width="14" height="14" viewBox="0 0 16 16" aria-hidden="true"><path d="M3 2.5h10A1.5 1.5 0 0 1 14.5 4v6a1.5 1.5 0 0 1-1.5 1.5H7.5L4 14.5v-3H3A1.5 1.5 0 0 1 1.5 10V4A1.5 1.5 0 0 1 3 2.5z" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round"/></svg>';
  function esc(s) {
    return (s || "").replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;").replace(/'/g, "&#39;");
  }
  var STATE_TAG = { addressed: "addressed", verified: "verified", wontfix: "wontfix" };
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
        if (typeof x.anchor !== "string" || STATUSES.indexOf(x.status) < 0 || STATES.indexOf(x.state) < 0) {
          invalid++;
          return;
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
    function committedState(a) {
      const arr = committed[a] || [];
      return arr.length ? repOf(arr).state : "";
    }
    function paintItems() {
      const out = [];
      Object.keys(committed).concat(Object.keys(ds)).forEach((a, i, all) => {
        if (all.indexOf(a) !== i || !/^[hq][0-9a-f]{8}$/.test(a)) return;
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
            tint: c.state === "addressed" || c.state === "verified" ? "sage" : c.state === "wontfix" ? "faint" : "amber"
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
    function paint() {
      if (destroyed) return;
      place();
      ds = /* @__PURE__ */ Object.create(null);
      store.drafts().forEach((m) => {
        ds[m.anchor] = m;
      });
      const n = Object.keys(ds).length;
      countEl.textContent = n ? n + " unsent" : "";
      port.send({ type: "paint", items: paintItems() });
    }
    function onPlaced(items) {
      const seen = /* @__PURE__ */ Object.create(null);
      seen[WHOLE.a] = true;
      placed = items.filter((p) => !seen[p.anchor] && (seen[p.anchor] = true));
      unplaced = Object.keys(committed).concat(Object.keys(ds)).filter((a, i, all) => !seen[a] && all.indexOf(a) === i);
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
            answer: c.state !== "open" && c.resolutionNote ? c.resolutionNote : ""
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
            answer: ""
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
        r.innerHTML = '<div class="rev-rail-h">Comments' + hide + "</div>" + plan + '<div class="rev-rail-empty">Click any section, risk, or row — or highlight text — to leave a comment.</div>';
        return;
      }
      let html = '<div class="rev-rail-h">Comments <span class="rev-rail-n">' + rows.length + "</span>" + hide + "</div>" + plan + '<ul class="rev-rail-list">';
      rows.forEach((row, i) => {
        const whole = row.a === WHOLE.a;
        let tag = "";
        if (row.tag) tag = '<span class="rev-rail-tag ' + row.tag + '">' + row.tag + "</span>";
        else if (STATE_TAG[row.state]) tag = '<span class="rev-rail-tag ' + STATE_TAG[row.state] + '">' + STATE_TAG[row.state] + "</span>";
        if (!row.at && !whole) tag += '<span class="rev-rail-tag">text changed</span>';
        const content = (row.quote ? '<div class="rev-rail-quote">' + esc(row.label) + "</div>" : "") + (row.note ? '<div class="rev-rail-note">' + esc(row.note) + "</div>" : row.quote || whole ? "" : '<div class="rev-rail-note muted">' + esc(row.label) + "</div>") + (row.answer ? '<div class="rev-rail-answer">↳ ' + esc(row.answer) + "</div>" : "");
        const whoTag = row.reviewer ? '<span class="rev-rail-who">' + esc(row.reviewer) + "</span>" : "";
        html += '<li class="rev-rail-item" data-i="' + i + '" data-status="' + esc(row.status) + '"><span class="rev-rail-glyph">' + glyphFor(row.status) + '</span><div class="rev-rail-body"><div class="rev-rail-sec"><span class="rev-rail-sec-t">' + esc(whole ? WHOLE.label : row.section || "(plan)") + "</span>" + whoTag + tag + "</div>" + content + "</div></li>";
      });
      html += "</ul>";
      r.innerHTML = html;
      r.querySelectorAll(".rev-rail-item").forEach((li) => {
        li.onclick = (ev) => {
          const row = rows[+li.getAttribute("data-i")];
          if (!row) return;
          if (!row.at) {
            openPop({ a: row.a, section: row.section, label: row.label, quote: row.quote }, { left: ev.clientX, bottom: ev.clientY });
            ev.stopPropagation();
            return;
          }
          port.send({ type: "reveal", anchor: row.at.anchor, nth: row.at.nth });
        };
      });
    }
    let pop = null;
    let popNote = "";
    function sendMode() {
      port.send({ type: "mode", on, open: !!pop });
    }
    function dirty() {
      const n = pop ? pop.querySelector(".rev-note") : null;
      return !!n && n.value.trim() !== popNote;
    }
    function closePop(replacing) {
      if (pop) {
        pop.remove();
        pop = null;
        sendMode();
      }
      port.send({ type: "focus", anchor: null });
      if (!replacing) idle();
    }
    function openPop(t, rect, fresh) {
      closePop(true);
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
        const kept = store.save(m);
        paint();
        closePop();
        if (!kept) showSync("err", "This browser could not save your note: it lives in this tab only and is lost when the tab closes. Free up browser storage, then save it again.");
      };
      p.querySelector(".rev-del").onclick = () => {
        store.remove(a);
        paint();
        closePop();
      };
    }
    function box() {
      return opts.frame ? opts.frame.getBoundingClientRect() : { left: 0, top: 0, right: window.innerWidth, bottom: window.innerHeight };
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
      const item = c ? { anchor: a, section: c.section, label: c.label, quote: c.quote } : { anchor: a };
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
      const json = JSON.stringify({ id: store.exportId(items), slug, reviewer: who(), items }, null, 2);
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
    bar.innerHTML = (live ? '<span class="rev-conn"></span>' : "") + '<button class="rev-toggle" title="Toggle review mode">Review</button><span class="rev-count"></span>' + (live && !opts.reviewer ? '<button class="rev-who" title="Set the name teammates see on your comments"></button>' : "") + '<button class="rev-submit" title="Send feedback to the agent">' + (live ? "Submit" : "Export") + "</button>" + (opts.approve ? '<button class="rev-approve" title="Approve and close the loop">Approve</button>' : "");
    body.appendChild(bar);
    const countEl = bar.querySelector(".rev-count");
    connEl = bar.querySelector(".rev-conn");
    paintConn();
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
    let hintBubble = null;
    function setReview(next, silent) {
      on = next;
      toggleEl.classList.toggle("on", on);
      body.classList.toggle("rev-on", on);
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
    submitEl.onclick = submit;
    if (opts.approve) bar.querySelector(".rev-approve").onclick = approve;
    function escape() {
      if (pop) {
        closePop();
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
        case "target": {
          if (!on || dirty()) break;
          const b = box();
          openPop({ a: m.target.anchor, section: m.target.section, label: m.target.label, quote: m.target.quote }, {
            left: Math.min(Math.max(m.target.rect.left + b.left, b.left), Math.max(b.left, b.right - 300)),
            bottom: Math.min(Math.max(m.target.rect.bottom + b.top, b.top), Math.max(b.top, b.bottom - 200))
          });
          break;
        }
        case "refused":
          if (!dirty()) closePop();
          toast(m.reason === "repeated" ? "“" + clip(m.quote, 40) + "” appears " + m.count + " times in this section — highlight a longer phrase so the comment points at one place." : "Highlight text within one section to comment on it.");
          break;
        case "dismiss":
          if ((on || pop) && !dirty()) closePop();
          break;
        case "key":
          if (dirty()) break;
          if (m.key === "Escape") escape();
          else setReview(!on);
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
        port.listen(() => {
        });
        port.send({ type: "mode", on: false, open: false });
        port.send({ type: "paint", items: [] });
        if (resized) resized.disconnect();
        [bar, rail, orphanBar, syncEl, toastEl, offlineBar, hintBubble].forEach((el) => {
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
      restartCommand: "ox plan review " + slug
    });
    startDocumentTools(ports.document);
    if (!base) return;
    if (window.EventSource) {
      try {
        const es = new EventSource(base + "/events?t=" + encodeURIComponent(token));
        es.onopen = back;
        es.onerror = () => {
          controls.setOffline(true);
          watch();
        };
        es.onmessage = reload;
      } catch (e) {
      }
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
