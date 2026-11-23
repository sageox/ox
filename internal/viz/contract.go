package viz

// VisualContract turns a catalog suggestion into construction-ready art
// direction. The AI teammate supplies truthful engineering evidence; ox owns
// the canvas, hierarchy, geometry, and perceptual constraints. This is
// intentionally more structured than Guidance: prose hints are easy to skim or
// reinterpret, while these fields can be validated, rendered, and evaluated.
type VisualContract struct {
	Version       string             `json:"version"`
	Question      string             `json:"reviewer_question"`
	Clarity       []string           `json:"conceptual_clarity"`
	RejectWhen    string             `json:"reject_when"`
	Canvas        CanvasContract     `json:"canvas"`
	Evidence      []EvidenceSlot     `json:"evidence_slots"`
	Composition   []string           `json:"composition"`
	Hierarchy     []string           `json:"visual_hierarchy"`
	Typography    TypographyContract `json:"typography"`
	Connectors    []string           `json:"connector_grammar,omitempty"`
	Color         []string           `json:"color_roles"`
	Constraints   []string           `json:"construction_constraints"`
	Variants      []VariantContract  `json:"variants"`
	AntiPatterns  []string           `json:"pattern_anti_patterns"`
	FinishingPass []string           `json:"finishing_pass"`
}

type CanvasContract struct {
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	AspectRatio string `json:"aspect_ratio"`
	Margin      int    `json:"margin"`
	Grid        int    `json:"grid"`
	Background  string `json:"background"`
}

type EvidenceSlot struct {
	ID       string `json:"id"`
	Required bool   `json:"required"`
	Prompt   string `json:"prompt"`
	Maximum  int    `json:"maximum,omitempty"`
}

type TypographyContract struct {
	Title      int `json:"title_px"`
	Section    int `json:"section_px"`
	Label      int `json:"label_px"`
	Detail     int `json:"detail_px"`
	Annotation int `json:"annotation_px"`
	Minimum    int `json:"minimum_px"`
	MaxLine    int `json:"max_characters_per_line"`
}

type VariantContract struct {
	ID      string `json:"id"`
	UseWhen string `json:"use_when"`
	Budget  string `json:"content_budget"`
}

// VisualContractByID returns a fresh contract for the rich patterns whose
// composition is mature enough to be a product promise. Other catalog entries
// keep their existing recipe until their grammar receives the same treatment.
func VisualContractByID(id string) (*VisualContract, bool) {
	build, ok := visualContractBuilders[id]
	if !ok {
		return nil, false
	}
	c := build()
	return &c, true
}

var visualContractBuilders = map[string]func() VisualContract{
	"architecture":            architectureContract,
	"coverage-matrix":         coverageMatrixContract,
	"data-flow":               dataFlowContract,
	"event-stream":            eventStreamContract,
	"execution-trace":         executionTraceContract,
	"flowchart":               flowchartContract,
	"operational-time-series": operationalTimeSeriesContract,
	"sequence-diagram":        sequenceContract,
	"state-machine":           stateMachineContract,
}

func baseContract(width, height int, ratio, question, reject string) VisualContract {
	return VisualContract{
		Version:    "2",
		Question:   question,
		RejectWhen: reject,
		Canvas: CanvasContract{
			Width: width, Height: height, AspectRatio: ratio,
			Margin: 88, Grid: 8, Background: "paper",
		},
		Typography: TypographyContract{
			Title: 44, Section: 24, Label: 26, Detail: 20,
			Annotation: 18, Minimum: 18, MaxLine: 32,
		},
		Hierarchy: []string{
			"One review conclusion is dominant; the diagram title is the conclusion, not the category name.",
			"Primary evidence uses ink and one accent; supporting context recedes to muted ink and hairlines.",
			"No decorative header, divider, legend card, footer, logo, or unused framing region.",
		},
		Clarity: []string{
			"Conceptual clarity outranks visual novelty and polish. Start with the reader's mental model: name the actors or states, the starting point, the primary path, the changed fact, and the outcome before placing marks.",
			"Preserve the simplest truthful topology. A richer treatment may add evidence or reveal a dimension, but it may not make the primary path, direction, grouping, or outcome harder to trace than prose, a table, or GitHub-safe Mermaid.",
			"Use position and grouping only for real domain meaning. Do not split one lifecycle into decorative bands, invent categories, or move a central event into a visually secondary exception lane merely to create a designed composition.",
			"Keep the same concept under one stable name and visual form. Never make the reader reconcile synonyms, duplicated states, or a title that answers a different question from the diagram.",
			"Run a five-second baseline test against the smallest truthful alternative: a new reader must identify the start, primary path, changed fact, and outcome at least as quickly. If the baseline is clearer, ship the baseline and improve only its typography, spacing, and emphasis.",
		},
		Color: []string{
			"paper is the canvas; ink carries labels and structure; muted carries context; rule carries guides.",
			"accent is reserved for the one changed, risky, or causally decisive element; never color every category.",
			"State and meaning must remain legible without color through position, label, stroke, or pattern.",
		},
		Variants: []VariantContract{
			{ID: "compact", UseWhen: "the reviewer question survives with one path or at most four primary items", Budget: "one focal claim; 2-4 primary items; no secondary panel"},
			{ID: "standard", UseWhen: "the full engineering mechanism needs one primary view", Budget: "one focal claim; 4-7 primary items; at most two annotations"},
			{ID: "deep", UseWhen: "one overview plus one tightly coupled detail is necessary", Budget: "one overview and one detail band; never two competing hero views"},
		},
		FinishingPass: []string{
			"Compare against the simplest truthful baseline with the same reviewer question. Reject the rich candidate if the baseline is faster to understand or easier to trace.",
			"Ask a reader unfamiliar with the implementation to identify the start, primary path, changed fact, and outcome in five seconds; revise or downgrade the medium if any answer is ambiguous.",
			"Read the exported image at 50% scale; every required label remains legible without zoom.",
			"Trace every relationship from source boundary to destination boundary; no floating, buried, or ambiguous connector survives.",
			"Keep every connector-label mask wholly inside an empty routing corridor; it may not touch a node, title, or unrelated edge.",
			"Delete any mark that does not encode evidence, grouping, sequence, scale, or emphasis.",
			"Confirm title, labels, and annotations tell one consistent story and make no claim absent from the PR evidence.",
		},
	}
}

func architectureContract() VisualContract {
	c := baseContract(1600, 1000, "8:5", "Where are the responsibilities and boundaries after this change?", "Use GitHub-safe Mermaid when the answer is a linear path of two to five nodes with no meaningful zones or boundary crossings.")
	c.Evidence = []EvidenceSlot{
		{ID: "zones", Required: true, Prompt: "Name 2-4 ownership, trust, or runtime zones.", Maximum: 4},
		{ID: "components", Required: true, Prompt: "Name only components needed to answer the reviewer question.", Maximum: 7},
		{ID: "relationships", Required: true, Prompt: "State direction and meaning of each consequential relationship.", Maximum: 9},
		{ID: "changed_boundary", Required: true, Prompt: "Identify the single responsibility or boundary changed by the PR.", Maximum: 1},
	}
	c.Composition = []string{
		"Lay zones as large quiet regions on a left-to-right responsibility axis; zone labels sit in the outer margin, never inside a node field.",
		"Place the changed boundary on the central third of the canvas and give it the only accent treatment.",
		"Align peer components to a shared baseline; vary size only when responsibility or fan-out differs.",
		"Reserve a clear orthogonal routing channel between zones before placing any labels.",
	}
	c.Connectors = []string{
		"Use orthogonal paths with rounded elbows; attach visibly to node boundaries and end with one modest arrowhead.",
		"Put short relationship labels above a horizontal segment with an opaque paper mask and at least 10px clearance.",
		"Fan multiple edges to distinct attachment points at least 16px apart; never stack connectors on one route.",
	}
	c.Constraints = []string{"2-4 zones", "3-7 components", "5-9 consequential relationships", "one changed boundary", "no node label longer than two lines"}
	c.AntiPatterns = []string{"uniform box grid", "all components accented", "connectors crossing labels", "floating arrows", "a legend for colors that can be direct labels"}
	return c
}

func sequenceContract() VisualContract {
	c := baseContract(1600, 1050, "32:21", "What happens in what order, and where does the decisive handoff, wait, or failure occur?", "Use a sentence for one call and response; use an execution trace when simultaneous CPU work is the reviewer question.")
	c.Evidence = []EvidenceSlot{
		{ID: "participants", Required: true, Prompt: "Name actors in causal order.", Maximum: 5},
		{ID: "messages", Required: true, Prompt: "Provide ordered messages with direction and terse labels.", Maximum: 9},
		{ID: "decisive_step", Required: true, Prompt: "Identify the message that establishes safety, ownership, or failure.", Maximum: 1},
		{ID: "alternate", Required: false, Prompt: "Name one failure or recovery branch only if review depends on it.", Maximum: 1},
	}
	c.Composition = []string{"Participant labels form one quiet header row; lifelines share equal spacing and begin on the same baseline.", "Time reads top to bottom with generous vertical rhythm; group request/response pairs through proximity, not containers.", "The decisive exchange occupies the visual center and receives the only accent; one alternate branch may use an unfilled outline that never obscures a lifeline, message, or label."}
	c.Connectors = []string{"Messages run horizontally between lifelines with labels above the stroke.", "Return messages use a dashed stroke; asynchronous messages use an open arrowhead and explicit label.", "Activation bars align exactly to message endpoints; no arrow ends in open space."}
	c.Constraints = []string{"3-5 participants", "4-9 messages", "one highlighted exchange", "at most one alternate band", "labels are verbs or payloads, not sentences"}
	c.AntiPatterns = []string{"equal emphasis on every message", "diagonal messages", "notes covering lifelines", "more than one time direction", "decorative participant icons"}
	return c
}

func stateMachineContract() VisualContract {
	c := baseContract(1600, 900, "16:9", "Which states are possible, and which transition or recovery path changes the reviewer’s risk?", "Use Mermaid for four or fewer states on one happy path with no guarded recovery or terminal distinction.")
	c.Evidence = []EvidenceSlot{
		{ID: "states", Required: true, Prompt: "Name observable states, not implementation steps.", Maximum: 8},
		{ID: "transitions", Required: true, Prompt: "Name event/guard on each allowed transition.", Maximum: 12},
		{ID: "happy_path", Required: true, Prompt: "Identify the primary left-to-right lifecycle.", Maximum: 1},
		{ID: "recovery", Required: false, Prompt: "Identify retry, orphan, rollback, or fail-closed route.", Maximum: 3},
		{ID: "terminal_states", Required: true, Prompt: "Name successful and unsuccessful terminal states.", Maximum: 3},
	}
	c.Composition = []string{"Derive the primary path from the reviewer question, not from whichever states happen to form the longest line. For a pause/resume change, recording → suspended → recording is central even when upload is the eventual terminal path.", "Place the happy path on one uninterrupted reading path from initial to terminal state; a loop that defines the feature stays adjacent to its source state and visually primary.", "Route recovery and exceptional states below the primary path in a shallow second tier; return paths stay outside the main corridor but reconnect visibly to the exact state they resume.", "Use section bands only when they represent real, mutually exclusive domains. Never separate states merely by visual taxonomy if doing so makes transitions harder to trace.", "Give terminal states unmistakable closure through a double rule or terminal glyph, not color alone.", "Highlight the transition changed by the PR rather than filling an entire state."}
	c.Connectors = []string{"Transitions attach to state boundaries and carry event labels beside the longest clear segment.", "Self-loops sit above the state; recovery loops route below all states and return through a separate attachment point.", "Arrow direction must be inferable at 50% scale."}
	c.Constraints = []string{"4-8 states", "one happy-path baseline", "at most three recovery states", "one changed transition", "no crossing transitions"}
	c.AntiPatterns = []string{"radial state cloud", "transition labels inside states", "all states as identical pills", "recovery edges crossing the happy path", "color as the only terminal encoding"}
	return c
}

func flowchartContract() VisualContract {
	c := baseContract(1400, 1000, "7:5", "Which decision changes the outcome, including the exceptional exit?", "Use prose for a single condition; use a state machine when nodes are durable states rather than actions.")
	c.Evidence = []EvidenceSlot{{ID: "actions", Required: true, Prompt: "Name verb-led actions.", Maximum: 7}, {ID: "decisions", Required: true, Prompt: "Name binary questions and label both exits.", Maximum: 3}, {ID: "outcomes", Required: true, Prompt: "Name terminal outcomes.", Maximum: 3}, {ID: "review_gate", Required: true, Prompt: "Identify the decision changed by the PR.", Maximum: 1}}
	c.Composition = []string{"Keep the happy path as one vertical spine with decisions centered on it.", "Route exceptional exits laterally into a narrow side column and terminate them quickly.", "Use whitespace between stages instead of background containers; accent only the decision under review."}
	c.Connectors = []string{"Use orthogonal downward connectors on the happy path and right-angle lateral exits.", "Place yes/no or guard labels at the branch origin, never midway between unrelated nodes.", "All paths visibly terminate in an action or outcome."}
	c.Constraints = []string{"3-7 actions", "1-3 decisions", "one primary spine", "at most two lateral exception columns", "no connector crossing"}
	c.AntiPatterns = []string{"serpentine happy path", "unlabeled branch exits", "diamonds used for actions", "multiple equal focal points", "arrows ending between nodes"}
	return c
}

func dataFlowContract() VisualContract {
	c := baseContract(1600, 900, "16:9", "How does information change as it moves from source to consumer?", "Use Mermaid when data passes unchanged through fewer than five components.")
	c.Evidence = []EvidenceSlot{{ID: "sources", Required: true, Prompt: "Name authoritative producers.", Maximum: 4}, {ID: "transformations", Required: true, Prompt: "Name transformations and the contract each emits.", Maximum: 4}, {ID: "consumers", Required: true, Prompt: "Name consumers and why each needs the data.", Maximum: 5}, {ID: "changed_contract", Required: true, Prompt: "Name the schema, ownership, or delivery contract changed by the PR.", Maximum: 1}}
	c.Composition = []string{"Use three vertical zones—sources, transformations, consumers—on one left-to-right axis.", "Make the changed data contract the focal edge label, not a floating callout.", "Align peers within each zone and reserve a central routing corridor."}
	c.Connectors = []string{"Edges encode the data artifact in direct labels and visibly attach to both endpoints.", "Fan-out begins at a named contract or store, not in empty space.", "Use dashed stroke only for optional or asynchronous delivery and label that semantic explicitly."}
	c.Constraints = []string{"2-3 zones", "3-9 total nodes", "one changed contract", "at most two fan-out levels", "direct labels instead of a legend"}
	c.AntiPatterns = []string{"arrows as decoration", "mixing control and data flow without labels", "unbounded fan-out hairball", "source and consumer in the same visual zone", "contract labels floating away from edges"}
	return c
}

func eventStreamContract() VisualContract {
	c := baseContract(1600, 900, "16:9", "How does a durable event reach independent consumers, and what happens on replay or failure?", "Use a compact Mermaid flow when delivery is synchronous, there is one consumer, or replay and independent ownership do not matter.")
	c.Evidence = []EvidenceSlot{{ID: "producer", Required: true, Prompt: "Name the producer and event.", Maximum: 2}, {ID: "durable_topic", Required: true, Prompt: "Name the durable topic/log and ordering key.", Maximum: 1}, {ID: "consumers", Required: true, Prompt: "Name independent consumer groups and their responsibility.", Maximum: 5}, {ID: "delivery_semantics", Required: true, Prompt: "State replay, idempotency, retry, and dead-letter semantics that actually exist.", Maximum: 4}, {ID: "failure", Required: false, Prompt: "Name one failure path reviewers must reason about.", Maximum: 1}}
	c.Composition = []string{"Place producer at left, the durable log as a strong central spine, and consumers as aligned lanes on the right.", "Show append and offset progression along the spine; consumer ownership reads from vertical separation, not rainbow color.", "Put replay or dead-letter behavior in one lower recovery band that reconnects to the exact consumer lane it affects."}
	c.Connectors = []string{"Producer append terminates on the log; fan-out begins at distinct offsets on the log and terminates on each consumer boundary.", "Use solid edges for delivery, dashed reverse edges only for retry/replay, and label both directly.", "No edge may imply a synchronous response from a consumer to the producer."}
	c.Constraints = []string{"1-2 producers", "one durable log", "2-5 consumer groups", "one recovery band", "ordering/replay semantics stated in the image"}
	c.AntiPatterns = []string{"drawing the stream as a request chain", "shared arrow endpoint for every consumer", "unlabeled retry loop", "color-only ownership", "consumer boxes scattered around the canvas"}
	return c
}

func executionTraceContract() VisualContract {
	c := baseContract(1600, 900, "16:9", "What executes concurrently on the shared clock, and which wait, handoff, or critical span determines completion?", "Use a sequence diagram when only message order matters; use an operational time series when aggregate measurements, not spans, answer the question.")
	c.Evidence = []EvidenceSlot{{ID: "clock", Required: true, Prompt: "Provide start/end or relative ticks on one shared clock.", Maximum: 8}, {ID: "lanes", Required: true, Prompt: "Name CPU, thread, goroutine, or task lanes.", Maximum: 6}, {ID: "spans", Required: true, Prompt: "Provide start, duration, label, and state for each span.", Maximum: 18}, {ID: "handoffs", Required: false, Prompt: "Name causal cross-lane handoffs.", Maximum: 5}, {ID: "critical_span", Required: true, Prompt: "Identify the span that gates completion or causes contention.", Maximum: 1}}
	c.Composition = []string{"Reserve the top band for title and shared clock; lane labels occupy a fixed left gutter outside the plot.", "Every span is positioned by time, not by convenient spacing; aligned vertical guides make concurrency visible.", "Encode running as solid bars, runnable wait as outlined bars, blocking I/O as hatched bars, and idle as absence.", "Give the critical span one accent outline and a short causal annotation in unused plot space."}
	c.Connectors = []string{"Handoffs begin at the exact end of one span and terminate at the exact start of another.", "Use thin orthogonal or gently curved connectors behind neither bars nor labels; arrowheads remain visible at 50% scale.", "Do not connect events that are merely correlated."}
	c.Constraints = []string{"2-6 lanes", "4-18 timed spans", "one shared clock", "one critical span", "at most five causal handoffs", "all time geometry data-derived"}
	c.AntiPatterns = []string{"equal-width decorative bars", "separate clocks per lane", "handoff ending before its destination", "legend larger than the trace", "using color as the only execution-state encoding"}
	return c
}

func operationalTimeSeriesContract() VisualContract {
	c := baseContract(1600, 1050, "32:21", "Which measured signals changed together around a deploy or incident, and did they cross an operational threshold?", "Use a sparkline for one contextual trend without axes; use prose when no measured observations exist.")
	c.Evidence = []EvidenceSlot{{ID: "time_window", Required: true, Prompt: "Provide a real or relative time window and aligned timestamps.", Maximum: 12}, {ID: "facets", Required: true, Prompt: "Provide 2-4 signals, each with its own unit and scale.", Maximum: 4}, {ID: "observations", Required: true, Prompt: "Provide measured values; never fabricate a smooth series.", Maximum: 48}, {ID: "thresholds", Required: true, Prompt: "Provide the relevant SLO, capacity, or baseline threshold per facet.", Maximum: 4}, {ID: "causal_marker", Required: true, Prompt: "Name one deploy, rollback, or incident marker supported by the PR.", Maximum: 2}}
	c.Composition = []string{"Stack unlike units as aligned horizontal facets sharing one time axis; never overlay them on a dual axis.", "Use a single vertical event marker across every facet so temporal correlation is immediate.", "Keep gridlines sparse and quiet; direct-label each series and threshold inside its facet.", "Place one conclusion annotation near the decisive inflection, leaving the rest of the plot free of prose."}
	c.Connectors = []string{"Series are open unfilled strokes; thresholds are thin dashed rules; the event marker is a distinct vertical rule.", "Annotation leaders terminate on an observed point and never cross another series label.", "Do not interpolate across missing data without an explicit gap encoding."}
	c.Constraints = []string{"2-4 aligned facets", "one shared time axis", "one unit per facet", "one or two causal markers", "one conclusion annotation", "no filled area unless area is the measured encoding"}
	c.AntiPatterns = []string{"dual axes", "filled polygon under a line", "independent time scales", "SLO line at the wrong coordinate", "legend detached from the series", "invented observations"}
	return c
}

func coverageMatrixContract() VisualContract {
	c := baseContract(1600, 1000, "8:5", "Which behaviors are proven across which implementations, and where is evidence missing?", "Use a Markdown table when there are fewer than four rows or columns and no grouped behavior hierarchy.")
	c.Evidence = []EvidenceSlot{{ID: "implementations", Required: true, Prompt: "Name agents, platforms, adapters, or test layers as columns.", Maximum: 9}, {ID: "behaviors", Required: true, Prompt: "Name independently reviewable behaviors as rows.", Maximum: 12}, {ID: "status", Required: true, Prompt: "Provide proven, partial, missing, or not-applicable for every cell.", Maximum: 108}, {ID: "provenance", Required: true, Prompt: "Name the real fixture, smoke gate, or evidence standard represented by proven.", Maximum: 3}, {ID: "critical_gap", Required: false, Prompt: "Identify the one gap that changes merge or follow-up decisions.", Maximum: 1}}
	c.Composition = []string{"Use a frozen left label column and equal-width implementation columns; group related behaviors with whitespace or a quiet rule.", "Make missing and partial evidence more visually salient than proven cells; proven should form a calm background texture.", "Put the evidence standard in a concise subtitle and annotate only the critical gap."}
	c.Connectors = nil
	c.Constraints = []string{"4-12 behavior rows", "4-9 implementation columns", "four explicit statuses", "one evidence standard", "one optional critical-gap annotation", "labels never rotate vertically"}
	c.AntiPatterns = []string{"rainbow heatmap", "checkmarks without an evidence definition", "red-green-only encoding", "rotated column labels", "decorative summary cards above the matrix"}
	return c
}
