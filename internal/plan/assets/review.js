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
  var on = false;
  var pendingReload = false;

  function load() { try { return JSON.parse(localStorage.getItem(KEY)) || {}; } catch (e) { return {}; } }
  function save() { try { localStorage.setItem(KEY, JSON.stringify(marks)); } catch (e) {} }
  function parseCommitted() {
    var el = document.getElementById('ox-review-state');
    if (!el) return {};
    try { var a = JSON.parse(el.textContent || '[]'); var m = {}; a.forEach(function (x) { m[x.anchor] = x; }); return m; }
    catch (e) { return {}; }
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
  function committedState(el) { var c = committed[anchorFor(el)]; return c ? c.state : ''; }

  function paint() {
    document.querySelectorAll('.rev-marked,.rev-committed').forEach(function (el) {
      el.classList.remove('rev-marked', 'rev-committed'); el.removeAttribute('data-rev'); el.removeAttribute('data-revstate');
      var g = el.querySelector(':scope > .rev-glyph'); if (g) g.remove();
    });
    var seen = {};
    document.querySelectorAll(SELECTOR).forEach(function (el) {
      var a = anchorFor(el);
      var c = committed[a], m = marks[a];
      if (!c && !m) return;
      if (c) seen[a] = true;
      var status = m ? m.status : c.status;
      var glyph = document.createElement('span');
      glyph.className = 'rev-glyph';
      if (c && !m) {
        el.classList.add('rev-committed'); el.setAttribute('data-revstate', c.state);
        glyph.textContent = (c.state === 'addressed' || c.state === 'verified') ? '✓' : (c.state === 'wontfix' ? '—' : glyphFor(status));
        glyph.title = c.state + (c.note ? (' — ' + c.note) : '');
      } else {
        el.classList.add('rev-marked'); el.setAttribute('data-rev', status);
        glyph.textContent = glyphFor(status);
      }
      el.appendChild(glyph);
    });
    // orphaned committed notes: their anchored text changed, so nothing matched.
    var orphans = Object.keys(committed).filter(function (a) { return !seen[a]; }).map(function (a) { return committed[a]; });
    renderOrphans(orphans);
    var n = Object.keys(marks).length;
    countEl.textContent = n ? (n + ' unsent') : '';
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

  function submit() {
    var items = Object.keys(marks).map(function (k) { return marks[k]; });
    if (!items.length) { alert('No marks yet. Toggle Review, click a section, leave a note.'); return; }
    var p = { slug: slug, items: items };
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
    '<button class="rev-submit" title="Send feedback to the agent">' + (live ? 'Submit' : 'Export') + '</button>' +
    (live ? '<button class="rev-approve" title="Approve and close the loop">Approve</button>' : '');
  document.body.appendChild(bar);
  var countEl = bar.querySelector('.rev-count');
  bar.querySelector('.rev-toggle').onclick = function () {
    on = !on; body.classList.toggle('rev-on', on); this.classList.toggle('on', on);
    if (!on) closePop();
    try { localStorage.setItem('ox-plan-rev-seen', '1'); } catch (e) {}
    if (hintBubble) { hintBubble.remove(); hintBubble = null; }
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
