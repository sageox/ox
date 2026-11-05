package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/pkg/adapterruntime"
	"github.com/stretchr/testify/assert"
)

// A recording whose native source cannot be bound to it may read another
// project's transcript, so none of the stop/finalize/upload paths may run.
func TestUnboundRecording_IsNeverProcessedOrPublished(t *testing.T) {
	project, ledger := t.TempDir(), t.TempDir()
	quarantined := func() *session.RecordingState {
		return &session.RecordingState{
			AgentID: "OxBind", AdapterName: "gemini", SessionPath: project,
			SessionFile: filepath.Join(t.TempDir(), "chat.json"),
		}
	}

	_, err := processAgentSession(project, quarantined())
	assert.ErrorIs(t, err, adapterruntime.ErrUnboundSource, "processAgentSession")
	assert.ErrorIs(t, writeRawHeaderWithAttribution(project, quarantined(), nil), adapterruntime.ErrUnboundSource, "writeRawHeaderWithAttribution")
	_, err = finalizeIncrementalSession(project, quarantined(), filepath.Join(project, "raw.jsonl"), nil, &agentSessionResult{})
	assert.ErrorIs(t, err, adapterruntime.ErrUnboundSource, "finalizeIncrementalSession")
	err = uploadSessionToLedger(project, &agentSessionResult{}, quarantined(), ledger, "2026-01-06T14-32-dana-OxBind")
	assert.ErrorIs(t, err, adapterruntime.ErrUnboundSource, "uploadSessionToLedger")

	entries, readErr := os.ReadDir(ledger)
	assert.NoError(t, readErr)
	assert.Empty(t, entries, "nothing may reach the ledger")
	_, statErr := os.Stat(filepath.Join(project, "raw.jsonl"))
	assert.True(t, errors.Is(statErr, os.ErrNotExist), "no raw transcript may be written")
}
