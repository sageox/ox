package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUsagePaneSetting_Lifecycle drives `ox config set|get|unset usage_pane`
// through the real settings plumbing against an isolated user config file.
func TestUsagePaneSetting_Lifecycle(t *testing.T) {
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
		{"set off", func() error { return SetConfigValue("usage_pane", "off", ConfigLevelUser, "") }, "off", ConfigLevelUser, false},
		{"set on", func() error { return SetConfigValue("usage_pane", "on", ConfigLevelUser, "") }, "on", ConfigLevelUser, true},
		{"unset falls back to default", func() error { return UnsetConfigValue("usage_pane", ConfigLevelUser, "") }, "on", ConfigLevelDefault, true},
	}
	for _, step := range steps {
		require.NoError(t, step.act(), step.name)
		cv, err := ResolveConfigValue("usage_pane", project)
		require.NoError(t, err, step.name)
		assert.Equal(t, step.wantValue, cv.Value, step.name)
		assert.Equal(t, step.wantSource, cv.Source, step.name)
		userCfg, err := config.LoadUserConfig()
		require.NoError(t, err, step.name)
		assert.Equal(t, step.wantOn, userCfg.IsUsagePaneEnabled(), step.name)
	}
}

// TestUsagePaneSetting_RejectsInvalidValues keeps the key to on/off at user level.
func TestUsagePaneSetting_RejectsInvalidValues(t *testing.T) {
	t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
	for _, tc := range []struct {
		value string
		level ConfigLevel
	}{{"of", ConfigLevelUser}, {"false", ConfigLevelUser}, {"off", ConfigLevelRepo}, {"off", ConfigLevelTeam}} {
		require.Error(t, SetConfigValue("usage_pane", tc.value, tc.level, t.TempDir()), tc)
	}
	userCfg, err := config.LoadUserConfig()
	require.NoError(t, err)
	assert.Nil(t, userCfg.UsagePaneEnabled)
}

// runConfigWith runs `ox config set|unset` with its output captured.
func runConfigWith(t *testing.T, run func(cmd *cobra.Command, args []string) error, cmd *cobra.Command, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetContext(context.Background())
	t.Cleanup(func() {
		cmd.SetOut(nil)
		cmd.SetErr(nil)
	})
	err := run(cmd, args)
	return out.String() + errOut.String(), err
}

// TestConfigSetUsagePane_AppliesToClaudeCode verifies `ox config set
// usage_pane off|on` (and unset) changes Claude Code right away, not at the
// next ox doctor --fix.
// Failure prevented: a coworker turns the pane off and it keeps loading, or
// turns it on and nothing appears until some later doctor run.
func TestConfigSetUsagePane_AppliesToClaudeCode(t *testing.T) {
	env := setupUsagePaneEnv(t, true)
	t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
	installedFile := filepath.Join(env.state, "installed")

	out, err := runConfigWith(t, runConfigSet, configSetCmd, "usage_pane", "on")
	require.NoError(t, err)
	assert.FileExists(t, installedFile, "set on: Enable installs the plugin")
	assert.Contains(t, env.log(t), "plugin install ox-cli-usage-pane@ox-cli-usage --scope user")
	assert.Contains(t, out, "/ox-usage")

	require.NoError(t, os.Remove(filepath.Join(env.state, "log")))
	_, err = runConfigWith(t, runConfigSet, configSetCmd, "usage_pane", "off")
	require.NoError(t, err)
	assert.NoFileExists(t, installedFile, "set off: Disable uninstalls the plugin")
	assert.Contains(t, env.log(t), "plugin uninstall ox-cli-usage-pane@ox-cli-usage --scope user")
	assert.NoDirExists(t, env.dir)

	_, err = runConfigWith(t, runConfigUnset, configUnsetCmd, "usage_pane")
	require.NoError(t, err)
	assert.FileExists(t, installedFile, "unset: back to the default (on) reinstalls")
}

// TestConfigSetUsagePane_ClaudeFailureIsAWarning verifies the setting is still
// saved when Claude Code cannot be updated, with a warning naming the retry.
func TestConfigSetUsagePane_ClaudeFailureIsAWarning(t *testing.T) {
	setupUsagePaneEnv(t, true)
	t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
	bin := filepath.Dir(firstOnPath(t, "claude"))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\necho broken; exit 2\n"), 0o755))

	out, err := runConfigWith(t, runConfigSet, configSetCmd, "usage_pane", "off")
	require.NoError(t, err)
	assert.Contains(t, out, "ox doctor --fix")
	userCfg, err := config.LoadUserConfig()
	require.NoError(t, err)
	assert.False(t, userCfg.IsUsagePaneEnabled())
}

// TestConfigSetUsagePane_SkipsWithoutClaude verifies a machine without the
// claude CLI just stores the setting.
func TestConfigSetUsagePane_SkipsWithoutClaude(t *testing.T) {
	env := setupUsagePaneEnv(t, false)
	t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
	out, err := runConfigWith(t, runConfigSet, configSetCmd, "usage_pane", "on")
	require.NoError(t, err)
	assert.Empty(t, out)
	assert.NoDirExists(t, env.dir)
}
