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
		t.Errorf("cost bars should be one color (gold), not a palette rotation: %s", out)
	}
	if strings.Count(out, "var(--gold)") != 3 {
		t.Errorf("expected all 3 bars gold, got: %s", out)
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
// the renderer cannot drift. Failure prevented: `ox viz` tells the AI coworker a
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
		"partition-bar":       `{"total":100,"unit":"KB","partitions":[{"label":"a","size":60},{"label":"b","size":40}]}`,
		"partition-map":       `{"unit":"KB","partitions":[{"label":"a","size":60,"offset":"0x0"},{"label":"b","size":4,"proposed":true}]}`,
		"donut":               `{"slices":[{"label":"a","value":1}]}`,
		"radar":               `{"axes":["a","b","c"],"series":[{"label":"x","values":[1,2,3]}]}`,
		"quadrant":            `{"points":[{"label":"a","x":1,"y":2}]}`,
		"treemap":             `{"items":[{"label":"a","size":1}]}`,
		"sankey":              `{"nodes":[{"name":"a"},{"name":"b"}],"links":[{"from":"a","to":"b","value":1}]}`,
		"chord":               `{"labels":["a","b"],"matrix":[[0,1],[1,0]]}`,
		"line-chart":          `{"series":[{"label":"a","points":[{"x":0,"y":0},{"x":1,"y":1}]}]}`,
		"waterfall":           `{"rows":[{"id":"a","label":"a","start":0,"dur":10}]}`,
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

// TestRenderViz_PartitionBar verifies the proportional bar computes share widths
// and pairs a detail table.
func TestRenderViz_PartitionBar(t *testing.T) {
	out, err := RenderViz("partition-bar", []byte(`{"total":100,"unit":"KB","partitions":[{"label":"big","size":75,"color":"sage"},{"label":"small","size":25,"color":"teal"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "width:75.000%") {
		t.Errorf("expected a 75%% segment, got: %s", out)
	}
	if !strings.Contains(out, `class="pbar-tab"`) {
		t.Error("detail table missing")
	}
	if !strings.Contains(out, "75.00%") {
		t.Error("share column missing")
	}
}

// TestRenderViz_PartitionMap verifies the log-scaled rail keeps a tiny partition
// visible (floored at 10%) while the largest fills it, and that proposed rows are
// marked and "0x0" doesn't false-floor a real largest to 0.
func TestRenderViz_PartitionMap(t *testing.T) {
	out, err := RenderViz("partition-map", []byte(`{"title":"flash","unit":"KB","partitions":[{"label":"big","size":6144,"offset":"0x20000"},{"label":"tiny","size":4,"proposed":true}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "pmapv-row proposed") {
		t.Error("proposed row not marked")
	}
	if !strings.Contains(out, "log scale") {
		t.Error("log-scale label missing")
	}
	if !strings.Contains(out, "width:100.0%") {
		t.Errorf("largest partition should fill the rail: %s", out)
	}
	if strings.Contains(out, "width:0.0%") {
		t.Error("tiny partition rendered at 0% — log floor not applied")
	}
}

// TestRenderViz_PartitionMapGroupDivider verifies a row's `group` interleaves one
// section divider before the group's first row — the "PROPOSED SECURE ADDITIONS"
// block in the canonical example.
// Failure prevented: the divider silently vanishes (proposed rows blend into the
// committed layout) or emits once per row instead of once per group.
func TestRenderViz_PartitionMapGroupDivider(t *testing.T) {
	out, err := RenderViz("partition-map", []byte(`{"unit":"KB","partitions":[{"label":"ota_0","size":6144},{"label":"a","size":4,"group":"PROPOSED","proposed":true},{"label":"b","size":2,"group":"PROPOSED","proposed":true}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(out, `class="pmapv-group"`); n != 1 {
		t.Errorf("expected exactly 1 group divider for the 2-row group, got %d: %s", n, out)
	}
	if !strings.Contains(out, `class="pmapv-group">PROPOSED</div>`) {
		t.Errorf("group divider label missing: %s", out)
	}
}

// TestRenderViz_PartitionMapEqualSizes verifies that when every partition is the
// same size (no log spread, lmax==lmin) each rail floors to 10% — no NaN, no panic.
// Failure prevented: a uniform layout divides by zero in railPct and renders NaN
// widths.
func TestRenderViz_PartitionMapEqualSizes(t *testing.T) {
	out, err := RenderViz("partition-map", []byte(`{"unit":"KB","partitions":[{"label":"a","size":1024},{"label":"b","size":1024},{"label":"c","size":1024}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "NaN") {
		t.Errorf("equal sizes produced a NaN rail width: %s", out)
	}
	if n := strings.Count(out, "width:10.0%"); n != 3 {
		t.Errorf("expected all 3 rails floored to 10%%, got %d: %s", n, out)
	}
}

// TestVizRenderers_AllInCatalog is the reverse of TestVizCatalog_ParamPatternsRenderable:
// every renderer wired into RenderViz must have a catalog entry advertising a
// `param:` shape, so no renderer is unreachable from `ox viz`.
// Failure prevented: a renderer ships with no catalog entry and agents can never
// discover or invoke it.
func TestVizRenderers_AllInCatalog(t *testing.T) {
	params := map[string]bool{}
	for _, p := range VizCatalog() {
		if p.Param != "" {
			params[p.ID] = true
		}
	}
	for id := range vizRenderers {
		if !params[id] {
			t.Errorf("renderer %q has no catalog entry with a param: shape (unreachable from `ox viz`)", id)
		}
	}
}

// TestVizColors_DefinedInBothThemes guards the color whitelist against theme
// drift: every color an agent can name in JSON must resolve in BOTH the dark
// (:root) and light (html[data-theme="light"]) blocks of scaffold.css.
// Failure prevented: a color added to one theme only renders wrong (or
// transparent) in the other.
func TestVizColors_DefinedInBothThemes(t *testing.T) {
	css, err := renderAssets.ReadFile("assets/scaffold.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, cssVar := range vizColors {
		if n := strings.Count(string(css), cssVar+":"); n < 2 {
			t.Errorf("color var %q defined %d time(s) in scaffold.css; expected both dark + light themes", cssVar, n)
		}
	}
}
