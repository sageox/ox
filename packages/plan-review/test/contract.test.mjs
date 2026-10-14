// Source pins for review behaviors whose real proof is the real-Chrome tests in
// cmd/ox (build tag `browser`), which CI does not run; these are the guard CI
// sees. Each string names the line that carries a behavior a reviewer relies on.
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import { load } from './load.mjs';

const src = (f) => readFileSync(new URL('../src/' + f, import.meta.url), 'utf8');
function pins(file, want) {
  const s = src(file);
  for (const w of want) assert.ok(s.includes(w), `${file} lost ${JSON.stringify(w)}`);
}

// Failure prevented: re-clicking a marked item computes a different anchor and
// creates an unreachable duplicate mark instead of editing the existing one.
test('an anchor ignores the glyph painted into its element', () => {
  pins('anchor.ts', ["clone.querySelectorAll('.rev-glyph, style, script')", 'contentAnchor(headingOf(el), anchorText(el))']);
});

// Failure prevented: a reviewer keeps marking up a dead page believing their
// feedback is reaching the agent (TestBrowser_UnsentMarkSurvivesReconnect).
test('a dead server is detected, said plainly, and recovered from', () => {
  pins('host-controls.ts', [
    'es.onerror', // SSE drop detection
    'es.onopen', // recovery detection
    'setOffline(true)', // a failed POST flips the page offline
    'NOT being saved', // the banner says it plainly
    'rev-offline-bar', // sticky banner element
    "'/healthz'", // recovery probe endpoint
    "'ox plan review ' + slug", // copyable restart command
    'serviceWorker', // offline shell registration
    'unsent mark(s) restored', // restored-marks notice after reconnect
    "if (offline) { offlineNotice(); if (fail) fail('offline', true); return; }", // sends refused while offline
  ]);
});

// Failure prevented: a reviewer clicks Review, sees only a green button, and
// has no visible way back — or every agent fix drops them out of review mode
// (TestBrowser_ReviewModeExitIsVisibleAndEscapable, …SurvivesLiveReload,
// TestBrowser_ReviewKeysWorkOnAuthoredPlan).
test('review mode announces itself, keeps its exit in view, and survives a reload', () => {
  pins('host-controls.ts', [
    "on ? 'Exit review' : 'Review'", // the button becomes the exit
    'Esc or Exit review to leave', // the entry toast names both exits
    'if (e.defaultPrevented) return;', // a key the page already handled is left alone
    'if (pop) { closePop(); e.preventDefault(); }', // Esc: the note first…
    'else if (on) { setReview(false); e.preventDefault(); }', // …then the mode, marked handled
    "if (e.key === 'r' && !typing(e)", // r toggles, not from a text field
    "sessionStorage.setItem(ON_KEY, '1')", // kept per tab, not per browser
    'if (sessionStorage.getItem(ON_KEY)) setReview(true, true);', // restored silently after a reload
    'if (t.closest && t.closest(CHROME) && (on || pop)) closePop();', // a click on the controls dismisses a note
    "case 'dismiss':\n        if (on || pop) closePop();", // so does a click on nothing markable
    'if (toastEl === el) toastEl = null;', // a toast timer removes only its own toast
  ]);
  pins('document-tools.ts', [
    "if (el0.closest && el0.closest(CHROME + ', .rev-pop')) return;", // the controls are never a target
    "if (!el) { port.send({ type: 'dismiss' }); return; }", // click-away
  ]);
});

// Failure prevented: the rail covers plan text with no way to read what is
// behind it, or a live reload pops it back open (TestBrowser_CommentsRailMinimizes).
test('the comments rail hides to a Show button that survives a reload', () => {
  pins('host-controls.ts', [
    `'<button class="rev-rail-hide"`, // the open rail's way out…
    `'<button class="rev-rail-show"`, // …and the hidden rail's way back
    'BUBBLE + rows.length', // including an explicit zero-comment count
    "sessionStorage.setItem(MIN_KEY, '1')", // hidden is kept per tab…
    'railMin = !!sessionStorage.getItem(MIN_KEY)', // …and read back after a reload
    "classList.toggle('rev-rail-min', railMin)", // the class the stylesheet keys on
  ]);
  pins('host.css', [
    '.rev-rail.rev-rail-min{width:fit-content', // hidden, the rail shrinks off the plan
    '.rev-rail-hide{',
    '.rev-rail-show{',
    'margin:24px 16px 72px', // in flow, it ends above the review bar
  ]);
});

// Failure prevented: highlighting a phrase opens a whole-section note, the agent
// receives the comment without the words it is about, or an addressed highlight
// whose words were rewritten can no longer be accepted
// (TestBrowser_HighlightCommentsOnExactWords, …LeftoverSelectionDoesNotHijackAClick,
// …HighlightStopsAtElementEdges).
test('a highlight comments on exactly its words', () => {
  pins('document-tools.ts', [
    '(madeSelection(ev) && selectionTarget()) || highlightAt(ev)', // this click's selection wins over the element
    'qRanges[a].range.getClientRects()', // a click opens a highlight only on its own words
    "(/^\\w/.test(q) ? '(^|\\\\W)' : '()')", // word edges match only at word boundaries…
    "if (prev && (brk || !inlineBetween(prev, t))) text += '\\n';", // …and text split by more than inline markup stays apart
    "if (n > 1) return { refuse: 'repeated'", // a repeated phrase names no one place
    'rect: t.rect || { left: ev.clientX, bottom: ev.clientY }', // the note opens below, never over
    "if (!on) { port.send({ type: 'dismiss' }); return; }", // a click elsewhere closes it, even outside review mode
  ]);
  pins('host-controls.ts', [
    'if (t.quote) m.quote = t.quote;', // the words travel with the mark
    'if (!row.at) { openPop({ a: row.a, section: row.section, label: row.label, quote: row.quote }', // a comment whose text is gone opens from its row
  ]);
  pins('document.css', ['sage', 'amber', 'red', 'gold', 'faint', 'focus'].map((t) => '::highlight(rev-q-' + t + '){'));
  pins('host.css', ['.rev-pop .rev-quote{', '.rev-rail-quote{']);
});

// Failure prevented: review mode is invisible until a hover, or the comments
// rail looks like a mark-up target.
test('review mode is styled, and the controls are never styled as targets', () => {
  pins('document.css', ['body.rev-on{cursor:crosshair}']);
  pins('host.css', ['body.rev-on .rev-rail li:hover{outline:none;cursor:pointer}', 'body.rev-on .rev-orphans li:hover{outline:none;cursor:auto}']);
});

// Failure prevented: a status or state read back from the Ledger, which no
// reader validates, closes the rail's attribute and adds a script handler
// that runs on the review server's origin, where the page holds its token.
test('text from the Ledger cannot close an attribute', async () => {
  const { esc } = await load('host-controls.ts');
  const hostile = 'flag" onmouseover="fetch(\'/approve\')';
  assert.ok(!esc(hostile).includes('"'));
  pins('host-controls.ts', ['data-status="\' + esc(row.status)', 'rev-rail-tag \' + esc(row.state)']);
});
