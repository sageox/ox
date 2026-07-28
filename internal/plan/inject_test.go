package plan

import (
	"strings"
	"testing"
)

// islandBody returns the text strictly between startTag (an opening-tag
// delimiter such as `id="foo">`) and the next "</script>". Unlike the
// package's between() helper (render_test.go), which returns a span that
// STARTS AT startTag (inclusive — see its use anchoring on a whole opening
// tag like `<section id="sec-1"`), islandBody strips startTag itself so
// callers can assert on exact island content.
func islandBody(s, startTag string) string {
	return strings.TrimPrefix(between(s, startTag, "</script>"), startTag)
}

// TestInjectChrome_AppendOnlyPlacement verifies the non-destructive contract:
// the author's bytes survive byte-for-byte up to the injection point, and the
// chrome bundle lands immediately before the last </body>. Failure prevented:
// InjectChrome re-serializing the page through an HTML parser (reordering
// attributes, closing unclosed tags, mangling author markup) instead of a
// pure byte-level splice.
func TestInjectChrome_AppendOnlyPlacement(t *testing.T) {
	authored := `<html><head><title>T</title></head><body><main id="m"><h2>A</h2></main></body></html>`
	out := InjectChrome([]byte(authored), ChromeData{Slug: "demo"})
	s := string(out)

	prefixEnd := strings.Index(authored, "</body>")
	wantPrefix := authored[:prefixEnd]
	if !strings.HasSuffix(wantPrefix, "</main>") {
		t.Fatalf("test setup broken: authored prefix does not end with </main>: %q", wantPrefix)
	}
	if !strings.HasPrefix(s, wantPrefix) {
		t.Fatalf("authored bytes not preserved byte-for-byte up to the injection point\n got prefix:  %q\n want prefix: %q", s[:min(len(s), len(wantPrefix)+40)], wantPrefix)
	}

	markerAt := strings.Index(s, ChromeMarkerStart)
	bodyAt := strings.Index(s, "</body>")
	if markerAt < 0 {
		t.Fatalf("ChromeMarkerStart not found in output")
	}
	if bodyAt < 0 || markerAt >= bodyAt {
		t.Errorf("chrome bundle not placed before </body>: markerAt=%d bodyAt=%d", markerAt, bodyAt)
	}
	if got := strings.Count(s, ChromeMarkerStart); got != 1 {
		t.Errorf("expected exactly one ChromeMarkerStart, got %d", got)
	}
}

// TestInjectChrome_Idempotent verifies re-injection replaces the previous
// bundle wholesale instead of stacking a second one. Failure prevented: every
// `ox plan review` re-render (or every re-run of the injector as a plan is
// iterated on) accumulating one more copy of the chrome bundle — duplicate
// review bars, duplicate review.js instances double-registering listeners.
func TestInjectChrome_Idempotent(t *testing.T) {
	authored := []byte(`<html><body><main><h2>A</h2></main></body></html>`)
	once := InjectChrome(authored, ChromeData{Slug: "first-slug"})
	twice := InjectChrome(once, ChromeData{Slug: "second-slug"})
	s := string(twice)

	if got := strings.Count(s, ChromeMarkerStart); got != 1 {
		t.Fatalf("re-injection left %d start markers, want exactly 1", got)
	}
	if got := strings.Count(s, ChromeMarkerEnd); got != 1 {
		t.Fatalf("re-injection left %d end markers, want exactly 1", got)
	}
	chromeIsland := islandBody(s, `id="ox-chrome-data">`)
	if !strings.Contains(chromeIsland, `"slug":"second-slug"`) {
		t.Errorf("re-injected bundle missing the new slug: %s", chromeIsland)
	}
	if strings.Contains(chromeIsland, "first-slug") {
		t.Errorf("re-injected bundle still carries the old slug — old bundle not fully replaced: %s", chromeIsland)
	}
}

// TestInjectChrome_NoBodyTagAppendsAtEnd verifies a body-less HTML fragment
// (no </body> at all) still gets the bundle, appended at the end with the
// authored bytes untouched. Failure prevented: InjectChrome panicking or
// silently dropping the bundle on a fragment/partial-document input, which is
// a realistic authored-plan shape (a snippet meant to be pasted into a larger
// page, or a page an agent generated without a full <html> skeleton).
func TestInjectChrome_NoBodyTagAppendsAtEnd(t *testing.T) {
	authored := []byte(`<div>fragment</div>`)
	out := InjectChrome(authored, ChromeData{Slug: "frag"})
	s := string(out)

	if !strings.HasPrefix(s, string(authored)) {
		t.Fatalf("authored prefix not preserved for a body-less fragment: %q", s[:min(len(s), 60)])
	}
	if !strings.Contains(s, ChromeMarkerStart) || !strings.Contains(s, ChromeMarkerEnd) {
		t.Fatalf("chrome bundle not appended to a body-less fragment")
	}
}

// TestInjectChrome_ReviewIslandAndEscaping verifies the review-state and
// chrome-data islands carry their content, and that a malicious note field
// containing a literal "</script>" cannot terminate the #ox-review-state
// island early. Failure prevented: a reviewer's own note text — free-form,
// attacker-controlled if the review server is ever exposed beyond
// localhost — breaking out of the JSON island and injecting a live <script>
// into every teammate's authored plan page.
func TestInjectChrome_ReviewIslandAndEscaping(t *testing.T) {
	malicious := `[{"anchor":"h1234","state":"open","note":"</script><script>alert(1)</script>"}]`
	data := ChromeData{
		Slug:           "demo",
		ReviewJSON:     malicious,
		ReviewEndpoint: "http://127.0.0.1:54321",
		ReviewToken:    "tok-abc",
	}
	out := InjectChrome([]byte(`<html><body></body></html>`), data)
	s := string(out)

	reviewIsland := islandBody(s, `id="ox-review-state">`)
	if !strings.Contains(reviewIsland, `"anchor":"h1234"`) || !strings.Contains(reviewIsland, `"state":"open"`) {
		t.Fatalf("review-state island missing expected content: %q", reviewIsland)
	}
	if strings.Contains(reviewIsland, "</script") {
		t.Errorf("review-state island contains an unescaped </script — early island termination: %q", reviewIsland)
	}
	// the whole document must still parse as exactly the 5 ox-owned script/style
	// tags plus zero attacker-injected ones: an early-terminated island would
	// leave a stray "<script>alert(1)</script>" as its own top-level element.
	if strings.Contains(s, "<script>alert(1)</script>") {
		t.Errorf("attacker script escaped the review-state island and landed as live markup")
	}

	chromeIsland := islandBody(s, `id="ox-chrome-data">`)
	if !strings.Contains(chromeIsland, `"endpoint":"http://127.0.0.1:54321"`) {
		t.Errorf("chrome-data island missing the review endpoint: %q", chromeIsland)
	}
	if !strings.Contains(chromeIsland, `"token":"tok-abc"`) {
		t.Errorf("chrome-data island missing the review token: %q", chromeIsland)
	}
}

// TestBuildChromeData_SignalsAndContext verifies the enrichment-Result
// projection: deterministic Why noise is stripped, a "commit:" SourceURL never
// surfaces as a link, judgment badges (aligns) ARE surfaced with their citing
// URL, rigor is excluded, and Context is capped at 5 ordered by Score
// descending. Failure prevented: agent-only provenance (commit SHAs, raw
// "N commits" counts) leaking into the human-facing overlay, or the panel
// silently dropping the cited judgment badges the client agent authored.
func TestBuildChromeData_SignalsAndContext(t *testing.T) {
	res := Result{
		Annotations: []Annotation{
			{
				Kind:      BadgeDeterministic,
				Type:      BadgeCollision,
				Why:       "teammate editing foo.go (3 commits, last touched Jan 2026)",
				SourceURL: "commit:abc123",
			},
			{
				Kind:      BadgeJudgment,
				Type:      BadgeAligns,
				Why:       "matches ADR-016",
				SourceURL: "docs/adr/016.md",
			},
			{
				Kind: BadgeJudgment,
				Type: BadgeRigor,
				Why:  "thoughtful human<->agent back-and-forth", // must NOT surface
			},
		},
		Context: []ContextItem{
			{Kind: "adr", Title: "one", Score: 0.90},
			{Kind: "adr", Title: "two", Score: 0.95},
			{Kind: "adr", Title: "three", Score: 0.10},
			{Kind: "adr", Title: "four", Score: 0.50},
			{Kind: "adr", Title: "five", Score: 0.70},
			{Kind: "adr", Title: "six", Score: 0.60},
			{Kind: "adr", Title: "seven", Score: 0.80},
		},
	}

	data := BuildChromeData(res, RenderOptions{})

	if len(data.Signals) != 2 {
		t.Fatalf("want 2 signals (rigor excluded), got %d: %+v", len(data.Signals), data.Signals)
	}
	var collision, aligns *ChromeSignal
	for i := range data.Signals {
		switch data.Signals[i].Type {
		case string(BadgeCollision):
			collision = &data.Signals[i]
		case string(BadgeAligns):
			aligns = &data.Signals[i]
		}
	}
	if collision == nil {
		t.Fatal("collision signal missing")
	}
	if strings.Contains(collision.Why, "commits") || strings.Contains(collision.Why, "Jan 2026") {
		t.Errorf("collision Why not stripped of the provenance parenthetical: %q", collision.Why)
	}
	if collision.URL != "" {
		t.Errorf("commit: SourceURL leaked as a signal URL: %q", collision.URL)
	}
	if aligns == nil {
		t.Fatal("aligns (judgment) signal missing — judgment badges must surface")
	}
	if aligns.Label != "Aligns" {
		t.Errorf("aligns label = %q, want %q", aligns.Label, "Aligns")
	}
	if aligns.Why != "matches ADR-016" {
		t.Errorf("aligns why = %q, want unmodified %q", aligns.Why, "matches ADR-016")
	}
	for _, sig := range data.Signals {
		if sig.Type == string(BadgeRigor) {
			t.Errorf("rigor badge leaked into chrome signals: %+v", sig)
		}
	}

	if len(data.Context) != 5 {
		t.Fatalf("context not capped at 5, got %d: %+v", len(data.Context), data.Context)
	}
	wantOrder := []string{"two", "one", "seven", "five", "six"} // scores 0.95,0.90,0.80,0.70,0.60 desc
	for i, want := range wantOrder {
		if data.Context[i].Title != want {
			t.Errorf("context[%d].Title = %q, want %q (full: %+v)", i, data.Context[i].Title, want, data.Context)
		}
	}
}

// TestInjectChrome_ZeroEnrichmentStillInjectsReview verifies the review loop
// works on a plan with no enrichment at all — the two layers are independent.
// Failure prevented: BuildChromeData/InjectChrome conflating "nothing to show
// in the overlay" with "skip the review layer too," which would silently
// disable review on every greenfield plan (the majority case: most plans
// carry no collision/prior-art/expert signal).
func TestInjectChrome_ZeroEnrichmentStillInjectsReview(t *testing.T) {
	data := ChromeData{Slug: "greenfield"} // no signals, no context, FooterCredit false
	out := InjectChrome([]byte(`<html><body></body></html>`), data)
	s := string(out)

	reviewIsland := islandBody(s, `id="ox-review-state">`)
	if strings.TrimSpace(reviewIsland) != "[]" {
		t.Errorf("review-state island should default to an empty array, got %q", reviewIsland)
	}
	if !strings.Contains(s, `id="ox-chrome-review">`) {
		t.Fatalf("review.js not embedded on an un-enriched plan")
	}
	reviewScript := islandBody(s, `id="ox-chrome-review">`)
	if !strings.Contains(reviewScript, "OX_REVIEW_SELECTOR") {
		t.Errorf("embedded review script missing the authored-page SELECTOR override")
	}
	if !strings.Contains(s, `id="ox-chrome-boot">`) {
		t.Errorf("chrome.js boot script not embedded on an un-enriched plan")
	}
	chromeIsland := islandBody(s, `id="ox-chrome-data">`)
	if !strings.Contains(chromeIsland, `"footer_credit":false`) {
		t.Errorf("footer_credit should be false with zero enrichment: %s", chromeIsland)
	}
	if !strings.Contains(chromeIsland, `"signals":[]`) || !strings.Contains(chromeIsland, `"context":[]`) {
		t.Errorf("signals/context should default to empty arrays, not null: %s", chromeIsland)
	}
}

// TestChromeJS_EscapesAttributesAndFiltersLinks guards the standalone injected
// script's XSS boundary. The script builds small HTML strings at runtime from
// annotation values, so it must escape attribute delimiters and route every
// href through the scheme ALLOWLIST — a denylist would miss the next
// executable scheme. The JS has no runtime harness, so the source is the
// contract.
func TestChromeJS_EscapesAttributesAndFiltersLinks(t *testing.T) {
	js := string(mustChromeAsset("assets/chrome.js"))
	for _, want := range []string{
		// attribute context: quotes neutralized, apostrophe as the numeric
		// entity (&apos; is XML, not HTML4)
		`.replace(/"/g, '&quot;')`,
		`.replace(/'/g, '&#39;')`,
		`function safeURL`,
		// allowlist, not a javascript:-only denylist
		`scheme === 'http' || scheme === 'https' || scheme === 'mailto'`,
		// both link builders route through it, and escape the survivor
		`safeURL(s && s.url)`,
		`safeURL(data.session_url)`,
		`esc(url)`,
		`esc(sessionURL)`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("chrome.js missing safety guard %q", want)
		}
	}
}
