package session

import (
	"encoding/json"
	"testing"
	"time"
)

// --- artifacts.go ---

func TestSummarizeResponseToSummaryView_Nil(t *testing.T) {
	result := SummarizeResponseToSummaryView(nil)
	if result != nil {
		t.Errorf("expected nil for nil input, got %+v", result)
	}
}

func TestSummarizeResponseToSummaryView(t *testing.T) {
	resp := &SummarizeResponse{
		Summary:     "test summary",
		KeyActions:  []string{"action1", "action2"},
		Outcome:     "success",
		TopicsFound: []string{"go", "testing"},
		FinalPlan:   "the plan",
		Diagrams:    []string{"graph TD; A-->B"},
	}

	view := SummarizeResponseToSummaryView(resp)
	if view == nil {
		t.Fatal("expected non-nil view")
	}
	if view.Text != "test summary" {
		t.Errorf("Text = %q, want %q", view.Text, "test summary")
	}
	if len(view.KeyActions) != 2 {
		t.Errorf("KeyActions len = %d, want 2", len(view.KeyActions))
	}
	if view.Outcome != "success" {
		t.Errorf("Outcome = %q, want %q", view.Outcome, "success")
	}
	if len(view.TopicsFound) != 2 {
		t.Errorf("TopicsFound len = %d, want 2", len(view.TopicsFound))
	}
	if view.FinalPlan != "the plan" {
		t.Errorf("FinalPlan = %q, want %q", view.FinalPlan, "the plan")
	}
	if len(view.Diagrams) != 1 {
		t.Errorf("Diagrams len = %d, want 1", len(view.Diagrams))
	}
}

func TestWriteSessionArtifacts_NoSummary(t *testing.T) {
	dir := t.TempDir()
	stored := &StoredSession{
		Meta:    &StoreMeta{AgentID: "test"},
		Entries: []map[string]any{},
	}

	paths, err := WriteSessionArtifacts(dir, stored, nil)
	if err != nil {
		t.Fatalf("WriteSessionArtifacts: %v", err)
	}

	// summary paths should be empty when no summary provided
	if paths.SummaryJSON != "" {
		t.Error("SummaryJSON should be empty with nil summary")
	}
	if paths.SummaryMD != "" {
		t.Error("SummaryMD should be empty with nil summary")
	}
	if paths.SessionMD == "" {
		t.Error("SessionMD path should be set")
	}
}

func TestFormatSummarizeGuidanceJSON_NilSummary(t *testing.T) {
	guidance := GetSummarizeGuidance("agent-2", "/tmp/ctx")

	data, err := FormatSummarizeGuidanceJSON("agent-2", guidance, nil)
	if err != nil {
		t.Fatalf("FormatSummarizeGuidanceJSON: %v", err)
	}

	var output SummarizeGuidanceOutput
	if err := json.Unmarshal(data, &output); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if output.Summary != nil {
		t.Error("Summary should be nil")
	}
}

// --- summary_md.go: Generate with topics ---

func TestSummaryMarkdownGenerator_Topics(t *testing.T) {
	gen := NewSummaryMarkdownGenerator()
	summary := &SummaryView{
		Text:        "built tests",
		TopicsFound: []string{"testing", "coverage"},
	}

	md, err := gen.Generate(nil, summary, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	content := string(md)
	if !contains(content, "## Topics") {
		t.Error("missing Topics section")
	}
	if !contains(content, "`testing`") {
		t.Error("missing testing topic tag")
	}
	if !contains(content, "`coverage`") {
		t.Error("missing coverage topic tag")
	}
}

func TestSummaryMarkdownGenerator_NoMetadata(t *testing.T) {
	gen := NewSummaryMarkdownGenerator()
	md, err := gen.Generate(nil, nil, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	content := string(md)
	if !contains(content, "_No metadata available_") {
		t.Error("missing 'No metadata available' fallback")
	}
}

func TestSummaryMarkdownGenerator_FullMetadata(t *testing.T) {
	gen := NewSummaryMarkdownGenerator()
	meta := &StoreMeta{
		CreatedAt:    time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC),
		AgentID:      "Ox1234",
		AgentType:    "claude-code",
		AgentVersion: "1.2.3",
		Model:        "claude-sonnet-4-20250514",
		Username:     "testuser",
	}
	summary := &SummaryView{
		Text:    "test",
		Outcome: "success",
	}

	md, err := gen.Generate(meta, summary, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	content := string(md)
	if !contains(content, "2026-03-15") {
		t.Error("missing date in metadata")
	}
	if !contains(content, "claude-code 1.2.3") {
		t.Error("missing agent type+version")
	}
	if !contains(content, "claude-sonnet-4-20250514") {
		t.Error("missing model")
	}
	if !contains(content, "testuser") {
		t.Error("missing username")
	}
	if !contains(content, "Ox1234") {
		t.Error("missing agent ID")
	}
	if !contains(content, "success") {
		t.Error("missing outcome")
	}
}

// --- signing.go ---

func TestVerifyRedactionSignature(t *testing.T) {
	// just exercise the function - it delegates to signing package
	result := VerifyRedactionSignature()
	// result may vary based on signing state, but should not panic
	_ = result
}
