package session

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSessionFilterMode_IsValid(t *testing.T) {
	tests := []struct {
		mode  SessionFilterMode
		valid bool
	}{
		{SessionFilterModeNone, true},
		{SessionFilterModeAll, true},
		{"", true}, // empty is valid (inherits)
		{"invalid", false},
		{"NONE", false}, // case sensitive
	}

	for _, tt := range tests {
		t.Run(string(tt.mode), func(t *testing.T) {
			assert.Equal(t, tt.valid, tt.mode.IsValid())
		})
	}
}

func TestSessionFilterMode_ShouldRecord(t *testing.T) {
	tests := []struct {
		mode   SessionFilterMode
		record bool
	}{
		{SessionFilterModeNone, false},
		{SessionFilterModeAll, true},
		{"", false}, // empty = none
	}

	for _, tt := range tests {
		t.Run(string(tt.mode), func(t *testing.T) {
			assert.Equal(t, tt.record, tt.mode.ShouldRecord())
		})
	}
}

func TestIsNoiseCommand(t *testing.T) {
	noiseCmds := []string{
		"ls",
		"ls -la",
		"pwd",
		"cat file.txt",
		"head -n 10",
		"echo hello",
	}

	meaningfulCmds := []string{
		"go build",
		"npm install",
		"git commit",
	}

	for _, cmd := range noiseCmds {
		t.Run(cmd, func(t *testing.T) {
			assert.True(t, IsNoiseCommand(cmd), "expected %q to be noise", cmd)
		})
	}

	for _, cmd := range meaningfulCmds {
		t.Run(cmd, func(t *testing.T) {
			assert.False(t, IsNoiseCommand(cmd), "expected %q to NOT be noise", cmd)
		})
	}
}
