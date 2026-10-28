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

// TestUsagePaneSetting_Lifecycle drives `ox config set|get|unset brief_pane`
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
		{"set off", func() error { return SetConfigValue("brief_pane", "off", ConfigLevelUser, "") }, "off", ConfigLevelUser, false},
		{"set on", func() error { return SetConfigValue("brief_pane", "on", ConfigLevelUser, "") }, "on", ConfigLevelUser, true},
		{"unset falls back to default", func() error { return UnsetConfigValue("brief_pane", ConfigLevelUser, "") }, "on", ConfigLevelDefault, true},
	}
	for _, step := range steps {
		require.NoError(t, step.act(), step.name)
		cv, err := ResolveConfigValue("brief_pane", project)
		require.NoError(t, err, step.name)
		assert.Equal(t, step.wantValue, cv.Value, step.name)
		assert.Equal(t, step.wantSource, cv.Source, step.name)
		userCfg, err := config.LoadUserConfig()
		require.NoError(t, err, step.name)
		assert.Equal(t, step.wantOn, userCfg.IsBriefPaneEnabled(), step.name)
	}
}

// TestUsagePaneSetting_RejectsInvalidValues keeps the key to on/off at user level.
func TestUsagePaneSetting_RejectsInvalidValues(t *testing.T) {
	t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
	for _, tc := range []struct {
		value string
		level ConfigLevel
	}{{"of", ConfigLevelUser}, {"false", ConfigLevelUser}, {"off", ConfigLevelRepo}, {"off", ConfigLevelTeam}} {
		require.Error(t, SetConfigValue("brief_pane", tc.value, tc.level, t.TempDir()), tc)
	}
	userCfg, err := config.LoadUserConfig()
	require.NoError(t, err)
	assert.Nil(t, userCfg.BriefPaneEnabled)
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
// brief_pane off|on` (and unset) changes Claude Code right away, not at the
// next ox doctor --fix.
// Failure prevented: a coworker turns the pane off and it keeps loading, or
// turns it on and nothing appears until some later doctor run.
func TestConfigSetUsagePane_AppliesToClaudeCode(t *testing.T) {
	env := setupUsagePaneEnv(t, true)
	t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
	installedFile := filepath.Join(env.state, "installed")

	out, err := runConfigWith(t, runConfigSet, configSetCmd, "brief_pane", "on")
	require.NoError(t, err)
	assert.FileExists(t, installedFile, "set on: Enable installs the plugin")
	assert.Contains(t, env.log(t), "plugin install ox-cli-brief-pane@ox-cli-brief --scope user")
	assert.Contains(t, out, "/ox-brief")

	require.NoError(t, os.Remove(filepath.Join(env.state, "log")))
	_, err = runConfigWith(t, runConfigSet, configSetCmd, "brief_pane", "off")
	require.NoError(t, err)
	assert.NoFileExists(t, installedFile, "set off: Disable uninstalls the plugin")
	assert.Contains(t, env.log(t), "plugin uninstall ox-cli-brief-pane@ox-cli-brief --scope user")
	assert.NoDirExists(t, env.dir)

	_, err = runConfigWith(t, runConfigUnset, configUnsetCmd, "brief_pane")
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

	out, err := runConfigWith(t, runConfigSet, configSetCmd, "brief_pane", "off")
	require.NoError(t, err)
	assert.Contains(t, out, "ox doctor --fix")
	userCfg, err := config.LoadUserConfig()
	require.NoError(t, err)
	assert.False(t, userCfg.IsBriefPaneEnabled())
}

// TestConfigSetUsagePane_SkipsWithoutClaude verifies a machine without the
// claude CLI just stores the setting.
func TestConfigSetUsagePane_SkipsWithoutClaude(t *testing.T) {
	env := setupUsagePaneEnv(t, false)
	t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
	out, err := runConfigWith(t, runConfigSet, configSetCmd, "brief_pane", "on")
	require.NoError(t, err)
	assert.Empty(t, out)
	assert.NoDirExists(t, env.dir)
}

// TestBriefPaneSetting_FallsBackToUsagePane verifies the rename from
// usage_pane to brief_pane keeps every coworker's choice. Failure prevented:
// a coworker who turned the pane off before the rename gets it reinstalled,
// or `ox config set usage_pane off` from an old script silently stops working.
func TestBriefPaneSetting_FallsBackToUsagePane(t *testing.T) {
	tests := []struct {
		name       string
		file       string // config.yaml before the step
		act        func() error
		key        string // key resolved after the step
		wantValue  string
		wantSource ConfigLevel
		wantOn     bool
		wantFile   []string // substrings config.yaml must hold after
		noFile     []string // substrings config.yaml must not hold after
	}{
		{
			name: "old usage_pane off is read by brief_pane", file: "usage_pane_enabled: false\n",
			act: func() error { return nil }, key: "brief_pane",
			wantValue: "off", wantSource: ConfigLevelUser, wantOn: false,
		},
		{
			name: "old usage_pane off is read by usage_pane", file: "usage_pane_enabled: false\n",
			act: func() error { return nil }, key: "usage_pane",
			wantValue: "off", wantSource: ConfigLevelUser, wantOn: false,
		},
		{
			name: "brief_pane wins over the old key", file: "usage_pane_enabled: false\nbrief_pane_enabled: true\n",
			act: func() error { return nil }, key: "brief_pane",
			wantValue: "on", wantSource: ConfigLevelUser, wantOn: true,
		},
		{
			name: "setting brief_pane migrates the old key", file: "usage_pane_enabled: false\n",
			act: func() error { return SetConfigValue("brief_pane", "on", ConfigLevelUser, "") }, key: "brief_pane",
			wantValue: "on", wantSource: ConfigLevelUser, wantOn: true,
			wantFile: []string{"brief_pane_enabled: true"}, noFile: []string{"usage_pane_enabled"},
		},
		{
			name: "setting the deprecated usage_pane writes brief_pane",
			act:  func() error { return SetConfigValue("usage_pane", "off", ConfigLevelUser, "") }, key: "brief_pane",
			wantValue: "off", wantSource: ConfigLevelUser, wantOn: false,
			wantFile: []string{"brief_pane_enabled: false"}, noFile: []string{"usage_pane_enabled"},
		},
		{
			name: "unsetting brief_pane clears the old key too", file: "usage_pane_enabled: false\n",
			act: func() error { return UnsetConfigValue("brief_pane", ConfigLevelUser, "") }, key: "brief_pane",
			wantValue: "on", wantSource: ConfigLevelDefault, wantOn: true,
			noFile: []string{"usage_pane_enabled", "brief_pane_enabled"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			t.Setenv(config.EnvUserConfig, path)
			if tt.file != "" {
				require.NoError(t, os.WriteFile(path, []byte(tt.file), 0o600))
			}
			require.NoError(t, tt.act())

			cv, err := ResolveConfigValue(tt.key, t.TempDir())
			require.NoError(t, err)
			assert.Equal(t, tt.wantValue, cv.Value)
			assert.Equal(t, tt.wantSource, cv.Source)
			userCfg, err := config.LoadUserConfig()
			require.NoError(t, err)
			assert.Equal(t, tt.wantOn, userCfg.IsBriefPaneEnabled())

			data, _ := os.ReadFile(path)
			for _, s := range tt.wantFile {
				assert.Contains(t, string(data), s)
			}
			for _, s := range tt.noFile {
				assert.NotContains(t, string(data), s)
			}
		})
	}
}

// TestUsagePaneSetting_IsDeprecatedAlias verifies `ox config set usage_pane`
// says it is renamed and names brief_pane. Failure prevented: a coworker keeps
// using the old key without ever learning the new one.
func TestUsagePaneSetting_IsDeprecatedAlias(t *testing.T) {
	setting := GetSetting("usage_pane")
	require.NotNil(t, setting)
	assert.True(t, setting.Deprecated)
	assert.Contains(t, setting.DeprecationNotice, "brief_pane")
	assert.False(t, GetSetting("brief_pane").Deprecated)

	t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
	out := string(captureRealStdout(t, func() {
		require.NoError(t, configSetCmd.RunE(configSetCmd, []string{"usage_pane", "off"}))
	}))
	assert.Contains(t, out, "renamed to brief_pane")
	assert.Contains(t, out, "ox config set brief_pane on|off")
}
