package knowledgeflow

import (
	"strings"
	"testing"

	"github.com/sageox/ox/internal/session/contexttrace"
)

// TestBuild_GradesAndKinds is the consumer contract: each event becomes a flow
// of the right kind at the right grade, and provable/inferred are never mixed
// up. Failure prevented: recap renders an inferred guess as a proven chain, or a
// deterministic consult as proven causation.
func TestBuild_GradesAndKinds(t *testing.T) {
	tests := []struct {
		name      string
		ev        contexttrace.Event
		wantKind  string
		wantGrade Grade
		contains  string
	}{
		{
			"cross-session consult",
			contexttrace.Event{Type: contexttrace.EventConsulted, Mechanism: contexttrace.MechanismRetrieval, RefType: "session", Ref: "2026-06-12-maya-Ox3"},
			"cross-session", GradeProvable, "prior session",
		},
		{
			"distillation consult",
			contexttrace.Event{Type: contexttrace.EventConsulted, Mechanism: contexttrace.MechanismRetrieval, RefType: "kb", Ref: "memory/daily/2026-06.md"},
			"distillation", GradeProvable, "knowledge bubble",
		},
		{
			"query consult",
			contexttrace.Event{Type: contexttrace.EventConsulted, Mechanism: contexttrace.MechanismRetrieval, RefType: "query", Query: "token refresh"},
			"consult", GradeProvable, "token refresh",
		},
		{
			"whisper injection",
			contexttrace.Event{Type: contexttrace.EventProvided, Mechanism: contexttrace.MechanismInjection, From: "OxDmitri", RefType: "murmur"},
			"injection", GradeProvable, "OxDmitri",
		},
		{
			"citation consult is provable",
			contexttrace.Event{Type: contexttrace.EventConsulted, Mechanism: contexttrace.MechanismCitation, RefType: "discussion", Ref: "cnv_9"},
			"consult", GradeProvable, "cnv_9",
		},
		{
			"used by read is provable with its turn",
			contexttrace.Event{Type: contexttrace.EventUsed, Mechanism: contexttrace.MechanismRetrieval, Evidence: contexttrace.EvidenceRead, Ref: "MEMORY.md", RefType: "doc", Seq: 14},
			"use", GradeProvable, "You used MEMORY.md (read, turn 14)",
		},
		{
			"used by commit citation is provable under the cited bucket",
			contexttrace.Event{Type: contexttrace.EventUsed, Mechanism: contexttrace.MechanismCitation, Evidence: contexttrace.EvidenceCitedCommit, Ref: "Ox7f2a", RefType: "murmur"},
			"use", GradeProvable, "You used Ox7f2a (cited)",
		},
		{
			"used by self-report is inferred and says so",
			contexttrace.Event{Type: contexttrace.EventUsed, Mechanism: contexttrace.MechanismSelfReport, Evidence: contexttrace.EvidenceReported, Ref: "mur_x", Decision: "kept the retry budget"},
			"use", GradeInferred, "You reported using mur_x: “kept the retry budget”",
		},
		{
			"used without evidence falls back to its mechanism",
			contexttrace.Event{Type: contexttrace.EventUsed, Mechanism: contexttrace.MechanismSelfReport, Ref: "mur_x"},
			"use", GradeInferred, "You reported using mur_x",
		},
		{
			"judged use is inferred and labeled",
			contexttrace.Event{Type: contexttrace.EventUsed, Mechanism: contexttrace.MechanismJudge, Evidence: contexttrace.EvidenceJudged, Ref: "cnv_9", RefType: "discussion"},
			"use", GradeInferred, "Judged used: cnv_9",
		},
		{
			"self-report attribution is inferred + labeled",
			contexttrace.Event{Type: contexttrace.EventInfluenced, Mechanism: contexttrace.MechanismSelfReport, Ref: "principles.md", Decision: "kept the retry contract"},
			"attribution", GradeInferred, "self-assessed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flows := Build("my-session", []contexttrace.Event{tt.ev})
			if len(flows) != 1 {
				t.Fatalf("expected 1 flow, got %d", len(flows))
			}
			f := flows[0]
			if f.Kind != tt.wantKind {
				t.Errorf("kind = %q, want %q", f.Kind, tt.wantKind)
			}
			if f.Grade != tt.wantGrade {
				t.Errorf("grade = %q, want %q", f.Grade, tt.wantGrade)
			}
			if !strings.Contains(f.Text, tt.contains) {
				t.Errorf("text %q missing %q", f.Text, tt.contains)
			}
			if f.Session != "my-session" {
				t.Errorf("session = %q", f.Session)
			}
		})
	}
}

// TestBuild_SkipsNoneAndUnknownUse ensures a "nothing used" report and an
// evidence value from a newer writer never render as a use.
func TestBuild_SkipsNoneAndUnknownUse(t *testing.T) {
	flows := Build("s", []contexttrace.Event{
		{Type: contexttrace.EventUsed, Mechanism: contexttrace.MechanismSelfReport, Evidence: contexttrace.EvidenceNone},
		{Type: contexttrace.EventUsed, Evidence: "telepathy", Ref: "x"},
	})
	if len(flows) != 0 {
		t.Fatalf("none/unknown used events must not be flows, got %+v", flows)
	}
}

// TestBuild_SkipsPlainProvided ensures ambient prime docs (provided, no
// injection) don't become flows — they're covered by recap's "reached you"
// section, not the influence axis.
func TestBuild_SkipsPlainProvided(t *testing.T) {
	flows := Build("s", []contexttrace.Event{
		{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamMemory, Doc: "MEMORY.md"},
	})
	if len(flows) != 0 {
		t.Fatalf("plain provided event must not be a flow, got %+v", flows)
	}
}

// TestHasProvable drives recap's honest Available decision.
func TestHasProvable(t *testing.T) {
	inferredOnly := []Flow{{Grade: GradeInferred}}
	if HasProvable(inferredOnly) {
		t.Error("inferred-only flows must not count as provable")
	}
	mixed := []Flow{{Grade: GradeInferred}, {Grade: GradeProvable}}
	if !HasProvable(mixed) {
		t.Error("a provable flow must be detected")
	}
}
