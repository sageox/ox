package plan

import (
	"fmt"
	"sort"
	"strings"
)

// diagram_hints.go computes deterministic, per-section diagram suggestions.
//
// Why this exists: HTML-plan rendering is now deterministic in the binary
// (render.go), so the page chrome is no longer the variable that decides whether
// a plan reads well — the AUTHORED CONTENT is. The single biggest content lever
// is whether the agent drew the RIGHT diagram for each section (a sequence
// diagram for a call path, a state machine for a lifecycle, a swimlane for a
// rollout) instead of flowcharting everything. ox already parses the plan into
// sections with their cited files; pattern-matching their structure to suggest a
// diagram form is pure-local context (ADR-021: context, not inference) — the
// agent still authors the Mermaid; ox just points it at the right kind.
//
// Precision over recall: a hint only fires when a section shows clear structure.
// A wrong/noisy suggestion is worse than none — the agent would draw a diagram
// that doesn't fit. Each rule therefore needs a minimum cue count to fire.

// maxDiagramHints caps the suggestions so the page stays focused: one hero
// diagram plus at most two section-specific diagrams (the html-plan contract —
// never two diagrams that show the same thing).
const maxDiagramHints = 3

// minCueScore is the cue-count threshold a keyword rule must clear to fire.
// Two distinct cues (not one incidental word) keeps prose from triggering a hint.
const minCueScore = 2

// cueRule maps a set of structural keywords to the diagram form that captures
// that structure. Cues are matched case-insensitively as substrings; they are
// padded where a bare word would over-match prose (see scoreCues).
type cueRule struct {
	kind DiagramKind
	cues []string
}

// diagramCueRules are evaluated in order; the highest-scoring rule wins a
// section. Ordering only breaks ties (earlier rule wins), so keep the more
// specific structures first.
var diagramCueRules = []cueRule{
	{DiagramSequence, []string{
		"request", "response", "round-trip", "round trip", "handshake",
		"endpoint", " calls ", " sends ", " returns ", " replies",
		"api call", "rpc", "then the", "in order",
		// NOTE: bare "->"/"→" are intentionally NOT cues — they appear in
		// dependency/flow prose and misfire sequence where topology is meant.
	}},
	{DiagramState, []string{
		" state", "transition", "retry", "backoff", "timeout", "lifecycle",
		"pending", "debounce", " idle", "in-progress", "expired", "rollback",
	}},
	{DiagramSwimlane, []string{
		"phase", "rollout", "milestone", "increment", "timeline", " week",
		" stage", "parallel", "concurrently", "sequencing", "in parallel",
	}},
}

// topology cues add weight to a dependency-graph suggestion alongside the raw
// file count of a section.
var topologyCues = []string{"depends on", " owns ", "boundary", "coupling", "module", "component", "blast radius"}

// branch cues fall back to a flowchart (the hero default) for a section that is a
// branching procedure but matched no richer structure.
var branchCues = []string{" if ", "branch", "decision", " gate", "otherwise", " else ", "pipeline", "fallback", "guard"}

// blocking cues mark a rollout/phase section whose real question is "what blocks
// what" (a dependency DAG / critical path), not just "what runs when" (a
// swimlane). When these co-occur with phase cues, prefer the topology DAG —
// matching the catalog's split (rollout-dag = order+blocking; swimlane = timing).
var blockingCues = []string{"gate", "blocks", "depends on", "prerequisite", "after ", "critical path", "blocked by"}

// hintCandidate is an internal scored suggestion before capping/ordering.
type hintCandidate struct {
	order int // section index, for stable output ordering
	score int // confidence, for top-N selection
	hint  DiagramHint
}

// computeDiagramHints inspects each section's structure and returns the
// strongest few diagram suggestions. Fail-open by construction: it only reads
// the already-parsed Input and never errors.
func computeDiagramHints(in Input) []DiagramHint {
	var cands []hintCandidate
	for i, sec := range in.Sections {
		if strings.TrimSpace(sec.Heading) == "" {
			continue // preamble is framing, not a step worth a diagram
		}
		if c, ok := scoreSection(i, sec); ok {
			cands = append(cands, c)
		}
	}
	if len(cands) == 0 {
		return nil
	}

	// Pick the top-N by confidence (stable: ties keep section order)…
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		return cands[i].order < cands[j].order
	})
	if len(cands) > maxDiagramHints {
		cands = cands[:maxDiagramHints]
	}
	// …then restore section order so the hints read top-to-bottom with the plan.
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].order < cands[j].order })

	hints := make([]DiagramHint, 0, len(cands))
	for _, c := range cands {
		hints = append(hints, c.hint)
	}
	return hints
}

// scoreSection returns the best-fitting diagram suggestion for one section, or
// ok=false when the section shows no clear structure worth a diagram.
func scoreSection(order int, sec Section) (hintCandidate, bool) {
	lc := strings.ToLower(" " + sec.Heading + "\n" + sec.Body + " ")
	fileN := len(sec.Files)

	bestKind := DiagramKind("")
	bestScore := 0
	for _, rule := range diagramCueRules {
		if s := scoreCues(lc, rule.cues); s > bestScore {
			bestScore, bestKind = s, rule.kind
		}
	}

	// Topology competes on file count + topology cues: a section coordinating
	// 3+ distinct files is a coupling/dependency picture even with few keywords.
	topoScore := scoreCues(lc, topologyCues)
	if fileN >= 3 {
		// coordinating 3+ files is itself a coupling picture; weight it so it
		// clears the fire threshold on file count alone.
		topoScore += fileN - 1
	}
	if topoScore > bestScore {
		bestScore, bestKind = topoScore, DiagramTopology
	}

	switch {
	case bestScore >= minCueScore:
		reason := reasonFor(bestKind, sec, fileN)
		// A phased section whose prose is about what BLOCKS what wants a
		// dependency DAG (critical path), not a timing swimlane.
		if bestKind == DiagramSwimlane && scoreCues(lc, blockingCues) >= 1 {
			bestKind = DiagramTopology
			reason = "phased rollout with blocking dependencies — a dependency DAG shows the critical path"
		}
		return hintCandidate{order, bestScore, DiagramHint{
			Section: sec.Heading, SuggestedType: bestKind, Reason: reason,
		}}, true
	case scoreCues(lc, branchCues) >= minCueScore:
		// branching procedure with no richer structure → the hero flowchart
		return hintCandidate{order, minCueScore, DiagramHint{
			Section: sec.Heading, SuggestedType: DiagramFlowchart,
			Reason: "section describes a branching procedure with decisions/gates",
		}}, true
	default:
		return hintCandidate{}, false
	}
}

// scoreCues counts how many DISTINCT cues from the set appear in the text. A
// distinct-count (not total-occurrence) keeps one repeated word from inflating a
// section's score past the threshold.
func scoreCues(lc string, cues []string) int {
	n := 0
	for _, c := range cues {
		if strings.Contains(lc, c) {
			n++
		}
	}
	return n
}

// reasonFor renders a one-clause, plan-specific reason for a suggestion.
func reasonFor(kind DiagramKind, sec Section, fileN int) string {
	switch kind {
	case DiagramSequence:
		return "section describes an ordered call/response path across components"
	case DiagramState:
		return "section describes states and time-bounded transitions (retry/timeout/lifecycle)"
	case DiagramSwimlane:
		return "section describes phased or parallel work with a temporal spine"
	case DiagramTopology:
		if fileN >= 3 {
			return fmt.Sprintf("section coordinates %d files — a dependency/topology graph reveals the coupling", fileN)
		}
		return "section describes module dependencies and boundaries"
	default:
		return "section has branching structure worth a diagram"
	}
}

// buildGuidance produces the concise, cross-agent authoring contract surfaced in
// `ox plan enrich --json` (Result.Guidance). It is deliberately lean — a checklist, not
// the 259-line skill spec — and folds in the plan-specific diagram hints so the
// agent gets concrete direction. Returns "" for a plan with no sections.
func buildGuidance(in Input, hints []DiagramHint) string {
	if len(in.Sections) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Render with `ox plan render --open` (self-contained, cross-agent). Author for a reviewer who has ~10 minutes: ")
	b.WriteString("lead with the conclusion and the biggest risk; keep every file/ID/PR framed enough to stand on its own; ")
	b.WriteString("let one hero diagram and a few tables replace prose rather than decorate it; cut anything that does not change the decision. ")
	b.WriteString("Explore visualization patterns with `ox plan viz` (sparklines, dependency graphs, swimlane timelines, Tufte tables, device mockups) and weave in the ones that compress understanding.")
	if len(hints) > 0 {
		b.WriteString(" Diagrams that fit this plan: ")
		parts := make([]string, 0, len(hints))
		for _, h := range hints {
			parts = append(parts, fmt.Sprintf("%q → %s (%s)", h.Section, h.SuggestedType, h.Reason))
		}
		b.WriteString(strings.Join(parts, "; "))
		b.WriteString(".")
	}
	return b.String()
}
