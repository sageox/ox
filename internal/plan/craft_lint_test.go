package plan

import "testing"

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
		"mermaid":       `<pre class="mermaid">x</pre>`,
		"swimlane":      `<div class="swim">`,
		"bar":           `<figure class="barc">`,
		"line (svg)":    `<figure class="linec"><svg></svg>`,
		"risk-matrix":   `<table class="riskm">`,
		"stat-cards":    `<div class="statrow">`,
		"file-impact":   `<ul class="ftree">`,
		"heatmap":       `<table class="heat">`,
		"multiples":     `<div class="multiples">`,
		"sparkline":     `<svg class="spark">`,
		"before-after":  `<div class="ba">`,
		"partition-bar": `<figure class="pbar-fig">`,
		"partition-map": `<div class="pmapv">`,
		"donut (svg)":   `<svg class="donut">`,
		"device mockup": `<div class="device">`,
	}
	for name, html := range present {
		if !anyVizPresent(html) {
			t.Errorf("anyVizPresent missed %s output: %q", name, html)
		}
	}
	if anyVizPresent(`<html><body><h2>Plan</h2><p>all prose, no visual</p></body></html>`) {
		t.Error("anyVizPresent matched a prose-only page")
	}
}
