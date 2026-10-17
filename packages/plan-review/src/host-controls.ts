// Host controls: the review bar, the composer, the comments rail and the sync
// line, plus the reviewer's drafts and the outbox that delivers what they
// submit. They are trusted host code and run in the host's own window: the
// plan's own window for ox's local review page (local.ts), the page around
// the plan's frame for a host that frames it. They reach the plan only
// through the port to the document tools, which pick targets and paint, and
// reach the host's store only through its transport. With a transport, a
// reviewer the host lets resolve can also Accept (verify) or Reopen an item
// the author addressed, and the rail keeps orphaned notes whose anchored text
// changed (never lose feedback). Without one (a file:// page) Submit exports
// the feedback as JSON.

import { clip } from './anchor.js';
import { CHROME, typing } from './document-tools.js';
import type { HostPort } from './ports.js';
import type { PaintItem, Placed, State, Status, Tint } from './protocol.js';
import { acceptBody, deliver, newId, openStore, queued, reopenBody, submitBody, syncMessage, wireItem, type Act, type Approval, type Entry, type Mark, type Receipt, type SyncMessage, type Transport } from './state.js';

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
  /** The reviewer's account. Drafts and the outbox are kept per account: another account on this browser never sees or sends them. */
  account: string;
  /** The plan under review as the host resolves it (ox: its directory), since two plans can share a slug. Drafts and the outbox are kept per plan. */
  plan: string;
  /** The plan's slug, shown to the reviewer and naming an exported file. */
  slug: string;
  committed: CommittedItem[];
  /** Problems the host had reading this review: each record or read it could not complete, and each operation stored with differing content. Non-zero means `committed` may be partial or show one copy. */
  problems?: number;
  /** Stores operations with the host. Without one, Submit exports the feedback as a file for `ox plan feedback apply`. */
  transport?: Transport;
  /** The signed-in reviewer's name. Without it the controls ask for a name, keep it in this browser and send it with each request, as ox's local review, which has no sign-in, does. */
  reviewer?: string;
  /** This reviewer may resolve: an addressed or verified item offers Accept and Reopen, which verify it or raise it again. Without it, that item opens the mark-up composer. Needs a transport. */
  resolve?: boolean;
  /** Stamp the plan approved: offered as the bar's Approve. */
  approve?: () => Promise<Approval>;
  /** The frame holding the plan, when the document tools run in one: target rects are in its viewport. */
  frame?: HTMLIFrameElement;
  /** How to bring the host back, shown with a copy button while it is offline. */
  restartCommand?: string;
}

/** A mounted set of controls. */
export interface HostControls {
  /** Show the review as the host read it again. */
  update(committed: CommittedItem[], problems?: number): void;
  /** Say the host is unreachable, or back; back, the outbox is sent again. */
  setOffline(down: boolean): void;
  /** Run fn once nothing is in flight and no note is open, so a reload then loses nothing. Replaces a waiting fn. */
  whenIdle(fn: () => void): void;
  /** Remove the controls and stop sending, before mounting them for another account or plan. */
  destroy(): void;
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
  tag: '' | 'unsent' | 'queued' | 'unsaved';
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
// a comment on the plan as a whole: WholePlanAnchor in pkg/planreview, with
// the label readers that predate it print (plan-review-ledger-contract.md 3.10)
const WHOLE: About = { a: 'plan', section: '', label: 'Whole plan' };
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
export function mountHostControls(port: HostPort, opts: HostControlsOptions): HostControls {
  const body = document.body;
  const slug = opts.slug;
  const transport = opts.transport;
  const live = !!transport;
  const store = openStore(window.localStorage, opts.account, opts.plan);
  const tab = encodeURIComponent(opts.plan);
  const ON_KEY = 'ox-plan-rev-on:' + tab; // sessionStorage: survives a reload, not a new tab
  const MIN_KEY = 'ox-plan-rail-min:' + tab; // sessionStorage, like ON_KEY
  const SYNC_KEY = 'ox-plan-fb-sync:' + tab; // sessionStorage: last send result, re-shown after the reload it triggers
  const off = new AbortController(); // every listener, removed by destroy()
  const signal = off.signal;

  let problems = opts.problems || 0;
  // anchor -> [mark, …]: multi-user, so several reviewers on one anchor all
  // show. No prototype: a stored anchor named "constructor" is just a key.
  let committed: Record<string, CommittedItem[]> = Object.create(null);
  function setCommitted(items: CommittedItem[]) {
    committed = Object.create(null);
    items.forEach((x) => { (committed[x.anchor] = committed[x.anchor] || []).push(x); });
  }
  setCommitted(opts.committed);
  // a host without sign-in: the name the reviewer gave, sent with each request
  let named = '';
  if (!opts.reviewer) try { named = (localStorage.getItem('ox-plan-reviewer') || '').trim(); } catch (e) { /* asked for on Submit */ }
  function who() { return opts.reviewer || named; }
  let on = false;
  let offline = false; // the transport's last answer, or the host through setOffline, said it is unreachable
  let destroyed = false;
  let inflight = 0; // a delivery or an approval awaiting its answer
  let flushing = false, again = false;
  let waiter: (() => void) | null = null;
  // drafts by anchor as of the last paint; every change to them repaints
  let ds: Record<string, Mark> = Object.create(null);
  let placed: Placed[] = []; // what the document painted, in reading order
  let unplaced: string[] = []; // anchors with a comment but nothing on the page

  function committedState(a: string) { const arr = committed[a] || []; return arr.length ? repOf(arr).state : ''; }

  // paintItems is what the document paints: per anchor, your unsent mark,
  // else the representative committed one; a highlight brings its words.
  // Only anchors the document computes can be placed; the rail lists the
  // rest, the whole plan first. Who marked what, and their notes, stay here:
  // the rail shows them.
  function paintItems(): PaintItem[] {
    const out: PaintItem[] = [];
    Object.keys(committed).concat(Object.keys(ds)).forEach((a, i, all) => {
      if (all.indexOf(a) !== i || !/^[hq][0-9a-f]{8}$/.test(a)) return;
      const carr = committed[a] || [];
      const c = carr.length ? repOf(carr) : null, m = ds[a];
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
    if (destroyed) return;
    ds = Object.create(null);
    store.drafts().forEach((m) => { ds[m.anchor] = m; });
    const n = Object.keys(ds).length;
    countEl.textContent = n ? (n + ' unsent') : '';
    port.send({ type: 'paint', items: paintItems() }); // answered by 'placed'
  }
  function onPlaced(items: Placed[]) {
    placed = items;
    // the whole plan names nothing on the page, so it is never unplaced
    const seen: Record<string, boolean> = Object.create(null);
    seen[WHOLE.a] = true;
    placed.forEach((p) => { seen[p.anchor] = true; });
    // comments whose text is gone from the page — rewritten by the agent, or
    // edited while this tab was away — keep a rail row, so an addressed one can
    // still be accepted or reopened
    unplaced = Object.keys(committed).concat(Object.keys(ds)).filter((a, i, all) => !seen[a] && all.indexOf(a) === i);
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
    if (!open.length && !problems) return;
    const bar = orphanBar = document.createElement('div');
    bar.className = 'rev-orphans';
    let html = problems ? '<div class="rev-orphans-h">⚠ This review could not be fully read or holds conflicting copies (' + problems + ' problem(s)): notes may be missing or shown resolved.</div>' : '';
    if (open.length) {
      const items = open.map((o) => '<li><strong>' + esc(o.status) + '</strong> ' + esc(o.label || o.anchor) + (o.note ? ' — ' + esc(o.note) : '') + '</li>').join('');
      html += '<div class="rev-orphans-h">⚠ ' + open.length + ' review note(s) no longer anchored (the text changed) — still open:</div><ul>' + items + '</ul>';
    }
    bar.innerHTML = html + '<button class="rev-orphans-x">dismiss</button>';
    body.appendChild(bar);
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
    // the whole plan first, then reading order, highlights interleaved with
    // element rows, as the document placed them, and rows with nothing on the
    // page last. Multi-user: one row per reviewer's committed mark on an
    // anchor, plus your unsent local mark.
    const waiting = store.entries();
    const rows: Row[] = [];
    function add(at: Row['at'], a: string, section: string, label: string, quote: string) {
      (committed[a] || []).forEach((c) => {
        rows.push({
          at, a, quote, status: c.status, note: c.note,
          section: c.section || section, label: c.label || label,
          state: c.state, reviewer: c.reviewer || '', tag: '',
        });
      });
      const m = ds[a];
      if (m) {
        rows.push({
          at, a, quote, status: m.status, note: m.note,
          section: m.section || section, label: m.label || label,
          state: '', reviewer: who() || '(you)',
          tag: store.unsaved(a) ? 'unsaved' : queued(waiting, m) ? 'queued' : 'unsent',
        });
      }
    }
    add(null, WHOLE.a, WHOLE.section, WHOLE.label, '');
    const nth: Record<string, number> = Object.create(null);
    placed.forEach((p) => {
      const k = nth[p.anchor] = (nth[p.anchor] === undefined ? 0 : nth[p.anchor] + 1);
      add({ anchor: p.anchor, nth: k }, p.anchor, p.section, p.label, p.quote || '');
    });
    unplaced.forEach((a) => {
      const m = ds[a];
      const its: Array<CommittedItem | Mark> = (committed[a] || []).concat(m ? [m as unknown as CommittedItem] : []);
      const q = its.filter((x) => x.quote)[0];
      const any = q || its.filter((x) => x.label)[0] || its[0];
      add(null, a, any.section || '', any.label || '', q ? q.quote || '' : '');
    });
    return rows;
  }
  function renderRail() {
    if (!rail) {
      rail = document.createElement('aside'); rail.className = 'rev-rail'; body.appendChild(rail);
      rail.addEventListener('click', (ev) => {
        const t = ev.target as Element;
        if (t.closest('.rev-rail-show, .rev-rail-hide')) toggleRail();
        const plan = t.closest('.rev-rail-plan');
        if (plan) { const b = plan.getBoundingClientRect(); openPop(WHOLE, { left: b.left, bottom: b.bottom }, true); }
      });
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
    const plan = on ? '<button class="rev-rail-plan">+ Comment on the whole plan</button>' : '';
    if (!rows.length) {
      r.innerHTML = '<div class="rev-rail-h">Comments' + hide + '</div>' + plan + '<div class="rev-rail-empty">Click any section, risk, or row — or highlight text — to leave a comment.</div>';
      return;
    }
    let html = '<div class="rev-rail-h">Comments <span class="rev-rail-n">' + rows.length + '</span>' + hide + '</div>' + plan + '<ul class="rev-rail-list">';
    rows.forEach((row, i) => {
      const whole = row.a === WHOLE.a; // on no element: its heading names it, and it has no text to change
      let tag = '';
      if (row.tag) tag = '<span class="rev-rail-tag ' + row.tag + '">' + row.tag + '</span>';
      else if (row.state && row.state !== 'open') tag = '<span class="rev-rail-tag ' + esc(row.state) + '">' + esc(row.state) + '</span>';
      if (!row.at && !whole) tag += '<span class="rev-rail-tag">text changed</span>';
      // a highlight's row leads with its words; its label is those words, so a
      // note-less highlight shows nothing more
      const content = (row.quote ? '<div class="rev-rail-quote">' + esc(row.label) + '</div>' : '') +
        (row.note ? '<div class="rev-rail-note">' + esc(row.note) + '</div>'
          : row.quote || whole ? '' : '<div class="rev-rail-note muted">' + esc(row.label) + '</div>');
      const whoTag = row.reviewer ? '<span class="rev-rail-who">' + esc(row.reviewer) + '</span>' : '';
      html += '<li class="rev-rail-item" data-i="' + i + '" data-status="' + esc(row.status) + '">' +
        '<span class="rev-rail-glyph">' + glyphFor(row.status) + '</span>' +
        '<div class="rev-rail-body"><div class="rev-rail-sec"><span class="rev-rail-sec-t">' + esc(whole ? WHOLE.label : row.section || '(plan)') + '</span>' + whoTag + tag + '</div>' + content + '</div></li>';
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
  // replacing: another note opens in its place, so a waiting reload keeps waiting
  function closePop(replacing?: boolean) {
    if (pop) { pop.remove(); pop = null; sendMode(); }
    port.send({ type: 'focus', anchor: null });
    if (!replacing) idle();
  }
  // t is what a note is about: an element or a highlight — anchor, section,
  // label, and a highlight's quote. rect is where it opens below, in this window.
  // fresh: a new comment, which never resolves the marks already there.
  function openPop(t: About, rect: { left: number; bottom: number }, fresh?: boolean) {
    closePop(true);
    const a = t.a;
    const cstate = committedState(a);
    const p = pop = document.createElement('div'); p.className = 'rev-pop';
    // a highlight's note names its words: the page selection is gone once the
    // reviewer clicks into the note, so the focus highlight keeps them visible too
    const quote = t.quote ? '<div class="rev-quote">' + esc(t.label) + '</div>' : '';
    port.send({ type: 'focus', anchor: a });
    // a committed, addressed/verified item gets Accept/Reopen (close the loop)
    // when this reviewer may resolve; everything else gets the mark-up controls.
    if (live && opts.resolve && !fresh && (cstate === 'addressed' || cstate === 'verified')) {
      p.innerHTML = quote + '<div class="rev-cap">Agent marked this ' + cstate + '.</div>' +
        '<textarea class="rev-note" placeholder="reopen note (optional)"></textarea>' +
        '<div class="rev-row"><button class="rev-accept">Accept</button><button class="rev-reopen">Reopen</button></div>';
      placePop(rect);
      (p.querySelector('.rev-accept') as HTMLElement).onclick = () => { accept(a); };
      (p.querySelector('.rev-reopen') as HTMLElement).onclick = () => { reopen(a, (p.querySelector('.rev-note') as HTMLTextAreaElement).value.trim()); };
      return;
    }
    const existing = ds[a] || { status: 'request-change', note: '' };
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
      const m: Omit<Mark, 'at'> = { anchor: a, section: t.section, label: t.label, status, note: (p.querySelector('.rev-note') as HTMLTextAreaElement).value.trim() };
      if (t.quote) m.quote = t.quote;
      const kept = store.save(m);
      paint(); closePop();
      if (!kept) showSync('err', 'This browser could not save your note: it lives in this tab only and is lost when the tab closes. Free up browser storage, then save it again.');
    };
    (p.querySelector('.rev-del') as HTMLElement).onclick = () => { store.remove(a); paint(); closePop(); };
  }
  // The note opens just below where the reviewer acted, never over it.
  function placePop(rect: { left: number; bottom: number }) {
    const p = pop as HTMLElement;
    body.appendChild(p);
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
  }, { capture: true, signal });

  // --- the outbox: every operation is frozen into storage BEFORE anything
  // sends it, and resent with the same id and bytes until a receipt for that
  // id says the host owns it, so a reload, a retry or a second tab never turns
  // one action into two. Nothing storage refused is ever sent. ---
  // queue freezes one operation and sends the outbox. retry runs the action
  // again once the reviewer has given a name, for a host without sign-in.
  function queue(act: Act, build: (id: string, reviewer: string) => string, stamps: Record<string, number>, retry: () => void) {
    if (!who()) { askName('Your name, so teammates know whose feedback this is:', retry); return; }
    const id = newId();
    // named, never who(): a signed-in host's server stamps the caller itself
    if (!store.freeze(act, id, build(id, named), stamps)) {
      showSync('err', 'Not sent — this browser could not store it, so nothing was sent. Free up browser storage, then try again.');
      return;
    }
    closePop();
    flush();
  }
  // Submit freezes the marks no entry carries yet as one round and sends
  // every entry; with nothing new it resends what is waiting.
  function submit() {
    if (!live) { exportJSON(); return; }
    const waiting = store.entries();
    const fresh = store.drafts().filter((m) => !queued(waiting, m));
    if (!fresh.length) {
      if (waiting.length) flush();
      else showSync('info', 'No marks yet. Toggle Review, click a section, leave a note.');
      return;
    }
    const stamps: Record<string, number> = Object.create(null);
    fresh.forEach((m) => { stamps[m.anchor] = m.at; });
    queue('submit', (id, r) => submitBody(id, r, fresh), stamps, submit);
  }
  function accept(a: string) {
    queue('accept', (id, r) => acceptBody(id, r, a), {}, () => accept(a));
  }
  // A reopen raises the item again with what it is about — its section,
  // label, and a highlight's words — or the agent's digest names it by anchor
  // alone, and a highlight's anchor cannot be turned back into its words.
  function reopen(a: string, note: string) {
    const c = (committed[a] || []).filter((x) => x.label || x.quote)[0];
    const item = c ? { anchor: a, section: c.section, label: c.label, quote: c.quote } : { anchor: a };
    queue('reopen', (id, r) => reopenBody(id, r, item, note), {}, () => reopen(a, note));
  }
  // flush tries even while the host looks offline: a host it reaches is back.
  function flush() {
    if (!transport || destroyed) return;
    if (flushing) { again = true; return; }
    if (!store.entries().length) return;
    flushing = true; inflight++; paintSubmit();
    showSync('pending', 'Sending…');
    deliver(store, transport, report, () => destroyed).finally(() => {
      flushing = false; inflight--; paintSubmit();
      if (again && !destroyed) { again = false; flush(); }
      idle();
    });
  }
  function report(e: Entry, r: Receipt) {
    if (destroyed) return;
    paint();
    // any answer but offline came from the host, so it is reachable
    setOffline(r.state === 'offline');
    const verb = e.act === 'accept' ? 'Accepted' : e.act === 'reopen' ? 'Reopened' : 'Sent';
    if (r.state === 'owned') reportSync(syncMessage(verb, r));
    else if (r.state === 'refused') showSync('err', 'Not saved — the server refused it (' + r.message + ').' + (e.act === 'submit' ? ' Your marks are kept: Submit sends them as a new review.' : ''));
    else if (r.state === 'failed') showSync('err', 'Not sent (' + r.message + '). ' + (e.act === 'submit' ? 'Your marks are kept.' : 'It stays queued in this browser.'), [{ label: 'Retry', fn: flush }]);
    else showSync('warn', 'Review server offline — your feedback is queued in this browser and sends when the server is back.');
  }
  function approve() {
    const send = opts.approve as () => Promise<Approval>;
    showSync('ask', 'Approve this plan? This stamps it approved and closes the review loop.', [
      { label: 'Approve', fn: () => {
        showSync('pending', 'Approving…');
        inflight++;
        Promise.resolve().then(send).catch((err): Approval => ({ state: 'failed', message: err instanceof Error ? err.message : String(err) })).then((r) => {
          inflight--;
          if (!destroyed) {
            setOffline(r.state === 'offline');
            if (r.state === 'owned') {
              const m = syncMessage('Approved', r);
              m.text += ' — you can close this tab.';
              reportSync(m);
            } else if (r.state === 'offline') showSync('err', 'The review server is offline — Approve can’t be sent right now.');
            else showSync('err', 'Not approved (' + r.message + ').', [{ label: 'Retry', fn: approve }]);
          }
          idle();
        });
      } },
      { label: 'Cancel', fn: hideSync },
    ]);
  }

  function exportJSON() {
    const items = store.drafts().map(wireItem);
    if (!items.length) { showSync('info', 'No marks yet. Toggle Review, click a section, leave a note.'); return; }
    if (!who()) { askName('Your name, so teammates know whose feedback this is:', exportJSON); return; }
    // a re-export of unchanged marks keeps its id, so `ox plan feedback apply`
    // run twice on the same feedback lands one round
    const json = JSON.stringify({ id: store.exportId(items), slug, reviewer: who(), items }, null, 2);
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
    body.appendChild(el);
    if (field) {
      const f = field;
      f.focus();
      f.onkeydown = (e) => { if (e.key === 'Enter' && actions && actions[0]) { e.preventDefault(); actions[0].fn(f.value.trim()); } };
    }
    // good news fades; a warning lingers; an error or a question waits for the reviewer
    const ttl = ({ ok: 10000, info: 8000, warn: 20000 } as Record<string, number>)[kind];
    if (ttl) syncTimer = setTimeout(() => { if (syncEl === el) hideSync(); }, ttl);
  }
  // reportSync shows a send result and keeps it for one reload: ox's local
  // server reloads the page right after it answers, which would wipe it.
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
        named = n;
        try { localStorage.setItem('ox-plan-reviewer', named); } catch (e) { /* named for this page only */ }
        hideSync(); updateWho(); paint();
        if (then) then();
      } },
      { label: 'Cancel', fn: hideSync },
    ], named);
  }

  // --- connection state: the page must never LOOK live when the host is
  // gone. Offline = red pill + sticky banner; drafts and the outbox stay in
  // storage, so nothing a reviewer wrote is lost. ---
  let connEl: HTMLElement | null = null, offlineBar: HTMLElement | null = null, toastEl: HTMLElement | null = null;
  function paintConn() {
    if (!connEl) return;
    connEl.textContent = offline ? '● offline' : '● live';
    connEl.className = 'rev-conn ' + (offline ? 'off' : 'ok');
    connEl.title = offline ? 'Review server unreachable — feedback is NOT reaching it' : 'Connected to the review server';
  }
  function setOffline(down: boolean) {
    if (offline === down || destroyed) return;
    offline = down;
    body.classList.toggle('rev-offline', down);
    paintConn();
    if (offlineBar) { offlineBar.remove(); offlineBar = null; }
    if (!down) { flush(); return; }
    const n = Object.keys(ds).length;
    const cmd = opts.restartCommand;
    const bar = offlineBar = document.createElement('div');
    bar.className = 'rev-offline-bar';
    bar.innerHTML = '<strong>⚠ Review server offline</strong> — feedback is NOT reaching it. ' +
      (n ? n + ' unsent mark(s) are' : 'Your marks are') + ' kept in this browser and sent on reconnect.' +
      (cmd ? ' Restart: <code>' + esc(cmd) + '</code> <button class="rev-offline-copy">copy</button>' : '');
    body.appendChild(bar);
    const copy = bar.querySelector('.rev-offline-copy') as HTMLElement | null;
    if (copy) copy.onclick = () => {
      if (navigator.clipboard) navigator.clipboard.writeText(cmd || '').then(() => { copy.textContent = 'copied ✓'; }).catch(() => {});
    };
  }
  function toast(msg: string) {
    if (toastEl) toastEl.remove();
    const el = toastEl = document.createElement('div');
    el.className = 'rev-toast';
    el.textContent = msg;
    body.appendChild(el);
    // this toast only: a newer one may have replaced it before the timer fires
    setTimeout(() => { el.remove(); if (toastEl === el) toastEl = null; }, 8000);
  }
  function idle() {
    if (waiter && !inflight && !pop) { const fn = waiter; waiter = null; fn(); }
  }

  // controls
  const bar = document.createElement('div');
  bar.className = 'rev-bar';
  bar.innerHTML = (live ? '<span class="rev-conn"></span>' : '') +
    '<button class="rev-toggle" title="Toggle review mode">Review</button><span class="rev-count"></span>' +
    (live && !opts.reviewer ? '<button class="rev-who" title="Set the name teammates see on your comments"></button>' : '') +
    '<button class="rev-submit" title="Send feedback to the agent">' + (live ? 'Submit' : 'Export') + '</button>' +
    (opts.approve ? '<button class="rev-approve" title="Approve and close the loop">Approve</button>' : '');
  body.appendChild(bar);
  const countEl = bar.querySelector('.rev-count') as HTMLElement;
  connEl = bar.querySelector('.rev-conn');
  paintConn();
  const whoEl = bar.querySelector('.rev-who') as HTMLElement | null;
  function updateWho() { if (whoEl) whoEl.textContent = named ? ('You: ' + named) : 'Set name'; }
  updateWho();
  if (whoEl) whoEl.onclick = () => { askName('Your name (shown to teammates on this plan):'); };
  const submitEl = bar.querySelector('.rev-submit') as HTMLButtonElement;
  function paintSubmit() {
    if (!live || destroyed) return;
    submitEl.disabled = flushing; // a second click while a send is in flight would race it
    submitEl.textContent = flushing ? 'Sending…' : 'Submit';
  }
  // Review is a mode: while on, clicks mark up instead of navigating. A mode has
  // to announce itself on entry and keep its exit in view — otherwise the only
  // signal is a green button and nothing visibly changes until a hover.
  const toggleEl = bar.querySelector('.rev-toggle') as HTMLElement;
  let hintBubble: HTMLElement | null = null;
  function setReview(next: boolean, silent?: boolean) {
    on = next; toggleEl.classList.toggle('on', on);
    body.classList.toggle('rev-on', on); // the controls' own look; a framed document marks its own body
    sendMode();
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
  if (opts.approve) (bar.querySelector('.rev-approve') as HTMLElement).onclick = approve;
  // Esc peels one layer at a time — an open note first, then the mode — and
  // fires while typing in the note too; that is the point of Esc.
  function escape(): boolean {
    if (pop) { closePop(); return true; }
    if (on) { setReview(false); return true; }
    return false;
  }
  // Keyboard, here rather than scaffold.js so authored HTML plans (chrome.js
  // adds no key map) get the same keys; a framed document forwards the keys
  // pressed inside it. `r` toggles the mode from anywhere but a text field.
  // Keys are shared with the page: one an earlier listener marked handled (an
  // authored inspector closing on Esc) is left alone, and one acted on here is
  // marked handled so later listeners that honor defaultPrevented skip it.
  document.addEventListener('keydown', (e) => {
    if (e.defaultPrevented) return;
    if (e.key === 'Escape') { if (escape()) e.preventDefault(); return; }
    if (e.key === 'r' && !typing(e) && !e.metaKey && !e.ctrlKey && !e.altKey) { setReview(!on); e.preventDefault(); }
  }, { signal });

  // first-visit discoverability: a one-time pointer at the Review toggle.
  try {
    if (!localStorage.getItem('ox-plan-rev-seen')) {
      const hint = hintBubble = document.createElement('div');
      hint.className = 'rev-hint';
      hint.textContent = '↓ Click Review to mark up this plan';
      // over the toggle, which a live bar's connection state pushes right
      hint.style.left = toggleEl.getBoundingClientRect().left + 'px';
      body.appendChild(hint);
      setTimeout(() => { if (hintBubble === hint) { hint.remove(); hintBubble = null; } }, 9000);
    }
  } catch (e) { /* no storage: no hint */ }

  // Another tab on this plan saved, deleted, sent or had something
  // acknowledged: a repaint reads it from storage. One answer clears a key
  // per mark it carried, so a burst of events gets one repaint. e.key is null
  // when storage was cleared wholesale.
  let repaint: ReturnType<typeof setTimeout> | undefined;
  window.addEventListener('storage', (e) => {
    if (e.key === null || e.key.indexOf(store.prefix) === 0) { clearTimeout(repaint); repaint = setTimeout(paint, 50); }
  }, { signal });
  // the browser is back on a network: a host it could not reach may answer now
  window.addEventListener('online', flush, { signal });

  let started = false;
  port.listen((m) => {
    switch (m.type) {
      case 'ready':
        // the document tools are listening: paint, and restore what a reload interrupted
        paint();
        sendMode();
        if (started) return;
        started = true;
        // A live reload — the agent addressed an item, or the server came back — must
        // not drop a reviewer mid-review back to reading mode. Restored silently: the
        // reviewer did not just enter, so no entry toast.
        try { if (sessionStorage.getItem(ON_KEY)) setReview(true, true); } catch (e) { /* starts in reading mode */ }
        break;
      case 'placed':
        onPlaced(m.items);
        break;
      case 'target': {
        // a framed document's rect is in the frame's viewport: move it into this one
        const f = opts.frame ? opts.frame.getBoundingClientRect() : { left: 0, top: 0 };
        openPop({ a: m.target.anchor, section: m.target.section, label: m.target.label, quote: m.target.quote },
          { left: m.target.rect.left + f.left, bottom: m.target.rect.bottom + f.top });
        break;
      }
      case 'refused':
        closePop();
        toast(m.reason === 'repeated'
          ? '“' + clip(m.quote, 40) + '” appears ' + m.count + ' times in this section — highlight a longer phrase so the comment points at one place.'
          : 'Highlight text within one section to comment on it.');
        break;
      case 'dismiss':
        if (on || pop) closePop();
        break;
      case 'key':
        if (m.key === 'Escape') escape();
        else setReview(!on);
        break;
    }
  });

  restoreSync();
  paint();
  // unsent marks that survived a restart or reload — tell the reviewer.
  const restored = Object.keys(ds).length;
  if (live && restored && !store.entries().length) toast(restored + ' unsent mark(s) restored — Submit to send them.');
  // An operation this page never saw acknowledged — a reload, a crash, or the
  // host dropping mid-send — is resent with its original id and bytes; the
  // host answers one it already has as stored, so this can't double-post.
  flush();

  return {
    update(items, n) {
      setCommitted(items);
      problems = n || 0;
      paint();
    },
    setOffline,
    whenIdle(fn) { waiter = fn; idle(); },
    destroy() {
      if (destroyed) return;
      waiter = null;
      closePop();
      destroyed = true;
      off.abort();
      clearTimeout(repaint);
      port.listen(() => {});
      port.send({ type: 'mode', on: false, open: false });
      port.send({ type: 'paint', items: [] });
      [bar, rail, orphanBar, syncEl, toastEl, offlineBar, hintBubble].forEach((el) => { if (el) el.remove(); });
      body.classList.remove('rev-on', 'rev-offline');
    },
  };
}
