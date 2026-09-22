package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/trace/model"
	"github.com/stretchr/testify/require"
)

// Exercise the actual hook doors: .recording.json is gone before the daemon
// runs, so the raw carrier must include the fixed stop offset, not a new EOF.
func TestTraceBoundariesHookCarrier(t *testing.T) {
	for _, door := range []string{"end", "clear"} {
		t.Run(door, func(t *testing.T) {
			projectRoot, _ := setupTestProject(t)
			t.Setenv("OX_XDG_DISABLE", "")
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			t.Setenv("SAGEOX_DAEMON", "false")
			configPath := filepath.Join(t.TempDir(), "config.yaml")
			require.NoError(t, os.WriteFile(configPath, []byte("trace:\n  enabled: true\n"), 0600))
			t.Setenv("OX_USER_CONFIG", configPath)
			const id = "8e6b4570-b823-4c19-a561-4511a6e89cad"
			state, err := session.StartRecording(projectRoot, session.StartRecordingOptions{AgentID: "OxTraceHook", AdapterName: "claude-code", Username: "test", AgentSessionID: id})
			require.NoError(t, err)
			require.NoError(t, writeRawHeader(projectRoot, state))
			dir := filepath.Join(paths.TraceSpoolDir(), id)
			require.NoError(t, os.MkdirAll(dir, 0700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "traces.jsonl"), []byte("spans\n"), 0600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "logs.jsonl"), []byte("events\n"), 0600))
			ctx := &HookContext{Phase: phaseEnd, AgentType: "claude-code", ProjectRoot: projectRoot, Marker: &SessionMarker{AgentID: state.AgentID}}
			if door == "end" {
				require.NoError(t, handleEnd(ctx))
			} else {
				stopSessionForClear(ctx, state.AgentID)
			}
			cleared, err := session.LoadRecordingStateForAgent(projectRoot, state.AgentID)
			require.NoError(t, err)
			require.Nil(t, cleared)
			stored, err := session.ReadSessionFromPath(filepath.Join(state.SessionPath, "raw.jsonl"))
			require.NoError(t, err)
			require.NotNil(t, stored.Meta.TraceCapture)
			boundaries := stored.Meta.TraceCapture.Boundaries
			require.Len(t, boundaries, 2)
			require.Equal(t, "start", boundaries[0].Action)
			require.Equal(t, "stop", boundaries[1].Action)
			require.Equal(t, model.Offsets{Spans: 6, Events: 7}, boundaries[1].Offsets[id])
		})
	}
}
