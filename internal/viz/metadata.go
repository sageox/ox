package viz

// Metadata is deliberately separate from the prose snippets: tags are a
// reviewed retrieval contract, not keywords scraped from sentences. The drift
// tests require every catalog entry to be represented here.
type patternMetadata struct {
	category  string
	authoring string
	tags      []string
	origin    string
}

const diagramDesignOrigin = "cathrynlavery/diagram-design@f3622cf"

var metadataByID = map[string]patternMetadata{
	"sequence-diagram":     {"diagram", "inline-svg", []string{"sequence", "request response", "round trip", "messages", "actors", "time order"}, diagramDesignOrigin},
	"budget-sequence":      {"diagram", "mermaid", []string{"latency budget", "cost budget", "critical path", "round trip"}, ""},
	"dependency-graph":     {"diagram", "mermaid", []string{"dependencies", "coupling", "topology", "blast radius", "modules"}, ""},
	"state-machine":        {"diagram", "inline-svg", []string{"states", "transitions", "lifecycle", "retry", "timeout", "backoff"}, diagramDesignOrigin},
	"swimlane-timeline":    {"diagram", "html-snippet", []string{"swimlane", "handoffs", "workstreams", "parallel", "relative effort", "rollout"}, ""},
	"gantt":                {"diagram", "mermaid", []string{"gantt", "calendar", "schedule", "dates", "milestones"}, ""},
	"sparkline":            {"chart", "html-snippet", []string{"sparkline", "inline trend", "tiny chart", "time series"}, ""},
	"small-multiples":      {"chart", "html-snippet", []string{"small multiples", "compare series", "outliers", "repeated charts"}, ""},
	"before-after":         {"layout", "html-snippet", []string{"before after", "old new", "delta", "comparison"}, ""},
	"decision-matrix":      {"layout", "html-snippet", []string{"decision matrix", "options", "criteria", "tradeoffs", "score"}, ""},
	"heatmap-table":        {"chart", "html-snippet", []string{"heatmap", "dense numbers", "magnitude", "hotspot"}, ""},
	"cost-telemetry-table": {"layout", "html-snippet", []string{"telemetry", "cost stages", "performance budget", "measurements"}, ""},
	"device-mockup":        {"mockup", "html-snippet", []string{"device", "mockup", "screen", "mobile", "user interface"}, ""},
	"callout":              {"layout", "html-snippet", []string{"callout", "decision", "blocker", "key risk", "tldr"}, ""},
	"rollout-dag":          {"diagram", "mermaid", []string{"rollout", "dag", "blocking", "phases", "critical path", "parallel"}, ""},
	"file-impact-map":      {"layout", "ox-render", []string{"files changed", "file impact", "change scope", "blast radius"}, ""},
	"risk-matrix":          {"layout", "ox-render", []string{"risk matrix", "severity", "mitigation", "blocker"}, ""},
	"stat-cards":           {"chart", "ox-render", []string{"metrics", "headline numbers", "before after", "delta", "counts"}, ""},
	"bar-chart":            {"chart", "ox-render", []string{"bar chart", "compare values", "categories", "magnitude"}, ""},
	"partition-bar":        {"chart", "ox-render", []string{"partition", "memory map", "disk layout", "flash", "proportion"}, ""},
	"partition-map":        {"chart", "ox-render", []string{"partition map", "offsets", "address space", "flash layout", "memory layout"}, ""},
	"data-model":           {"diagram", "mermaid", []string{"data model", "schema", "entities", "relationships", "foreign keys"}, ""},
	"coverage-matrix":      {"layout", "html-snippet", []string{"test coverage", "coverage matrix", "test layers", "gaps"}, ""},
	"flag-rollout-matrix":  {"chart", "ox-render", []string{"feature flag", "rollout", "environments", "percentage", "stages"}, ""},
	"cost-waterfall":       {"chart", "ox-render", []string{"waterfall", "cost", "cumulative", "budget", "components"}, ""},
	"decision-grid":        {"layout", "html-snippet", []string{"decision grid", "experts", "review lenses", "options"}, ""},
	"ox-annotation":        {"annotation", "html-snippet", []string{"annotation", "citation", "decision record", "prior art", "reference"}, ""},
	"donut":                {"chart", "ox-render", []string{"donut", "part of whole", "proportion", "share", "slices"}, ""},
	"radar":                {"chart", "ox-render", []string{"radar", "spider", "multi criteria", "compare options"}, ""},
	"quadrant":             {"chart", "ox-render", []string{"quadrant", "two axis", "impact effort", "value risk", "prioritization"}, ""},
	"treemap":              {"chart", "ox-render", []string{"treemap", "proportional hierarchy", "area", "package size"}, ""},
	"sankey":               {"chart", "ox-render", []string{"sankey", "flow magnitude", "traffic split", "tokens", "cost flow"}, ""},
	"chord":                {"chart", "ox-render", []string{"chord", "coupling", "interactions", "who touches what"}, ""},
	"line-chart":           {"chart", "ox-render", []string{"line chart", "trend", "time series", "growth", "threshold", "sawtooth"}, ""},
	"pull-quote":           {"layout", "html-snippet", []string{"quote", "doctrine", "verbatim", "key sentence"}, ""},
	"status-pair":          {"layout", "html-snippet", []string{"progress", "partial", "shipped", "not built", "status"}, ""},
	"wordmark":             {"annotation", "html-snippet", []string{"wordmark", "sageox credit", "attribution", "branding"}, ""},
	"risk-register":        {"layout", "html-snippet", []string{"risk register", "risk owner", "severity", "fallback", "trigger"}, ""},

	"architecture": {"diagram", "inline-svg", []string{"architecture", "system components", "services", "boundaries", "infrastructure", "topology"}, diagramDesignOrigin},
	"flowchart":    {"diagram", "inline-svg", []string{"flowchart", "decision logic", "branches", "gates", "fallback", "procedure"}, diagramDesignOrigin},
	"data-flow":    {"diagram", "inline-svg", []string{"data flow", "pipeline", "sources", "transformation", "consumers", "handoffs"}, diagramDesignOrigin},
	"layer-stack":  {"diagram", "inline-svg", []string{"layers", "layer stack", "abstractions", "enforcement", "defense", "controls"}, diagramDesignOrigin},
	"timeline":     {"diagram", "inline-svg", []string{"timeline", "events", "chronology", "history", "milestones", "time axis"}, diagramDesignOrigin},
	"loop":         {"diagram", "inline-svg", []string{"loop", "flywheel", "cycle", "feedback", "reinforcing", "shared memory"}, diagramDesignOrigin},
}

func applyMetadata(p *VizPattern) {
	m, ok := metadataByID[p.ID]
	if !ok {
		return
	}
	p.Category = m.category
	p.Authoring = m.authoring
	p.Tags = append([]string(nil), m.tags...)
	p.Origin = m.origin
}
