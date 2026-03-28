package main

import (
	"testing"
)

func TestResolvePhase_Coverage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		agentType string
		event     string
		wantEmpty bool
	}{
		{
			name:      "claude-code SessionStart maps to start phase",
			agentType: "claude-code",
			event:     "SessionStart",
			wantEmpty: false,
		},
		{
			name:      "unknown agent type falls back to cross-agent lookup",
			agentType: "unknown-agent",
			event:     "SessionStart",
			wantEmpty: false, // fallback scans all agents
		},
		{
			name:      "completely unknown event",
			agentType: "claude-code",
			event:     "nonexistent_event_xyz",
			wantEmpty: true,
		},
		{
			name:      "empty event",
			agentType: "claude-code",
			event:     "",
			wantEmpty: true,
		},
		{
			name:      "empty agent type with unknown event",
			agentType: "",
			event:     "nonexistent_event_xyz",
			wantEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := resolvePhase(tt.agentType, tt.event)
			if tt.wantEmpty && got != "" {
				t.Errorf("resolvePhase(%q, %q) = %q, want empty", tt.agentType, tt.event, got)
			}
			if !tt.wantEmpty && got == "" {
				t.Errorf("resolvePhase(%q, %q) = empty, want non-empty", tt.agentType, tt.event)
			}
		})
	}
}

func TestDispatchPhase_UnknownPhase(t *testing.T) {
	t.Parallel()
	ctx := &HookContext{
		Phase:     "nonexistent_phase",
		AgentType: "claude-code",
	}
	err := dispatchPhase(ctx)
	if err != nil {
		t.Errorf("dispatchPhase with unknown phase should return nil, got: %v", err)
	}
}

func TestActivePhaseBehavior_Coverage(t *testing.T) {
	t.Parallel()
	// verify the known active phases are registered
	expectedActive := []string{phaseStart, phaseCompact, phaseAfterTool, phaseStop, phasePrompt}
	for _, phase := range expectedActive {
		if !activePhaseBehavior[phase] {
			t.Errorf("expected phase %q to be active", phase)
		}
	}

	// verify some phases are NOT active
	expectedInactive := []string{phaseEnd, phaseBeforeTool}
	for _, phase := range expectedInactive {
		if activePhaseBehavior[phase] {
			t.Errorf("expected phase %q to be inactive", phase)
		}
	}
}
