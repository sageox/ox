package plan

import (
	"strings"
	"testing"
)

// TestShakeUnusedCSS_DropsAbsentFamilies: a page with no device mockup, no
// donut, and no partition map must not ship their CSS; families the page DOES
// use survive.
func TestShakeUnusedCSS_DropsAbsentFamilies(t *testing.T) {
	page := []byte(`<html><style>
.device{max-width:300px}
.device-screen{position:relative}
.donut-svg{width:140px}
.pmapv-row{display:grid}
.swim{display:flex}
.swim .bar{position:absolute}
body{margin:0}
</style><body><figure class="swim"><span class="bar done">A1</span></figure></body></html>`)
	got := string(shakeUnusedCSS(page))
	for _, gone := range []string{".device{", ".device-screen{", ".donut-svg{", ".pmapv-row{"} {
		if strings.Contains(got, gone) {
			t.Errorf("unused family rule %q survived the shake", gone)
		}
	}
	for _, kept := range []string{".swim{", ".swim .bar{", "body{margin:0}"} {
		if !strings.Contains(got, kept) {
			t.Errorf("used/base rule %q was wrongly dropped", kept)
		}
	}
}

// TestShakeUnusedCSS_KeepsRuntimeAndMixedRules: rules whose selectors are not
// family-owned (the review layer, body.rev-on interaction rules, at-rule
// preludes) are always kept — the review layer builds its DOM at runtime, so
// markup absence proves nothing. A line mixing a present family with absent
// ones is also kept.
func TestShakeUnusedCSS_KeepsRuntimeAndMixedRules(t *testing.T) {
	page := []byte(`<html><style>
.rev-bar{position:fixed}
body.rev-on section[id]:hover,body.rev-on .ox-chip:hover{outline:1px dashed}
@media(max-width:900px){.jump-meta{display:none}}
main>section .mermaid,main>section .swim{max-width:none}
</style><body><section><pre class="mermaid">graph LR</pre></section></body></html>`)
	got := string(shakeUnusedCSS(page))
	for _, kept := range []string{
		".rev-bar{position:fixed}",                 // runtime-built review layer
		"body.rev-on section[id]:hover",            // mixed line with unowned selector
		"@media(max-width:900px){.jump-meta",       // at-rule prelude is unowned
		"main>section .mermaid,main>section .swim", // mermaid present keeps the shared line
	} {
		if !strings.Contains(got, kept) {
			t.Errorf("must-keep rule %q was dropped", kept)
		}
	}
}

// TestShakeUnusedCSS_PageWithoutStyleIsUntouched pins fail-open behavior.
func TestShakeUnusedCSS_PageWithoutStyleIsUntouched(t *testing.T) {
	page := []byte(`<html><body>no style block</body></html>`)
	if got := string(shakeUnusedCSS(page)); got != string(page) {
		t.Error("page without a <style> block must pass through unchanged")
	}
}

// TestRenderHTML_TreeShakesUnusedPrimitives: an end-to-end render of a plain
// prose plan (no charts, no devices) must not ship chart/device CSS, and must
// keep the base typography + the primitives it does use.
func TestRenderHTML_TreeShakesUnusedPrimitives(t *testing.T) {
	out, err := RenderHTML(Parse("# T\n\nLede.\n\n## One\n\na.\n\n## Two\n\nb.\n\n## Three\n\nc.\n\n## Four\n\nd.\n"), Result{})
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	s := string(out)
	for _, gone := range []string{".device{", ".donut-svg{", ".sankey-link{", ".pmapv-row{", "table.riskm"} {
		if strings.Contains(s, gone) {
			t.Errorf("unused primitive CSS %q shipped", gone)
		}
	}
	for _, kept := range []string{"body{margin:0", ".tabbar{", ".tldr{"} {
		if !strings.Contains(s, kept) {
			t.Errorf("needed CSS %q missing", kept)
		}
	}
	// balanced braces after shaking (a corrupted stylesheet fails silently in
	// the browser — guard structurally)
	css := s[strings.Index(s, "<style>")+7 : strings.Index(s, "</style>")]
	if strings.Count(css, "{") != strings.Count(css, "}") {
		t.Errorf("shaken CSS has unbalanced braces: %d open vs %d close",
			strings.Count(css, "{"), strings.Count(css, "}"))
	}
}
