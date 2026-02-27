//go:build !short

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolvePhase(t *testing.T) {
	tests := []struct {
		name      string
		agentType string
		event     string
		want      string
	}{
		{"claude SessionStart", "claude-code", "SessionStart", phaseStart},
		{"claude SessionEnd", "claude-code", "SessionEnd", phaseEnd},
		{"claude PreToolUse", "claude-code", "PreToolUse", phaseBeforeTool},
		{"claude PostToolUse", "claude-code", "PostToolUse", phaseAfterTool},
		{"claude UserPromptSubmit", "claude-code", "UserPromptSubmit", phasePrompt},
		{"claude Stop", "claude-code", "Stop", phaseStop},
		{"claude PreCompact", "claude-code", "PreCompact", phaseCompact},
		{"claude unknown event", "claude-code", "SubagentStop", ""},
		{"claude alias resolves", "claudecode", "SessionStart", phaseStart},
		{"claude short alias resolves", "claude", "SessionStart", phaseStart},
		{"unknown agent falls back", "codex", "SessionStart", phaseStart},
		{"unknown agent unknown event", "codex", "FooBar", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolvePhase(tt.agentType, tt.event)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestActivePhaseBehavior(t *testing.T) {
	// phases with behavior
	assert.True(t, activePhaseBehavior[phaseStart])
	assert.True(t, activePhaseBehavior[phaseCompact])

	// noop phases
	assert.False(t, activePhaseBehavior[phaseEnd])
	assert.False(t, activePhaseBehavior[phaseBeforeTool])
	assert.False(t, activePhaseBehavior[phasePrompt])
	assert.False(t, activePhaseBehavior[phaseStop])
}

func TestDispatchPhase_NoopPhases(t *testing.T) {
	ctx := &HookContext{
		AgentType:   "claude-code",
		ProjectRoot: t.TempDir(),
	}

	// noop phases should return nil
	for _, phase := range []string{phaseEnd, phaseBeforeTool, phaseAfterTool, phasePrompt, phaseStop} {
		ctx.Phase = phase
		err := dispatchPhase(ctx)
		assert.NoError(t, err, "phase %s should be noop", phase)
	}
}

func TestRunAgentHook_NoArgs(t *testing.T) {
	err := runAgentHook([]string{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "usage")
}
