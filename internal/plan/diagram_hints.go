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

// --- data-visualization hints (the parameterized catalog, derived from use:) ---
//
// computeDiagramHints covers Mermaid/CSS FORMS. computeVizHints covers the
// PARAMETERIZED data-viz catalog (risk-matrix, file-impact-map, cost-waterfall,
// stat-cards, flag-rollout-matrix, …) so a Risks / Files-changed / cost / metrics
// section gets a content-aware push, not just a menu it has to browse.
//
// The match signal is DERIVED from each pattern's `use:` line — there is no
// separate cue field in the catalog (that would duplicate `use:` and drift). The
// only code-side knobs are a shared stopword/generic list and the scoring weights
// (heading match counts double), mirroring how diagram_hints keeps its cues in
// code rather than in the catalog.

const maxVizHints = 3

// minVizScore is the fire threshold: a single heading-noun match (weight 2) or two
// distinct body-term matches. Precision over recall — a wrong push is worse than
// none, same discipline as minCueScore.
const minVizScore = 2

// vizStopwords drops grammar, catalog-meta shape words, and domain-generic terms
// so cue derivation from a `use:` line keeps only DISTINCTIVE triggers (e.g.
// "memory"/"disk"/"layout" are too ambiguous to fire partition on their own —
// "flash"/"partition"/"offset" carry it).
var vizStopwords = map[string]bool{
	// grammar
	"and": true, "the": true, "for": true, "are": true, "its": true, "with": true,
	"per": true, "where": true, "when": true, "that": true, "each": true, "than": true,
	"more": true, "not": true, "just": true, "into": true, "from": true, "out": true,
	// catalog-meta / generic shape words
	"use": true, "show": true, "story": true, "section": true, "list": true,
	"flat": true, "full": true, "few": true, "many": true, "handful": true,
	"layout": true, "share": true, "takes": true, "step": true, "steps": true,
	"thing": true, "things": true, "kind": true, "matter": true, "matters": true,
	"some": true, "which": true, "what": true,
	// domain-generic (too ambiguous to trigger on alone)
	"memory": true, "disk": true, "data": true, "component": true, "subsystem": true,
}

// vizUseCues derives distinctive trigger terms from a pattern's `use:` line. It
// reads only the LEAD clause (before the em-dash): the catalog convention is
// "use: <trigger> — <elaboration / contrast>", and the contrast ("instead of a
// flat list") would otherwise pollute the cues. Hyphenated tokens also yield their
// parts, and plural nouns their singular stem, so "Files changed" matches "files"
// and "Feature flags" matches the "flag" from "feature-flag".
func vizUseCues(use string) []string {
	lead := use
	if i := strings.Index(lead, "—"); i >= 0 {
		lead = lead[:i]
	}
	var sb strings.Builder
	for _, r := range strings.ToLower(lead) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			sb.WriteRune(r)
		} else {
			sb.WriteRune(' ')
		}
	}
	seen := map[string]bool{}
	var cues []string
	add := func(tok string) {
		tok = strings.Trim(tok, "-")
		if len(tok) < 3 || vizStopwords[tok] || seen[tok] {
			return
		}
		seen[tok] = true
		cues = append(cues, tok)
	}
	for _, tok := range strings.Fields(sb.String()) {
		tok = strings.Trim(tok, "-")
		if tok == "" {
			continue
		}
		add(tok)
		if strings.Contains(tok, "-") { // feature-flag → feature, flag
			for _, part := range strings.Split(tok, "-") {
				add(part)
			}
		}
		if strings.HasSuffix(tok, "s") && len(tok) >= 5 { // risks → risk, files → file
			add(strings.TrimSuffix(tok, "s"))
		}
	}
	return cues
}

// scoreVizSection scores one section against a pattern's cues. A heading match
// counts double (the agent TITLED the section that — a strong signal); a body
// match counts once. Returns the score and the distinct terms that hit.
func scoreVizSection(heading, body string, cues []string) (int, []string) {
	h := strings.ToLower(" " + heading + " ")
	bd := strings.ToLower(" " + body + " ")
	score := 0
	var hits []string
	for _, c := range cues {
		switch {
		case strings.Contains(h, c):
			score += 2
			hits = append(hits, c)
		case strings.Contains(bd, c):
			score++
			hits = append(hits, c)
		}
	}
	return score, hits
}

type vizCueSet struct {
	id    string
	param string
	cues  []string
}

// computeVizHints returns the strongest few per-section data-viz suggestions.
// Fail-open: it only reads the already-parsed Input and the embedded catalog, and
// never errors. Only patterns with a deterministic renderer (Param != "") are
// candidates — every hint is therefore actionable via `ox plan viz render`.
func computeVizHints(in Input) []VizHint {
	var sets []vizCueSet
	for _, p := range VizCatalog() {
		if p.Param == "" {
			continue
		}
		if cues := vizUseCues(p.Use); len(cues) > 0 {
			sets = append(sets, vizCueSet{p.ID, p.Param, cues})
		}
	}
	if len(sets) == 0 {
		return nil
	}

	type cand struct {
		order int
		score int
		hint  VizHint
	}
	var cands []cand
	for i, sec := range in.Sections {
		if strings.TrimSpace(sec.Heading) == "" {
			continue
		}
		bestID, bestParam, bestScore, bestHits := "", "", 0, []string(nil)
		for _, s := range sets {
			if score, hits := scoreVizSection(sec.Heading, sec.Body, s.cues); score > bestScore {
				bestID, bestParam, bestScore, bestHits = s.id, s.param, score, hits
			}
		}
		if bestScore < minVizScore || bestID == "" {
			continue
		}
		cands = append(cands, cand{i, bestScore, VizHint{
			Section:   sec.Heading,
			PatternID: bestID,
			Reason:    "section names " + joinAnd(topVizCues(bestHits, 2)),
			Param:     bestParam,
		}})
	}
	if len(cands) == 0 {
		return nil
	}
	// top-N by confidence (stable), then restore section order for reading.
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		return cands[i].order < cands[j].order
	})
	if len(cands) > maxVizHints {
		cands = cands[:maxVizHints]
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].order < cands[j].order })
	out := make([]VizHint, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.hint)
	}
	return out
}

// topVizCues picks the first n matched terms for the reason, dropping any that are
// a substring of another already chosen (so "risks"/"risk" don't both show).
func topVizCues(hits []string, n int) []string {
	var out []string
	for _, h := range hits {
		dup := false
		for _, o := range out {
			if strings.Contains(o, h) || strings.Contains(h, o) {
				dup = true
				break
			}
		}
		if !dup {
			if out = append(out, h); len(out) == n {
				break
			}
		}
	}
	return out
}

// buildGuidance produces the concise, cross-agent authoring contract surfaced in
// `ox plan enrich --json` (Result.Guidance). It is deliberately lean — a checklist, not
// the 259-line skill spec — and folds in the plan-specific diagram hints so the
// agent gets concrete direction. Returns "" for a plan with no sections.
//
// It LEADS with the plan-specific team-context evidence (sig) the agent is already
// holding: only `ox plan render` surfaces those signals and saves the plan to the
// ledger — a self-authored HTML render (the agent's own skill, which ox can't see
// or control, and there may be several) structurally cannot. Specific evidence the
// agent can verify beats a generic pitch, and wins on a capability competitors
// lack rather than trying to out-style them.
func buildGuidance(in Input, sig SignalSummary, hints []DiagramHint, vizHints []VizHint) string {
	if len(in.Sections) == 0 {
		return ""
	}
	var b strings.Builder
	if lead := guidanceLead(sig); lead != "" {
		b.WriteString(lead)
		b.WriteString(" ")
	} else {
		b.WriteString("Render with `ox plan render --open` (self-contained, cross-agent): it folds in team context and saves the plan to the ledger where teammates find it — a self-authored HTML render does neither. ")
	}
	b.WriteString("Author for a reviewer who has ~10 minutes: ")
	b.WriteString("lead with the conclusion and the biggest risk; keep every file/ID/PR framed enough to stand on its own; ")
	b.WriteString("let one hero diagram and a few tables replace prose rather than decorate it; cut anything that does not change the decision. ")
	b.WriteString("Explore visualization patterns with `ox plan viz` (sparklines, dependency graphs, swimlane timelines, budget sequences, Tufte tables, device mockups) and weave in the ones that compress understanding.")
	if len(hints) > 0 {
		b.WriteString(" Diagrams that fit this plan: ")
		parts := make([]string, 0, len(hints))
		for _, h := range hints {
			parts = append(parts, fmt.Sprintf("%q → %s (%s)", h.Section, h.SuggestedType, h.Reason))
		}
		b.WriteString(strings.Join(parts, "; "))
		b.WriteString(".")
	}
	if len(vizHints) > 0 {
		// Name the pattern AND its render command so select→render is one step.
		b.WriteString(" Data visualizations that fit this plan: ")
		parts := make([]string, 0, len(vizHints))
		for _, v := range vizHints {
			parts = append(parts, fmt.Sprintf("%q → %s (`ox plan viz render %s --data`)", v.Section, v.PatternID, v.PatternID))
		}
		b.WriteString(strings.Join(parts, "; "))
		b.WriteString(".")
	}
	return b.String()
}

// guidanceLead frames the render call with the SPECIFIC team-context evidence the
// plan triggered, so the agent reaches for `ox plan render` because it can see what
// a self-authored render would silently drop — not because we asserted authority.
// Returns "" when no team-context signal fired (the caller falls back to the
// generic capability line).
func guidanceLead(sig SignalSummary) string {
	var parts []string
	if n := sig.Collisions; n > 0 {
		parts = append(parts, fmt.Sprintf("%d file%s in open PRs / active work", n, plural(n)))
	}
	if n := sig.ExpertRoutes; n > 0 {
		parts = append(parts, fmt.Sprintf("%d expert route%s", n, plural(n)))
	}
	if n := sig.PriorArt; n > 0 {
		parts = append(parts, fmt.Sprintf("%d prior-art hit%s", n, plural(n)))
	}
	if len(parts) == 0 {
		return ""
	}
	return "This plan touches " + joinAnd(parts) +
		" — `ox plan render --open` is the only renderer that surfaces them and saves the plan to the ledger; a self-authored HTML render drops all of it."
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// joinAnd renders a human list: "a", "a and b", or "a, b, and c".
func joinAnd(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " and " + parts[1]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + ", and " + parts[len(parts)-1]
	}
}
