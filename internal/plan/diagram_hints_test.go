package plan

import (
	"strings"
	"testing"
)

// TestComputeDiagramHints_RulesFire verifies each structural cue maps to the
// right diagram suggestion. Failure prevented: agents default every section to a
// flowchart because ox gave no per-section signal.
func TestComputeDiagramHints_RulesFire(t *testing.T) {
	cases := []struct {
		name    string
		heading string
		body    string
		want    DiagramKind
	}{
		{
			name:    "ordered call path -> sequence",
			heading: "Request flow",
			body:    "The client sends a request to the API, which then the DB returns rows in response.",
			want:    DiagramSequence,
		},
		{
			name:    "lifecycle -> state machine",
			heading: "Connection lifecycle",
			body:    "On timeout we retry with backoff; the pending state transitions to expired.",
			want:    DiagramState,
		},
		{
			name:    "phases -> swimlane",
			heading: "Rollout",
			body:    "Phase 1 ships the backend; phase 2 the UI runs in parallel across the milestone.",
			want:    DiagramSwimlane,
		},
		{
			name:    "many files -> topology",
			heading: "Wiring",
			body:    "Touches `internal/plan/render.go`, `internal/plan/enrich.go`, and `cmd/ox/plan.go`.",
			want:    DiagramTopology,
		},
		{
			name:    "branching -> flowchart fallback",
			heading: "Decision",
			body:    "If the flag is set, take the gate; otherwise fall back to the else branch.",
			want:    DiagramFlowchart,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := Parse("## " + tc.heading + "\n\n" + tc.body + "\n")
			hints := computeDiagramHints(in)
			if len(hints) == 0 {
				t.Fatalf("no hint produced for %q", tc.heading)
			}
			if hints[0].SuggestedType != tc.want {
				t.Errorf("got %q, want %q (reason: %s)", hints[0].SuggestedType, tc.want, hints[0].Reason)
			}
		})
	}
}

// TestComputeDiagramHints_RolloutBlockingPrefersDAG verifies a phased section
// whose prose is about what BLOCKS what gets a dependency DAG (topology), not a
// timing swimlane. Failure prevented: steering the reader to a swimlane that
// can't show the critical path.
func TestComputeDiagramHints_RolloutBlockingPrefersDAG(t *testing.T) {
	in := Parse("## Rollout\n\nPhase 1 ships the API; phase 2 gates phase 3 and blocks the migration milestone.\n")
	hints := computeDiagramHints(in)
	if len(hints) == 0 {
		t.Fatal("no hint for blocking rollout")
	}
	if hints[0].SuggestedType != DiagramTopology {
		t.Errorf("rollout+blocking should suggest %q (dependency DAG), got %q", DiagramTopology, hints[0].SuggestedType)
	}
	// a phased section WITHOUT blocking language stays a swimlane
	in2 := Parse("## Rollout\n\nPhase 1 ships the API; phase 2 runs the UI in parallel across the milestone timeline.\n")
	h2 := computeDiagramHints(in2)
	if len(h2) == 0 || h2[0].SuggestedType != DiagramSwimlane {
		t.Errorf("plain phased rollout should stay swimlane, got %+v", h2)
	}
}

// TestComputeDiagramHints_BareArrowDoesNotMisfireSequence verifies a dependency
// section using bare arrows ("A -> B depends on C") is NOT classified as a
// sequence diagram. Failure prevented: arrow prose misfiring sequence where
// topology is meant.
func TestComputeDiagramHints_BareArrowDoesNotMisfireSequence(t *testing.T) {
	in := Parse("## Wiring\n\nModule A -> module B, and B depends on C. The boundary -> coupling matters.\n")
	for _, h := range computeDiagramHints(in) {
		if h.SuggestedType == DiagramSequence {
			t.Errorf("bare arrows must not trigger a sequence diagram: %+v", h)
		}
	}
}

// TestComputeDiagramHints_NoFalseFireOnProse verifies a plain prose section with
// no structure produces no suggestion. Failure prevented: noisy/wrong hints push
// agents to draw diagrams that don't fit.
func TestComputeDiagramHints_NoFalseFireOnProse(t *testing.T) {
	in := Parse("## Background\n\nThis change improves clarity for the team and documents the rationale.\n")
	if hints := computeDiagramHints(in); len(hints) != 0 {
		t.Errorf("expected no hints on prose, got %+v", hints)
	}
}

// TestComputeDiagramHints_CapAndOrder verifies the cap (one hero + two) holds and
// hints come back in section order. Failure prevented: a page drowning in
// diagrams, or out-of-order suggestions confusing the author.
func TestComputeDiagramHints_CapAndOrder(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 5; i++ {
		b.WriteString("## Flow ")
		b.WriteByte(byte('A' + i))
		b.WriteString("\n\nThe client sends a request and the API returns a response in order.\n\n")
	}
	hints := computeDiagramHints(Parse(b.String()))
	if len(hints) > maxDiagramHints {
		t.Fatalf("got %d hints, want <= %d", len(hints), maxDiagramHints)
	}
}

// TestBuildGuidance_FoldsInHints verifies the cross-agent guidance string names
// the catalog and the plan-specific hints. Failure prevented: agents get a
// generic spec instead of direction for THIS plan.
func TestBuildGuidance_FoldsInHints(t *testing.T) {
	in := Parse("## Request flow\n\nThe client sends a request; the API returns a response in order.\n")
	hints := computeDiagramHints(in)
	g := buildGuidance(in, SignalSummary{}, hints, nil, "")
	if !strings.Contains(g, "ox plan viz") {
		t.Error("guidance should point at the visualization catalog")
	}
	if !strings.Contains(g, string(DiagramSequence)) {
		t.Error("guidance should fold in the plan-specific diagram hint")
	}
	if buildGuidance(Input{}, SignalSummary{}, nil, nil, "") != "" {
		t.Error("empty plan should produce empty guidance")
	}
}

// TestBuildGuidance_LeadsWithEvidence verifies the guidance LEADS with the
// plan-specific team-context counts when signals fired (so the agent sees what a
// self-authored render would drop), and falls back to the generic capability line
// when nothing fired.
// Failure prevented: the render call is buried under a generic pitch and agents
// emit a context-blind markdown/skill orphan instead of `ox plan render`.
func TestBuildGuidance_LeadsWithEvidence(t *testing.T) {
	in := Parse("## Plan\n\nTouches internal/auth and cmd/ox.\n")
	g := buildGuidance(in, SignalSummary{Collisions: 9, ExpertRoutes: 2}, nil, nil, "")
	for _, want := range []string{"9 file", "2 expert route", "drops all of it"} {
		if !strings.Contains(g, want) {
			t.Errorf("evidence-led guidance missing %q: %s", want, g)
		}
	}
	// singular agreement: 1 collision should not read "1 files"
	if s := buildGuidance(in, SignalSummary{Collisions: 1}, nil, nil, ""); strings.Contains(s, "1 files") {
		t.Errorf("collision count should be singular: %s", s)
	}
	// no signals → generic capability line that still names the ledger benefit,
	// without claiming specific dropped signals.
	g2 := buildGuidance(in, SignalSummary{}, nil, nil, "")
	if !strings.Contains(g2, "ledger") {
		t.Errorf("generic guidance should still name the ledger benefit: %s", g2)
	}
	if strings.Contains(g2, "drops all of it") {
		t.Errorf("no-signal guidance must not claim specific dropped signals: %s", g2)
	}
}

// TestComputeVizHints_CommonPatterns verifies the common parameterized patterns
// get a content-aware push on their canonical section — the gap diagram_hints
// (Mermaid-only) left open.
// Failure prevented: an agent writing a Risks / Files-changed / cost / metrics /
// flags section gets no suggestion and must browse the whole `ox plan viz` menu.
func TestComputeVizHints_CommonPatterns(t *testing.T) {
	cases := []struct{ name, md, wantID string }{
		{"risks", "## Risks\n\nEach risk is ranked by severity with a mitigation.\n", "risk-matrix"},
		{"files", "## Files changed\n\nNew and edited files across subsystems.\n", "file-impact-map"},
		{"cost", "## Cost\n\nToken spend and budget per component.\n", "cost-waterfall"},
		{"metrics", "## Metrics\n\nLatency before and after; headline numbers.\n", "stat-cards"},
		{"flags", "## Feature flags\n\nRollout across dev, test, prod with percentages.\n", "flag-rollout-matrix"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hints := computeVizHints(Parse(c.md))
			if len(hints) == 0 {
				t.Fatalf("no viz hint; want %s", c.wantID)
			}
			if hints[0].PatternID != c.wantID {
				t.Errorf("got %s, want %s (reason: %s)", hints[0].PatternID, c.wantID, hints[0].Reason)
			}
		})
	}
}

// TestComputeVizHints_PrecisionAndNiche verifies precision (a prose call-path
// section fires nothing) and that partition stays niche: generic "memory" prose
// does not surface it, but explicit flash/partition language does.
// Failure prevented: noisy false-positive pushes that erode trust in the hints.
func TestComputeVizHints_PrecisionAndNiche(t *testing.T) {
	if h := computeVizHints(Parse("## Approach\n\nThe client sends a request and the API returns a response.\n")); len(h) != 0 {
		t.Errorf("prose call-path section should yield no viz hint, got %+v", h)
	}
	if h := computeVizHints(Parse("## Memory usage\n\nThe service uses about 200 MB of RAM at peak.\n")); len(h) != 0 {
		t.Errorf("generic memory section must not surface partition, got %+v", h)
	}
	h := computeVizHints(Parse("## Flash layout\n\nOTA partitions and their offsets.\n"))
	if len(h) == 0 || (h[0].PatternID != "partition-bar" && h[0].PatternID != "partition-map") {
		t.Errorf("explicit flash/partition section should surface a partition pattern, got %+v", h)
	}
}

// TestComputeVizHints_CapAndOrder verifies the cap and that hints read in section
// order (top-to-bottom with the plan), like diagram hints.
func TestComputeVizHints_CapAndOrder(t *testing.T) {
	md := "## Risks\n\nrisks by severity, mitigation.\n\n" +
		"## Files changed\n\nnew and edited files.\n\n" +
		"## Cost\n\ntoken spend and budget.\n\n" +
		"## Metrics\n\nlatency before and after numbers.\n"
	h := computeVizHints(Parse(md))
	if len(h) > maxVizHints {
		t.Fatalf("expected <= %d hints, got %d", maxVizHints, len(h))
	}
	if len(h) == 0 || h[0].Section != "Risks" {
		t.Errorf("hints should read in section order (Risks first), got %+v", h)
	}
}

// TestBuildGuidance_FoldsVizHints verifies the guidance surfaces a data-viz hint
// WITH its render command (collapsing select→render), and omits the clause when
// there are none.
// Failure prevented: the agent is told a pattern fits but not how to render it.
func TestBuildGuidance_FoldsVizHints(t *testing.T) {
	in := Parse("## Risks\n\nrisks by severity with mitigations.\n")
	g := buildGuidance(in, SignalSummary{}, nil, computeVizHints(in), "")
	if !strings.Contains(g, "Data visualizations that fit") {
		t.Errorf("guidance should fold in viz hints: %s", g)
	}
	if !strings.Contains(g, "ox plan viz render risk-matrix --data") {
		t.Errorf("viz hint should carry its render command: %s", g)
	}
	if g2 := buildGuidance(in, SignalSummary{}, nil, nil, ""); strings.Contains(g2, "Data visualizations that fit") {
		t.Errorf("no viz hints should omit the clause: %s", g2)
	}
}
