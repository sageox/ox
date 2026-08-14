package prime

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestClassifyAgentTier verifies plan-enrichment tier classification, including
// canonicalization of display names/aliases and the unknown-agent baseline.
// Failure prevented: an agent landing in the wrong tier and being shown
// guidance its lifecycle can't honor (or losing the baseline command entirely).
func TestClassifyAgentTier(t *testing.T) {
	tests := []struct {
		agentType string
		want      AgentTier
	}{
		{"claude-code", TierGold},
		{"Claude Code", TierGold},
		{"claudecode", TierGold},
		{"codex", TierSilver},
		{"gemini", TierSilver},
		{"gemini-cli", TierSilver},
		{"amp", TierBronze},
		{"opencode", TierBronze},
		{"pi", TierBronze},
		{"", TierUnknown},
		{"some-future-agent", TierUnknown},
		{"cursor", TierUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			assert.Equal(t, tt.want, ClassifyAgentTier(tt.agentType),
				"ClassifyAgentTier(%q)", tt.agentType)
		})
	}
}

func TestCanonicalAgentType(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty", input: "", want: ""},
		{name: "claude-code", input: "claude-code", want: "claude"},
		{name: "claudecode no dash", input: "claudecode", want: "claude"},
		{name: "claude code with space", input: "claude code", want: "claude"},
		{name: "codex", input: "codex", want: "codex"},
		{name: "uppercase claude", input: "Claude-Code", want: "claude"},
		{name: "whitespace padding", input: "  claude-code  ", want: "claude"},
		{name: "unknown agent", input: "some-unknown-agent", want: "some-unknown-agent"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CanonicalAgentType(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIsAgentSupported(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{name: "claude-code supported", input: "claude-code", want: true},
		{name: "codex supported", input: "codex", want: true},
		{name: "empty unsupported", input: "", want: false},
		{name: "unknown unsupported", input: "vim-copilot", want: false},
		{name: "case insensitive", input: "Claude-Code", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsAgentSupported(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGetAgentSupportNotice(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantEmpty bool
		contains  string
	}{
		{name: "supported returns empty", input: "claude-code", wantEmpty: true},
		{name: "codex returns empty", input: "codex", wantEmpty: true},
		{name: "empty returns generic notice", input: "", contains: "unknown if this agent"},
		{name: "unknown agent returns specific notice", input: "cursor", contains: "explicitly designed for use with Claude Code"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetAgentSupportNotice(tt.input)
			if tt.wantEmpty {
				assert.Empty(t, got)
			} else {
				assert.Contains(t, got, tt.contains)
			}
		})
	}
}

func TestCodexLifecycleNotification(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantEmpty bool
	}{
		{name: "codex returns notification", input: "codex", wantEmpty: false},
		{name: "claude-code returns empty", input: "claude-code", wantEmpty: true},
		{name: "empty returns empty", input: "", wantEmpty: true},
		{name: "unknown returns empty", input: "cursor", wantEmpty: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CodexLifecycleNotification(tt.input)
			if tt.wantEmpty {
				assert.Empty(t, got)
			} else {
				assert.Contains(t, got, "hooks via .codex/hooks.json")
				assert.NotContains(t, got, "codex_hooks")
			}
		})
	}
}
