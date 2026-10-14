// A framed plan document is untrusted: what it posts reaches the host only
// through parseDocumentMessage. Failure prevented: a document smuggles fields
// the host would act on (a reviewer, a submission), a stale frame's messages
// land in the current one, or malformed or oversized data reaches the
// composer and rail. parseHostMessage holds the document side to the same shape.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { load } from './load.mjs';

const { PROTOCOL, envelope, parseDocumentMessage, parseHostMessage } = await load('protocol.ts');
const { connectFrame } = await load('ports.ts');
const I = 'inst-0001';
const doc = (m) => envelope(m, I);
const element = { anchor: 'h1234abcd', section: 'Risks', label: 'Retry storms', rect: { left: 10, bottom: 20 } };

test('the document messages a host acts on parse to their known fields only', () => {
  assert.deepEqual(parseDocumentMessage(doc({ type: 'target', target: { ...element, reviewer: 'mallory' }, submit: true }), I), { type: 'target', target: element });
  const quote = { ...element, anchor: 'q00ff00ff', quote: 'retry storms' };
  assert.deepEqual(parseDocumentMessage(doc({ type: 'target', target: quote }), I), { type: 'target', target: quote });
  assert.deepEqual(parseDocumentMessage(doc({ type: 'placed', items: [{ anchor: 'h1234abcd', section: '', label: 'x', extra: 1 }] }), I), { type: 'placed', items: [{ anchor: 'h1234abcd', section: '', label: 'x' }] });
  assert.deepEqual(parseDocumentMessage(doc({ type: 'refused', reason: 'repeated', quote: 'retry', count: 3 }), I), { type: 'refused', reason: 'repeated', quote: 'retry', count: 3 });
  assert.deepEqual(parseDocumentMessage(doc({ type: 'key', key: 'Escape' }), I), { type: 'key', key: 'Escape' });
  assert.deepEqual(parseDocumentMessage(doc({ type: 'ready' }), I), { type: 'ready' });
});

test('anything else from a document is dropped', () => {
  const t = (target) => ({ type: 'target', target: { ...element, ...target } });
  const cases = {
    'another instance': envelope({ type: 'ready' }, 'inst-0002'),
    'another version': { ...doc({ type: 'ready' }), v: PROTOCOL + 1 },
    'not ours': { ...doc({ type: 'ready' }), ox: 'draw' },
    'an array': [doc({ type: 'ready' })],
    'a host verb': doc({ type: 'approve' }),
    'a short anchor': doc(t({ anchor: 'h123' })),
    'a drawing anchor': doc(t({ anchor: 'q-r3-mermaid' })),
    'a highlight without words': doc(t({ anchor: 'q00ff00ff' })),
    'an element with words': doc(t({ quote: 'retry' })),
    'a rect off the page': doc(t({ rect: { left: Infinity, bottom: 0 } })),
    'an oversized label': doc(t({ label: 'x'.repeat(65537) })),
    'too many placed': doc({ type: 'placed', items: Array(10001).fill({ anchor: 'h1234abcd', section: '', label: '' }) }),
    'one occurrence called repeated': doc({ type: 'refused', reason: 'repeated', quote: 'retry', count: 1 }),
    'another key': doc({ type: 'key', key: 'Enter' }),
  };
  for (const [name, data] of Object.entries(cases)) assert.equal(parseDocumentMessage(data, I), null, name);
});

// A plan's own script reads what the host posts and can carry it off by
// navigating its frame, so no reviewer name or note may reach the document.
test('host messages carry no names or notes, and only anchors the document can place', () => {
  const item = { anchor: 'q00ff00ff', kind: 'committed', status: 'flag', state: 'open', glyph: '⚑', tint: 'amber' };
  assert.deepEqual(parseHostMessage(doc({ type: 'paint', items: [{ ...item, title: 'Devon · open — rotate the key', html: '<b>' }] }), I), { type: 'paint', items: [item] });
  assert.deepEqual(parseHostMessage(doc({ type: 'focus', anchor: null }), I), { type: 'focus', anchor: null });
  for (const bad of [
    { type: 'mode', on: 'yes', open: false },
    { type: 'paint', items: [{ ...item, status: 'delete' }] },
    ...['__proto__', 'constructor', 'q-r3-mermaid', '../x'].map((anchor) => ({ type: 'paint', items: [{ ...item, anchor }] })),
    { type: 'focus', anchor: '__proto__' },
    { type: 'reveal', anchor: 'h1234abcd', nth: -1 },
  ]) assert.equal(parseHostMessage(doc(bad), I), null, JSON.stringify(bad));
});

// The frame's own scripts read the raw message, so stripping unknown fields on
// arrival is too late. Failure prevented: a host that spreads a stored review
// item into a paint posts its note and reviewer to the plan.
test('connectFrame posts only the protocol\'s fields, on the port the document handed over', () => {
  const listeners = [];
  globalThis.window = { addEventListener: (_, fn) => listeners.push(fn), removeEventListener: () => {} };
  try {
    const frame = { contentWindow: {} };
    const posted = [];
    const port = { postMessage: (m) => posted.push(m), close: () => {} };
    const host = connectFrame(frame, I);
    const heard = [];
    host.listen((m) => heard.push(m));
    listeners[0]({ source: frame.contentWindow, origin: 'null', data: doc({ type: 'ready' }), ports: [port] });
    assert.deepEqual(heard, [{ type: 'ready' }]);
    const item = { anchor: 'h1234abcd', kind: 'committed', status: 'flag', state: 'open', glyph: '⚑', tint: 'amber' };
    host.send({ type: 'paint', items: [{ ...item, note: 'rotate the key', reviewer: 'Devon' }] });
    assert.deepEqual(posted, [doc({ type: 'paint', items: [item] })]);
  } finally {
    delete globalThis.window;
  }
});
