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

// TestUsagePaneSetting_Lifecycle drives `ox config set|get|unset session_report_pane`
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
		{"set off", func() error { return SetConfigValue("session_report_pane", "off", ConfigLevelUser, "") }, "off", ConfigLevelUser, false},
		{"set on", func() error { return SetConfigValue("session_report_pane", "on", ConfigLevelUser, "") }, "on", ConfigLevelUser, true},
		{"unset falls back to default", func() error { return UnsetConfigValue("session_report_pane", ConfigLevelUser, "") }, "on", ConfigLevelDefault, true},
	}
	for _, step := range steps {
		require.NoError(t, step.act(), step.name)
		cv, err := ResolveConfigValue("session_report_pane", project)
		require.NoError(t, err, step.name)
		assert.Equal(t, step.wantValue, cv.Value, step.name)
		assert.Equal(t, step.wantSource, cv.Source, step.name)
		userCfg, err := config.LoadUserConfig()
		require.NoError(t, err, step.name)
		assert.Equal(t, step.wantOn, userCfg.IsSessionReportPaneEnabled(), step.name)
	}
}

// TestUsagePaneSetting_RejectsInvalidValues keeps the key to on/off at user level.
func TestUsagePaneSetting_RejectsInvalidValues(t *testing.T) {
	t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
	for _, tc := range []struct {
		value string
		level ConfigLevel
	}{{"of", ConfigLevelUser}, {"false", ConfigLevelUser}, {"off", ConfigLevelRepo}, {"off", ConfigLevelTeam}} {
		require.Error(t, SetConfigValue("session_report_pane", tc.value, tc.level, t.TempDir()), tc)
	}
	userCfg, err := config.LoadUserConfig()
	require.NoError(t, err)
	assert.Nil(t, userCfg.SessionReportPaneEnabled)
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
// session_report_pane off|on` (and unset) changes Claude Code right away, not at the
// next ox doctor --fix.
// Failure prevented: a coworker turns the pane off and it keeps loading, or
// turns it on and nothing appears until some later doctor run.
func TestConfigSetUsagePane_AppliesToClaudeCode(t *testing.T) {
	env := setupUsagePaneEnv(t, true)
	t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
	installedFile := filepath.Join(env.state, "installed")

	out, err := runConfigWith(t, runConfigSet, configSetCmd, "session_report_pane", "on")
	require.NoError(t, err)
	assert.FileExists(t, installedFile, "set on: Enable installs the plugin")
	assert.Contains(t, env.log(t), "plugin install ox-cli-brief-pane@ox-cli-brief --scope user")
	assert.Contains(t, out, "/ox-session-report")

	require.NoError(t, os.Remove(filepath.Join(env.state, "log")))
	_, err = runConfigWith(t, runConfigSet, configSetCmd, "session_report_pane", "off")
	require.NoError(t, err)
	assert.NoFileExists(t, installedFile, "set off: Disable uninstalls the plugin")
	assert.Contains(t, env.log(t), "plugin uninstall ox-cli-brief-pane@ox-cli-brief --scope user")
	assert.NoDirExists(t, env.dir)

	_, err = runConfigWith(t, runConfigUnset, configUnsetCmd, "session_report_pane")
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

	out, err := runConfigWith(t, runConfigSet, configSetCmd, "session_report_pane", "off")
	require.NoError(t, err)
	assert.Contains(t, out, "ox doctor --fix")
	userCfg, err := config.LoadUserConfig()
	require.NoError(t, err)
	assert.False(t, userCfg.IsSessionReportPaneEnabled())
}

// TestConfigSetUsagePane_SkipsWithoutClaude verifies a machine without the
// claude CLI just stores the setting.
func TestConfigSetUsagePane_SkipsWithoutClaude(t *testing.T) {
	env := setupUsagePaneEnv(t, false)
	t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
	out, err := runConfigWith(t, runConfigSet, configSetCmd, "session_report_pane", "on")
	require.NoError(t, err)
	assert.Empty(t, out)
	assert.NoDirExists(t, env.dir)
}

// TestSessionReportPaneSetting_FallsBackToBriefPane verifies the rename from
// brief_pane to session_report_pane keeps every coworker's choice. Failure prevented:
// a coworker who turned the pane off before the rename gets it reinstalled,
// or `ox config set brief_pane off` from an old script silently stops working.
func TestSessionReportPaneSetting_FallsBackToBriefPane(t *testing.T) {
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
			name: "old brief_pane off is read by session_report_pane", file: "brief_pane_enabled: false\n",
			act: func() error { return nil }, key: "session_report_pane",
			wantValue: "off", wantSource: ConfigLevelUser, wantOn: false,
		},
		{
			name: "old brief_pane off is read by brief_pane", file: "brief_pane_enabled: false\n",
			act: func() error { return nil }, key: "brief_pane",
			wantValue: "off", wantSource: ConfigLevelUser, wantOn: false,
		},
		{
			name: "session_report_pane wins over the old key", file: "brief_pane_enabled: false\nsession_report_pane_enabled: true\n",
			act: func() error { return nil }, key: "session_report_pane",
			wantValue: "on", wantSource: ConfigLevelUser, wantOn: true,
		},
		{
			name: "setting session_report_pane migrates the old key", file: "brief_pane_enabled: false\n",
			act: func() error { return SetConfigValue("session_report_pane", "on", ConfigLevelUser, "") }, key: "session_report_pane",
			wantValue: "on", wantSource: ConfigLevelUser, wantOn: true,
			wantFile: []string{"session_report_pane_enabled: true"}, noFile: []string{"brief_pane_enabled"},
		},
		{
			name: "setting the deprecated brief_pane writes session_report_pane",
			act:  func() error { return SetConfigValue("brief_pane", "off", ConfigLevelUser, "") }, key: "session_report_pane",
			wantValue: "off", wantSource: ConfigLevelUser, wantOn: false,
			wantFile: []string{"session_report_pane_enabled: false"}, noFile: []string{"brief_pane_enabled"},
		},
		{
			name: "unsetting session_report_pane clears the old key too", file: "brief_pane_enabled: false\n",
			act: func() error { return UnsetConfigValue("session_report_pane", ConfigLevelUser, "") }, key: "session_report_pane",
			wantValue: "on", wantSource: ConfigLevelDefault, wantOn: true,
			noFile: []string{"brief_pane_enabled", "session_report_pane_enabled"},
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
			assert.Equal(t, tt.wantOn, userCfg.IsSessionReportPaneEnabled())

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

// TestBriefPaneSetting_IsDeprecatedAlias verifies `ox config set brief_pane`
// says it is renamed and names session_report_pane. Failure prevented: a coworker keeps
// using the old key without ever learning the new one.
func TestBriefPaneSetting_IsDeprecatedAlias(t *testing.T) {
	setting := GetSetting("brief_pane")
	require.NotNil(t, setting)
	assert.True(t, setting.Deprecated)
	assert.Contains(t, setting.DeprecationNotice, "session_report_pane")
	assert.False(t, GetSetting("session_report_pane").Deprecated)

	t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
	out := string(captureRealStdout(t, func() {
		require.NoError(t, configSetCmd.RunE(configSetCmd, []string{"brief_pane", "off"}))
	}))
	assert.Contains(t, out, "renamed to session_report_pane")
	assert.Contains(t, out, "ox config set session_report_pane on|off")

	// the old key flips the same value the new key reads
	cv, err := ResolveConfigValue("session_report_pane", t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, "off", cv.Value)
	assert.Equal(t, ConfigLevelUser, cv.Source)
	userCfg, err := config.LoadUserConfig()
	require.NoError(t, err)
	assert.False(t, userCfg.IsSessionReportPaneEnabled())
	require.NotNil(t, userCfg.SessionReportPaneEnabled, "brief_pane must write session_report_pane_enabled")
	assert.Nil(t, userCfg.BriefPaneEnabled)
}
