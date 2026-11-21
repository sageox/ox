package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLedgerSessionDestinationOwnership(t *testing.T) {
	const (
		name        = "2026-01-06T14-32-ryan-Ox7f3a"
		recordingID = "ses_019f6f6a-d182-7f4e-a54d-8dcb44567c8d"
		otherID     = "ses_019f6f6a-d182-7f4e-a54d-8dcb44567c8e"
		finalized   = `{"session_id":"` + recordingID + `"}`
		draft       = `{"draft":true,"session_id":"` + recordingID + `"}`
	)
	tests := []struct {
		desc        string
		name        string
		meta        string
		incomingID  string
		recoveryErr string
		finalizeErr string
	}{
		{desc: "new destination", name: name, incomingID: recordingID},
		{desc: "retry of the same recording", name: name, meta: finalized, incomingID: recordingID},
		{desc: "name belongs to a finalized recording", name: name, meta: finalized, incomingID: otherID,
			recoveryErr: "not the incoming session ID", finalizeErr: "not the incoming session ID"},
		{desc: "incoming recording lacks identity", name: name, meta: finalized,
			recoveryErr: "recording carries no session ID", finalizeErr: "recording carries no session ID"},
		{desc: "legacy finalized metadata lacks identity", name: name, meta: `{}`, incomingID: recordingID,
			recoveryErr: "legacy metadata without a session ID", finalizeErr: "legacy metadata without a session ID"},
		{desc: "unreadable metadata cannot establish ownership", name: name, meta: "{", incomingID: recordingID,
			recoveryErr: "unreadable existing metadata", finalizeErr: "unreadable existing metadata"},
		{desc: "matching draft", name: name, meta: draft, incomingID: recordingID},
		{desc: "normal finalization can keep the draft's ID", name: name, meta: draft, incomingID: otherID,
			recoveryErr: "belongs to session ID"},
		{desc: "recovery can adopt the draft's ID", name: name, meta: draft},
		{desc: "destination must be a single session", name: "nested/session", incomingID: recordingID,
			recoveryErr: "not a single session name", finalizeErr: "not a single session name"},
	}
	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			dest := t.TempDir()
			if tt.meta != "" {
				require.NoError(t, os.WriteFile(filepath.Join(dest, "meta.json"), []byte(tt.meta), 0o600))
			}
			checks := []struct {
				err  error
				want string
			}{
				{CheckLedgerSessionDestination(dest, tt.name, tt.incomingID), tt.recoveryErr},
				{CheckFinalizedLedgerSessionDestination(dest, tt.name, tt.incomingID), tt.finalizeErr},
			}
			for _, check := range checks {
				if check.want == "" {
					require.NoError(t, check.err)
				} else {
					require.ErrorIs(t, check.err, ErrLedgerSessionOverwrite)
					require.ErrorContains(t, check.err, check.want)
				}
			}
		})
	}
}
