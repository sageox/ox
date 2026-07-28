package plan

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// planfacts.go — markdown-level structure parsing for the render's
// structure-keyed primitives. Real plans express tracks as H3 subsections with
// bold "**Gate:**" prose lines (not tables), mark shipped work in a status
// preamble ("A1 + A2 + B* are SHIPPED"), and list beads in a "Bead map"
// section. The table-shaped detectors in autoviz.go never see any of that, so
// this file parses those PROSE shapes deterministically from the plan source
// and feeds the swimlane, hero stat chips, and bead-chip emitters.

// trackItem is one numbered, bold-labeled step inside a track subsection
// ("1. **A1 — the contract.** …").
type trackItem struct {
	ID      string // "A1"
	Label   string // "the contract" (may be empty)
	Shipped bool
}

// trackLane is one H3 track/phase/wave subsection.
type trackLane struct {
	Name    string // short lane name, e.g. "Track A"
	Full    string // full heading text (tooltip)
	Items   []trackItem
	Gate    string // gate paragraph text ("" = no gate)
	Shipped bool   // whole lane shipped (all items, or a "B*" marker)
}

// planFacts aggregates the structure-derived numbers the hero chips show.
type planFacts struct {
	Tracks  []trackLane
	Gates   int
	Shipped int // shipped items (a shipped item-less lane counts 1)
	Holds   int // distinct [HUMAN]-flagged work items
	Risks   int // data rows across risk-section tables
	shipped shippedSet
	beadRe  *regexp.Regexp // non-nil when a bead namespace was detected
}

// shippedSet is the doc-level set of shipped tokens: "A1", "B*" (whole track).
type shippedSet map[string]bool

var (
	// "### Track A — Converge the Turn *(…)*" — the H3 track/phase heading.
	// The short name is the keyword + its identifier; the rest is descriptive.
	trackH3Re = regexp.MustCompile(`(?mi)^###\s+((track|phase|wave|stage|workstream)\s+([A-Za-z0-9]+))\b[^\n]*$`)
	// any H3/H2 boundary, to bound a track's body.
	headingRe = regexp.MustCompile(`(?m)^#{2,3}\s`)
	// "1. **A1 — the contract.** …" — a numbered, bold-ID'd track item.
	trackItemRe = regexp.MustCompile(`(?m)^\s*\d+\.\s+\*\*([A-Z]{1,3}\d+)\b\s*(?:[—–-]+\s*([^*.\n]+))?`)
	// "**Gate:** A1 merged before A2…" — a bold gate line opening a paragraph.
	gateLineRe = regexp.MustCompile(`(?m)^\*\*Gate:?\*\*:?\s*(.+)$`)
	// "A1 + A2 + B* are SHIPPED" — the status-preamble shipped declaration.
	// SHIPPED must be all-caps: lowercase "shipped" appears in ordinary prose
	// ("spec-vs-shipped gap") and would poison the set.
	shippedDeclRe = regexp.MustCompile(`\b([A-Z]\d*\*?(?:\s*\+\s*[A-Z]\d*\*?)*)\s+(?:are|is|:)\s+SHIPPED\b`)
	shippedTokRe  = regexp.MustCompile(`[A-Z]\d*\*?`)
	// a backticked namespaced work-item ref, e.g. `sageox-nrqu9`.
	beadTokenRe = regexp.MustCompile("`([a-z][a-z0-9]*)-([a-z0-9]{3,8})`")
	// a [HUMAN …] hold marker.
	humanMarkRe = regexp.MustCompile(`\[HUMAN[^\]<]*\]`)
	// markdown table plumbing (for risk-row counting).
	tableRowLineRe = regexp.MustCompile(`(?m)^\|.*\|?\s*$`)
	tableSepLineRe = regexp.MustCompile(`(?m)^\|(?:\s*:?-+:?\s*\|)+\s*$`)
)

// parseShippedSet scans the whole plan for shipped declarations and returns
// the token set, with "B*" kept as a whole-track wildcard.
func parseShippedSet(raw string) shippedSet {
	set := shippedSet{}
	for _, m := range shippedDeclRe.FindAllStringSubmatch(raw, -1) {
		for _, tok := range shippedTokRe.FindAllString(m[1], -1) {
			set[tok] = true
		}
	}
	return set
}

// laneShipped reports whether the whole lane is declared shipped ("B*").
func (s shippedSet) laneShipped(lane string) bool { return s[lane+"*"] }

// parseTracks extracts the track lanes from one markdown fragment (a section
// body). Returns nil when fewer than two lanes exist — a single lane is not a
// swimlane.
func parseTracks(md string, shipped shippedSet) []trackLane {
	locs := trackH3Re.FindAllStringSubmatchIndex(md, -1)
	if len(locs) < 2 {
		return nil
	}
	var lanes []trackLane
	for i, loc := range locs {
		full := strings.TrimSpace(md[loc[0]:loc[1]])
		full = strings.TrimLeft(full, "# ")
		name := md[loc[2]:loc[3]]
		letter := md[loc[6]:loc[7]]
		bodyStart := loc[1]
		bodyEnd := len(md)
		if i+1 < len(locs) {
			bodyEnd = locs[i+1][0]
		} else if next := headingRe.FindStringIndex(md[bodyStart:]); next != nil {
			// bound the last track at the next heading of any level
			bodyEnd = bodyStart + next[0]
		}
		body := md[bodyStart:bodyEnd]

		lane := trackLane{Name: name, Full: full, Shipped: shipped.laneShipped(letter)}
		for _, im := range trackItemRe.FindAllStringSubmatch(body, -1) {
			it := trackItem{ID: im[1], Label: strings.TrimSpace(im[2])}
			it.Shipped = lane.Shipped || shipped[it.ID]
			lane.Items = append(lane.Items, it)
		}
		if gm := gateLineRe.FindStringSubmatchIndex(body); gm != nil {
			lane.Gate = gateParagraph(body[gm[2]:])
		}
		if len(lane.Items) > 0 && !lane.Shipped {
			all := true
			for _, it := range lane.Items {
				if !it.Shipped {
					all = false
					break
				}
			}
			lane.Shipped = all
		}
		lanes = append(lanes, lane)
	}
	return lanes
}

// gateParagraph joins a gate line with its markdown continuation lines (until a
// blank line or a new block) into one tooltip-sized string.
func gateParagraph(fromGateText string) string {
	lines := strings.Split(fromGateText, "\n")
	var out []string
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if i > 0 && (t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "|") || strings.HasPrefix(t, "-")) {
			break
		}
		out = append(out, t)
	}
	return strings.TrimSpace(mdInlineStrip(strings.Join(out, " ")))
}

// mdInlineStrip removes the inline markers that would read as noise in a
// tooltip/label (**bold**, `code`, *em*).
func mdInlineStrip(s string) string {
	return strings.NewReplacer("**", "", "`", "", "*", "").Replace(s)
}

// detectBeadNamespaces finds work-item ref namespaces: a backticked
// prefix-suffix token family where one prefix carries at least three distinct
// suffixes (`sageox-nrqu9`, `sageox-6f288`, …). One-off hyphenated code spans
// (`git-file`) never reach the threshold, so no hardcoded prefix list is
// needed. Returns a regexp matching a rendered `<code>` bead ref, or nil.
func detectBeadNamespaces(raw string) *regexp.Regexp {
	suffixes := map[string]map[string]bool{}
	for _, m := range beadTokenRe.FindAllStringSubmatch(raw, -1) {
		if suffixes[m[1]] == nil {
			suffixes[m[1]] = map[string]bool{}
		}
		suffixes[m[1]][m[2]] = true
	}
	var prefixes []string
	for p, s := range suffixes {
		if len(s) >= 3 {
			prefixes = append(prefixes, regexp.QuoteMeta(p))
		}
	}
	if len(prefixes) == 0 {
		return nil
	}
	sort.Strings(prefixes)
	return regexp.MustCompile(`<code>((?:` + strings.Join(prefixes, "|") + `)-[a-z0-9]{3,8})</code>`)
}

// countHolds counts distinct human-gated work items. A [HUMAN …] marker
// attributes to the NEAREST bead ref preceding it on the same line (a bead-map
// row lists several beads but the marker gates only the one it follows), and
// the same bead held in two places (bead map + risk table) is ONE hold. A
// [HUMAN] marker with no preceding bead counts once.
func countHolds(raw string) int {
	beads := map[string]bool{}
	bare := 0
	for _, ln := range strings.Split(raw, "\n") {
		for _, hm := range humanMarkRe.FindAllStringIndex(ln, -1) {
			owner := ""
			for _, rm := range beadTokenRe.FindAllStringSubmatchIndex(ln, -1) {
				if rm[1] <= hm[0] {
					owner = ln[rm[2]:rm[3]] + "-" + ln[rm[4]:rm[5]]
				}
			}
			if owner == "" {
				bare++
				continue
			}
			beads[owner] = true
		}
	}
	return len(beads) + bare
}

// countRiskRows counts data rows in the markdown tables of risk-heading
// sections: pipe rows minus a header + separator per table.
func countRiskRows(in Input) int {
	n := 0
	for _, s := range in.Sections {
		if !riskHeading.MatchString(s.Heading) {
			continue
		}
		rows := len(tableRowLineRe.FindAllString(s.Body, -1))
		tables := len(tableSepLineRe.FindAllString(s.Body, -1))
		if rows > 2*tables {
			n += rows - 2*tables
		}
	}
	return n
}

// parsePlanFacts derives the doc-level structure facts for the hero chips and
// the per-section visualization passes.
func parsePlanFacts(in Input) planFacts {
	f := planFacts{shipped: parseShippedSet(in.Raw)}
	f.Tracks = parseTracks(in.Raw, f.shipped)
	f.Gates = len(gateLineRe.FindAllString(in.Raw, -1))
	for _, lane := range f.Tracks {
		if len(lane.Items) == 0 {
			if lane.Shipped {
				f.Shipped++
			}
			continue
		}
		for _, it := range lane.Items {
			if it.Shipped {
				f.Shipped++
			}
		}
	}
	f.Holds = countHolds(in.Raw)
	f.Risks = countRiskRows(in)
	f.beadRe = detectBeadNamespaces(in.Raw)
	return f
}

// structureStats projects the facts into hero chips, using the fixed semantic
// hue map (sage=shipped/good · copper=gate · amber=hold · red=risk).
func structureStats(f planFacts) []statChip {
	var out []statChip
	add := func(n int, singular, plural, class string) {
		if n <= 0 {
			return
		}
		label := plural
		if n == 1 {
			label = singular
		}
		out = append(out, statChip{Value: fmt.Sprintf("%d", n), Label: label, Class: class})
	}
	add(len(f.Tracks), "track", "tracks", "neutral")
	add(f.Gates, "gate", "gates", "copper")
	add(f.Shipped, "shipped", "shipped", "sage")
	add(f.Holds, "human hold", "human holds", "amber")
	add(f.Risks, "risk", "risks", "red")
	return out
}

// buildTrackSwim renders the parsed lanes as the scaffold's .swim figure: one
// lane per track, one bar per item (shipped filled sage, pending outlined), a
// ◆ gate diamond at the lane end carrying the gate text as tooltip. The
// figcaption doubles as the state legend (never color alone).
func buildTrackSwim(lanes []trackLane) string {
	var b strings.Builder
	b.WriteString(`<figure class="swim auto-swim track-swim"><figcaption>Tracks &amp; gates — <span class="sw-key done">■ shipped</span> · <span class="sw-key todo">□ pending</span> · ◆ gate (hover for the gate)</figcaption>`)
	for _, l := range lanes {
		b.WriteString(`<div class="lane"><div class="lane-name" title="` + htmlEscape(l.Full) + `">` + htmlEscape(l.Name) + `</div><div class="track">`)
		if len(l.Items) == 0 {
			cls := stateClass(l.Shipped)
			fmt.Fprintf(&b, `<span class="bar %s" style="left:2%%;width:96%%">%s</span>`, cls, htmlEscape(l.Name))
		} else {
			slot := 96.0 / float64(len(l.Items))
			for i, it := range l.Items {
				left := 2 + slot*float64(i)
				width := slot - 1
				title := it.ID
				if it.Label != "" {
					title += " — " + it.Label
				}
				fmt.Fprintf(&b, `<span class="bar %s" style="left:%.1f%%;width:%.1f%%" title="%s">%s</span>`,
					stateClass(it.Shipped), left, width, htmlEscape(title), htmlEscape(it.ID))
			}
		}
		if l.Gate != "" {
			fmt.Fprintf(&b, `<span class="gate" style="left:99%%" title="gate: %s">◆</span>`, htmlEscape(l.Gate))
		}
		b.WriteString(`</div></div>`)
	}
	b.WriteString(`</figure>`)
	return b.String()
}

func stateClass(shipped bool) string {
	if shipped {
		return "done"
	}
	return "todo"
}
