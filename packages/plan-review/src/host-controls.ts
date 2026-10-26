// Host controls: the review bar, the composer, the comments rail and the sync
// line, plus the reviewer's drafts and the outbox that delivers what they
// submit. They are trusted host code and run in the host's own window: the
// plan's own window for ox's local review page (local.ts), the page around
// the plan's frame for a host that frames it. They reach the plan only
// through the port to the document tools, which pick targets, capture what
// the reviewer draws around, and paint; and they reach the host's store only
// through its transport. With a transport, a reviewer the host lets resolve
// can also Accept (verify) or Reopen an item the author addressed, and the
// rail keeps orphaned notes whose anchored text changed (never lose
// feedback). Without one (a file:// page) Submit exports the feedback as JSON.

import { clip } from './anchor.js';
import { CHROME, typing } from './document-tools.js';
import { originalView } from './original.js';
import type { HostPort } from './ports.js';
import { startPresence, type Presence, type PresenceTransport } from './presence.js';
import { drawingPaint, paintItem, STATES, STATUSES, type DrawingPaint, type PaintItem, type Placed, type State, type Status, type Tint } from './protocol.js';
import { checkRegion, DRAWING, drawingLabel, type Region } from './region.js';
import { acceptBody, deliver, newId, openStore, queued, reopenBody, roundItems, submitBody, syncMessage, wireItem, type Act, type Approval, type Entry, type Mark, type Receipt, type SyncMessage, type Transport } from './state.js';

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
  /** The author's note on the item's latest resolution, while it stands. */
  resolutionNote?: string;
  /** A drawing's record, on an item anchored "q-<its id>". */
  region?: Region;
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
  /** The frame holding the plan, when the document tools run in one: target rects are in its viewport, and the controls sit over its box instead of the window's. */
  frame?: HTMLIFrameElement;
  /** Open in review mode, for a host that mounts the controls because the reviewer asked to review. */
  reviewing?: boolean;
  /** How to bring the host back, shown with a copy button while it is offline. */
  restartCommand?: string;
  /**
   * The revision of the plan the reviewer sees: "sha256:" and the hex SHA-256
   * of the bytes the page shows, which the host's server must serve and no
   * other. Each drawing is stamped with it. Without it the bar offers no Draw.
   */
  revision?: string;
  /**
   * The plan's live presence room: the bar lists who else has this plan
   * open, and their pointers are drawn over the plan with their names when
   * they show the same revision. Needs revision.
   */
  presence?: PresenceTransport;
}

/** A mounted set of controls. */
export interface HostControls {
  /** Show the review as the host read it again. */
  update(committed: CommittedItem[], problems?: number): void;
  /** Change mode like the Review button, keeping drafts, pending deliveries and presence connected. */
  setReviewing(reviewing: boolean): void;
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
  answer: string; // the author's resolution note
  region?: Region;
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
// a committed mark's color: resolved, declined, or still open
const stateTint = (state: string): Tint => (state === 'addressed' || state === 'verified' ? 'sage' : state === 'wontfix' ? 'faint' : 'amber');
// why a stroke made no drawing, said to the reviewer; a cancel says nothing
const DRAW_REFUSED: Record<string, string> = {
  'no-target': 'Nothing in that oval can take a comment. Draw around a paragraph, an item, a row or part of a diagram.',
  'too-large': 'That oval holds too much of the plan to keep. Draw around less of it.',
  'too-small': 'Drag to draw a larger oval around what you mean.',
  'layout-changed': 'The page changed while you drew. Draw the oval again.',
};
const REVISION = /^sha256:[0-9a-f]{64}$/;
// a comment on the plan as a whole: WholePlanAnchor in pkg/planreview, with
// the label readers that predate it print (plan-review-ledger-contract.md 3.10)
const WHOLE: About = { a: 'plan', section: '', label: 'Whole plan' };
const BUBBLE = '<svg width="14" height="14" viewBox="0 0 16 16" aria-hidden="true"><path d="M3 2.5h10A1.5 1.5 0 0 1 14.5 4v6a1.5 1.5 0 0 1-1.5 1.5H7.5L4 14.5v-3H3A1.5 1.5 0 0 1 1.5 10V4A1.5 1.5 0 0 1 3 2.5z" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round"/></svg>';

// esc makes untrusted text safe in element content and in quoted attributes.
export function esc(s: string | undefined): string {
  return (s || '').replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');
}
// A rail tag's class names a resolution state; anything else gets none.
const STATE_TAG: Record<string, string> = { addressed: 'addressed', verified: 'verified', wontfix: 'wontfix' };
const TEXT_FIELDS = ['section', 'label', 'quote', 'note', 'reviewer', 'resolutionNote'] as const;
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
  let invalid = 0; // committed items left out: a status or state outside the contract
  // anchor -> [mark, …]: multi-user, so several reviewers on one anchor all
  // show. No prototype: a stored anchor named "constructor" is just a key.
  let committed: Record<string, CommittedItem[]> = Object.create(null);
  // Ledger records are member-written. An item with a status or state the
  // contract doesn't name would reach the rail's markup and fail every paint,
  // so it is left out and counted as a problem reading the review.
  function setCommitted(items: CommittedItem[]) {
    committed = Object.create(null);
    invalid = 0;
    items.forEach((x) => {
      // a member wrote the record: one of the wrong shape would stop the rail drawing
      if (!x || typeof x.anchor !== 'string' || STATUSES.indexOf(x.status) < 0 || STATES.indexOf(x.state) < 0 ||
          TEXT_FIELDS.some((k) => x[k] !== undefined && typeof x[k] !== 'string')) { invalid++; return; }
      // a drawing that does not read as one keeps its row, by its label
      if (x.region !== undefined && (checkRegion(x.region) || x.anchor !== 'q-' + x.region.id)) {
        invalid++;
        x = Object.assign({}, x);
        delete x.region;
      }
      (committed[x.anchor] = committed[x.anchor] || []).push(x);
    });
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
  // Draw around, offered only with the revision every drawing is stamped with
  const revision = opts.revision && REVISION.test(opts.revision) ? opts.revision : '';
  let tool = false; // the Draw tool is selected
  let pending: Region | null = null; // a capture whose note is open, kept only on Save
  const ownedDrawings = new Set<string>(); // receipts may arrive before the host refreshes committed
  let presence: Presence | null = null; // who else is here, and their pointers
  // a drawing's record: the capture being noted, else your draft's, else the Ledger's
  function regionOf(a: string): Region | undefined {
    if (pending && 'q-' + pending.id === a) return pending;
    if (ds[a] && ds[a].region) return ds[a].region;
    return (committed[a] || []).map((x) => x.region).filter((r) => r)[0];
  }

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
          tint: stateTint(c.state),
        };
      } else return;
      // a highlight is found by its words: your mark's, else the first committed one's
      const q = m && m.quote ? m : carr.filter((x) => x.quote)[0];
      if (q && q.quote) { it.quote = q.quote; it.section = q.section || ''; if (q.label) it.label = q.label; }
      // one item the protocol refuses would drop the whole paint
      const ok = paintItem(it);
      if (ok) out.push(ok);
    });
    return out;
  }

  // paintDrawings is every drawing to place: per anchor, your unsent mark's,
  // else the representative committed one's, and the capture being noted.
  // Its geometry and targets only: who drew it, and their note, stay here.
  function paintDrawings(): DrawingPaint[] {
    const out: DrawingPaint[] = [];
    const waiting = store.entries();
    Object.keys(committed).concat(Object.keys(ds), pending ? ['q-' + pending.id] : []).forEach((a, i, all) => {
      const r = all.indexOf(a) === i && DRAWING.test(a) ? regionOf(a) : undefined;
      if (!r) return;
      const m = ds[a], carr = committed[a] || [];
      const d = drawingPaint({
        anchor: a,
        kind: carr.length || ownedDrawings.has(a) || waiting.some((e) => e.stamps[a] !== undefined) ? 'committed' : 'unsent',
        tint: m ? HL_TINT[m.status] || 'gold' : carr.length ? stateTint(repOf(carr).state) : 'gold',
        ellipse: r.ellipse,
        targets: r.targets.map((t) => ({ anchor: t.anchor, box: t.box, nth: t.nth, of: t.of })),
        open: r.layout.open,
        checked: r.layout.checked,
      });
      if (d) out.push(d);
    });
    return out;
  }

  function paint() {
    if (destroyed) return;
    place();
    ds = Object.create(null);
    store.drafts().forEach((m) => { ds[m.anchor] = m; });
    const n = Object.keys(ds).length;
    countEl.textContent = n ? (n + ' unsent') : '';
    port.send({ type: 'paint', items: paintItems(), drawings: paintDrawings() }); // answered by 'placed'
  }
  let outcome: Record<string, string> = Object.create(null); // drawing anchor -> where it landed
  function onPlaced(items: Placed[]) {
    outcome = Object.create(null);
    items.forEach((p) => { if (p.outcome) outcome[p.anchor] = p.outcome; });
    // the whole plan names nothing on the page, so it is never unplaced
    const seen: Record<string, boolean> = Object.create(null);
    seen[WHOLE.a] = true;
    // one row per comment, however often the page repeats its anchor: the
    // plan's own scripts can send this answer with 10,000 copies of one. A
    // drawing that is missing or ambiguous is not on the page.
    placed = items.filter((p) => p.outcome !== 'missing' && p.outcome !== 'ambiguous' && !seen[p.anchor] && (seen[p.anchor] = true));
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
    const n = problems + invalid;
    if (!open.length && !n) return;
    const bar = orphanBar = document.createElement('div');
    bar.className = 'rev-orphans';
    let html = n ? '<div class="rev-orphans-h">⚠ This review could not be fully read or holds conflicting copies (' + n + ' problem(s)): notes may be missing or shown resolved.</div>' : '';
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
          answer: c.state !== 'open' && c.resolutionNote ? c.resolutionNote : '',
          region: c.region,
        });
      });
      const m = ds[a];
      if (m) {
        rows.push({
          at, a, quote, status: m.status, note: m.note,
          section: m.section || section, label: m.label || label,
          state: '', reviewer: who() || '(you)',
          tag: store.unsaved(a) ? 'unsaved' : queued(waiting, m) ? 'queued' : 'unsent',
          answer: '',
          region: m.region,
        });
      }
    }
    add(null, WHOLE.a, WHOLE.section, WHOLE.label, '');
    placed.forEach((p) => add({ anchor: p.anchor, nth: 0 }, p.anchor, p.section, p.label, p.quote || ''));
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
        if (plan) { const b = plan.getBoundingClientRect(); openPop(WHOLE, { left: b.left, bottom: b.bottom }, true); popByHost = true; }
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
      r.innerHTML = '<div class="rev-rail-h">Comments' + hide + '</div>' + plan + '<div class="rev-rail-empty">Click any section, risk, or row — or highlight text' +
        (revision ? ', or Draw around part of the plan' : '') + ' — to leave a comment.</div>';
      return;
    }
    let html = '<div class="rev-rail-h">Comments <span class="rev-rail-n">' + rows.length + '</span>' + hide + '</div>' + plan + '<ul class="rev-rail-list">';
    rows.forEach((row, i) => {
      const whole = row.a === WHOLE.a; // on no element: its heading names it, and it has no text to change
      let tag = '';
      if (row.tag) tag = '<span class="rev-rail-tag ' + row.tag + '">' + row.tag + '</span>';
      else if (STATE_TAG[row.state]) tag = '<span class="rev-rail-tag ' + STATE_TAG[row.state] + '">' + STATE_TAG[row.state] + '</span>';
      if (!row.at && !whole) tag += outcome[row.a] === 'ambiguous'
        ? '<span class="rev-rail-tag" title="What it was drawn around now appears a different number of times">repeated</span>'
        : '<span class="rev-rail-tag">text changed</span>';
      else if (outcome[row.a] === 'hidden') tag += '<span class="rev-rail-tag" title="Collapsed or on another tab: click to show it">hidden</span>';
      // a highlight's row leads with its words; its label is those words, so a
      // note-less highlight shows nothing more
      const content = (row.quote ? '<div class="rev-rail-quote">' + esc(row.label) + '</div>' : '') +
        (row.note ? '<div class="rev-rail-note">' + esc(row.note) + '</div>'
          : row.quote || whole ? '' : '<div class="rev-rail-note muted">' + esc(row.label) + '</div>') +
        (row.answer ? '<div class="rev-rail-answer">↳ ' + esc(row.answer) + '</div>' : '') +
        (row.region ? '<button class="rev-rail-orig" title="What lay under this drawing when it was drawn">View original</button>' : '');
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
        if (row.region && (ev.target as Element).closest('.rev-rail-orig')) { showOriginal(row.region); ev.stopPropagation(); return; }
        // A drawing takes no click on the page, so its row shows it and opens
        // its note; anything with nothing on the page opens from its row too
        if (row.at) port.send({ type: 'reveal', anchor: row.at.anchor, nth: row.at.nth });
        if (!row.at || DRAWING.test(row.a)) { openPop({ a: row.a, section: row.section, label: row.label, quote: row.quote }, { left: ev.clientX, bottom: ev.clientY }); popByHost = true; ev.stopPropagation(); }
      };
    });
  }

  let pop: HTMLElement | null = null;
  let popAbout: About | null = null;
  let popNote = ''; // the open note's text when it opened
  let popByHost = false; // opened from the rail, not by a pick in the plan's frame
  function sendMode() { port.send({ type: 'mode', on, open: !!pop, draw: tool }); }
  // The open note is held when the reviewer opened it from the rail or typed
  // in it: only their own Esc, Save or Delete ends it, never the plan's frame.
  // A plan that closed every note it could would leave no way to object, and
  // the rail's notes are the ones it cannot open.
  function held(): boolean {
    const n = pop ? pop.querySelector('.rev-note') as HTMLTextAreaElement | null : null;
    return !!pop && (popByHost || (!!n && n.value.trim() !== popNote));
  }
  // replacing: another note opens in its place, so a waiting reload keeps waiting
  function closePop(replacing?: boolean) {
    popByHost = false;
    if (pop) {
      pop.remove(); pop = null; sendMode();
      popAbout = null;
      // a capture is kept only by Save
      if (pending) { pending = null; paint(); }
    }
    port.send({ type: 'focus', anchor: null });
    if (!replacing) idle();
  }
  // t is what a note is about: an element or a highlight — anchor, section,
  // label, and a highlight's quote. rect is where it opens below, in this window.
  // fresh: a new comment, which never resolves the marks already there.
  function openPop(t: About, rect: { left: number; bottom: number }, fresh?: boolean) {
    closePop(true);
    popAbout = t;
    const a = t.a;
    const cstate = committedState(a);
    const p = pop = document.createElement('div'); p.className = 'rev-pop';
    // What the note is about stays in view: a highlight's words (the page
    // selection is gone once the reviewer clicks into the note), else the
    // element's label, so a note is never filed under something unseen.
    const quote = '<div class="' + (t.quote ? 'rev-quote' : 'rev-about') + '">' + esc(clip(t.label || t.a, 280)) + '</div>';
    port.send({ type: 'focus', anchor: a });
    // a committed, addressed/verified item gets Accept/Reopen (close the loop)
    // when this reviewer may resolve; everything else gets the mark-up controls.
    if (live && opts.resolve && !fresh && (cstate === 'addressed' || cstate === 'verified')) {
      popNote = '';
      p.innerHTML = quote + '<div class="rev-cap">Agent marked this ' + cstate + '.</div>' +
        '<textarea class="rev-note" placeholder="reopen note (optional)"></textarea>' +
        '<div class="rev-row"><button class="rev-accept">Accept</button><button class="rev-reopen">Reopen</button></div>';
      placePop(rect);
      (p.querySelector('.rev-accept') as HTMLElement).onclick = () => { accept(a); };
      (p.querySelector('.rev-reopen') as HTMLElement).onclick = () => { reopen(a, (p.querySelector('.rev-note') as HTMLTextAreaElement).value.trim()); };
      return;
    }
    const existing = ds[a] || { status: 'request-change', note: '' };
    popNote = existing.note || '';
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
      // a drawing's mark carries what was drawn, whoever marks it
      const r = regionOf(a);
      if (r) m.region = r;
      const kept = store.save(m);
      pending = null;
      paint(); closePop();
      if (!kept) showSync('err', 'This browser could not save your note: it lives in this tab only and is lost when the tab closes. Free up browser storage, then save it again.');
    };
    (p.querySelector('.rev-del') as HTMLElement).onclick = () => { store.remove(a); pending = null; paint(); closePop(); };
  }
  // View original: a drawing redrawn from its record, in this window, never the plan's
  let original: HTMLElement | null = null;
  function closeOriginal() { if (original) { original.remove(); original = null; } }
  function showOriginal(r: Region) {
    closeOriginal();
    const el = original = document.createElement('div');
    el.className = 'rev-original';
    el.setAttribute('role', 'dialog');
    el.setAttribute('aria-label', 'The original drawing');
    const head = document.createElement('div');
    head.className = 'rev-original-h';
    const l = r.layout;
    head.textContent = 'Drawn on revision ' + r.plan_revision.slice(7, 19) + ' at ' + l.viewport.join(' × ') + ', ' + l.theme +
      (revision ? (r.plan_revision === revision ? ' · the revision shown' : ' · not the revision shown') : '');
    const x = document.createElement('button');
    x.className = 'rev-original-x';
    x.textContent = 'Close';
    x.onclick = closeOriginal;
    head.appendChild(x);
    el.appendChild(head);
    el.appendChild(originalView(r, document));
    body.appendChild(el);
  }
  // The box the controls sit over: the plan's frame, else the window.
  function box() {
    return opts.frame ? opts.frame.getBoundingClientRect() : { left: 0, top: 0, right: window.innerWidth, bottom: window.innerHeight };
  }
  // toHost moves a point in a framed document's viewport into this one, so
  // that the whole note (about 300 x 200) stays over the frame wherever the
  // plan says.
  function toHost(r: { left: number; bottom: number }) {
    const b = box();
    return {
      left: Math.min(Math.max(r.left + b.left, b.left), Math.max(b.left, b.right - 300)),
      bottom: Math.min(Math.max(r.bottom + b.top, b.top), Math.max(b.top, b.bottom - 200)),
    };
  }
  // A framed plan is the reader's page: the controls sit over the frame's box,
  // as they sit over the window on ox's own page, so the host's chrome around
  // the frame stays clear. host.css reads these properties under .rev-framed.
  function place() {
    if (!opts.frame || destroyed) return;
    const r = opts.frame.getBoundingClientRect();
    const top = Math.max(r.top, 0), bottom = Math.min(r.bottom, window.innerHeight);
    const v: Record<string, number> = {
      top, left: Math.max(r.left, 0), right: Math.max(window.innerWidth - r.right, 0),
      bottom: Math.max(window.innerHeight - bottom, 0), width: Math.max(r.width, 0), height: Math.max(bottom - top, 0),
    };
    Object.keys(v).forEach((k) => body.style.setProperty('--rev-' + k, v[k] + 'px'));
    if (presence) presence.reposition();
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
    paint();
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
    const item = c ? { anchor: a, section: c.section, label: c.label, quote: c.quote, region: regionOf(a) } : { anchor: a, region: regionOf(a) };
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
    if (r.state === 'owned') Object.keys(e.stamps).forEach((a) => { if (DRAWING.test(a)) ownedDrawings.add(a); });
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
    const json = JSON.stringify(Object.assign({ id: store.exportId(items), slug, reviewer: who() }, roundItems(items)), null, 2);
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

  if (opts.frame) {
    body.classList.add('rev-framed');
    place();
    window.addEventListener('resize', place, { signal });
    // a host's own scroller moves the frame without a window scroll
    document.addEventListener('scroll', place, { capture: true, passive: true, signal });
  }
  const resized = opts.frame && typeof ResizeObserver !== 'undefined' ? new ResizeObserver(place) : null;
  if (resized && opts.frame) resized.observe(opts.frame);

  // controls
  const bar = document.createElement('div');
  bar.className = 'rev-bar';
  bar.innerHTML = (live ? '<span class="rev-conn"></span>' : '') +
    (opts.presence && revision ? '<span class="rev-here" hidden></span>' : '') +
    '<button class="rev-toggle" title="Toggle review mode">Review</button>' +
    (revision ? '<button class="rev-draw" aria-pressed="false" title="Drag to draw an oval around part of the plan to comment on it">Draw</button>' : '') +
    '<span class="rev-count"></span>' +
    (live && !opts.reviewer ? '<button class="rev-who" title="Set the name teammates see on your comments"></button>' : '') +
    '<button class="rev-submit" title="Send feedback to the agent">' + (live ? 'Submit' : 'Export') + '</button>' +
    (opts.approve ? '<button class="rev-approve" title="Approve and close the loop">Approve</button>' : '');
  body.appendChild(bar);
  let drag: { x: number; y: number } | null = null;
  bar.addEventListener('pointerdown', (e) => {
    if (drag || !e.isPrimary || e.button !== 0 || (e.target as Element).closest('button')) return;
    const r = bar.getBoundingClientRect();
    drag = { x: e.clientX - r.left, y: e.clientY - r.top };
    bar.setPointerCapture(e.pointerId);
    bar.classList.add('rev-dragging');
    if (hintBubble) { hintBubble.remove(); hintBubble = null; }
    e.preventDefault();
  }, { signal });
  bar.addEventListener('pointermove', (e) => {
    if (!drag || !e.isPrimary) return;
    const b = box();
    bar.style.left = Math.max(0, b.left, Math.min(e.clientX - drag.x, Math.min(b.right, window.innerWidth) - bar.offsetWidth)) + 'px';
    bar.style.top = Math.max(0, b.top, Math.min(e.clientY - drag.y, Math.min(b.bottom, window.innerHeight) - bar.offsetHeight)) + 'px';
    bar.style.bottom = 'auto';
  }, { signal });
  bar.addEventListener('lostpointercapture', () => {
    drag = null;
    bar.classList.remove('rev-dragging');
  }, { signal });
  const countEl = bar.querySelector('.rev-count') as HTMLElement;
  connEl = bar.querySelector('.rev-conn');
  paintConn();
  if (opts.presence && revision) presence = startPresence(port, opts.presence, revision, bar.querySelector('.rev-here') as HTMLElement, opts.frame);
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
  const drawEl = bar.querySelector('.rev-draw') as HTMLElement | null;
  let hintBubble: HTMLElement | null = null;
  // The Draw tool: while selected, a stroke on the plan draws instead of
  // picking. It is part of review mode, so selecting it enters review mode,
  // and leaving review mode puts it down.
  let drawTaught = false; // the tool's toast shows once a page
  function setTool(next: boolean) {
    if (next && !on) setReview(true, true);
    tool = next;
    if (drawEl) { drawEl.classList.toggle('on', tool); drawEl.setAttribute('aria-pressed', String(tool)); }
    sendMode();
    if (tool && !drawTaught) { drawTaught = true; toast('Drag to draw an oval. Drag its outline to move it, or its handles to resize it before submitting. Esc puts the pen down.'); }
  }
  function setReview(next: boolean, silent?: boolean) {
    on = next; toggleEl.classList.toggle('on', on);
    body.classList.toggle('rev-on', on); // the controls' own look; a framed document marks its own body
    if (!on && tool) { tool = false; if (drawEl) { drawEl.classList.remove('on'); drawEl.setAttribute('aria-pressed', 'false'); } }
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
  if (drawEl) drawEl.onclick = () => { setTool(!tool); };
  submitEl.onclick = submit;
  if (opts.approve) (bar.querySelector('.rev-approve') as HTMLElement).onclick = approve;
  // Esc peels one layer at a time — View original, then an open note, then
  // the Draw tool, then the mode — and fires while typing in the note too. A
  // stroke in progress takes it first, in the document tools.
  function escape(): boolean {
    if (original) { closeOriginal(); return true; }
    if (pop) { closePop(); return true; }
    if (tool) { setTool(false); return true; }
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
    if (!opts.reviewing && !localStorage.getItem('ox-plan-rev-seen')) {
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
        if (presence) presence.ready();
        if (started) return;
        started = true;
        // A live reload — the agent addressed an item, or the server came back — must
        // not drop a reviewer mid-review back to reading mode. Restored silently: the
        // reviewer did not just enter, so no entry toast.
        try { if (!on && sessionStorage.getItem(ON_KEY)) setReview(true, true); } catch (e) { /* starts in reading mode */ }
        break;
      case 'placed':
        onPlaced(m.items);
        break;
      case 'target':
        // The document picks only in review mode, so any other target came
        // from the plan's own scripts, as does one replacing a note in progress.
        if (!on || held()) break;
        openPop({ a: m.target.anchor, section: m.target.section, label: m.target.label, quote: m.target.quote }, toHost(m.target.rect));
        break;
      case 'capture': {
        // Only the reviewer's stroke in the Draw tool makes a capture; any
        // other came from the plan's own scripts.
        if (!on || !tool) break;
        const anchor = m.anchor;
        if (anchor) {
          // A document may propose geometry for an existing draft, never
          // rewrite a submitted drawing or mint its own identity.
          if (committed[anchor]?.length || ownedDrawings.has(anchor) || store.entries().some((e) => e.stamps[anchor] !== undefined)) break;
          const old = pending && 'q-' + pending.id === anchor ? pending : ds[anchor]?.region;
          if (!old) break;
          const r: Region = { ...m.capture, id: old.id, plan_revision: revision, created_at: old.created_at };
          if (checkRegion(r)) break;
          const label = drawingLabel(r), section = r.targets[0].section;
          if (pending && pending.id === r.id) pending = r;
          else if (!store.save({ ...ds[anchor], region: r, label, section })) toast('This browser could not save the updated drawing. It is kept in this tab only.');
          if (pop && popAbout?.a === anchor) {
            popAbout.label = label;
            popAbout.section = section;
            const about = pop.querySelector('.rev-about');
            if (about) about.textContent = label;
            placePop(toHost(m.rect));
          }
          paint();
          break;
        }
        if (held()) { toast('Save or delete the open note, then draw again.'); break; }
        // what the document proposed, with the identity, revision and time
        // only this host can give it
        const r: Region = { ...m.capture, id: newId(), plan_revision: revision, created_at: new Date().toISOString() };
        const why = checkRegion(r);
        if (why) { toast('That drawing cannot be kept: its ' + why + ' is out of bounds.'); break; }
        closePop(true);
        pending = r;
        paint();
        openPop({ a: 'q-' + r.id, section: r.targets[0].section, label: drawingLabel(r) }, toHost(m.rect), true);
        break;
      }
      case 'refused':
        if (m.reason === 'cancelled') break;
        if (!held() && !pending) closePop();
        if (m.reason === 'repeated') toast('“' + clip(m.quote, 40) + '” appears ' + m.count + ' times in this section — highlight a longer phrase so the comment points at one place.');
        else toast(DRAW_REFUSED[m.reason] || 'Highlight text within one section to comment on it.');
        break;
      case 'dismiss':
        if ((on || pop) && !held()) closePop();
        break;
      case 'key':
        if (held()) break;
        if (m.key === 'Escape') escape();
        else setReview(!on);
        break;
      case 'pointer':
        if (presence) presence.pointer(m.at);
        break;
      case 'located':
        if (presence) presence.located(m.points);
        break;
    }
  });

  restoreSync();
  if (opts.reviewing) {
    // the entry toast teaches the mode once; after that it is noise
    let seen = true;
    try { seen = !!localStorage.getItem('ox-plan-rev-seen'); } catch (e) { /* no storage: quiet */ }
    setReview(true, seen);
  }
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
    setReviewing(next) { if (!destroyed && on !== next) setReview(next); },
    whenIdle(fn) { waiter = fn; idle(); },
    destroy() {
      if (destroyed) return;
      waiter = null;
      closePop();
      destroyed = true;
      off.abort();
      clearTimeout(repaint);
      if (presence) presence.leave();
      port.listen(() => {});
      port.send({ type: 'mode', on: false, open: false, draw: false });
      port.send({ type: 'paint', items: [], drawings: [] });
      if (resized) resized.disconnect();
      [bar, rail, orphanBar, syncEl, toastEl, offlineBar, hintBubble, original].forEach((el) => { if (el) el.remove(); });
      body.classList.remove('rev-on', 'rev-offline', 'rev-framed');
      ['top', 'left', 'right', 'bottom', 'width', 'height'].forEach((k) => body.style.removeProperty('--rev-' + k));
    },
  };
}
