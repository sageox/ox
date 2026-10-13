// Host controls: the review bar, the composer, the comments rail and the sync
// line, plus the reviewer's drafts and their submission. They reach the plan
// only through the port to the document tools, which pick targets and paint.
// The CLI's review page mounts both in the plan's own window (local.ts) and
// submits to the `ox plan review` server; served by it the page also
//   - live-reloads via SSE as the agent addresses items + re-renders,
//   - lets the reviewer Accept (verify) or Reopen an addressed item inline,
//   - offers a top-level Approve to close the loop,
//   - surfaces orphaned notes whose anchored text changed (never lose feedback).
// Without a server (file://) it falls back to exporting the feedback as JSON.

import { clip } from './anchor.js';
import { CHROME, typing } from './document-tools.js';
import type { HostPort } from './ports.js';
import type { PaintItem, Placed, State, Status, Tint } from './protocol.js';
import { ackState, mergeState, newRoundId, pruneTombs, roundAcked, sameItems, stampOf, syncMessage, type Drafts, type Mark, type Outbox, type SyncMessage, type SyncResult } from './state.js';

/** One committed review item, as the page's review-state island carries it (buildReviewState in internal/plan). */
export interface CommittedItem {
  anchor: string;
  section?: string;
  label?: string;
  quote?: string;
  status: Status;
  state: State;
  note?: string;
  reviewer?: string;
}

export interface HostControlsOptions {
  slug: string;
  /** Base URL of the live review server; '' exports the feedback instead. */
  endpoint: string;
  token: string;
  committed: CommittedItem[];
}

/** What a note is about, and where its composer opens. */
interface About {
  a: string;
  section: string;
  label: string;
  quote?: string;
}
interface Row {
  at: { anchor: string; nth: number } | null; // null: nothing on the page to scroll to
  a: string;
  quote: string;
  status: Status;
  note?: string;
  section: string;
  label: string;
  state: string;
  reviewer: string;
  unsent: boolean;
}
type Kind = SyncMessage['kind'] | 'info' | 'pending' | 'ask';
type Action = { label: string; fn: (input?: string) => void };

const STATUS: Array<{ id: Status; glyph: string }> = [
  { id: 'approve', glyph: '✓' },
  { id: 'request-change', glyph: '✎' },
  { id: 'flag', glyph: '⚑' },
  { id: 'comment', glyph: '◌' },
];
const HL_TINT: Record<string, Tint> = { 'approve': 'sage', 'request-change': 'amber', 'flag': 'red', 'comment': 'gold' };
// A tombstone only has to outlive a stale tab's copy of the mark. A week
// covers a laptop left open over a weekend; a tab stale for longer than
// that can resurrect a sent mark as unsent, which the reviewer can delete.
const TOMB_TTL = 7 * 24 * 3600 * 1000;
const BUBBLE = '<svg width="14" height="14" viewBox="0 0 16 16" aria-hidden="true"><path d="M3 2.5h10A1.5 1.5 0 0 1 14.5 4v6a1.5 1.5 0 0 1-1.5 1.5H7.5L4 14.5v-3H3A1.5 1.5 0 0 1 1.5 10V4A1.5 1.5 0 0 1 3 2.5z" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round"/></svg>';

// esc makes untrusted text safe in element content and in double-quoted
// attributes: a status read back from the Ledger is not validated.
export function esc(s: string | undefined): string {
  return (s || '').replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/"/g, '&quot;');
}
function glyphFor(id: string): string {
  for (let i = 0; i < STATUS.length; i++) if (STATUS[i].id === id) return STATUS[i].glyph;
  return '◌';
}
// repOf picks the representative mark for an anchor's inline glyph. Prefer a
// still-OPEN blocking mark (request-change/flag) so one reviewer's unresolved
// feedback is never painted as resolved just because another reviewer's mark on
// the same anchor was addressed; then any open mark; then addressed/verified.
function repOf(arr: CommittedItem[]): CommittedItem {
  for (let i = 0; i < arr.length; i++) { if (arr[i].state === 'open' && (arr[i].status === 'request-change' || arr[i].status === 'flag')) return arr[i]; }
  for (let j = 0; j < arr.length; j++) { if (arr[j].state === 'open') return arr[j]; }
  for (let k = 0; k < arr.length; k++) { if (arr[k].state === 'addressed' || arr[k].state === 'verified') return arr[k]; }
  return arr[0];
}

/** mountHostControls draws the review controls into this window and drives the document tools on port. */
export function mountHostControls(port: HostPort, opts: HostControlsOptions): void {
  const body = document.body;
  const slug = opts.slug;
  const base = opts.endpoint; // live server base, else ""
  const token = opts.token;
  const live = base !== '';
  const KEY = 'ox-plan-fb:' + slug;
  const ON_KEY = 'ox-plan-rev-on:' + slug; // sessionStorage: survives a reload, not a new tab
  const MIN_KEY = 'ox-plan-rail-min:' + slug; // sessionStorage, like ON_KEY

  // Storage layout (all per slug, localStorage so every tab on the plan shares it):
  //   KEY        anchor -> unsent mark, each stamped with updated_at (ms)
  //   TOMB_KEY   anchor -> ms a mark was deleted or sent; a tab's stale copy
  //              older than that never brings it back
  //   OUTBOX_KEY the one submission in flight: {id, items, reviewer,
  //              created_at, stamps}. Written BEFORE the POST and resent with
  //              the SAME id until a matching ack, so a reload, a retry, or a
  //              second tab can never turn one Submit into two rounds.
  const TOMB_KEY = KEY + ':tombs';
  const OUTBOX_KEY = KEY + ':outbox';
  const EXPORT_KEY = KEY + ':export'; // static mode: the id of the last export, reused while its items are unchanged
  const SYNC_KEY = 'ox-plan-fb-sync:' + slug; // sessionStorage: last send result, re-shown after the reload it triggers

  const stored = readStored();
  let marks = stored.marks;
  let tombs = stored.tombs;
  // anchor -> [mark, …]: multi-user, so several reviewers on one anchor all
  // show. No prototype: a stored anchor named "constructor" is just a key.
  const committed: Record<string, CommittedItem[]> = Object.create(null);
  opts.committed.forEach((x) => { (committed[x.anchor] = committed[x.anchor] || []).push(x); });
  let reviewer = (localStorage.getItem('ox-plan-reviewer') || '').trim(); // multi-user: who you are
  let on = false;
  let pendingReload = false;
  let offline = false; // live server unreachable — nothing can be saved until it returns
  let probeTimer: ReturnType<typeof setInterval> | null = null;
  let posting = 0; // POSTs awaiting a response; an SSE reload waits for them
  let reloadAfterPost = false;
  let sending = false; // this tab's /feedback send is in flight
  let placed: Placed[] = []; // what the document painted, in reading order
  let unplaced: string[] = []; // anchors with a comment but nothing on the page

  let lastStamp = 0;
  function nextStamp() { const n = Date.now(); lastStamp = n > lastStamp ? n : lastStamp + 1; return lastStamp; }
  function readJSON(k: string) { try { return JSON.parse(localStorage.getItem(k) as string) || null; } catch (e) { return null; } }
  function readStored(): Drafts { return { marks: readJSON(KEY) || {}, tombs: readJSON(TOMB_KEY) || {} }; }
  // save merges with what other tabs wrote rather than overwriting it: each
  // tab saves its whole map, so a plain write would drop the other tab's marks.
  function save() {
    const m = mergeState({ marks, tombs }, readStored());
    marks = m.marks; tombs = pruneTombs(m.tombs, Date.now(), TOMB_TTL);
    try { localStorage.setItem(KEY, JSON.stringify(marks)); localStorage.setItem(TOMB_KEY, JSON.stringify(tombs)); } catch (e) { /* storage full: the marks stay in this tab */ }
  }
  function removeMark(a: string) { delete marks[a]; tombs[a] = nextStamp(); }
  // draft is your unsent mark on a, for anchors that may come from the Ledger
  function draft(a: string): Mark | undefined { return Object.prototype.hasOwnProperty.call(marks, a) ? marks[a] : undefined; }
  function readOutbox(): Outbox | null { const o = readJSON(OUTBOX_KEY); return o && o.id && o.items ? o : null; }
  function writeOutbox(o: Outbox | null) {
    try { if (o) localStorage.setItem(OUTBOX_KEY, JSON.stringify(o)); else localStorage.removeItem(OUTBOX_KEY); return true; }
    catch (e) { return false; }
  }
  // post never uses alert/confirm/prompt: browsers block them in the iframe
  // the web app embeds plans in, so every outcome is reported inline. fail(msg,
  // isOffline) runs on any failure; without it an HTTP error shows inline.
  function post(path: string, payload: unknown, ok?: (data: SyncResult) => void, fail?: (msg: string, isOffline: boolean) => void) {
    if (offline) { offlineNotice(); if (fail) fail('offline', true); return; }
    posting++;
    fetch(base + path, { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Review-Token': token }, body: JSON.stringify(payload) })
      .then((r) => {
        if (!r.ok) {
          return r.text().catch(() => '').then((t) => {
            throw new Error('HTTP ' + r.status + (t.trim() ? ' — ' + clip(t.trim(), 120) : ''));
          });
        }
        return r.json().catch(() => ({}));
      })
      .then((data) => { if (ok) ok(data || {}); })
      .catch((e) => {
        // a network-level failure means the server is gone — flip to
        // disconnected mode (marks stay in localStorage; nothing is lost)
        // rather than surfacing a raw fetch error.
        if (e instanceof TypeError) { setOffline(true); offlineNotice(); if (fail) fail('offline', true); return; }
        if (fail) fail(e.message, false);
        else showSync('err', 'Request failed: ' + e.message);
      })
      .then(() => {
        posting--;
        if (!posting && reloadAfterPost) { reloadAfterPost = false; reloadWhenIdle(); }
      });
  }

  function committedState(a: string) { const arr = committed[a] || []; return arr.length ? repOf(arr).state : ''; }

  // paintItems is what the document paints: per anchor, your unsent mark,
  // else the representative committed one; a highlight brings its words.
  // Only anchors the document computes can be placed; the rest list as
  // unplaced. Who marked what, and their notes, stay here: the rail shows them.
  function paintItems(): PaintItem[] {
    const out: PaintItem[] = [];
    Object.keys(committed).concat(Object.keys(marks)).forEach((a, i, all) => {
      if (all.indexOf(a) !== i || !/^[hq][0-9a-f]{8}$/.test(a)) return;
      const carr = committed[a] || [];
      const c = carr.length ? repOf(carr) : null, m = draft(a);
      let it: PaintItem;
      if (m) {
        it = { anchor: a, kind: 'unsent', status: m.status, glyph: glyphFor(m.status), tint: HL_TINT[m.status] || 'gold' };
      } else if (c) {
        it = {
          anchor: a, kind: 'committed', status: c.status, state: c.state,
          glyph: (c.state === 'addressed' || c.state === 'verified') ? '✓' : (c.state === 'wontfix' ? '—' : glyphFor(c.status)),
          tint: (c.state === 'addressed' || c.state === 'verified') ? 'sage' : c.state === 'wontfix' ? 'faint' : 'amber',
        };
      } else return;
      // a highlight is found by its words: your mark's, else the first committed one's
      const q = m && m.quote ? m : carr.filter((x) => x.quote)[0];
      if (q && q.quote) { it.quote = q.quote; it.section = q.section || ''; if (q.label) it.label = q.label; }
      out.push(it);
    });
    return out;
  }

  function paint() {
    const n = Object.keys(marks).length;
    countEl.textContent = n ? (n + ' unsent') : '';
    port.send({ type: 'paint', items: paintItems() }); // answered by 'placed'
  }
  function onPlaced(items: Placed[]) {
    placed = items;
    const seen: Record<string, boolean> = {};
    placed.forEach((p) => { seen[p.anchor] = true; });
    // comments whose text is gone from the page — rewritten by the agent, or
    // edited while this tab was away — keep a rail row, so an addressed one can
    // still be accepted or reopened
    unplaced = Object.keys(committed).concat(Object.keys(marks)).filter((a, i, all) => !seen[a] && all.indexOf(a) === i);
    // orphaned committed notes: their anchored text changed, so nothing matched.
    const orphans: CommittedItem[] = [];
    Object.keys(committed).forEach((a) => { if (!seen[a]) committed[a].forEach((x) => orphans.push(x)); });
    renderOrphans(orphans);
    renderRail();
  }

  let orphanBar: HTMLElement | null = null;
  function renderOrphans(orphans: CommittedItem[]) {
    if (orphanBar) { orphanBar.remove(); orphanBar = null; }
    const open = orphans.filter((o) => o.state === 'open');
    if (!open.length) return;
    const bar = orphanBar = document.createElement('div');
    bar.className = 'rev-orphans';
    const items = open.map((o) => '<li><strong>' + esc(o.status) + '</strong> ' + esc(o.label || o.anchor) + (o.note ? ' — ' + esc(o.note) : '') + '</li>').join('');
    bar.innerHTML = '<div class="rev-orphans-h">⚠ ' + open.length + ' review note(s) no longer anchored (the text changed) — still open:</div><ul>' + items + '</ul><button class="rev-orphans-x">dismiss</button>';
    document.body.appendChild(bar);
    (bar.querySelector('.rev-orphans-x') as HTMLElement).onclick = () => { bar.remove(); if (orphanBar === bar) orphanBar = null; };
  }

  // --- comments rail: surface the note TEXT in the right gutter (not just a
  // margin glyph), each row scroll-jumping to its anchored element on click ---
  let rail: HTMLElement | null = null;
  // Hide shrinks the rail to a Show button (comment icon + count) so the plan
  // under it can be read; kept per tab, so a live reload doesn't reopen it.
  let railMin = false;
  try { railMin = !!sessionStorage.getItem(MIN_KEY); } catch (e) { /* no sessionStorage: start shown */ }
  function toggleRail() {
    railMin = !railMin;
    try { if (railMin) sessionStorage.setItem(MIN_KEY, '1'); else sessionStorage.removeItem(MIN_KEY); } catch (e) { /* kept for this page only */ }
    renderRail();
    (rail!.querySelector('.rev-rail-show, .rev-rail-hide') as HTMLElement).focus(); // the clicked button was just replaced
  }
  function railRows(): Row[] {
    // reading order, highlights interleaved with element rows, as the document
    // placed them, and rows with nothing on the page last. Multi-user: one row
    // per reviewer's committed mark on an anchor, plus your unsent local mark.
    const rows: Row[] = [];
    function add(at: Row['at'], a: string, section: string, label: string, quote: string) {
      (committed[a] || []).forEach((c) => {
        rows.push({
          at, a, quote, status: c.status, note: c.note,
          section: c.section || section, label: c.label || label,
          state: c.state, reviewer: c.reviewer || '', unsent: false,
        });
      });
      const m = draft(a);
      if (m) {
        rows.push({
          at, a, quote, status: m.status, note: m.note,
          section: m.section || section, label: m.label || label,
          state: '', reviewer: reviewer || '(you)', unsent: true,
        });
      }
    }
    const nth: Record<string, number> = {};
    placed.forEach((p) => {
      const k = nth[p.anchor] = (nth[p.anchor] === undefined ? 0 : nth[p.anchor] + 1);
      add({ anchor: p.anchor, nth: k }, p.anchor, p.section, p.label, p.quote || '');
    });
    unplaced.forEach((a) => {
      const m = draft(a);
      const its: Array<CommittedItem | Mark> = (committed[a] || []).concat(m ? [m as unknown as CommittedItem] : []);
      const q = its.filter((x) => x.quote)[0];
      const any = q || its.filter((x) => x.label)[0] || its[0];
      add(null, a, any.section || '', any.label || '', q ? q.quote || '' : '');
    });
    return rows;
  }
  function renderRail() {
    if (!rail) {
      rail = document.createElement('aside'); rail.className = 'rev-rail'; document.body.appendChild(rail);
      rail.addEventListener('click', (ev) => { if ((ev.target as Element).closest('.rev-rail-show, .rev-rail-hide')) toggleRail(); });
    }
    const r = rail;
    const rows = railRows();
    // hidden when there's nothing to read and we're not actively reviewing.
    if (!on && !rows.length) { r.style.display = 'none'; r.innerHTML = ''; return; }
    r.style.display = '';
    r.classList.toggle('rev-rail-min', railMin);
    if (railMin) {
      r.innerHTML = '<button class="rev-rail-show" title="Show comments" aria-label="Show comments (' + rows.length + ')">' + BUBBLE + rows.length + '</button>';
      return;
    }
    const hide = '<button class="rev-rail-hide" title="Hide comments">Hide</button>';
    if (!rows.length) {
      r.innerHTML = '<div class="rev-rail-h">Comments' + hide + '</div><div class="rev-rail-empty">Click any section, risk, or row — or highlight text — to leave a comment.</div>';
      return;
    }
    let html = '<div class="rev-rail-h">Comments <span class="rev-rail-n">' + rows.length + '</span>' + hide + '</div><ul class="rev-rail-list">';
    rows.forEach((row, i) => {
      let tag = '';
      if (row.unsent) tag = '<span class="rev-rail-tag unsent">unsent</span>';
      else if (row.state && row.state !== 'open') tag = '<span class="rev-rail-tag ' + esc(row.state) + '">' + esc(row.state) + '</span>';
      if (!row.at) tag += '<span class="rev-rail-tag">text changed</span>';
      // a highlight's row leads with its words; its label is those words, so a
      // note-less highlight shows nothing more
      const content = (row.quote ? '<div class="rev-rail-quote">' + esc(row.label) + '</div>' : '') +
        (row.note ? '<div class="rev-rail-note">' + esc(row.note) + '</div>'
          : row.quote ? '' : '<div class="rev-rail-note muted">' + esc(row.label) + '</div>');
      const whoTag = row.reviewer ? '<span class="rev-rail-who">' + esc(row.reviewer) + '</span>' : '';
      html += '<li class="rev-rail-item" data-i="' + i + '" data-status="' + esc(row.status) + '">' +
        '<span class="rev-rail-glyph">' + glyphFor(row.status) + '</span>' +
        '<div class="rev-rail-body"><div class="rev-rail-sec"><span class="rev-rail-sec-t">' + esc(row.section || '(plan)') + '</span>' + whoTag + tag + '</div>' + content + '</div></li>';
    });
    html += '</ul>';
    r.innerHTML = html;
    r.querySelectorAll('.rev-rail-item').forEach((li) => {
      (li as HTMLElement).onclick = (ev) => {
        const row = rows[+(li.getAttribute('data-i') as string)];
        if (!row) return;
        // nothing on the page to scroll to: open the comment from its row
        if (!row.at) { openPop({ a: row.a, section: row.section, label: row.label, quote: row.quote }, { left: ev.clientX, bottom: ev.clientY }); ev.stopPropagation(); return; }
        port.send({ type: 'reveal', anchor: row.at.anchor, nth: row.at.nth });
      };
    });
  }

  let pop: HTMLElement | null = null;
  function sendMode() { port.send({ type: 'mode', on, open: !!pop }); }
  function closePop() {
    if (pop) { pop.remove(); pop = null; sendMode(); }
    port.send({ type: 'focus', anchor: null });
    if (pendingReload) { pendingReload = false; location.reload(); }
  }
  // t is what a note is about: an element or a highlight — anchor, section,
  // label, and a highlight's quote. rect is where it opens below.
  function openPop(t: About, rect: { left: number; bottom: number }) {
    closePop();
    const a = t.a;
    const cstate = committedState(a);
    const p = pop = document.createElement('div'); p.className = 'rev-pop';
    // a highlight's note names its words: the page selection is gone once the
    // reviewer clicks into the note, so the focus highlight keeps them visible too
    const quote = t.quote ? '<div class="rev-quote">' + esc(t.label) + '</div>' : '';
    port.send({ type: 'focus', anchor: a });
    // a committed, addressed/verified item gets Accept/Reopen (close the loop);
    // everything else gets the mark-up controls.
    if (live && (cstate === 'addressed' || cstate === 'verified')) {
      p.innerHTML = quote + '<div class="rev-cap">Agent marked this ' + cstate + '.</div>' +
        '<textarea class="rev-note" placeholder="reopen note (optional)"></textarea>' +
        '<div class="rev-row"><button class="rev-accept">Accept</button><button class="rev-reopen">Reopen</button></div>';
      placePop(rect);
      (p.querySelector('.rev-accept') as HTMLElement).onclick = () => {
        post('/accept', { anchor: a }, (data) => { closePop(); reportSync(syncMessage('Accepted', data)); });
      };
      (p.querySelector('.rev-reopen') as HTMLElement).onclick = () => {
        post('/reopen', { anchor: a, note: (p.querySelector('.rev-note') as HTMLTextAreaElement).value.trim() }, (data) => {
          closePop(); reportSync(syncMessage('Reopened', data));
        });
      };
      return;
    }
    const existing = draft(a) || { status: 'request-change', note: '' };
    const btns = STATUS.map((s) => '<button data-s="' + s.id + '" class="rev-s' + (existing.status === s.id ? ' on' : '') + '">' + s.glyph + ' ' + s.id + '</button>').join('');
    p.innerHTML = quote + '<div class="rev-row">' + btns + '</div>' +
      '<textarea class="rev-note" placeholder="note (optional)">' + esc(existing.note || '') + '</textarea>' +
      '<div class="rev-row"><button class="rev-save">Save</button><button class="rev-del">Delete</button></div>';
    placePop(rect);
    let status = existing.status as Status;
    p.querySelectorAll('.rev-s').forEach((b) => {
      (b as HTMLElement).onclick = () => { status = b.getAttribute('data-s') as Status; p.querySelectorAll('.rev-s').forEach((x) => x.classList.remove('on')); b.classList.add('on'); };
    });
    (p.querySelector('.rev-save') as HTMLElement).onclick = () => {
      const m: Mark = { anchor: a, section: t.section, label: t.label, status, note: (p.querySelector('.rev-note') as HTMLTextAreaElement).value.trim(), updated_at: nextStamp() };
      if (t.quote) m.quote = t.quote;
      marks[a] = m;
      save(); paint(); closePop();
    };
    (p.querySelector('.rev-del') as HTMLElement).onclick = () => { removeMark(a); save(); paint(); closePop(); };
  }
  // The note opens just below where the reviewer acted, never over it.
  function placePop(rect: { left: number; bottom: number }) {
    const p = pop as HTMLElement;
    document.body.appendChild(p);
    p.style.top = (window.scrollY + rect.bottom + 8) + 'px';
    p.style.left = (window.scrollX + Math.min(rect.left, window.innerWidth - 300)) + 'px';
    sendMode();
  }

  // A click on the controls outside the note dismisses it — in review mode
  // always, which also clears a highlight's focus. Clicks on the plan reach
  // the document tools, which answer target, refused or dismiss.
  document.addEventListener('click', (ev) => {
    const t = ev.target as Element;
    if (pop && pop.contains(t)) return;
    if (t.closest && t.closest(CHROME) && (on || pop)) closePop();
  }, true);

  // Submit snapshots the unsent marks into the outbox BEFORE sending, then
  // sends that outbox; a Submit (or page load) that finds one already there
  // resends it as-is, so its id — the server's dedupe key — never changes.
  function submit() {
    if (sending) return;
    let box = live ? readOutbox() : null;
    if (!box) {
      const items = Object.keys(marks).map((k) => wireItem(marks[k]));
      if (!items.length) { showSync('info', 'No marks yet. Toggle Review, click a section, leave a note.'); return; }
      if (!reviewer) { askName('Your name, so teammates know whose feedback this is:', submit); return; }
      const stamps: Record<string, number> = {};
      Object.keys(marks).forEach((k) => { stamps[k] = stampOf(marks[k]); });
      box = { id: newRoundId(), items, reviewer, created_at: new Date().toISOString(), stamps };
      if (!live) { exportJSON(box); return; }
      writeOutbox(box); // a full storage still sends; only a reload mid-flight would lose the id
    }
    send(box);
  }
  function wireItem(m: Mark): Outbox['items'][number] {
    const o: Record<string, unknown> = {};
    Object.keys(m).forEach((k) => { if (k !== 'updated_at') o[k] = (m as unknown as Record<string, unknown>)[k]; });
    return o as Outbox['items'][number];
  }
  function send(box: Outbox) {
    sending = true; paintSubmit();
    showSync('pending', 'Sending…');
    post('/feedback', { id: box.id, slug, reviewer: box.reviewer, items: box.items }, (data) => {
      sending = false;
      if (!roundAcked(box, data)) {
        paintSubmit();
        showSync('err', 'Not confirmed — the server acknowledged a different round. Your marks are kept.', [{ label: 'Retry', fn: submit }]);
        return;
      }
      // marks first, outbox second: a crash between them leaves an outbox
      // whose resend the server reports as a duplicate, never lost marks
      const acked = ackState(mergeState({ marks, tombs }, readStored()), box);
      marks = acked.marks; tombs = acked.tombs; save();
      const cur = readOutbox();
      if (cur && cur.id === box.id) writeOutbox(null);
      paint(); paintSubmit();
      reportSync(syncMessage('Sent', data));
    }, (msg, isOffline) => {
      sending = false; paintSubmit();
      if (isOffline) showSync('warn', 'Review server offline — your feedback is queued in this browser and sends when the server is back.');
      else showSync('err', 'Not sent (' + msg + '). Your marks are kept.', [{ label: 'Retry', fn: submit }]);
    });
  }
  function approve() {
    if (!live) { showSync('info', 'Approve is available in the live `ox plan review` loop.'); return; }
    showSync('ask', 'Approve this plan? This stamps it approved and closes the review loop.', [
      { label: 'Approve', fn: () => {
        showSync('pending', 'Approving…');
        post('/approve', {}, (data) => {
          const m = syncMessage('Approved', data);
          if (m.kind !== 'err') m.text += ' — you can close this tab.';
          reportSync(m);
        });
      } },
      { label: 'Cancel', fn: hideSync },
    ]);
  }

  function exportJSON(box: Outbox) {
    // a re-export of unchanged marks keeps its id, so `ox plan feedback apply`
    // run twice on the same feedback lands one round
    const prev = readJSON(EXPORT_KEY);
    const id = prev && prev.id && sameItems(prev.items, box.items) ? prev.id : box.id;
    try { localStorage.setItem(EXPORT_KEY, JSON.stringify({ id, items: box.items })); } catch (e) { /* the next export mints a new id */ }
    const json = JSON.stringify({ id, slug, reviewer: box.reviewer, items: box.items }, null, 2);
    const blob = new Blob([json], { type: 'application/json' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a'); a.href = url; a.download = slug + '-feedback.json'; a.click();
    URL.revokeObjectURL(url);
    if (navigator.clipboard) navigator.clipboard.writeText(json).catch(() => {});
    showSync('ok', 'Saved ' + slug + '-feedback.json (and copied to clipboard). Hand it to the agent, or run: ox plan feedback apply ' + slug + ' --from ' + slug + '-feedback.json');
  }

  // --- sync status: one inline, non-modal line that says what actually
  // happened to the reviewer's feedback. Never alert/confirm/prompt — those
  // are blocked inside the iframe the web app embeds plans in. ---
  let syncEl: HTMLElement | null = null, syncTimer: ReturnType<typeof setTimeout> | null = null;
  function hideSync() { if (syncTimer) { clearTimeout(syncTimer); syncTimer = null; } if (syncEl) { syncEl.remove(); syncEl = null; } }
  // kind: ok | warn | err | info | pending | ask. actions: [{label, fn(inputValue)}];
  // input: initial text for a one-line field (asking for a name).
  function showSync(kind: Kind, text: string, actions?: Action[], input?: string) {
    hideSync();
    const el = syncEl = document.createElement('div');
    el.className = 'rev-sync ' + kind;
    el.setAttribute('role', kind === 'err' ? 'alert' : 'status');
    const msg = document.createElement('span'); msg.className = 'rev-sync-msg'; msg.textContent = text; el.appendChild(msg);
    let field: HTMLInputElement | null = null;
    if (input !== undefined) {
      field = document.createElement('input'); field.className = 'rev-sync-input'; field.value = input; el.appendChild(field);
    }
    (actions || []).forEach((a) => {
      const b = document.createElement('button'); b.className = 'rev-sync-act'; b.textContent = a.label;
      b.onclick = () => { a.fn(field ? field.value.trim() : undefined); };
      el.appendChild(b);
    });
    if (kind !== 'pending' && kind !== 'ask') {
      const x = document.createElement('button'); x.className = 'rev-sync-x'; x.textContent = '×'; x.title = 'Dismiss'; x.onclick = hideSync;
      el.appendChild(x);
    }
    document.body.appendChild(el);
    if (field) {
      const f = field;
      f.focus();
      f.onkeydown = (e) => { if (e.key === 'Enter' && actions && actions[0]) { e.preventDefault(); actions[0].fn(f.value.trim()); } };
    }
    // good news fades; a warning lingers; an error or a question waits for the reviewer
    const ttl = ({ ok: 10000, info: 8000, warn: 20000 } as Record<string, number>)[kind];
    if (ttl) syncTimer = setTimeout(() => { if (syncEl === el) hideSync(); }, ttl);
  }
  // reportSync shows a send result and keeps it for one reload: the server's
  // SSE reload usually lands right after the response, and would wipe it.
  function reportSync(m: SyncMessage) {
    showSync(m.kind, m.text);
    try { sessionStorage.setItem(SYNC_KEY, JSON.stringify({ kind: m.kind, text: m.text, at: Date.now() })); } catch (e) { /* shown now, not after the reload */ }
  }
  function restoreSync() {
    try {
      const s = JSON.parse(sessionStorage.getItem(SYNC_KEY) || 'null');
      sessionStorage.removeItem(SYNC_KEY);
      // 30s: long enough to span a slow reload, short enough that an old
      // result never reads as the outcome of something newer
      if (s && Date.now() - s.at < 30000) showSync(s.kind, s.text);
    } catch (e) { /* nothing to restore */ }
  }
  function askName(q: string, then?: () => void) {
    showSync('ask', q, [
      { label: 'Save', fn: (n) => {
        if (!n) return;
        reviewer = n;
        try { localStorage.setItem('ox-plan-reviewer', reviewer); } catch (e) { /* named for this page only */ }
        hideSync(); updateWho(); paint();
        if (then) then();
      } },
      { label: 'Cancel', fn: hideSync },
    ], reviewer);
  }

  // --- connection state (live mode): the page must never LOOK live when the
  // server is gone. Offline = red pill + sticky banner + refused sends; marks
  // keep saving to localStorage and are restored after a restart, so nothing a
  // reviewer wrote is ever lost. ---
  let connEl: HTMLElement | null = null, offlineBar: HTMLElement | null = null, toastEl: HTMLElement | null = null;
  function restartCmd() { return 'ox plan review ' + slug; }
  function paintConn() {
    if (!connEl) return;
    connEl.textContent = offline ? '● offline' : '● live';
    connEl.className = 'rev-conn ' + (offline ? 'off' : 'ok');
    connEl.title = offline ? 'Review server unreachable — feedback is NOT being saved' : 'Connected to the review server';
  }
  function setOffline(down: boolean) {
    if (offline === down) return;
    offline = down;
    body.classList.toggle('rev-offline', down);
    paintConn();
    if (down) { showOfflineBar(); startProbe(); }
    else { hideOfflineBar(); stopProbe(); }
  }
  function showOfflineBar() {
    hideOfflineBar();
    const n = Object.keys(marks).length;
    const bar = offlineBar = document.createElement('div');
    bar.className = 'rev-offline-bar';
    bar.innerHTML = '<strong>⚠ Review server offline</strong> — new feedback is NOT being saved. ' +
      (n ? n + ' unsent mark(s) are' : 'Your marks are') + ' kept in this browser and restored on reconnect. Restart: ' +
      '<code>' + esc(restartCmd()) + '</code> <button class="rev-offline-copy">copy</button>';
    document.body.appendChild(bar);
    const copy = bar.querySelector('.rev-offline-copy') as HTMLElement;
    copy.onclick = () => {
      if (navigator.clipboard) navigator.clipboard.writeText(restartCmd()).then(() => { copy.textContent = 'copied ✓'; }).catch(() => {});
    };
  }
  function hideOfflineBar() { if (offlineBar) { offlineBar.remove(); offlineBar = null; } }
  function offlineNotice() {
    const n = Object.keys(marks).length;
    showSync('err', 'The review server is offline — feedback can NOT be saved right now. ' +
      (n ? 'Your ' + n + ' unsent mark(s) stay in this browser and will be restored. ' : '') +
      'Restart the loop with: ' + restartCmd());
  }
  function toast(msg: string) {
    if (toastEl) toastEl.remove();
    const el = toastEl = document.createElement('div');
    el.className = 'rev-toast';
    el.textContent = msg;
    document.body.appendChild(el);
    // this toast only: a newer one may have replaced it before the timer fires
    setTimeout(() => { el.remove(); if (toastEl === el) toastEl = null; }, 8000);
  }
  // While offline, poll /healthz; the instant the server is back, reload —
  // stable port + persisted token mean the same origin serves fresh state, and
  // paint() restores unsent marks from localStorage. Covers the case where the
  // EventSource died permanently (e.g. a 403 from a rotated token).
  function startProbe() {
    if (probeTimer) return;
    probeTimer = setInterval(() => {
      fetch(base + '/healthz', { cache: 'no-store' })
        .then((r) => (r.ok ? r.json() : null))
        .then((j) => { if (j && j.app === 'ox-plan-review') reloadWhenIdle(); })
        .catch(() => {});
    }, 3000);
  }
  function stopProbe() { if (probeTimer) { clearInterval(probeTimer); probeTimer = null; } }
  function reloadWhenIdle() { if (pop) { pendingReload = true; return; } location.reload(); }

  // controls
  const bar = document.createElement('div');
  bar.className = 'rev-bar';
  bar.innerHTML = (live ? '<span class="rev-conn"></span>' : '') +
    '<button class="rev-toggle" title="Toggle review mode">Review</button><span class="rev-count"></span>' +
    (live ? '<button class="rev-who" title="Set the name teammates see on your comments"></button>' : '') +
    '<button class="rev-submit" title="Send feedback to the agent">' + (live ? 'Submit' : 'Export') + '</button>' +
    (live ? '<button class="rev-approve" title="Approve and close the loop">Approve</button>' : '');
  document.body.appendChild(bar);
  const countEl = bar.querySelector('.rev-count') as HTMLElement;
  connEl = bar.querySelector('.rev-conn');
  paintConn();
  const whoEl = bar.querySelector('.rev-who') as HTMLElement | null;
  function updateWho() { if (whoEl) whoEl.textContent = reviewer ? ('You: ' + reviewer) : 'Set name'; }
  updateWho();
  if (whoEl) whoEl.onclick = () => { askName('Your name (shown to teammates on this plan):'); };
  const submitEl = bar.querySelector('.rev-submit') as HTMLButtonElement;
  function paintSubmit() {
    if (!live) return;
    submitEl.disabled = sending; // a second click while one send is in flight would race it
    submitEl.textContent = sending ? 'Sending…' : 'Submit';
  }
  // Review is a mode: while on, clicks mark up instead of navigating. A mode has
  // to announce itself on entry and keep its exit in view — otherwise the only
  // signal is a green button and nothing visibly changes until a hover.
  const toggleEl = bar.querySelector('.rev-toggle') as HTMLElement;
  let hintBubble: HTMLElement | null = null;
  function setReview(next: boolean, silent?: boolean) {
    on = next; toggleEl.classList.toggle('on', on);
    sendMode(); // the document marks its body rev-on
    toggleEl.textContent = on ? 'Exit review' : 'Review';
    toggleEl.title = on ? 'Leave review mode (Esc)' : 'Enter review mode (r)';
    if (on && !silent) toast('Review mode — click any section, item, or row to mark it up, or highlight text to comment on it. Esc or Exit review to leave.');
    if (!on) { closePop(); if (toastEl) { toastEl.remove(); toastEl = null; } }
    try { localStorage.setItem('ox-plan-rev-seen', '1'); } catch (e) { /* the hint shows again next visit */ }
    try { if (on) sessionStorage.setItem(ON_KEY, '1'); else sessionStorage.removeItem(ON_KEY); } catch (e) { /* not restored after a reload */ }
    if (hintBubble) { hintBubble.remove(); hintBubble = null; }
    renderRail();
  }
  toggleEl.onclick = () => { setReview(!on); };
  submitEl.onclick = submit;
  if (live) (bar.querySelector('.rev-approve') as HTMLElement).onclick = approve;
  // Keyboard, here rather than scaffold.js so authored HTML plans (chrome.js
  // adds no key map) get the same keys. Esc peels one layer at a time — an
  // open note first, then the mode — and fires while typing in the note too;
  // that is the point of Esc. `r` toggles the mode from anywhere but a text field.
  // Keys are shared with the page: one an earlier listener marked handled (an
  // authored inspector closing on Esc) is left alone, and one acted on here is
  // marked handled so later listeners that honor defaultPrevented skip it.
  document.addEventListener('keydown', (e) => {
    if (e.defaultPrevented) return;
    if (e.key === 'Escape') {
      if (pop) { closePop(); e.preventDefault(); }
      else if (on) { setReview(false); e.preventDefault(); }
      return;
    }
    if (e.key === 'r' && !typing(e) && !e.metaKey && !e.ctrlKey && !e.altKey) { setReview(!on); e.preventDefault(); }
  });

  // first-visit discoverability: a one-time pointer at the Review toggle.
  try {
    if (!localStorage.getItem('ox-plan-rev-seen')) {
      const hint = hintBubble = document.createElement('div');
      hint.className = 'rev-hint';
      hint.textContent = '← Click Review to mark up this plan';
      document.body.appendChild(hint);
      setTimeout(() => { if (hintBubble === hint) { hint.remove(); hintBubble = null; } }, 9000);
    }
  } catch (e) { /* no storage: no hint */ }

  // live reload as the agent addresses items + re-renders
  if (live && window.EventSource) {
    try {
      const es = new EventSource(base + '/events?t=' + encodeURIComponent(token));
      es.onopen = () => {
        // recovered from a dead server: reload for fresh state (the browser
        // repaints unsent marks from localStorage after the reload).
        if (offline) { setOffline(false); reloadWhenIdle(); return; }
        paintConn();
      };
      es.onerror = () => { setOffline(true); };
      es.onmessage = () => {
        // a reload mid-POST would drop its result (the server writes the file,
        // and the watcher fires, before a slow commit+push answers): wait for it
        if (posting) { reloadAfterPost = true; return; }
        // don't yank the page while the reviewer is mid-note; reload on close
        if (pop) { pendingReload = true; return; }
        location.reload();
      };
    } catch (e) { /* no live reload; Submit still works */ }
  }

  // offline shell: cache the page so a reload while the server is down still
  // shows the plan (this layer then flips to disconnected mode instead of the
  // browser's connection-error page).
  if (live && 'serviceWorker' in navigator) {
    try { navigator.serviceWorker.register('/sw.js').catch(() => {}); } catch (e) { /* no offline shell */ }
  }

  // Another tab on this plan saved, deleted, or sent marks: fold its writes in
  // (save() already merged ours into storage) and repaint. e.key is null when
  // storage was cleared wholesale.
  window.addEventListener('storage', (e) => {
    if (e.key === null || e.key === KEY || e.key === TOMB_KEY) {
      const m = mergeState({ marks, tombs }, readStored());
      marks = m.marks; tombs = m.tombs;
      paint();
    }
  });

  // unsent marks that survived a server restart or reload — tell the reviewer.
  const pendingBox = live ? readOutbox() : null;
  if (live && Object.keys(marks).length && !pendingBox) {
    toast(Object.keys(marks).length + ' unsent mark(s) restored — Submit to send them.');
  }

  let started = false;
  port.listen((m) => {
    switch (m.type) {
      case 'ready':
        // the document tools are listening: paint, and restore what a reload interrupted
        paint();
        if (started) { sendMode(); return; }
        started = true;
        restoreSync();
        // A submission this page never saw acknowledged — a reload, a crash, or the
        // server dropping mid-send — is resent with its original id; the server
        // answers a round it already has as a duplicate, so this can't double-post.
        if (pendingBox) send(pendingBox);
        // A live reload — the agent addressed an item, or the server came back — must
        // not drop a reviewer mid-review back to reading mode. Restored silently: the
        // reviewer did not just enter, so no entry toast.
        try { if (sessionStorage.getItem(ON_KEY)) setReview(true, true); } catch (e) { /* starts in reading mode */ }
        break;
      case 'placed':
        onPlaced(m.items);
        break;
      case 'target':
        openPop({ a: m.target.anchor, section: m.target.section, label: m.target.label, quote: m.target.quote }, m.target.rect);
        break;
      case 'refused':
        closePop();
        toast(m.reason === 'repeated'
          ? '“' + clip(m.quote, 40) + '” appears ' + m.count + ' times in this section — highlight a longer phrase so the comment points at one place.'
          : 'Highlight text within one section to comment on it.');
        break;
      case 'dismiss':
        if (on || pop) closePop();
        break;
    }
  });
}
