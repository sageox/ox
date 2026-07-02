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
