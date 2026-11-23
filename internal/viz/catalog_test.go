package viz

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

func TestCatalogMetadataComplete(t *testing.T) {
	patterns := Catalog()
	if len(patterns) == 0 {
		t.Fatal("catalog is empty")
	}
	seen := make(map[string]bool, len(patterns))
	for _, p := range patterns {
		seen[p.ID] = true
		if p.Category == "" || p.Authoring == "" || len(p.Tags) == 0 || p.Guidance == "" {
			t.Errorf("pattern %q has incomplete metadata: %+v", p.ID, p)
		}
	}
	for id := range metadataByID {
		if !seen[id] {
			t.Errorf("metadata exists for missing catalog pattern %q", id)
		}
	}
}

func TestRichVisualContractsAreConstructionReady(t *testing.T) {
	ids := []string{
		"architecture", "coverage-matrix", "data-flow", "event-stream",
		"execution-trace", "flowchart", "operational-time-series",
		"sequence-diagram", "state-machine",
	}
	for _, id := range ids {
		t.Run(id, func(t *testing.T) {
			p, ok := PatternByID(id)
			if !ok {
				t.Fatalf("missing pattern %q", id)
			}
			c := p.Contract
			if c == nil {
				t.Fatal("missing visual contract")
			}
			if c.Version == "" || c.Question == "" || c.RejectWhen == "" || len(c.Clarity) < 4 {
				t.Fatalf("contract lacks selection boundary: %+v", c)
			}
			clarity := strings.Join(c.Clarity, " ")
			for _, want := range []string{"Conceptual clarity", "simplest truthful", "five-second baseline"} {
				if !strings.Contains(clarity, want) {
					t.Errorf("contract clarity gate missing %q: %s", want, clarity)
				}
			}
			if c.Canvas.Width < 1200 || c.Canvas.Height < 675 || c.Canvas.Margin < 64 || c.Canvas.Grid < 4 {
				t.Errorf("canvas is not PR-export ready: %+v", c.Canvas)
			}
			if c.Typography.Minimum < 18 || c.Typography.Label < 24 || c.Typography.MaxLine > 36 {
				t.Errorf("typography permits unreadable PR output: %+v", c.Typography)
			}
			if len(c.Evidence) == 0 || len(c.Composition) < 3 || len(c.Hierarchy) < 3 || len(c.Constraints) < 4 || len(c.FinishingPass) < 4 {
				t.Errorf("contract is guidance-shaped, not construction-ready: %+v", c)
			}
			for _, slot := range c.Evidence {
				if slot.ID == "" || slot.Prompt == "" || slot.Maximum < 1 {
					t.Errorf("evidence slot is not actionable: %+v", slot)
				}
			}
			seenVariants := map[string]bool{}
			for _, variant := range c.Variants {
				seenVariants[variant.ID] = variant.UseWhen != "" && variant.Budget != ""
			}
			for _, want := range []string{"compact", "standard", "deep"} {
				if !seenVariants[want] {
					t.Errorf("missing usable %s variant: %+v", want, c.Variants)
				}
			}
		})
	}
}

func TestOpenStrokePatternsCannotBecomeFilledPolygons(t *testing.T) {
	data := []byte(`{"title":"latency","series":[{"label":"p95","points":[{"x":0,"y":10},{"x":1,"y":20}]}]}`)
	fragment, err := Render("line-chart", data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fragment, `class="linec-series"`) || !strings.Contains(fragment, `fill="none"`) {
		t.Fatalf("line-chart must render series as explicit open strokes: %s", fragment)
	}
	for _, id := range []string{"sparkline", "small-multiples", "loop"} {
		p, ok := PatternByID(id)
		if !ok {
			t.Fatalf("catalog is missing %q", id)
		}
		if !strings.Contains(p.Body, `fill="none"`) && !strings.Contains(p.Body, `fill:none`) {
			t.Errorf("%s must explicitly use unfilled open paths", id)
		}
	}
}

func TestDiagramDesignSubsetIsPortableAndAttributed(t *testing.T) {
	ids := []string{
		"architecture", "flowchart", "data-flow", "layer-stack",
		"sequence-diagram", "state-machine", "timeline", "loop",
	}
	for _, id := range ids {
		t.Run(id, func(t *testing.T) {
			p, ok := PatternByID(id)
			if !ok {
				t.Fatalf("catalog is missing %q", id)
			}
			if p.Origin != diagramDesignOrigin {
				t.Errorf("origin = %q, want %q", p.Origin, diagramDesignOrigin)
			}
			for _, want := range []string{`data-ox-viz="` + id + `"`, `role="img"`, "aria-labelledby", "<title", "<desc", "viewBox"} {
				if !strings.Contains(p.Body, want) {
					t.Errorf("portable recipe missing %q", want)
				}
			}
			fragment := firstHTMLFence(p.Body)
			if fragment == "" {
				t.Fatal("recipe has no HTML fence")
			}
			if findings := Lint([]byte(fragment), LintOptions{}); HasErrors(findings) {
				t.Errorf("catalog recipe has lint errors: %+v", findings)
			}
		})
	}
}

func TestSuggestDeterministicMatches(t *testing.T) {
	cases := []struct {
		intent string
		want   string
	}{
		{"request response between API and database", "sequence-diagram"},
		{"architecture trust boundaries between services", "architecture"},
		{"branching validation gates with a fallback", "flowchart"},
		{"reinforcing feedback cycle around shared memory", "loop"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			got := Suggest(tc.intent, 1)
			if len(got) != 1 || got[0].ID != tc.want {
				t.Fatalf("Suggest(%q) = %+v, want %q first", tc.intent, got, tc.want)
			}
		})
	}
	for _, got := range Suggest("notes and annotations for a decision record", 5) {
		if got.ID == "partition-map" || got.ID == "partition-bar" {
			t.Errorf("reviewed tags should prevent partition false positive: %+v", got)
		}
	}
	for _, got := range Suggest("feedback loop around shared memory", 5) {
		if got.ID == "donut" {
			t.Errorf("word-boundary matching must not treat shared as share: %+v", got)
		}
	}

	// PR #773 is a representative architecture story: one portable catalog
	// reconciles into native and shared targets with a safe, recoverable apply.
	// Its language must select visuals instead of forcing an AI teammate to
	// browse the entire catalog or silently omit the explanation.
	intent := "show how one portable skill catalog is reconciled into native Claude and shared Codex/Gemini project targets, including deduplication, safe apply, ownership, and crash recovery"
	got := Suggest(intent, 5)
	if len(got) == 0 {
		t.Fatalf("Suggest(%q) returned no visual for an architecture story", intent)
	}
	ids := make(map[string]bool, len(got))
	for _, suggestion := range got {
		ids[suggestion.ID] = true
	}
	for _, want := range []string{"architecture", "flowchart", "sequence-diagram"} {
		if !ids[want] {
			t.Errorf("Suggest(%q) = %+v, want %q", intent, got, want)
		}
	}
	for _, suggestion := range got {
		if suggestion.Guidance == "" {
			t.Errorf("suggestion %q omitted its design guidance", suggestion.ID)
		}
	}

	got = Suggest("show reviewers an unfamiliar system behavior", 3)
	if len(got) == 0 || !strings.Contains(got[0].Reason, "low-confidence fallback") {
		t.Errorf("explicit visual request must get a labeled fallback, got %+v", got)
	}
}

func TestLintAccessibilitySafetyAndEditorialFindings(t *testing.T) {
	bad := `<svg data-ox-viz="bad" viewBox="0 0 100 100" aria-labelledby="dup missing">
		<title id="dup">Bad</title><desc id="dup">Duplicate</desc>
		<style>@import "https://example.com/theme.css";</style>
		<image href="https://example.com/remote.png"/>
		<line data-ox-connector x1="0" y1="0" x2="10" y2="10"/>
	</svg>`
	findings := Lint([]byte(bad), LintOptions{})
	for _, want := range []string{"viz.a11y.duplicate-id", "viz.a11y.role", "viz.a11y.labelledby", "viz.self-contained.external", "viz.connector.diagonal"} {
		if !hasRule(findings, want) {
			t.Errorf("missing %s in %+v", want, findings)
		}
	}
	if !HasErrors(findings) {
		t.Error("objective accessibility/safety failures must be errors")
	}

	warn := `<svg data-ox-viz="dense" role="img" aria-labelledby="t d"><title id="t">Dense</title><desc id="d">Dense diagram</desc>` +
		strings.Repeat(`<g data-ox-node data-ox-focus><text font-size="8px" fill="#fff">x</text></g>`, 13) + `</svg>`
	findings = Lint([]byte(warn), LintOptions{})
	for _, want := range []string{"viz.responsive.viewbox", "viz.type.too-small", "viz.theme.hard-color", "viz.density.nodes", "viz.focus.budget"} {
		if !hasRule(findings, want) {
			t.Errorf("missing advisory %s in %+v", want, findings)
		}
	}
}

func TestLintRejectsFilledLineChartSeries(t *testing.T) {
	bad := `<svg data-ox-viz="line-chart" role="img" aria-labelledby="t d" viewBox="0 0 100 100"><title id="t">Trend</title><desc id="d">Trend</desc><polyline class="linec-series" points="0,90 50,40 100,10"/></svg>`
	if findings := Lint([]byte(bad), LintOptions{}); !hasRule(findings, "viz.chart.open-stroke") {
		t.Fatalf("filled line-chart series escaped lint: %+v", findings)
	}
}

func TestLintSupportsHTMLCatalogFragments(t *testing.T) {
	clean := `<div class="device" style="background:var(--panel,#111411)">Mockup</div>`
	if findings := Lint([]byte(clean), LintOptions{}); len(findings) != 0 {
		t.Errorf("portable HTML fragment should lint cleanly: %+v", findings)
	}
	bad := `<style>.hard{color:#abc}.themed{color:var(--ink,#fff)}</style><div onclick="go()" style="color:var(--ink,#fff);border-color:#fff"><img src="https://example.com/mock.png"></div>`
	findings := Lint([]byte(bad), LintOptions{})
	for _, want := range []string{"viz.self-contained.external", "viz.motion.inline-handler", "viz.theme.hard-color"} {
		if !hasRule(findings, want) {
			t.Errorf("missing %s in %+v", want, findings)
		}
	}
}

func TestLintChecksHTMLSurroundingSVG(t *testing.T) {
	fragment := `<div><script src="https://example.com/remote.js"></script>` +
		`<svg data-ox-viz="safe" viewBox="0 0 10 10" role="img" aria-labelledby="t d"><title id="t">Safe</title><desc id="d">Safe SVG</desc></svg></div>`
	findings := Lint([]byte(fragment), LintOptions{})
	if !hasRule(findings, "viz.self-contained.external") {
		t.Errorf("remote HTML wrapper must not evade SVG lint: %+v", findings)
	}
}

func TestLintPRPNGEncouragesLightweightIndexedExports(t *testing.T) {
	palette := color.Palette{color.White, color.Black}
	indexed := image.NewPaletted(image.Rect(0, 0, 8, 8), palette)
	var indexedPNG bytes.Buffer
	if err := png.Encode(&indexedPNG, indexed); err != nil {
		t.Fatal(err)
	}
	if hasRule(LintPRPNG(indexedPNG.Bytes()), "viz.pr.png-palette") {
		t.Fatal("indexed PNG must not be asked to quantize again")
	}

	trueColor := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	var trueColorPNG bytes.Buffer
	if err := png.Encode(&trueColorPNG, trueColor); err != nil {
		t.Fatal(err)
	}
	if !hasRule(LintPRPNG(trueColorPNG.Bytes()), "viz.pr.png-palette") {
		t.Fatalf("true-color PNG must receive a palette finding: %+v", LintPRPNG(trueColorPNG.Bytes()))
	}
}

func firstHTMLFence(body string) string {
	const open = "```html\n"
	start := strings.Index(body, open)
	if start < 0 {
		return ""
	}
	rest := body[start+len(open):]
	end := strings.Index(rest, "\n```")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

func hasRule(findings []Finding, rule string) bool {
	for _, f := range findings {
		if f.Rule == rule {
			return true
		}
	}
	return false
}
