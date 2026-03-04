package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsValidSessionRecordingMode(t *testing.T) {
	tests := []struct {
		mode  string
		valid bool
	}{
		{SessionRecordingDisabled, true},
		{SessionRecordingManual, true},
		{SessionRecordingAuto, true},
		{"", true}, // empty is valid (inherits)
		{"invalid", false},
		{"DISABLED", false}, // case sensitive
		{"Disabled", false},
	}

	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			assert.Equal(t, tt.valid, IsValidSessionRecordingMode(tt.mode))
		})
	}
}

func TestResolvedSessionRecording_ShouldRecord(t *testing.T) {
	tests := []struct {
		mode   string
		record bool
	}{
		{SessionRecordingDisabled, false},
		{SessionRecordingManual, true},
		{SessionRecordingAuto, true},
		{"", false}, // empty = disabled
	}

	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			resolved := &ResolvedSessionRecording{Mode: tt.mode}
			assert.Equal(t, tt.record, resolved.ShouldRecord())
		})
	}
}

func TestResolveSessionRecording_DefaultsToAuto(t *testing.T) {
	// with no config, should default to manual (opt-in recording)
	tmpDir := t.TempDir()
	userConfigDir := t.TempDir()

	// isolate from real user config
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_CONFIG_HOME", userConfigDir)

	resolved := ResolveSessionRecording(tmpDir)

	assert.Equal(t, SessionRecordingManual, resolved.Mode)
	assert.Equal(t, SessionRecordingSourceDefault, resolved.Source)
}

func TestResolveSessionRecording_ReadsFromProjectConfig(t *testing.T) {
	tmpDir := t.TempDir()
	userConfigDir := t.TempDir()

	// isolate from real user config and env vars
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_CONFIG_HOME", userConfigDir)
	t.Setenv("OX_SESSION_RECORDING", "")

	// create .sageox/config.json with session_recording
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	configContent := `{
		"config_version": "2",
		"session_recording": "auto"
	}`
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte(configContent), 0644))

	resolved := ResolveSessionRecording(tmpDir)

	assert.Equal(t, SessionRecordingAuto, resolved.Mode)
	assert.Equal(t, SessionRecordingSourceRepo, resolved.Source)
}

func TestResolveSessionRecording_EmptyProjectConfig_FallsThrough(t *testing.T) {
	tmpDir := t.TempDir()
	userConfigDir := t.TempDir()

	// isolate from real user config
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_CONFIG_HOME", userConfigDir)

	// create .sageox/config.json without session_recording
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	configContent := `{
		"config_version": "2"
	}`
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte(configContent), 0644))

	resolved := ResolveSessionRecording(tmpDir)

	// should fall through to default since project config has no session_recording
	assert.Equal(t, SessionRecordingManual, resolved.Mode)
	assert.Equal(t, SessionRecordingSourceDefault, resolved.Source)
}

func TestGetSessionRecording(t *testing.T) {
	tmpDir := t.TempDir()
	userConfigDir := t.TempDir()

	// isolate from real user config and env vars
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_CONFIG_HOME", userConfigDir)
	t.Setenv("OX_SESSION_RECORDING", "")

	// create .sageox/config.json with session_recording
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	configContent := `{
		"config_version": "2",
		"session_recording": "manual"
	}`
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte(configContent), 0644))

	mode := GetSessionRecording(tmpDir)
	assert.Equal(t, SessionRecordingManual, mode)
}

func TestSessionsConfig_GetMode(t *testing.T) {
	tests := []struct {
		name     string
		config   *SessionsConfig
		expected string
	}{
		{
			name:     "nil config returns none",
			config:   nil,
			expected: "none",
		},
		{
			name:     "mode set returns mode",
			config:   &SessionsConfig{Mode: "all"},
			expected: "all",
		},
		{
			name:     "enabled true without mode returns all (backward compat)",
			config:   &SessionsConfig{Enabled: boolPtr(true)},
			expected: "all",
		},
		{
			name:     "enabled false without mode returns none",
			config:   &SessionsConfig{Enabled: boolPtr(false)},
			expected: "none",
		},
		{
			name:     "mode takes precedence over enabled",
			config:   &SessionsConfig{Mode: "infra", Enabled: boolPtr(true)},
			expected: "infra",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.config.GetMode())
		})
	}
}

func TestResolveSessionRecording_EnvVarOverridesAll(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		wantMode string
	}{
		{"auto", "auto", SessionRecordingAuto},
		{"disabled", "disabled", SessionRecordingDisabled},
		{"manual", "manual", SessionRecordingManual},
		{"unrecognized normalizes to manual", "bogus", SessionRecordingManual},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			userConfigDir := t.TempDir()

			t.Setenv("OX_XDG_ENABLE", "1")
			t.Setenv("XDG_CONFIG_HOME", userConfigDir)
			t.Setenv("OX_SESSION_RECORDING", tt.envValue)

			// even with project config set to something else, env wins
			sageoxDir := filepath.Join(tmpDir, ".sageox")
			require.NoError(t, os.MkdirAll(sageoxDir, 0755))
			configContent := `{"config_version": "2", "session_recording": "manual"}`
			require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte(configContent), 0644))

			resolved := ResolveSessionRecording(tmpDir)

			assert.Equal(t, tt.wantMode, resolved.Mode)
			assert.Equal(t, SessionRecordingSourceEnv, resolved.Source)
		})
	}
}

func TestResolveSessionRecording_EnvVarDisabledOverridesAutoConfig(t *testing.T) {
	tmpDir := t.TempDir()
	userConfigDir := t.TempDir()

	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_CONFIG_HOME", userConfigDir)
	t.Setenv("OX_SESSION_RECORDING", "disabled")

	// project config says auto
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	configContent := `{"config_version": "2", "session_recording": "auto"}`
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte(configContent), 0644))

	resolved := ResolveSessionRecording(tmpDir)

	assert.Equal(t, SessionRecordingDisabled, resolved.Mode)
	assert.Equal(t, SessionRecordingSourceEnv, resolved.Source)
}

func boolPtr(b bool) *bool {
	return &b
}
