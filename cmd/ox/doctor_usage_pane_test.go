package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCheckUsagePane drives the doctor check against a fake claude CLI and
// asserts what Claude Code ends up with, not just the check's verdict.
// Failure prevented: usage_pane is on (the default) and doctor reports
// healthy while Claude Code never got the plugin — or it is off and the pane
// keeps loading.
func TestCheckUsagePane(t *testing.T) {
	const want = "2.0.0"
	enableAt := func(v string) func(t *testing.T, env usagePaneEnv) {
		return func(t *testing.T, env usagePaneEnv) {
			in := newUsagePaneInstaller()
			in.Version = v
			_, err := in.Enable(context.Background())
			require.NoError(t, err)
		}
	}
	installSeq := func(env usagePaneEnv) []string {
		return []string{
			"plugin list --json", // Status
			"plugin validate " + env.dir,
			"plugin marketplace list --json",
			"plugin marketplace add " + env.dir,
			"plugin list --json",
			"plugin install ox-cli-usage-pane@ox-cli-usage --scope user",
		}
	}

	tests := []struct {
		name        string
		setup       func(t *testing.T, env usagePaneEnv)
		enabled     bool
		fix         bool
		noClaude    bool
		wantPassed  bool
		wantWarning bool
		wantSkipped bool
		wantMessage string
		wantLog     func(env usagePaneEnv) []string // claude calls made by the check
		wantVersion string                          // Claude Code's installed version after; "" = not installed
	}{
		{
			name: "enabled, not installed, fix installs", enabled: true, fix: true,
			wantPassed: true, wantMessage: usagePaneInstalledMsg, wantLog: installSeq, wantVersion: want,
		},
		{
			name: "enabled, not installed, no fix warns", enabled: true,
			wantPassed: true, wantWarning: true, wantMessage: "not installed",
			wantLog: func(usagePaneEnv) []string { return []string{"plugin list --json"} },
		},
		{
			name: "enabled, current, no CLI mutations", setup: enableAt(want), enabled: true, fix: true,
			wantPassed: true, wantMessage: "installed",
			wantLog:     func(usagePaneEnv) []string { return []string{"plugin list --json"} },
			wantVersion: want,
		},
		{
			name: "enabled, from an older ox, no fix warns", setup: enableAt("1.0.0"), enabled: true,
			wantPassed: true, wantWarning: true, wantMessage: "from an older ox",
			wantLog:     func(usagePaneEnv) []string { return []string{"plugin list --json"} },
			wantVersion: "1.0.0",
		},
		{
			name: "enabled, from an older ox, fix reinstalls", setup: enableAt("1.0.0"), enabled: true, fix: true,
			wantPassed: true, wantMessage: usagePaneInstalledMsg,
			wantLog: func(env usagePaneEnv) []string {
				return []string{
					"plugin list --json",
					"plugin validate " + env.dir,
					"plugin marketplace list --json",
					"plugin list --json",
					"plugin uninstall ox-cli-usage-pane@ox-cli-usage --scope user",
					"plugin install ox-cli-usage-pane@ox-cli-usage --scope user",
				}
			},
			wantVersion: want,
		},
		{
			name: "disabled, installed, fix removes", setup: enableAt(want), fix: true,
			wantPassed: true, wantMessage: "removed (usage_pane is off)",
			wantLog: func(usagePaneEnv) []string {
				return []string{
					"plugin list --json",
					"plugin list --json",
					"plugin uninstall ox-cli-usage-pane@ox-cli-usage --scope user",
					"plugin marketplace list --json",
					"plugin marketplace remove ox-cli-usage",
				}
			},
		},
		{
			name: "disabled, installed, no fix warns", setup: enableAt(want),
			wantPassed: true, wantWarning: true, wantMessage: "off but still installed",
			wantLog:     func(usagePaneEnv) []string { return []string{"plugin list --json"} },
			wantVersion: want,
		},
		{
			name: "disabled, absent passes", fix: true,
			wantPassed: true, wantMessage: "off",
			wantLog: func(usagePaneEnv) []string { return []string{"plugin list --json"} },
		},
		{
			name: "no claude CLI is skipped", enabled: true, fix: true, noClaude: true,
			wantSkipped: true, wantMessage: "claude CLI not found",
		},
		{
			name: "claude CLI without plugins is skipped", enabled: true, fix: true,
			setup: func(t *testing.T, env usagePaneEnv) {
				require.NoError(t, os.WriteFile(filepath.Join(env.state, "old"), nil, 0o644))
			},
			wantSkipped: true, wantMessage: "claude CLI too old for plugins",
			wantLog: func(usagePaneEnv) []string { return []string{"plugin list --json"} },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := setupUsagePaneEnv(t, true)
			if tt.setup != nil {
				tt.setup(t, env)
			}
			require.NoError(t, os.RemoveAll(filepath.Join(env.state, "log")))

			in := newUsagePaneInstaller()
			if tt.noClaude {
				in.Claude = nil
			}
			res := checkUsagePane(in, tt.enabled, want, tt.fix)

			assert.Equal(t, tt.wantMessage, res.message, res.detail)
			assert.Equal(t, tt.wantPassed, res.passed, res.message)
			assert.Equal(t, tt.wantWarning, res.warning, res.message)
			assert.Equal(t, tt.wantSkipped, res.skipped, res.message)
			var wantLog []string
			if tt.wantLog != nil {
				wantLog = tt.wantLog(env)
			}
			assert.Equal(t, wantLog, env.log(t), "claude CLI calls")

			installed, err := os.ReadFile(filepath.Join(env.state, "installed"))
			if tt.wantVersion == "" {
				assert.True(t, os.IsNotExist(err), "Claude Code should not have the plugin installed")
				return
			}
			require.NoError(t, err, "Claude Code should have the plugin installed")
			assert.Equal(t, tt.wantVersion+"\n", string(installed))
		})
	}
}

// TestCheckUsagePane_RestoresInterruptedRefresh verifies a refresh that
// stopped between its two renames is put back before the check reads state.
func TestCheckUsagePane_RestoresInterruptedRefresh(t *testing.T) {
	env := setupUsagePaneEnv(t, true)
	_, err := newUsagePaneInstaller().Enable(context.Background())
	require.NoError(t, err)
	require.NoError(t, os.Rename(env.dir, env.dir+".old"))

	in := newUsagePaneInstaller()
	res := checkUsagePane(in, true, in.Version, false)
	assert.Equal(t, "installed", res.message)
	assert.DirExists(t, env.dir)
}

// TestUsagePaneCheck_IsRegistered verifies the doctor registry runs the check
// with the user's usage_pane setting, against the real data path.
func TestUsagePaneCheck_IsRegistered(t *testing.T) {
	env := setupUsagePaneEnv(t, true)
	t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
	check := GetDoctorCheck(CheckSlugUsagePane)
	require.NotNil(t, check)
	assert.Equal(t, FixLevelAuto, check.FixLevel)
	assert.Equal(t, "Integration", check.Category)

	// default on: the fix installs the pane
	res := check.Run(true)
	assert.Equal(t, usagePaneInstalledMsg, res.message)
	assert.FileExists(t, filepath.Join(env.state, "installed"))

	// off: the fix removes it
	require.NoError(t, SetConfigValue("usage_pane", "off", ConfigLevelUser, ""))
	res = check.Run(true)
	assert.Equal(t, "removed (usage_pane is off)", res.message)
	assert.NoFileExists(t, filepath.Join(env.state, "installed"))
	assert.NoDirExists(t, env.dir)
}

// TestEnsureUsagePane_FromInit verifies `ox init`'s best-effort install
// follows the setting and stays quiet when there is nothing to do.
func TestEnsureUsagePane_FromInit(t *testing.T) {
	env := setupUsagePaneEnv(t, true)
	t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))

	require.NoError(t, SetConfigValue("usage_pane", "off", ConfigLevelUser, ""))
	ensureUsagePane(true)
	assert.Nil(t, env.log(t), "usage_pane off: init must not touch Claude Code")

	require.NoError(t, UnsetConfigValue("usage_pane", ConfigLevelUser, ""))
	ensureUsagePane(true)
	assert.FileExists(t, filepath.Join(env.state, "installed"))
	assert.Contains(t, env.log(t), "plugin install ox-cli-usage-pane@ox-cli-usage --scope user")
}

// TestEnsureUsagePane_NeverFailsInit verifies a broken claude CLI cannot
// stop `ox init`: ensureUsagePane has no error to return and installs nothing.
func TestEnsureUsagePane_NeverFailsInit(t *testing.T) {
	env := setupUsagePaneEnv(t, true)
	t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
	bin := filepath.Dir(firstOnPath(t, "claude"))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\necho broken; exit 2\n"), 0o755))
	ensureUsagePane(true)
	assert.NoFileExists(t, filepath.Join(env.state, "installed"))
}
