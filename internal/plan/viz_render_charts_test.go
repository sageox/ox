package plan

import (
	"strings"
	"testing"
)

// viz_render_charts_test.go covers the SVG chart forms (donut, radar, quadrant,
// treemap, sankey, chord) plus the two robustness wins (shape-echoing errors,
// hint param skeletons). Each geometry assertion checks a value ox COMPUTES, so a
// broken layout fails the test rather than passing vacuously.

// TestRenderViz_DonutGeometry verifies slice arc length is computed from the data
// (a 75% slice's stroke-dasharray ≈ 0.75 of the circumference) and the legend
// carries the color-independent share read.
// Failure prevented: an agent-supplied value stops driving the ring.
func TestRenderViz_DonutGeometry(t *testing.T) {
	data := `{"unit":"","slices":[{"label":"pass","value":75,"color":"sage"},{"label":"fail","value":25,"color":"teal"}]}`
	out, err := RenderViz("donut", []byte(data))
	if err != nil {
		t.Fatalf("RenderViz: %v", err)
	}
	// circumference = 2*pi*54 = 339.292; 75% slice dash = 254.469 minus a 2px gap.
	if !strings.Contains(out, `stroke-dasharray="252.47`) {
		t.Errorf("75%% slice dash not computed from value: %s", out)
	}
	if !strings.Contains(out, "75.0%") || !strings.Contains(out, "25.0%") {
		t.Errorf("legend shares missing: %s", out)
	}
	if !strings.Contains(out, "var(--sage)") || !strings.Contains(out, "var(--teal)") {
		t.Errorf("explicit slice colors not applied: %s", out)
	}
	if !strings.Contains(out, ">100<") {
		t.Errorf("center should show the total: %s", out)
	}
}

// TestRenderViz_DonutRejectsNegative verifies a negative slice fails loud rather
// than rendering a broken (negative-length) arc.
func TestRenderViz_DonutRejectsNegative(t *testing.T) {
	if _, err := RenderViz("donut", []byte(`{"slices":[{"label":"x","value":-1}]}`)); err == nil {
		t.Error("expected error for a negative slice value")
	}
}

// TestRenderViz_RadarPlotsOnAxis verifies a criterion value maps to the right
// radius along its axis spoke (computed polar geometry): value 7 of max 10 sits 70%
// up axis 0, which points straight up from center (120,116) over radius 84.
func TestRenderViz_RadarPlotsOnAxis(t *testing.T) {
	out, err := RenderViz("radar", []byte(`{"axes":["a","b","c"],"max":10,"series":[{"label":"x","values":[7,5,5]}]}`))
	if err != nil {
		t.Fatalf("RenderViz: %v", err)
	}
	// 120, 116 - 84*0.7 = 120, 57.2 — a radius no grid ring sits on, so this proves
	// the SERIES point, not a coincidental gridline.
	if !strings.Contains(out, "120.00,57.20") {
		t.Errorf("criterion not plotted at the computed radius on its axis: %s", out)
	}
}

// TestRenderViz_RadarValidates verifies the legibility guards: < 3 axes and a
// values/axes length mismatch both fail loud.
func TestRenderViz_RadarValidates(t *testing.T) {
	if _, err := RenderViz("radar", []byte(`{"axes":["a","b"],"series":[{"label":"x","values":[1,2]}]}`)); err == nil {
		t.Error("expected error for < 3 axes")
	}
	if _, err := RenderViz("radar", []byte(`{"axes":["a","b","c"],"series":[{"label":"x","values":[1,2]}]}`)); err == nil {
		t.Error("expected error for values/axes length mismatch")
	}
}

// TestRenderViz_QuadrantPlacesPoint verifies a point at max x and max y lands in
// the top-right corner of the plot box (x grows right; y is inverted so high = up).
func TestRenderViz_QuadrantPlacesPoint(t *testing.T) {
	out, err := RenderViz("quadrant", []byte(`{"x_label":"effort","y_label":"impact","points":[{"label":"a","x":10,"y":10}]}`))
	if err != nil {
		t.Fatalf("RenderViz: %v", err)
	}
	if !strings.Contains(out, `cx="288.00" cy="14.00"`) {
		t.Errorf("max x/y point should sit top-right: %s", out)
	}
}

// TestRenderViz_TreemapAreaProportional verifies squarified cell AREA is
// proportional to size: a size-3 item gets 3x the area of a size-1 item and the
// two cells tile the whole box.
// Failure prevented: the treemap stops encoding magnitude as area — its reason to exist.
func TestRenderViz_TreemapAreaProportional(t *testing.T) {
	out, err := RenderViz("treemap", []byte(`{"items":[{"label":"big","size":3,"color":"sage"},{"label":"small","size":1,"color":"teal"}]}`))
	if err != nil {
		t.Fatalf("RenderViz: %v", err)
	}
	// box 320x200 (area 64000); size 3:1 => 48000:16000 => 240x200 and 80x200.
	if !strings.Contains(out, `width="240.00" height="200.00"`) {
		t.Errorf("big cell area not proportional: %s", out)
	}
	if !strings.Contains(out, `x="240.00"`) || !strings.Contains(out, `width="80.00" height="200.00"`) {
		t.Errorf("small cell not placed/sized proportionally: %s", out)
	}
}

// TestRenderViz_TreemapZeroSizeItem verifies a zero-size item renders as a
// zero-area cell instead of panicking, in every position.
// Failure prevented: squarify dropped the all-zero row, returning fewer rects than
// items, so rects[k] indexed out of bounds — a runtime panic on any treemap with a 0.
func TestRenderViz_TreemapZeroSizeItem(t *testing.T) {
	cases := []string{
		`{"items":[{"label":"a","size":10},{"label":"z","size":0}]}`,                          // trailing zero
		`{"items":[{"label":"z","size":0},{"label":"a","size":10}]}`,                          // leading zero
		`{"items":[{"label":"a","size":10},{"label":"z","size":0},{"label":"b","size":5}]}`,   // middle zero
		`{"items":[{"label":"a","size":10},{"label":"z1","size":0},{"label":"z2","size":0}]}`, // multiple zeros
	}
	for _, data := range cases {
		out, err := RenderViz("treemap", []byte(data))
		if err != nil {
			t.Fatalf("treemap %s: %v", data, err)
		}
		// the legend lists every item, so the zero-size label must still appear.
		if !strings.Contains(out, ">z") && !strings.Contains(out, ">z1") {
			t.Errorf("zero-size item missing from render: %s", out)
		}
	}
}

// TestRenderViz_SankeyNodeHeight verifies node heights are computed from flow
// magnitude (node value = max(in,out), height = value*scale): C carries 4, A
// carries 3, B carries 1 — heights 188/141/47 at scale 47.
func TestRenderViz_SankeyNodeHeight(t *testing.T) {
	data := `{"nodes":[{"name":"A"},{"name":"B"},{"name":"C"}],"links":[{"from":"A","to":"C","value":3},{"from":"B","to":"C","value":1}]}`
	out, err := RenderViz("sankey", []byte(data))
	if err != nil {
		t.Fatalf("RenderViz: %v", err)
	}
	if !strings.Contains(out, `height="188.00"`) { // sink node C = max(in 4)
		t.Errorf("sink node height not computed from flow: %s", out)
	}
	if !strings.Contains(out, `height="141.00"`) || !strings.Contains(out, `height="47.00"`) {
		t.Errorf("source node heights not proportional to flow: %s", out)
	}
	if n := strings.Count(out, `class="sankey-link"`); n != 2 {
		t.Errorf("expected 2 ribbons, got %d", n)
	}
	if n := strings.Count(out, `class="sankey-node"`); n != 3 {
		t.Errorf("expected 3 nodes, got %d", n)
	}
}

// TestRenderViz_SankeyRejectsCycle verifies a cyclic link set fails loud — a
// sankey can't lay out a cycle, and silently dropping links would mislead.
func TestRenderViz_SankeyRejectsCycle(t *testing.T) {
	data := `{"nodes":[{"name":"A"},{"name":"B"}],"links":[{"from":"A","to":"B","value":1},{"from":"B","to":"A","value":1}]}`
	if _, err := RenderViz("sankey", []byte(data)); err == nil {
		t.Error("expected error for a cyclic flow")
	}
}

// TestRenderViz_ChordStructure verifies the matrix becomes one ribbon per
// unordered pair and one arc per label, and that shape is validated (square matrix,
// >= 2 labels).
func TestRenderViz_ChordStructure(t *testing.T) {
	out, err := RenderViz("chord", []byte(`{"labels":["a","b"],"matrix":[[0,1],[1,0]]}`))
	if err != nil {
		t.Fatalf("RenderViz: %v", err)
	}
	if n := strings.Count(out, `class="chord-rib"`); n != 1 {
		t.Errorf("expected 1 ribbon for the single pair, got %d", n)
	}
	if n := strings.Count(out, `class="chord-arc"`); n != 2 {
		t.Errorf("expected 2 node arcs, got %d", n)
	}
	if _, err := RenderViz("chord", []byte(`{"labels":["a"],"matrix":[[0]]}`)); err == nil {
		t.Error("expected error for < 2 labels")
	}
	if _, err := RenderViz("chord", []byte(`{"labels":["a","b"],"matrix":[[0,1,2],[1,0,0]]}`)); err == nil {
		t.Error("expected error for a non-square matrix row")
	}
}

// TestRenderViz_LineChartPlotsPoint verifies a data point maps to the COMPUTED
// pixel: with the plot box (x0=52,y0=14,pw=236,ph=176), (0,0) lands bottom-left
// and (x_max,y_max) lands top-right (y inverted so high = up).
// Failure prevented: the line stops tracking the data — the chart's reason to exist.
func TestRenderViz_LineChartPlotsPoint(t *testing.T) {
	out, err := RenderViz("line-chart", []byte(`{"series":[{"label":"a","points":[{"x":0,"y":0},{"x":10,"y":100}]}]}`))
	if err != nil {
		t.Fatalf("RenderViz: %v", err)
	}
	if !strings.Contains(out, "52.00,190.00") {
		t.Errorf("origin (0,0) not projected to the bottom-left: %s", out)
	}
	if !strings.Contains(out, "288.00,14.00") {
		t.Errorf("max (x_max,y_max) not projected to the top-right: %s", out)
	}
}

// TestRenderViz_LineChartThreshold verifies the threshold renders as a dashed
// horizontal reference line at the computed y for its value (py(8000) of y_max
// 20000 = 119.60).
func TestRenderViz_LineChartThreshold(t *testing.T) {
	out, err := RenderViz("line-chart", []byte(`{"y_max":20000,"threshold":{"at":8000,"label":"8k cap"},"series":[{"label":"a","points":[{"x":0,"y":0},{"x":1,"y":1}]}]}`))
	if err != nil {
		t.Fatalf("RenderViz: %v", err)
	}
	if !strings.Contains(out, `class="linec-thresh"`) || !strings.Contains(out, `y1="119.60"`) {
		t.Errorf("threshold not drawn at the computed y: %s", out)
	}
}

// TestRenderViz_LineChartSawtooth verifies a reset series keeps its drop-back
// vertex — the same x climbing to the cap then returning to the floor — so the
// sawtooth tooth survives, and that each series renders its own polyline + legend.
// Failure prevented: the reset is smoothed away and the "bounded, resets" story is lost.
func TestRenderViz_LineChartSawtooth(t *testing.T) {
	data := `{"y_max":10000,"series":[` +
		`{"label":"before","color":"red","points":[{"x":0,"y":0},{"x":6,"y":10000}]},` +
		`{"label":"after","color":"sage","points":[{"x":0,"y":0},{"x":2,"y":8000},{"x":2,"y":1000},{"x":4,"y":8000}]}]}`
	out, err := RenderViz("line-chart", []byte(data))
	if err != nil {
		t.Fatalf("RenderViz: %v", err)
	}
	if n := strings.Count(out, `class="linec-series"`); n != 2 {
		t.Errorf("expected 2 series polylines, got %d: %s", n, out)
	}
	// xMax=6 (observed); px(2)=130.67. The climb to 8000 (py=49.20) and the reset to
	// 1000 (py=172.40) at the SAME x are both present — the vertical drop is the tooth.
	if !strings.Contains(out, "130.67,49.20") || !strings.Contains(out, "130.67,172.40") {
		t.Errorf("sawtooth reset vertex not preserved: %s", out)
	}
	if !strings.Contains(out, ">before<") || !strings.Contains(out, ">after<") {
		t.Errorf("legend missing series labels: %s", out)
	}
}

// TestRenderViz_LineChartValidates verifies the data guards fail loud: no series,
// a single-point series (can't draw a line), and a negative coordinate.
func TestRenderViz_LineChartValidates(t *testing.T) {
	bad := []string{
		`{"series":[]}`,
		`{"series":[{"label":"a","points":[{"x":0,"y":0}]}]}`,
		`{"series":[{"label":"a","points":[{"x":0,"y":0},{"x":1,"y":-5}]}]}`,
	}
	for _, data := range bad {
		if _, err := RenderViz("line-chart", []byte(data)); err == nil {
			t.Errorf("expected error for invalid line-chart data: %s", data)
		}
	}
}

// TestRenderViz_ChartsAreArtifactSafe verifies the new SVG forms emit no <script>
// and no external URL, so they survive the CSP-safe `--artifact` render mode (the
// same property the sparkline relies on).
func TestRenderViz_ChartsAreArtifactSafe(t *testing.T) {
	cases := map[string]string{
		"donut":      `{"slices":[{"label":"a","value":1}]}`,
		"radar":      `{"axes":["a","b","c"],"series":[{"label":"x","values":[1,2,3]}]}`,
		"quadrant":   `{"points":[{"label":"a","x":1,"y":2}]}`,
		"treemap":    `{"items":[{"label":"a","size":1}]}`,
		"sankey":     `{"nodes":[{"name":"a"},{"name":"b"}],"links":[{"from":"a","to":"b","value":1}]}`,
		"chord":      `{"labels":["a","b"],"matrix":[[0,1],[1,0]]}`,
		"line-chart": `{"series":[{"label":"a","points":[{"x":0,"y":0},{"x":1,"y":1}]}]}`,
	}
	for id, data := range cases {
		out, err := RenderViz(id, []byte(data))
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if strings.Contains(out, "<script") {
			t.Errorf("%s emitted a <script> (breaks CSP --artifact mode)", id)
		}
		low := strings.ToLower(out)
		if strings.Contains(low, "http://") || strings.Contains(low, "https://") {
			t.Errorf("%s emitted an external URL (breaks CSP --artifact mode)", id)
		}
	}
}

// TestRenderViz_ShapeEchoOnError verifies a JSON-shape mismatch surfaces the
// pattern's expected `param:` shape (Improvement A) so an agent self-corrects in one
// shot instead of guessing the schema.
func TestRenderViz_ShapeEchoOnError(t *testing.T) {
	_, err := RenderViz("donut", []byte(`{"slices":"not-an-array"}`))
	if err == nil {
		t.Fatal("expected an unmarshal error")
	}
	if !strings.Contains(err.Error(), "expected shape:") {
		t.Errorf("error should echo the expected shape: %v", err)
	}
	if !strings.Contains(err.Error(), `"slices"`) {
		t.Errorf("echoed shape should name the data fields: %v", err)
	}
}

// TestComputeVizHints_CarriesParamSkeleton verifies a fired viz hint carries the
// matched pattern's param skeleton (Improvement B) so the agent goes straight to
// fill-in-the-blanks.
// Failure prevented: hints name a pattern but force a second `ox viz <id>`
// round-trip to recall the data shape.
func TestComputeVizHints_CarriesParamSkeleton(t *testing.T) {
	in := Input{Sections: []Section{{Heading: "Files changed", Body: "new and edited files across the renderer"}}}
	hints := computeVizHints(in)
	var fh *VizHint
	for i := range hints {
		if hints[i].PatternID == "file-impact-map" {
			fh = &hints[i]
		}
	}
	if fh == nil {
		t.Fatalf("expected a file-impact-map hint for a Files-changed section")
	}
	if fh.Param == "" {
		t.Fatal("viz hint should carry the pattern's param skeleton")
	}
	if !strings.Contains(fh.Param, "files") {
		t.Errorf("param skeleton should name the data shape: %s", fh.Param)
	}
}
