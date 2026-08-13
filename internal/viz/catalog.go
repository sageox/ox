package viz

import (
	_ "embed"
	"strings"
)

// catalog.go exposes the artifact-neutral visualization catalog used by every
// AI coworker. Plans are one consumer; documentation, PRs, reports, and design
// notes can use the same vocabulary and portable snippets.
//
// Why this exists: rendering is deterministic, so the lever on whether an
// artifact AIDS UNDERSTANDING is which visualizations the author reaches for. A flat wall
// of prose has high cognitive load; a sparkline, a dependency graph, or a Tufte
// table compresses the same information into something the eye grasps at once.
// The catalog is surfaced PROGRESSIVELY (list cheaply via `ox viz`, pull a
// single pattern's snippet on demand) so any coding agent can explore options
// while authoring and paste in the ones that fit — cross-agent, in the binary,
// not locked in a Claude-only skill. Snippets use the scaffold's CSS classes so
// they render design-faithfully and theme with the page.

//go:embed assets/viz-catalog.md
var vizCatalogMD string

// VizPattern is one catalog entry.
type VizPattern struct {
	ID        string   `json:"id"`               // stable slug, e.g. "sparkline"
	Category  string   `json:"category"`         // diagram|chart|layout|mockup|annotation
	Authoring string   `json:"authoring"`        // inline-svg|ox-render|mermaid|html-snippet
	Tags      []string `json:"tags"`             // reviewed discovery terms; never inferred from prose
	Origin    string   `json:"origin,omitempty"` // provenance for adapted third-party patterns
	Use       string   `json:"use"`              // when to reach for it
	Why       string   `json:"why"`              // the cognitive payoff
	Param     string   `json:"param,omitempty"`  // data-shape hint when `ox viz render <id> --data` is supported
	Body      string   `json:"body"`             // copy-paste snippet(s) + any notes
}

// Pattern is the concise package-level name used by new consumers.
type Pattern = VizPattern

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
			applyMetadata(&p)
			out = append(out, p)
		}
	}
	return out
}

func Catalog() []Pattern { return VizCatalog() }

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

func PatternByID(id string) (Pattern, bool) { return VizPatternByID(id) }

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
