package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/session"
)

// writeRawJSONL writes a synthetic recording raw.jsonl into dir and returns the
// recording state pointing at it.
func writeRawJSONL(t *testing.T, lines string) *session.RecordingState {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	return &session.RecordingState{SessionPath: dir}
}

// TestDeriveCollabSignals_Counts verifies the deterministic effort counts from a
// transcript: user prompts, tool calls, AskUserQuestion clarifications, and the
// span from first user turn to last entry.
// Failure prevented: collaboration-rigor signals are wrong, so any later
// judgment of plan thoughtfulness is built on bad counts.
func TestDeriveCollabSignals_Counts(t *testing.T) {
	raw := `{"type":"header","metadata":{"version":"1.0"}}
{"type":"user","ts":"2026-06-04T10:00:00Z","content":"do X"}
{"type":"assistant","ts":"2026-06-04T10:00:05Z","content":"thinking"}
{"type":"tool","ts":"2026-06-04T10:00:10Z","tool_name":"Grep"}
{"type":"tool","ts":"2026-06-04T10:01:00Z","tool_name":"mcp__conductor__AskUserQuestion"}
{"type":"user","ts":"2026-06-04T10:02:00Z","content":"answer"}
{"type":"user","ts":"2026-06-04T10:05:00Z","content":"go"}
{"type":"footer","closed_at":"2026-06-04T10:05:00Z"}
`
	got := deriveCollabSignals(writeRawJSONL(t, raw))
	if got == nil {
		t.Fatal("expected signals, got nil")
	}
	if got.UserPrompts != 3 {
		t.Errorf("UserPrompts = %d, want 3", got.UserPrompts)
	}
	if got.ToolCalls != 2 {
		t.Errorf("ToolCalls = %d, want 2", got.ToolCalls)
	}
	if got.AgentQuestions != 1 {
		t.Errorf("AgentQuestions = %d, want 1 (AskUserQuestion)", got.AgentQuestions)
	}
	// first user 10:00:00 → last entry 10:05:00 == 300s.
	if got.DurationSeconds != 300 {
		t.Errorf("DurationSeconds = %d, want 300", got.DurationSeconds)
	}
}

// TestDeriveCollabSignals_NoSignalSources verifies the all-empty cases return
// nil so the plan omits the collaboration block rather than writing all-zero.
func TestDeriveCollabSignals_NoSignalSources(t *testing.T) {
	// nil state
	if got := deriveCollabSignals(nil); got != nil {
		t.Errorf("nil state: want nil, got %+v", got)
	}
	// state with no raw.jsonl
	if got := deriveCollabSignals(&session.RecordingState{SessionPath: t.TempDir()}); got != nil {
		t.Errorf("missing raw.jsonl: want nil, got %+v", got)
	}
	// transcript with only assistant turns (nothing countable)
	raw := `{"type":"assistant","ts":"2026-06-04T10:00:00Z","content":"hi"}
{"type":"assistant","ts":"2026-06-04T10:00:05Z","content":"bye"}
`
	if got := deriveCollabSignals(writeRawJSONL(t, raw)); got != nil {
		t.Errorf("only-assistant transcript: want nil, got %+v", got)
	}
}

// TestAppendProducedPlan_NoRecordingNoOp verifies the reverse-link append is a
// safe no-op when there is no live recording for the agent.
// Failure prevented: a plan saved outside a recording errors instead of just
// skipping the reverse link.
func TestAppendProducedPlan_NoRecordingNoOp(t *testing.T) {
	if err := appendProducedPlan(t.TempDir(), "Oxnope", "some-slug"); err != nil {
		t.Errorf("expected no-op nil, got %v", err)
	}
	// empty agent / slug are also no-ops.
	if err := appendProducedPlan(t.TempDir(), "", "s"); err != nil {
		t.Errorf("empty agent: %v", err)
	}
}
