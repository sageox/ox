package plan

// autoviz.go — structure-driven auto-visualization for the markdown-path
// render. The scaffold ships swimlanes, stat cards and interactive primitives
// that a markdown author has no way to reach; these passes detect the plan
// STRUCTURES those primitives were built for and emit them deterministically,
// so a quick markdown plan still lands closer to the hand-built quality bar:
//   - a gated-track table (Track/Phase column + Gate column) gains a .swim
//     swimlane figure above it — the table stays as the detail record;
//   - a comparison/correspondence table (>=3 columns under a comparison
//     heading) becomes click-to-inspect: rows light up and a docked explainer
//     shows the row's fields (scaffold.js drives it; no external deps).
// Both passes are additive: the source table is never removed, and a plan
// with neither structure renders byte-identically.

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	tableBlockRe = regexp.MustCompile(`(?s)<table>.*?</table>`)
	headerCellRe = regexp.MustCompile(`(?s)<th[^>]*>(.*?)</th>`)
	rowRe        = regexp.MustCompile(`(?s)<tr>.*?</tr>`)
	cellRe       = regexp.MustCompile(`(?s)<td[^>]*>(.*?)</td>`)
	tagStripRe   = regexp.MustCompile(`<[^>]*>`)

	trackHeaderRe = regexp.MustCompile(`(?i)\b(track|phase|wave|workstream|stage)\b`)
	gateHeaderRe  = regexp.MustCompile(`(?i)\b(gate|gated|blocked|blocks|depends|after)\b`)
	compareHeadRe = regexp.MustCompile(`(?i)compar|correspond|\bvs\b|versus`)
)

// swimPalette cycles bar fills for auto-swimlanes. Dark label text (#0c1112,
// per the scaffold's .swim .bar) needs light fills — these are the catalog's
// own accent hexes.
var swimPalette = []string{"#99c693", "#e0a56a", "#14b8a6", "#a78bfa", "#64748b", "#f59e0b"}

// autoVisualize runs the structure-driven passes over one rendered section.
// heading is the section's H2 text (comparison detection keys off it).
func autoVisualize(sectionHTML, heading string) string {
	out := autoSwimlane(sectionHTML)
	if compareHeadRe.MatchString(heading) {
		out = autoInspector(out)
	}
	return out
}

// stripTags flattens a table-cell fragment to text.
func stripTags(s string) string {
	return strings.TrimSpace(tagStripRe.ReplaceAllString(s, ""))
}

// autoSwimlane finds the first table whose header carries BOTH a track-ish and
// a gate-ish column and prepends a .swim swimlane figure: one lane per row,
// bars staggered in row order (the table's order IS the sequence), a ◆ gate
// marker at each gated bar's end carrying the gate text as its tooltip. Only
// the first matching table converts — one hero visual per section, the rest
// stay tables.
func autoSwimlane(sectionHTML string) string {
	converted := false
	return tableBlockRe.ReplaceAllStringFunc(sectionHTML, func(table string) string {
		if converted {
			return table
		}
		headers := headerCellRe.FindAllStringSubmatch(table, -1)
		trackCol, gateCol := -1, -1
		for i, h := range headers {
			txt := stripTags(h[1])
			if trackCol < 0 && trackHeaderRe.MatchString(txt) {
				trackCol = i
			}
			if gateCol < 0 && gateHeaderRe.MatchString(txt) {
				gateCol = i
			}
		}
		if trackCol < 0 || gateCol < 0 || trackCol == gateCol {
			return table
		}
		var lanes [][2]string // [name, gate] per data row
		for _, row := range rowRe.FindAllString(table, -1) {
			cells := cellRe.FindAllStringSubmatch(row, -1)
			if len(cells) <= trackCol || len(cells) <= gateCol {
				continue // header row (th) or ragged row
			}
			lanes = append(lanes, [2]string{stripTags(cells[trackCol][1]), stripTags(cells[gateCol][1])})
		}
		if len(lanes) < 2 {
			return table // a single lane is not a timeline
		}
		converted = true
		return buildSwimlane(lanes) + table
	})
}

// buildSwimlane emits the scaffold's .swim markup: sequential bars (row order
// = execution order — the only ordering a gated-track table asserts), each
// gated lane carrying a ◆ marker at its bar end with the gate as tooltip.
func buildSwimlane(lanes [][2]string) string {
	n := len(lanes)
	// bars share a 2%..98% band; each occupies its sequential slot with a 1%
	// gutter so adjacent lanes read as ordered, not stacked.
	slot := 96.0 / float64(n)
	var b strings.Builder
	b.WriteString(`<figure class="swim auto-swim"><figcaption>Sequenced tracks (gates marked ◆ — hover for the gate)</figcaption>`)
	for i, l := range lanes {
		left := 2 + slot*float64(i)
		width := slot - 1
		color := swimPalette[i%len(swimPalette)]
		name := htmlEscape(l[0])
		b.WriteString(`<div class="lane"><div class="lane-name">` + name + `</div><div class="track">`)
		fmt.Fprintf(&b, `<span class="bar" style="left:%.1f%%;width:%.1f%%;background:%s">%s</span>`, left, width, color, name)
		if l[1] != "" {
			fmt.Fprintf(&b, `<span class="gate" style="left:%.1f%%" title="gate: %s">◆</span>`, left+width, htmlEscape(l[1]))
		}
		b.WriteString(`</div></div>`)
	}
	b.WriteString(`</figure>`)
	return b.String()
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;")
	return r.Replace(s)
}

// autoInspector upgrades comparison tables (>=3 columns, >=3 data rows) to the
// click-to-inspect register: the table gains class "inspect" and a docked
// explainer follows it; scaffold.js lights the clicked row and projects its
// cells (labeled by the header row) into the dock — the generatable core of
// the hand-built field-inspector experience.
func autoInspector(sectionHTML string) string {
	return tableBlockRe.ReplaceAllStringFunc(sectionHTML, func(table string) string {
		headers := headerCellRe.FindAllStringSubmatch(table, -1)
		if len(headers) < 3 {
			return table
		}
		dataRows := 0
		for _, row := range rowRe.FindAllString(table, -1) {
			if len(cellRe.FindAllStringSubmatch(row, -1)) >= 3 {
				dataRows++
			}
		}
		if dataRows < 3 {
			return table
		}
		out := strings.Replace(table, "<table>", `<table class="inspect">`, 1)
		return out + `<div class="inspect-dock" hidden><span class="inspect-dock-hint">Click a row to inspect the correspondence.</span><div class="inspect-dock-body"></div></div>`
	})
}
