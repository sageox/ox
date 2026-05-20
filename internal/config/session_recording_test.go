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

	resolved := ResolveSessionRecording(tmpDir, "", "")

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

	resolved := ResolveSessionRecording(tmpDir, "", "")

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

	resolved := ResolveSessionRecording(tmpDir, "", "")

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

			resolved := ResolveSessionRecording(tmpDir, "", "")

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

	resolved := ResolveSessionRecording(tmpDir, "", "")

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

	resolved := ResolveSessionRecording(tmpDir, "", "")
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
	resolved := ResolveSessionRecording(tmpDir, "", "")
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

// --- KB-aware precedence + safety inversion ---
//
// These tests exercise the kbID/kbType code path. They write a real
// .sageox/config.yaml under the XDG_DATA_HOME tree so paths.KBDir resolves
// to a tmp location and the resolver reads the fixture for real.

// writeKBConfig drops a minimal config.yaml under the location paths.KBDir
// will compute given the env. dataHome must be the value of XDG_DATA_HOME.
func writeKBConfig(t *testing.T, dataHome, endpointSlug, kbID, mode string) {
	t.Helper()
	kbDir := filepath.Join(dataHome, "sageox", endpointSlug, "kb", kbID, ".sageox")
	require.NoError(t, os.MkdirAll(kbDir, 0755))
	yaml := "version: 1\nfeatures:\n  session_recording:\n    mode: " + mode + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(kbDir, "config.yaml"), []byte(yaml), 0644))
}

// kbTestEnv isolates user config, endpoint, and KB data home for a test.
// Returns the dataHome path so the test can stage a KB config fixture.
func kbTestEnv(t *testing.T) (tmpDir, dataHome, endpointSlug string) {
	t.Helper()
	tmpDir = t.TempDir()
	userConfigDir := t.TempDir()
	dataHome = t.TempDir()
	endpointSlug = "test.sageox.ai"

	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_CONFIG_HOME", userConfigDir)
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("SAGEOX_ENDPOINT", endpointSlug)
	t.Setenv("OX_SESSION_RECORDING", "")
	return
}

func writeUserMode(t *testing.T, mode string) {
	t.Helper()
	xdg := os.Getenv("XDG_CONFIG_HOME")
	require.NotEmpty(t, xdg, "XDG_CONFIG_HOME must be set first")
	dir := filepath.Join(xdg, "sageox")
	require.NoError(t, os.MkdirAll(dir, 0755))
	body := "sessions:\n  mode: " + mode + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0644))
}

func TestResolveSessionRecording_KB_PrecedenceMatrix(t *testing.T) {
	tests := []struct {
		name          string
		envMode       string
		userMode      string
		kbMode        string
		kbID          string
		kbType        string
		wantMode      string
		wantSource    SessionRecordingSource
		wantInversion bool
	}{
		{
			name:          "env wins over everything",
			envMode:       "auto",
			userMode:      "disabled",
			kbMode:        "disabled",
			kbID:          "kb_test",
			kbType:        "personal",
			wantMode:      SessionRecordingAuto,
			wantSource:    SessionRecordingSourceEnv,
			wantInversion: false,
		},
		{
			name:          "user beats kb",
			userMode:      "auto",
			kbMode:        "manual",
			kbID:          "kb_test",
			kbType:        "personal",
			wantMode:      SessionRecordingAuto,
			wantSource:    SessionRecordingSourceUser,
			wantInversion: false,
		},
		{
			name:          "kb set user unset",
			kbMode:        "auto",
			kbID:          "kb_test",
			kbType:        "personal",
			wantMode:      SessionRecordingAuto,
			wantSource:    SessionRecordingSourceKB,
			wantInversion: false,
		},
		{
			name:          "user disabled vetoes kb auto",
			userMode:      "disabled",
			kbMode:        "auto",
			kbID:          "kb_test",
			kbType:        "personal",
			wantMode:      SessionRecordingDisabled,
			wantSource:    SessionRecordingSourceUser,
			wantInversion: false,
		},
		{
			name:          "kb disabled vetoes user auto",
			userMode:      "auto",
			kbMode:        "disabled",
			kbID:          "kb_test",
			kbType:        "personal",
			wantMode:      SessionRecordingDisabled,
			wantSource:    SessionRecordingSourceKB,
			wantInversion: true,
		},
		{
			name:          "default from personal kb type",
			kbID:          "kb_missing",
			kbType:        "personal",
			wantMode:      SessionRecordingAuto,
			wantSource:    SessionRecordingSourceDefault,
			wantInversion: false,
		},
		{
			name:          "default from profile kb type",
			kbID:          "kb_missing",
			kbType:        "profile",
			wantMode:      SessionRecordingManual,
			wantSource:    SessionRecordingSourceDefault,
			wantInversion: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir, dataHome, ep := kbTestEnv(t)
			if tt.envMode != "" {
				t.Setenv("OX_SESSION_RECORDING", tt.envMode)
			}
			if tt.userMode != "" {
				writeUserMode(t, tt.userMode)
			}
			if tt.kbMode != "" {
				writeKBConfig(t, dataHome, ep, tt.kbID, tt.kbMode)
			}

			resolved := ResolveSessionRecording(tmpDir, tt.kbID, tt.kbType)
			assert.Equal(t, tt.wantMode, resolved.Mode)
			assert.Equal(t, tt.wantSource, resolved.Source)
			assert.Equal(t, tt.wantInversion, resolved.SafetyInversion)
			assert.Equal(t, tt.kbID, resolved.KBID)
			assert.Equal(t, tt.kbType, resolved.KBType)
		})
	}
}

func TestResolveSessionRecording_KB_BoundProjectDefaultsToAutoWhileTypeUnknown(t *testing.T) {
	tmpDir, _, _ := kbTestEnv(t)
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".sageox"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(tmpDir, ".sageox", "config.yaml"),
		[]byte("kb_id: kb_project\nrepo_id: repo_abc\n"),
		0o644,
	))

	resolved := ResolveSessionRecording(tmpDir, "kb_project", "")
	assert.Equal(t, SessionRecordingAuto, resolved.Mode)
	assert.Equal(t, SessionRecordingSourceDefault, resolved.Source)
	assert.Equal(t, "kb_project", resolved.KBID)
}
