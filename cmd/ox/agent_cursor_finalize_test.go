package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/trace/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCursorHostFinalizePreservesCallerStopBoundaryAcrossCheckpointRefresh(t *testing.T) {
	f := newCursorHostFixture(t)
	state := f.record(t)
	f.write(t, cursorHostUser+cursorHostAnswer+cursorHostTerminal)

	staleStop := time.Date(2026, 10, 8, 8, 0, 0, 0, time.UTC)
	requestedStop := staleStop.Add(45 * time.Minute)
	staleTrace := &model.Capture{Errors: []string{"stale marker trace"}}
	latestNative := []lfs.NativeSession{{
		ID: cursorConversationID, Source: "cursor",
		FirstSeen: staleStop.Add(-time.Minute), LastSeen: requestedStop,
	}}
	require.NoError(t, session.UpdateRecordingStateForAgent(f.root, state.AgentID, func(persisted *session.RecordingState) {
		persisted.Trace = staleTrace
		persisted.StoppedAt = &staleStop
		persisted.NativeSessions = latestNative
	}))

	// Model a stop whose boundary mutation succeeded in memory but whose
	// best-effort marker checkpoint failed. Nil is authoritative: restoring the
	// older trace would publish spans outside the stop boundary.
	state.Trace = nil
	state.StoppedAt = &requestedStop
	rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
	result, err := finalizeIncrementalSession(f.root, state, rawPath, f.reader, &agentSessionResult{})
	require.NoError(t, err)
	require.NotNil(t, result)

	stored, err := session.ReadSessionFromPath(rawPath)
	require.NoError(t, err)
	require.NotNil(t, stored.Meta)
	require.NotNil(t, stored.Meta.StoppedAt)
	assert.Equal(t, requestedStop, *stored.Meta.StoppedAt)
	assert.Nil(t, stored.Meta.TraceCapture)
	assert.Equal(t, latestNative, stored.Meta.NativeSessions, "Cursor checkpoint fields still come from the refreshed marker")

	raw, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), `"trace_capture"`, "an omitted authoritative trace must not stamp the stale marker value")
}
