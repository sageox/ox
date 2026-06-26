package plan

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// viz_render_charts.go holds the SVG chart renderers added to round out the
// catalog: donut, radar, quadrant, treemap, sankey, chord. Same contract as
// viz_render.go — the agent supplies DATA, ox computes the GEOMETRY (arc sweeps,
// polar points, squarified rects, ribbon widths) and emits a self-contained,
// pure-inline-SVG fragment. Pure SVG (no Mermaid, no CDN) so the fragments survive
// the CSP-safe `--artifact` render mode, exactly like the sparkline pattern.
//
// Every form carries a redundant, non-color channel (a legend, value labels, or
// shape ordering) so it still reads in grayscale / under color-vision deficiency —
// the same discipline as the risk-matrix shape glyphs.

// vizPalette is the default categorical color order for charts whose segments are
// genuinely distinct categories (donut slices, treemap cells, sankey nodes) — used
// when a datum names no color. Drawn from the same semantic whitelist as colorVar,
// so it themes with the page and can't inject CSS.
var vizPalette = []string{"sage", "copper", "teal", "violet", "amber", "slate"}

// paletteColor resolves a datum's color: an explicit (whitelisted) name wins; an
// empty name cycles the palette by index; an unknown name falls back to sage via
// colorVar (so a typo can't inject arbitrary CSS — guarded by the whitelist test).
func paletteColor(name string, i int) string {
	if strings.TrimSpace(name) == "" {
		return colorVar(vizPalette[i%len(vizPalette)])
	}
	return colorVar(name)
}

// co formats an SVG coordinate at fixed precision: compact, and deterministic so
// golden geometry tests assert exact computed points.
func co(f float64) string { return strconv.FormatFloat(f, 'f', 2, 64) }

// svgPolar maps a center + radius + angle (degrees, 0° = 12 o'clock, increasing
// clockwise) to an SVG point. SVG y grows downward, so the vertical component is
// negated to place 0° at the top.
func svgPolar(cx, cy, r, deg float64) (float64, float64) {
	rad := deg * math.Pi / 180
	return cx + r*math.Sin(rad), cy - r*math.Cos(rad)
}

// --- donut (part-of-whole, few slices) ---

type donutData struct {
	Title  string `json:"title"`
	Unit   string `json:"unit"`
	Center string `json:"center"` // optional center caption; defaults to the total
	Slices []struct {
		Label string  `json:"label"`
		Value float64 `json:"value"`
		Color string  `json:"color"`
	} `json:"slices"`
}

func renderDonut(data []byte) (string, error) {
	var d donutData
	if err := json.Unmarshal(data, &d); err != nil {
		return "", fmt.Errorf("donut data: %w", err)
	}
	if len(d.Slices) == 0 {
		return "", fmt.Errorf("donut: no slices")
	}
	total := 0.0
	for _, s := range d.Slices {
		if s.Value < 0 {
			return "", fmt.Errorf("donut: %q has a negative value %s (must be >= 0)", s.Label, fmtNum(s.Value))
		}
		total += s.Value
	}
	if total <= 0 {
		return "", fmt.Errorf("donut: total value is zero")
	}
	const cx, cy, r, thick = 70.0, 70.0, 54.0, 26.0
	circ := 2 * math.Pi * r

	var b strings.Builder
	b.WriteString(`<figure class="donut">`)
	if d.Title != "" {
		b.WriteString(`<figcaption>` + esc(d.Title) + `</figcaption>`)
	}
	b.WriteString(`<div class="donut-body">`)
	b.WriteString(`<svg class="donut-svg" viewBox="0 0 140 140" role="img" aria-label="` + esc(d.Title) + `">`)
	// track ring under the slices
	fmt.Fprintf(&b, `<circle cx="70" cy="70" r="54" fill="none" stroke="var(--hair2)" stroke-width="%s"/>`, co(thick))
	acc := 0.0
	for i, s := range d.Slices {
		frac := s.Value / total
		dash := frac * circ
		gap := 0.0
		if len(d.Slices) > 1 {
			gap = math.Min(2.0, dash*0.04) // hairline separator, but never eat a sliver
		}
		seg := math.Max(0, dash-gap)
		rot := acc*360 - 90 // first slice starts at 12 o'clock
		fmt.Fprintf(&b,
			`<circle cx="70" cy="70" r="54" fill="none" stroke="%s" stroke-width="%s" stroke-dasharray="%s %s" transform="rotate(%s 70 70)"><title>%s</title></circle>`,
			paletteColor(s.Color, i), co(thick), co(seg), co(circ-seg), co(rot),
			esc(s.Label+": "+fmtUnit(d.Unit, s.Value)))
		acc += frac
	}
	center := d.Center
	if center == "" {
		center = fmtUnit(d.Unit, total)
	}
	b.WriteString(`<text x="70" y="70" class="donut-ctr" text-anchor="middle" dominant-baseline="central">` + esc(center) + `</text>`)
	b.WriteString(`</svg>`)
	// legend — redundant, color-independent read (label + value + share)
	b.WriteString(`<ul class="donut-leg">`)
	for i, s := range d.Slices {
		pct := s.Value / total * 100
		fmt.Fprintf(&b,
			`<li><span class="vsw" style="background:%s"></span><span class="donut-lab">%s</span><span class="donut-val">%s · %.1f%%</span></li>`,
			paletteColor(s.Color, i), esc(s.Label), esc(fmtUnit(d.Unit, s.Value)), pct)
	}
	b.WriteString(`</ul></div></figure>`)
	return b.String(), nil
}

// --- radar (multi-criteria comparison of <= 3 options) ---

type radarData struct {
	Title  string   `json:"title"`
	Axes   []string `json:"axes"`
	Max    float64  `json:"max"` // optional shared scale ceiling; defaults to observed max
	Series []struct {
		Label  string    `json:"label"`
		Values []float64 `json:"values"`
		Color  string    `json:"color"`
	} `json:"series"`
}

func renderRadar(data []byte) (string, error) {
	var d radarData
	if err := json.Unmarshal(data, &d); err != nil {
		return "", fmt.Errorf("radar data: %w", err)
	}
	n := len(d.Axes)
	if n < 3 {
		return "", fmt.Errorf("radar: need >= 3 axes, got %d", n)
	}
	if len(d.Series) == 0 {
		return "", fmt.Errorf("radar: no series")
	}
	if len(d.Series) > 3 {
		return "", fmt.Errorf("radar: max 3 series for legibility, got %d", len(d.Series))
	}
	max := d.Max
	if max <= 0 {
		for _, s := range d.Series {
			for _, v := range s.Values {
				if v > max {
					max = v
				}
			}
		}
	}
	if max <= 0 {
		max = 1
	}
	for _, s := range d.Series {
		if len(s.Values) != n {
			return "", fmt.Errorf("radar: series %q has %d values but there are %d axes", s.Label, len(s.Values), n)
		}
		for _, v := range s.Values {
			if v < 0 {
				return "", fmt.Errorf("radar: series %q has a negative value", s.Label)
			}
		}
	}
	const cx, cy, R = 120.0, 116.0, 84.0
	axisAngle := func(i int) float64 { return float64(i) * 360.0 / float64(n) }

	var b strings.Builder
	b.WriteString(`<figure class="radar">`)
	if d.Title != "" {
		b.WriteString(`<figcaption>` + esc(d.Title) + `</figcaption>`)
	}
	b.WriteString(`<div class="radar-body">`)
	b.WriteString(`<svg class="radar-svg" viewBox="0 0 240 232" role="img" aria-label="` + esc(d.Title) + `">`)
	// concentric grid rings
	for _, lvl := range []float64{0.25, 0.5, 0.75, 1.0} {
		pts := make([]string, 0, n)
		for i := 0; i < n; i++ {
			x, y := svgPolar(cx, cy, R*lvl, axisAngle(i))
			pts = append(pts, co(x)+","+co(y))
		}
		b.WriteString(`<polygon class="radar-grid" points="` + strings.Join(pts, " ") + `"/>`)
	}
	// spokes + axis labels
	for i := 0; i < n; i++ {
		x, y := svgPolar(cx, cy, R, axisAngle(i))
		fmt.Fprintf(&b, `<line class="radar-spoke" x1="%s" y1="%s" x2="%s" y2="%s"/>`, co(cx), co(cy), co(x), co(y))
		lx, ly := svgPolar(cx, cy, R+12, axisAngle(i))
		anchor := "middle"
		if lx > cx+1 {
			anchor = "start"
		} else if lx < cx-1 {
			anchor = "end"
		}
		fmt.Fprintf(&b, `<text class="radar-axis" x="%s" y="%s" text-anchor="%s">%s</text>`, co(lx), co(ly+3), anchor, esc(d.Axes[i]))
	}
	// series polygons — distinct stroke dash per series (redundant with color so the
	// options stay distinguishable in grayscale / under CVD)
	dashes := []string{"", "5 3", "1.5 3"}
	for si, s := range d.Series {
		pts := make([]string, 0, n)
		for i, v := range s.Values {
			x, y := svgPolar(cx, cy, R*(v/max), axisAngle(i))
			pts = append(pts, co(x)+","+co(y))
		}
		col := paletteColor(s.Color, si)
		dash := ""
		if dd := dashes[si%len(dashes)]; dd != "" {
			dash = ` stroke-dasharray="` + dd + `"`
		}
		fmt.Fprintf(&b, `<polygon class="radar-series" points="%s" style="stroke:%s;fill:%s"%s/>`, strings.Join(pts, " "), col, col, dash)
	}
	b.WriteString(`</svg>`)
	// legend
	b.WriteString(`<ul class="radar-leg">`)
	for si, s := range d.Series {
		fmt.Fprintf(&b, `<li><span class="vsw" style="background:%s"></span>%s</li>`, paletteColor(s.Color, si), esc(s.Label))
	}
	b.WriteString(`</ul></div></figure>`)
	return b.String(), nil
}

// --- quadrant (impact x effort scatter / 2x2) ---

type quadrantData struct {
	Title  string  `json:"title"`
	XLabel string  `json:"x_label"`
	YLabel string  `json:"y_label"`
	XMax   float64 `json:"x_max"` // optional; defaults to observed max x (min 1)
	YMax   float64 `json:"y_max"`
	Points []struct {
		Label string  `json:"label"`
		X     float64 `json:"x"`
		Y     float64 `json:"y"`
		Color string  `json:"color"`
	} `json:"points"`
}

func renderQuadrant(data []byte) (string, error) {
	var d quadrantData
	if err := json.Unmarshal(data, &d); err != nil {
		return "", fmt.Errorf("quadrant data: %w", err)
	}
	if len(d.Points) == 0 {
		return "", fmt.Errorf("quadrant: no points")
	}
	xMax, yMax := d.XMax, d.YMax
	for _, p := range d.Points {
		if p.X < 0 || p.Y < 0 {
			return "", fmt.Errorf("quadrant: %q has a negative coordinate (x/y must be >= 0)", p.Label)
		}
		if p.X > xMax {
			xMax = p.X
		}
		if p.Y > yMax {
			yMax = p.Y
		}
	}
	if xMax <= 0 {
		xMax = 1
	}
	if yMax <= 0 {
		yMax = 1
	}
	// plot area inside the viewBox, leaving room for axis labels
	const x0, y0, pw, ph = 50.0, 14.0, 238.0, 182.0
	px := func(x float64) float64 { return x0 + (x/xMax)*pw }
	py := func(y float64) float64 { return y0 + (1-y/yMax)*ph } // invert: up = high

	var b strings.Builder
	b.WriteString(`<figure class="quad">`)
	if d.Title != "" {
		b.WriteString(`<figcaption>` + esc(d.Title) + `</figcaption>`)
	}
	b.WriteString(`<svg class="quad-svg" viewBox="0 0 300 224" role="img" aria-label="` + esc(d.Title) + `">`)
	// plot frame + midlines (the 2x2 split)
	fmt.Fprintf(&b, `<rect class="quad-box" x="%s" y="%s" width="%s" height="%s"/>`, co(x0), co(y0), co(pw), co(ph))
	fmt.Fprintf(&b, `<line class="quad-mid" x1="%s" y1="%s" x2="%s" y2="%s"/>`, co(x0+pw/2), co(y0), co(x0+pw/2), co(y0+ph))
	fmt.Fprintf(&b, `<line class="quad-mid" x1="%s" y1="%s" x2="%s" y2="%s"/>`, co(x0), co(y0+ph/2), co(x0+pw), co(y0+ph/2))
	// points
	for i, p := range d.Points {
		cx, cy := px(p.X), py(p.Y)
		col := paletteColor(p.Color, i)
		fmt.Fprintf(&b, `<circle class="quad-pt" cx="%s" cy="%s" r="5" style="fill:%s"><title>%s</title></circle>`,
			co(cx), co(cy), col, esc(p.Label))
		anchor := "start"
		lx := cx + 8
		if cx > x0+pw*0.7 { // near the right edge, label leftwards so it stays in-frame
			anchor = "end"
			lx = cx - 8
		}
		fmt.Fprintf(&b, `<text class="quad-lab" x="%s" y="%s" text-anchor="%s">%s</text>`, co(lx), co(cy+3.5), anchor, esc(p.Label))
	}
	// axis labels
	if d.XLabel != "" {
		fmt.Fprintf(&b, `<text class="quad-axl" x="%s" y="218" text-anchor="middle">%s →</text>`, co(x0+pw/2), esc(d.XLabel))
	}
	if d.YLabel != "" {
		fmt.Fprintf(&b, `<text class="quad-axl" x="14" y="%s" text-anchor="middle" transform="rotate(-90 14 %s)">%s →</text>`, co(y0+ph/2), co(y0+ph/2), esc(d.YLabel))
	}
	b.WriteString(`</svg></figure>`)
	return b.String(), nil
}

// sortIndexByDesc returns the indices of vals sorted by descending value (stable).
// Shared by treemap (squarify expects descending) and others.
func sortIndexByDesc(vals []float64) []int {
	idx := make([]int, len(vals))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return vals[idx[a]] > vals[idx[b]] })
	return idx
}

// --- treemap (proportional hierarchy: cell area is proportional to size) ---

type treemapData struct {
	Title string `json:"title"`
	Unit  string `json:"unit"`
	Items []struct {
		Label string  `json:"label"`
		Size  float64 `json:"size"`
		Color string  `json:"color"`
	} `json:"items"`
}

// vrect is a geometry rectangle (distinct from the partition segment types).
type vrect struct{ x, y, w, h float64 }

// squarify lays out scaled `areas` (whose sum equals space.w*space.h) into `space`,
// returning one vrect per area in input order, using the squarified treemap
// algorithm (Bruls, Huizing, van Wijk) so cells stay near-square instead of thin
// slivers. Areas should be descending for the best aspect ratios; the caller sorts
// and maps the rects back to original items.
func squarify(areas []float64, space vrect) []vrect {
	out := make([]vrect, 0, len(areas))
	s := space
	i := 0
	for i < len(areas) {
		short := math.Min(s.w, s.h)
		row := []float64{areas[i]}
		j := i + 1
		for j < len(areas) {
			grown := make([]float64, len(row), len(row)+1)
			copy(grown, row)
			grown = append(grown, areas[j])
			if worstAspect(grown, short) <= worstAspect(row, short) {
				row = grown
				j++
			} else {
				break
			}
		}
		s = layoutStrip(row, s, &out)
		i = j
	}
	return out
}

// worstAspect returns the worst (largest) aspect ratio among the cells that `row`
// would form along a strip of length `short` — the quantity squarify minimizes.
func worstAspect(row []float64, short float64) float64 {
	sum, mx, mn := 0.0, 0.0, math.MaxFloat64
	for _, a := range row {
		sum += a
		if a > mx {
			mx = a
		}
		if a < mn {
			mn = a
		}
	}
	if sum <= 0 || short <= 0 {
		return math.MaxFloat64
	}
	s2, sum2 := short*short, sum*sum
	return math.Max(s2*mx/sum2, sum2/(s2*mn))
}

// layoutStrip places `row` as one strip along the shorter side of `s`, appends its
// rects to out, and returns the remaining space.
func layoutStrip(row []float64, s vrect, out *[]vrect) vrect {
	sum := 0.0
	for _, a := range row {
		sum += a
	}
	if sum <= 0 {
		// an all-zero row (zero-size items): emit a zero-area rect PER item so the
		// result stays one-rect-per-input — callers index rects by item, so a short
		// slice would index out of bounds. Leave the remaining space unchanged.
		for range row {
			*out = append(*out, vrect{s.x, s.y, 0, 0})
		}
		return s
	}
	if s.w >= s.h {
		thick := sum / s.h // vertical strip on the left
		y := s.y
		for _, a := range row {
			h := a / thick
			*out = append(*out, vrect{s.x, y, thick, h})
			y += h
		}
		return vrect{s.x + thick, s.y, s.w - thick, s.h}
	}
	thick := sum / s.w // horizontal strip on top
	x := s.x
	for _, a := range row {
		w := a / thick
		*out = append(*out, vrect{x, s.y, w, thick})
		x += w
	}
	return vrect{s.x, s.y + thick, s.w, s.h - thick}
}

// truncLabel clips a label to roughly the character count a cell of widthPx can
// hold, so in-cell text never overflows its rectangle.
func truncLabel(s string, widthPx float64) string {
	maxCh := int(widthPx / 6.5)
	r := []rune(s)
	if len(r) <= maxCh {
		return s
	}
	if maxCh <= 1 {
		return "…"
	}
	return string(r[:maxCh-1]) + "…"
}

func renderTreemap(data []byte) (string, error) {
	var d treemapData
	if err := json.Unmarshal(data, &d); err != nil {
		return "", fmt.Errorf("treemap data: %w", err)
	}
	if len(d.Items) == 0 {
		return "", fmt.Errorf("treemap: no items")
	}
	sizes := make([]float64, len(d.Items))
	total := 0.0
	for i, it := range d.Items {
		if it.Size < 0 {
			return "", fmt.Errorf("treemap: %q has a negative size %s (must be >= 0)", it.Label, fmtNum(it.Size))
		}
		sizes[i] = it.Size
		total += it.Size
	}
	if total <= 0 {
		return "", fmt.Errorf("treemap: total size is zero")
	}
	const W, H = 320.0, 200.0
	order := sortIndexByDesc(sizes) // descending for squarify; order[k] -> original item
	areas := make([]float64, len(order))
	scale := (W * H) / total
	for k, oi := range order {
		areas[k] = sizes[oi] * scale
	}
	rects := squarify(areas, vrect{0, 0, W, H})

	var b strings.Builder
	b.WriteString(`<figure class="tmap">`)
	if d.Title != "" {
		b.WriteString(`<figcaption>` + esc(d.Title) + `</figcaption>`)
	}
	fmt.Fprintf(&b, `<svg class="tmap-svg" viewBox="0 0 %s %s" preserveAspectRatio="none" role="img" aria-label="%s">`, co(W), co(H), esc(d.Title))
	for k, oi := range order {
		it := d.Items[oi]
		rc := rects[k]
		col := paletteColor(it.Color, k)
		pct := sizes[oi] / total * 100
		b.WriteString(`<g class="tmap-cell">`)
		fmt.Fprintf(&b, `<rect x="%s" y="%s" width="%s" height="%s" style="fill:%s"><title>%s — %s · %.1f%%</title></rect>`,
			co(rc.x), co(rc.y), co(rc.w), co(rc.h), col, esc(it.Label), esc(fmtUnit(d.Unit, sizes[oi])), pct)
		if rc.w >= 46 && rc.h >= 24 { // only label cells that can hold text legibly
			fmt.Fprintf(&b, `<text class="tmap-lab" x="%s" y="%s">%s</text>`, co(rc.x+6), co(rc.y+15), esc(truncLabel(it.Label, rc.w-10)))
			if rc.h >= 40 {
				fmt.Fprintf(&b, `<text class="tmap-val" x="%s" y="%s">%s</text>`, co(rc.x+6), co(rc.y+28), esc(fmtUnit(d.Unit, sizes[oi])))
			}
		}
		b.WriteString(`</g>`)
	}
	b.WriteString(`</svg>`)
	// legend — guarantees a color-independent read even for sliver cells (CVD)
	b.WriteString(`<ul class="tmap-leg">`)
	for k, oi := range order {
		it := d.Items[oi]
		pct := sizes[oi] / total * 100
		fmt.Fprintf(&b,
			`<li><span class="vsw" style="background:%s"></span><span class="tmap-leg-lab">%s</span><span class="tmap-leg-val">%s · %.1f%%</span></li>`,
			paletteColor(it.Color, k), esc(it.Label), esc(fmtUnit(d.Unit, sizes[oi])), pct)
	}
	b.WriteString(`</ul></figure>`)
	return b.String(), nil
}

// --- sankey (flow magnitude across stages) ---

type sankeyData struct {
	Title string `json:"title"`
	Unit  string `json:"unit"`
	Nodes []struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	} `json:"nodes"`
	Links []struct {
		From  string  `json:"from"`
		To    string  `json:"to"`
		Value float64 `json:"value"`
		Color string  `json:"color"`
	} `json:"links"`
}

func renderSankey(data []byte) (string, error) {
	var d sankeyData
	if err := json.Unmarshal(data, &d); err != nil {
		return "", fmt.Errorf("sankey data: %w", err)
	}
	if len(d.Nodes) == 0 {
		return "", fmt.Errorf("sankey: no nodes")
	}
	if len(d.Links) == 0 {
		return "", fmt.Errorf("sankey: no links")
	}
	idx := make(map[string]int, len(d.Nodes))
	for i, nd := range d.Nodes {
		if strings.TrimSpace(nd.Name) == "" {
			return "", fmt.Errorf("sankey: node %d has no name", i)
		}
		if _, dup := idx[nd.Name]; dup {
			return "", fmt.Errorf("sankey: duplicate node name %q", nd.Name)
		}
		idx[nd.Name] = i
	}
	type ln struct {
		s, t  int
		v     float64
		color string
	}
	links := make([]ln, 0, len(d.Links))
	for _, l := range d.Links {
		s, ok1 := idx[l.From]
		t, ok2 := idx[l.To]
		if !ok1 {
			return "", fmt.Errorf("sankey: link references unknown node %q", l.From)
		}
		if !ok2 {
			return "", fmt.Errorf("sankey: link references unknown node %q", l.To)
		}
		if l.Value <= 0 {
			return "", fmt.Errorf("sankey: link %q→%q has value %s (must be > 0)", l.From, l.To, fmtNum(l.Value))
		}
		if s == t {
			return "", fmt.Errorf("sankey: link %q→%q is a self-loop", l.From, l.To)
		}
		links = append(links, ln{s, t, l.Value, l.Color})
	}
	n := len(d.Nodes)
	// longest-path layering: layer[t] = max(layer[s]+1). Relax up to n times; still
	// changing after that means a cycle, which a sankey can't lay out.
	layer := make([]int, n)
	for pass := 0; pass < n; pass++ {
		changed := false
		for _, l := range links {
			if layer[l.t] < layer[l.s]+1 {
				layer[l.t] = layer[l.s] + 1
				changed = true
			}
		}
		if !changed {
			break
		}
		if pass == n-1 {
			return "", fmt.Errorf("sankey: links form a cycle (a DAG is required)")
		}
	}
	nLayers := 0
	for _, lv := range layer {
		if lv+1 > nLayers {
			nLayers = lv + 1
		}
	}
	inSum := make([]float64, n)
	outSum := make([]float64, n)
	for _, l := range links {
		outSum[l.s] += l.v
		inSum[l.t] += l.v
	}
	nodeVal := make([]float64, n)
	for i := range nodeVal {
		nodeVal[i] = math.Max(inSum[i], outSum[i])
	}
	byLayer := make([][]int, nLayers)
	for i := 0; i < n; i++ {
		byLayer[layer[i]] = append(byLayer[layer[i]], i)
	}
	maxLayerTotal, maxGaps := 0.0, 0
	for _, mem := range byLayer {
		t := 0.0
		for _, i := range mem {
			t += nodeVal[i]
		}
		if t > maxLayerTotal {
			maxLayerTotal = t
		}
		if len(mem)-1 > maxGaps {
			maxGaps = len(mem) - 1
		}
	}
	if maxLayerTotal <= 0 {
		maxLayerTotal = 1
	}
	const W, plotTop, plotH, nodeW, labelPad = 360.0, 22.0, 196.0, 11.0, 56.0
	gapPx := 8.0
	scale := (plotH - float64(maxGaps)*gapPx) / maxLayerTotal
	if scale <= 0 { // pathological: too many nodes for the gap budget — shrink gaps
		gapPx = 2.0
		scale = (plotH - float64(maxGaps)*gapPx) / maxLayerTotal
	}
	xOf := func(lyr int) float64 {
		if nLayers <= 1 {
			return labelPad
		}
		inner := W - 2*labelPad - nodeW
		return labelPad + float64(lyr)*(inner/float64(nLayers-1))
	}
	posInLayer := make([]int, n)
	ny := make([]float64, n)
	nh := make([]float64, n)
	for _, mem := range byLayer {
		y := plotTop
		for p, i := range mem {
			h := nodeVal[i] * scale
			if h < 1 {
				h = 1
			}
			ny[i], nh[i], posInLayer[i] = y, h, p
			y += h + gapPx
		}
	}
	// order each node's links so ribbons stack without crossing within a node edge
	outLinks := make([][]int, n)
	inLinks := make([][]int, n)
	for li, l := range links {
		outLinks[l.s] = append(outLinks[l.s], li)
		inLinks[l.t] = append(inLinks[l.t], li)
	}
	for s := range outLinks {
		ls := outLinks[s]
		sort.SliceStable(ls, func(a, b int) bool {
			la, lb := links[ls[a]], links[ls[b]]
			if layer[la.t] != layer[lb.t] {
				return layer[la.t] < layer[lb.t]
			}
			return posInLayer[la.t] < posInLayer[lb.t]
		})
	}
	for t := range inLinks {
		lt := inLinks[t]
		sort.SliceStable(lt, func(a, b int) bool {
			la, lb := links[lt[a]], links[lt[b]]
			if layer[la.s] != layer[lb.s] {
				return layer[la.s] < layer[lb.s]
			}
			return posInLayer[la.s] < posInLayer[lb.s]
		})
	}
	srcY := make([]float64, len(links))
	tgtY := make([]float64, len(links))
	for s := range outLinks {
		oy := ny[s]
		for _, li := range outLinks[s] {
			srcY[li] = oy
			oy += links[li].v * scale
		}
	}
	for t := range inLinks {
		iy := ny[t]
		for _, li := range inLinks[t] {
			tgtY[li] = iy
			iy += links[li].v * scale
		}
	}

	var b strings.Builder
	b.WriteString(`<figure class="sankey">`)
	if d.Title != "" {
		b.WriteString(`<figcaption>` + esc(d.Title) + `</figcaption>`)
	}
	fmt.Fprintf(&b, `<svg class="sankey-svg" viewBox="0 0 %s 240" role="img" aria-label="%s">`, co(W), esc(d.Title))
	for li, l := range links { // ribbons under nodes
		h := l.v * scale
		sx := xOf(layer[l.s]) + nodeW
		tx := xOf(layer[l.t])
		sy0, ty0 := srcY[li], tgtY[li]
		mx := (sx + tx) / 2
		col := paletteColor(l.color, l.s) // default: the source node's palette color
		fmt.Fprintf(&b,
			`<path class="sankey-link" d="M%s %s C%s %s %s %s %s %s L%s %s C%s %s %s %s %s %s Z" style="fill:%s"><title>%s → %s: %s</title></path>`,
			co(sx), co(sy0), co(mx), co(sy0), co(mx), co(ty0), co(tx), co(ty0),
			co(tx), co(ty0+h), co(mx), co(ty0+h), co(mx), co(sy0+h), co(sx), co(sy0+h),
			col, esc(d.Nodes[l.s].Name), esc(d.Nodes[l.t].Name), esc(fmtUnit(d.Unit, l.v)))
	}
	for i := 0; i < n; i++ { // node bars + labels
		x := xOf(layer[i])
		col := paletteColor(d.Nodes[i].Color, i)
		fmt.Fprintf(&b, `<rect class="sankey-node" x="%s" y="%s" width="%s" height="%s" style="fill:%s"><title>%s: %s</title></rect>`,
			co(x), co(ny[i]), co(nodeW), co(nh[i]), col, esc(d.Nodes[i].Name), esc(fmtUnit(d.Unit, nodeVal[i])))
		cy := ny[i] + nh[i]/2 + 3
		switch layer[i] {
		case 0: // first stage: label to the left of the node
			fmt.Fprintf(&b, `<text class="sankey-lab" x="%s" y="%s" text-anchor="end">%s</text>`, co(x-5), co(cy), esc(d.Nodes[i].Name))
		case nLayers - 1: // last stage: label to the right
			fmt.Fprintf(&b, `<text class="sankey-lab" x="%s" y="%s" text-anchor="start">%s</text>`, co(x+nodeW+5), co(cy), esc(d.Nodes[i].Name))
		default: // middle stages: label above to avoid colliding with ribbons
			fmt.Fprintf(&b, `<text class="sankey-lab mid" x="%s" y="%s" text-anchor="middle">%s</text>`, co(x+nodeW/2), co(ny[i]-4), esc(d.Nodes[i].Name))
		}
	}
	b.WriteString(`</svg></figure>`)
	return b.String(), nil
}

// --- chord (symmetric coupling between entities) ---

type chordData struct {
	Title  string      `json:"title"`
	Labels []string    `json:"labels"`
	Matrix [][]float64 `json:"matrix"`
	Colors []string    `json:"colors"` // optional per-node colors
}

func colorAt(colors []string, i int) string {
	if i < len(colors) {
		return colors[i]
	}
	return ""
}

func renderChord(data []byte) (string, error) {
	var d chordData
	if err := json.Unmarshal(data, &d); err != nil {
		return "", fmt.Errorf("chord data: %w", err)
	}
	n := len(d.Labels)
	if n < 2 {
		return "", fmt.Errorf("chord: need >= 2 labels, got %d", n)
	}
	if len(d.Matrix) != n {
		return "", fmt.Errorf("chord: matrix has %d rows but there are %d labels", len(d.Matrix), n)
	}
	for i, row := range d.Matrix {
		if len(row) != n {
			return "", fmt.Errorf("chord: matrix row %d has %d cols, need %d", i, len(row), n)
		}
		for _, v := range row {
			if v < 0 {
				return "", fmt.Errorf("chord: matrix has a negative value")
			}
		}
	}
	// coupling is undirected: symmetrize to max(m[i][j], m[j][i]); ignore the diagonal
	sym := make([][]float64, n)
	total := make([]float64, n)
	grand := 0.0
	for i := 0; i < n; i++ {
		sym[i] = make([]float64, n)
		for j := 0; j < n; j++ {
			if i == j {
				continue
			}
			s := math.Max(d.Matrix[i][j], d.Matrix[j][i])
			sym[i][j] = s
			total[i] += s
		}
		grand += total[i]
	}
	if grand <= 0 {
		return "", fmt.Errorf("chord: matrix is all zero")
	}
	const cx, cy, rInner, rOuter = 130.0, 130.0, 96.0, 108.0
	gapDeg := 3.0
	avail := 360.0 - float64(n)*gapDeg
	if avail < 60 { // very many nodes: shrink the inter-arc gaps to keep arcs visible
		gapDeg = 1.0
		avail = 360.0 - float64(n)*gapDeg
	}
	arcStart := make([]float64, n)
	arcW := make([]float64, n)
	ang := 0.0
	for i := 0; i < n; i++ {
		w := total[i] / grand * avail
		arcStart[i], arcW[i] = ang, w
		ang += w + gapDeg
	}
	cursor := make([]float64, n) // sub-span allocator within each node's arc
	copy(cursor, arcStart)
	p := func(a float64) (float64, float64) { return svgPolar(cx, cy, rInner, a) }

	var b strings.Builder
	b.WriteString(`<figure class="chord">`)
	if d.Title != "" {
		b.WriteString(`<figcaption>` + esc(d.Title) + `</figcaption>`)
	}
	b.WriteString(`<div class="chord-body">`)
	b.WriteString(`<svg class="chord-svg" viewBox="0 0 260 260" role="img" aria-label="` + esc(d.Title) + `">`)
	// chords first (under the arcs); each unordered pair drawn once
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			v := sym[i][j]
			if v <= 0 {
				continue
			}
			wi := v / total[i] * arcW[i]
			ai0, ai1 := cursor[i], cursor[i]+wi
			cursor[i] = ai1
			wj := v / total[j] * arcW[j]
			aj0, aj1 := cursor[j], cursor[j]+wj
			cursor[j] = aj1
			x1, y1 := p(ai0)
			x2, y2 := p(ai1)
			x3, y3 := p(aj0)
			x4, y4 := p(aj1)
			col := paletteColor(colorAt(d.Colors, i), i)
			fmt.Fprintf(&b,
				`<path class="chord-rib" d="M%s %s A%s %s 0 0 1 %s %s Q%s %s %s %s A%s %s 0 0 1 %s %s Q%s %s %s %s Z" style="fill:%s"><title>%s ↔ %s: %s</title></path>`,
				co(x1), co(y1), co(rInner), co(rInner), co(x2), co(y2),
				co(cx), co(cy), co(x3), co(y3),
				co(rInner), co(rInner), co(x4), co(y4),
				co(cx), co(cy), co(x1), co(y1),
				col, esc(d.Labels[i]), esc(d.Labels[j]), fmtNum(v))
		}
	}
	// node arcs (annular band) + radial labels
	for i := 0; i < n; i++ {
		if arcW[i] <= 0 {
			continue
		}
		a0, a1 := arcStart[i], arcStart[i]+arcW[i]
		large := "0"
		if a1-a0 > 180 {
			large = "1"
		}
		ox0, oy0 := svgPolar(cx, cy, rOuter, a0)
		ox1, oy1 := svgPolar(cx, cy, rOuter, a1)
		ix1, iy1 := svgPolar(cx, cy, rInner, a1)
		ix0, iy0 := svgPolar(cx, cy, rInner, a0)
		col := paletteColor(colorAt(d.Colors, i), i)
		fmt.Fprintf(&b,
			`<path class="chord-arc" d="M%s %s A%s %s 0 %s 1 %s %s L%s %s A%s %s 0 %s 0 %s %s Z" style="fill:%s"><title>%s: %s</title></path>`,
			co(ox0), co(oy0), co(rOuter), co(rOuter), large, co(ox1), co(oy1),
			co(ix1), co(iy1), co(rInner), co(rInner), large, co(ix0), co(iy0),
			col, esc(d.Labels[i]), fmtNum(total[i]))
		mid := (a0 + a1) / 2
		lx, ly := svgPolar(cx, cy, rOuter+10, mid)
		anchor := "middle"
		if lx > cx+1 {
			anchor = "start"
		} else if lx < cx-1 {
			anchor = "end"
		}
		fmt.Fprintf(&b, `<text class="chord-lab" x="%s" y="%s" text-anchor="%s">%s</text>`, co(lx), co(ly+3), anchor, esc(d.Labels[i]))
	}
	b.WriteString(`</svg></div></figure>`)
	return b.String(), nil
}

// --- line chart (trend / time-series over a continuous axis, with an optional threshold) ---

type lineChartData struct {
	Title     string  `json:"title"`
	XLabel    string  `json:"x_label"`
	YLabel    string  `json:"y_label"`
	XMax      float64 `json:"x_max"` // optional; defaults to the observed max x (floor 1)
	YMax      float64 `json:"y_max"` // optional; defaults to the observed max y (floor 1)
	Threshold *struct {
		At    float64 `json:"at"`
		Label string  `json:"label"`
		Color string  `json:"color"`
	} `json:"threshold"`
	XTicks []struct {
		At    float64 `json:"at"`
		Label string  `json:"label"`
	} `json:"x_ticks"`
	YTicks []struct {
		At    float64 `json:"at"`
		Label string  `json:"label"`
	} `json:"y_ticks"`
	Series []struct {
		Label  string `json:"label"`
		Color  string `json:"color"`
		Marker bool   `json:"marker"`
		Points []struct {
			X    float64 `json:"x"`
			Y    float64 `json:"y"`
			Note string  `json:"note"`
		} `json:"points"`
	} `json:"series"`
}

// renderLineChart plots one or more series of (x,y) points on a shared 0-based axis
// pair, with an optional dashed threshold reference line and per-point notes. The
// agent supplies the points (a sawtooth/reset is just data); ox owns the axis
// scaling, the pixel projection, the threshold placement, and the legend. A
// per-series stroke dash is the redundant, color-independent channel (grayscale/CVD).
func renderLineChart(data []byte) (string, error) {
	var d lineChartData
	if err := json.Unmarshal(data, &d); err != nil {
		return "", fmt.Errorf("line-chart data: %w", err)
	}
	if len(d.Series) == 0 {
		return "", fmt.Errorf("line-chart: no series")
	}
	if len(d.Series) > 4 {
		return "", fmt.Errorf("line-chart: max 4 series for legibility, got %d", len(d.Series))
	}
	xMax, yMax := d.XMax, d.YMax
	for _, s := range d.Series {
		if len(s.Points) < 2 {
			return "", fmt.Errorf("line-chart: series %q needs >= 2 points to draw a line, got %d", s.Label, len(s.Points))
		}
		for _, p := range s.Points {
			if p.X < 0 || p.Y < 0 {
				return "", fmt.Errorf("line-chart: series %q has a negative coordinate (x/y must be >= 0)", s.Label)
			}
			if p.X > xMax {
				xMax = p.X
			}
			if p.Y > yMax {
				yMax = p.Y
			}
		}
	}
	if d.Threshold != nil && d.Threshold.At > yMax {
		yMax = d.Threshold.At
	}
	if xMax <= 0 {
		xMax = 1
	}
	if yMax <= 0 {
		yMax = 1
	}
	// plot area inside the viewBox, leaving room for axis ticks + labels
	const x0, y0, pw, ph = 52.0, 14.0, 236.0, 176.0
	px := func(x float64) float64 { return x0 + (x/xMax)*pw }
	py := func(y float64) float64 { return y0 + (1-y/yMax)*ph } // invert: up = high

	var b strings.Builder
	b.WriteString(`<figure class="linec">`)
	if d.Title != "" {
		b.WriteString(`<figcaption>` + esc(d.Title) + `</figcaption>`)
	}
	b.WriteString(`<svg class="linec-svg" viewBox="0 0 300 224" role="img" aria-label="` + esc(d.Title) + `">`)
	// axes (left + bottom)
	fmt.Fprintf(&b, `<line class="linec-axis" x1="%s" y1="%s" x2="%s" y2="%s"/>`, co(x0), co(y0), co(x0), co(y0+ph))
	fmt.Fprintf(&b, `<line class="linec-axis" x1="%s" y1="%s" x2="%s" y2="%s"/>`, co(x0), co(y0+ph), co(x0+pw), co(y0+ph))
	// y ticks + labels
	for _, t := range d.YTicks {
		yy := py(t.At)
		fmt.Fprintf(&b, `<line class="linec-tick" x1="%s" y1="%s" x2="%s" y2="%s"/>`, co(x0-3), co(yy), co(x0), co(yy))
		fmt.Fprintf(&b, `<text class="linec-tlab" x="%s" y="%s" text-anchor="end">%s</text>`, co(x0-6), co(yy+3), esc(t.Label))
	}
	// x ticks + labels
	for _, t := range d.XTicks {
		xx := px(t.At)
		fmt.Fprintf(&b, `<line class="linec-tick" x1="%s" y1="%s" x2="%s" y2="%s"/>`, co(xx), co(y0+ph), co(xx), co(y0+ph+3))
		fmt.Fprintf(&b, `<text class="linec-tlab" x="%s" y="%s" text-anchor="middle">%s</text>`, co(xx), co(y0+ph+14), esc(t.Label))
	}
	// threshold reference line (dashed) — the limit the series is measured against
	if d.Threshold != nil {
		ty := py(d.Threshold.At)
		col := colorVar(d.Threshold.Color)
		fmt.Fprintf(&b, `<line class="linec-thresh" x1="%s" y1="%s" x2="%s" y2="%s" style="stroke:%s"/>`, co(x0), co(ty), co(x0+pw), co(ty), col)
		if d.Threshold.Label != "" {
			fmt.Fprintf(&b, `<text class="linec-thlab" x="%s" y="%s" text-anchor="end" style="fill:%s">%s</text>`, co(x0+pw), co(ty-3), col, esc(d.Threshold.Label))
		}
	}
	// series polylines — distinct stroke dash per series so the regimes stay
	// distinguishable in grayscale / under color-vision deficiency
	dashes := []string{"", "5 3", "1.5 3", "6 2 1 2"}
	for si, s := range d.Series {
		col := paletteColor(s.Color, si)
		pts := make([]string, 0, len(s.Points))
		for _, p := range s.Points {
			pts = append(pts, co(px(p.X))+","+co(py(p.Y)))
		}
		dash := ""
		if dd := dashes[si%len(dashes)]; dd != "" {
			dash = ` stroke-dasharray="` + dd + `"`
		}
		fmt.Fprintf(&b, `<polyline class="linec-series" points="%s" style="stroke:%s"%s/>`, strings.Join(pts, " "), col, dash)
		for _, p := range s.Points {
			ptx, pty := px(p.X), py(p.Y)
			if s.Marker {
				fmt.Fprintf(&b, `<circle class="linec-dot" cx="%s" cy="%s" r="2.6" style="fill:%s"/>`, co(ptx), co(pty), col)
			}
			if strings.TrimSpace(p.Note) != "" {
				fmt.Fprintf(&b, `<text class="linec-note" x="%s" y="%s" text-anchor="middle" style="fill:%s">%s</text>`, co(ptx), co(pty-6), col, esc(p.Note))
			}
		}
	}
	// axis labels
	if d.XLabel != "" {
		fmt.Fprintf(&b, `<text class="linec-axl" x="%s" y="220" text-anchor="middle">%s</text>`, co(x0+pw/2), esc(d.XLabel))
	}
	if d.YLabel != "" {
		fmt.Fprintf(&b, `<text class="linec-axl" x="13" y="%s" text-anchor="middle" transform="rotate(-90 13 %s)">%s</text>`, co(y0+ph/2), co(y0+ph/2), esc(d.YLabel))
	}
	b.WriteString(`</svg>`)
	// legend — color-independent read (one entry per series)
	b.WriteString(`<ul class="linec-leg">`)
	for si, s := range d.Series {
		fmt.Fprintf(&b, `<li><span class="vsw" style="background:%s"></span>%s</li>`, paletteColor(s.Color, si), esc(s.Label))
	}
	b.WriteString(`</ul></figure>`)
	return b.String(), nil
}
