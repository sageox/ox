package main

import (
	"encoding/json"
	"testing"
)

// TestCodexPlanFromStop pins which Codex Stop payloads carry a capturable plan.
// Failure prevented: capturing clarifying plan-mode turns as half-plans, or
// missing a finished plan because Codex misreports permission_mode
// (openai/codex#42881).
func TestCodexPlanFromStop(t *testing.T) {
	mk := func(mode, msg string) []byte {
		b, _ := json.Marshal(map[string]any{"hook_event_name": "Stop", "permission_mode": mode, "last_assistant_message": msg})
		return b
	}
	tests := []struct {
		name     string
		raw      []byte
		wantPlan string
	}{
		{"plan mode with proposed plan", mk("plan", "Here:\n<proposed_plan>\n# Retry\n1. a\n</proposed_plan>\nDone."), "# Retry\n1. a"},
		{"mode misreported, block still captured", mk("default", "<proposed_plan>ship it</proposed_plan>"), "ship it"},
		{"last block wins", mk("plan", "<proposed_plan>v1</proposed_plan> then <proposed_plan>v2</proposed_plan>"), "v2"},
		{"plan mode clarifying question", mk("plan", "Which database should I target?"), ""},
		{"unterminated block", mk("plan", "<proposed_plan>half"), ""},
		{"no last message", []byte(`{"hook_event_name":"Stop","permission_mode":"plan"}`), ""},
		{"not json", []byte("nope"), ""},
		{"empty payload", nil, ""},
		{"close tag without open", mk("plan", "done</proposed_plan>"), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, reason := codexPlanFromStop(tt.raw)
			if got != tt.wantPlan {
				t.Errorf("plan = %q, want %q", got, tt.wantPlan)
			}
			if got == "" && reason == "" {
				t.Error("a skip must say why")
			}
		})
	}
}

// TestMaybeCaptureCodexPlan_ReusesEnrichPathOnce verifies the Codex Stop
// capture goes through the same enrich --persist path as ExitPlanMode, once
// per distinct plan, and clears the prompt-armed unsaved stamp.
// Failure prevented: every Stop re-saving the same plan, or a captured plan
// still triggering "you never saved your plan".
func TestMaybeCaptureCodexPlan_ReusesEnrichPathOnce(t *testing.T) {
	root := t.TempDir()
	calls := 0
	prev := runPlanEnrichment
	runPlanEnrichment = func(planText string) (planJSONResult, bool) {
		calls++
		return planJSONResult{}, true
	}
	t.Cleanup(func() { runPlanEnrichment = prev })

	armUnsavedPlanFromPrompt(root, "Oxcx", []byte(`{"prompt":"p","permission_mode":"plan"}`))
	raw, _ := json.Marshal(map[string]any{"permission_mode": "plan", "last_assistant_message": "<proposed_plan># P\n1. x</proposed_plan>"})
	ctx := &HookContext{AgentType: "codex", ProjectRoot: root, Input: &AgentHookInput{RawBytes: raw}}

	maybeCaptureCodexPlan(ctx, "Oxcx")
	maybeCaptureCodexPlan(ctx, "Oxcx") // the same plan restated on a later Stop
	if calls != 1 {
		t.Errorf("enrich calls = %d, want 1", calls)
	}
	if _, ok := readUnsavedPlanStamp(planUnsavedPath(root, "Oxcx")); ok {
		t.Error("capture must clear the unsaved-plan stamp")
	}

	ctx.AgentType = "claude-code"
	raw2, _ := json.Marshal(map[string]any{"last_assistant_message": "<proposed_plan>other</proposed_plan>"})
	ctx.Input = &AgentHookInput{RawBytes: raw2}
	maybeCaptureCodexPlan(ctx, "Oxcx")
	if calls != 1 {
		t.Error("the Stop capture is Codex-only; Claude Code uses ExitPlanMode")
	}
}

// TestMaybeCaptureCodexPlan_FailedCaptureRetries verifies a failed capture
// leaves no dedupe marker, so the next Stop restating the plan tries again,
// and that a Stop with no plan never reaches enrichment.
// Failure prevented: one transient enrich failure permanently marking a plan
// "captured" that was never saved.
func TestMaybeCaptureCodexPlan_FailedCaptureRetries(t *testing.T) {
	tests := []struct {
		name      string
		msg       string
		agentID   string
		enrichOK  bool
		wantCalls int
	}{
		{name: "failed capture retries on the next Stop", msg: "<proposed_plan># P</proposed_plan>", agentID: "Oxcx", wantCalls: 2},
		{name: "no plan block never enriches", msg: "Which database?", agentID: "Oxcx", enrichOK: true, wantCalls: 0},
		{name: "no agent id is a no-op", msg: "<proposed_plan># P</proposed_plan>", agentID: "", enrichOK: true, wantCalls: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			calls := 0
			prev := runPlanEnrichment
			runPlanEnrichment = func(string) (planJSONResult, bool) {
				calls++
				return planJSONResult{}, tt.enrichOK
			}
			t.Cleanup(func() { runPlanEnrichment = prev })

			raw, _ := json.Marshal(map[string]any{"last_assistant_message": tt.msg})
			ctx := &HookContext{AgentType: "codex", ProjectRoot: root, Input: &AgentHookInput{RawBytes: raw}}
			maybeCaptureCodexPlan(ctx, tt.agentID)
			maybeCaptureCodexPlan(ctx, tt.agentID)
			if calls != tt.wantCalls {
				t.Errorf("enrich calls = %d, want %d", calls, tt.wantCalls)
			}
		})
	}
}
