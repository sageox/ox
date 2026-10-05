// ox plan — in-document review LOOP layer. Vanilla JS, zero deps, file://-safe.
// Toggle Review, mark up a section/risk/decision — or highlight a phrase —
// (approve / request-change / flag / comment + note), Submit. When served by
// `ox plan review` it also:
//   - live-reloads via SSE as the agent addresses items + re-renders,
//   - lets the reviewer Accept (verify) or Reopen an addressed item inline,
//   - offers a top-level Approve to close the loop,
//   - surfaces orphaned notes whose anchored text changed (never lose feedback).
// Anchors are a CONTENT hash (section heading + element text, or + the quoted
// text for a highlight) so a mark survives a re-render. Static (file://) mode
// falls back to clipboard export. NOT Agentation (license-clean). Inert until
// toggled.
(function () {
  var body = document.body;
  var slug = (body.getAttribute('data-slug') || document.title || 'plan').trim();
  var base = body.getAttribute('data-review-endpoint') || ''; // live server base, else ""
  var token = body.getAttribute('data-review-token') || '';
  var live = base !== '';
  var KEY = 'ox-plan-fb:' + slug;
  var ON_KEY = 'ox-plan-rev-on:' + slug; // sessionStorage: survives a reload, not a new tab
  var MIN_KEY = 'ox-plan-rail-min:' + slug; // sessionStorage, like ON_KEY
  var STATUS = [
    { id: 'approve', glyph: '✓' },
    { id: 'request-change', glyph: '✎' },
    { id: 'flag', glyph: '⚑' },
    { id: 'comment', glyph: '◌' }
  ];
  var SELECTOR = window.OX_REVIEW_SELECTOR || 'section[id], li, tr, .ox-chip, .stat, .bar-row';

  // Storage layout (all per slug, localStorage so every tab on the plan shares it):
  //   KEY        anchor -> unsent mark, each stamped with updated_at (ms)
  //   TOMB_KEY   anchor -> ms a mark was deleted or sent; a tab's stale copy
  //              older than that never brings it back
  //   OUTBOX_KEY the one submission in flight: {id, items, reviewer,
  //              created_at, stamps}. Written BEFORE the POST and resent with
  //              the SAME id until a matching ack, so a reload, a retry, or a
  //              second tab can never turn one Submit into two rounds.
  var TOMB_KEY = KEY + ':tombs';
  var OUTBOX_KEY = KEY + ':outbox';
  var EXPORT_KEY = KEY + ':export'; // static mode: the id of the last export, reused while its items are unchanged
  var SYNC_KEY = 'ox-plan-fb-sync:' + slug; // sessionStorage: last send result, re-shown after the reload it triggers
  // A tombstone only has to outlive a stale tab's copy of the mark. A week
  // covers a laptop left open over a weekend; a tab stale for longer than
  // that can resurrect a sent mark as unsent, which the reviewer can delete.
  var TOMB_TTL = 7 * 24 * 3600 * 1000;

  var stored = readStored();
  var marks = stored.marks;
  var tombs = stored.tombs;
  var committed = parseCommitted();
  var reviewer = (localStorage.getItem('ox-plan-reviewer') || '').trim(); // multi-user: who you are
  var on = false;
  var pendingReload = false;
  var offline = false; // live server unreachable — nothing can be saved until it returns
  var probeTimer = null;
  var posting = 0; // POSTs awaiting a response; an SSE reload waits for them
  var reloadAfterPost = false;
  var sending = false; // this tab's /feedback send is in flight

  // --- pure: begin — no DOM, no storage; review_state_test.go runs this block in node ---
  function stampOf(m) { return (m && +m.updated_at) || 0; }
  // mergeState joins two {marks, tombs} views of the same plan per anchor: the
  // newest write wins, and a tombstone beats a mark stamped at or before it, so
  // a delete (or a send) in one tab is not undone by another tab's stale copy.
  function mergeState(x, y) {
    var out = { marks: {}, tombs: {} }, keys = {};
    [x, y].forEach(function (s) {
      Object.keys(s.marks || {}).forEach(function (k) { keys[k] = 1; });
      Object.keys(s.tombs || {}).forEach(function (k) { keys[k] = 1; });
    });
    Object.keys(keys).forEach(function (k) {
      var best = null;
      [x, y].forEach(function (s) { var m = (s.marks || {})[k]; if (m && (!best || stampOf(m) > stampOf(best))) best = m; });
      var tomb = Math.max(+((x.tombs || {})[k]) || 0, +((y.tombs || {})[k]) || 0);
      // tomb 0 = never deleted, so a mark saved before stamps existed (0) stays
      if (best && (!tomb || stampOf(best) > tomb)) out.marks[k] = best;
      else if (tomb) out.tombs[k] = tomb;
    });
    return out;
  }
  function pruneTombs(t, now, ttl) {
    var out = {};
    Object.keys(t).forEach(function (k) { if (now - t[k] < ttl) out[k] = t[k]; });
    return out;
  }
  // ackState clears what a round carried once the server acknowledged it: each
  // sent anchor is tombstoned at the stamp it was sent with, so an edit made
  // after Submit (a newer stamp, in any tab) survives as still unsent.
  function ackState(s, box) {
    var out = { marks: {}, tombs: {} };
    Object.keys(s.marks).forEach(function (k) { out.marks[k] = s.marks[k]; });
    Object.keys(s.tombs).forEach(function (k) { out.tombs[k] = s.tombs[k]; });
    (box.items || []).forEach(function (it) {
      // a mark from before stamps existed is sent at 0; tomb it at 1 so it still beats a stale 0
      var at = Math.max(+((box.stamps || {})[it.anchor]) || 0, 1);
      if (out.marks[it.anchor] && stampOf(out.marks[it.anchor]) <= at) delete out.marks[it.anchor];
      if (!out.marks[it.anchor]) out.tombs[it.anchor] = Math.max(out.tombs[it.anchor] || 0, at);
    });
    return out;
  }
  // roundAcked: whether a 2xx acknowledges this outbox. A server from before
  // round ids answers without round_id — saved, but which round is unknown.
  function roundAcked(box, data) { return !data || data.round_id === undefined || data.round_id === box.id; }
  // syncMessage words a 2xx truthfully: "synced" only when the server says the
  // ledger was pushed. An older server reports nothing about sync, so the page
  // claims no more than that the author's machine has it.
  function syncMessage(verb, data) {
    data = data || {};
    var m;
    if (data.saved === undefined) m = { kind: 'ok', text: verb + ' · received by the author’s machine' };
    else if (data.saved === false) m = { kind: 'err', text: 'Not saved — the author’s machine could not store it' };
    else if (data.pushed) m = { kind: 'ok', text: verb + ' · synced to your team' };
    else m = { kind: 'warn', text: 'Saved on the author’s machine · not yet synced (will retry)' };
    // a duplicate is a resend of a round whose first save already notified (or tried to)
    if (data.notified === false && !data.duplicate) m.text += ' · the plan’s authoring coworker was not notified automatically — tell them directly';
    return m;
  }
  function sameItems(a, b) { return JSON.stringify(a) === JSON.stringify(b); }
  // newRoundId: the server accepts ^[A-Za-z0-9_-]{8,64}$.
  function newRoundId() {
    var c = (typeof crypto !== 'undefined') ? crypto : null;
    if (c && c.randomUUID) return c.randomUUID();
    var b = new Uint8Array(16), s = '';
    if (c && c.getRandomValues) c.getRandomValues(b);
    else for (var i = 0; i < 16; i++) b[i] = Math.floor(Math.random() * 256); // no Web Crypto: still unique enough to dedupe one reviewer's retries
    for (var j = 0; j < 16; j++) s += ('0' + b[j].toString(16)).slice(-2);
    return s;
  }
  // --- pure: end ---

  var lastStamp = 0;
  function nextStamp() { var n = Date.now(); lastStamp = n > lastStamp ? n : lastStamp + 1; return lastStamp; }
  function readJSON(k) { try { return JSON.parse(localStorage.getItem(k)) || null; } catch (e) { return null; } }
  function readStored() { return { marks: readJSON(KEY) || {}, tombs: readJSON(TOMB_KEY) || {} }; }
  // save merges with what other tabs wrote rather than overwriting it: each
  // tab saves its whole map, so a plain write would drop the other tab's marks.
  function save() {
    var m = mergeState({ marks: marks, tombs: tombs }, readStored());
    marks = m.marks; tombs = pruneTombs(m.tombs, Date.now(), TOMB_TTL);
    try { localStorage.setItem(KEY, JSON.stringify(marks)); localStorage.setItem(TOMB_KEY, JSON.stringify(tombs)); } catch (e) {}
  }
  function removeMark(a) { delete marks[a]; tombs[a] = nextStamp(); }
  function readOutbox() { var o = readJSON(OUTBOX_KEY); return o && o.id && o.items ? o : null; }
  function writeOutbox(o) {
    try { if (o) localStorage.setItem(OUTBOX_KEY, JSON.stringify(o)); else localStorage.removeItem(OUTBOX_KEY); return true; }
    catch (e) { return false; }
  }
  function parseCommitted() {
    // anchor -> [mark, …]: multi-user, so several reviewers on one anchor all show.
    var el = document.getElementById('ox-review-state');
    if (!el) return {};
    try { var a = JSON.parse(el.textContent || '[]'); var m = {}; a.forEach(function (x) { (m[x.anchor] = m[x.anchor] || []).push(x); }); return m; }
    catch (e) { return {}; }
  }
  // repOf picks the representative mark for an anchor's inline glyph. Prefer a
  // still-OPEN blocking mark (request-change/flag) so one reviewer's unresolved
  // feedback is never painted as resolved just because another reviewer's mark on
  // the same anchor was addressed; then any open mark; then addressed/verified.
  function repOf(arr) {
    for (var i = 0; i < arr.length; i++) { if (arr[i].state === 'open' && (arr[i].status === 'request-change' || arr[i].status === 'flag')) return arr[i]; }
    for (var j = 0; j < arr.length; j++) { if (arr[j].state === 'open') return arr[j]; }
    for (var k = 0; k < arr.length; k++) { if (arr[k].state === 'addressed' || arr[k].state === 'verified') return arr[k]; }
    return arr[0];
  }
  // post never uses alert/confirm/prompt: browsers block them in the iframe
  // the web app embeds plans in, so every outcome is reported inline. fail(msg,
  // isOffline) runs on any failure; without it an HTTP error shows inline.
  function post(path, payload, ok, fail) {
    if (offline) { offlineNotice(); if (fail) fail('offline', true); return; }
    posting++;
    fetch(base + path, { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Review-Token': token }, body: JSON.stringify(payload) })
      .then(function (r) {
        if (!r.ok) {
          return r.text().catch(function () { return ''; }).then(function (t) {
            throw new Error('HTTP ' + r.status + (t.trim() ? ' — ' + clip(t.trim(), 120) : ''));
          });
        }
        return r.json().catch(function () { return {}; });
      })
      .then(function (data) { if (ok) ok(data || {}); })
      .catch(function (e) {
        // a network-level failure means the server is gone — flip to
        // disconnected mode (marks stay in localStorage; nothing is lost)
        // rather than surfacing a raw fetch error.
        if (e instanceof TypeError) { setOffline(true); offlineNotice(); if (fail) fail('offline', true); return; }
        if (fail) fail(e.message, false);
        else showSync('err', 'Request failed: ' + e.message);
      })
      .then(function () {
        posting--;
        if (!posting && reloadAfterPost) { reloadAfterPost = false; reloadWhenIdle(); }
      });
  }

  function fnv1a(s) { var h = 0x811c9dc5; for (var i = 0; i < s.length; i++) { h ^= s.charCodeAt(i); h = (h * 0x01000193) >>> 0; } return ('0000000' + h.toString(16)).slice(-8); }
  function norm(s) { return (s || '').replace(/\s+/g, ' ').trim().toLowerCase(); }
  function headingOf(el) { var sec = el.closest('section[id], [data-ox-section]'); if (!sec) return ''; var ds = sec.getAttribute && sec.getAttribute('data-ox-section'); if (ds) return ds; var h = sec.querySelector('h2, h3'); return h ? h.textContent : (sec.id || ''); }
  function anchorText(el) {
    var clone = el.cloneNode(true);
    clone.querySelectorAll('.rev-glyph').forEach(function (g) { g.remove(); });
    return clone.textContent || '';
  }
  function anchorFor(el) { return 'h' + fnv1a(norm(headingOf(el)) + '\u0000' + norm(anchorText(el))); }
  function glyphFor(id) { for (var i = 0; i < STATUS.length; i++) if (STATUS[i].id === id) return STATUS[i].glyph; return '◌'; }
  function clip(t, n) { return t.length > n ? t.slice(0, n - 1) + '…' : t; }
  function labelFor(el) { return clip((el.textContent || '').replace(/\s+/g, ' ').trim(), 70); }
  function committedState(a) { var arr = committed[a] || []; return arr.length ? repOf(arr).state : ''; }

  function paint() {
    document.querySelectorAll('.rev-marked,.rev-committed').forEach(function (el) {
      el.classList.remove('rev-marked', 'rev-committed'); el.removeAttribute('data-rev'); el.removeAttribute('data-revstate');
      var g = el.querySelector(':scope > .rev-glyph'); if (g) g.remove();
    });
    var seen = {};
    document.querySelectorAll(SELECTOR).forEach(function (el) {
      var a = anchorFor(el);
      var carr = committed[a] || [];
      var c = carr.length ? repOf(carr) : null, m = marks[a];
      if (!c && !m) return;
      seen[a] = true;
      var status = m ? m.status : c.status;
      var glyph = document.createElement('span');
      glyph.className = 'rev-glyph';
      if (c && !m) {
        el.classList.add('rev-committed'); el.setAttribute('data-revstate', c.state);
        glyph.textContent = (c.state === 'addressed' || c.state === 'verified') ? '✓' : (c.state === 'wontfix' ? '—' : glyphFor(status));
        var who = carr.map(function (x) { return x.reviewer || 'someone'; }).join(', ');
        glyph.title = who + ' · ' + c.state + (carr.length > 1 ? ' (' + carr.length + ' reviewers)' : '') + (c.note ? (' — ' + c.note) : '');
      } else {
        el.classList.add('rev-marked'); el.setAttribute('data-rev', status);
        glyph.textContent = glyphFor(status);
      }
      el.appendChild(glyph);
    });
    paintQuotes(seen);
    // comments whose text is gone from the page — rewritten by the agent, or
    // edited while this tab was away — keep a rail row, so an addressed one can
    // still be accepted or reopened
    unplaced = Object.keys(committed).concat(Object.keys(marks)).filter(function (a, i, all) { return !seen[a] && all.indexOf(a) === i; });
    // orphaned committed notes: their anchored text changed, so nothing matched.
    var orphans = [];
    Object.keys(committed).forEach(function (a) { if (!seen[a]) committed[a].forEach(function (x) { orphans.push(x); }); });
    renderOrphans(orphans);
    var n = Object.keys(marks).length;
    countEl.textContent = n ? (n + ' unsent') : '';
    renderRail();
  }

  var orphanBar;
  function renderOrphans(orphans) {
    if (orphanBar) { orphanBar.remove(); orphanBar = null; }
    var open = orphans.filter(function (o) { return o.state === 'open'; });
    if (!open.length) return;
    orphanBar = document.createElement('div');
    orphanBar.className = 'rev-orphans';
    var items = open.map(function (o) {
      return '<li><strong>' + esc(o.status) + '</strong> ' + esc(o.label || o.anchor) + (o.note ? ' — ' + esc(o.note) : '') + '</li>';
    }).join('');
    orphanBar.innerHTML = '<div class="rev-orphans-h">⚠ ' + open.length + ' review note(s) no longer anchored (the text changed) — still open:</div><ul>' + items + '</ul><button class="rev-orphans-x">dismiss</button>';
    document.body.appendChild(orphanBar);
    orphanBar.querySelector('.rev-orphans-x').onclick = function () { orphanBar.remove(); orphanBar = null; };
  }
  function esc(s) { return (s || '').replace(/&/g, '&amp;').replace(/</g, '&lt;'); }

  // --- comments rail: surface the note TEXT in the right gutter (not just a
  // margin glyph), each row scroll-jumping to its anchored element on click ---
  var rail;
  var unplaced = []; // anchors with a comment but nothing on the page, set by paint()
  // Hide shrinks the rail to a Show button (comment icon + count) so the plan
  // under it can be read; kept per tab, so a live reload doesn't reopen it.
  var railMin = false;
  try { railMin = !!sessionStorage.getItem(MIN_KEY); } catch (e) {}
  var BUBBLE = '<svg width="14" height="14" viewBox="0 0 16 16" aria-hidden="true"><path d="M3 2.5h10A1.5 1.5 0 0 1 14.5 4v6a1.5 1.5 0 0 1-1.5 1.5H7.5L4 14.5v-3H3A1.5 1.5 0 0 1 1.5 10V4A1.5 1.5 0 0 1 3 2.5z" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round"/></svg>';
  function toggleRail() {
    railMin = !railMin;
    try { if (railMin) sessionStorage.setItem(MIN_KEY, '1'); else sessionStorage.removeItem(MIN_KEY); } catch (e) {}
    renderRail();
    rail.querySelector('.rev-rail-show, .rev-rail-hide').focus(); // the clicked button was just replaced
  }
  function flashEl(el) {
    el.classList.add('rev-flash');
    setTimeout(function () { el.classList.remove('rev-flash'); }, 1200);
  }
  function railRows() {
    // document order; each row carries its live element or highlight range for
    // scroll-to. Multi-user: one row per reviewer's committed mark on an anchor,
    // plus your unsent local mark.
    var rows = [];
    function add(at, a, section, label, quote) {
      (committed[a] || []).forEach(function (c) {
        rows.push({
          at: at, a: a, quote: quote, status: c.status, note: c.note,
          section: c.section || section, label: c.label || label,
          state: c.state, reviewer: c.reviewer || '', unsent: false
        });
      });
      var m = marks[a];
      if (m) {
        rows.push({
          at: at, a: a, quote: quote, status: m.status, note: m.note,
          section: m.section || section, label: m.label || label,
          state: '', reviewer: reviewer || '(you)', unsent: true
        });
      }
    }
    document.querySelectorAll(SELECTOR).forEach(function (el) { add(el, anchorFor(el), headingOf(el), labelFor(el), ''); });
    Object.keys(qRanges).forEach(function (a) { var q = qRanges[a]; add(q.range, a, q.section, q.label, q.quote); });
    unplaced.forEach(function (a) {
      var its = (committed[a] || []).concat(marks[a] ? [marks[a]] : []);
      var q = its.filter(function (x) { return x.quote; })[0];
      var any = q || its.filter(function (x) { return x.label; })[0] || its[0];
      add(null, a, any.section || '', any.label || '', q ? q.quote : '');
    });
    // reading order, highlights interleaved with element rows, and rows with
    // nothing on the page last; the sort is stable, so one anchor's rows keep
    // their committed-then-unsent order
    rows.forEach(function (r) {
      if (!r.at || r.at.startContainer) { r.pos = r.at; return; }
      r.pos = document.createRange(); r.pos.selectNode(r.at);
    });
    rows.sort(function (x, y) {
      if (!x.pos || !y.pos) return (x.pos ? 0 : 1) - (y.pos ? 0 : 1);
      return x.pos.compareBoundaryPoints(Range.START_TO_START, y.pos);
    });
    return rows;
  }
  function renderRail() {
    if (!rail) {
      rail = document.createElement('aside'); rail.className = 'rev-rail'; document.body.appendChild(rail);
      rail.addEventListener('click', function (ev) { if (ev.target.closest('.rev-rail-show, .rev-rail-hide')) toggleRail(); });
    }
    var rows = railRows();
    // hidden when there's nothing to read and we're not actively reviewing.
    if (!on && !rows.length) { rail.style.display = 'none'; rail.innerHTML = ''; return; }
    rail.style.display = '';
    rail.classList.toggle('rev-rail-min', railMin);
    if (railMin) {
      rail.innerHTML = '<button class="rev-rail-show" title="Show comments" aria-label="Show comments (' + rows.length + ')">' + BUBBLE + rows.length + '</button>';
      return;
    }
    var hide = '<button class="rev-rail-hide" title="Hide comments">Hide</button>';
    if (!rows.length) {
      rail.innerHTML = '<div class="rev-rail-h">Comments' + hide + '</div><div class="rev-rail-empty">Click any section, risk, or row — or highlight text — to leave a comment.</div>';
      return;
    }
    var html = '<div class="rev-rail-h">Comments <span class="rev-rail-n">' + rows.length + '</span>' + hide + '</div><ul class="rev-rail-list">';
    rows.forEach(function (r, i) {
      var tag = '';
      if (r.unsent) tag = '<span class="rev-rail-tag unsent">unsent</span>';
      else if (r.state && r.state !== 'open') tag = '<span class="rev-rail-tag ' + esc(r.state) + '">' + esc(r.state) + '</span>';
      if (!r.at) tag += '<span class="rev-rail-tag">text changed</span>';
      // a highlight's row leads with its words; its label is those words, so a
      // note-less highlight shows nothing more
      var body = (r.quote ? '<div class="rev-rail-quote">' + esc(r.label) + '</div>' : '') +
        (r.note ? '<div class="rev-rail-note">' + esc(r.note) + '</div>'
          : r.quote ? '' : '<div class="rev-rail-note muted">' + esc(r.label) + '</div>');
      var whoTag = r.reviewer ? '<span class="rev-rail-who">' + esc(r.reviewer) + '</span>' : '';
      html += '<li class="rev-rail-item" data-i="' + i + '" data-status="' + esc(r.status) + '">' +
        '<span class="rev-rail-glyph">' + glyphFor(r.status) + '</span>' +
        '<div class="rev-rail-body"><div class="rev-rail-sec"><span class="rev-rail-sec-t">' + esc(r.section || '(plan)') + '</span>' + whoTag + tag + '</div>' + body + '</div></li>';
    });
    html += '</ul>';
    rail.innerHTML = html;
    rail.querySelectorAll('.rev-rail-item').forEach(function (li) {
      li.onclick = function (ev) {
        var r = rows[+li.getAttribute('data-i')];
        if (!r) return;
        // nothing on the page to scroll to: open the comment from its row
        if (!r.at) { openPop({ a: r.a, section: r.section, label: r.label, quote: r.quote }, ev); return; }
        var range = r.at.startContainer ? r.at : null;
        var el = range ? range.startContainer.parentElement : r.at;
        el.scrollIntoView({ behavior: 'smooth', block: 'center' });
        if (range) flashRange(range); else flashEl(el);
      };
    });
  }

  var pop;
  function closePop() {
    if (pop) { pop.remove(); pop = null; }
    focusRange(null);
    if (pendingReload) { pendingReload = false; location.reload(); }
  }
  // t is what a note is about: an element (elementTarget) or a highlight
  // (selectionTarget, highlightAt) — anchor, section, label, and a highlight's quote.
  function openPop(t, ev) {
    closePop();
    var a = t.a;
    var cstate = committedState(a);
    pop = document.createElement('div'); pop.className = 'rev-pop';
    // a highlight's note names its words: the page selection is gone once the
    // reviewer clicks into the note, so the focus highlight keeps them visible too
    var quote = t.quote ? '<div class="rev-quote">' + esc(t.label) + '</div>' : '';
    focusRange(t.range);
    // a committed, addressed/verified item gets Accept/Reopen (close the loop);
    // everything else gets the mark-up controls.
    if (live && (cstate === 'addressed' || cstate === 'verified')) {
      pop.innerHTML = quote + '<div class="rev-cap">Agent marked this ' + cstate + '.</div>' +
        '<textarea class="rev-note" placeholder="reopen note (optional)"></textarea>' +
        '<div class="rev-row"><button class="rev-accept">Accept</button><button class="rev-reopen">Reopen</button></div>';
      placePop(t, ev);
      pop.querySelector('.rev-accept').onclick = function () {
        post('/accept', { anchor: a }, function (data) { closePop(); reportSync(syncMessage('Accepted', data)); });
      };
      pop.querySelector('.rev-reopen').onclick = function () {
        post('/reopen', { anchor: a, note: pop.querySelector('.rev-note').value.trim() }, function (data) {
          closePop(); reportSync(syncMessage('Reopened', data));
        });
      };
      if (ev) ev.stopPropagation();
      return;
    }
    var existing = marks[a] || { status: 'request-change', note: '' };
    var btns = STATUS.map(function (s) {
      return '<button data-s="' + s.id + '" class="rev-s' + (existing.status === s.id ? ' on' : '') + '">' + s.glyph + ' ' + s.id + '</button>';
    }).join('');
    pop.innerHTML = quote + '<div class="rev-row">' + btns + '</div>' +
      '<textarea class="rev-note" placeholder="note (optional)">' + esc(existing.note || '') + '</textarea>' +
      '<div class="rev-row"><button class="rev-save">Save</button><button class="rev-del">Delete</button></div>';
    placePop(t, ev);
    var status = existing.status;
    pop.querySelectorAll('.rev-s').forEach(function (b) {
      b.onclick = function () { status = b.getAttribute('data-s'); pop.querySelectorAll('.rev-s').forEach(function (x) { x.classList.remove('on'); }); b.classList.add('on'); };
    });
    pop.querySelector('.rev-save').onclick = function () {
      marks[a] = { anchor: a, section: t.section, label: t.label, status: status, note: pop.querySelector('.rev-note').value.trim(), updated_at: nextStamp() };
      if (t.quote) marks[a].quote = t.quote;
      save(); paint(); closePop();
    };
    pop.querySelector('.rev-del').onclick = function () { removeMark(a); save(); paint(); closePop(); };
    if (ev) ev.stopPropagation();
  }
  // The note opens just below where the reviewer acted — under a new highlight's
  // text, else under the click — never over it: the second press of a
  // double-click lands where the first one opened a note, and has to reach the
  // word to select it.
  function placePop(t, ev) {
    document.body.appendChild(pop);
    var r = t.rect || { left: ev.clientX, bottom: ev.clientY };
    pop.style.top = (window.scrollY + r.bottom + 8) + 'px';
    pop.style.left = (window.scrollX + Math.min(r.left, window.innerWidth - 300)) + 'px';
  }

  // Review chrome is never a mark-up target. The rail and orphan list are <li>s
  // and would otherwise match SELECTOR, hijacking their own click handlers.
  var CHROME = '.rev-bar, .rev-rail, .rev-orphans, .rev-toast, .rev-sync, .rev-offline-bar';

  // --- highlights: a text selection comments on exactly the words selected.
  // The click that ends a drag or double-click carries a non-empty selection;
  // a plain click does not — that is what tells a highlight from an element
  // mark. A highlight anchors on (section heading, quoted text), not on its
  // element's full text, so an edit elsewhere in the section leaves it in place. ---
  var NOT_TEXT = CHROME + ', .rev-pop, .rev-glyph, .rev-hint, script, style, noscript, textarea';
  // The CSS Custom Highlight API tints ranges without touching the page's DOM.
  // Without it, highlights still anchor, list in the rail, and reopen on click.
  var HL = !!(window.CSS && CSS.highlights && window.Highlight);
  var HL_TINT = { 'approve': 'sage', 'request-change': 'amber', 'flag': 'red', 'comment': 'gold' };
  var qRanges = {}; // anchor -> highlight target, for each highlight the last paint found

  function quoteAnchor(section, quote) { return 'q' + fnv1a(norm(section) + '\u0000' + norm(quote)); }
  function elementTarget(el) { return { a: anchorFor(el), section: headingOf(el), label: labelFor(el) }; }

  // textIndex joins the page's text nodes — never review chrome, glyphs, or
  // scripts — into one string, remembering where each starts, so a quote is
  // found across inline markup and mapped back to a DOM Range. Text split by
  // anything but inline markup (cells, items, paragraphs, a <br> or an <img>)
  // is joined with a newline, even where the markup has no whitespace between
  // the elements, so the end of one cell and the start of the next never read
  // as one word.
  function textIndex() {
    var nodes = [], text = '', prev = null, brk = false;
    var w = document.createTreeWalker(body, NodeFilter.SHOW_TEXT | NodeFilter.SHOW_ELEMENT);
    for (var n = w.nextNode(); n; n = w.nextNode()) {
      // entering one splits the text around it; inlineBetween catches the
      // elements the walk leaves, which it never reports
      if (n.nodeType === 1) { if (!INLINE.test(n.tagName)) brk = true; continue; }
      if (!n.parentElement || n.parentElement.closest(NOT_TEXT)) continue;
      if (prev && (brk || !inlineBetween(prev, n))) text += '\n';
      brk = false;
      nodes.push({ node: n, start: text.length });
      text += n.data;
      prev = n;
    }
    return { nodes: nodes, text: text };
  }
  var INLINE = /^(A|ABBR|B|BDI|BDO|CITE|CODE|DATA|DEL|DFN|EM|I|INS|KBD|MARK|Q|S|SAMP|SMALL|SPAN|STRONG|SUB|SUP|TIME|U|VAR|WBR)$/;
  // inlineBetween: whether every element from each text node up to their
  // common ancestor is inline markup, i.e. the two run on as one line of text.
  function inlineBetween(a, b) {
    var up = [];
    for (var e = a.parentElement; e; e = e.parentElement) up.push(e);
    for (var f = b.parentElement; f && up.indexOf(f) < 0; f = f.parentElement) if (!INLINE.test(f.tagName)) return false;
    for (var i = 0; i < up.length && !up[i].contains(b); i++) if (!INLINE.test(up[i].tagName)) return false;
    return true;
  }
  // nodeAt: the entry holding offset pos. An end offset resolves to the entry
  // it closes, so a range never ends at offset 0 of the next node.
  function nodeAt(idx, pos, isEnd) {
    var lo = 0, hi = idx.nodes.length - 1;
    while (lo < hi) {
      var mid = (lo + hi + 1) >> 1, s = idx.nodes[mid].start;
      if (isEnd ? s < pos : s <= pos) lo = mid; else hi = mid - 1;
    }
    return idx.nodes[lo];
  }
  function rangeAt(idx, s, e) {
    var a = nodeAt(idx, s), b = nodeAt(idx, e, true), r = document.createRange();
    r.setStart(a.node, s - a.start); r.setEnd(b.node, e - b.start);
    return r;
  }
  function headingAt(idx, pos) {
    var x = nodeAt(idx, pos);
    if (x.h === undefined) x.h = norm(headingOf(x.node.parentElement));
    return x.h;
  }
  // quoteHits: every [start, end) where quote occurs under the section heading
  // (anywhere, when section is null), matched the way norm() compares text —
  // any whitespace run, any case — and, where the quote starts or ends with a
  // word character, only at a word boundary, so "retry" is not found in
  // "retrying".
  function quoteHits(idx, section, quote) {
    var q = quote.trim();
    var words = q.split(/\s+/).map(function (w) { return w.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'); });
    var re = new RegExp((/^\w/.test(q) ? '(^|\\W)' : '()') + words.join('\\s+') + (/\w$/.test(q) ? '(?!\\w)' : ''), 'gi');
    var want = section === null ? null : norm(section), hits = [], m;
    while ((m = re.exec(idx.text))) {
      var at = m.index + m[1].length; // past the boundary character the pattern consumed
      if (want === null || headingAt(idx, at) === want) hits.push([at, m.index + m[0].length]);
    }
    return hits;
  }

  // selectionTarget turns the page selection into a highlight target: null
  // when there is none (a plain click, or a stray drag across one character),
  // { refuse } when the words cannot anchor to one place.
  function selectionTarget() {
    var sel = window.getSelection();
    if (!sel || sel.isCollapsed || !sel.rangeCount) return null;
    var range = sel.getRangeAt(0), idx = textIndex(), s = -1, e = -1;
    idx.nodes.forEach(function (x) {
      if (!range.intersectsNode(x.node)) return;
      var from = x.node === range.startContainer ? range.startOffset : 0;
      var to = x.node === range.endContainer ? range.endOffset : x.node.data.length;
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
    var quote = idx.text.slice(s, e).replace(/\s+/g, ' ');
    // the agent reads a highlight as its words plus its section, so the words
    // must sit in one section (or all outside every section, like the TL;DR)
    // and name one place in it
    var first = nodeAt(idx, s).node.parentElement, last = nodeAt(idx, e, true).node.parentElement;
    if (first.closest('section[id], [data-ox-section]') !== last.closest('section[id], [data-ox-section]')) {
      return { refuse: 'Highlight text within one section to comment on it.' };
    }
    var section = headingOf(first);
    var n = quoteHits(idx, section, quote).length;
    if (n > 1) return { refuse: '“' + clip(quote, 40) + '” appears ' + n + ' times in this section — highlight a longer phrase so the comment points at one place.' };
    return { a: quoteAnchor(section, quote), section: section, label: clip(quote, 70), quote: quote, range: rangeAt(idx, s, e), rect: range.getBoundingClientRect() };
  }
  // highlightAt: the painted highlight under the click point, by its words' own
  // boxes. The caret can't tell: a click beside a highlight snaps it to the
  // highlight's edge, and a click on an image or a button leaves it where it was.
  function highlightAt(ev) {
    for (var a in qRanges) {
      var rects = qRanges[a].range.getClientRects();
      for (var i = 0; i < rects.length; i++) {
        var r = rects[i];
        if (ev.clientX >= r.left && ev.clientX <= r.right && ev.clientY >= r.top && ev.clientY <= r.bottom) return qRanges[a];
      }
    }
    return null;
  }
  // A selection counts only if this click made it: a drag, a double or triple
  // click, or a shift-click extending one. One left from before (Esc on its
  // note, text selected to copy) survives a click on a button or an image, and
  // that click marks the element, not the old words.
  var downAt = null;
  document.addEventListener('mousedown', function (e) { downAt = { x: e.clientX, y: e.clientY }; }, true);
  function madeSelection(ev) {
    if (ev.detail > 1 || ev.shiftKey) return true;
    return ev.detail === 1 && !!downAt && Math.abs(ev.clientX - downAt.x) + Math.abs(ev.clientY - downAt.y) > 3;
  }

  // paintQuotes finds each highlight's words on the page, records it for clicks
  // and the rail, and tints it the way element marks are colored: your unsent
  // mark by its status, else the committed state.
  function paintQuotes(seen) {
    qRanges = {};
    var want = {};
    Object.keys(committed).forEach(function (a) { committed[a].forEach(function (c) { if (c.quote && !want[a]) want[a] = c; }); });
    Object.keys(marks).forEach(function (a) { if (marks[a].quote) want[a] = marks[a]; });
    var tints = {}, idx = null;
    Object.keys(want).forEach(function (a) {
      var m = want[a];
      if (!norm(m.quote)) return; // a blank quote builds an empty pattern, which never advances
      idx = idx || textIndex();
      var hit = quoteHits(idx, m.section || '', m.quote)[0];
      // a renamed heading moves no words: accept them anywhere, if only once
      var anywhere = hit ? null : quoteHits(idx, null, m.quote);
      if (anywhere && anywhere.length === 1) hit = anywhere[0];
      if (!hit) return; // its words changed: a committed one surfaces as an orphan
      var t = qRanges[a] = { a: a, section: m.section || '', label: m.label || clip(m.quote, 70), quote: m.quote, range: rangeAt(idx, hit[0], hit[1]) };
      seen[a] = true;
      var st = marks[a] ? '' : repOf(committed[a]).state;
      var tint = marks[a] ? HL_TINT[marks[a].status] || 'gold'
        : (st === 'addressed' || st === 'verified') ? 'sage' : st === 'wontfix' ? 'faint' : 'amber';
      (tints[tint] = tints[tint] || []).push(t.range);
    });
    if (!HL) return;
    ['sage', 'amber', 'red', 'gold', 'faint'].forEach(function (c) {
      CSS.highlights.delete('rev-q-' + c);
      if (!tints[c]) return;
      var h = new Highlight();
      tints[c].forEach(function (r) { h.add(r); });
      CSS.highlights.set('rev-q-' + c, h);
    });
  }
  // focusRange tints one range in the focus color — the words an open note is
  // about, or the highlight a rail row jumped to; null clears it.
  var focused = null;
  function focusRange(r) {
    focused = r || null;
    if (!HL) return;
    if (!r) { CSS.highlights.delete('rev-q-focus'); return; }
    var h = new Highlight();
    h.add(r);
    CSS.highlights.set('rev-q-focus', h);
  }
  function flashRange(r) {
    focusRange(r);
    setTimeout(function () { if (focused === r) focusRange(null); }, 1200); // a note opened since keeps its own focus
  }

  function onClick(ev) {
    if (!on) {
      // outside review mode only a rail row opens a note; a click elsewhere
      // closes it, as it would in review mode
      if (pop && !pop.contains(ev.target)) closePop();
      return;
    }
    if (pop && pop.contains(ev.target)) return;
    // any click outside the note dismisses it; only page content opens one
    if (ev.target.closest(CHROME)) { closePop(); return; }
    // a selection means a drag or double-click just ended: comment on those
    // words. A plain click on highlighted words reopens that highlight; any
    // other click marks the element under it.
    var t = (madeSelection(ev) && selectionTarget()) || highlightAt(ev);
    if (t && t.refuse) { ev.preventDefault(); closePop(); toast(t.refuse); return; }
    if (!t) {
      var el = ev.target.closest(SELECTOR);
      if (!el) { closePop(); return; }
      t = elementTarget(el);
    }
    ev.preventDefault();
    openPop(t, ev);
  }

  // Submit snapshots the unsent marks into the outbox BEFORE sending, then
  // sends that outbox; a Submit (or page load) that finds one already there
  // resends it as-is, so its id — the server's dedupe key — never changes.
  function submit() {
    if (sending) return;
    var box = live ? readOutbox() : null;
    if (!box) {
      var items = Object.keys(marks).map(function (k) { return wireItem(marks[k]); });
      if (!items.length) { showSync('info', 'No marks yet. Toggle Review, click a section, leave a note.'); return; }
      if (!reviewer) { askName('Your name, so teammates know whose feedback this is:', submit); return; }
      var stamps = {};
      Object.keys(marks).forEach(function (k) { stamps[k] = stampOf(marks[k]); });
      box = { id: newRoundId(), items: items, reviewer: reviewer, created_at: new Date().toISOString(), stamps: stamps };
      if (!live) { exportJSON(box); return; }
      writeOutbox(box); // a full storage still sends; only a reload mid-flight would lose the id
    }
    send(box);
  }
  function wireItem(m) {
    var o = {};
    Object.keys(m).forEach(function (k) { if (k !== 'updated_at') o[k] = m[k]; });
    return o;
  }
  function send(box) {
    sending = true; paintSubmit();
    showSync('pending', 'Sending…');
    post('/feedback', { id: box.id, slug: slug, reviewer: box.reviewer, items: box.items }, function (data) {
      sending = false;
      if (!roundAcked(box, data)) {
        paintSubmit();
        showSync('err', 'Not confirmed — the server acknowledged a different round. Your marks are kept.', [{ label: 'Retry', fn: submit }]);
        return;
      }
      // marks first, outbox second: a crash between them leaves an outbox
      // whose resend the server reports as a duplicate, never lost marks
      var acked = ackState(mergeState({ marks: marks, tombs: tombs }, readStored()), box);
      marks = acked.marks; tombs = acked.tombs; save();
      var cur = readOutbox();
      if (cur && cur.id === box.id) writeOutbox(null);
      paint(); paintSubmit();
      reportSync(syncMessage('Sent', data));
    }, function (msg, isOffline) {
      sending = false; paintSubmit();
      if (isOffline) showSync('warn', 'Review server offline — your feedback is queued in this browser and sends when the server is back.');
      else showSync('err', 'Not sent (' + msg + '). Your marks are kept.', [{ label: 'Retry', fn: submit }]);
    });
  }
  function approve() {
    if (!live) { showSync('info', 'Approve is available in the live `ox plan review` loop.'); return; }
    showSync('ask', 'Approve this plan? This stamps it approved and closes the review loop.', [
      { label: 'Approve', fn: function () {
        showSync('pending', 'Approving…');
        post('/approve', {}, function (data) {
          var m = syncMessage('Approved', data);
          if (m.kind !== 'err') m.text += ' — you can close this tab.';
          reportSync(m);
        });
      } },
      { label: 'Cancel', fn: hideSync }
    ]);
  }

  function exportJSON(box) {
    // a re-export of unchanged marks keeps its id, so `ox plan feedback apply`
    // run twice on the same feedback lands one round
    var prev = readJSON(EXPORT_KEY);
    var id = prev && prev.id && sameItems(prev.items, box.items) ? prev.id : box.id;
    try { localStorage.setItem(EXPORT_KEY, JSON.stringify({ id: id, items: box.items })); } catch (e) {}
    var json = JSON.stringify({ id: id, slug: slug, reviewer: box.reviewer, items: box.items }, null, 2);
    var blob = new Blob([json], { type: 'application/json' });
    var url = URL.createObjectURL(blob);
    var a = document.createElement('a'); a.href = url; a.download = slug + '-feedback.json'; a.click();
    URL.revokeObjectURL(url);
    if (navigator.clipboard) navigator.clipboard.writeText(json).catch(function () {});
    showSync('ok', 'Saved ' + slug + '-feedback.json (and copied to clipboard). Hand it to the agent, or run: ox plan feedback apply ' + slug + ' --from ' + slug + '-feedback.json');
  }

  // --- sync status: one inline, non-modal line that says what actually
  // happened to the reviewer's feedback. Never alert/confirm/prompt — those
  // are blocked inside the iframe the web app embeds plans in. ---
  var syncEl = null, syncTimer = null;
  function hideSync() { if (syncTimer) { clearTimeout(syncTimer); syncTimer = null; } if (syncEl) { syncEl.remove(); syncEl = null; } }
  // kind: ok | warn | err | info | pending | ask. actions: [{label, fn(inputValue)}];
  // input: initial text for a one-line field (asking for a name).
  function showSync(kind, text, actions, input) {
    hideSync();
    var el = syncEl = document.createElement('div');
    el.className = 'rev-sync ' + kind;
    el.setAttribute('role', kind === 'err' ? 'alert' : 'status');
    var msg = document.createElement('span'); msg.className = 'rev-sync-msg'; msg.textContent = text; el.appendChild(msg);
    var field = null;
    if (input !== undefined) {
      field = document.createElement('input'); field.className = 'rev-sync-input'; field.value = input; el.appendChild(field);
    }
    (actions || []).forEach(function (a) {
      var b = document.createElement('button'); b.className = 'rev-sync-act'; b.textContent = a.label;
      b.onclick = function () { a.fn(field ? field.value.trim() : undefined); };
      el.appendChild(b);
    });
    if (kind !== 'pending' && kind !== 'ask') {
      var x = document.createElement('button'); x.className = 'rev-sync-x'; x.textContent = '×'; x.title = 'Dismiss'; x.onclick = hideSync;
      el.appendChild(x);
    }
    document.body.appendChild(el);
    if (field) {
      field.focus();
      field.onkeydown = function (e) { if (e.key === 'Enter' && actions && actions[0]) { e.preventDefault(); actions[0].fn(field.value.trim()); } };
    }
    // good news fades; a warning lingers; an error or a question waits for the reviewer
    var ttl = { ok: 10000, info: 8000, warn: 20000 }[kind];
    if (ttl) syncTimer = setTimeout(function () { if (syncEl === el) hideSync(); }, ttl);
  }
  // reportSync shows a send result and keeps it for one reload: the server's
  // SSE reload usually lands right after the response, and would wipe it.
  function reportSync(m) {
    showSync(m.kind, m.text);
    try { sessionStorage.setItem(SYNC_KEY, JSON.stringify({ kind: m.kind, text: m.text, at: Date.now() })); } catch (e) {}
  }
  function restoreSync() {
    try {
      var s = JSON.parse(sessionStorage.getItem(SYNC_KEY) || 'null');
      sessionStorage.removeItem(SYNC_KEY);
      // 30s: long enough to span a slow reload, short enough that an old
      // result never reads as the outcome of something newer
      if (s && Date.now() - s.at < 30000) showSync(s.kind, s.text);
    } catch (e) {}
  }
  function askName(q, then) {
    showSync('ask', q, [
      { label: 'Save', fn: function (n) {
        if (!n) return;
        reviewer = n;
        try { localStorage.setItem('ox-plan-reviewer', reviewer); } catch (e) {}
        hideSync(); updateWho(); paint();
        if (then) then();
      } },
      { label: 'Cancel', fn: hideSync }
    ], reviewer);
  }

  // --- connection state (live mode): the page must never LOOK live when the
  // server is gone. Offline = red pill + sticky banner + refused sends; marks
  // keep saving to localStorage and are restored after a restart, so nothing a
  // reviewer wrote is ever lost. ---
  var connEl = null, offlineBar = null, toastEl = null;
  function restartCmd() { return 'ox plan review ' + slug; }
  function paintConn() {
    if (!connEl) return;
    connEl.textContent = offline ? '● offline' : '● live';
    connEl.className = 'rev-conn ' + (offline ? 'off' : 'ok');
    connEl.title = offline ? 'Review server unreachable — feedback is NOT being saved' : 'Connected to the review server';
  }
  function setOffline(down) {
    if (offline === down) return;
    offline = down;
    body.classList.toggle('rev-offline', down);
    paintConn();
    if (down) { showOfflineBar(); startProbe(); }
    else { hideOfflineBar(); stopProbe(); }
  }
  function showOfflineBar() {
    hideOfflineBar();
    var n = Object.keys(marks).length;
    offlineBar = document.createElement('div');
    offlineBar.className = 'rev-offline-bar';
    offlineBar.innerHTML = '<strong>⚠ Review server offline</strong> — new feedback is NOT being saved. ' +
      (n ? n + ' unsent mark(s) are' : 'Your marks are') + ' kept in this browser and restored on reconnect. Restart: ' +
      '<code>' + esc(restartCmd()) + '</code> <button class="rev-offline-copy">copy</button>';
    document.body.appendChild(offlineBar);
    offlineBar.querySelector('.rev-offline-copy').onclick = function () {
      var b = this;
      if (navigator.clipboard) navigator.clipboard.writeText(restartCmd()).then(function () { b.textContent = 'copied ✓'; }).catch(function () {});
    };
  }
  function hideOfflineBar() { if (offlineBar) { offlineBar.remove(); offlineBar = null; } }
  function offlineNotice() {
    var n = Object.keys(marks).length;
    showSync('err', 'The review server is offline — feedback can NOT be saved right now. ' +
      (n ? 'Your ' + n + ' unsent mark(s) stay in this browser and will be restored. ' : '') +
      'Restart the loop with: ' + restartCmd());
  }
  function toast(msg) {
    if (toastEl) toastEl.remove();
    var el = toastEl = document.createElement('div');
    el.className = 'rev-toast';
    el.textContent = msg;
    document.body.appendChild(el);
    // this toast only: a newer one may have replaced it before the timer fires
    setTimeout(function () { el.remove(); if (toastEl === el) toastEl = null; }, 8000);
  }
  // While offline, poll /healthz; the instant the server is back, reload —
  // stable port + persisted token mean the same origin serves fresh state, and
  // paint() restores unsent marks from localStorage. Covers the case where the
  // EventSource died permanently (e.g. a 403 from a rotated token).
  function startProbe() {
    if (probeTimer) return;
    probeTimer = setInterval(function () {
      fetch(base + '/healthz', { cache: 'no-store' })
        .then(function (r) { return r.ok ? r.json() : null; })
        .then(function (j) { if (j && j.app === 'ox-plan-review') reloadWhenIdle(); })
        .catch(function () {});
    }, 3000);
  }
  function stopProbe() { if (probeTimer) { clearInterval(probeTimer); probeTimer = null; } }
  function reloadWhenIdle() { if (pop) { pendingReload = true; return; } location.reload(); }

  // controls
  var bar = document.createElement('div');
  bar.className = 'rev-bar';
  bar.innerHTML = (live ? '<span class="rev-conn"></span>' : '') +
    '<button class="rev-toggle" title="Toggle review mode">Review</button><span class="rev-count"></span>' +
    (live ? '<button class="rev-who" title="Set the name teammates see on your comments"></button>' : '') +
    '<button class="rev-submit" title="Send feedback to the agent">' + (live ? 'Submit' : 'Export') + '</button>' +
    (live ? '<button class="rev-approve" title="Approve and close the loop">Approve</button>' : '');
  document.body.appendChild(bar);
  var countEl = bar.querySelector('.rev-count');
  connEl = bar.querySelector('.rev-conn');
  paintConn();
  var whoEl = bar.querySelector('.rev-who');
  function updateWho() { if (whoEl) whoEl.textContent = reviewer ? ('You: ' + reviewer) : 'Set name'; }
  updateWho();
  if (whoEl) whoEl.onclick = function () { askName('Your name (shown to teammates on this plan):'); };
  var submitEl = bar.querySelector('.rev-submit');
  function paintSubmit() {
    if (!live) return;
    submitEl.disabled = sending; // a second click while one send is in flight would race it
    submitEl.textContent = sending ? 'Sending…' : 'Submit';
  }
  // Review is a mode: while on, clicks mark up instead of navigating. A mode has
  // to announce itself on entry and keep its exit in view — otherwise the only
  // signal is a green button and nothing visibly changes until a hover.
  var toggleEl = bar.querySelector('.rev-toggle');
  function setReview(next, silent) {
    on = next; body.classList.toggle('rev-on', on); toggleEl.classList.toggle('on', on);
    toggleEl.textContent = on ? 'Exit review' : 'Review';
    toggleEl.title = on ? 'Leave review mode (Esc)' : 'Enter review mode (r)';
    if (on && !silent) toast('Review mode — click any section, item, or row to mark it up, or highlight text to comment on it. Esc or Exit review to leave.');
    if (!on) { closePop(); if (toastEl) { toastEl.remove(); toastEl = null; } }
    try { localStorage.setItem('ox-plan-rev-seen', '1'); } catch (e) {}
    try { if (on) sessionStorage.setItem(ON_KEY, '1'); else sessionStorage.removeItem(ON_KEY); } catch (e) {}
    if (hintBubble) { hintBubble.remove(); hintBubble = null; }
    renderRail();
  }
  toggleEl.onclick = function () { setReview(!on); };
  submitEl.onclick = submit;
  if (live) bar.querySelector('.rev-approve').onclick = approve;
  document.addEventListener('click', onClick, true);
  // Keyboard, here rather than scaffold.js so authored HTML plans (chrome.js
  // adds no key map) get the same keys. Esc peels one layer at a time — an
  // open note first, then the mode — and fires while typing in the note too;
  // that is the point of Esc. `r` toggles the mode from anywhere but a text field.
  // Keys are shared with the page: one an earlier listener marked handled (an
  // authored inspector closing on Esc) is left alone, and one acted on here is
  // marked handled so later listeners that honor defaultPrevented skip it.
  function typing(e) { var t = e.target; return !!t && (t.tagName === 'TEXTAREA' || t.tagName === 'INPUT' || t.isContentEditable); }
  document.addEventListener('keydown', function (e) {
    if (e.defaultPrevented) return;
    if (e.key === 'Escape') {
      if (pop) { closePop(); e.preventDefault(); }
      else if (on) { setReview(false); e.preventDefault(); }
      return;
    }
    if (e.key === 'r' && !typing(e) && !e.metaKey && !e.ctrlKey && !e.altKey) { setReview(!on); e.preventDefault(); }
  });

  // first-visit discoverability: a one-time pointer at the Review toggle.
  var hintBubble = null;
  try {
    if (!localStorage.getItem('ox-plan-rev-seen')) {
      hintBubble = document.createElement('div');
      hintBubble.className = 'rev-hint';
      hintBubble.textContent = '← Click Review to mark up this plan';
      document.body.appendChild(hintBubble);
      setTimeout(function () { if (hintBubble) { hintBubble.remove(); hintBubble = null; } }, 9000);
    }
  } catch (e) {}

  // live reload as the agent addresses items + re-renders
  if (live && window.EventSource) {
    try {
      var es = new EventSource(base + '/events?t=' + encodeURIComponent(token));
      es.onopen = function () {
        // recovered from a dead server: reload for fresh state (the browser
        // repaints unsent marks from localStorage after the reload).
        if (offline) { setOffline(false); reloadWhenIdle(); return; }
        paintConn();
      };
      es.onerror = function () { setOffline(true); };
      es.onmessage = function () {
        // a reload mid-POST would drop its result (the server writes the file,
        // and the watcher fires, before a slow commit+push answers): wait for it
        if (posting) { reloadAfterPost = true; return; }
        // don't yank the page while the reviewer is mid-note; reload on close
        if (pop) { pendingReload = true; return; }
        location.reload();
      };
    } catch (e) {}
  }

  // offline shell: cache the page so a reload while the server is down still
  // shows the plan (this layer then flips to disconnected mode instead of the
  // browser's connection-error page).
  if (live && 'serviceWorker' in navigator) {
    try { navigator.serviceWorker.register('/sw.js').catch(function () {}); } catch (e) {}
  }

  // Another tab on this plan saved, deleted, or sent marks: fold its writes in
  // (save() already merged ours into storage) and repaint. e.key is null when
  // storage was cleared wholesale.
  window.addEventListener('storage', function (e) {
    if (e.key === null || e.key === KEY || e.key === TOMB_KEY) {
      var m = mergeState({ marks: marks, tombs: tombs }, readStored());
      marks = m.marks; tombs = m.tombs;
      paint();
    }
  });

  // unsent marks that survived a server restart or reload — tell the reviewer.
  var pendingBox = live ? readOutbox() : null;
  if (live && Object.keys(marks).length && !pendingBox) {
    toast(Object.keys(marks).length + ' unsent mark(s) restored — Submit to send them.');
  }

  paint();
  restoreSync();
  // A submission this page never saw acknowledged — a reload, a crash, or the
  // server dropping mid-send — is resent with its original id; the server
  // answers a round it already has as a duplicate, so this can't double-post.
  if (pendingBox) send(pendingBox);
  // A live reload — the agent addressed an item, or the server came back — must
  // not drop a reviewer mid-review back to reading mode. Restored silently: the
  // reviewer did not just enter, so no entry toast.
  try { if (sessionStorage.getItem(ON_KEY)) setReview(true, true); } catch (e) {}
})();
