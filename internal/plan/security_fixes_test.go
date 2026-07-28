package plan

import (
	"os"
	"strings"
	"testing"
)

// Tests for the PR #720 review-hardening pass: link-scheme allowlisting in the
// derived-markdown extractor, server/page anchor lockstep on authored-page
// sections, and the injected-chrome escaping contract. Each hostile-input case
// here was red before its fix.

// TestExtractMarkdown_LinkSchemeAllowlist: the old filter denylisted only
// javascript:; data:/vbscript: (and any future scheme) flowed into derived
// markdown that gets re-rendered as HTML. The allowlist admits relative +
// http/https/mailto only.
func TestExtractMarkdown_LinkSchemeAllowlist(t *testing.T) {
	page := `<html><body><h1>T</h1><h2>S</h2><p>
<a href="data:text/html,<script>alert(1)</script>">d</a>
<a href="vbscript:msgbox(1)">v</a>
<a href="JAVASCRIPT:alert(1)">j</a>
<a href="DaTa:text/html;base64,x">dc</a>
<a href="https://sageox.ai/x">ok-abs</a>
<a href="mailto:team@example.com">ok-mail</a>
<a href="companions/deep-dive.html">ok-rel</a>
<a href="#local">frag</a>
</p></body></html>`
	md := ExtractMarkdown([]byte(page))
	for _, banned := range []string{"](data:", "](vbscript:", "](javascript:", "](JAVASCRIPT:", "](DaTa:"} {
		if strings.Contains(strings.ToLower(md), strings.ToLower(banned)) {
			t.Errorf("hostile scheme survived into derived markdown: %q in\n%s", banned, md)
		}
	}
	for _, want := range []string{"[ok-abs](https://sageox.ai/x)", "[ok-mail](mailto:team@example.com)", "[ok-rel](companions/deep-dive.html)"} {
		if !strings.Contains(md, want) {
			t.Errorf("allowlisted link missing: %q in\n%s", want, md)
		}
	}
	// link text always survives, even when the destination is dropped
	for _, txt := range []string{"d", "v", "j", "frag"} {
		if !strings.Contains(md, txt) {
			t.Errorf("dropped-link text %q lost from derived markdown", txt)
		}
	}
}

// TestMarkdownLinkDest_SyntaxHardening: hrefs that would break the []( )
// markdown syntax are angle-bracketed (parens, spaces) or dropped (<, >,
// newline) — never emitted raw.
func TestMarkdownLinkDest_SyntaxHardening(t *testing.T) {
	cases := []struct{ href, want string }{
		{"https://x.dev/a(1).html", "<https://x.dev/a(1).html>"},
		{"docs/plan (v2).html", "<docs/plan (v2).html>"},
		{"https://x.dev/<b>", ""},
		{"https://x.dev/a\nb", ""},
		{"#fragment-only", ""},
		{"render.go:42", ""}, // scheme-shaped prefix — dropped, never linked
		{"internal/plan/render.go:42", "internal/plan/render.go:42"},
		{"https://x.dev/ok", "https://x.dev/ok"},
	}
	for _, c := range cases {
		if got := markdownLinkDest(c.href); got != c.want {
			t.Errorf("markdownLinkDest(%q) = %q, want %q", c.href, got, c.want)
		}
	}
}

// TestExtractReviewTargets_DataOxSectionRoundTrip: review.js headingOf() was
// extended to closest('section[id], [data-ox-section]') + h3 fallback, but the
// server-side anchor extraction wasn't — a browser mark inside an authored
// [data-ox-section] container (or an h3-titled section) hashed a different
// heading server-side and never resolved in the ledger.
func TestExtractReviewTargets_DataOxSectionRoundTrip(t *testing.T) {
	page := []byte(`<html><body>
<div data-ox-section="Design decisions"><ul><li>use the layer model</li></ul></div>
<section id="sec-9"><h3>Rollout notes</h3><ul><li>stage first</li></ul></section>
</body></html>`)
	targets, err := extractReviewTargets(page)
	if err != nil {
		t.Fatalf("extractReviewTargets: %v", err)
	}
	byAnchor := map[string]reviewTarget{}
	for _, tg := range targets {
		byAnchor[tg.Anchor] = tg
	}
	// the page side computes anchorFor(li) = hash(heading + text); the server
	// must land on the identical anchor for both authored-page shapes
	liInDS := AnchorFor("Design decisions", "use the layer model")
	if _, ok := byAnchor[liInDS]; !ok {
		t.Errorf("mark on li inside [data-ox-section] does not round-trip: server never computed %s (targets: %+v)", liInDS, targets)
	}
	liInH3 := AnchorFor("Rollout notes", "stage first")
	if _, ok := byAnchor[liInH3]; !ok {
		t.Errorf("mark inside an h3-titled section does not round-trip: server used a different heading (targets: %+v)", targets)
	}
	// the [data-ox-section] container itself is markable on authored pages
	dsSelf := AnchorFor("Design decisions", "use the layer model")
	if _, ok := byAnchor[dsSelf]; !ok {
		t.Errorf("[data-ox-section] container itself is not a server-side target")
	}
}

// chrome.js's escape + URL-allowlist contract is pinned by
// TestChromeJS_EscapesAttributesAndFiltersLinks in inject_test.go.

// TestScaffoldJS_InspectorHeaderNeverConcatenated pins the inspector dock's XSS
// boundary structurally rather than by escaping. Header labels are read from
// the table's textContent — plain TEXT — and a header containing "<" or "&"
// must not become markup in the dock. Assigning them via textContent on a
// created element makes that impossible by construction; string-concatenating
// them into innerHTML would only be as safe as the escape helper in front of
// them. Cell VALUES are the opposite case: legitimately first-party rendered
// markup (code spans, links), so those stay innerHTML.
func TestScaffoldJS_InspectorHeaderNeverConcatenated(t *testing.T) {
	src, err := os.ReadFile("assets/scaffold.js")
	if err != nil {
		t.Fatalf("read scaffold.js: %v", err)
	}
	s := string(src)
	if !strings.Contains(s, `k.textContent=heads[i]`) {
		t.Error("scaffold.js inspector must set the header label via textContent, not string concatenation")
	}
	if strings.Contains(s, `heads[i]`) && strings.Contains(s, `+'<div class="inspect-f">`) {
		t.Error("scaffold.js inspector is building dock rows by string concatenation — header labels can escape their context")
	}
}
