package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsValidMemoryImportMode(t *testing.T) {
	tests := []struct {
		mode  string
		valid bool
	}{
		{MemoryImportDisabled, true},
		{MemoryImportManual, true},
		{MemoryImportAuto, true},
		{"", true},
		{"invalid", false},
		{"MANUAL", false},
	}

	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			assert.Equal(t, tt.valid, IsValidMemoryImportMode(tt.mode))
		})
	}
}

func TestNormalizeMemoryImport(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"manual", "manual"},
		{"auto", "auto"},
		{"disabled", "disabled"},
		{"", "manual"},       // default
		{"invalid", "manual"}, // default
		{"MANUAL", "manual"},  // default
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, NormalizeMemoryImport(tt.input))
		})
	}
}

func TestIsMemoryImportEnabled(t *testing.T) {
	root := CreateInitializedProject(t)
	cfg, err := LoadProjectConfig(root)
	require.NoError(t, err)

	// default (empty) = manual = enabled
	assert.True(t, IsMemoryImportEnabled(root))

	// explicit manual = enabled
	cfg.MemoryImport = "manual"
	require.NoError(t, SaveProjectConfig(root, cfg))
	assert.True(t, IsMemoryImportEnabled(root))

	// auto = enabled
	cfg.MemoryImport = "auto"
	require.NoError(t, SaveProjectConfig(root, cfg))
	assert.True(t, IsMemoryImportEnabled(root))

	// disabled = not enabled
	cfg.MemoryImport = "disabled"
	require.NoError(t, SaveProjectConfig(root, cfg))
	assert.False(t, IsMemoryImportEnabled(root))
}

func TestIsMemoryImportAuto(t *testing.T) {
	root := CreateInitializedProject(t)
	cfg, err := LoadProjectConfig(root)
	require.NoError(t, err)

	cfg.MemoryImport = "auto"
	require.NoError(t, SaveProjectConfig(root, cfg))
	assert.True(t, IsMemoryImportAuto(root))

	cfg.MemoryImport = "manual"
	require.NoError(t, SaveProjectConfig(root, cfg))
	assert.False(t, IsMemoryImportAuto(root))

	// default (empty) = manual, not auto
	cfg.MemoryImport = ""
	require.NoError(t, SaveProjectConfig(root, cfg))
	assert.False(t, IsMemoryImportAuto(root))
}
