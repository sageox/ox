// The ox CLI's review page: host controls and document tools in the plan's
// own window, talking through local ports. Served by `ox plan review`, the
// page sends to that server and also
//   - live-reloads via SSE as the agent addresses items and re-renders,
//   - shows who else has the plan open, and their pointers, through the
//     same stream: the server joins the plan's presence room as the
//     signed-in user, so the page never holds their credentials,
//   - goes offline when the server is gone and reloads once it is back,
//   - keeps an offline shell (sw.js) so a reload during an outage shows the plan.
// Built into internal/plan/assets/review.js.

import { clip } from './anchor.js';
import { startDocumentTools } from './document-tools.js';
import { mountHostControls, type CommittedItem } from './host-controls.js';
import { localPorts } from './ports.js';
import type { PresenceTransport } from './presence.js';
import { migrateLegacy, newId, openStore, type Approval, type Operation, type Receipt } from './state.js';

// ox's local review has no sign-in: one account per browser profile, and the
// reviewer's name travels in each request.
const ACCOUNT = 'local';

(function () {
  // A host that frames a stored plan in an opaque origin denies storage, and
  // drafts cannot live there: that host injects the document tools on their
  // own (frame.ts), so this layer stays out of its way.
  let storage: Storage;
  try {
    storage = window.localStorage;
    if (!storage) return;
  } catch (e) {
    return;
  }
  const body = document.body;
  const slug = (body.getAttribute('data-slug') || document.title || 'plan').trim();
  // the plan's directory; a page rendered without one, by slug
  const plan = body.getAttribute('data-review-plan') || slug;
  const base = body.getAttribute('data-review-endpoint') || ''; // live server base, else ""
  const token = body.getAttribute('data-review-token') || '';
  // the committed review state the page was rendered with; anything
  // unreadable there paints nothing rather than stopping the review layer
  let committed: CommittedItem[] = [];
  try {
    const el = document.getElementById('ox-review-state');
    const v: unknown = el ? JSON.parse(el.textContent || '[]') : [];
    if (Array.isArray(v)) committed = v.filter((x) => x && typeof x.anchor === 'string');
  } catch (e) {
    committed = [];
  }
  migrateLegacy(storage, slug, openStore(storage, ACCOUNT, plan));

  // While the server is unreachable, poll /healthz. Once it answers — or the
  // live-reload stream reconnects — the page is live again, sends what waits,
  // and reloads for fresh state: stable port + persisted token mean the same
  // origin serves it.
  let probe: ReturnType<typeof setInterval> | null = null;
  function reload() { controls.whenIdle(() => location.reload()); }
  function watch() {
    if (probe) return;
    probe = setInterval(() => {
      fetch(base + '/healthz', { cache: 'no-store' })
        .then((r) => (r.ok ? r.json() : null))
        .then((j) => { if (j && j.app === 'ox-plan-review') back(); })
        .catch(() => {});
    }, 3000);
  }
  function back() {
    if (!probe) return;
    clearInterval(probe);
    probe = null;
    controls.setOffline(false);
    reload();
  }

  // null: the server could not be reached
  function post(path: string, payload: string): Promise<Response | null> {
    return fetch(base + path, { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Review-Token': token }, body: payload }).catch(() => {
      watch();
      return null;
    });
  }
  async function failure(r: Response): Promise<string> {
    const t = (await r.text().catch(() => '')).trim();
    return 'HTTP ' + r.status + (t ? ' — ' + clip(t, 120) : '');
  }
  // The server answers saved:true only once the record and its recovery entry
  // are on disk (publishReviewRecord in cmd/ox): only then does it own the
  // operation.
  async function send(op: Operation): Promise<Receipt> {
    const r = await post(op.kind === 'round' ? '/feedback' : '/accept', op.body);
    if (!r) return { state: 'offline' };
    if (!r.ok) {
      const message = await failure(r);
      // the server refuses these bytes on every attempt: malformed, too
      // large, or the id already stored with other content
      if (r.status === 400 || r.status === 409 || r.status === 413) return { state: 'refused', message };
      return { state: 'failed', message };
    }
    const d = await r.json().catch(() => ({}));
    if (d.saved !== true) return { state: 'failed', message: 'the server did not say it stored this' };
    return {
      state: 'owned',
      id: op.kind === 'round' ? d.round_id : d.resolution_id,
      synced: !!d.pushed,
      // a duplicate is a resend of an operation whose first save already notified (or tried to)
      warning: d.notified === false && !d.duplicate ? 'the plan’s authoring coworker was not notified automatically — tell them directly' : undefined,
    };
  }
  async function approve(): Promise<Approval> {
    const r = await post('/approve', '{}');
    if (!r) return { state: 'offline' };
    if (!r.ok) return { state: 'failed', message: await failure(r) };
    const d = await r.json().catch(() => ({}));
    return { state: 'owned', synced: !!d.pushed };
  }

  // The live-reload stream. A page that names the revision it shows, and
  // itself, is also in the plan's presence room while the stream is open;
  // its pointer goes to /presence under the same name.
  const revision = body.getAttribute('data-review-revision') || '';
  const key = newId();
  let es: EventSource | null = null;
  if (base && window.EventSource) {
    try {
      es = new EventSource(base + '/events?t=' + encodeURIComponent(token) + (revision ? '&rev=' + encodeURIComponent(revision) + '&k=' + encodeURIComponent(key) : ''));
    } catch (e) { /* no live reload; Submit still works */ }
  }
  const stream = es;
  const presence: PresenceTransport | undefined = stream && revision ? {
    join(_shown, on) {
      stream.addEventListener('presence', (e) => {
        try { on(JSON.parse((e as MessageEvent).data)); } catch (err) { /* not an event */ }
      });
      // the review server is gone, and with it the room: nobody is known to
      // be here until a reconnected page hears welcome
      stream.addEventListener('error', () => on({ type: 'lost' }));
      return {
        // a pointer that cannot reach the server is dropped: the stream's
        // own error already says the server is gone
        move(at) {
          return fetch(base + '/presence', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Review-Token': token }, body: JSON.stringify({ key, at }) }).catch(() => {});
        },
        leave() { /* the stream's end leaves the room */ },
      };
    },
  } : undefined;

  const ports = localPorts();
  const controls = mountHostControls(ports.host, {
    account: ACCOUNT,
    plan,
    slug,
    committed,
    problems: Number(body.getAttribute('data-review-problems')) || 0,
    transport: base ? { send } : undefined,
    resolve: true, // any reviewer of a local review may Accept or Reopen
    approve: base ? approve : undefined,
    restartCommand: 'ox plan review ' + slug,
    // the revision ox rendered this page from; a page without one offers no Draw
    revision: revision || undefined,
    presence,
  });
  startDocumentTools(ports.document);
  if (!base) return;

  // live reload as the agent addresses items + re-renders. The server writes
  // a record, and its watcher fires, before a slow commit+push answers: the
  // reload waits for that answer, and for a note being written.
  if (es) {
    es.onopen = back;
    es.onerror = () => { controls.setOffline(true); watch(); };
    es.onmessage = reload;
  }

  // offline shell: cache the page so a reload while the server is down still
  // shows the plan (this layer then flips to offline mode instead of the
  // browser's connection-error page).
  if ('serviceWorker' in navigator) {
    try { navigator.serviceWorker.register('/sw.js').catch(() => {}); } catch (e) { /* no offline shell */ }
  }
})();
