package plan

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// reviewJSPureBlock returns review.js's DOM-free helpers (between the
// "pure: begin" and "pure: end" markers) so node can run the real code, not a
// Go transcription of it.
func reviewJSPureBlock(t *testing.T) string {
	t.Helper()
	b, err := renderAssets.ReadFile("assets/review.js")
	if err != nil {
		t.Fatalf("read review.js: %v", err)
	}
	s := string(b)
	i := strings.Index(s, "// --- pure: begin")
	j := strings.Index(s, "// --- pure: end")
	if i < 0 || j < i {
		t.Fatal("review.js lost its pure-block markers; this test runs that block in node")
	}
	return s[i:j]
}

// TestReviewJS_StateMergeAndSyncInNode runs review.js's merge, ack, and status
// helpers in node. Failure prevented: two tabs on one plan overwrite each
// other's unsent marks, a sent or deleted mark comes back from a stale tab,
// a resend mints a new round id (a duplicate round), or the page claims
// "synced" when the server only saved locally. Skips without node; the real
// Chrome tests (build tag `browser`) cover the same paths end to end.
func TestReviewJS_StateMergeAndSyncInNode(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed — skipping the JS-engine run of review.js's state helpers")
	}
	pure := reviewJSPureBlock(t)
	quoted, _ := json.Marshal(pure) // a JSON string is a valid JS string literal
	script := "var PURE = " + string(quoted) + ";\n" + pure + `
var fails = [];
function check(name, ok) { if (!ok) fails.push(name); }

// two tabs, different anchors: both survive a merge
var a = { marks: { h1: { anchor: 'h1', note: 'tab A', updated_at: 10 } }, tombs: {} };
var b = { marks: { h2: { anchor: 'h2', note: 'tab B', updated_at: 20 } }, tombs: {} };
var m = mergeState(a, b);
check('both tabs kept', m.marks.h1 && m.marks.h2);

// same anchor: newest edit wins either way round
var older = { marks: { h1: { note: 'old', updated_at: 10 } }, tombs: {} };
var newer = { marks: { h1: { note: 'new', updated_at: 30 } }, tombs: {} };
check('newest wins (x)', mergeState(older, newer).marks.h1.note === 'new');
check('newest wins (y)', mergeState(newer, older).marks.h1.note === 'new');

// a delete is not resurrected by a stale copy, but a later edit beats it
var deleted = { marks: {}, tombs: { h1: 15 } };
check('tomb beats stale', !mergeState(deleted, older).marks.h1 && mergeState(deleted, older).tombs.h1 === 15);
check('tomb beats equal stamp', !mergeState({ marks: {}, tombs: { h1: 10 } }, older).marks.h1);
check('later edit beats tomb', mergeState(deleted, newer).marks.h1.note === 'new');
check('legacy unstamped mark kept', mergeState({ marks: { h9: { note: 'x' } }, tombs: {} }, { marks: {}, tombs: {} }).marks.h9);

// ack clears what was sent, keeps what was edited after Submit, tombstones the rest
var s = { marks: { h1: { updated_at: 10 }, h2: { updated_at: 50 }, h3: { updated_at: 5 } }, tombs: {} };
var box = { id: 'r1', items: [{ anchor: 'h1' }, { anchor: 'h2' }, { anchor: 'h4' }], stamps: { h1: 10, h2: 40, h4: 7 } };
var acked = ackState(s, box);
check('sent mark cleared', !acked.marks.h1 && acked.tombs.h1 === 10);
check('post-submit edit kept', acked.marks.h2 && !acked.tombs.h2);
check('unsent mark untouched', acked.marks.h3);
check('sent-elsewhere anchor tombed', acked.tombs.h4 === 7);
check('stale tab cannot resurrect sent mark', !mergeState(acked, { marks: { h1: { updated_at: 10 } }, tombs: {} }).marks.h1);
check('ack does not mutate input', s.marks.h1);
var legacy = ackState({ marks: { h5: { note: 'pre-stamp' } }, tombs: {} }, { items: [{ anchor: 'h5' }], stamps: {} });
check('legacy mark cleared and tombed', !legacy.marks.h5 && legacy.tombs.h5 === 1);

check('prune drops old tombs', JSON.stringify(pruneTombs({ a: 1, b: 990 }, 1000, 100)) === '{"b":990}');

// round ids: valid shape, unique
var ids = {};
for (var i = 0; i < 50; i++) { var id = newRoundId(); check('id shape ' + id, /^[A-Za-z0-9_-]{8,64}$/.test(id)); ids[id] = 1; }
check('ids unique', Object.keys(ids).length === 50);
// no randomUUID (older Safari, http:// origins other than localhost): 32 hex from getRandomValues
var noUUID = new Function('crypto', PURE + '; return newRoundId;')({ getRandomValues: function (b) { for (var i = 0; i < b.length; i++) b[i] = i * 7; return b; } });
check('hex fallback', /^[0-9a-f]{32}$/.test(noUUID()));

// acks: matching round id, or an older server with none
var ob = { id: 'r1' };
check('ack match', roundAcked(ob, { round_id: 'r1' }));
check('ack old server', roundAcked(ob, { notified: true }));
check('ack mismatch', !roundAcked(ob, { round_id: 'r2' }));

// status text tells the truth
check('pushed', syncMessage('Sent', { saved: true, pushed: true, round_id: 'r1' }).text === 'Sent · synced to your team');
var local = syncMessage('Sent', { saved: true, committed: true, pushed: false });
check('not pushed', local.kind === 'warn' && local.text === 'Saved on the author’s machine · not yet synced (will retry)');
var old = syncMessage('Sent', { notified: true });
check('old server never claims synced', old.kind === 'ok' && old.text.indexOf('synced') < 0);
check('duplicate is plain success', syncMessage('Sent', { saved: true, pushed: true, duplicate: true }).text === 'Sent · synced to your team');
check('not notified named', syncMessage('Sent', { saved: true, pushed: true, notified: false }).text.indexOf('not notified') > 0);
check('saved false is an error', syncMessage('Sent', { saved: false }).kind === 'err');

if (fails.length) { console.log('FAIL: ' + fails.join('; ')); process.exit(1); }
console.log('ok');
`
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("review.js state helpers failed in node: %v\n%s", err, out)
	}
}

// TestReviewJS_NoBlockingDialogs pins that review.js reports through inline
// status, never alert/confirm/prompt. Failure prevented: inside the web app's
// iframe those calls are blocked, so Submit or Approve silently does nothing
// (or a name prompt returns null and Submit refuses forever).
func TestReviewJS_NoBlockingDialogs(t *testing.T) {
	b, err := renderAssets.ReadFile("assets/review.js")
	if err != nil {
		t.Fatalf("read review.js: %v", err)
	}
	for _, bad := range []string{"alert(", "confirm(", "prompt("} {
		if strings.Contains(string(b), bad) {
			t.Errorf("review.js calls %s — blocked inside an iframe; use showSync", bad)
		}
	}
}
