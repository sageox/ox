package agentwork

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The daemon finalize door has no .recording.json by the time it writes
// meta.json for a SessionEnd / clear / orphan recording. These tests prove
// it still lands native_sessions and stopped_at — from the header the CLI
// stamped, from a state file that is still present, or (for stopped_at)
// from the last entry when nobody recorded a stop.
//
// Customer failure prevented: every recording that ended by closing the
// agent (the common case) arrives in the Ledger with no native ids and no
// stop time, so a Claude Code trace can never be matched to it.

func writeNativeFixtureRaw(t *testing.T, sessionDir, headerExtra string) (string, *session.StoredSession) {
	t.Helper()
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	raw := `{"type":"header","metadata":{"version":"1.0","created_at":"2026-09-21T10:00:00Z","agent_id":"Ox7f3a","agent_type":"claude-code"` + headerExtra + `}}
{"type":"user","content":"hello","timestamp":"2026-09-21T10:05:00Z","seq":1}
{"type":"assistant","content":"hi","timestamp":"2026-09-21T10:40:00Z","seq":2}
`
	rawPath := filepath.Join(sessionDir, "raw.jsonl")
	require.NoError(t, os.WriteFile(rawPath, []byte(raw), 0o644))
	stored, err := session.ReadSessionFromPath(rawPath)
	require.NoError(t, err)
	return rawPath, stored
}

func TestWriteMetaAndUploadLFS_NativeSessionsAndStoppedAt(t *testing.T) {
	stampedStop := time.Date(2026, 9, 21, 11, 0, 0, 0, time.UTC)
	stateStop := time.Date(2026, 9, 21, 11, 30, 0, 0, time.UTC)
	lastEntry := time.Date(2026, 9, 21, 10, 40, 0, 0, time.UTC)
	headerNative := `,"native_sessions":[{"id":"hdr-a","source":"startup","first_seen":"2026-09-21T10:00:00Z","last_seen":"2026-09-21T10:00:00Z"}],"stopped_at":"2026-09-21T11:00:00Z"`

	tests := []struct {
		name        string
		headerExtra string
		state       *session.RecordingState // written as .recording.json when non-nil
		wantIDs     []string
		wantStop    time.Time
	}{
		{
			name:        "header stamped by the hook is the carrier",
			headerExtra: headerNative,
			wantIDs:     []string{"hdr-a"},
			wantStop:    stampedStop,
			// Mutation this reds: dropping the stored.Meta fallback in
			// recordingCarrierFields — ids vanish, stop falls to last entry.
		},
		{
			name:        "a still-present state file wins over the header",
			headerExtra: headerNative,
			state: &session.RecordingState{
				AgentID: "Ox7f3a", StoppedAt: &stateStop,
				NativeSessions: []session.NativeSession{{ID: "state-a", Source: "startup"}, {ID: "state-b", Source: "clear"}},
			},
			wantIDs:  []string{"state-a", "state-b"},
			wantStop: stateStop,
		},
		{
			name:     "legacy raw with nothing recorded: no ids, stop is the last entry",
			wantIDs:  nil,
			wantStop: lastEntry,
			// Mutation this reds: `next.StoppedAt = &resolved` removed —
			// meta.json would carry no stop time at all.
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewSessionFinalizeHandler(slog.Default())
			handler.skipGit = true

			sessionName := "2026-09-21T10-00-testuser-Ox7f3a"
			ledgerPath := t.TempDir()
			sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
			rawPath, stored := writeNativeFixtureRaw(t, sessionDir, tt.headerExtra)
			if tt.state != nil {
				data, err := json.Marshal(tt.state)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(sessionDir, recordingMarker), data, 0o600))
			}

			payload := &SessionFinalizePayload{SessionDir: sessionDir, RawPath: rawPath, LedgerPath: ledgerPath}
			_, err := handler.writeMetaAndUploadLFS(payload, stored, &session.SummarizeResponse{Title: "x", Summary: "x"})
			require.NoError(t, err)

			meta, err := lfs.ReadSessionMeta(sessionDir)
			require.NoError(t, err)

			var gotIDs []string
			for _, ns := range meta.NativeSessions {
				gotIDs = append(gotIDs, ns.ID)
			}
			assert.Equal(t, tt.wantIDs, gotIDs)
			require.NotNil(t, meta.StoppedAt, "every finalized session must carry a stop time")
			assert.True(t, meta.StoppedAt.Equal(tt.wantStop), "stopped_at=%s want %s", meta.StoppedAt, tt.wantStop)
			assert.True(t, meta.StoppedAt.After(meta.CreatedAt), "stopped_at must be later than created_at")
		})
	}
}

// A CLI door that already wrote the fields must not have them overwritten
// by the daemon's weaker estimate on a retry.
func TestWriteMetaAndUploadLFS_PreservesCLIWrittenNativeFields(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())
	handler.skipGit = true

	sessionName := "2026-09-21T10-00-testuser-Ox7f3a"
	ledgerPath := t.TempDir()
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	rawPath, stored := writeNativeFixtureRaw(t, sessionDir, "")

	cliStop := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	prior := lfs.NewSessionMeta(sessionName, "testuser", "Ox7f3a", "claude-code", time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)).
		NativeSessions([]lfs.NativeSession{{ID: "cli-a", Source: "startup"}}).
		StoppedAt(cliStop).
		Build()
	require.NoError(t, lfs.WriteSessionMetaOnly(sessionDir, prior))

	payload := &SessionFinalizePayload{SessionDir: sessionDir, RawPath: rawPath, LedgerPath: ledgerPath}
	_, err := handler.writeMetaAndUploadLFS(payload, stored, &session.SummarizeResponse{Title: "x", Summary: "x"})
	require.NoError(t, err)

	meta, err := lfs.ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	require.Len(t, meta.NativeSessions, 1)
	assert.Equal(t, "cli-a", meta.NativeSessions[0].ID)
	require.NotNil(t, meta.StoppedAt)
	assert.True(t, meta.StoppedAt.Equal(cliStop))
}

func TestSynthesizeMeta_CarriesNativeSessionsAndStoppedAt(t *testing.T) {
	handler := NewSessionFinalizeHandlerForTest(nil)
	sessionDir := filepath.Join(t.TempDir(), "2026-09-21T10-00-testuser-Ox7f3a")
	_, _ = writeNativeFixtureRaw(t, sessionDir,
		`,"native_sessions":[{"id":"hdr-a","source":"resume","first_seen":"2026-09-21T10:00:00Z","last_seen":"2026-09-21T10:00:00Z"}],"stopped_at":"2026-09-21T11:00:00Z"`)

	meta := handler.synthesizeMeta(sessionDir, filepath.Base(sessionDir))
	require.NotNil(t, meta)
	require.Len(t, meta.NativeSessions, 1)
	assert.Equal(t, "hdr-a", meta.NativeSessions[0].ID)
	assert.Equal(t, "resume", meta.NativeSessions[0].Source)
	require.NotNil(t, meta.StoppedAt)
	assert.True(t, meta.StoppedAt.Equal(time.Date(2026, 9, 21, 11, 0, 0, 0, time.UTC)))
}
