package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsValidMurmuringMode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		mode  string
		valid bool
	}{
		{MurmuringOff, true},
		{MurmuringAuto, true},
		{"", true},
		{"invalid", false},
		{"on", false},
		{"enabled", false},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.valid, IsValidMurmuringMode(tt.mode))
		})
	}
}

func TestNormalizeMurmuring(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
	}{
		{MurmuringOff, MurmuringOff},
		{MurmuringAuto, MurmuringAuto},
		{"", MurmuringOff},
		{"invalid", MurmuringOff},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, NormalizeMurmuring(tt.input))
		})
	}
}

func TestResolveMurmuring_Default(t *testing.T) {
	got := ResolveMurmuring("")
	assert.Equal(t, MurmuringOff, got)
}

func TestResolveMurmuring_NoConfig(t *testing.T) {
	dir := t.TempDir()
	got := ResolveMurmuring(dir)
	assert.Equal(t, MurmuringOff, got)
}

func TestMurmuringEnabled_Default(t *testing.T) {
	assert.False(t, MurmuringEnabled(""))
}
