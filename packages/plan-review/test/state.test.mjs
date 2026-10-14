// The host's draft state and sync wording. Failure prevented: two tabs on one
// plan overwrite each other's unsent marks, a sent or deleted mark comes back
// from a stale tab, a resend mints a new round id (a duplicate round), or the
// page claims "synced" when the server only saved locally. The real-Chrome
// tests (cmd/ox, build tag `browser`) cover the same paths end to end.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { load } from './load.mjs';

const { mergeState, ackState, pruneTombs, roundAcked, syncMessage, newRoundId } = await load('state.ts');

test('tabs merge per anchor: newest write wins, a tombstone beats an older mark', () => {
  const a = { marks: { h1: { anchor: 'h1', note: 'tab A', updated_at: 10 } }, tombs: {} };
  const b = { marks: { h2: { anchor: 'h2', note: 'tab B', updated_at: 20 } }, tombs: {} };
  const m = mergeState(a, b);
  assert.ok(m.marks.h1 && m.marks.h2, 'both tabs kept');
  const older = { marks: { h1: { note: 'old', updated_at: 10 } }, tombs: {} };
  const newer = { marks: { h1: { note: 'new', updated_at: 30 } }, tombs: {} };
  assert.equal(mergeState(older, newer).marks.h1.note, 'new');
  assert.equal(mergeState(newer, older).marks.h1.note, 'new');
  const deleted = { marks: {}, tombs: { h1: 15 } };
  assert.ok(!mergeState(deleted, older).marks.h1, 'tomb beats a stale copy');
  assert.equal(mergeState(deleted, older).tombs.h1, 15);
  assert.ok(!mergeState({ marks: {}, tombs: { h1: 10 } }, older).marks.h1, 'tomb beats an equal stamp');
  assert.equal(mergeState(deleted, newer).marks.h1.note, 'new', 'a later edit beats the tomb');
  assert.ok(mergeState({ marks: { h9: { note: 'x' } }, tombs: {} }, { marks: {}, tombs: {} }).marks.h9, 'unstamped legacy mark kept');
});

test('an ack clears what was sent and keeps what was edited after Submit', () => {
  const s = { marks: { h1: { updated_at: 10 }, h2: { updated_at: 50 }, h3: { updated_at: 5 } }, tombs: {} };
  const box = { id: 'r1', items: [{ anchor: 'h1' }, { anchor: 'h2' }, { anchor: 'h4' }], stamps: { h1: 10, h2: 40, h4: 7 } };
  const acked = ackState(s, box);
  assert.ok(!acked.marks.h1 && acked.tombs.h1 === 10, 'sent mark cleared');
  assert.ok(acked.marks.h2 && !acked.tombs.h2, 'post-submit edit kept');
  assert.ok(acked.marks.h3, 'unsent mark untouched');
  assert.equal(acked.tombs.h4, 7, 'anchor sent from another tab tombed');
  assert.ok(!mergeState(acked, { marks: { h1: { updated_at: 10 } }, tombs: {} }).marks.h1, 'a stale tab cannot resurrect it');
  assert.ok(s.marks.h1, 'input not mutated');
  const legacy = ackState({ marks: { h5: { note: 'pre-stamp' } }, tombs: {} }, { items: [{ anchor: 'h5' }], stamps: {} });
  assert.ok(!legacy.marks.h5 && legacy.tombs.h5 === 1, 'legacy mark cleared and tombed');
  assert.deepEqual(pruneTombs({ a: 1, b: 990 }, 1000, 100), { b: 990 });
});

test('round ids fit the server pattern and never repeat', () => {
  const ids = new Set();
  for (let i = 0; i < 50; i++) {
    const id = newRoundId();
    assert.match(id, /^[A-Za-z0-9_-]{8,64}$/);
    ids.add(id);
  }
  assert.equal(ids.size, 50);
  // no randomUUID (older Safari, http:// origins other than localhost): 32 hex from getRandomValues
  const real = Object.getOwnPropertyDescriptor(globalThis, 'crypto');
  Object.defineProperty(globalThis, 'crypto', { configurable: true, value: { getRandomValues: (b) => b.map((_, i) => i * 7) } });
  try {
    assert.match(newRoundId(), /^[0-9a-f]{32}$/);
  } finally {
    Object.defineProperty(globalThis, 'crypto', real);
  }
});

test('acks match the round, or an older server that names none', () => {
  assert.ok(roundAcked({ id: 'r1' }, { round_id: 'r1' }));
  assert.ok(roundAcked({ id: 'r1' }, { notified: true }));
  assert.ok(!roundAcked({ id: 'r1' }, { round_id: 'r2' }));
});

test('sync wording tells the truth', () => {
  assert.equal(syncMessage('Sent', { saved: true, pushed: true, round_id: 'r1' }).text, 'Sent · synced to your team');
  const local = syncMessage('Sent', { saved: true, committed: true, pushed: false });
  assert.equal(local.kind, 'warn');
  assert.equal(local.text, 'Saved on the author’s machine · not yet synced (will retry)');
  const old = syncMessage('Sent', { notified: true });
  assert.ok(old.kind === 'ok' && !old.text.includes('synced'), 'an older server never reads as synced');
  assert.equal(syncMessage('Sent', { saved: true, pushed: true, duplicate: true, notified: false }).text, 'Sent · synced to your team');
  assert.ok(syncMessage('Sent', { saved: true, pushed: true, notified: false }).text.includes('not notified'));
  assert.equal(syncMessage('Sent', { saved: false }).kind, 'err');
});
