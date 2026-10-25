package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// reviewServerProtocol is the protocol the `ox plan review` server
// (cmd/ox/plan_review.go) speaks with the review page: the bodies of POST
// /feedback, /accept and /approve. Raise it with the server change that
// serves a new one.
const reviewServerProtocol = 3

// TestReviewAssets_MatchTheirManifest: review.js and review.css are built from
// packages/plan-review, and review.manifest.json records that build. CI's
// plan-review job rebuilds them and compares; this check needs no node.
// Failure prevented: a hand edit to a generated asset, which the next build
// silently reverts and the npm package never gets, or a page built for a
// protocol this server does not speak.
func TestReviewAssets_MatchTheirManifest(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("assets", "review.manifest.json"))
	if err != nil {
		t.Fatalf("read review.manifest.json: %v", err)
	}
	var m struct {
		Package  string            `json:"package"`
		Protocol int               `json:"protocol"`
		Files    map[string]string `json:"files"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("parse review.manifest.json: %v", err)
	}
	if m.Package != "@sageox/plan-review" || m.Protocol != reviewServerProtocol {
		t.Errorf("review assets are %s protocol %d; the review server speaks protocol %d", m.Package, m.Protocol, reviewServerProtocol)
	}
	// the bytes ox ships: review.js in the render embed, review.css in the chrome one
	for name, data := range map[string][]byte{"review.js": mustReviewScript(), "review.css": mustChromeAsset("assets/review.css")} {
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != m.Files[name] {
			t.Errorf("assets/%s is not the file its manifest records: edit packages/plan-review/src and run `make plan-review`", name)
		}
	}
}

// TestReviewCSS_InBothPageKinds: the review layer has one stylesheet, carried
// by the generated page and by the chrome injected into an authored one.
// Failure prevented: review runs unstyled on one page kind, invisible until a
// hover, with its rail looking like a mark-up target.
func TestReviewCSS_InBothPageKinds(t *testing.T) {
	css := string(mustChromeAsset("assets/review.css"))
	page, err := RenderHTMLOpts(Parse("# Plan\n\n## Rollout\n\nShip it."), Result{}, RenderOptions{Slug: "plan"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(string(page), css) {
		t.Error("a generated page does not carry review.css")
	}
	if !strings.Contains(string(InjectChrome([]byte(`<html><body></body></html>`), ChromeData{Slug: "plan"})), css) {
		t.Error("the chrome injected into an authored page does not carry review.css")
	}
}

// TestReviewSW_OfflineShellContract pins the service worker: network-first,
// cache fallback, a copy saved at install, and scoped to GET / only. The
// real-browser proof of the install copy is
// TestBrowser_UnsentHighlightSurvivesServerRestart (cmd/ox, build tag
// `browser`). Failure prevented: a reload with the server down shows the
// browser's connection-error page — or, before the tab's second load, the
// worker's bare "not running" page — and the plan (and the disconnected-mode
// messaging) vanishes with it.
func TestReviewSW_OfflineShellContract(t *testing.T) {
	b, err := ReviewServiceWorkerJS()
	if err != nil {
		t.Fatalf("read sw.js: %v", err)
	}
	s := string(b)
	for _, want := range []string{
		"fetch(e.request)",     // network-first
		"caches.match('/')",    // cache fallback
		"return c.add('/');",   // the first load, which the worker never sees, is saved at install
		"url.pathname !== '/'", // everything else passes through
		"ox plan review",       // even the bare 503 names the restart command
	} {
		if !strings.Contains(s, want) {
			t.Errorf("sw.js missing %q", want)
		}
	}
}

// TestScaffoldJS_LeavesReviewKeysToTheReviewLayer: the review layer handles
// `r` on every page kind (authored pages have no scaffold.js). Failure
// prevented: one keypress toggles review mode twice on a generated page.
func TestScaffoldJS_LeavesReviewKeysToTheReviewLayer(t *testing.T) {
	sc, err := renderAssets.ReadFile("assets/scaffold.js")
	if err != nil {
		t.Fatalf("read scaffold.js: %v", err)
	}
	if strings.Contains(string(sc), "e.key==='r'") {
		t.Error("scaffold.js handles r too — one keypress would toggle review mode twice")
	}
}
