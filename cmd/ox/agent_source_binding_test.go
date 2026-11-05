package main

import (
	"errors"
	"testing"

	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/pkg/adapterruntime"
	"github.com/stretchr/testify/require"
)

func TestRecoverFromCache_RefusesUnboundLegacySource(t *testing.T) {
	state := &session.RecordingState{AgentID: "OxGem", AdapterName: "gemini"}
	err := recoverFromCache(nil, t.TempDir(), state, "")
	require.Error(t, err)
	require.True(t, errors.Is(err, adapterruntime.ErrUnboundSource))
	require.True(t, state.SourceRejected)
}

func TestSendSessionWatchStart_RefusesUnboundSource(t *testing.T) {
	state := &session.RecordingState{AgentID: "OxGem", AdapterName: "gemini"}
	sendSessionWatchStart(state, t.TempDir())
	require.True(t, state.SourceRejected)
	require.Empty(t, state.SessionFile, "no native file may be discovered for an unbound source")
}
