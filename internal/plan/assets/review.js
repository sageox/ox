// ox plan — in-document review LOOP layer. Vanilla JS, zero deps, file://-safe.
// Toggle Review, mark up a section/risk/decision (approve / request-change /
// flag / comment + note), Submit. When served by `ox plan review` it also:
//   - live-reloads via SSE as the agent addresses items + re-renders,
//   - lets the reviewer Accept (verify) or Reopen an addressed item inline,
//   - offers a top-level Approve to close the loop,
//   - surfaces orphaned notes whose anchored text changed (never lose feedback).
// Anchors are a CONTENT hash (section heading + element text) so a mark survives
// a re-render. Static (file://) mode falls back to clipboard export. NOT
// Agentation (license-clean). Inert until toggled.
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

  var marks = load();
  var committed = parseCommitted();
  var reviewer = (localStorage.getItem('ox-plan-reviewer') || '').trim(); // multi-user: who you are
  var on = false;
  var pendingReload = false;
  var offline = false; // live server unreachable — nothing can be saved until it returns
  var probeTimer = null;

  function load() { try { return JSON.parse(localStorage.getItem(KEY)) || {}; } catch (e) { return {}; } }
  function save() { try { localStorage.setItem(KEY, JSON.stringify(marks)); } catch (e) {} }
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
  function post(path, payload, ok) {
    if (offline) { offlineNotice(); return; }
    fetch(base + path, { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Review-Token': token }, body: JSON.stringify(payload) })
      .then(function (r) {
        if (!r.ok) throw new Error('HTTP ' + r.status);
        return r.json().catch(function () { return {}; });
      })
      .then(function (data) { if (ok) ok(data || {}); })
      .catch(function (e) {
        // a network-level failure means the server is gone — flip to
        // disconnected mode (marks stay in localStorage; nothing is lost)
        // rather than surfacing a raw fetch error.
        if (e instanceof TypeError) { setOffline(true); offlineNotice(); return; }
        alert('Request failed: ' + e.message);
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
  function labelFor(el) { var t = (el.textContent || '').replace(/\s+/g, ' ').trim(); return t.length > 70 ? t.slice(0, 69) + '…' : t; }
  function committedState(el) { var arr = committed[anchorFor(el)] || []; return arr.length ? repOf(arr).state : ''; }

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
      if (c) seen[a] = true;
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
    // document order; each row carries its live element for scroll-to. Multi-user:
    // one row per reviewer's committed mark on an element, plus your unsent local mark.
    var rows = [];
    document.querySelectorAll(SELECTOR).forEach(function (el) {
      var a = anchorFor(el), m = marks[a], carr = committed[a] || [];
      carr.forEach(function (c) {
        rows.push({
          el: el, status: c.status, note: c.note,
          section: c.section || headingOf(el), label: c.label || labelFor(el),
          state: c.state, reviewer: c.reviewer || '', unsent: false
        });
      });
      if (m) {
        rows.push({
          el: el, status: m.status, note: m.note,
          section: m.section || headingOf(el), label: m.label || labelFor(el),
          state: '', reviewer: reviewer || '(you)', unsent: true
        });
      }
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
      rail.innerHTML = '<div class="rev-rail-h">Comments' + hide + '</div><div class="rev-rail-empty">Click any section, risk, or row to leave a comment.</div>';
      return;
    }
    var html = '<div class="rev-rail-h">Comments <span class="rev-rail-n">' + rows.length + '</span>' + hide + '</div><ul class="rev-rail-list">';
    rows.forEach(function (r, i) {
      var tag = '';
      if (r.unsent) tag = '<span class="rev-rail-tag unsent">unsent</span>';
      else if (r.state && r.state !== 'open') tag = '<span class="rev-rail-tag ' + esc(r.state) + '">' + esc(r.state) + '</span>';
      var body = r.note ? '<div class="rev-rail-note">' + esc(r.note) + '</div>'
        : '<div class="rev-rail-note muted">' + esc(r.label) + '</div>';
      var whoTag = r.reviewer ? '<span class="rev-rail-who">' + esc(r.reviewer) + '</span>' : '';
      html += '<li class="rev-rail-item" data-i="' + i + '" data-status="' + esc(r.status) + '">' +
        '<span class="rev-rail-glyph">' + glyphFor(r.status) + '</span>' +
        '<div class="rev-rail-body"><div class="rev-rail-sec"><span class="rev-rail-sec-t">' + esc(r.section || '(plan)') + '</span>' + whoTag + tag + '</div>' + body + '</div></li>';
    });
    html += '</ul>';
    rail.innerHTML = html;
    rail.querySelectorAll('.rev-rail-item').forEach(function (li) {
      li.onclick = function () {
        var r = rows[+li.getAttribute('data-i')];
        if (!r || !r.el) return;
        r.el.scrollIntoView({ behavior: 'smooth', block: 'center' });
        flashEl(r.el);
      };
    });
  }

  var pop;
  function closePop() {
    if (pop) { pop.remove(); pop = null; }
    if (pendingReload) { pendingReload = false; location.reload(); }
  }
  function openPop(el, ev) {
    closePop();
    var a = anchorFor(el);
    var cstate = committedState(el);
    pop = document.createElement('div'); pop.className = 'rev-pop';
    // a committed, addressed/verified item gets Accept/Reopen (close the loop);
    // everything else gets the mark-up controls.
    if (live && (cstate === 'addressed' || cstate === 'verified')) {
      pop.innerHTML = '<div class="rev-cap">Agent marked this ' + cstate + '.</div>' +
        '<textarea class="rev-note" placeholder="reopen note (optional)"></textarea>' +
        '<div class="rev-row"><button class="rev-accept">Accept</button><button class="rev-reopen">Reopen</button></div>';
      placePop(el);
      pop.querySelector('.rev-accept').onclick = function () { post('/accept', { anchor: a }, function () { closePop(); }); };
      pop.querySelector('.rev-reopen').onclick = function () {
        post('/reopen', { anchor: a, note: pop.querySelector('.rev-note').value.trim() }, function (data) {
          closePop();
          if (data.notified === false) toast('Reopened — but the plan’s authoring coworker could not be notified automatically. Tell them directly.');
        });
      };
      if (ev) ev.stopPropagation();
      return;
    }
    var existing = marks[a] || { status: 'request-change', note: '' };
    var btns = STATUS.map(function (s) {
      return '<button data-s="' + s.id + '" class="rev-s' + (existing.status === s.id ? ' on' : '') + '">' + s.glyph + ' ' + s.id + '</button>';
    }).join('');
    pop.innerHTML = '<div class="rev-row">' + btns + '</div>' +
      '<textarea class="rev-note" placeholder="note (optional)">' + esc(existing.note || '') + '</textarea>' +
      '<div class="rev-row"><button class="rev-save">Save</button><button class="rev-del">Delete</button></div>';
    placePop(el);
    var status = existing.status;
    pop.querySelectorAll('.rev-s').forEach(function (b) {
      b.onclick = function () { status = b.getAttribute('data-s'); pop.querySelectorAll('.rev-s').forEach(function (x) { x.classList.remove('on'); }); b.classList.add('on'); };
    });
    pop.querySelector('.rev-save').onclick = function () {
      marks[a] = { anchor: a, section: headingOf(el), label: labelFor(el), status: status, note: pop.querySelector('.rev-note').value.trim() };
      save(); paint(); closePop();
    };
    pop.querySelector('.rev-del').onclick = function () { delete marks[a]; save(); paint(); closePop(); };
    if (ev) ev.stopPropagation();
  }
  function placePop(el) {
    document.body.appendChild(pop);
    var r = el.getBoundingClientRect();
    pop.style.top = (window.scrollY + r.top) + 'px';
    pop.style.left = (window.scrollX + Math.min(r.left, window.innerWidth - 300)) + 'px';
  }

  // Review chrome is never a mark-up target. The rail and orphan list are <li>s
  // and would otherwise match SELECTOR, hijacking their own click handlers.
  var CHROME = '.rev-bar, .rev-rail, .rev-orphans, .rev-toast, .rev-offline-bar';
  function onClick(ev) {
    if (!on) return;
    if (pop && pop.contains(ev.target)) return;
    // any click outside the note dismisses it; only page content opens one
    if (ev.target.closest(CHROME)) { closePop(); return; }
    var el = ev.target.closest(SELECTOR);
    if (!el) { closePop(); return; }
    ev.preventDefault();
    openPop(el, ev);
  }

  function ensureReviewer() {
    if (!reviewer) {
      var n = (prompt('Your name (shown to teammates reviewing this plan):', '') || '').trim();
      if (n) { reviewer = n; try { localStorage.setItem('ox-plan-reviewer', reviewer); } catch (e) {} if (typeof updateWho === 'function') updateWho(); }
    }
    return reviewer;
  }
  function submit() {
    var items = Object.keys(marks).map(function (k) { return marks[k]; });
    if (!items.length) { alert('No marks yet. Toggle Review, click a section, leave a note.'); return; }
    var who = ensureReviewer();
    if (!who) { alert('Set your name first (the "Set name" button in the bar) — feedback is attributed per reviewer.'); return; }
    var p = { slug: slug, reviewer: who, items: items };
    if (live) {
      post('/feedback', p, function (data) {
        marks = {}; save(); /* SSE reload will repaint */
        if (data.notified === false) toast('Sent — but the plan’s authoring coworker could not be notified automatically. Tell them directly.');
      });
      return;
    }
    exportJSON(p);
  }
  function approve() {
    if (!live) { alert('Approve is available in the live `ox plan review` loop.'); return; }
    if (!confirm('Approve this plan? This stamps it approved and closes the review loop.')) return;
    post('/approve', {}, function () { alert('Plan approved ✓ — you can close this tab.'); });
  }

  function exportJSON(p) {
    var json = JSON.stringify(p, null, 2);
    var blob = new Blob([json], { type: 'application/json' });
    var url = URL.createObjectURL(blob);
    var a = document.createElement('a'); a.href = url; a.download = slug + '-feedback.json'; a.click();
    URL.revokeObjectURL(url);
    if (navigator.clipboard) navigator.clipboard.writeText(json).catch(function () {});
    alert('Saved ' + slug + '-feedback.json (and copied to clipboard).\nHand it to the agent, or run:\n  ox plan feedback apply ' + slug + ' --from ' + slug + '-feedback.json');
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
    alert('The review server is offline — feedback can NOT be saved right now.\n' +
      (n ? 'Your ' + n + ' unsent mark(s) stay in this browser and will be restored.\n' : '') +
      'Restart the loop with:\n  ' + restartCmd());
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
  if (whoEl) whoEl.onclick = function () { var n = (prompt('Your name (shown to teammates on this plan):', reviewer) || '').trim(); if (n) { reviewer = n; try { localStorage.setItem('ox-plan-reviewer', reviewer); } catch (e) {} updateWho(); paint(); } };
  // Review is a mode: while on, clicks mark up instead of navigating. A mode has
  // to announce itself on entry and keep its exit in view — otherwise the only
  // signal is a green button and nothing visibly changes until a hover.
  var toggleEl = bar.querySelector('.rev-toggle');
  function setReview(next, silent) {
    on = next; body.classList.toggle('rev-on', on); toggleEl.classList.toggle('on', on);
    toggleEl.textContent = on ? 'Exit review' : 'Review';
    toggleEl.title = on ? 'Leave review mode (Esc)' : 'Enter review mode (r)';
    if (on && !silent) toast('Review mode — click any section, item, or row to mark it up. Esc or Exit review to leave.');
    if (!on) { closePop(); if (toastEl) { toastEl.remove(); toastEl = null; } }
    try { localStorage.setItem('ox-plan-rev-seen', '1'); } catch (e) {}
    try { if (on) sessionStorage.setItem(ON_KEY, '1'); else sessionStorage.removeItem(ON_KEY); } catch (e) {}
    if (hintBubble) { hintBubble.remove(); hintBubble = null; }
    renderRail();
  }
  toggleEl.onclick = function () { setReview(!on); };
  bar.querySelector('.rev-submit').onclick = submit;
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

  // unsent marks that survived a server restart or reload — tell the reviewer.
  if (live && Object.keys(marks).length) {
    toast(Object.keys(marks).length + ' unsent mark(s) restored — Submit to send them.');
  }

  paint();
  // A live reload — the agent addressed an item, or the server came back — must
  // not drop a reviewer mid-review back to reading mode. Restored silently: the
  // reviewer did not just enter, so no entry toast.
  try { if (sessionStorage.getItem(ON_KEY)) setReview(true, true); } catch (e) {}
})();
