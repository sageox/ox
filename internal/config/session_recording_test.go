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

// writeUserSessionPublishing writes a top-level session_publishing key to
// the isolated user config.yaml. XDG_CONFIG_HOME must already be set (see
// kbTestEnv).
func writeUserSessionPublishing(t *testing.T, mode string) {
	t.Helper()
	xdg := os.Getenv("XDG_CONFIG_HOME")
	require.NotEmpty(t, xdg, "XDG_CONFIG_HOME must be set first")
	dir := filepath.Join(xdg, "sageox")
	require.NoError(t, os.MkdirAll(dir, 0755))
	body := "session_publishing: " + mode + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0644))
}

// TestResolveSessionPublishing_PrecedenceMatrix pins the precedence chain
// for session_publishing (contract D13, bead ox-6p5y.13):
// OX_SESSION_PUBLISHING > user config > repo config > default(auto),
// mirroring ResolveSessionRecording's env-wins / user-beats-repo rules
// exactly. Before the user-config layer existed, ResolveSessionPublishing
// consulted project config only — 'ox config set session_publishing manual'
// at the user level would report success but have zero effect on whether
// the transcript uploads, reproducing exactly the "setting lists but
// doesn't set" defect class fixed for context_git.auto_push/auto_commit
// (bead ox-6p5y.11). The env var is the pipeline/automation escape hatch
// (D13), approved after an initial "no new OX_* var without review" hold.
//
// Run red-first against the pre-fix resolver (project-config-only, no user
// or env layer): every "user" and "env" case here fails because those
// layers are never consulted. Failure prevented: a user-level publishing
// preference, or a pipeline's env override, silently not taking effect.
func TestResolveSessionPublishing_PrecedenceMatrix(t *testing.T) {
	tests := []struct {
		name        string
		envMode     string
		userMode    string
		projectMode string
		wantMode    string
		wantSource  SessionRecordingSource
	}{
		{name: "env wins over everything", envMode: "manual", userMode: "auto", projectMode: "auto", wantMode: SessionPublishingManual, wantSource: SessionRecordingSourceEnv},
		{name: "env-only", envMode: "manual", wantMode: SessionPublishingManual, wantSource: SessionRecordingSourceEnv},
		{name: "user-only", userMode: "manual", wantMode: SessionPublishingManual, wantSource: SessionRecordingSourceUser},
		{name: "repo-only", projectMode: "manual", wantMode: SessionPublishingManual, wantSource: SessionRecordingSourceRepo},
		{name: "user overrides repo", userMode: "manual", projectMode: "auto", wantMode: SessionPublishingManual, wantSource: SessionRecordingSourceUser},
		{name: "neither set defaults to auto", wantMode: SessionPublishingAuto, wantSource: SessionRecordingSourceDefault},
		{name: "invalid user value normalizes to auto but source stays user", userMode: "bogus", wantMode: SessionPublishingAuto, wantSource: SessionRecordingSourceUser},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir, _, _ := kbTestEnv(t)
			if tt.projectMode != "" {
				require.NoError(t, SaveProjectConfig(tmpDir, &ProjectConfig{RepoID: "repo_x", SessionPublishing: tt.projectMode}))
			}
			if tt.userMode != "" {
				writeUserSessionPublishing(t, tt.userMode)
			}
			if tt.envMode != "" {
				t.Setenv(EnvSessionPublishing, tt.envMode)
			}
			resolved := ResolveSessionPublishing(tmpDir)
			assert.Equal(t, tt.wantMode, resolved.Mode)
			assert.Equal(t, tt.wantSource, resolved.Source)
		})
	}
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

// --- ox ADR-028 / epic ox-6hvs regression matrix ---
//
// Sessions record into the project ledger; Knowledge Bubbles play no part in
// recording resolution. These tests pin two behaviors:
//  1. the full precedence chain (env > user > project > team > default) for
//     projects, byte-identical to the pre-ADR-017 resolver, and
//  2. a project whose .sageox/config.yaml still carries a kb_id key (written
//     by the abandoned ADR-017 migration) resolves EXACTLY like one without —
//     the key is ignored, and no KB-side config can veto or enable recording.
// Failure prevented: a stale kb_id binding silently changing whether a
// coding session is recorded (privacy surface).

func TestResolveSessionRecording_PrecedenceMatrix(t *testing.T) {
	tests := []struct {
		name        string
		envMode     string
		userMode    string
		projectMode string
		wantMode    string
		wantSource  SessionRecordingSource
	}{
		{name: "env wins over everything", envMode: "manual", userMode: "disabled", projectMode: "auto", wantMode: SessionRecordingManual, wantSource: SessionRecordingSourceEnv},
		{name: "user beats project", userMode: "disabled", projectMode: "auto", wantMode: SessionRecordingDisabled, wantSource: SessionRecordingSourceUser},
		{name: "project set user unset", projectMode: "manual", wantMode: SessionRecordingManual, wantSource: SessionRecordingSourceRepo},
		{name: "initialized default is auto", wantMode: SessionRecordingAuto, wantSource: SessionRecordingSourceRepo},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir, _, _ := kbTestEnv(t)
			RequireSageoxDir(t, tmpDir)
			cfg := &ProjectConfig{RepoID: "repo_x", SessionRecording: tt.projectMode}
			require.NoError(t, SaveProjectConfig(tmpDir, cfg))
			if tt.userMode != "" {
				writeUserMode(t, tt.userMode)
			}
			if tt.envMode != "" {
				t.Setenv("OX_SESSION_RECORDING", tt.envMode)
			}
			resolved := ResolveSessionRecording(tmpDir)
			assert.Equal(t, tt.wantMode, resolved.Mode)
			assert.Equal(t, tt.wantSource, resolved.Source)
		})
	}
}

func TestResolveSessionRecording_StaleKBBindingIsIgnored(t *testing.T) {
	// Two identical initialized projects; one additionally carries a
	// .sageox/config.yaml with a kb_id (the abandoned ADR-017 binding) AND a
	// staged KB-side config.yaml that says "disabled" — the exact setup that
	// would have vetoed recording under the old resolver. Resolution must be
	// identical for both projects.
	resolve := func(withBinding bool) *ResolvedSessionRecording {
		tmpDir, dataHome, endpointSlug := kbTestEnv(t)
		RequireSageoxDir(t, tmpDir)
		// stage a vetoing KB-side config at the location the old resolver
		// would have consulted — it must have no effect now.
		writeKBConfig(t, dataHome, endpointSlug, "kb_stale_binding", "disabled")
		require.NoError(t, SaveProjectConfig(tmpDir, &ProjectConfig{RepoID: "repo_x"}))
		if withBinding {
			yamlBody := "kb_id: kb_stale_binding\nrepo_id: repo_x\n"
			require.NoError(t, os.WriteFile(
				filepath.Join(tmpDir, sageoxDir, projectConfigYAMLFilename),
				[]byte(yamlBody), 0o644))
		}
		return ResolveSessionRecording(tmpDir)
	}

	without := resolve(false)
	with := resolve(true)
	assert.Equal(t, without.Mode, with.Mode, "stale kb_id binding must not change the recording mode")
	assert.Equal(t, without.Source, with.Source, "stale kb_id binding must not change the recording source")
}
