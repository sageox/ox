// The host controls' drafts and outbox, over a fake Storage. The real-Chrome
// tests (cmd/ox, build tag `browser`) cover the same paths end to end.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { load } from './load.mjs';

const { openStore, deliver, queued, submitBody, acceptBody, migrateLegacy, syncMessage, newId } = await load('state.ts');

// fakeStorage is localStorage over a Map, recording the keys each write
// touches; full(true) makes every write throw, as a full or denied storage does.
function fakeStorage() {
  const map = new Map();
  const writes = [];
  let full = false;
  return {
    map, writes,
    get length() { return map.size; },
    key: (i) => [...map.keys()][i] ?? null,
    getItem: (k) => (map.has(k) ? map.get(k) : null),
    setItem: (k, v) => { if (full) throw new Error('QuotaExceededError'); writes.push(k); map.set(k, String(v)); },
    removeItem: (k) => { writes.push(k); map.delete(k); },
    full: (on) => { full = on; },
  };
}
const mark = (anchor, note) => ({ anchor, section: 'Risks', label: anchor, status: 'request-change', note });
// transport answers each send with the next scripted answer (a receipt, or a
// function of the operation) and records what it was sent
function transport(...answers) {
  const sent = [];
  return { sent, send: async (op) => { sent.push(op); const a = answers.shift() || { state: 'failed', message: 'no answer scripted' }; return typeof a === 'function' ? a(op) : a; } };
}
const owned = (op) => ({ state: 'owned', id: op.id, synced: true });
const run = (store, t) => deliver(store, t, () => {}, () => false);

// Failure prevented: two tabs on one plan each rewrite a shared map of drafts
// or a single outbox, so whichever writes last drops the other tab's marks or
// its submission.
test('a tab writes only the keys of the draft or operation it saved', () => {
  const s = fakeStorage();
  const a = openStore(s, 'acct', 'pln_1'), b = openStore(s, 'acct', 'pln_1');
  a.save(mark('h00000001', 'from tab A'));
  a.freeze('submit', 'op-a0000001', '{}', {});
  s.writes.length = 0;
  b.save(mark('h00000002', 'from tab B'));
  b.freeze('submit', 'op-b0000002', '{}', {});
  assert.deepEqual(s.writes, ['ox-review:acct:pln_1:draft:h00000002', 'ox-review:acct:pln_1:op:op-b0000002']);
  assert.deepEqual(a.drafts().map((m) => m.note), ['from tab A', 'from tab B']);
  assert.deepEqual(a.entries().map((e) => e.id), ['op-a0000001', 'op-b0000002']);
});

// Failure prevented: storage that is full or denied silently drops a draft,
// or a submission is sent without being stored first, so a reload resends
// nothing and a lost answer loses the feedback.
test('nothing storage refused is sent, and a draft it refused stays in this tab, marked unsaved', async () => {
  const s = fakeStorage(), st = openStore(s, 'acct', 'pln_1');
  s.full(true);
  assert.equal(st.save(mark('h00000001', 'kept in this tab')), false);
  assert.ok(st.unsaved('h00000001'));
  assert.deepEqual(st.drafts().map((m) => m.note), ['kept in this tab']);
  assert.equal(st.freeze('submit', 'op-00000001', '{}', {}), null);
  const t = transport();
  await run(st, t);
  assert.equal(t.sent.length, 0);
  s.full(false);
  assert.ok(st.save(mark('h00000001', 'saved now')));
  assert.ok(!st.unsaved('h00000001'));
});

// Failure prevented: the answer to an older submission clears a mark the
// reviewer edited after Submit, in this tab or another, even within the same
// millisecond, or an answer naming another operation clears this one.
test('a receipt clears only its own operation, and never a draft edited after it was frozen', async () => {
  const s = fakeStorage(), st = openStore(s, 'acct', 'pln_1');
  st.save(mark('h00000001', 'first'));
  st.save(mark('h00000002', 'second'));
  const ds = st.drafts(), stamps = Object.fromEntries(ds.map((m) => [m.anchor, m.at]));
  st.freeze('submit', 'op-00000001', submitBody('op-00000001', 'Devon', ds), stamps);
  assert.ok(ds.every((m) => queued(st.entries(), m)));

  await run(st, transport({ state: 'owned', id: 'someone-elses', synced: true }));
  assert.equal(st.entries().length, 1, 'an answer for another operation acknowledges nothing');

  const edit = async (op) => { openStore(s, 'acct', 'pln_1').save(mark('h00000002', 'edited during send')); return owned(op); };
  await run(st, transport(edit));
  assert.deepEqual(st.entries(), []);
  assert.deepEqual(st.drafts().map((m) => m.note), ['edited during send']);
  assert.ok(!queued(st.entries(), st.drafts()[0]), 'the edit waits for the next Submit');
});

// Failure prevented: a later operation is stored before an earlier one (so a
// stale note reads as the latest), a deterministic refusal blocks the outbox
// forever, or a resend after a lost answer sends different bytes and the host
// sees a conflicting operation.
test('operations go oldest first with their frozen bytes, until one fails or finds the host offline', async () => {
  const st = openStore(fakeStorage(), 'acct', 'pln_1');
  const a = st.freeze('submit', 'op-00000001', '{"id":"op-00000001"}', {});
  const b = st.freeze('accept', 'op-00000002', '{"id":"op-00000002"}', {});
  const c = st.freeze('reopen', 'op-00000003', '{"id":"op-00000003"}', {});
  // a is stored, but its answer is lost
  const lost = transport({ state: 'offline' });
  await run(st, lost);
  assert.deepEqual(lost.sent.map((op) => op.id), [a.id], 'nothing after an operation that went unanswered');

  const back = transport((op) => ({ ...owned(op), duplicate: true }), { state: 'refused', message: 'HTTP 400' }, { state: 'failed', message: 'HTTP 500' });
  await run(st, back);
  assert.deepEqual(back.sent.map((op) => [op.id, op.kind, op.body]), [[a.id, 'round', a.body], [b.id, 'resolution', b.body], [c.id, 'round', c.body]]);
  assert.equal(back.sent[0].body, lost.sent[0].body);
  assert.deepEqual(st.entries().map((e) => e.id), [c.id], 'the owned and the refused are cleared; the failed waits');
});

// Failure prevented: upgrading ox strands a reviewer's unsent marks, or turns
// a submission waiting for its answer into a second round.
test('what pages before 0.3.0 kept under a slug carries over, and a waiting submission resends byte for byte', async () => {
  const s = fakeStorage();
  const box = { id: 'legacy-round-1', items: [{ anchor: 'h00000001', section: 'Risks', label: 'Retry', status: 'flag', note: 'old' }], reviewer: 'Quinn', created_at: '2026-10-01T00:00:00.000Z', stamps: { h00000001: 5 } };
  s.setItem('ox-plan-fb:my-plan', JSON.stringify({ h00000001: { ...box.items[0], updated_at: 5 }, h00000002: { anchor: 'h00000002', status: 'comment', note: 'unsent', updated_at: 9 } }));
  s.setItem('ox-plan-fb:my-plan:outbox', JSON.stringify(box));
  s.setItem('ox-plan-fb:my-plan:tombs', '{"h00000003":4}');
  const st = openStore(s, 'local', 'pln_1');
  s.full(true);
  migrateLegacy(s, 'my-plan', st);
  assert.equal(s.map.size, 3, 'a migration storage refuses keeps the old keys');
  s.full(false);
  migrateLegacy(s, 'my-plan', st);
  assert.deepEqual([...s.map.keys()].filter((k) => k.startsWith('ox-plan-fb:')), []);
  assert.deepEqual(st.drafts().map((m) => m.note), ['old', 'unsent']);
  const t = transport(owned);
  await run(st, t);
  assert.equal(t.sent[0].body, JSON.stringify({ id: box.id, slug: 'my-plan', reviewer: box.reviewer, items: box.items }));
  assert.deepEqual(st.drafts().map((m) => m.note), ['unsent'], 'its answer clears the mark it carried');
});

// Failure prevented: a signed-in host's request names a reviewer it should
// stamp itself, or ox's local review sends one with no reviewer at all.
test('requests name the reviewer only for a host without sign-in', () => {
  const ds = [{ ...mark('q00ff00ff', 'n'), quote: 'retry path', at: 1 }];
  assert.deepEqual(JSON.parse(submitBody('op-00000001', 'Devon', ds)), { id: 'op-00000001', reviewer: 'Devon', items: [{ anchor: 'q00ff00ff', section: 'Risks', label: 'q00ff00ff', quote: 'retry path', status: 'request-change', note: 'n' }] });
  assert.ok(!('reviewer' in JSON.parse(submitBody('op-00000001', '', ds))));
  assert.deepEqual(JSON.parse(acceptBody('op-00000002', 'Devon', 'h00000001')), { id: 'op-00000002', reviewer: 'Devon', anchor: 'h00000001', state: 'verified', note: 'accepted by reviewer' });
});

test('operation ids fit the hosts\' pattern and never repeat', () => {
  const ids = new Set();
  for (let i = 0; i < 50; i++) {
    const id = newId();
    assert.match(id, /^[A-Za-z0-9_-]{8,64}$/);
    ids.add(id);
  }
  assert.equal(ids.size, 50);
  // no randomUUID (older Safari, http:// origins other than localhost): 32 hex from getRandomValues
  const real = Object.getOwnPropertyDescriptor(globalThis, 'crypto');
  Object.defineProperty(globalThis, 'crypto', { configurable: true, value: { getRandomValues: (b) => b.map((_, i) => i * 7) } });
  try {
    assert.match(newId(), /^[0-9a-f]{32}$/);
  } finally {
    Object.defineProperty(globalThis, 'crypto', real);
  }
});

// Failure prevented: the page says "synced" when the feedback only sits on
// the author's machine.
test('sync wording tells the truth', () => {
  assert.deepEqual(syncMessage('Sent', { synced: true }), { kind: 'ok', text: 'Sent · synced to your team' });
  assert.deepEqual(syncMessage('Sent', { synced: false }), { kind: 'warn', text: 'Saved on the author’s machine · not yet synced (will retry)' });
  assert.ok(syncMessage('Sent', { synced: true, warning: 'tell them directly' }).text.endsWith(' · tell them directly'));
});
