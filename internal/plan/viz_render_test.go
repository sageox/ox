package plan

import (
	"strings"
	"testing"
)

// TestRenderViz_BarChart verifies bar widths are computed from the data (the
// max-valued bar is full width) and the unit is formatted. Failure prevented:
// agents hand-write SVG widths and get them wrong/misaligned.
func TestRenderViz_BarChart(t *testing.T) {
	data := `{"title":"cost","unit":"$","bars":[{"label":"a","value":0.04,"color":"sage"},{"label":"b","value":0.02,"color":"teal"}]}`
	out, err := RenderViz("bar-chart", []byte(data))
	if err != nil {
		t.Fatalf("RenderViz: %v", err)
	}
	if !strings.Contains(out, "width:100.0%") {
		t.Errorf("max bar should be full width, got: %s", out)
	}
	if !strings.Contains(out, "width:50.0%") {
		t.Errorf("half-value bar should be 50%%, got: %s", out)
	}
	if !strings.Contains(out, "$0.04") {
		t.Errorf("symbol unit should prefix value, got: %s", out)
	}
	if !strings.Contains(out, "var(--sage)") {
		t.Error("whitelisted color should map to a CSS var")
	}
}

// TestRenderViz_ColorWhitelist verifies an unknown color can't inject CSS — it
// falls back to sage.
func TestRenderViz_ColorWhitelist(t *testing.T) {
	out, _ := RenderViz("bar-chart", []byte(`{"bars":[{"label":"x","value":1,"color":"url(javascript:alert(1))"}]}`))
	if strings.Contains(out, "javascript") {
		t.Error("non-whitelisted color leaked into output")
	}
	if !strings.Contains(out, "var(--sage)") {
		t.Error("expected fallback to sage")
	}
}

// TestRenderViz_RiskMatrixSortsBySeverity verifies blocker rows come before low
// rows regardless of input order. Failure prevented: the load-bearing risk is
// buried below cosmetic ones.
func TestRenderViz_RiskMatrixSortsBySeverity(t *testing.T) {
	data := `{"risks":[{"title":"cosmetic","severity":"low"},{"title":"showstopper","severity":"blocker"}]}`
	out, err := RenderViz("risk-matrix", []byte(data))
	if err != nil {
		t.Fatalf("RenderViz: %v", err)
	}
	if strings.Index(out, "showstopper") > strings.Index(out, "cosmetic") {
		t.Errorf("blocker must sort before low:\n%s", out)
	}
	if !strings.Contains(out, `class="sev-blocker"`) {
		t.Error("severity class missing")
	}
}

// TestRenderViz_RiskMatrixSeverityGlyph verifies each severity gets a distinct
// shape glyph (redundant encoding), so the ranking survives grayscale + CVD.
func TestRenderViz_RiskMatrixSeverityGlyph(t *testing.T) {
	out, err := RenderViz("risk-matrix", []byte(`{"risks":[{"title":"x","severity":"blocker"},{"title":"y","severity":"low"}]}`))
	if err != nil {
		t.Fatalf("RenderViz: %v", err)
	}
	if !strings.Contains(out, "■ blocker") {
		t.Errorf("blocker should carry its shape glyph: %s", out)
	}
	if !strings.Contains(out, "· low") {
		t.Errorf("low should carry its shape glyph: %s", out)
	}
}

// TestRenderViz_CostWaterfallSingleColor verifies cost bars use one color (length
// is the channel), not a rainbow that fights the "which costs most" read.
func TestRenderViz_CostWaterfallSingleColor(t *testing.T) {
	out, err := RenderViz("cost-waterfall", []byte(`{"unit":"$","items":[{"name":"a","value":3},{"name":"b","value":1},{"name":"c","value":2}]}`))
	if err != nil {
		t.Fatalf("RenderViz: %v", err)
	}
	if strings.Contains(out, "var(--sage)") || strings.Contains(out, "var(--teal)") {
		t.Errorf("cost bars should be one color (copper), not a palette rotation: %s", out)
	}
	if strings.Count(out, "var(--copper)") != 3 {
		t.Errorf("expected all 3 bars copper, got: %s", out)
	}
}

// TestRenderViz_FileImpactGroupsByDir verifies files are grouped under their
// directory and change type maps to a class.
func TestRenderViz_FileImpactGroupsByDir(t *testing.T) {
	data := `{"files":[{"path":"internal/plan/render.go","change":"new","scope":"lg"},{"path":"internal/plan/enrich.go","change":"edit"}]}`
	out, err := RenderViz("file-impact-map", []byte(data))
	if err != nil {
		t.Fatalf("RenderViz: %v", err)
	}
	if !strings.Contains(out, "internal/plan") {
		t.Error("expected directory group header")
	}
	if !strings.Contains(out, `class="chg new"`) || !strings.Contains(out, `class="chg edit"`) {
		t.Errorf("change-type classes missing: %s", out)
	}
	// basename only inside the group, not the full path
	if strings.Contains(out, ">internal/plan/render.go<") {
		t.Error("file should render as basename inside its dir group")
	}
}

// TestRenderViz_StatCardsEscapesAndIntents verifies user text is escaped and the
// intent class is applied.
func TestRenderViz_StatCardsEscapes(t *testing.T) {
	data := `{"cards":[{"label":"<b>x</b>","value":"44KB","delta":"-63%","trend":"down","intent":"good"}]}`
	out, err := RenderViz("stat-cards", []byte(data))
	if err != nil {
		t.Fatalf("RenderViz: %v", err)
	}
	if strings.Contains(out, "<b>x</b>") {
		t.Error("label not HTML-escaped")
	}
	if !strings.Contains(out, `class="stat good"`) {
		t.Error("intent class missing")
	}
	if !strings.Contains(out, "▼") {
		t.Error("down trend arrow missing")
	}
}

// TestRenderViz_UnknownPattern verifies a non-parameterized pattern returns an
// actionable error rather than empty output.
func TestRenderViz_UnknownPattern(t *testing.T) {
	if _, err := RenderViz("sequence-diagram", []byte(`{}`)); err == nil {
		t.Error("expected error for a non-parameterized pattern")
	}
}

// TestVizCatalog_ParamPatternsRenderable verifies every catalog entry that
// advertises a `param:` shape is actually wired into RenderViz — the catalog and
// the renderer cannot drift. Failure prevented: `ox plan viz` tells the agent a
// pattern is parameterized but `render` errors.
func TestVizCatalog_ParamPatternsRenderable(t *testing.T) {
	// minimal valid data per parameterized pattern
	samples := map[string]string{
		"bar-chart":           `{"bars":[{"label":"a","value":1}]}`,
		"cost-waterfall":      `{"items":[{"name":"a","value":1}]}`,
		"stat-cards":          `{"cards":[{"label":"a","value":"1"}]}`,
		"file-impact-map":     `{"files":[{"path":"a/b.go","change":"new"}]}`,
		"risk-matrix":         `{"risks":[{"title":"a","severity":"high"}]}`,
		"flag-rollout-matrix": `{"envs":["dev"],"stages":["merge"],"cells":{"dev":{"merge":"100%"}}}`,
	}
	for _, p := range VizCatalog() {
		if p.Param == "" {
			continue
		}
		sample, ok := samples[p.ID]
		if !ok {
			t.Errorf("catalog pattern %q advertises param but no renderer sample/test exists", p.ID)
			continue
		}
		if _, err := RenderViz(p.ID, []byte(sample)); err != nil {
			t.Errorf("param pattern %q failed to render: %v", p.ID, err)
		}
	}
}
