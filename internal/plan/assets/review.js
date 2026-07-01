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
  var STATUS = [
    { id: 'approve', glyph: '✓' },
    { id: 'request-change', glyph: '✎' },
    { id: 'flag', glyph: '⚑' },
    { id: 'comment', glyph: '◌' }
  ];
  var SELECTOR = 'section[id], li, tr, .ox-chip, .stat, .bar-row';

  var marks = load();
  var committed = parseCommitted();
  var reviewer = (localStorage.getItem('ox-plan-reviewer') || '').trim(); // multi-user: who you are
  var on = false;
  var pendingReload = false;

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
    fetch(base + path, { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Review-Token': token }, body: JSON.stringify(payload) })
      .then(function (r) { if (!r.ok) throw new Error('HTTP ' + r.status); if (ok) ok(); })
      .catch(function (e) { alert('Request failed: ' + e.message); });
  }

  function fnv1a(s) { var h = 0x811c9dc5; for (var i = 0; i < s.length; i++) { h ^= s.charCodeAt(i); h = (h * 0x01000193) >>> 0; } return ('0000000' + h.toString(16)).slice(-8); }
  function norm(s) { return (s || '').replace(/\s+/g, ' ').trim().toLowerCase(); }
  function headingOf(el) { var sec = el.closest('section[id]'); if (!sec) return ''; var h = sec.querySelector('h2'); return h ? h.textContent : sec.id; }
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
    if (!rail) { rail = document.createElement('aside'); rail.className = 'rev-rail'; document.body.appendChild(rail); }
    var rows = railRows();
    // hidden when there's nothing to read and we're not actively reviewing.
    if (!on && !rows.length) { rail.style.display = 'none'; rail.innerHTML = ''; return; }
    rail.style.display = '';
    if (!rows.length) {
      rail.innerHTML = '<div class="rev-rail-h">Comments</div><div class="rev-rail-empty">Click any section, risk, or row to leave a comment.</div>';
      return;
    }
    var html = '<div class="rev-rail-h">Comments <span class="rev-rail-n">' + rows.length + '</span></div><ul class="rev-rail-list">';
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
      pop.querySelector('.rev-reopen').onclick = function () { post('/reopen', { anchor: a, note: pop.querySelector('.rev-note').value.trim() }, function () { closePop(); }); };
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

  function onClick(ev) {
    if (!on) return;
    if (pop && pop.contains(ev.target)) return;
    var el = ev.target.closest(SELECTOR);
    if (!el) return;
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
      post('/feedback', p, function () { marks = {}; save(); /* SSE reload will repaint */ });
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

  // controls
  var bar = document.createElement('div');
  bar.className = 'rev-bar';
  bar.innerHTML = '<button class="rev-toggle" title="Toggle review mode">Review</button><span class="rev-count"></span>' +
    (live ? '<button class="rev-who" title="Set the name teammates see on your comments"></button>' : '') +
    '<button class="rev-submit" title="Send feedback to the agent">' + (live ? 'Submit' : 'Export') + '</button>' +
    (live ? '<button class="rev-approve" title="Approve and close the loop">Approve</button>' : '');
  document.body.appendChild(bar);
  var countEl = bar.querySelector('.rev-count');
  var whoEl = bar.querySelector('.rev-who');
  function updateWho() { if (whoEl) whoEl.textContent = reviewer ? ('You: ' + reviewer) : 'Set name'; }
  updateWho();
  if (whoEl) whoEl.onclick = function () { var n = (prompt('Your name (shown to teammates on this plan):', reviewer) || '').trim(); if (n) { reviewer = n; try { localStorage.setItem('ox-plan-reviewer', reviewer); } catch (e) {} updateWho(); paint(); } };
  bar.querySelector('.rev-toggle').onclick = function () {
    on = !on; body.classList.toggle('rev-on', on); this.classList.toggle('on', on);
    if (!on) closePop();
    try { localStorage.setItem('ox-plan-rev-seen', '1'); } catch (e) {}
    if (hintBubble) { hintBubble.remove(); hintBubble = null; }
    renderRail();
  };
  bar.querySelector('.rev-submit').onclick = submit;
  if (live) bar.querySelector('.rev-approve').onclick = approve;
  document.addEventListener('click', onClick, true);

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
      es.onmessage = function () {
        // don't yank the page while the reviewer is mid-note; reload on close
        if (pop) { pendingReload = true; return; }
        location.reload();
      };
    } catch (e) {}
  }

  paint();
})();
