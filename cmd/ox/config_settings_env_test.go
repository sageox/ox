package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/testguard"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Reject invalid selection before a handler runs, preserving each hosted-read
// protocol and never opening a caller's local config on those isolated paths.
func TestConfigFileStartupRefusal(t *testing.T) {
	noInputCLIEnv(t)
	t.Setenv("DO_NOT_TRACK", "1")
	for _, command := range []string{"config", "glance", "list", "git-credential-helper"} {
		t.Run(command, func(t *testing.T) {
			cmd := &cobra.Command{
				Use:               command,
				PersistentPreRunE: rootCmd.PersistentPreRunE,
				SilenceErrors:     true,
				SilenceUsage:      true,
				Run: func(_ *cobra.Command, _ []string) {
					t.Fatal("invalid configuration reached the command handler")
				},
			}
			path := filepath.Join(t.TempDir(), "missing.yaml")
			cmd.Flags().String("config", "", "")
			cmd.Flags().String("repo", "", "")
			cmd.Flags().String("read-repo", "", "")
			args := []string{"--config", path, "--repo", readSyncTestRepoID, "--read-repo", readSyncTestRepoID}
			cmd.SetArgs(args)
			var stdout, stderr bytes.Buffer
			cmd.SetOut(&stdout)
			cmd.SetErr(&stderr)
			err := cmd.Execute()
			if command == "config" {
				require.ErrorContains(t, err, "--config")
				require.ErrorIs(t, err, os.ErrNotExist)
			} else {
				require.Equal(t, 2, exitCodeOf(t, err))
				assert.NotContains(t, stderr.String(), path, "hosted reads must not inspect or report local config paths")
			}
			assert.Empty(t, stdout.String())
			if command == "config" || command == "git-credential-helper" {
				assert.Empty(t, stderr.String())
			} else {
				assert.Equal(t, "Ledger read failed: invalid_arguments\n", stderr.String())
			}
		})
	}
}

// Invalid selections must preserve the environment override; valid relative
// paths must keep selecting the same file after a command changes directory.
func TestConfigFileSelection(t *testing.T) {
	noInputCLIEnv(t)
	for _, selection := range []string{"unchanged", "relative", "empty", "missing", "directory", "malformed"} {
		t.Run(selection, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			original := filepath.Join(dir, "original.yaml")
			t.Setenv(config.EnvUserConfig, original)
			require.NoError(t, os.WriteFile(original, []byte("display_name: original\n"), 0o600))
			path := "selected.yaml"
			require.NoError(t, os.WriteFile(path, []byte("display_name: selected\n"), 0o600))
			switch selection {
			case "empty":
				path = ""
			case "missing":
				path = "missing.yaml"
			case "directory":
				path = dir
			case "malformed":
				require.NoError(t, os.WriteFile(path, []byte("display_name: [\n"), 0o600))
			}
			cmd := &cobra.Command{}
			cmd.Flags().StringP("config", "c", "", "")
			if selection != "unchanged" {
				require.NoError(t, cmd.ParseFlags([]string{"--config=" + path}))
			}
			err := applyConfigFlag(cmd, nil)
			if selection != "relative" {
				if selection == "unchanged" {
					require.NoError(t, err)
				} else {
					require.ErrorContains(t, err, "--config")
				}
				assert.Equal(t, original, os.Getenv(config.EnvUserConfig))
				return
			}
			require.NoError(t, err)
			assert.True(t, filepath.IsAbs(os.Getenv(config.EnvUserConfig)))
			t.Chdir(t.TempDir())
			selected, err := config.LoadUserConfig()
			require.NoError(t, err)
			assert.Equal(t, "selected", selected.DisplayName)
			require.NoError(t, SetConfigValue("tips", "off", ConfigLevelUser, ""))
			selected, err = config.LoadUserConfigFrom(dir)
			require.NoError(t, err)
			assert.True(t, selected.AreTipsEnabled(), "the default file must not receive this write")
			selected, err = config.LoadUserConfig()
			require.NoError(t, err)
			assert.False(t, selected.AreTipsEnabled(), "the selected file must receive this write")
			before, err := os.ReadFile(original)
			require.NoError(t, err)
			assert.Equal(t, "display_name: original\n", string(before))
		})
	}
}

// Explicit file selection must control real command reads and writes, and a bad
// path must fail before a command can fall back to changing the normal config.
func TestConfigFileCLI(t *testing.T) {
	skipIntegration(t)
	oxBin := testguard.BuildOxBinary(t, repoPath("..", ".."))

	for _, tt := range []struct {
		name      string
		selection string
		command   []string
		value     string
		source    ConfigLevel
		wantError string
	}{
		{name: "default file", command: []string{"config", "get", "session_recording", "--json"}, value: "auto", source: ConfigLevelUser},
		{name: "environment file", selection: "env", command: []string{"config", "get", "session_recording", "--json"}, value: "manual", source: ConfigLevelUser},
		{name: "long flag before command", selection: "before", command: []string{"config", "get", "session_recording", "--json"}, value: "disabled", source: ConfigLevelUser},
		{name: "short flag after command", selection: "short", command: []string{"config", "get", "session_recording", "--json"}, value: "disabled", source: ConfigLevelUser},
		{name: "relative path", selection: "relative", command: []string{"config", "get", "session_recording", "--json"}, value: "disabled", source: ConfigLevelUser},
		{name: "setting environment still wins", selection: "setting-env", command: []string{"config", "get", "session_recording", "--json"}, value: "auto", source: ConfigLevelEnv},
		{name: "write selected file", selection: "flag", command: []string{"config", "set", "session_recording", "auto"}, value: "auto"},
		{name: "unset selected file", selection: "flag", command: []string{"config", "unset", "session_recording"}},
		{name: "missing file read", selection: "missing", command: []string{"config", "get", "session_recording", "--json"}, wantError: "--config"},
		{name: "missing file write", selection: "missing", command: []string{"config", "set", "session_recording", "auto"}, wantError: "--config"},
		{name: "malformed file read", selection: "malformed", command: []string{"config", "get", "session_recording", "--json"}, wantError: "--config"},
		{name: "malformed file write", selection: "malformed", command: []string{"config", "set", "session_recording", "auto"}, wantError: "--config"},
		{name: "directory", selection: "directory", command: []string{"config", "get", "session_recording", "--json"}, wantError: "--config"},
		{name: "empty flag", selection: "empty", command: []string{"config", "set", "session_recording", "auto"}, wantError: "--config requires a file path"},
		{name: "trace reads selected file", selection: "flag", command: []string{"session", "trace", "status", "--json"}},
		{name: "trace rejects missing file", selection: "missing", command: []string{"session", "trace", "status", "--json"}, wantError: "--config"},
		{name: "hosted glance refuses local config", selection: "flag", command: []string{"glance", "--repo", readSyncTestRepoID}, wantError: "Ledger read failed: invalid_arguments"},
		{name: "hosted session list refuses local config", selection: "missing", command: []string{"session", "list", "--repo", readSyncTestRepoID}, wantError: "Ledger read failed: invalid_arguments"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env := append(noInputCLIEnv(t), "FEATURE_TRACE=1")
			dir := t.TempDir()
			defaultFile := filepath.Join(config.GetUserConfigDir(), "config.yaml")
			envFile := filepath.Join(dir, "env.yaml")
			selectedFile := filepath.Join(dir, "selected config.yaml")
			files := map[string]string{
				defaultFile:  "sessions:\n  mode: auto\n",
				envFile:      "sessions:\n  mode: manual\n",
				selectedFile: "sessions:\n  mode: disabled\ntrace:\n  port: 14321\n",
			}
			for path, content := range files {
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
				require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
			}
			args := append([]string(nil), tt.command...)
			if tt.selection != "" {
				env = append(env, config.EnvUserConfig+"="+envFile)
			}
			path := selectedFile
			switch tt.selection {
			case "", "env":
			case "before":
				args = append([]string{"--config", path}, args...)
			case "short":
				args = append(args, "-c", path)
			default:
				switch tt.selection {
				case "relative":
					path = filepath.Base(path)
				case "setting-env":
					env = append(env, config.EnvSessionRecording+"=auto")
				case "missing":
					path = filepath.Join(dir, "missing.yaml")
				case "malformed":
					files[selectedFile] = "session_recording: [\n"
					require.NoError(t, os.WriteFile(selectedFile, []byte(files[selectedFile]), 0o600))
				case "directory":
					path = dir
				case "empty":
					path = ""
				}
				args = append(args, "--config="+path)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := testguard.OxCmdContext(t, ctx, oxBin, dir, env, args...)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			require.NoError(t, ctx.Err(), "stdout: %s\nstderr: %s", stdout.String(), stderr.String())
			if tt.wantError != "" {
				assert.Error(t, err)
				assert.Empty(t, stdout.String(), "invalid configuration must stop before the command runs")
				assert.Contains(t, stderr.String(), tt.wantError)
				if path != "" && tt.wantError == "--config" {
					assert.Contains(t, stderr.String(), path)
				}
			} else {
				require.NoError(t, err, "stderr: %s", stderr.String())
				switch tt.command[1] {
				case "get":
					var value ConfigValue
					require.NoError(t, json.Unmarshal(stdout.Bytes(), &value))
					assert.Equal(t, tt.value, value.Value)
					assert.Equal(t, tt.source, value.Source)
				case "set", "unset":
					t.Setenv(config.EnvUserConfig, selectedFile)
					selected, loadErr := config.LoadUserConfig()
					require.NoError(t, loadErr)
					require.NotNil(t, selected.Sessions)
					assert.Equal(t, tt.value, selected.Sessions.Mode)
					require.NotNil(t, selected.Trace, "saving one preference must preserve other settings")
					assert.Equal(t, 14321, selected.Trace.Port)
					delete(files, selectedFile)
				case "trace":
					var status sessionTraceStatus
					require.NoError(t, json.Unmarshal(stdout.Bytes(), &status))
					assert.Equal(t, 14321, status.Port, "commands with their own pre-run must honor --config too")
				}
			}
			for path, before := range files {
				after, readErr := os.ReadFile(path)
				require.NoError(t, readErr)
				assert.Equal(t, before, string(after), "unexpected modification to %s", path)
			}
			assert.NoFileExists(t, filepath.Join(dir, "missing.yaml"))
		})
	}
}

// TestResolveConfigValue_SessionPublishing_EnvWinsOverUser is the red-first
// proof for the CodeRabbit thread on cmd/ox/config_settings.go:601:
// ResolveSessionPublishing (the resolver actually consulted at runtime)
// gives OX_SESSION_PUBLISHING top priority, but ResolveConfigValue (what
// 'ox config get session_publishing' displays) never consulted the env var
// at all — so a coworker auditing their privacy posture with 'ox config get
// session_publishing' would see "auto" (their stored user value) while the
// CLI was actually running in "manual" because of the env var. That is
// exactly the "setting lists but doesn't reflect what's in effect" defect
// class this PR closes everywhere else.
//
// Run red-first against the pre-fix ResolveConfigValue (no EnvVal / no env
// read in the "session_publishing" case): this fails because cv.Value comes
// back "auto" (from UserVal) and cv.Source comes back "user", not "env".
func TestResolveConfigValue_SessionPublishing_EnvWinsOverUser(t *testing.T) {
	setupIsolatedUserConfig(t)
	require.NoError(t, SetConfigValue("session_publishing", "auto", ConfigLevelUser, ""))
	t.Setenv(config.EnvSessionPublishing, "manual")

	cv, err := ResolveConfigValue("session_publishing", "")
	require.NoError(t, err)

	assert.Equal(t, "manual", cv.Value, "displayed effective value must match what ResolveSessionPublishing actually uses")
	assert.Equal(t, ConfigLevelEnv, cv.Source, "source must be attributed to the env var, not the stored user value")
	assert.Equal(t, "manual", cv.EnvVal)
	assert.Equal(t, "auto", cv.UserVal, "the stored user value is still shown in the override chain, just not treated as effective")
}

// TestResolveConfigValue_SessionRecording_EnvWinsOverUser mirrors the above
// for session_recording, the sibling setting named in contract C2.
func TestResolveConfigValue_SessionRecording_EnvWinsOverUser(t *testing.T) {
	setupIsolatedUserConfig(t)
	require.NoError(t, SetConfigValue("session_recording", "auto", ConfigLevelUser, ""))
	t.Setenv(config.EnvSessionRecording, "disabled")

	cv, err := ResolveConfigValue("session_recording", "")
	require.NoError(t, err)

	assert.Equal(t, "disabled", cv.Value)
	assert.Equal(t, ConfigLevelEnv, cv.Source)
	assert.Equal(t, "disabled", cv.EnvVal)
	assert.Equal(t, "auto", cv.UserVal)
}

// TestResolveConfigValue_SessionPublishing_NoEnv_UnaffectedByFix guards the
// precedence chain the fix must NOT disturb: with no env var set, the
// pre-existing user > repo > team > default resolution is untouched.
func TestResolveConfigValue_SessionPublishing_NoEnv_UnaffectedByFix(t *testing.T) {
	setupIsolatedUserConfig(t)
	t.Setenv(config.EnvSessionPublishing, "") // explicit: no ambient leak from the developer's shell
	require.NoError(t, SetConfigValue("session_publishing", "manual", ConfigLevelUser, ""))

	cv, err := ResolveConfigValue("session_publishing", "")
	require.NoError(t, err)

	assert.Equal(t, "manual", cv.Value)
	assert.Equal(t, ConfigLevelUser, cv.Source)
	assert.Empty(t, cv.EnvVal)
}

// TestResolveConfigValue_OtherSettings_NeverGetEnvVal confirms the fix is
// scoped to session_publishing/session_recording only (contract C2 — no
// re-architecture of ResolveConfigValue for every setting). A setting with
// no env layer must never populate EnvVal or report ConfigLevelEnv.
func TestResolveConfigValue_OtherSettings_NeverGetEnvVal(t *testing.T) {
	setupIsolatedUserConfig(t)
	require.NoError(t, SetConfigValue("telemetry", "off", ConfigLevelUser, ""))

	cv, err := ResolveConfigValue("telemetry", "")
	require.NoError(t, err)

	assert.Equal(t, "off", cv.Value)
	assert.Equal(t, ConfigLevelUser, cv.Source)
	assert.Empty(t, cv.EnvVal)
}
