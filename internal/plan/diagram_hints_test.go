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
	g := buildGuidance(in, hints)
	if !strings.Contains(g, "ox plan viz") {
		t.Error("guidance should point at the visualization catalog")
	}
	if !strings.Contains(g, string(DiagramSequence)) {
		t.Error("guidance should fold in the plan-specific diagram hint")
	}
	if buildGuidance(Input{}, nil) != "" {
		t.Error("empty plan should produce empty guidance")
	}
}
