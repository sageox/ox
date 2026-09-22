package main

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/trace/materialize"
	"github.com/stretchr/testify/require"
)

// A pause marker survives an AI coworker restart. Auto-start must persist its
// trace pause before writing the raw header, so a recovered recording cannot
// expose exports produced while the inherited pause remains in effect.
func TestTracePrimeInheritsPauseBeforeDurableHeader(t *testing.T) {
	project, _ := setupTestProject(t)
	t.Chdir(project)
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("SAGEOX_DAEMON", "false")
	t.Setenv("OX_SESSION_RECORDING", "auto")
	t.Setenv("OX_CLEAR_PRIOR_SESSION", "")
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("trace:\n  enabled: true\n"), 0600))
	t.Setenv("OX_USER_CONFIG", configPath)
	oldCfg := cfg
	cfg = &config.Config{}
	t.Cleanup(func() { cfg = oldCfg })
	ledgerPath, err := ledger.DefaultPath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(ledgerPath, ".git"), 0700))
	const agentID = "OxTracePausedPrime"
	const nativeID = "55555555-5555-4555-8555-555555555555"
	require.NoError(t, session.MarkExplicitPause(project, agentID, 7))

	status := startSessionRecording(project, agentID, "claude-code", "", "", nativeID)
	require.NotNil(t, status)
	require.True(t, status.AutoStarted)
	state, err := session.LoadRecordingStateForAgent(project, agentID)
	require.NoError(t, err)
	require.NotNil(t, state)
	require.True(t, state.InheritedPause)
	require.NotNil(t, state.SuspendedAt)
	require.Equal(t, 1, state.PauseCount)
	require.NotNil(t, state.Trace)
	require.Len(t, state.Trace.Boundaries, 2)
	require.Equal(t, "start", state.Trace.Boundaries[0].Action)
	require.Equal(t, "pause", state.Trace.Boundaries[1].Action)
	rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
	stored, err := session.ReadSessionFromPath(rawPath)
	require.NoError(t, err)
	require.Equal(t, state.Trace, stored.Meta.TraceCapture, "the raw header must retain the inherited trace pause")
	_, _, paused := session.PeekExplicitPause(project, agentID)
	require.True(t, paused, "auto-start must not consume the explicit pause")

	spool := filepath.Join(paths.TraceSpoolDir(), nativeID)
	require.NoError(t, os.MkdirAll(spool, 0700))
	private := []byte(`{"resourceSpans":[{"scopeSpans":[{"spans":[{"name":"PRIVATE_WHILE_PAUSED"}]}]}]}` + "\n")
	require.NoError(t, os.WriteFile(filepath.Join(spool, "traces.jsonl"), private, 0600))
	require.NoError(t, stampRecordingCarrierAtStop(state, time.Now()))
	stored, err = session.ReadSessionFromPath(rawPath)
	require.NoError(t, err)
	cache, trace, err := session.MaterializeTraces(ledgerPath, filepath.Base(state.SessionPath), stored.Meta.TraceCapture)
	require.NoError(t, err)
	require.Zero(t, trace.Spans)
	require.Equal(t, int64(len(private)), trace.PausedBytesSkipped)
	file, err := os.Open(filepath.Join(cache, materialize.SpansFile))
	require.NoError(t, err)
	defer file.Close()
	gz, err := gzip.NewReader(file)
	require.NoError(t, err)
	plain, err := io.ReadAll(gz)
	require.NoError(t, err)
	require.NoError(t, gz.Close())
	require.Empty(t, plain, "exports from the inherited pause must never enter attachments")
}
