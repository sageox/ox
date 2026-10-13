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
  function contentAnchor(heading, text) {
    return "h" + fnv1a(norm(heading) + "\0" + norm(text));
  }
  function quoteAnchor(section, quote) {
    return "q" + fnv1a(norm(section) + "\0" + norm(quote));
  }
  function labelOf(text) {
    return clip((text || "").replace(/\s+/g, " ").trim(), 70);
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
    return h ? h.textContent || "" : sec.id || "";
  }
  function anchorText(el) {
    const clone = el.cloneNode(true);
    clone.querySelectorAll(".rev-glyph").forEach((g) => g.remove());
    return clone.textContent || "";
  }
  function anchorFor(el) {
    return contentAnchor(headingOf(el), anchorText(el));
  }
  function labelFor(el) {
    return labelOf(anchorText(el));
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
  var TINTS = ["sage", "amber", "red", "gold", "faint"];
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
      TINTS.forEach((c) => {
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
      let text = "", prev = null, brk = false;
      const w = document.createTreeWalker(body, NodeFilter.SHOW_TEXT | NodeFilter.SHOW_ELEMENT);
      for (let n = w.nextNode(); n; n = w.nextNode()) {
        if (n.nodeType === 1) {
          if (!INLINE.test(n.tagName)) brk = true;
          continue;
        }
        const t = n;
        if (!t.parentElement || t.parentElement.closest(NOT_TEXT)) continue;
        if (prev && (brk || !inlineBetween(prev, t))) text += "\n";
        brk = false;
        nodes.push({ node: t, start: text.length });
        text += t.data;
        prev = t;
      }
      return { nodes, text };
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
        const el = el0.closest(SELECTOR);
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
    port.send({ type: "ready" });
  }

  // src/state.ts
  function stampOf(m) {
    return m && +(m.updated_at || 0) || 0;
  }
  function mergeState(x, y) {
    const out = { marks: {}, tombs: {} };
    const keys = {};
    [x, y].forEach((s) => {
      Object.keys(s.marks || {}).forEach((k) => {
        keys[k] = 1;
      });
      Object.keys(s.tombs || {}).forEach((k) => {
        keys[k] = 1;
      });
    });
    Object.keys(keys).forEach((k) => {
      let best = null;
      [x, y].forEach((s) => {
        const m = (s.marks || {})[k];
        if (m && (!best || stampOf(m) > stampOf(best))) best = m;
      });
      const tomb = Math.max(+(x.tombs || {})[k] || 0, +(y.tombs || {})[k] || 0);
      if (best && (!tomb || stampOf(best) > tomb)) out.marks[k] = best;
      else if (tomb) out.tombs[k] = tomb;
    });
    return out;
  }
  function pruneTombs(t, now, ttl) {
    const out = {};
    Object.keys(t).forEach((k) => {
      if (now - t[k] < ttl) out[k] = t[k];
    });
    return out;
  }
  function ackState(s, box) {
    const out = { marks: { ...s.marks }, tombs: { ...s.tombs } };
    (box.items || []).forEach((it) => {
      const at = Math.max(+(box.stamps || {})[it.anchor] || 0, 1);
      if (out.marks[it.anchor] && stampOf(out.marks[it.anchor]) <= at) delete out.marks[it.anchor];
      if (!out.marks[it.anchor]) out.tombs[it.anchor] = Math.max(out.tombs[it.anchor] || 0, at);
    });
    return out;
  }
  function roundAcked(box, data) {
    return !data || data.round_id === void 0 || data.round_id === box.id;
  }
  function syncMessage(verb, data) {
    data = data || {};
    let m;
    if (data.saved === void 0) m = { kind: "ok", text: verb + " · received by the author’s machine" };
    else if (data.saved === false) m = { kind: "err", text: "Not saved — the author’s machine could not store it" };
    else if (data.pushed) m = { kind: "ok", text: verb + " · synced to your team" };
    else m = { kind: "warn", text: "Saved on the author’s machine · not yet synced (will retry)" };
    if (data.notified === false && !data.duplicate) m.text += " · the plan’s authoring coworker was not notified automatically — tell them directly";
    return m;
  }
  function sameItems(a, b) {
    return JSON.stringify(a) === JSON.stringify(b);
  }
  function newRoundId() {
    const c = typeof crypto !== "undefined" ? crypto : null;
    if (c && c.randomUUID) return c.randomUUID();
    const b = new Uint8Array(16);
    let s = "";
    if (c && c.getRandomValues) c.getRandomValues(b);
    else for (let i = 0; i < 16; i++) b[i] = Math.floor(Math.random() * 256);
    for (let j = 0; j < 16; j++) s += ("0" + b[j].toString(16)).slice(-2);
    return s;
  }

  // src/host-controls.ts
  var STATUS = [
    { id: "approve", glyph: "✓" },
    { id: "request-change", glyph: "✎" },
    { id: "flag", glyph: "⚑" },
    { id: "comment", glyph: "◌" }
  ];
  var HL_TINT = { "approve": "sage", "request-change": "amber", "flag": "red", "comment": "gold" };
  var TOMB_TTL = 7 * 24 * 3600 * 1e3;
  var BUBBLE = '<svg width="14" height="14" viewBox="0 0 16 16" aria-hidden="true"><path d="M3 2.5h10A1.5 1.5 0 0 1 14.5 4v6a1.5 1.5 0 0 1-1.5 1.5H7.5L4 14.5v-3H3A1.5 1.5 0 0 1 1.5 10V4A1.5 1.5 0 0 1 3 2.5z" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round"/></svg>';
  function esc(s) {
    return (s || "").replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/"/g, "&quot;");
  }
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
    const base = opts.endpoint;
    const token = opts.token;
    const live = base !== "";
    const KEY = "ox-plan-fb:" + slug;
    const ON_KEY = "ox-plan-rev-on:" + slug;
    const MIN_KEY = "ox-plan-rail-min:" + slug;
    const TOMB_KEY = KEY + ":tombs";
    const OUTBOX_KEY = KEY + ":outbox";
    const EXPORT_KEY = KEY + ":export";
    const SYNC_KEY = "ox-plan-fb-sync:" + slug;
    const stored = readStored();
    let marks = stored.marks;
    let tombs = stored.tombs;
    const committed = /* @__PURE__ */ Object.create(null);
    opts.committed.forEach((x) => {
      (committed[x.anchor] = committed[x.anchor] || []).push(x);
    });
    let reviewer = (localStorage.getItem("ox-plan-reviewer") || "").trim();
    let on = false;
    let pendingReload = false;
    let offline = false;
    let probeTimer = null;
    let posting = 0;
    let reloadAfterPost = false;
    let sending = false;
    let placed = [];
    let unplaced = [];
    let lastStamp = 0;
    function nextStamp() {
      const n = Date.now();
      lastStamp = n > lastStamp ? n : lastStamp + 1;
      return lastStamp;
    }
    function readJSON(k) {
      try {
        return JSON.parse(localStorage.getItem(k)) || null;
      } catch (e) {
        return null;
      }
    }
    function readStored() {
      return { marks: readJSON(KEY) || {}, tombs: readJSON(TOMB_KEY) || {} };
    }
    function save() {
      const m = mergeState({ marks, tombs }, readStored());
      marks = m.marks;
      tombs = pruneTombs(m.tombs, Date.now(), TOMB_TTL);
      try {
        localStorage.setItem(KEY, JSON.stringify(marks));
        localStorage.setItem(TOMB_KEY, JSON.stringify(tombs));
      } catch (e) {
      }
    }
    function removeMark(a) {
      delete marks[a];
      tombs[a] = nextStamp();
    }
    function draft(a) {
      return Object.prototype.hasOwnProperty.call(marks, a) ? marks[a] : void 0;
    }
    function readOutbox() {
      const o = readJSON(OUTBOX_KEY);
      return o && o.id && o.items ? o : null;
    }
    function writeOutbox(o) {
      try {
        if (o) localStorage.setItem(OUTBOX_KEY, JSON.stringify(o));
        else localStorage.removeItem(OUTBOX_KEY);
        return true;
      } catch (e) {
        return false;
      }
    }
    function post(path, payload, ok, fail) {
      if (offline) {
        offlineNotice();
        if (fail) fail("offline", true);
        return;
      }
      posting++;
      fetch(base + path, { method: "POST", headers: { "Content-Type": "application/json", "X-Review-Token": token }, body: JSON.stringify(payload) }).then((r) => {
        if (!r.ok) {
          return r.text().catch(() => "").then((t) => {
            throw new Error("HTTP " + r.status + (t.trim() ? " — " + clip(t.trim(), 120) : ""));
          });
        }
        return r.json().catch(() => ({}));
      }).then((data) => {
        if (ok) ok(data || {});
      }).catch((e) => {
        if (e instanceof TypeError) {
          setOffline(true);
          offlineNotice();
          if (fail) fail("offline", true);
          return;
        }
        if (fail) fail(e.message, false);
        else showSync("err", "Request failed: " + e.message);
      }).then(() => {
        posting--;
        if (!posting && reloadAfterPost) {
          reloadAfterPost = false;
          reloadWhenIdle();
        }
      });
    }
    function committedState(a) {
      const arr = committed[a] || [];
      return arr.length ? repOf(arr).state : "";
    }
    function paintItems() {
      const out = [];
      Object.keys(committed).concat(Object.keys(marks)).forEach((a, i, all) => {
        if (all.indexOf(a) !== i || !/^[hq][0-9a-f]{8}$/.test(a)) return;
        const carr = committed[a] || [];
        const c = carr.length ? repOf(carr) : null, m = draft(a);
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
        out.push(it);
      });
      return out;
    }
    function paint() {
      const n = Object.keys(marks).length;
      countEl.textContent = n ? n + " unsent" : "";
      port.send({ type: "paint", items: paintItems() });
    }
    function onPlaced(items) {
      placed = items;
      const seen = {};
      placed.forEach((p) => {
        seen[p.anchor] = true;
      });
      unplaced = Object.keys(committed).concat(Object.keys(marks)).filter((a, i, all) => !seen[a] && all.indexOf(a) === i);
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
      if (!open.length) return;
      const bar2 = orphanBar = document.createElement("div");
      bar2.className = "rev-orphans";
      const items = open.map((o) => "<li><strong>" + esc(o.status) + "</strong> " + esc(o.label || o.anchor) + (o.note ? " — " + esc(o.note) : "") + "</li>").join("");
      bar2.innerHTML = '<div class="rev-orphans-h">⚠ ' + open.length + " review note(s) no longer anchored (the text changed) — still open:</div><ul>" + items + '</ul><button class="rev-orphans-x">dismiss</button>';
      document.body.appendChild(bar2);
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
            unsent: false
          });
        });
        const m = draft(a);
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
            reviewer: reviewer || "(you)",
            unsent: true
          });
        }
      }
      const nth = {};
      placed.forEach((p) => {
        const k = nth[p.anchor] = nth[p.anchor] === void 0 ? 0 : nth[p.anchor] + 1;
        add({ anchor: p.anchor, nth: k }, p.anchor, p.section, p.label, p.quote || "");
      });
      unplaced.forEach((a) => {
        const m = draft(a);
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
        document.body.appendChild(rail);
        rail.addEventListener("click", (ev) => {
          if (ev.target.closest(".rev-rail-show, .rev-rail-hide")) toggleRail();
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
      if (!rows.length) {
        r.innerHTML = '<div class="rev-rail-h">Comments' + hide + '</div><div class="rev-rail-empty">Click any section, risk, or row — or highlight text — to leave a comment.</div>';
        return;
      }
      let html = '<div class="rev-rail-h">Comments <span class="rev-rail-n">' + rows.length + "</span>" + hide + '</div><ul class="rev-rail-list">';
      rows.forEach((row, i) => {
        let tag = "";
        if (row.unsent) tag = '<span class="rev-rail-tag unsent">unsent</span>';
        else if (row.state && row.state !== "open") tag = '<span class="rev-rail-tag ' + esc(row.state) + '">' + esc(row.state) + "</span>";
        if (!row.at) tag += '<span class="rev-rail-tag">text changed</span>';
        const content = (row.quote ? '<div class="rev-rail-quote">' + esc(row.label) + "</div>" : "") + (row.note ? '<div class="rev-rail-note">' + esc(row.note) + "</div>" : row.quote ? "" : '<div class="rev-rail-note muted">' + esc(row.label) + "</div>");
        const whoTag = row.reviewer ? '<span class="rev-rail-who">' + esc(row.reviewer) + "</span>" : "";
        html += '<li class="rev-rail-item" data-i="' + i + '" data-status="' + esc(row.status) + '"><span class="rev-rail-glyph">' + glyphFor(row.status) + '</span><div class="rev-rail-body"><div class="rev-rail-sec"><span class="rev-rail-sec-t">' + esc(row.section || "(plan)") + "</span>" + whoTag + tag + "</div>" + content + "</div></li>";
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
    function sendMode() {
      port.send({ type: "mode", on, open: !!pop });
    }
    function closePop() {
      if (pop) {
        pop.remove();
        pop = null;
        sendMode();
      }
      port.send({ type: "focus", anchor: null });
      if (pendingReload) {
        pendingReload = false;
        location.reload();
      }
    }
    function openPop(t, rect) {
      closePop();
      const a = t.a;
      const cstate = committedState(a);
      const p = pop = document.createElement("div");
      p.className = "rev-pop";
      const quote = t.quote ? '<div class="rev-quote">' + esc(t.label) + "</div>" : "";
      port.send({ type: "focus", anchor: a });
      if (live && (cstate === "addressed" || cstate === "verified")) {
        p.innerHTML = quote + '<div class="rev-cap">Agent marked this ' + cstate + '.</div><textarea class="rev-note" placeholder="reopen note (optional)"></textarea><div class="rev-row"><button class="rev-accept">Accept</button><button class="rev-reopen">Reopen</button></div>';
        placePop(rect);
        p.querySelector(".rev-accept").onclick = () => {
          post("/accept", { anchor: a }, (data) => {
            closePop();
            reportSync(syncMessage("Accepted", data));
          });
        };
        p.querySelector(".rev-reopen").onclick = () => {
          post("/reopen", { anchor: a, note: p.querySelector(".rev-note").value.trim() }, (data) => {
            closePop();
            reportSync(syncMessage("Reopened", data));
          });
        };
        return;
      }
      const existing = draft(a) || { status: "request-change", note: "" };
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
        const m = { anchor: a, section: t.section, label: t.label, status, note: p.querySelector(".rev-note").value.trim(), updated_at: nextStamp() };
        if (t.quote) m.quote = t.quote;
        marks[a] = m;
        save();
        paint();
        closePop();
      };
      p.querySelector(".rev-del").onclick = () => {
        removeMark(a);
        save();
        paint();
        closePop();
      };
    }
    function placePop(rect) {
      const p = pop;
      document.body.appendChild(p);
      p.style.top = window.scrollY + rect.bottom + 8 + "px";
      p.style.left = window.scrollX + Math.min(rect.left, window.innerWidth - 300) + "px";
      sendMode();
    }
    document.addEventListener("click", (ev) => {
      const t = ev.target;
      if (pop && pop.contains(t)) return;
      if (t.closest && t.closest(CHROME) && (on || pop)) closePop();
    }, true);
    function submit() {
      if (sending) return;
      let box = live ? readOutbox() : null;
      if (!box) {
        const items = Object.keys(marks).map((k) => wireItem(marks[k]));
        if (!items.length) {
          showSync("info", "No marks yet. Toggle Review, click a section, leave a note.");
          return;
        }
        if (!reviewer) {
          askName("Your name, so teammates know whose feedback this is:", submit);
          return;
        }
        const stamps = {};
        Object.keys(marks).forEach((k) => {
          stamps[k] = stampOf(marks[k]);
        });
        box = { id: newRoundId(), items, reviewer, created_at: (/* @__PURE__ */ new Date()).toISOString(), stamps };
        if (!live) {
          exportJSON(box);
          return;
        }
        writeOutbox(box);
      }
      send(box);
    }
    function wireItem(m) {
      const o = {};
      Object.keys(m).forEach((k) => {
        if (k !== "updated_at") o[k] = m[k];
      });
      return o;
    }
    function send(box) {
      sending = true;
      paintSubmit();
      showSync("pending", "Sending…");
      post("/feedback", { id: box.id, slug, reviewer: box.reviewer, items: box.items }, (data) => {
        sending = false;
        if (!roundAcked(box, data)) {
          paintSubmit();
          showSync("err", "Not confirmed — the server acknowledged a different round. Your marks are kept.", [{ label: "Retry", fn: submit }]);
          return;
        }
        const acked = ackState(mergeState({ marks, tombs }, readStored()), box);
        marks = acked.marks;
        tombs = acked.tombs;
        save();
        const cur = readOutbox();
        if (cur && cur.id === box.id) writeOutbox(null);
        paint();
        paintSubmit();
        reportSync(syncMessage("Sent", data));
      }, (msg, isOffline) => {
        sending = false;
        paintSubmit();
        if (isOffline) showSync("warn", "Review server offline — your feedback is queued in this browser and sends when the server is back.");
        else showSync("err", "Not sent (" + msg + "). Your marks are kept.", [{ label: "Retry", fn: submit }]);
      });
    }
    function approve() {
      if (!live) {
        showSync("info", "Approve is available in the live `ox plan review` loop.");
        return;
      }
      showSync("ask", "Approve this plan? This stamps it approved and closes the review loop.", [
        { label: "Approve", fn: () => {
          showSync("pending", "Approving…");
          post("/approve", {}, (data) => {
            const m = syncMessage("Approved", data);
            if (m.kind !== "err") m.text += " — you can close this tab.";
            reportSync(m);
          });
        } },
        { label: "Cancel", fn: hideSync }
      ]);
    }
    function exportJSON(box) {
      const prev = readJSON(EXPORT_KEY);
      const id = prev && prev.id && sameItems(prev.items, box.items) ? prev.id : box.id;
      try {
        localStorage.setItem(EXPORT_KEY, JSON.stringify({ id, items: box.items }));
      } catch (e) {
      }
      const json = JSON.stringify({ id, slug, reviewer: box.reviewer, items: box.items }, null, 2);
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
    function showSync(kind, text, actions, input) {
      hideSync();
      const el = syncEl = document.createElement("div");
      el.className = "rev-sync " + kind;
      el.setAttribute("role", kind === "err" ? "alert" : "status");
      const msg = document.createElement("span");
      msg.className = "rev-sync-msg";
      msg.textContent = text;
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
      document.body.appendChild(el);
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
          reviewer = n;
          try {
            localStorage.setItem("ox-plan-reviewer", reviewer);
          } catch (e) {
          }
          hideSync();
          updateWho();
          paint();
          if (then) then();
        } },
        { label: "Cancel", fn: hideSync }
      ], reviewer);
    }
    let connEl = null, offlineBar = null, toastEl = null;
    function restartCmd() {
      return "ox plan review " + slug;
    }
    function paintConn() {
      if (!connEl) return;
      connEl.textContent = offline ? "● offline" : "● live";
      connEl.className = "rev-conn " + (offline ? "off" : "ok");
      connEl.title = offline ? "Review server unreachable — feedback is NOT being saved" : "Connected to the review server";
    }
    function setOffline(down) {
      if (offline === down) return;
      offline = down;
      body.classList.toggle("rev-offline", down);
      paintConn();
      if (down) {
        showOfflineBar();
        startProbe();
      } else {
        hideOfflineBar();
        stopProbe();
      }
    }
    function showOfflineBar() {
      hideOfflineBar();
      const n = Object.keys(marks).length;
      const bar2 = offlineBar = document.createElement("div");
      bar2.className = "rev-offline-bar";
      bar2.innerHTML = "<strong>⚠ Review server offline</strong> — new feedback is NOT being saved. " + (n ? n + " unsent mark(s) are" : "Your marks are") + " kept in this browser and restored on reconnect. Restart: <code>" + esc(restartCmd()) + '</code> <button class="rev-offline-copy">copy</button>';
      document.body.appendChild(bar2);
      const copy = bar2.querySelector(".rev-offline-copy");
      copy.onclick = () => {
        if (navigator.clipboard) navigator.clipboard.writeText(restartCmd()).then(() => {
          copy.textContent = "copied ✓";
        }).catch(() => {
        });
      };
    }
    function hideOfflineBar() {
      if (offlineBar) {
        offlineBar.remove();
        offlineBar = null;
      }
    }
    function offlineNotice() {
      const n = Object.keys(marks).length;
      showSync("err", "The review server is offline — feedback can NOT be saved right now. " + (n ? "Your " + n + " unsent mark(s) stay in this browser and will be restored. " : "") + "Restart the loop with: " + restartCmd());
    }
    function toast(msg) {
      if (toastEl) toastEl.remove();
      const el = toastEl = document.createElement("div");
      el.className = "rev-toast";
      el.textContent = msg;
      document.body.appendChild(el);
      setTimeout(() => {
        el.remove();
        if (toastEl === el) toastEl = null;
      }, 8e3);
    }
    function startProbe() {
      if (probeTimer) return;
      probeTimer = setInterval(() => {
        fetch(base + "/healthz", { cache: "no-store" }).then((r) => r.ok ? r.json() : null).then((j) => {
          if (j && j.app === "ox-plan-review") reloadWhenIdle();
        }).catch(() => {
        });
      }, 3e3);
    }
    function stopProbe() {
      if (probeTimer) {
        clearInterval(probeTimer);
        probeTimer = null;
      }
    }
    function reloadWhenIdle() {
      if (pop) {
        pendingReload = true;
        return;
      }
      location.reload();
    }
    const bar = document.createElement("div");
    bar.className = "rev-bar";
    bar.innerHTML = (live ? '<span class="rev-conn"></span>' : "") + '<button class="rev-toggle" title="Toggle review mode">Review</button><span class="rev-count"></span>' + (live ? '<button class="rev-who" title="Set the name teammates see on your comments"></button>' : "") + '<button class="rev-submit" title="Send feedback to the agent">' + (live ? "Submit" : "Export") + "</button>" + (live ? '<button class="rev-approve" title="Approve and close the loop">Approve</button>' : "");
    document.body.appendChild(bar);
    const countEl = bar.querySelector(".rev-count");
    connEl = bar.querySelector(".rev-conn");
    paintConn();
    const whoEl = bar.querySelector(".rev-who");
    function updateWho() {
      if (whoEl) whoEl.textContent = reviewer ? "You: " + reviewer : "Set name";
    }
    updateWho();
    if (whoEl) whoEl.onclick = () => {
      askName("Your name (shown to teammates on this plan):");
    };
    const submitEl = bar.querySelector(".rev-submit");
    function paintSubmit() {
      if (!live) return;
      submitEl.disabled = sending;
      submitEl.textContent = sending ? "Sending…" : "Submit";
    }
    const toggleEl = bar.querySelector(".rev-toggle");
    let hintBubble = null;
    function setReview(next, silent) {
      on = next;
      toggleEl.classList.toggle("on", on);
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
    if (live) bar.querySelector(".rev-approve").onclick = approve;
    document.addEventListener("keydown", (e) => {
      if (e.defaultPrevented) return;
      if (e.key === "Escape") {
        if (pop) {
          closePop();
          e.preventDefault();
        } else if (on) {
          setReview(false);
          e.preventDefault();
        }
        return;
      }
      if (e.key === "r" && !typing(e) && !e.metaKey && !e.ctrlKey && !e.altKey) {
        setReview(!on);
        e.preventDefault();
      }
    });
    try {
      if (!localStorage.getItem("ox-plan-rev-seen")) {
        const hint = hintBubble = document.createElement("div");
        hint.className = "rev-hint";
        hint.textContent = "← Click Review to mark up this plan";
        document.body.appendChild(hint);
        setTimeout(() => {
          if (hintBubble === hint) {
            hint.remove();
            hintBubble = null;
          }
        }, 9e3);
      }
    } catch (e) {
    }
    if (live && window.EventSource) {
      try {
        const es = new EventSource(base + "/events?t=" + encodeURIComponent(token));
        es.onopen = () => {
          if (offline) {
            setOffline(false);
            reloadWhenIdle();
            return;
          }
          paintConn();
        };
        es.onerror = () => {
          setOffline(true);
        };
        es.onmessage = () => {
          if (posting) {
            reloadAfterPost = true;
            return;
          }
          if (pop) {
            pendingReload = true;
            return;
          }
          location.reload();
        };
      } catch (e) {
      }
    }
    if (live && "serviceWorker" in navigator) {
      try {
        navigator.serviceWorker.register("/sw.js").catch(() => {
        });
      } catch (e) {
      }
    }
    window.addEventListener("storage", (e) => {
      if (e.key === null || e.key === KEY || e.key === TOMB_KEY) {
        const m = mergeState({ marks, tombs }, readStored());
        marks = m.marks;
        tombs = m.tombs;
        paint();
      }
    });
    const pendingBox = live ? readOutbox() : null;
    if (live && Object.keys(marks).length && !pendingBox) {
      toast(Object.keys(marks).length + " unsent mark(s) restored — Submit to send them.");
    }
    let started = false;
    port.listen((m) => {
      switch (m.type) {
        case "ready":
          paint();
          if (started) {
            sendMode();
            return;
          }
          started = true;
          restoreSync();
          if (pendingBox) send(pendingBox);
          try {
            if (sessionStorage.getItem(ON_KEY)) setReview(true, true);
          } catch (e) {
          }
          break;
        case "placed":
          onPlaced(m.items);
          break;
        case "target":
          openPop({ a: m.target.anchor, section: m.target.section, label: m.target.label, quote: m.target.quote }, m.target.rect);
          break;
        case "refused":
          closePop();
          toast(m.reason === "repeated" ? "“" + clip(m.quote, 40) + "” appears " + m.count + " times in this section — highlight a longer phrase so the comment points at one place." : "Highlight text within one section to comment on it.");
          break;
        case "dismiss":
          if (on || pop) closePop();
          break;
      }
    });
  }

  // src/local.ts
  (function() {
    try {
      if (!window.localStorage) return;
    } catch (e) {
      return;
    }
    const body = document.body;
    let committed = [];
    try {
      const el = document.getElementById("ox-review-state");
      const v = el ? JSON.parse(el.textContent || "[]") : [];
      if (Array.isArray(v)) committed = v.filter((x) => x && typeof x.anchor === "string");
    } catch (e) {
      committed = [];
    }
    const ports = localPorts();
    mountHostControls(ports.host, {
      slug: (body.getAttribute("data-slug") || document.title || "plan").trim(),
      endpoint: body.getAttribute("data-review-endpoint") || "",
      token: body.getAttribute("data-review-token") || "",
      committed
    });
    startDocumentTools(ports.document);
  })();
})();
