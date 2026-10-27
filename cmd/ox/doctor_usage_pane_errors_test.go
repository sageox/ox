package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/config"
)

// replaceFakeClaude swaps the fake claude on PATH for one that answers
// `plugin list --json` (installed or not) and fails every other call with msg.
func replaceFakeClaude(t *testing.T, installed bool, msg string) {
	t.Helper()
	list := `[]`
	if installed {
		list = `[{"id":"ox-cli-brief-pane@ox-cli-brief","version":"1.0.0"}]`
	}
	script := "#!/bin/sh\n" +
		"if [ \"$*\" = \"plugin list --json\" ]; then echo '" + list + "'; exit 0; fi\n" +
		"echo \"" + msg + "\"; exit 1\n"
	bin := filepath.Dir(firstOnPath(t, "claude"))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755))
}

// brokenUserConfig makes the user config unreadable.
func brokenUserConfig(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("brief_pane: [unclosed\n"), 0o644))
	t.Setenv(config.EnvUserConfig, path)
}

// TestCheckUsagePane_CLIFailures drives the doctor check against a claude CLI
// whose install or removal fails. Failure prevented: doctor reports the pane
// healthy after Claude Code refused it, or flags as broken a claude CLI that
// is simply too old for plugins (nothing ox can fix).
func TestCheckUsagePane_CLIFailures(t *testing.T) {
	tests := []struct {
		name        string
		installed   bool
		msg         string
		enabled     bool
		wantSkipped bool
		wantMessage string
	}{
		{"install fails", false, "boom", true, false, "install failed"},
		{"install flag unknown to an old CLI", false, "error: unknown option '--scope'", true, true, "claude CLI too old for plugins"},
		{"removal fails", true, "boom", false, false, "removal failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupUsagePaneEnv(t, true)
			replaceFakeClaude(t, tt.installed, tt.msg)

			res := checkUsagePane(newUsagePaneInstaller(), tt.enabled, "2.0.0", true)

			assert.Equal(t, tt.wantMessage, res.message, res.detail)
			assert.Equal(t, tt.wantSkipped, res.skipped)
			if !tt.wantSkipped {
				assert.False(t, res.passed, "a refused install or removal is never a pass")
			}
		})
	}
}

// TestCheckUsagePane_UnrecoverableSwap: an interrupted refresh that cannot be
// put back is reported as failed, not papered over with a fresh install.
func TestCheckUsagePane_UnrecoverableSwap(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	env := setupUsagePaneEnv(t, true)
	_, err := newUsagePaneInstaller().Enable(context.Background())
	require.NoError(t, err)
	require.NoError(t, os.Rename(env.dir, env.dir+".old"))
	parent := filepath.Dir(env.dir)
	require.NoError(t, os.Chmod(parent, 0o555))
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })

	res := checkUsagePane(newUsagePaneInstaller(), true, "2.0.0", true)

	assert.False(t, res.passed)
	assert.Equal(t, "interrupted refresh", res.message)
}

// TestUsagePane_UnreadableSetting: a broken user config skips the doctor check
// and surfaces from `ox config`, instead of installing or removing the pane on
// a guess.
func TestUsagePane_UnreadableSetting(t *testing.T) {
	env := setupUsagePaneEnv(t, true)
	brokenUserConfig(t)

	res := GetDoctorCheck(CheckSlugUsagePane).Run(true)
	assert.True(t, res.skipped)
	assert.Equal(t, "could not read the brief_pane setting", res.message)

	_, err := applyUsagePaneSetting(context.Background())
	require.Error(t, err)
	assert.Nil(t, env.log(t), "an unreadable setting must not touch Claude Code")
}

// TestApplyUsagePaneSetting_InstallFails: `ox config set brief_pane on` with a
// claude CLI that refuses the install reports the error instead of a hint
// pointing at a pane that does not exist.
func TestApplyUsagePaneSetting_InstallFails(t *testing.T) {
	setupUsagePaneEnv(t, true)
	t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
	replaceFakeClaude(t, false, "boom")

	hint, err := applyUsagePaneSetting(context.Background())

	require.Error(t, err)
	assert.Empty(t, hint)
}

// TestEnsureUsagePane_Outcomes covers `ox init`'s best-effort install when it
// is not quiet. Failure prevented: init stays silent after installing the
// pane, or crashes on a refused install instead of warning.
func TestEnsureUsagePane_Outcomes(t *testing.T) {
	t.Run("installs and hints", func(t *testing.T) {
		env := setupUsagePaneEnv(t, true)
		t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
		ensureUsagePane(false)
		assert.FileExists(t, filepath.Join(env.state, "installed"))
	})
	t.Run("refused install warns", func(t *testing.T) {
		env := setupUsagePaneEnv(t, true)
		t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
		replaceFakeClaude(t, false, "boom")
		ensureUsagePane(false)
		assert.NoFileExists(t, filepath.Join(env.state, "installed"))
	})
}

// TestUsagePaneCommands_CLIFails: `ox brief pane enable|disable|status` against
// a claude CLI that cannot run its plugin commands returns an error naming
// the action. Failure prevented: a coworker is told the pane was installed or
// removed when Claude Code never changed.
func TestUsagePaneCommands_CLIFails(t *testing.T) {
	tests := []struct {
		sub     string
		wantErr string
	}{
		{"enable", "enable the brief pane"},
		{"disable", "disable the brief pane"},
		{"status", "check the brief pane"},
	}
	for _, tt := range tests {
		t.Run(tt.sub, func(t *testing.T) {
			env := setupUsagePaneEnv(t, true)
			if tt.sub == "disable" {
				_, err := newUsagePaneInstaller().Enable(context.Background())
				require.NoError(t, err)
			}
			require.NoError(t, os.WriteFile(filepath.Join(env.state, "old"), nil, 0o644))

			_, _, err := runUsagePane(t, tt.sub)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
