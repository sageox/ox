package main

import (
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStatuslineSetting_Lifecycle drives `ox config set|get|unset statusline`
// through the real settings plumbing against an isolated user config file.
// Failure prevented: `ox config set statusline off` is accepted but never
// reaches IsStatuslineEnabled, so the activity line keeps showing after a
// coworker turned it off — or an unset leaves it stuck off.
func TestStatuslineSetting_Lifecycle(t *testing.T) {
	t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
	project := t.TempDir()

	steps := []struct {
		name       string
		act        func() error
		wantValue  string
		wantSource ConfigLevel
		wantOn     bool
	}{
		{"default is on", func() error { return nil }, "on", ConfigLevelDefault, true},
		{"set off", func() error { return SetConfigValue("statusline", "off", ConfigLevelUser, "") }, "off", ConfigLevelUser, false},
		{"set on", func() error { return SetConfigValue("statusline", "on", ConfigLevelUser, "") }, "on", ConfigLevelUser, true},
		{"set off again", func() error { return SetConfigValue("statusline", "off", ConfigLevelUser, "") }, "off", ConfigLevelUser, false},
		{"unset falls back to default", func() error { return UnsetConfigValue("statusline", ConfigLevelUser, "") }, "on", ConfigLevelDefault, true},
	}

	for _, step := range steps {
		require.NoError(t, step.act(), step.name)

		cv, err := ResolveConfigValue("statusline", project)
		require.NoError(t, err, step.name)
		assert.Equal(t, step.wantValue, cv.Value, step.name)
		assert.Equal(t, step.wantSource, cv.Source, step.name)
		assert.Equal(t, "on", cv.Default, step.name)

		userCfg, err := config.LoadUserConfig()
		require.NoError(t, err, step.name)
		assert.Equal(t, step.wantOn, userCfg.IsStatuslineEnabled(), step.name)
	}
}

// TestStatuslineSetting_RejectsInvalidValues keeps the key to on/off and to the
// user level. Failure prevented: a typo ("of") silently stored and read as on.
func TestStatuslineSetting_RejectsInvalidValues(t *testing.T) {
	t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))

	tests := []struct {
		name  string
		value string
		level ConfigLevel
	}{
		{"typo", "of", ConfigLevelUser},
		{"boolean spelling", "false", ConfigLevelUser},
		{"repo level", "off", ConfigLevelRepo},
		{"team level", "off", ConfigLevelTeam},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Error(t, SetConfigValue("statusline", tt.value, tt.level, t.TempDir()))
		})
	}

	userCfg, err := config.LoadUserConfig()
	require.NoError(t, err)
	assert.Nil(t, userCfg.StatuslineEnabled, "a rejected write must not touch the stored value")
	assert.True(t, userCfg.IsStatuslineEnabled())
}
