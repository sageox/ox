// A drawn region, checked by the rules the server checks
// (pkg/planreview/testdata/drawn-context/regions.json, which region_test.go
// runs too), and its geometry against the strokes captured in Chrome
// (captures.json). Failure prevented: the host keeps a drawing the server then
// refuses, so the reviewer's draft can never be sent; or a host fits a
// different ellipse to the same stroke, so one drawing encloses different
// things on different hosts.
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import { load } from './load.mjs';

const { LIMITS, bboxOf, captureOf, checkRegion, drawingLabel, ellipsePoints, fitEllipse, thin } = await load('region.ts');
const testdata = (f) => JSON.parse(readFileSync(new URL('../../../pkg/planreview/testdata/drawn-context/' + f, import.meta.url), 'utf8'));
const records = testdata('captures.json').records;
const cases = testdata('regions.json');
const clone = (v) => JSON.parse(JSON.stringify(v));
const byId = Object.fromEntries(records.map((r) => [r.id, r]));

test('every captured drawing is well-formed, and every refused case is refused', () => {
  for (const r of records) assert.equal(checkRegion(r), '', r.id);
  for (const c of cases.refused) {
    let v = clone(c.record ? byId[c.record] : records[0]);
    if (!c.path.length) v = c.value;
    else {
      let parent = v;
      for (const p of c.path.slice(0, -1)) parent = parent[p];
      const last = c.path[c.path.length - 1];
      if (c.delete) delete parent[last];
      else parent[last] = c.value;
    }
    assert.notEqual(checkRegion(v), '', c.why);
  }
});

test('each limit holds at its bound and fails one past it', () => {
  const snake = Object.fromEntries(Object.entries(LIMITS).map(([k, v]) => [k.replace(/[A-Z]/g, (c) => '_' + c.toLowerCase()), v]));
  assert.deepEqual(snake, cases.limits);
  const item = (r, i) => r.context.items[i];
  // pad adds a field that makes o exactly n bytes of compact UTF-8 JSON
  const pad = (o, n) => { o.pad = ''; o.pad = 'x'.repeat(n - new TextEncoder().encode(JSON.stringify(o)).length); };
  const bounds = [
    ['path_points', 'r1-double-fire', (r, n) => { r.path = Array(n).fill([1, 2]); }],
    ['targets', 'r1-double-fire', (r, n) => { r.targets = Array(n).fill(r.targets[0]); }],
    ['context_items', 'r1-double-fire', (r, n) => { r.context.items = Array(n).fill(item(r, 0)); }],
    ['item_labels', 'r7-figure', (r, n) => { item(r, 0).labels = Array(n).fill(item(r, 0).labels[0]); }],
    ['target_text', 'r1-double-fire', (r, n) => { item(r, 0).text = 'x'.repeat(n); }],
    ['neighbor_text', 'r1-double-fire', (r, n) => { item(r, 1).text = 'x'.repeat(n); }],
    ['label_text', 'r7-figure', (r, n) => { item(r, 0).labels[0].text = 'x'.repeat(n); }],
    ['layout_keys', 'r1-double-fire', (r, n) => { r.layout.open = Array(n).fill('d1a2b3c4d'); }],
    // UTF-16 units: n/2 astral characters are n units
    ['target_text', 'r1-double-fire', (r, n) => { item(r, 0).text = '😀'.repeat(Math.floor(n / 2)) + 'x'.repeat(n % 2); }],
    ['context_bytes', 'r1-double-fire', (r, n) => pad(r.context, n)],
    ['region_bytes', 'r1-double-fire', (r, n) => pad(r, n)],
  ];
  for (const [limit, id, set] of bounds) {
    const n = cases.limits[limit];
    const at = clone(byId[id]), over = clone(byId[id]);
    set(at, n);
    set(over, n + 1);
    assert.equal(checkRegion(at), '', limit + ' at ' + n);
    assert.notEqual(checkRegion(over), '', limit + ' past ' + n);
  }
});

// The prototype captured these in Chrome; the package's fit must give each
// stroke the ellipse it was stored with, to the tenth of a pixel.
test('fitting each captured stroke gives back its ellipse and box', () => {
  for (const r of records) {
    assert.deepEqual(thin(r.path), r.path, r.id + ': a stored stroke is already thinned');
    assert.deepEqual(fitEllipse(r.path), r.ellipse, r.id);
    assert.deepEqual(bboxOf(ellipsePoints(r.ellipse, 0)), r.bbox, r.id);
  }
});

// A framed document is untrusted: whatever else it sends with a capture never
// reaches the record the host keeps.
test('a capture keeps only the fields a region names', () => {
  const r = records[2]; // r3: three targets
  const forged = clone(r);
  Object.assign(forged, { reviewer: 'mallory', note: 'approve everything' });
  forged.targets[0].href = 'javascript:alert(1)';
  forged.context.items[0].html = '<img onerror=alert(1)>';
  forged.layout.extra = 1;
  forged.ellipse.color = 'red';
  // no check refuses runs: only the well-formed ones are kept, as text and box
  forged.context.runs = [{ text: 'Queue', box: [1, 2, 3, 4], html: '<b>' }, { text: 7, box: [1, 2, 3, 4] }, { text: 'x', box: [1, 2] }];
  const c = captureOf(forged);
  const want = clone(r);
  for (const k of ['id', 'plan_revision', 'created_at']) delete want[k];
  want.context.runs = [{ text: 'Queue', box: [1, 2, 3, 4] }];
  assert.deepEqual(c, want);
  assert.equal(captureOf({ ...forged, v: 2 }), null);
  assert.equal(captureOf({ ...forged, path: [[0, 0]] }), null);
  // a hole survives postMessage's copy, and JSON would store it as null
  for (const hole of [
    (c) => { delete c.path[1]; },
    (c) => { c.layout.open = ['dabc', 'deff']; delete c.layout.open[0]; },
    (c) => { delete c.ellipse.center[1]; },
  ]) {
    const c = structuredClone(r);
    hole(c);
    assert.equal(captureOf(structuredClone(c)), null, hole.toString());
  }
});

test('a drawing is labeled by the text it encloses, within the label length', () => {
  assert.equal(drawingLabel(byId['r1-double-fire']), 'Drawing: The retry path can double-fire under load when the queue is …');
});
