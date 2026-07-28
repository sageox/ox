package plan

import (
	"strings"
	"testing"
)

// manySectionInput builds a plan with n H2 sections (plus a preamble).
func manySectionInput(n int) Input {
	var b strings.Builder
	b.WriteString("# Big Plan\n\nPreamble.\n\n")
	headings := []string{"Context", "Approach", "Sequencing", "Verification", "Risks", "Rollout"}
	for i := 0; i < n; i++ {
		b.WriteString("## " + headings[i%len(headings)] + "\n\nBody " + headings[i%len(headings)] + ".\n\n")
	}
	return Parse(b.String())
}

// TestRenderHTML_SectionJumpsOverThreeSections verifies the raised ceiling: a
// plan with more than 3 H2 sections renders the sticky jump bar (one button per
// section, comparison-page register), while sections stay in the document flow
// with ids so hash/review deep links still resolve. Failure prevented: a long
// plan either rendering as one undifferentiated scroll or hiding most sections
// behind a tab state the reviewer/comment rail can miss.
func TestRenderHTML_SectionJumpsOverThreeSections(t *testing.T) {
	out, err := RenderHTML(manySectionInput(6), Result{})
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	s := string(out)
	for _, want := range []string{
		`class="tabbar jump-bar" aria-label="Plan sections"`,
		`data-tab="sec-1"`,
		`data-tab="sec-6"`,
		`<section id="sec-6"`,
		// the jump bar carries the keyboard map and the semantic color legend
		`class="kbd-map"`,
		`<kbd>1</kbd>`,
		`<kbd>[</kbd>`,
		`<kbd>r</kbd>`,
		`<kbd>t</kbd>`,
		`class="legend"`,
		`class="lg-dot sage"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("tabbed render missing %q", want)
		}
	}
	if got := strings.Count(s, `data-tab="sec-`); got != 6 {
		t.Errorf("expected 6 section jump buttons, got %d", got)
	}
	if strings.Contains(s, `role="tab"`) || strings.Contains(s, `role="tablist"`) {
		t.Error("section jumps must not advertise hidden tab semantics")
	}
}

// TestRenderHTML_SingleScrollAtThreeOrFewer pins the fallback: small plans
// keep the single scroll — a tab bar with 2-3 entries is chrome without
// information — and existing plans render byte-compatible (no tabbar).
func TestRenderHTML_SingleScrollAtThreeOrFewer(t *testing.T) {
	for _, n := range []int{1, 2, 3} {
		out, err := RenderHTML(manySectionInput(n), Result{})
		if err != nil {
			t.Fatalf("RenderHTML(%d sections): %v", n, err)
		}
		if strings.Contains(string(out), `class="tabbar"`) {
			t.Errorf("%d-section plan unexpectedly rendered tabs", n)
		}
	}
}

// TestRenderHTML_InteractivePassthrough verifies the sanctioned interactivity
// channel: a ```html-interactive fence passes through as raw, executable HTML
// (entities un-escaped, script intact) in the normal render. Failure
// prevented: plan-authored interactivity (inspectors, animated figures)
// arriving as an escaped, dead code listing.
func TestRenderHTML_InteractivePassthrough(t *testing.T) {
	raw := "# P\n\n## One\n\na\n\n## Two\n\n```html-interactive\n<div id=\"insp\">x &amp; y</div>\n\n<script>document.getElementById('insp').textContent='live';</script>\n```\n"
	out, err := RenderHTML(Parse(raw), Result{})
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	s := string(out)
	for _, want := range []string{
		`<div class="interactive-block">`,
		`<div id="insp">x &amp; y</div>`, // entity in CONTENT survives one unescape
		`<script>document.getElementById('insp').textContent='live';</script>`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("interactive render missing %q", want)
		}
	}
	if strings.Contains(s, "language-html-interactive") {
		t.Error("interactive fence leaked as an escaped code listing")
	}
}

// TestRenderHTML_InteractiveStrippedInArtifact pins the artifact contract:
// --artifact stays strict — the author-scripting block is REPLACED by a static
// placeholder, keeping the CSP-safe export free of plan-authored scripts.
func TestRenderHTML_InteractiveStrippedInArtifact(t *testing.T) {
	raw := "# P\n\n## One\n\na\n\n## Two\n\n```html-interactive\n<script>alert(1)</script>\n```\n"
	out, err := RenderHTMLOpts(Parse(raw), Result{}, RenderOptions{Artifact: true})
	if err != nil {
		t.Fatalf("RenderHTMLOpts: %v", err)
	}
	s := string(out)
	if strings.Contains(s, "alert(1)") {
		t.Error("artifact render carried the plan-authored script")
	}
	if !strings.Contains(s, `class="interactive-omitted"`) {
		t.Error("artifact render missing the interactive-omitted placeholder")
	}
	if findings := LintArtifact(out); len(findings) != 0 {
		t.Errorf("artifact render with stripped interactivity failed LintArtifact: %+v", findings)
	}
}

// TestRenderHTML_CompareContainer verifies the comparison-pane marker: a
// `:::compare … :::` block wraps its two tables into adjacent panes.
func TestRenderHTML_CompareContainer(t *testing.T) {
	raw := strings.Join([]string{
		"# P", "",
		"## Formats", "",
		":::compare", "",
		"| ours | |", "|---|---|", "| a | 1 |", "",
		"| theirs | |", "|---|---|", "| b | 2 |", "",
		":::", "",
	}, "\n")
	out, err := RenderHTML(Parse(raw), Result{})
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, `<div class="compare">`) {
		t.Fatal("compare container not rendered")
	}
	if got := strings.Count(s, `<div class="compare-pane">`); got != 2 {
		t.Errorf("expected 2 compare panes, got %d", got)
	}
	if strings.Contains(s, ":::compare") {
		t.Error("compare marker paragraph leaked into the output")
	}
}

// TestRenderHTML_CompareUnbalancedMarkerVisible pins the fail-visible rule:
// an opener with no close is left as-is (author feedback beats silent damage).
func TestRenderHTML_CompareUnbalancedMarkerVisible(t *testing.T) {
	raw := "# P\n\n## S\n\n:::compare\n\nprose only, no close\n"
	out, err := RenderHTML(Parse(raw), Result{})
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	s := string(out)
	if strings.Contains(s, `class="compare"`) {
		t.Error("unbalanced compare marker was wrapped anyway")
	}
	if !strings.Contains(s, ":::compare") {
		t.Error("unbalanced marker should remain visible in the output")
	}
}

// TestRenderHTML_CompanionCard verifies Leg A's render surface: a bundled
// companion renders as a prominent card near the top with the uniform
// companions/<name> href — and artifact mode omits it (a self-contained page
// must not carry sibling-file links).
func TestRenderHTML_CompanionCard(t *testing.T) {
	in := manySectionInput(2)
	comps := CompanionRefs([]string{"deep-dive.html"})

	out, err := RenderHTMLOpts(in, Result{}, RenderOptions{Companions: comps})
	if err != nil {
		t.Fatalf("RenderHTMLOpts: %v", err)
	}
	s := string(out)
	for _, want := range []string{
		`class="companion-card"`,
		`href="companions/deep-dive.html"`,
		"Companion · interactive deep-dive",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("companion render missing %q", want)
		}
	}
	// prominence: the card must appear before the first section, not below it.
	if strings.Index(s, `class="companion-card"`) > strings.Index(s, `<section id="sec-1"`) {
		t.Error("companion card rendered below the sections — must lead")
	}

	art, err := RenderHTMLOpts(in, Result{}, RenderOptions{Companions: comps, Artifact: true})
	if err != nil {
		t.Fatalf("RenderHTMLOpts artifact: %v", err)
	}
	if strings.Contains(string(art), `class="companion-card"`) {
		t.Error("artifact render must omit companion cards")
	}
}
