// The anchors every Ledger already holds, as pkg/planreview/testdata/anchors.json
// records them: the package must compute the same ones, or existing marks are
// orphaned. The vectors' page section is checked in a browser
// (TestBrowser_DocumentToolsInOpaqueFrame in cmd/ox).
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import { load } from './load.mjs';

const vectors = JSON.parse(readFileSync(new URL('../../../pkg/planreview/testdata/anchors.json', import.meta.url), 'utf8'));
const { contentAnchor, labelOf } = await load('anchor.ts');

test('element anchors match the shared vectors', () => {
  for (const v of vectors.anchors) assert.equal(contentAnchor(v.heading, v.text), v.anchor, JSON.stringify(v));
});

test('labels match the shared vectors', () => {
  for (const v of vectors.labels) assert.equal(labelOf(v.text), v.label);
});
