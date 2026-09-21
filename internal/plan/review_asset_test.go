package plan

import (
	"strings"
	"testing"
)

// TestReviewJS_AnchorIgnoresReviewGlyph verifies the review asset hashes the
// underlying content, not the glyphs it injects during paint. Failure prevented:
// re-clicking an already-marked item computes a different anchor and creates an
// unreachable duplicate mark instead of editing the existing one.
func TestReviewJS_AnchorIgnoresReviewGlyph(t *testing.T) {
	b, err := renderAssets.ReadFile("assets/review.js")
	if err != nil {
		t.Fatalf("read review.js: %v", err)
	}
	s := string(b)
	for _, want := range []string{
		"function anchorText(el)",
		"clone.querySelectorAll('.rev-glyph')",
		"norm(anchorText(el))",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("review.js missing %q", want)
		}
	}
}

// TestReviewJS_DisconnectedModeContract pins the connection-state layer: the
// page must detect a dead server (SSE error AND failed POST), say plainly that
// feedback is NOT being saved, keep marks in localStorage, poll /healthz, and
// reload on recovery. Failure prevented: a reviewer keeps marking up a dead
// page believing their feedback is reaching the agent.
func TestReviewJS_DisconnectedModeContract(t *testing.T) {
	b, err := renderAssets.ReadFile("assets/review.js")
	if err != nil {
		t.Fatalf("read review.js: %v", err)
	}
	s := string(b)
	for _, want := range []string{
		"es.onerror",              // SSE drop detection
		"es.onopen",               // recovery detection
		"setOffline(true)",        // failed POST flips the page offline
		"NOT being saved",         // the banner says it plainly
		"rev-offline-bar",         // sticky banner element
		"'/healthz'",              // recovery probe endpoint
		"ox plan review ' + slug", // copyable restart command
		"serviceWorker",           // offline shell registration
		"unsent mark(s) restored", // restored-marks notice after reconnect
		"if (offline) { offlineNotice(); return; }", // sends refused while offline
	} {
		if !strings.Contains(s, want) {
			t.Errorf("review.js missing %q", want)
		}
	}
}

// TestReviewSW_OfflineShellContract pins the service worker: network-first,
// cache fallback, and scoped to GET / only. Failure prevented: a reload with
// the server down shows the browser's connection-error page and the plan (and
// the disconnected-mode messaging) vanishes with it.
func TestReviewSW_OfflineShellContract(t *testing.T) {
	b, err := ReviewServiceWorkerJS()
	if err != nil {
		t.Fatalf("read sw.js: %v", err)
	}
	s := string(b)
	for _, want := range []string{
		"fetch(e.request)",     // network-first
		"caches.match('/')",    // cache fallback
		"url.pathname !== '/'", // everything else passes through
		"ox plan review",       // even the bare 503 names the restart command
	} {
		if !strings.Contains(s, want) {
			t.Errorf("sw.js missing %q", want)
		}
	}
}

// TestReviewJS_ModeExitContract pins that review mode announces itself, keeps
// its exit in view, and survives the live loop: the toggle relabels to "Exit
// review", entry shows a toast that names Esc, Esc closes the note then the
// mode, `r` toggles the mode (handled ONCE — review.js, not scaffold.js, so
// authored HTML plans get it too), keys are shared with the page through
// defaultPrevented, the mode is restored silently across a live reload but
// scoped to the tab, and review chrome is never a mark-up target.
// The real-browser proofs are TestBrowser_ReviewModeExitIsVisibleAndEscapable,
// TestBrowser_ReviewModeSurvivesLiveReload and TestBrowser_ReviewKeysWorkOnAuthoredPlan
// (cmd/ox, build tag `browser`); this is the hermetic guard CI sees.
// Failure prevented: a reviewer clicks Review, sees only a green button, and has
// no visible way back to reading the plan — or gets dropped out of review mode
// by every agent fix.
func TestReviewJS_ModeExitContract(t *testing.T) {
	b, err := renderAssets.ReadFile("assets/review.js")
	if err != nil {
		t.Fatalf("read review.js: %v", err)
	}
	s := string(b)
	for _, want := range []string{
		"on ? 'Exit review' : 'Review'",                              // the button becomes the exit
		"Esc or Exit review to leave",                                // entry toast names both exits
		"if (e.defaultPrevented) return;",                            // a key the page already handled is left alone
		"if (pop) { closePop(); e.preventDefault(); }",               // Esc: note first…
		"else if (on) { setReview(false); e.preventDefault(); }",     // …then mode, marked handled
		"if (e.key === 'r' && !typing(e)",                            // r toggles, not from a text field
		"sessionStorage.setItem(ON_KEY, '1')",                        // persisted per tab, not per browser
		"if (sessionStorage.getItem(ON_KEY)) setReview(true, true);", // restored silently after a reload
		"if (ev.target.closest(CHROME)) { closePop(); return; }",     // chrome clicks dismiss a note, never open one
		"if (!el) { closePop(); return; }",                           // click-away dismisses a note
		"if (toastEl === el) toastEl = null;",                        // a toast timer removes only its own toast
	} {
		if !strings.Contains(s, want) {
			t.Errorf("review.js missing %q", want)
		}
	}
	sc, err := renderAssets.ReadFile("assets/scaffold.js")
	if err != nil {
		t.Fatalf("read scaffold.js: %v", err)
	}
	if strings.Contains(string(sc), "e.key==='r'") {
		t.Error("scaffold.js handles r too — one keypress would toggle review mode twice")
	}
}

// TestReviewCSS_ModeStylingInBothPageKinds pins the review-mode styling in both
// stylesheets: scaffold.css (markdown-derived pages) and chrome.css (authored
// HTML plans) each carry their own copy. The browser tests render the scaffold
// page only, so this is what keeps the authored-page copy from drifting.
// Failure prevented: on authored plans only, review mode is invisible until a
// hover, or the comments rail looks like a mark-up target.
func TestReviewCSS_ModeStylingInBothPageKinds(t *testing.T) {
	for _, f := range []struct {
		name string
		read func(string) ([]byte, error)
	}{
		{"assets/scaffold.css", renderAssets.ReadFile},
		{"assets/chrome.css", chromeAssets.ReadFile},
	} {
		b, err := f.read(f.name)
		if err != nil {
			t.Fatalf("read %s: %v", f.name, err)
		}
		for _, want := range []string{
			"body.rev-on{cursor:crosshair}",                               // the mode is visible without a hover
			"body.rev-on .rev-rail li:hover{outline:none;cursor:pointer}", // rail rows are not targets
			"body.rev-on .rev-orphans li:hover{outline:none;cursor:auto}", // nor are orphan rows
		} {
			if !strings.Contains(string(b), want) {
				t.Errorf("%s missing %q", f.name, want)
			}
		}
	}
}
