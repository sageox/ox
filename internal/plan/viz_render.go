package plan

import (
	"encoding/json"
	"fmt"
	"html"
	"math"
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
	"partition-bar":       renderPartitionBar,
	"partition-map":       renderPartitionMap,
	"donut":               renderDonut,
	"radar":               renderRadar,
	"quadrant":            renderQuadrant,
	"treemap":             renderTreemap,
	"sankey":              renderSankey,
	"chord":               renderChord,
}

// RenderViz renders one parameterized pattern from its JSON data into an HTML
// fragment. Returns an error for an unknown pattern or malformed data so the
// command layer can show an actionable message.
//
// On a render failure (usually a JSON-shape mismatch), the error echoes the
// pattern's expected `param:` shape from the catalog so the caller — typically an
// AI coworker driving ox from inside another agent — can self-correct in one shot
// instead of guessing the schema. (Goose's Auto Visualiser, which has the agent
// supply the full chart spec, documented exactly this failure mode with no shape
// hint to recover from; ox supplies data only, and names the shape on a miss.)
func RenderViz(pattern string, data []byte) (string, error) {
	id := strings.ToLower(strings.TrimSpace(pattern))
	r, ok := vizRenderers[id]
	if !ok {
		return "", fmt.Errorf("pattern %q does not support --data rendering; run `ox plan viz` to see which patterns are parameterized", pattern)
	}
	out, err := r(data)
	if err != nil {
		if p, ok := VizPatternByID(id); ok && p.Param != "" {
			return "", fmt.Errorf("%w\n  expected shape: %s", err, p.Param)
		}
		return "", err
	}
	return out, nil
}

// vizColors is the whitelist of semantic color names a caller may reference;
// anything else falls back to sage so a typo can't inject arbitrary CSS.
var vizColors = map[string]string{
	"sage": "--sage", "copper": "--copper", "amber": "--amber",
	"red": "--red", "teal": "--teal", "slate": "--slate", "violet": "--violet",
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

// --- partition maps (two variations of the disk/flash partition idiom) ---
//
// Chosen by WHAT the data is and HOW MANY partitions:
//   partition-bar : few partitions (<=8), SHARE is the story — a 100% stacked
//                   proportional bar + a detail table. Linear, honest about size.
//   partition-map : many partitions / a full address-space layout where ORDER and
//                   per-row annotation matter — vertical rows, offset-ordered, with
//                   a LOG-scaled size rail so 4 KB partitions stay visible next to
//                   6 MB ones (true linear would render the small ones <1px:
//                   dishonest by omission). The rail is labeled "log scale" so no
//                   false linear proportion is implied — use partition-bar for that.
// Both share the {title,total,unit,partitions[]} shape so the same data renders
// either way.

// pbarLabelMinPct is the narrowest slice (% of total) that can still show its
// label legibly inside the proportional bar. Below it the label is dropped — the
// identity stays reachable via the hover tooltip and the paired detail table — so
// a sub-1% slice doesn't render an unreadable clipped nub over its sliver.
const pbarLabelMinPct = 7.0

type partitionSeg struct {
	Label    string  `json:"label"`
	Size     float64 `json:"size"`
	Offset   string  `json:"offset"`   // e.g. "0x20000"; optional
	Color    string  `json:"color"`    // category color (semantic whitelist)
	Flag     string  `json:"flag"`     // small badge, e.g. "SIGNED" / "ENC"
	Note     string  `json:"note"`     // one-line annotation
	Group    string  `json:"group"`    // optional section label; a new value emits a divider row (partition-map only)
	Proposed bool    `json:"proposed"` // dashed/muted, uncommitted
}

type partitionData struct {
	Title      string         `json:"title"`
	Total      float64        `json:"total"` // optional; defaults to sum of sizes
	Unit       string         `json:"unit"`
	Partitions []partitionSeg `json:"partitions"`
}

func partitionParse(data []byte, who string) (partitionData, float64, error) {
	var d partitionData
	if err := json.Unmarshal(data, &d); err != nil {
		return d, 0, fmt.Errorf("%s data: %w", who, err)
	}
	if len(d.Partitions) == 0 {
		return d, 0, fmt.Errorf("%s: no partitions", who)
	}
	sum := 0.0
	for _, p := range d.Partitions {
		if p.Size < 0 {
			return d, 0, fmt.Errorf("%s: %q has a negative size %s (must be >= 0)", who, p.Label, fmtNum(p.Size))
		}
		sum += p.Size
	}
	total := d.Total
	if total <= 0 {
		total = sum
	}
	return d, total, nil
}

func pflag(f string) string {
	if strings.TrimSpace(f) == "" {
		return ""
	}
	return `<span class="pm-flag">` + esc(f) + `</span>`
}

func pnote(n string) string {
	if strings.TrimSpace(n) == "" {
		return ""
	}
	return `<small>` + esc(n) + `</small>`
}

// ptip builds the pure-CSS hover tooltip shown over a segment/row: the richer
// detail the compact view can't always fit (offset, exact size + share, flag,
// the full note even when the row ellipsizes it).
func ptip(p partitionSeg, pct float64, unit string) string {
	var s strings.Builder
	s.WriteString(`<span class="pm-tip"><b>` + esc(p.Label) + `</b>`)
	if strings.TrimSpace(p.Offset) != "" {
		s.WriteString(`<span class="pm-tip-k">@ ` + esc(p.Offset) + `</span>`)
	}
	fmt.Fprintf(&s, `<span class="pm-tip-k">%s · %.1f%%</span>`, esc(fmtUnit(unit, p.Size)), pct)
	if strings.TrimSpace(p.Flag) != "" {
		s.WriteString(`<span class="pm-tip-flag">` + esc(p.Flag) + `</span>`)
	}
	if p.Proposed {
		s.WriteString(`<span class="pm-tip-flag prop">PROPOSED</span>`)
	}
	if strings.TrimSpace(p.Note) != "" {
		s.WriteString(`<span class="pm-tip-note">` + esc(p.Note) + `</span>`)
	}
	s.WriteString(`</span>`)
	return s.String()
}

func renderPartitionBar(data []byte) (string, error) {
	d, total, err := partitionParse(data, "partition-bar")
	if err != nil {
		return "", err
	}
	if total <= 0 {
		total = 1
	}
	var b strings.Builder
	b.WriteString(`<figure class="pbar-fig">`)
	if d.Title != "" {
		b.WriteString(`<figcaption>` + esc(d.Title) + `</figcaption>`)
	}
	b.WriteString(`<div class="pbar">`)
	for i, p := range d.Partitions {
		pct := p.Size / total * 100
		lbl := ""
		if pct >= pbarLabelMinPct {
			lbl = `<span class="pseg-lbl">` + esc(p.Label) + `</span>`
		}
		fmt.Fprintf(&b,
			`<span class="pseg" style="--i:%d;width:%.3f%%;background:%s">%s%s</span>`,
			i, pct, colorVar(p.Color), lbl, ptip(p, pct, d.Unit))
	}
	b.WriteString(`</div>`)
	b.WriteString(`<table class="pbar-tab"><tr><th>partition</th><th>offset</th><th>size</th><th>share</th></tr>`)
	for _, p := range d.Partitions {
		pct := p.Size / total * 100
		off := p.Offset
		if off == "" {
			off = "—"
		}
		fmt.Fprintf(&b,
			`<tr><td><span class="pm-dot" style="background:%s"></span>%s%s</td><td class="pm-mono">%s</td><td class="pm-mono">%s</td><td class="pm-mono">%.2f%%</td></tr>`,
			colorVar(p.Color), esc(p.Label), pflag(p.Flag), esc(off), esc(fmtUnit(d.Unit, p.Size)), pct)
	}
	b.WriteString(`</table></figure>`)
	return b.String(), nil
}

func renderPartitionMap(data []byte) (string, error) {
	d, total, err := partitionParse(data, "partition-map")
	if err != nil {
		return "", err
	}
	if total <= 0 {
		total = 1
	}
	// Log scale for the size rail across the non-zero sizes.
	lmin, lmax := math.MaxFloat64, -math.MaxFloat64
	for _, p := range d.Partitions {
		if p.Size <= 0 {
			continue
		}
		l := math.Log10(p.Size)
		if l < lmin {
			lmin = l
		}
		if l > lmax {
			lmax = l
		}
	}
	railPct := func(sz float64) float64 {
		// sz<=0 has no log; lmax<=lmin means a single partition (or all sizes
		// equal) — there's nothing to differentiate, so every rail floors at 10%.
		if sz <= 0 || lmax <= lmin {
			return 10
		}
		// map log(size) into 10..100% so the smallest partition is still visible
		return (math.Log10(sz)-lmin)/(lmax-lmin)*90 + 10
	}
	var b strings.Builder
	// Always emit the figcaption: the "log scale" label is a data-honesty
	// disclosure (no false linear proportion) and must show even without a title.
	b.WriteString(`<figure class="pmapv"><figcaption>`)
	if d.Title != "" {
		b.WriteString(esc(d.Title) + ` `)
	}
	b.WriteString(`<span class="pmapv-rk">size · log scale</span></figcaption>`)
	prevGroup := ""
	for i, p := range d.Partitions {
		// a new section label interleaves a full-width divider before its first
		// row (e.g. committed rows, then a "PROPOSED ADDITIONS" group).
		if g := strings.TrimSpace(p.Group); g != prevGroup {
			if g != "" {
				b.WriteString(`<div class="pmapv-group">` + esc(g) + `</div>`)
			}
			prevGroup = g
		}
		cls := "pmapv-row"
		if p.Proposed {
			cls += " proposed"
		}
		off := p.Offset
		if off == "" {
			off = "TBD"
		}
		pct := p.Size / total * 100
		fmt.Fprintf(&b,
			`<div class="%s" style="--i:%d"><span class="pm-dot" style="background:%s"></span><span class="pmapv-off pm-mono">%s</span><span class="pmapv-nm"><b>%s</b>%s%s</span><span class="pmapv-rail"><i style="width:%.1f%%;background:%s"></i></span><span class="pmapv-sz pm-mono">%s</span>%s</div>`,
			cls, i, colorVar(p.Color), esc(off), esc(p.Label), pflag(p.Flag), pnote(p.Note), railPct(p.Size), colorVar(p.Color), esc(fmtUnit(d.Unit, p.Size)), ptip(p, pct, d.Unit))
	}
	b.WriteString(`</figure>`)
	return b.String(), nil
}
