package plan

import (
	"strings"
	"testing"
)

func sampleInput() Input {
	raw := "# Sample Plan\n\nA short preamble.\n\n## Approach\n\nDo the thing.\n\n```mermaid\nflowchart LR\n  A[\"start\"] --> B[\"end\"]\n```\n\n## Verify\n\n| step | ok |\n|---|---|\n| build | yes |\n"
	return Parse(raw)
}

func TestRenderHTML_AttributionByConstruction(t *testing.T) {
	in := sampleInput()
	res := Result{
		Annotations: []Annotation{
			{Kind: "deterministic", Type: "collision", Why: "teammate murmured editing foo.go 1h ago", SourceURL: "murmur:abc"},
			{Kind: "judgment", Type: "rigor", Why: "thoughtful"}, // must NOT surface in the panel
		},
	}
	out, err := RenderHTML(in, res)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}

	// The render must satisfy the lint contract WITHOUT any hand-editing —
	// that is the whole point of generating attribution by construction.
	findings := LintBranding(out, res)
	if len(findings) != 0 {
		t.Fatalf("render failed lint contract by construction: %+v", findings)
	}

	s := string(out)
	for _, want := range []string{
		`aria-label="SageOx insight"`,             // OX marker
		"enriched by SageOx",                      // footer credit
		`<pre class="mermaid">`,                   // mermaid fence converted
		`<section id="sec-1"`,                     // section + anchor id
		"teammate murmured editing foo.go 1h ago", // the signal why
		"<table>",                    // GFM table rendered
		"<title>Sample Plan</title>", // H1 -> title
	} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered HTML missing %q", want)
		}
	}
	// judgment badges are agent-authored and must not appear in the deterministic panel
	if strings.Contains(s, "thoughtful") {
		t.Error("judgment badge leaked into the enrichment panel")
	}
}

func TestRenderHTML_NoSignalsNoMarker(t *testing.T) {
	in := sampleInput()
	res := Result{} // greenfield: no signals, no context

	out, err := RenderHTML(in, res)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	s := string(out)
	// No deterministic badges -> no OX marker (lint forbids overclaiming).
	if strings.Contains(s, `aria-label="SageOx insight"`) {
		t.Error("OX marker present with zero signals (overclaim)")
	}
	// Still a usable render.
	if !strings.Contains(s, `<section id="sec-1"`) {
		t.Error("sections not rendered")
	}
	// Every render carries a very subtle SageOx nod (a brand/tool credit, NOT an
	// enrichment claim) — so even a greenfield plan is recognizably SageOx.
	if !strings.Contains(s, `<code>ox plan</code> · SageOx`) {
		t.Error("subtle always-on SageOx footer nod missing on un-enriched render")
	}
	// ...but the un-enriched render must NOT carry the enrichment credit (overclaim).
	if strings.Contains(strings.ToLower(s), "enriched by sageox") {
		t.Error("un-enriched render claims enrichment credit (overclaim)")
	}
	if findings := LintBranding(out, res); len(findings) != 0 {
		t.Fatalf("no-signal render failed lint: %+v", findings)
	}
}

// TestRenderHTML_ReviewLoopNod verifies the differentiated attribution: a page
// SERVED by the live review loop (ReviewEndpoint set) earns a slightly stronger
// SageOx nod, while a plain render does not — and an un-enriched live-loop page
// still does not overclaim enrichment.
// Failure prevented: the unique live-review-loop capability ships without the
// extra SageOx credit, or the stronger nod leaks onto every static render.
func TestRenderHTML_ReviewLoopNod(t *testing.T) {
	in := sampleInput()
	res := Result{} // un-enriched: proves the loop nod is independent of enrichment

	// plain render: no live-loop nod.
	plain, err := RenderHTML(in, res)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	if strings.Contains(string(plain), "powered by SageOx") {
		t.Error("live-loop nod leaked onto a plain (non-served) render")
	}

	// served-by-review-loop render: stronger nod present.
	served, err := RenderHTMLOpts(in, res, RenderOptions{
		Slug:           "sample-plan",
		ReviewEndpoint: "http://127.0.0.1:54321",
		ReviewToken:    "tok",
	})
	if err != nil {
		t.Fatalf("RenderHTMLOpts: %v", err)
	}
	ss := string(served)
	if !strings.Contains(ss, "Live review loop · powered by SageOx") {
		t.Error("live-review-loop render missing the stronger SageOx nod")
	}
	if !strings.Contains(ss, "foot-live") {
		t.Error("live-loop nod not styled (missing foot-live class)")
	}
	// even with the stronger nod, an un-enriched page must not overclaim enrichment.
	if strings.Contains(strings.ToLower(ss), "enriched by sageox") {
		t.Error("live-loop render overclaims enrichment on an un-enriched plan")
	}
	if findings := LintBranding(served, res); len(findings) != 0 {
		t.Fatalf("un-enriched live-loop render failed lint: %+v", findings)
	}
}

// TestRenderHTML_ContentPresentation verifies the deterministic presentation
// features that aid the reader: a TL;DR hero callout, a risk-flagged section,
// colored verdict cells, and a signal anchored to the section whose file it
// concerns. Failure prevented: a flat render where the decision, the risk, and
// the team signal are buried in undifferentiated prose.
func TestRenderHTML_ContentPresentation(t *testing.T) {
	raw := strings.Join([]string{
		"# Big Plan",
		"",
		"> TL;DR: ship the renderer; one risk is CDN availability.",
		"",
		"## Approach",
		"",
		"Edit `internal/plan/render.go` to add presentation.",
		"",
		"| step | ok |",
		"|---|---|",
		"| build | yes |",
		"| deploy | no |",
		"",
		"## Risks",
		"",
		"CDN could be unavailable at view time.",
		"",
	}, "\n")
	in := Parse(raw)
	res := Result{Annotations: []Annotation{
		// a collision on render.go must anchor to the "Approach" section
		{Kind: "deterministic", Type: "collision", Why: "teammate editing render.go", Files: []string{"internal/plan/render.go"}},
	}}

	out, err := RenderHTML(in, res)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	s := string(out)

	for _, want := range []string{
		`class="tldr"`,                     // TL;DR callout lifted out
		`<section id="sec-2" class="risk"`, // Risks section flagged
		`class="v-good"`,                   // "yes" verdict colored
		`class="v-bad"`,                    // "no" verdict colored
		`class="ox-rail"`,                  // per-section anchored signal rail
		"teammate editing render.go",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered HTML missing %q", want)
		}
	}

	// The anchored signal must sit inside the Approach section (sec-1), not only
	// the global panel.
	sec1 := between(s, `<section id="sec-1"`, `<section id="sec-2"`)
	if !strings.Contains(sec1, "ox-rail") {
		t.Error("collision on render.go should anchor to the Approach section's rail")
	}
}

func between(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return ""
	}
	j := strings.Index(s[i:], end)
	if j < 0 {
		return s[i:]
	}
	return s[i : i+j]
}
