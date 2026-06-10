package plan

import (
	_ "embed"
	"strings"
)

// viz_catalog.go exposes a catalog of information-visualization patterns for HTML
// plans — sequence/dependency/state diagrams, swimlane timelines, sparklines,
// small multiples, before/after, decision matrices, heatmaps, device mockups,
// callouts.
//
// Why this exists: rendering is deterministic, so the lever on whether a plan
// AIDS UNDERSTANDING is which visualizations the agent reaches for. A flat wall
// of prose has high cognitive load; a sparkline, a dependency graph, or a Tufte
// table compresses the same information into something the eye grasps at once.
// The catalog is surfaced PROGRESSIVELY (list cheaply via `ox plan viz`, pull a
// single pattern's snippet on demand) so any coding agent can explore options
// while authoring and paste in the ones that fit — cross-agent, in the binary,
// not locked in a Claude-only skill. Snippets use the scaffold's CSS classes so
// they render design-faithfully and theme with the page.

//go:embed assets/viz-catalog.md
var vizCatalogMD string

// VizPattern is one catalog entry.
type VizPattern struct {
	ID    string `json:"id"`              // stable slug, e.g. "sparkline"
	Use   string `json:"use"`             // when to reach for it
	Why   string `json:"why"`             // the cognitive payoff
	Param string `json:"param,omitempty"` // data-shape hint when `ox plan viz render <id> --data` is supported
	Body  string `json:"body"`            // copy-paste snippet(s) + any notes
}

// VizCatalog parses and returns every visualization pattern, in document order.
func VizCatalog() []VizPattern {
	var out []VizPattern
	// Split on "## " headings at line start. The leading comment block has no
	// "## " heading, so it is naturally excluded.
	blocks := strings.Split("\n"+vizCatalogMD, "\n## ")
	for i, blk := range blocks {
		if i == 0 {
			continue // preamble/comment before the first heading
		}
		if p, ok := parseVizBlock(blk); ok {
			out = append(out, p)
		}
	}
	return out
}

// VizPatternByID returns the pattern with the given id (case-insensitive), or
// ok=false when none matches.
func VizPatternByID(id string) (VizPattern, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, p := range VizCatalog() {
		if strings.ToLower(p.ID) == id {
			return p, true
		}
	}
	return VizPattern{}, false
}

// parseVizBlock turns one "## "-stripped block into a VizPattern. The first line
// is the id; subsequent `use:` / `why:` lines are metadata; the remainder is the
// snippet body.
func parseVizBlock(blk string) (VizPattern, bool) {
	lines := strings.Split(blk, "\n")
	if len(lines) == 0 {
		return VizPattern{}, false
	}
	p := VizPattern{ID: strings.TrimSpace(lines[0])}
	if p.ID == "" {
		return VizPattern{}, false
	}
	var body []string
	for _, ln := range lines[1:] {
		switch {
		case strings.HasPrefix(ln, "use:") && p.Use == "":
			p.Use = strings.TrimSpace(strings.TrimPrefix(ln, "use:"))
		case strings.HasPrefix(ln, "why:") && p.Why == "":
			p.Why = strings.TrimSpace(strings.TrimPrefix(ln, "why:"))
		case strings.HasPrefix(ln, "param:") && p.Param == "":
			p.Param = strings.TrimSpace(strings.TrimPrefix(ln, "param:"))
		default:
			body = append(body, ln)
		}
	}
	p.Body = strings.TrimSpace(strings.Join(body, "\n"))
	return p, true
}
