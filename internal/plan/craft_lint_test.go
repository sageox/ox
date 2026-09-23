package plan

import (
	"strings"
	"testing"
)

// --- Design-craft realization check (cross-agent belt-and-suspenders) ---
//
// Detection lives at enrich (DiagramHints / MockupSection); CraftRealization only
// compares what ox EXPECTED against what the page DREW. These tests pin the
// realization semantics — most importantly the false-positive the old class regex
// caused: nagging a plan that DID visualize, just not in Mermaid.

// TestCraftRealization_DiagramMetByAnyVisual is the correctness regression: a
// diagram was suggested, and ANY visual the renderer can emit (a Mermaid diagram,
// a swimlane, OR an SVG/HTML chart) realizes it. Only a visually barren page is
// nagged. Failure prevented: nagging "no diagram" on a plan drawn with ox's own
// line-chart / risk-matrix — "you ignored your tooling" when the user used it.
func TestCraftRealization_DiagramMetByAnyVisual(t *testing.T) {
	res := Result{DiagramHints: []DiagramHint{{
		Section: "Flow", SuggestedType: DiagramSequence, Reason: "request/response cues",
	}}}

	realized := map[string]string{
		"mermaid diagram": `<pre class="mermaid">sequenceDiagram</pre>`,
		"swimlane":        `<div class="swim"><div class="lane"></div></div>`,
		"svg line-chart":  `<figure class="linec"><svg viewBox="0 0 10 10"></svg></figure>`,
		"bar chart":       `<figure class="barc"><div class="bar-row"></div></figure>`,
		"stat cards":      `<div class="statrow"><div class="stat"></div></div>`,
		"risk matrix":     `<table class="riskm"><tr><td></td></tr></table>`,
		"device mockup":   `<div class="device"><div class="device-screen"></div></div>`,
	}
	for name, viz := range realized {
		t.Run("realized/"+name, func(t *testing.T) {
			page := []byte(`<html><body><section><h2>Flow</h2>` + viz + `</section></body></html>`)
			if hasRule(LintCraft(res, page), "craft.missing-diagram") {
				t.Errorf("craft.missing-diagram fired even though the page drew a %s", name)
			}
			if rep := CraftRealization(res, page); rep.Emitted != 1 || rep.Realized != 1 {
				t.Errorf("expected emitted=1 realized=1, got emitted=%d realized=%d", rep.Emitted, rep.Realized)
			}
		})
	}

	// barren prose-only page → nudge fires, realization 0/1.
	barren := []byte(`<html><body><section><h2>Flow</h2><p>prose only</p></section></body></html>`)
	if !hasRule(LintCraft(res, barren), "craft.missing-diagram") {
		t.Error("expected craft.missing-diagram when a hint fired but the page drew no visual")
	}
	if rep := CraftRealization(res, barren); rep.Emitted != 1 || rep.Realized != 0 {
		t.Errorf("barren page: expected emitted=1 realized=0, got emitted=%d realized=%d", rep.Emitted, rep.Realized)
	}

	// no hint at all → nothing to realize, nothing measured.
	if hasRule(LintCraft(Result{}, barren), "craft.missing-diagram") {
		t.Error("craft.missing-diagram fired with no DiagramHint present")
	}
	if rep := CraftRealization(Result{}, barren); rep.Emitted != 0 {
		t.Errorf("no expectation should record nothing, got emitted=%d", rep.Emitted)
	}
}

// TestCraftRealization_Mockup gates the mockup nudge on the enrich-detected
// MockupSection (not a render-side cue bag) and realizes it only with a device
// mockup. Failure prevented: a UI plan that describes screens in prose ships with
// no mockup; conversely a plan that drew a mockup is left alone.
func TestCraftRealization_Mockup(t *testing.T) {
	res := Result{MockupSection: "Onboarding"}

	noMockup := []byte(`<html><body><section><h2>Onboarding</h2><p>prose</p></section></body></html>`)
	if !hasRule(LintCraft(res, noMockup), "craft.missing-mockup") {
		t.Error("expected craft.missing-mockup for a user-facing plan with no device mockup")
	}
	withMockup := []byte(`<html><body><div class="device ios"><div class="device-screen"></div></div></body></html>`)
	if hasRule(LintCraft(res, withMockup), "craft.missing-mockup") {
		t.Error("craft.missing-mockup fired even though a device mockup is present")
	}
	// no surface detected at enrich → no mockup expectation, no nudge.
	if hasRule(LintCraft(Result{}, noMockup), "craft.missing-mockup") {
		t.Error("craft.missing-mockup fired with no MockupSection detected")
	}
}

// TestCraftRealization_FailOpen: an empty page yields a zero report and no panic,
// even with expectations present.
func TestCraftRealization_FailOpen(t *testing.T) {
	rep := CraftRealization(Result{DiagramHints: []DiagramHint{{Section: "X"}}, MockupSection: "Y"}, nil)
	if rep.Emitted != 0 || rep.Realized != 0 || len(rep.Gaps) != 0 {
		t.Errorf("empty page must yield a zero report, got %+v", rep)
	}
}

// TestAnyVizPresent_CoversRenderers is the drift guard for the "a visual exists"
// predicate: every viz family the renderer can emit must be recognized, so adding
// a renderer whose output anyVizPresent can't see fails HERE rather than silently
// re-opening the missing-diagram false-positive. Add a row when a viz class is
// added to scaffold.css / viz_render*.go.
func TestAnyVizPresent_CoversRenderers(t *testing.T) {
	present := map[string]string{
		"mermaid":                 `<pre class="mermaid">x</pre>`,
		"swimlane":                `<div class="swim">`,
		"bar":                     `<figure class="barc">`,
		"line (svg)":              `<figure class="linec"><svg></svg>`,
		"risk-matrix":             `<table class="riskm">`,
		"stat-cards":              `<div class="statrow">`,
		"file-impact":             `<ul class="ftree">`,
		"heatmap":                 `<table class="heat">`,
		"multiples":               `<div class="multiples">`,
		"sparkline":               `<svg class="spark">`,
		"before-after":            `<div class="ba">`,
		"partition-bar":           `<figure class="pbar-fig">`,
		"partition-map":           `<div class="pmapv">`,
		"donut (svg)":             `<svg class="donut">`,
		"device mockup":           `<div class="device">`,
		"authored system map":     `<div class="hero-map">System topology</div>`,
		"accessible authored SVG": `<svg role="img" aria-label="Architecture">`,
	}
	for name, html := range present {
		if !anyVizPresent(html) {
			t.Errorf("anyVizPresent missed %s output: %q", name, html)
		}
	}
	if anyVizPresent(`<html><body><h2>Plan</h2><p>all prose, no visual</p></body></html>`) {
		t.Error("anyVizPresent matched a prose-only page")
	}
	if anyVizPresent(`<svg width="0" height="0" aria-hidden="true"><defs><symbol id="ox"></symbol></defs></svg><button class="ox-marker"><svg aria-hidden="true"></svg></button>`) {
		t.Error("decorative OX chrome must not satisfy the diagram requirement")
	}
}

func TestAnyVizPresent_RejectsEmptyOrHiddenSemanticContainers(t *testing.T) {
	for name, html := range map[string]string{
		"empty":        `<div class="hero-map"></div>`,
		"aria hidden":  `<div class="hero-map" aria-hidden="true">System topology</div>`,
		"hidden":       `<div class="hero-map" hidden>System topology</div>`,
		"inert":        `<div class="hero-map" inert>System topology</div>`,
		"display none": `<div class="hero-map" style="display: none">System topology</div>`,
		"stylesheet":   `<style>.hero-map { display: none }</style><div class="hero-map">System topology</div>`,
	} {
		t.Run(name, func(t *testing.T) {
			if anyVizPresent(html) {
				t.Fatalf("semantic container %q must not satisfy the hero-visual requirement", name)
			}
		})
	}

	for name, html := range map[string]string{
		"visible content":  `<div class="hero-map">System topology</div>`,
		"accessible image": `<div class="hero-map" role="img" aria-label="System topology"></div>`,
	} {
		t.Run(name, func(t *testing.T) {
			if !anyVizPresent(html) {
				t.Fatalf("semantic container %q must satisfy the hero-visual requirement", name)
			}
		})
	}
}

func TestAnyVizPresent_GenericRendererChromeIsNotAVisualization(t *testing.T) {
	in := Parse("# Plan\n\n## Approach\nProse only.\n")
	out, err := RenderHTML(in, Result{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `<svg width="0" height="0"`) {
		t.Fatal("fixture no longer carries the decorative OX sprite that caused the false positive")
	}
	if anyVizPresent(string(out)) {
		t.Fatal("generic renderer chrome must not masquerade as an explanatory visualization")
	}
}

// TestCraftRealization_ProgressiveDisclosure pins the two-reader contract: a
// substantial plan needs implementer depth, but that depth must not become the
// approver's initial wall of text.
func TestCraftRealization_ProgressiveDisclosure(t *testing.T) {
	res := Result{Signals: SignalSummary{NonTrivial: true}}
	visualOnly := []byte(`<html><body><div class="hero-map">architecture</div><h2>Files</h2><p>many details</p></body></html>`)
	if !hasRule(LintCraft(res, visualOnly), "craft.missing-progressive-disclosure") {
		t.Fatal("a material visual page without collapsed implementation depth must be flagged")
	}

	withDepth := []byte(`<html><body><div class="hero-map">architecture</div><details><summary>Implementation notes</summary><p>Exact files and gotchas.</p></details></body></html>`)
	if hasRule(LintCraft(res, withDepth), "craft.missing-progressive-disclosure") {
		t.Fatal("closed Implementation notes must satisfy progressive disclosure")
	}

	openDepth := []byte(`<details open><summary>Implementation notes</summary><p>Still overloads the first scan.</p></details>`)
	if !hasRule(LintCraft(res, openDepth), "craft.missing-progressive-disclosure") {
		t.Fatal("an initially open appendix must not satisfy progressive disclosure")
	}
}

func TestCraftRealization_MaterialPlanNeedsHeroVisual(t *testing.T) {
	res := Result{Signals: SignalSummary{Material: true}}
	withDepthOnly := []byte(`<details><summary>Implementation notes</summary><p>Exact edits.</p></details>`)
	if !hasRule(LintCraft(res, withDepthOnly), "craft.missing-hero-visual") {
		t.Fatal("a material prose-only plan must be flagged even when no diagram hint fired")
	}
	withVisual := []byte(`<svg role="img" aria-label="Runtime architecture"></svg><details><summary>Implementation notes</summary><p>Exact edits.</p></details>`)
	if hasRule(LintCraft(res, withVisual), "craft.missing-hero-visual") {
		t.Fatal("an accessible authored hero visual must satisfy the material-plan requirement")
	}
}

// TestCraftRealization_KindAware pins the defect this fixes: the craft
// expectations were written for a PLAN and applied to every artifact kind, so
// saving a design mockup with `--kind mockup` nagged the author to add a mockup
// and to relocate implementation notes into a collapsed appendix.
//
// Two of the three expectations are plan-shaped and only plan-shaped:
//   - the mockup nudge asks the author to PROPOSE a surface; a mockup artifact
//     IS that proposal, so the ask is circular;
//   - the progressive-disclosure nudge serves a plan's second reader (the
//     implementer, who needs exact files and gotchas); a mockup, a review sheet
//     and an evidence page have no such reader.
//
// The hero-visual expectation is kind-agnostic and survives for every kind — a
// mockup that is a prose wall is a real defect.
//
// Failure prevented: `ox plan save --file page.html --kind mockup` printing
// `craft.missing-mockup` at the author. Observed 2026-09-22 on a real save.
func TestCraftRealization_KindAware(t *testing.T) {
	// Material + a detected user-facing surface + a page that drew a chart but
	// no device frame and no <details> appendix: under KindPlan this is two
	// genuine gaps.
	res := Result{MockupSection: "Before & after", Signals: SignalSummary{Material: true}}
	page := []byte(`<html><body><section><h2>Before &amp; after</h2>` +
		`<figure class="barc"><div class="bar-row"></div></figure></section></body></html>`)

	t.Run("plan keeps both plan-shaped expectations", func(t *testing.T) {
		gaps := LintCraftFor(KindPlan, res, page)
		for _, want := range []string{"craft.missing-mockup", "craft.missing-progressive-disclosure"} {
			if !hasRule(gaps, want) {
				t.Errorf("KindPlan must still emit %s", want)
			}
		}
	})

	// Empty kind is KindPlan — every artifact saved before kinds existed keeps
	// its old behavior (mirrors KindOrDefault).
	t.Run("empty kind behaves as plan", func(t *testing.T) {
		if !hasRule(LintCraftFor("", res, page), "craft.missing-mockup") {
			t.Error("empty kind must behave as KindPlan")
		}
	})

	for _, kind := range []ArtifactKind{KindMockup, KindReview, KindEvidence} {
		t.Run(string(kind)+" drops the plan-shaped expectations", func(t *testing.T) {
			rep := CraftRealizationFor(kind, res, page)
			for _, banned := range []string{"craft.missing-mockup", "craft.missing-progressive-disclosure"} {
				if hasRule(rep.Gaps, banned) {
					t.Errorf("%s must not emit %s — that expectation is plan-shaped", kind, banned)
				}
			}
			// A suppressed expectation must not be COUNTED either, or the
			// hint→realization metric quietly reports a miss nobody can fix.
			if rep.Emitted != 1 {
				t.Errorf("%s: expected only the hero-visual expectation to be emitted, got emitted=%d", kind, rep.Emitted)
			}
			if rep.Realized != 1 {
				t.Errorf("%s: the page drew a chart, so the hero-visual expectation is realized; got realized=%d", kind, rep.Realized)
			}
		})
	}

	// The kind-agnostic expectation still bites: a prose-only mockup is a real gap.
	t.Run("hero visual still required for a mockup", func(t *testing.T) {
		barren := []byte(`<html><body><section><h2>Before</h2><p>prose only</p></section></body></html>`)
		if !hasRule(LintCraftFor(KindMockup, res, barren), "craft.missing-hero-visual") {
			t.Error("a prose-only mockup must still be nagged for drawing nothing")
		}
	})
}
