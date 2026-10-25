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
	"sequence-diagram":     {"diagram", "inline-svg", []string{"sequence", "request response", "round trip", "messages", "actors", "time order", "safe apply", "safe update", "interrupted apply", "crash recovery", "recover safely", "ownership"}, diagramDesignOrigin},
	"budget-sequence":      {"diagram", "mermaid", []string{"latency budget", "cost budget", "critical path", "round trip"}, ""},
	"dependency-graph":     {"diagram", "mermaid", []string{"dependencies", "coupling", "topology", "blast radius", "modules", "native targets", "shared targets", "shared destination", "where does this land"}, ""},
	"state-machine":        {"diagram", "inline-svg", []string{"states", "transitions", "lifecycle", "retry", "timeout", "backoff"}, diagramDesignOrigin},
	"swimlane-timeline":    {"diagram", "html-snippet", []string{"swimlane", "handoffs", "workstreams", "parallel", "relative effort", "rollout"}, ""},
	"gantt":                {"diagram", "mermaid", []string{"gantt", "calendar", "schedule", "dates", "milestones"}, ""},
	"sparkline":            {"chart", "html-snippet", []string{"sparkline", "inline trend", "tiny chart", "time series"}, ""},
	"small-multiples":      {"chart", "html-snippet", []string{"small multiples", "compare series", "outliers", "repeated charts"}, ""},
	"before-after":         {"layout", "html-snippet", []string{"before after", "old new", "old versus new", "delta", "comparison"}, ""},
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
	"waterfall":            {"chart", "ox-render", []string{"waterfall", "latency", "critical path", "long pole", "trace", "spans", "where the time went", "timeline", "overlap", "queue time", "pipeline stages"}, ""},
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

	"architecture":            {"diagram", "inline-svg", []string{"architecture", "system components", "services", "boundaries", "infrastructure", "topology", "portable skill catalog", "native Claude", "shared Codex Gemini", "project targets"}, diagramDesignOrigin},
	"flowchart":               {"diagram", "inline-svg", []string{"flowchart", "decision logic", "branches", "gates", "fallback", "procedure", "reconciliation", "reconciled", "deduplication"}, diagramDesignOrigin},
	"data-flow":               {"diagram", "inline-svg", []string{"data flow", "pipeline", "sources", "transformation", "consumers", "handoffs"}, diagramDesignOrigin},
	"layer-stack":             {"diagram", "inline-svg", []string{"layers", "layer stack", "abstractions", "enforcement", "defense", "controls"}, diagramDesignOrigin},
	"timeline":                {"diagram", "inline-svg", []string{"timeline", "events", "chronology", "history", "milestones", "time axis"}, diagramDesignOrigin},
	"loop":                    {"diagram", "inline-svg", []string{"loop", "flywheel", "cycle", "feedback", "reinforcing", "shared memory"}, diagramDesignOrigin},
	"execution-trace":         {"diagram", "inline-svg", []string{"execution trace", "cpu cores", "parallel execution", "threads", "concurrency", "what is executing", "blocking I O", "scheduler"}, ""},
	"event-stream":            {"diagram", "inline-svg", []string{"event stream", "events", "evented workflow", "topics", "fan out", "replay", "idempotency", "consumers"}, ""},
	"operational-time-series": {"chart", "inline-svg", []string{"operational time series", "cpu saturation", "p99 latency", "request rate", "deploy marker", "incident window", "correlated metrics"}, ""},
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
	p.Guidance = conceptualClarityGuidance + " " + guidanceByID[p.ID]
	if contract, ok := VisualContractByID(p.ID); ok {
		p.Contract = contract
	}
}

const conceptualClarityGuidance = "Conceptual clarity comes first: preserve the reader's simplest truthful mental model, and prefer prose, a table, or GitHub-safe Mermaid whenever it makes the primary relationship faster to understand. Rich styling must reveal information the simpler baseline cannot—not reorganize an already-clear idea."

// guidanceByID is deliberately separate from the primitive snippets. A
// primitive says how to draw a form; this tells an AI coworker what to make
// dominant, what to omit, and the perceptual failure mode to avoid. Every
// catalog entry must have one (enforced by TestCatalogMetadataComplete).
var guidanceByID = map[string]string{
	"sequence-diagram":        "Use 3–5 participants and align each message to a shared time axis. Make one causally decisive request distinct; do not turn every arrow into a highlight.",
	"budget-sequence":         "Give every hop one budget and one lever. Put the total at the end of the path; do not decorate the sequence with a second chart.",
	"dependency-graph":        "Cluster by ownership, make the shared/contended dependency central, and label only consequential edges. Every connector must visibly terminate at the boundary of its source and destination; never leave a floating arrow or route an edge through a label. Avoid a uniform hairball.",
	"state-machine":           "Derive the primary path from the reviewer question and keep the feature-defining loop central; do not demote it into a decorative secondary band. Use a left-to-right happy path, with retries below it. Highlight one transition under review; terminal states need unmistakable visual closure.",
	"swimlane-timeline":       "Use one shared horizontal clock and no more than five lanes. Bars describe work; arrows describe handoff. Keep lane labels outside the work area.",
	"gantt":                   "Use actual dates only. Show critical dependencies and one decision milestone; omit speculative precision and decorative progress bars.",
	"sparkline":               "Use one signal inline beside the sentence it supports—no title card, frame, or full-width plate. For axes, a threshold, or a causal annotation, promote it to a line chart.",
	"small-multiples":         "Use only for three or more comparable series. Keep scales identical, render series as unfilled strokes, and annotate the outlier; do not put a lone tiny chart inside a decorative card.",
	"before-after":            "If the delta is a 2–5-node linear path, use GitHub-safe Mermaid or two concise code snippets instead. Use a rich comparison only for a structural change that needs mirrored multi-step evidence; make the changed ownership or path the only high-contrast delta.",
	"decision-matrix":         "Limit to 3–5 options and 3–5 criteria. Make the recommendation visible but preserve the decisive trade-off; never imply false numeric precision.",
	"heatmap-table":           "Use a stable sequential scale with visible values only where they alter the decision. Sort rows and columns to reveal the hotspot.",
	"cost-telemetry-table":    "Keep units in every column and use emphasis only for the stage that changes the decision. Pair totals with the relevant budget, not a decorative badge.",
	"device-mockup":           "Show one realistic state at native hierarchy—especially the failure or recovery state. Crop surrounding device chrome aggressively.",
	"callout":                 "One claim, one evidence line, one source. Give the claim generous space; a callout is not a second introduction or a container for a paragraph.",
	"rollout-dag":             "Arrange gates as prerequisites, not calendar steps. Show the rollback edge and the evidence that unlocks each expansion.",
	"file-impact-map":         "Group files by subsystem and distinguish changed, reviewed, and intentionally untouched. Make the cross-cutting file visually central.",
	"risk-matrix":             "Use position for likelihood and impact, color only for severity. Label the few risks that change the decision; pair each with an owner or mitigation.",
	"stat-cards":              "Use at most three decision metrics with a shared comparison period. The value is primary, the delta secondary, and no card exists merely to fill space.",
	"bar-chart":               "Sort bars by value, start from zero, and annotate the comparison that matters. Use color for the selected or exceptional bar only.",
	"partition-bar":           "Use one total and label each segment directly. Keep small segments readable or aggregate them; never rely on a remote legend.",
	"partition-map":           "Show a real spatial structure only when location is meaningful. Direct-label regions and use an inset or table if tiny areas cannot be read.",
	"data-model":              "Show entities, keys, cardinality, and the ownership boundary. Omit fields that do not affect the design decision.",
	"coverage-matrix":         "Rows are behaviors, columns are test layers, and gaps must be visually louder than passes. Do not use a heatmap when binary coverage is the question.",
	"flag-rollout-matrix":     "Put cohorts on one axis and environments or rollout stages on the other. Highlight the unsafe combination and the gate that resolves it.",
	"cost-waterfall":          "Use signed contributions on a common baseline and end with the total. Each segment needs an operational cause, not merely a category name.",
	"waterfall":               "Put every stage on ONE clock anchored at a named t0, and say what t0 is. Give rows `after` edges so the critical path is derived rather than asserted — a lit path the reader cannot check is decoration. Draw wait gaps: idle time between stages is the finding, not a rendering detail. Segment a bar only where the phases are separately actionable (queued vs running); do not invent phases to add color. Never drop off-path rows silently — the rows that did not matter are evidence too.",
	"decision-grid":           "Use this for qualitative lenses, not pseudo-scoring. Let a concise recommendation sit next to the grid, not inside every cell.",
	"ox-annotation":           "Anchor the annotation to a precise visual claim and cite its source. It should clarify provenance, never compete with the diagram.",
	"donut":                   "Use only for a small number of parts with a meaningful whole. Put the decisive value in the center and directly label the largest remainder.",
	"radar":                   "Use identical axes and a small number of series. Prefer a table if exact magnitude matters more than shape.",
	"quadrant":                "Name both axes with direction and use position before color. Label only the decision-relevant points; avoid overlapping bubbles.",
	"treemap":                 "Area encodes one quantitative total. Keep hierarchy shallow and label only rectangles large enough to support readable text.",
	"sankey":                  "Width means volume and must remain conserved across each split. Direct-label the major paths and aggregate visual noise into an other path.",
	"chord":                   "Use only for genuinely reciprocal coupling. Order arcs to reduce crossings; annotate the strongest relationship instead of asking the reader to trace ribbons.",
	"line-chart":              "Use a true time axis, one visible threshold, and annotations at causal changes. Render each series as a connected unfilled stroke; do not use an opaque area or polygon unless area is the explicit encoding. Use shared-scale facets rather than stacking incompatible units.",
	"pull-quote":              "Quote one load-bearing sentence verbatim with attribution. Give it a visual pause; do not stylize it into a marketing headline.",
	"status-pair":             "Use only for a real shipped-versus-not-built or healthy-versus-degraded state, with one decisive metric or checklist delta. Do not use it to redraw a simple architecture change; a Mermaid flow or prose is clearer.",
	"wordmark":                "Use only when a user explicitly asks for a standalone brand component. Do not add it to technical PR visuals, diagrams, charts, or their footers by default.",
	"risk-register":           "Use one dense scan row per risk: severity, risk, resolution, owner. Reveal mechanism and fallback only on demand.",
	"architecture":            "Use named zones, orthogonal edges, and one trust or ownership boundary. Keep the hero view under seven nodes; split detail rather than shrink type.",
	"flowchart":               "Use verb actions and question diamonds. Keep the happy path straight, exceptional exits lateral, and label every branch condition.",
	"data-flow":               "Separate sources, transformations, and consumers. Put the changing data contract on the edge that carries it; never use arrows as decoration.",
	"layer-stack":             "Each band needs a distinct responsibility and boundary. Keep ordering meaningful and make the enforcement layer visually decisive.",
	"timeline":                "Use one real or relative axis with 3–7 inflection points. Annotate the event that changed the trajectory, not every date.",
	"loop":                    "Show 3–5 directed, unfilled arrow paths and the durable shared state at the hub. Never draw the loop as a filled/closed shape. If there is no durable feedback or the cycle has only two steps, use a Mermaid flowchart instead.",
	"execution-trace":         "Use aligned core/task lanes on one clock. Encode executing, runnable wait, blocking I/O, and handoff differently; highlight the span that gates completion.",
	"event-stream":            "Use only when durable fan-out, replay, or independent consumer ownership matters. Show producer, topic, and consumers with direct-label edges; otherwise prefer a compact Mermaid flow. Preserve ordering without drawing it as a synchronous call chain.",
	"operational-time-series": "Use aligned facets for unlike units, a deploy/incident marker, and an explicit threshold. Correlate signals on time—not on a deceptive dual axis.",
}
