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

func TestProcessAgentSession_RefusesUnboundSource(t *testing.T) {
	state := &session.RecordingState{AgentID: "OxGem", AdapterName: "gemini"}
	result, err := processAgentSession(t.TempDir(), state)
	require.Nil(t, result)
	require.True(t, errors.Is(err, adapterruntime.ErrUnboundSource))
}

func TestWriteRawHeader_RefusesUnboundSource(t *testing.T) {
	state := &session.RecordingState{AgentID: "OxGem", AdapterName: "gemini", SessionPath: t.TempDir()}
	err := writeRawHeader(t.TempDir(), state)
	require.True(t, errors.Is(err, adapterruntime.ErrUnboundSource))
	require.NoFileExists(t, state.SessionPath+"/raw.jsonl")
}

func TestProcessAgentSession_RefusesQuarantinedSource(t *testing.T) {
	state := &session.RecordingState{AgentID: "OxCla", AdapterName: "claude-code", SourceRejected: true}
	result, err := processAgentSession(t.TempDir(), state)
	require.Nil(t, result)
	require.ErrorContains(t, err, "preserved for manual review")
}
