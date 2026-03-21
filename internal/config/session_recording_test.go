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

func TestResolveSessionRecording_NoProjectConfig_DefaultsToManual(t *testing.T) {
	// no .sageox/ at all — not an ox-initialized repo, default to manual
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

func TestResolveSessionRecording_EmptyProjectConfig_DefaultsToAuto(t *testing.T) {
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

	// ox-initialized repo with no explicit setting defaults to auto
	assert.Equal(t, SessionRecordingAuto, resolved.Mode)
	assert.Equal(t, SessionRecordingSourceRepo, resolved.Source)
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

func TestResolveSessionRecording_UserOverridesProject(t *testing.T) {
	tmpDir := t.TempDir()
	userConfigDir := t.TempDir()

	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_CONFIG_HOME", userConfigDir)
	t.Setenv("OX_SESSION_RECORDING", "")

	// project config says auto
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	require.NoError(t, os.WriteFile(
		filepath.Join(sageoxDir, "config.json"),
		[]byte(`{"config_version": "2", "session_recording": "auto"}`),
		0644,
	))

	// user config says disabled — should win over project
	sageoxUserDir := filepath.Join(userConfigDir, "sageox")
	require.NoError(t, os.MkdirAll(sageoxUserDir, 0755))
	require.NoError(t, os.WriteFile(
		filepath.Join(sageoxUserDir, "config.yaml"),
		[]byte("sessions:\n  mode: disabled\n"),
		0644,
	))

	resolved := ResolveSessionRecording(tmpDir)
	// NormalizeSessionRecording maps "disabled" → "disabled"
	// but sessions.GetMode() returns "disabled" for mode: disabled
	// The function checks if mode != "" && mode != "none"
	// "disabled" is not "" and not "none", so it enters the user branch
	// NormalizeSessionRecording("disabled") → "disabled"
	assert.Equal(t, SessionRecordingDisabled, resolved.Mode)
	assert.Equal(t, SessionRecordingSourceUser, resolved.Source)
}

func TestResolveSessionRecording_DefaultIsManual(t *testing.T) {
	tmpDir := t.TempDir()
	userConfigDir := t.TempDir()

	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_CONFIG_HOME", userConfigDir)
	t.Setenv("OX_SESSION_RECORDING", "")

	// no .sageox/ project config, no user config → should default to manual
	resolved := ResolveSessionRecording(tmpDir)
	assert.Equal(t, SessionRecordingManual, resolved.Mode)
	assert.Equal(t, SessionRecordingSourceDefault, resolved.Source)
}

func TestNormalizeSessionRecording_LegacyNone(t *testing.T) {
	assert.Equal(t, SessionRecordingDisabled, NormalizeSessionRecording("none"))
}

func TestResolvedSessionRecording_IsAuto(t *testing.T) {
	assert.True(t, (&ResolvedSessionRecording{Mode: SessionRecordingAuto}).IsAuto())
	assert.False(t, (&ResolvedSessionRecording{Mode: SessionRecordingManual}).IsAuto())
}

func TestResolvedSessionRecording_IsManual(t *testing.T) {
	assert.True(t, (&ResolvedSessionRecording{Mode: SessionRecordingManual}).IsManual())
	assert.False(t, (&ResolvedSessionRecording{Mode: SessionRecordingAuto}).IsManual())
}

func TestIsValidSessionPublishingMode(t *testing.T) {
	assert.True(t, IsValidSessionPublishingMode("auto"))
	assert.True(t, IsValidSessionPublishingMode("manual"))
	assert.True(t, IsValidSessionPublishingMode(""))
	assert.False(t, IsValidSessionPublishingMode("invalid"))
}

func TestNormalizeSessionPublishing(t *testing.T) {
	assert.Equal(t, SessionPublishingAuto, NormalizeSessionPublishing("auto"))
	assert.Equal(t, SessionPublishingManual, NormalizeSessionPublishing("manual"))
	assert.Equal(t, SessionPublishingAuto, NormalizeSessionPublishing(""))
	assert.Equal(t, SessionPublishingAuto, NormalizeSessionPublishing("bogus"))
}

func boolPtr(b bool) *bool {
	return &b
}
