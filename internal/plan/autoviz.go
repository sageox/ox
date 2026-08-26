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
	"sort"
	"strings"

	"github.com/sageox/ox/internal/viz"
)

var (
	tableBlockRe = regexp.MustCompile(`(?s)<table>.*?</table>`)
	// `<th(?:\s…)?>` — NOT `<th[^>]*>`, which also matches `<thead>` and
	// swallows everything up to the first real `</th>`. goldmark always wraps
	// header rows in <thead>, so the sloppier form corrupts header parsing on
	// every real-world table (the original reason table detectors misfired).
	headerCellRe = regexp.MustCompile(`(?s)<th(?:\s[^>]*)?>(.*?)</th>`)
	rowRe        = regexp.MustCompile(`(?s)<tr>.*?</tr>`)
	cellRe       = regexp.MustCompile(`(?s)<td(?:\s[^>]*)?>(.*?)</td>`)
	tagStripRe   = regexp.MustCompile(`<[^>]*>`)

	trackHeaderRe = regexp.MustCompile(`(?i)\b(track|phase|wave|workstream|stage)\b`)
	gateHeaderRe  = regexp.MustCompile(`(?i)\b(gate|gated|blocked|blocks|depends|after)\b`)
	compareHeadRe = regexp.MustCompile(`(?i)compar|correspond|\bvs\b|versus`)
)

// swimPalette cycles bar fills for auto-swimlanes. These are emitted as inline
// `background:` on each bar rather than as var(--x), so unlike the rest of the
// page they do NOT re-theme on the light/dark toggle — every entry therefore has
// to stay light enough for the dark label text (#111411, per the scaffold's
// .swim .bar) in both modes, which rules out the ramps' deep stops.
//
// Six separable hues drawn from the sageox-design ramps. They are deliberately
// NOT six stops of one hue: adjacent lanes must be told apart at a glance, and
// sage-400/sage-300 side by side cannot be. Ordered so the common 2-3-lane plan
// gets the most distinct openers.
var swimPalette = []string{
	"#99c693", // sage-400
	"#d9b654", // warning-400 — semantic warning only (DDR-010)
	"#97aebd", // info
	"#d77e6c", // error-400
	"#b7b6a3", // silt-300
	"#9b8bb4", // muted plum — series-only, no semantic meaning
}

// sectionViz carries the per-section context the structure-driven passes key
// off: the H2 heading, the prose-parsed track lanes (planfacts.go), whether
// the section is a risk section, and the detected bead-ref namespace.
type sectionViz struct {
	Heading string
	Lanes   []trackLane    // >=2 → the section gets the track swimlane
	IsRisk  bool           // risk-heading section → riskm table upgrade
	Beads   *regexp.Regexp // bead-map section → chip styling (nil = off)
}

// beadHeading flags a section that maps work items to beads.
var beadHeading = regexp.MustCompile(`(?i)\bbeads?\b`)

// autoVisualize runs the structure-driven passes over one rendered section.
func autoVisualize(sectionHTML string, v sectionViz) string {
	out := sectionHTML
	if len(v.Lanes) >= 2 {
		// tracks-as-H3-subsections: the swimlane leads the section; the prose
		// stays as the detail record. The table-shaped swimlane pass is skipped
		// so a section never draws two competing timelines.
		out = buildTrackSwim(v.Lanes) + out
	} else {
		out = autoSwimlane(out)
	}
	if v.IsRisk {
		out = autoRiskTable(out)
	}
	if v.Beads != nil && beadHeading.MatchString(v.Heading) {
		out = autoBeadChips(out, v.Beads)
	}
	if compareHeadRe.MatchString(v.Heading) {
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

// --- risk-matrix table upgrade ---

// severityColRe matches a header cell that already carries severity.
var severityColRe = regexp.MustCompile(`(?i)\bsever|priorit|impact`)

// Severity inference tiers for risk tables that ship no severity column (the
// common real-world shape: Risk | Mitigation). Keyed on the row's own wording;
// the rendered caption discloses that severity is inferred, not authored.
var (
	sevBlockerRe = regexp.MustCompile(`(?i)data loss|unrecoverable|irreversible|\blegal\b|compliance|corrupt`)
	sevHighRe    = regexp.MustCompile(`(?i)customer|migrat|stall|block|deadline|outage|security`)
	sevLowRe     = regexp.MustCompile(`(?i)cosmetic|docs-only|polish|nice[- ]to[- ]have`)
)

func inferSeverity(rowText string) string {
	switch {
	case sevBlockerRe.MatchString(rowText):
		return "blocker"
	case sevHighRe.MatchString(rowText):
		return "high"
	case sevLowRe.MatchString(rowText):
		return "low"
	default:
		return "medium"
	}
}

// canonSeverity normalizes an authored severity cell to the riskm vocabulary.
func canonSeverity(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "blocker", "critical", "crit":
		return "blocker"
	case "high", "severe":
		return "high"
	case "medium", "med", "moderate":
		return "medium"
	case "low", "minor":
		return "low"
	}
	return ""
}

// autoRiskTable upgrades the first table of a risk section to the riskm
// severity register: every row gains a glyph+word severity cell (column 2 — the
// scaffold's riskm styling keys on it), rows sort blocker-first, and the
// figcaption discloses when severity was inferred from wording rather than
// authored. In-place upgrade, not additive: the output carries every original
// cell, so keeping the source table too would only duplicate it.
func autoRiskTable(sectionHTML string) string {
	converted := false
	return tableBlockRe.ReplaceAllStringFunc(sectionHTML, func(table string) string {
		if converted {
			return table
		}
		headers := headerCellRe.FindAllStringSubmatch(table, -1)
		if len(headers) < 2 {
			return table
		}
		sevCol := -1
		for i, h := range headers {
			if severityColRe.MatchString(stripTags(h[1])) {
				sevCol = i
				break
			}
		}
		type riskRow struct {
			cells []string
			sev   string
		}
		var rows []riskRow
		for _, row := range rowRe.FindAllString(table, -1) {
			cells := cellRe.FindAllStringSubmatch(row, -1)
			if len(cells) < 2 {
				continue // header row (th) or ragged row
			}
			var inner []string
			for _, c := range cells {
				inner = append(inner, c[1])
			}
			sev := ""
			if sevCol >= 0 && sevCol < len(inner) {
				sev = canonSeverity(stripTags(inner[sevCol]))
			}
			if sev == "" {
				sev = inferSeverity(stripTags(strings.Join(inner, " ")))
			}
			rows = append(rows, riskRow{cells: inner, sev: sev})
		}
		if len(rows) < 2 {
			return table
		}
		converted = true
		sort.SliceStable(rows, func(i, j int) bool {
			return viz.SeverityRank(rows[i].sev) < viz.SeverityRank(rows[j].sev)
		})

		caption := "Risks — sorted by severity"
		if sevCol < 0 {
			caption += " (severity inferred from wording)"
		}
		var b strings.Builder
		b.WriteString(`<figure class="riskm-fig"><figcaption>` + caption + `</figcaption>`)
		b.WriteString(`<table class="riskm"><thead><tr><th>` + headers[0][1] + `</th><th>Severity</th>`)
		for i, h := range headers[1:] {
			if i+1 == sevCol {
				continue // severity moved to column 2
			}
			b.WriteString(`<th>` + h[1] + `</th>`)
		}
		b.WriteString(`</tr></thead><tbody>`)
		for _, r := range rows {
			// title-case the severity word beside its shape glyph (shape survives
			// grayscale/CVD — never color alone)
			word := strings.ToUpper(r.sev[:1]) + r.sev[1:]
			fmt.Fprintf(&b, `<tr class="sev-%s"><td>%s</td><td class="sev">%s%s</td>`, r.sev, r.cells[0], viz.SeverityGlyph(r.sev), word)
			for i, c := range r.cells[1:] {
				if i+1 == sevCol {
					continue
				}
				b.WriteString(`<td>` + c + `</td>`)
			}
			b.WriteString(`</tr>`)
		}
		b.WriteString(`</tbody></table></figure>`)
		return b.String()
	})
}

// --- bead-map chips ---

// autoBeadChips styles a bead-map section's work-item refs as chips: each
// namespaced `<code>` ref becomes an ox-chip, [HUMAN …] hold markers get the
// amber hold chip, and the section's table is tagged for the compact bead-map
// column styling. Purely presentational — no text is added or removed.
func autoBeadChips(sectionHTML string, beadRe *regexp.Regexp) string {
	out := beadRe.ReplaceAllString(sectionHTML, `<span class="ox-chip bead"><code>$1</code></span>`)
	out = humanMarkRe.ReplaceAllString(out, `<span class="ox-chip bead hold">$0</span>`)
	return strings.Replace(out, "<table>", `<table class="bead-map">`, 1)
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
