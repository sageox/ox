// The ox CLI's review page: host controls and document tools in the plan's own
// window, talking through local ports. Built into internal/plan/assets/review.js.

import { startDocumentTools } from './document-tools.js';
import { mountHostControls, type CommittedItem } from './host-controls.js';
import { localPorts } from './ports.js';

(function () {
  // A host that frames a stored plan in an opaque origin denies storage, and
  // drafts cannot live there: that host injects the document tools on their
  // own (frame.ts), so this layer stays out of its way.
  try {
    if (!window.localStorage) return;
  } catch (e) {
    return;
  }
  const body = document.body;
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
  const ports = localPorts();
  mountHostControls(ports.host, {
    slug: (body.getAttribute('data-slug') || document.title || 'plan').trim(),
    endpoint: body.getAttribute('data-review-endpoint') || '',
    token: body.getAttribute('data-review-token') || '',
    committed,
    problems: Number(body.getAttribute('data-review-problems')) || 0,
  });
  startDocumentTools(ports.document);
})();
