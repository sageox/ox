package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupIsolatedUserConfig points user config at a temp dir so tests don't
// touch the real ~/.config/sageox/config.yaml.
func setupIsolatedUserConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, ".config"))
	sageoxDir := filepath.Join(dir, ".config", "sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0o755))
	return dir
}

func TestUnsetConfigValue_UnknownSetting(t *testing.T) {
	t.Parallel()

	err := UnsetConfigValue("nonexistent", ConfigLevelUser, "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown setting")
}

func TestUnsetConfigValue_UnsupportedLevel(t *testing.T) {
	t.Parallel()

	// telemetry only supports user level
	err := UnsetConfigValue("telemetry", ConfigLevelRepo, "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be set at repo level")
}

func TestUnsetConfigValue_DefaultLevel(t *testing.T) {
	t.Parallel()

	err := UnsetConfigValue("session_recording", ConfigLevelDefault, "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot")
}

func TestUnsetConfigValue_UserLevel_StringSetting(t *testing.T) {
	setupIsolatedUserConfig(t)

	// set a value first
	require.NoError(t, SetConfigValue("murmur_send", "manual", ConfigLevelUser, ""))

	// verify it's set
	cv, err := ResolveConfigValue("murmur_send", "")
	require.NoError(t, err)
	assert.Equal(t, "manual", cv.Value)
	assert.Equal(t, ConfigLevelUser, cv.Source)

	// unset it
	require.NoError(t, UnsetConfigValue("murmur_send", ConfigLevelUser, ""))

	// should fall back to default
	cv, err = ResolveConfigValue("murmur_send", "")
	require.NoError(t, err)
	assert.Equal(t, "auto", cv.Value)
	assert.Equal(t, ConfigLevelDefault, cv.Source)
	assert.Empty(t, cv.UserVal)
}

func TestUnsetConfigValue_UserLevel_BoolSetting(t *testing.T) {
	setupIsolatedUserConfig(t)

	// set telemetry off
	require.NoError(t, SetConfigValue("telemetry", "off", ConfigLevelUser, ""))

	cv, err := ResolveConfigValue("telemetry", "")
	require.NoError(t, err)
	assert.Equal(t, "off", cv.Value)
	assert.Equal(t, ConfigLevelUser, cv.Source)

	// unset
	require.NoError(t, UnsetConfigValue("telemetry", ConfigLevelUser, ""))

	// verify the pointer is nil at storage level
	cfg, err := config.LoadUserConfig()
	require.NoError(t, err)
	assert.Nil(t, cfg.TelemetryEnabled, "TelemetryEnabled should be nil after unset")
}

func TestUnsetConfigValue_ContextGit_PartialUnset(t *testing.T) {
	setupIsolatedUserConfig(t)

	// set both auto_commit and auto_push
	require.NoError(t, SetConfigValue("context_git.auto_commit", "off", ConfigLevelUser, ""))
	require.NoError(t, SetConfigValue("context_git.auto_push", "off", ConfigLevelUser, ""))

	// unset only auto_commit
	require.NoError(t, UnsetConfigValue("context_git.auto_commit", ConfigLevelUser, ""))

	// auto_commit should fall back to default
	cv, err := ResolveConfigValue("context_git.auto_commit", "")
	require.NoError(t, err)
	assert.Equal(t, "on", cv.Value)
	assert.Equal(t, ConfigLevelDefault, cv.Source)

	// auto_push should still be set
	cv, err = ResolveConfigValue("context_git.auto_push", "")
	require.NoError(t, err)
	assert.Equal(t, "off", cv.Value)
	assert.Equal(t, ConfigLevelUser, cv.Source)

	// verify ContextGit struct is NOT nil (auto_push still set)
	cfg, err := config.LoadUserConfig()
	require.NoError(t, err)
	assert.NotNil(t, cfg.ContextGit, "ContextGit struct should survive partial unset")
	assert.Nil(t, cfg.ContextGit.AutoCommit, "AutoCommit should be nil after unset")
	assert.NotNil(t, cfg.ContextGit.AutoPush, "AutoPush should still be set")
}

func TestUnsetConfigValue_ContextGit_FullUnset(t *testing.T) {
	setupIsolatedUserConfig(t)

	// set both
	require.NoError(t, SetConfigValue("context_git.auto_commit", "off", ConfigLevelUser, ""))
	require.NoError(t, SetConfigValue("context_git.auto_push", "off", ConfigLevelUser, ""))

	// unset both
	require.NoError(t, UnsetConfigValue("context_git.auto_commit", ConfigLevelUser, ""))
	require.NoError(t, UnsetConfigValue("context_git.auto_push", ConfigLevelUser, ""))

	// both fall back to default
	for _, key := range []string{"context_git.auto_commit", "context_git.auto_push"} {
		cv, err := ResolveConfigValue(key, "")
		require.NoError(t, err)
		assert.Equal(t, "on", cv.Value, "key=%s", key)
		assert.Equal(t, ConfigLevelDefault, cv.Source, "key=%s", key)
	}

	// ContextGit struct should be nil (both children unset)
	cfg, err := config.LoadUserConfig()
	require.NoError(t, err)
	assert.Nil(t, cfg.ContextGit, "ContextGit should be nil when both children unset")
}

func TestUnsetConfigValue_Noop_WhenNotSet(t *testing.T) {
	setupIsolatedUserConfig(t)

	// unset something that was never set — should not error
	require.NoError(t, UnsetConfigValue("murmur_send", ConfigLevelUser, ""))

	cv, err := ResolveConfigValue("murmur_send", "")
	require.NoError(t, err)
	assert.Equal(t, "auto", cv.Value) // still default
}

func TestUnsetConfigValue_RepoLevel(t *testing.T) {
	setupIsolatedUserConfig(t)

	// create a project dir with config
	dir := t.TempDir()
	sageoxDir := filepath.Join(dir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(sageoxDir, "config.json"),
		[]byte(`{"repo_id":"test","team_id":"test","endpoint":"https://sageox.ai"}`),
		0o644,
	))

	// set at repo level
	require.NoError(t, SetConfigValue("session_recording", "disabled", ConfigLevelRepo, dir))

	cv, err := ResolveConfigValue("session_recording", dir)
	require.NoError(t, err)
	assert.Equal(t, "disabled", cv.RepoVal)

	// unset at repo level
	require.NoError(t, UnsetConfigValue("session_recording", ConfigLevelRepo, dir))

	cv, err = ResolveConfigValue("session_recording", dir)
	require.NoError(t, err)
	assert.Empty(t, cv.RepoVal)
	assert.Equal(t, "auto", cv.Value) // falls back to default
}

func TestUnsetConfigValue_FallsThrough_UserToRepo(t *testing.T) {
	setupIsolatedUserConfig(t)

	// create a project with repo-level setting
	dir := t.TempDir()
	sageoxDir := filepath.Join(dir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(sageoxDir, "config.json"),
		[]byte(`{"repo_id":"test","team_id":"test","endpoint":"https://sageox.ai","session_recording":"manual"}`),
		0o644,
	))

	// set user-level override
	require.NoError(t, SetConfigValue("session_recording", "disabled", ConfigLevelUser, dir))

	// user wins
	cv, err := ResolveConfigValue("session_recording", dir)
	require.NoError(t, err)
	assert.Equal(t, "disabled", cv.Value)
	assert.Equal(t, ConfigLevelUser, cv.Source)

	// unset user level — should fall through to repo
	require.NoError(t, UnsetConfigValue("session_recording", ConfigLevelUser, dir))

	cv, err = ResolveConfigValue("session_recording", dir)
	require.NoError(t, err)
	assert.Equal(t, "manual", cv.Value)
	assert.Equal(t, ConfigLevelRepo, cv.Source)
}

func TestUnsetConfigValue_WithoutProject(t *testing.T) {
	t.Parallel()

	err := UnsetConfigValue("session_recording", ConfigLevelRepo, "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in a SageOx project")

	err = UnsetConfigValue("session_recording", ConfigLevelTeam, "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in a SageOx project")
}
