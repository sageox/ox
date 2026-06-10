package plan

import (
	"encoding/json"
	"fmt"
	"html"
	"path"
	"sort"
	"strconv"
	"strings"
)

// viz_render.go turns structured data into a self-contained HTML/SVG fragment for
// the data-shaped visualization patterns (bar charts, stat cards, file-impact
// trees, risk matrices, …).
//
// Why parameterize: hand-authoring SVG bar widths or grouped file trees inline is
// where agents produce broken/misaligned visuals — wrong percentages, unescaped
// labels, inconsistent class names. Computing the geometry deterministically in
// the binary (ADR-021: ox does the mechanical work, the agent supplies the data)
// makes these visuals correct and consistent every time, for every agent. The
// fragments use the scaffold's CSS classes so they theme with the page. Pure
// string building — no network, no LLM.

// vizRenderers maps a catalog pattern id to its parameterized renderer. It is
// the single source of which patterns support `ox plan viz render --data`; the
// catalog↔renderer drift test enumerates it against the `param:` entries in
// viz-catalog.md so the two can't diverge. Adding a parameterized pattern = one
// entry here + the catalog `param:` line + the CSS.
var vizRenderers = map[string]func([]byte) (string, error){
	"bar-chart":           renderBarChart,
	"cost-waterfall":      renderCostWaterfall,
	"stat-cards":          renderStatCards,
	"file-impact-map":     renderFileImpact,
	"risk-matrix":         renderRiskMatrix,
	"flag-rollout-matrix": renderFlagMatrix,
}

// RenderViz renders one parameterized pattern from its JSON data into an HTML
// fragment. Returns an error for an unknown pattern or malformed data so the
// command layer can show an actionable message.
func RenderViz(pattern string, data []byte) (string, error) {
	r, ok := vizRenderers[strings.ToLower(strings.TrimSpace(pattern))]
	if !ok {
		return "", fmt.Errorf("pattern %q does not support --data rendering; run `ox plan viz` to see which patterns are parameterized", pattern)
	}
	return r(data)
}

// vizColors is the whitelist of semantic color names a caller may reference;
// anything else falls back to sage so a typo can't inject arbitrary CSS.
var vizColors = map[string]string{
	"sage": "--sage", "copper": "--copper", "amber": "--amber",
	"red": "--red", "teal": "--teal",
}

func colorVar(name string) string {
	if v, ok := vizColors[strings.ToLower(strings.TrimSpace(name))]; ok {
		return "var(" + v + ")"
	}
	return "var(--sage)"
}

func esc(s string) string { return html.EscapeString(s) }

// fmtNum trims trailing zeros from a float for compact display.
func fmtNum(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// fmtUnit places a symbol unit before the number ($12) and a word unit after
// (12 ms).
func fmtUnit(unit string, f float64) string {
	n := fmtNum(f)
	u := strings.TrimSpace(unit)
	if u == "" {
		return n
	}
	if len(u) <= 1 || strings.ContainsAny(u[:1], "$€£¥") {
		return u + n
	}
	return n + " " + u
}

// --- bar chart / cost waterfall ---

type barChartData struct {
	Title string `json:"title"`
	Unit  string `json:"unit"`
	Bars  []struct {
		Label string  `json:"label"`
		Value float64 `json:"value"`
		Color string  `json:"color"`
	} `json:"bars"`
}

func renderBarChart(data []byte) (string, error) {
	var d barChartData
	if err := json.Unmarshal(data, &d); err != nil {
		return "", fmt.Errorf("bar-chart data: %w", err)
	}
	if len(d.Bars) == 0 {
		return "", fmt.Errorf("bar-chart: no bars")
	}
	max := 0.0
	for _, b := range d.Bars {
		if b.Value < 0 {
			// a negative value would compute a negative width:% and render blank;
			// fail loud so the data gets fixed rather than silently disappearing.
			return "", fmt.Errorf("bar-chart: %q has a negative value %s (values must be >= 0)", b.Label, fmtNum(b.Value))
		}
		if b.Value > max {
			max = b.Value
		}
	}
	if max <= 0 {
		max = 1
	}
	var b strings.Builder
	b.WriteString(`<figure class="barc">`)
	if d.Title != "" {
		b.WriteString(`<figcaption>` + esc(d.Title) + `</figcaption>`)
	}
	for _, bar := range d.Bars {
		pct := bar.Value / max * 100
		fmt.Fprintf(&b,
			`<div class="bar-row"><span class="bl">%s</span><span class="bt"><span class="bf" style="width:%.1f%%;background:%s"></span></span><span class="bv">%s</span></div>`,
			esc(bar.Label), pct, colorVar(bar.Color), esc(fmtUnit(d.Unit, bar.Value)))
	}
	b.WriteString(`</figure>`)
	return b.String(), nil
}

// cost-waterfall is a bar chart with a running-total caption; it reuses the bar
// renderer over the {unit,items[{name,value}]} shape.
func renderCostWaterfall(data []byte) (string, error) {
	var d struct {
		Unit  string `json:"unit"`
		Items []struct {
			Name  string  `json:"name"`
			Value float64 `json:"value"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return "", fmt.Errorf("cost-waterfall data: %w", err)
	}
	if len(d.Items) == 0 {
		return "", fmt.Errorf("cost-waterfall: no items")
	}
	var bc barChartData
	bc.Unit = d.Unit
	total := 0.0
	// One color for the whole series: length is the channel here, not hue. A
	// rainbow would fight the "which item costs most" read (perceptual review).
	for _, it := range d.Items {
		total += it.Value
		bc.Bars = append(bc.Bars, struct {
			Label string  `json:"label"`
			Value float64 `json:"value"`
			Color string  `json:"color"`
		}{Label: it.Name, Value: it.Value, Color: "copper"})
	}
	bc.Title = "Total " + fmtUnit(d.Unit, total)
	bcBytes, _ := json.Marshal(bc)
	return renderBarChart(bcBytes)
}

// --- stat cards ---

func renderStatCards(data []byte) (string, error) {
	var d struct {
		Cards []struct {
			Label  string `json:"label"`
			Value  string `json:"value"`
			Delta  string `json:"delta"`
			Trend  string `json:"trend"`  // up|down|flat
			Intent string `json:"intent"` // good|bad|warn|neutral
		} `json:"cards"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return "", fmt.Errorf("stat-cards data: %w", err)
	}
	if len(d.Cards) == 0 {
		return "", fmt.Errorf("stat-cards: no cards")
	}
	arrow := map[string]string{"up": "▲", "down": "▼", "flat": "◆"}
	intentClass := map[string]string{"good": "good", "bad": "bad", "warn": "warn", "neutral": "neutral"}
	var b strings.Builder
	b.WriteString(`<div class="statrow">`)
	for _, c := range d.Cards {
		cls := intentClass[c.Intent]
		if cls == "" {
			cls = "neutral"
		}
		fmt.Fprintf(&b, `<div class="stat %s"><div class="sv">%s</div><div class="sl">%s</div>`, cls, esc(c.Value), esc(c.Label))
		if c.Delta != "" {
			fmt.Fprintf(&b, `<div class="sd">%s %s</div>`, arrow[c.Trend], esc(c.Delta))
		}
		b.WriteString(`</div>`)
	}
	b.WriteString(`</div>`)
	return b.String(), nil
}

// --- file impact map ---

func renderFileImpact(data []byte) (string, error) {
	var d struct {
		Files []struct {
			Path   string `json:"path"`
			Change string `json:"change"` // new|edit|delete|read
			Scope  string `json:"scope"`  // sm|md|lg
			Note   string `json:"note"`
		} `json:"files"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return "", fmt.Errorf("file-impact-map data: %w", err)
	}
	if len(d.Files) == 0 {
		return "", fmt.Errorf("file-impact-map: no files")
	}
	// group by directory
	type fentry struct{ name, change, scope, note string }
	groups := map[string][]fentry{}
	for _, f := range d.Files {
		dir := path.Dir(f.Path)
		if dir == "." {
			dir = "(root)"
		}
		groups[dir] = append(groups[dir], fentry{path.Base(f.Path), f.Change, f.Scope, f.Note})
	}
	dirs := make([]string, 0, len(groups))
	for dir := range groups {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)

	changeClass := map[string]string{"new": "new", "edit": "edit", "delete": "del", "read": "read"}
	var b strings.Builder
	b.WriteString(`<ul class="ftree">`)
	for _, dir := range dirs {
		files := groups[dir]
		sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
		fmt.Fprintf(&b, `<li class="grp">%s<ul>`, esc(dir))
		for _, f := range files {
			cc := changeClass[strings.ToLower(f.change)]
			if cc == "" {
				cc = "edit"
			}
			fmt.Fprintf(&b, `<li><span class="chg %s">%s</span> %s`, cc, esc(f.change), esc(f.name))
			if f.scope != "" {
				fmt.Fprintf(&b, ` <span class="sc %s">%s</span>`, esc(strings.ToLower(f.scope)), esc(f.scope))
			}
			if f.note != "" {
				fmt.Fprintf(&b, ` <span class="fnote">%s</span>`, esc(f.note))
			}
			b.WriteString(`</li>`)
		}
		b.WriteString(`</ul></li>`)
	}
	b.WriteString(`</ul>`)
	return b.String(), nil
}

// --- risk matrix ---

var severityRank = map[string]int{"blocker": 0, "high": 1, "medium": 2, "low": 3}

// severityGlyph gives each severity a distinct SHAPE so the ranking survives
// grayscale + color-vision deficiency (redundant encoding, not color alone).
var severityGlyph = map[string]string{"blocker": "■ ", "high": "▲ ", "medium": "◆ ", "low": "· "}

func renderRiskMatrix(data []byte) (string, error) {
	var d struct {
		Risks []struct {
			Title      string `json:"title"`
			Severity   string `json:"severity"`
			Category   string `json:"category"`
			Mitigation string `json:"mitigation"`
		} `json:"risks"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return "", fmt.Errorf("risk-matrix data: %w", err)
	}
	if len(d.Risks) == 0 {
		return "", fmt.Errorf("risk-matrix: no risks")
	}
	// sort by severity (blocker first), stable on input order within a tier
	sort.SliceStable(d.Risks, func(i, j int) bool {
		return rank(d.Risks[i].Severity) < rank(d.Risks[j].Severity)
	})
	var b strings.Builder
	b.WriteString(`<table class="riskm"><tr><th>Risk</th><th>Severity</th><th>Category</th><th>Mitigation</th></tr>`)
	for _, r := range d.Risks {
		sev := strings.ToLower(strings.TrimSpace(r.Severity))
		fmt.Fprintf(&b, `<tr class="sev-%s"><td>%s</td><td>%s%s</td><td>%s</td><td>%s</td></tr>`,
			esc(sev), esc(r.Title), severityGlyph[sev], esc(r.Severity), esc(r.Category), esc(r.Mitigation))
	}
	b.WriteString(`</table>`)
	return b.String(), nil
}

func rank(sev string) int {
	if r, ok := severityRank[strings.ToLower(strings.TrimSpace(sev))]; ok {
		return r
	}
	return 99
}

// --- feature-flag rollout matrix ---

func renderFlagMatrix(data []byte) (string, error) {
	var d struct {
		Envs   []string                     `json:"envs"`
		Stages []string                     `json:"stages"`
		Cells  map[string]map[string]string `json:"cells"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return "", fmt.Errorf("flag-rollout-matrix data: %w", err)
	}
	if len(d.Envs) == 0 || len(d.Stages) == 0 {
		return "", fmt.Errorf("flag-rollout-matrix: need envs and stages")
	}
	var b strings.Builder
	b.WriteString(`<table class="flagm"><tr><th>env</th>`)
	for _, s := range d.Stages {
		b.WriteString(`<th>` + esc(s) + `</th>`)
	}
	b.WriteString(`</tr>`)
	for _, env := range d.Envs {
		b.WriteString(`<tr><td>` + esc(env) + `</td>`)
		for _, s := range d.Stages {
			b.WriteString(`<td>` + esc(d.Cells[env][s]) + `</td>`)
		}
		b.WriteString(`</tr>`)
	}
	b.WriteString(`</table>`)
	return b.String(), nil
}
