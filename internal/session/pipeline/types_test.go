package pipeline

import (
	"encoding/json"
	"testing"
)

func TestLedgerFileConstants(t *testing.T) {
	// verify constants have expected values (guards against accidental changes)
	tests := []struct {
		name     string
		got      string
		expected string
	}{
		{"LedgerFileRaw", LedgerFileRaw, "raw.jsonl"},
		{"LedgerFileSummaryMD", LedgerFileSummaryMD, "summary.md"},
		{"LedgerFileSessionMD", LedgerFileSessionMD, "session.md"},
		{"LedgerFilePlan", LedgerFilePlan, "plan.md"},
		{"LedgerFileContextTrace", LedgerFileContextTrace, "context-trace.jsonl"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.expected {
				t.Errorf("got %q, want %q", tt.got, tt.expected)
			}
		})
	}
}

func TestResultSecondaryArtifacts(t *testing.T) {
	r := &Result{
		SummaryMDPath:    "/tmp/summary.md",
		SessionMDPath:    "/tmp/session.md",
		PlanPath:         "/tmp/plan.md",
		ContextTracePath: "/tmp/context-trace.jsonl",
	}

	artifacts := r.SecondaryArtifacts()

	if artifacts[LedgerFileSummaryMD] != "/tmp/summary.md" {
		t.Errorf("SummaryMD: got %q", artifacts[LedgerFileSummaryMD])
	}
	if artifacts[LedgerFileSessionMD] != "/tmp/session.md" {
		t.Errorf("SessionMD: got %q", artifacts[LedgerFileSessionMD])
	}
	if artifacts[LedgerFilePlan] != "/tmp/plan.md" {
		t.Errorf("Plan: got %q", artifacts[LedgerFilePlan])
	}
	if artifacts[LedgerFileContextTrace] != "/tmp/context-trace.jsonl" {
		t.Errorf("ContextTrace: got %q", artifacts[LedgerFileContextTrace])
	}
}

func TestResultSecondaryArtifactsEmpty(t *testing.T) {
	r := &Result{}
	artifacts := r.SecondaryArtifacts()

	for name, path := range artifacts {
		if path != "" {
			t.Errorf("expected empty path for %q, got %q", name, path)
		}
	}
}

func TestStartOutputJSONTags(t *testing.T) {
	out := StartOutput{
		Success: true,
		Type:    "session_start",
		AgentID: "agent-123",
		Adapter: "claude-code",
		Started: "2026-03-26T10:00:00Z",
	}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}

	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}

	// verify key JSON field names
	if m["success"] != true {
		t.Error("expected success=true")
	}
	if m["type"] != "session_start" {
		t.Errorf("expected type=session_start, got %v", m["type"])
	}
	if m["agent_id"] != "agent-123" {
		t.Errorf("expected agent_id=agent-123, got %v", m["agent_id"])
	}
	// omitempty fields should be absent
	if _, ok := m["title"]; ok {
		t.Error("title should be omitted when empty")
	}
	if _, ok := m["notice"]; ok {
		t.Error("notice should be omitted when empty")
	}
}

func TestStopOutputJSONTags(t *testing.T) {
	out := StopOutput{
		Success:  true,
		Type:     "session_stop",
		AgentID:  "agent-456",
		Duration: "5m",
		Timing:   map[string]int64{"process_ms": 100},
	}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}

	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}

	if m["duration"] != "5m" {
		t.Errorf("expected duration=5m, got %v", m["duration"])
	}
	timing := m["timing"].(map[string]any)
	if timing["process_ms"] != float64(100) {
		t.Errorf("expected timing.process_ms=100, got %v", timing["process_ms"])
	}
	// omitempty fields should be absent
	if _, ok := m["raw_path"]; ok {
		t.Error("raw_path should be omitted when empty")
	}
}

func TestRemindOutputJSONTags(t *testing.T) {
	out := RemindOutput{
		Success: true,
		Type:    "session_remind",
		AgentID: "agent-789",
		Message: "recording active",
	}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}

	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}

	if m["message"] != "recording active" {
		t.Errorf("expected message, got %v", m["message"])
	}
}

func TestStopOutputContextTraceJSONTag(t *testing.T) {
	out := StopOutput{
		Success:          true,
		Type:             "session_stop",
		AgentID:          "agent-ct",
		Duration:         "3m",
		ContextTracePath: "/ledger/sessions/s1/context-trace.jsonl",
	}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}

	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}

	if m["context_trace_path"] != "/ledger/sessions/s1/context-trace.jsonl" {
		t.Errorf("context_trace_path: got %v", m["context_trace_path"])
	}
}

func TestStopOutputContextTraceOmitEmpty(t *testing.T) {
	out := StopOutput{
		Success: true,
		Type:    "session_stop",
		AgentID: "agent-ct",
	}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}

	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}

	if _, ok := m["context_trace_path"]; ok {
		t.Error("context_trace_path should be omitted when empty")
	}
}
