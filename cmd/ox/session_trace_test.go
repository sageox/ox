package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/flags"
	localtrace "github.com/sageox/ox/internal/trace"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func isolateTraceCommand(t *testing.T) {
	t.Helper()
	base := t.TempDir()
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("OX_USER_CONFIG", filepath.Join(base, "user.yaml"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(base, "cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(base, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(base, "runtime"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(base, "claude"))
	t.Setenv("OX_SESSION_RECORDING", "disabled")
	t.Setenv("SAGEOX_DAEMON", "false")
	ensure, stop, purge, health := traceEnsureRunning, traceStop, tracePurge, traceHealth
	previous := flags.Get()
	t.Cleanup(func() {
		traceEnsureRunning, traceStop, tracePurge, traceHealth = ensure, stop, purge, health
		flags.Init(context.Background(), traceFlagsSnapshot{previous})
		syncFeatureGatedCommands(rootCmd)
	})
	traceHealth = func(context.Context, int) (localtrace.HealthInfo, error) {
		return localtrace.HealthInfo{}, errors.New("not listening")
	}
}

type traceFlagsSnapshot struct{ flags.Flags }

func (s traceFlagsSnapshot) Patch(context.Context) (*flags.Patch, flags.Source, error) {
	return &flags.Patch{TraceEnabled: &s.TraceEnabled, BulletinEnabled: &s.BulletinEnabled, AttestEnabled: &s.AttestEnabled,
		CodeDBEnabled: &s.CodeDBEnabled, WhisperEnabled: &s.WhisperEnabled, DistillEnabled: &s.DistillEnabled,
		AutoDistill: &s.AutoDistill, TUIEnabled: &s.TUIEnabled, PrimeAppend: &s.PrimeAppend,
		DisableFileDeleteTools: &s.DisableFileDeleteTools, DisableShellExecTools: &s.DisableShellExecTools}, flags.SourceDefault, nil
}

func traceTestCommand(jsonMode bool) (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().Int("port", localtrace.DefaultPort, "")
	cmd.Flags().Bool("purge", false, "")
	cmd.Flags().Bool("json", jsonMode, "")
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetContext(context.Background())
	return cmd, out
}

// Failure prevented: a copied feature flag hides help but leaves the experimental
// command executable, or leaks it into generated reference docs.
func TestTraceCommandsAreUnavailableUntilFlagEnabled(t *testing.T) {
	isolateTraceCommand(t)
	for _, enabled := range []string{"", "1", "false"} {
		t.Setenv("FEATURE_TRACE", enabled)
		flags.Init(context.Background(), flags.EnvProvider{})
		syncFeatureGatedCommands(rootCmd)
		found, rest, err := rootCmd.Find([]string{"session", "trace", "status"})
		if enabled == "1" {
			require.NoError(t, err)
			require.Equal(t, sessionTraceStatusCmd, found)
			require.Empty(t, rest)
		} else {
			require.False(t, commandRegistered(sessionCmd, sessionTraceCmd))
			require.NotEqual(t, sessionTraceStatusCmd, found)
		}
	}
	// Docs must remain public-only even if the generating developer opted in.
	root := &cobra.Command{Use: "ox"}
	sessions := &cobra.Command{Use: "session"}
	sessions.AddCommand(&cobra.Command{Use: "trace"}, &cobra.Command{Use: "list"})
	root.AddCommand(sessions)
	prepareDocsCommandTree(root)
	require.Len(t, sessions.Commands(), 1)
	require.Equal(t, "list", sessions.Commands()[0].Name())
}

// Failure prevented: enable appears successful without persisting opt-in, or
// disable stops the process but the next hook immediately starts it again.
func TestTraceEnableDisablePersistsOptInBeforeProcessActions(t *testing.T) {
	isolateTraceCommand(t)
	require.NoError(t, config.SaveUserConfig(&config.UserConfig{DisplayName: "Keep Me"}))
	started, stopped, purged := 0, 0, 0
	traceEnsureRunning = func(_ context.Context, port int, allowed func() bool) error {
		require.True(t, allowed())
		require.Equal(t, 15432, port)
		started++
		return nil
	}
	traceStop = func(_ context.Context, port int) error {
		require.False(t, traceOptedIn(port))
		stopped++
		return nil
	}
	tracePurge = func(_ context.Context, port int) error {
		require.False(t, traceOptedIn(port))
		purged++
		return nil
	}
	cmd, out := traceTestCommand(true)
	require.NoError(t, cmd.Flags().Set("port", "15432"))
	require.NoError(t, runSessionTraceEnable(cmd, nil))
	require.Equal(t, 1, started)
	var status sessionTraceStatus
	require.NoError(t, json.Unmarshal(out.Bytes(), &status))
	require.True(t, status.Enabled)
	require.Equal(t, 15432, status.Port)
	require.NoError(t, runSessionTraceDisable(cmd, nil))
	require.Equal(t, 1, stopped)
	cfg, err := config.LoadUserConfig()
	require.NoError(t, err)
	require.Nil(t, cfg.Trace)
	require.Equal(t, "Keep Me", cfg.DisplayName)
	require.NoError(t, cmd.Flags().Set("purge", "true"))
	require.NoError(t, runSessionTraceDisable(cmd, nil))
	require.Equal(t, 1, purged)
}

func TestTraceEnableReportsStartupFailureAndKeepsRetryableOptIn(t *testing.T) {
	isolateTraceCommand(t)
	traceEnsureRunning = func(context.Context, int, func() bool) error { return errors.New("port occupied") }
	cmd, _ := traceTestCommand(false)
	require.ErrorContains(t, runSessionTraceEnable(cmd, nil), "opt-in saved, but receiver did not start")
	require.True(t, traceOptedIn(localtrace.DefaultPort))
	require.NoError(t, cmd.Flags().Set("port", "23456"))
	require.ErrorContains(t, runSessionTraceEnable(cmd, nil), "disable")
	require.NoError(t, cmd.Flags().Set("port", "0"))
	require.ErrorContains(t, runSessionTraceEnable(cmd, nil), "between 1 and 65535")
}

// Failure prevented: receiver restart works in an isolated helper but is never
// wired into the actual hook, or errors escape and disrupt Claude Code startup.
func TestTraceSessionStartRestartsOnlyForPersistentOptIn(t *testing.T) {
	isolateTraceCommand(t)
	t.Setenv("FEATURE_TRACE", "")
	called := 0
	traceEnsureRunning = func(_ context.Context, port int, allowed func() bool) error {
		called++
		require.True(t, allowed())
		return errors.New("receiver unavailable")
	}
	ctx := &HookContext{AgentType: "claude-code", Marker: &SessionMarker{AgentID: "Oxtest"}, ProjectRoot: t.TempDir()}
	require.NoError(t, handleStart(ctx))
	require.Zero(t, called)
	require.NoError(t, config.SaveUserConfig(&config.UserConfig{Trace: &config.TraceConfig{Enabled: true}}))
	require.NoError(t, handleStart(ctx))
	require.Equal(t, 1, called, "FEATURE_TRACE is a command gate, not a saved opt-in override")
	ctx.AgentType = "codex"
	require.NoError(t, handleStart(ctx))
	require.Equal(t, 1, called)
}

func TestTraceDoctorRegistersOnlyForFlagAndOptInAndRepairsReceiver(t *testing.T) {
	isolateTraceCommand(t)
	t.Setenv("FEATURE_TRACE", "1")
	flags.Init(context.Background(), flags.EnvProvider{})
	syncTraceDoctorCheck()
	require.Nil(t, GetDoctorCheck(CheckSlugSessionTrace))
	require.NoError(t, config.SaveUserConfig(&config.UserConfig{Trace: &config.TraceConfig{Enabled: true}}))
	syncTraceDoctorCheck()
	require.NotNil(t, GetDoctorCheck(CheckSlugSessionTrace))
	require.Equal(t, FixLevelAuto, GetDoctorCheck(CheckSlugSessionTrace).FixLevel)
	started := false
	traceEnsureRunning = func(_ context.Context, port int, allowed func() bool) error { started = allowed(); return nil }
	check := checkSessionTrace(true)
	require.True(t, started)
	require.True(t, check.warning, "missing receiver/exporter must never report healthy")
	t.Setenv("FEATURE_TRACE", "0")
	flags.Init(context.Background(), flags.EnvProvider{})
	syncTraceDoctorCheck()
	require.Nil(t, GetDoctorCheck(CheckSlugSessionTrace))
}

func TestTraceConfigErrorsDoNotBecomeDisabledSuccess(t *testing.T) {
	isolateTraceCommand(t)
	require.NoError(t, os.WriteFile(os.Getenv("OX_USER_CONFIG"), []byte("trace: [bad"), 0600))
	cmd, _ := traceTestCommand(false)
	require.Error(t, runSessionTraceEnable(cmd, nil))
	require.Error(t, runSessionTraceStatus(cmd, nil))
	require.Error(t, runSessionTraceDisable(cmd, nil))
}

func TestTraceInvalidSavedPortCanBeDisabledOrRepaired(t *testing.T) {
	isolateTraceCommand(t)
	invalid := &config.UserConfig{Trace: &config.TraceConfig{Enabled: true, Port: -7}}
	require.NoError(t, config.SaveUserConfig(invalid))
	stopped := false
	traceStop = func(_ context.Context, port int) error {
		stopped = true
		require.Equal(t, localtrace.DefaultPort, port)
		return nil
	}
	cmd, _ := traceTestCommand(true)
	require.NoError(t, runSessionTraceDisable(cmd, nil))
	require.True(t, stopped)
	require.False(t, traceOptedIn(localtrace.DefaultPort))
	require.NoError(t, config.SaveUserConfig(invalid))
	traceEnsureRunning = func(_ context.Context, port int, allowed func() bool) error {
		require.Equal(t, localtrace.DefaultPort, port)
		require.True(t, allowed())
		return nil
	}
	require.NoError(t, cmd.Flags().Set("port", "14318"))
	require.NoError(t, runSessionTraceEnable(cmd, nil))
}

func TestTraceDoctorAcceptsEnvironmentOnlyAndReportsConflictsWithoutEdits(t *testing.T) {
	isolateTraceCommand(t)
	for _, entry := range os.Environ() { // safe: reads names solely to clear inherited exporter variables; no subprocess or credentials.
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "OTEL_") || key == "BETA_TRACING_ENDPOINT" || key == "ENABLE_BETA_TRACING_DETAILED" {
			t.Setenv(key, "")
		}
	}
	for key, value := range map[string]string{
		"FEATURE_TRACE": "1", "CLAUDE_CODE_ENABLE_TELEMETRY": "1", "CLAUDE_CODE_ENHANCED_TELEMETRY_BETA": "1",
		"OTEL_TRACES_EXPORTER": "otlp", "OTEL_LOGS_EXPORTER": "otlp", "OTEL_METRICS_EXPORTER": "none",
		"OTEL_EXPORTER_OTLP_PROTOCOL": "http/json", "OTEL_EXPORTER_OTLP_ENDPOINT": "http://127.0.0.1:14318",
	} {
		t.Setenv(key, value)
	}
	flags.Init(context.Background(), flags.EnvProvider{})
	require.NoError(t, config.SaveUserConfig(&config.UserConfig{Trace: &config.TraceConfig{Enabled: true}}))
	traceHealth = func(context.Context, int) (localtrace.HealthInfo, error) { return localtrace.HealthInfo{}, nil }
	traceEnsureRunning = func(context.Context, int, func() bool) error { return nil }
	check := checkSessionTrace(true)
	require.False(t, check.warning, "%+v", check)
	require.True(t, check.passed, "%+v", check)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "grpc")
	require.True(t, checkSessionTrace(true).warning)
	_, err := os.Stat(os.Getenv("CLAUDE_CONFIG_DIR"))
	require.True(t, os.IsNotExist(err), "doctor must not install Claude settings")
}
