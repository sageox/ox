package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetSetting_KnownKeys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		key      string
		wantNil  bool
		category string
	}{
		{"session_recording", false, "Sessions"},
		{"telemetry", false, "Privacy"},
		{"tips", false, "Display"},
		{"context_git.auto_commit", false, "Sessions"},
		{"context_git.auto_push", false, "Sessions"},
		{"view_format", false, "Display"},
		{"agent_worker", false, "Sessions"},
		{"nonexistent_setting", true, ""},
		{"", true, ""},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			t.Parallel()
			setting := GetSetting(tt.key)
			if tt.wantNil {
				assert.Nil(t, setting)
			} else {
				require.NotNil(t, setting)
				assert.Equal(t, tt.key, setting.Key)
				assert.Equal(t, tt.category, setting.Category)
				assert.NotEmpty(t, setting.Description)
				assert.NotEmpty(t, setting.Default)
				assert.NotEmpty(t, setting.Levels)
			}
		})
	}
}

func TestGetSetting_ValidValuesNotEmpty(t *testing.T) {
	t.Parallel()

	for _, setting := range AllSettings {
		t.Run(setting.Key, func(t *testing.T) {
			t.Parallel()
			// free-text settings (e.g., attribution.commit) accept any string;
			// skip enumerated-value checks for them
			if len(setting.ValidValues) == 0 {
				return
			}
			assert.NotEmpty(t, setting.ValidValues,
				"setting %q should have valid values defined", setting.Key)

			// default value should be in valid values
			found := false
			for _, v := range setting.ValidValues {
				if v == setting.Default {
					found = true
					break
				}
			}
			assert.True(t, found,
				"default %q for setting %q should be in valid values %v",
				setting.Default, setting.Key, setting.ValidValues)
		})
	}
}

func TestSetConfigValue_UnknownSetting(t *testing.T) {
	t.Parallel()

	err := SetConfigValue("totally_fake", "value", ConfigLevelUser, "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown setting")
}

func TestSetConfigValue_InvalidValue(t *testing.T) {
	t.Parallel()

	err := SetConfigValue("session_recording", "invalid_mode", ConfigLevelUser, "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid value")
	assert.Contains(t, err.Error(), "valid values")
}

func TestSetConfigValue_UnsupportedLevel(t *testing.T) {
	t.Parallel()

	// telemetry only supports user level
	err := SetConfigValue("telemetry", "on", ConfigLevelRepo, "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be set at repo level")
}

func TestSetConfigValue_DefaultLevel(t *testing.T) {
	t.Parallel()

	err := SetConfigValue("session_recording", "auto", ConfigLevelDefault, "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be set at default level")
}

func TestSetConfigValue_WithoutProject(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		level ConfigLevel
	}{
		{"repo level", ConfigLevelRepo},
		{"team level", ConfigLevelTeam},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := SetConfigValue("session_recording", "auto", tt.level, "")
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "not in a SageOx project")
		})
	}
}

func TestResolveConfigValue_UnknownSetting(t *testing.T) {
	t.Parallel()

	cv, err := ResolveConfigValue("nonexistent", "")
	assert.Error(t, err)
	assert.Nil(t, cv)
	assert.Contains(t, err.Error(), "unknown setting")
}

func TestContainsLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		levels []ConfigLevel
		target ConfigLevel
		want   bool
	}{
		{"found in list", []ConfigLevel{ConfigLevelUser, ConfigLevelRepo}, ConfigLevelUser, true},
		{"not in list", []ConfigLevel{ConfigLevelUser}, ConfigLevelRepo, false},
		{"empty list", []ConfigLevel{}, ConfigLevelUser, false},
		{"nil list", nil, ConfigLevelUser, false},
		{"single match", []ConfigLevel{ConfigLevelTeam}, ConfigLevelTeam, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, containsLevel(tt.levels, tt.target))
		})
	}
}

func TestAllSettings_UniqueKeys(t *testing.T) {
	t.Parallel()

	seen := make(map[string]bool)
	for _, s := range AllSettings {
		assert.False(t, seen[s.Key], "duplicate setting key: %s", s.Key)
		seen[s.Key] = true
	}
}

func TestAllSettings_LevelsNonEmpty(t *testing.T) {
	t.Parallel()

	for _, s := range AllSettings {
		assert.NotEmpty(t, s.Levels, "setting %q must have at least one level", s.Key)
	}
}

func TestConfigLevel_Constants(t *testing.T) {
	t.Parallel()

	assert.Equal(t, ConfigLevel("user"), ConfigLevelUser)
	assert.Equal(t, ConfigLevel("repo"), ConfigLevelRepo)
	assert.Equal(t, ConfigLevel("team"), ConfigLevelTeam)
	assert.Equal(t, ConfigLevel("default"), ConfigLevelDefault)
}

func TestSetRepoConfig_UnsupportedKey(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sageoxDir := filepath.Join(dir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte(`{}`), 0o644))

	err := setRepoConfig("telemetry", "on", dir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not supported at repo level")
}

func TestSetTeamConfig_NoTeamContext(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	err := setTeamConfig("session_recording", "auto", dir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no team context")
}

func TestResolveConfigValue_KnownSettings(t *testing.T) {
	t.Parallel()

	// resolve with empty project root — resolves from user config or defaults
	for _, setting := range AllSettings {
		t.Run(setting.Key, func(t *testing.T) {
			t.Parallel()
			cv, err := ResolveConfigValue(setting.Key, "")
			require.NoError(t, err)
			require.NotNil(t, cv)
			assert.Equal(t, setting.Key, cv.Key)
			assert.Equal(t, setting.Default, cv.Default)
			assert.NotEmpty(t, cv.Value, "resolved value should not be empty")
			assert.NotEmpty(t, cv.Source, "source should be set")
			assert.Empty(t, cv.RepoVal)
			assert.Empty(t, cv.TeamVal)
		})
	}
}

func TestResolveConfigValue_WithFakeProjectRoot(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cv, err := ResolveConfigValue("session_recording", dir)
	require.NoError(t, err)
	require.NotNil(t, cv)
	assert.Equal(t, "session_recording", cv.Key)
	assert.Equal(t, "auto", cv.Default)
	assert.NotEmpty(t, cv.Value, "resolved value should not be empty")
	assert.NotEmpty(t, cv.Source, "source should be set")
}

func TestResolveConfigValue_AllKnownKeys(t *testing.T) {
	t.Parallel()

	keys := []string{"session_recording", "telemetry", "tips", "context_git.auto_commit", "context_git.auto_push", "view_format", "agent_worker"}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			cv, err := ResolveConfigValue(key, t.TempDir())
			require.NoError(t, err)
			require.NotNil(t, cv)
			assert.Equal(t, key, cv.Key)
			assert.NotEmpty(t, cv.Value, "resolved value should not be empty")
		})
	}
}

// --- Team-timezone revert regression (Unit 4) -----------------------------
//
// These tests lock in that the `timezone` key is no longer a recognized
// config setting. Failure prevented: re-adding a timezone entry to the
// registry would silently resurrect a setting that the distill helpers
// no longer consult.

func TestGetSetting_TimezoneRemoved(t *testing.T) {
	t.Parallel()
	assert.Nil(t, GetSetting("timezone"), "timezone setting must no longer be registered")
}

func TestSetConfigValue_TimezoneRejected(t *testing.T) {
	t.Parallel()
	err := SetConfigValue("timezone", "UTC", ConfigLevelUser, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown setting")
}

func TestAllSettings_NoTimezoneEntry(t *testing.T) {
	t.Parallel()
	for _, s := range AllSettings {
		assert.NotEqual(t, "timezone", s.Key, "AllSettings must not contain a timezone entry")
	}
}

// --- plan.open round trip (Ryan follow-up to ox-mj0s) -----------------------
//
// Proves the FULL path an agent's "Always open from now on" / "Never ask
// again" answer takes: SetConfigValue (the same entry point `ox config set`
// calls) through to config.PlanOpen resolving it back. A unit test on either
// half alone would pass even if the registry wiring silently dropped the
// setting between them. No t.Parallel() here — these tests call t.Setenv,
// which Go's testing package forbids combining with Parallel.

func TestSetConfigValue_PlanOpen_RoundTripsThroughResolver(t *testing.T) {
	for _, val := range []string{config.PlanOpenNever, config.PlanOpenAsk, config.PlanOpenAlways} {
		t.Run(val, func(t *testing.T) {
			t.Setenv("OX_USER_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
			t.Setenv(config.EnvPlanOpen, "") // isolate from ambient env

			require.NoError(t, SetConfigValue("plan.open", val, ConfigLevelUser, ""))
			assert.Equal(t, val, config.PlanOpen(""), "plan.open set via SetConfigValue must resolve back through config.PlanOpen")
		})
	}
}

func TestSetConfigValue_PlanOpen_InvalidValue(t *testing.T) {
	err := SetConfigValue("plan.open", "sometimes", ConfigLevelUser, "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid value")
	assert.Contains(t, err.Error(), "valid values")
}

func TestUnsetConfigValue_PlanOpen_FallsBackToDefault(t *testing.T) {
	t.Setenv("OX_USER_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	t.Setenv(config.EnvPlanOpen, "")

	require.NoError(t, SetConfigValue("plan.open", config.PlanOpenNever, ConfigLevelUser, ""))
	assert.Equal(t, config.PlanOpenNever, config.PlanOpen(""))

	require.NoError(t, UnsetConfigValue("plan.open", ConfigLevelUser, ""))
	assert.Equal(t, config.DefaultPlanOpen, config.PlanOpen(""), "unsetting plan.open must fall back to the default (ask)")
}
