package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/pkg/adapterruntime"
	"github.com/stretchr/testify/require"
)

func TestUnboundSourceCannotCaptureOrPublish(t *testing.T) {
	for _, tc := range []struct {
		name    string
		adapter string
		id      string
	}{
		{"gemini-missing-id", "gemini", ""},
		{"gemini-unmatched-id", "gemini", "current"},
		{"gemini-unverified-repo", "gemini", "foreign"},
		{"amp-missing-id", "amp", ""},
		{"amp-unmatched-id", "amp", "T-current"},
	} {
		for _, phase := range []string{"hook", "final-drain", "upload"} {
			t.Run(tc.name+"/"+phase, func(t *testing.T) {
				projectRoot, agentID, _ := setupHandleAfterToolTest(t)
				foreign := filepath.Join(t.TempDir(), "T-foreign.jsonl")
				require.NoError(t, os.WriteFile(foreign, []byte("{\"type\":\"user\",\"content\":\"foreign project secret\"}\n"), 0o600))
				require.NoError(t, session.UpdateRecordingStateForAgent(projectRoot, agentID, func(s *session.RecordingState) {
					s.AdapterName = tc.adapter
					s.AgentSessionID = tc.id
					s.SessionFile = foreign
				}))
				state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
				require.NoError(t, err)
				require.NotNil(t, state)
				raw := filepath.Join(state.SessionPath, "raw.jsonl")
				before, err := os.ReadFile(raw)
				require.NoError(t, err)
				ledger := filepath.Join(t.TempDir(), "ledger")
				switch phase {
				case "hook":
					err = captureHookEntries(&HookContext{ProjectRoot: projectRoot}, agentID, state.SessionPath, state.SessionID)
				case "final-drain":
					_, err = finalizeIncrementalSession(projectRoot, state, raw, nil, &agentSessionResult{})
				case "upload":
					err = uploadSessionToLedgerWithEffects(projectRoot, &agentSessionResult{RawPath: raw, EntryCount: 1}, state, ledger, "test", sessionUploadEffects{})
				}
				require.ErrorIs(t, err, adapterruntime.ErrUnboundSource)
				after, err := os.ReadFile(raw)
				require.NoError(t, err)
				require.Equal(t, before, after, "rejected source must not reach raw recording")
				persisted, err := session.ReadRecordingStateFile(state.SessionPath)
				require.NoError(t, err)
				require.True(t, persisted.SourceRejected)
				require.Equal(t, state.SourceOffset, persisted.SourceOffset)
				_, err = os.Stat(ledger)
				require.True(t, os.IsNotExist(err), "rejection must precede ledger writes")
			})
		}
	}
}
