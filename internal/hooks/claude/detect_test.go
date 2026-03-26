package claude

import (
	"testing"

	"github.com/sageox/ox/internal/constants"
	"github.com/stretchr/testify/assert"
)

func TestIsOxPrimeCommand(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want bool
	}{
		{"claude code prime command", constants.OxPrimeCommandClaudeCode, true},
		{"legacy prime command", constants.OxPrimeCommand, true},
		{"idempotent prime command", constants.OxPrimeCommandClaudeCodeIdempotent, true},
		{"gemini prime command", constants.OxPrimeCommandGemini, true},
		{"bare ox agent prime", "ox agent prime", true},
		{"wrapped ox agent prime", "if command -v ox; then ox agent prime; fi", true},
		{"ox agent hook command", "ox agent hook SessionStart", false},
		{"unrelated command", "echo hello", false},
		{"empty string", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsOxPrimeCommand(tt.cmd))
		})
	}
}

func TestIsOxHookCommand(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want bool
	}{
		{"session start hook", "ox agent hook SessionStart", true},
		{"stop hook", "ox agent hook Stop", true},
		{"wrapped hook command", "if command -v ox; then ox agent hook Stop 2>&1 || true; fi", true},
		{"with agent env", "AGENT_ENV=claude-code ox agent hook PreCompact", true},
		{"prime command", "ox agent prime", false},
		{"unrelated command", "echo hello", false},
		{"empty string", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsOxHookCommand(tt.cmd))
		})
	}
}

func TestIsAnyOxCommand(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want bool
	}{
		{"prime command", constants.OxPrimeCommandClaudeCode, true},
		{"hook command", "ox agent hook SessionStart", true},
		{"legacy prime", constants.OxPrimeCommand, true},
		{"unrelated command", "echo hello", false},
		{"empty string", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsAnyOxCommand(tt.cmd))
		})
	}
}
