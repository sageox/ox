package agentwork

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: a capture that dies mid-batch leaves raw.jsonl.append.json
// beside the transcript. When that recording is then abandoned, the daemon's
// reclaim copied every cache file into sessions/<name>/ and staged the whole
// folder, so the machine-local journal, and the unacknowledged batch it
// describes, were committed to the shared Ledger.

const (
	journalAcked = "{\"_meta\":{\"schema_version\":\"1\",\"agent_type\":\"claude-code\"}}\n" +
		"{\"type\":\"user\",\"content\":\"hello\",\"seq\":1}\n" +
		"{\"type\":\"assistant\",\"content\":\"hi there\",\"seq\":2}\n"
	journalBatch = "{\"type\":\"user\",\"content\":\"unacknowledged batch\",\"seq\":3}\n"

	journalOldCursor int64 = 100
	journalNewCursor int64 = 250
)

// writeInterruptedBatch lays out what AppendRecordingBatch leaves when its
// process dies after sealing a batch but before the cursor commit: the
// acknowledged transcript, the batch bytes past it, and the sealed journal.
func writeInterruptedBatch(t *testing.T, sessionDir string) (rawPath, journalPath string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	rawPath = filepath.Join(sessionDir, artifactRaw)
	require.NoError(t, os.WriteFile(rawPath, []byte(journalAcked+journalBatch), 0o644))

	journal, err := json.Marshal(map[string]int64{
		"raw_size":   int64(len(journalAcked)),
		"final_size": int64(len(journalAcked) + len(journalBatch)),
		"old_offset": journalOldCursor,
		"new_offset": journalNewCursor,
	})
	require.NoError(t, err)
	journalPath = rawPath + ".append.json"
	require.NoError(t, os.WriteFile(journalPath, journal, 0o600))
	return rawPath, journalPath
}

func assertBatchSettled(t *testing.T, rawPath, journalPath string, wantBatch, wantJournal bool) {
	t.Helper()
	raw, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	assert.True(t, bytes.HasPrefix(raw, []byte(journalAcked)), "acknowledged entries must survive recovery")
	assert.Equal(t, wantBatch, bytes.Contains(raw, []byte("unacknowledged batch")), "batch bytes in raw.jsonl")
	_, statErr := os.Stat(journalPath)
	assert.Equal(t, wantJournal, statErr == nil, "journal left on disk")
}

func TestRecoverRawFromSessionFile_SettlesAppendJournal(t *testing.T) {
	tests := []struct {
		name        string
		cursor      int64
		wantErr     bool
		wantBatch   bool
		wantJournal bool
	}{
		{name: "unacknowledged batch is rolled back", cursor: journalOldCursor},
		{name: "committed batch is kept", cursor: journalNewCursor, wantBatch: true},
		{name: "conflicting cursor fails closed", cursor: 175, wantErr: true, wantBatch: true, wantJournal: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessionDir := t.TempDir()
			rawPath, journalPath := writeInterruptedBatch(t, sessionDir)
			recPath := filepath.Join(sessionDir, recordingMarker)
			writeRecordingState(t, recPath, session.RecordingState{
				AgentID:      "OxJRNL",
				StartedAt:    time.Now().Add(-25 * time.Hour),
				SourceOffset: tt.cursor,
			})

			hasRaw, err := recoverRawFromSessionFile(slog.Default(), recPath, sessionDir, rawPath)
			if tt.wantErr {
				require.Error(t, err, "an unprovable journal must defer recovery")
			} else {
				require.NoError(t, err)
				assert.True(t, hasRaw, "acknowledged entries are substantive")
			}
			assertBatchSettled(t, rawPath, journalPath, tt.wantBatch, tt.wantJournal)
		})
	}
}

// Both daemon entry points that reclaim an abandoned recording must settle the
// journal before the session is queued for finalization.
func TestAbandonedRecordingDetect_SettlesAppendJournal(t *testing.T) {
	const agentID = "OxJRNL"
	tests := []struct {
		name   string
		detect func(h *SessionFinalizeHandler, ledgerPath string, pid int) ([]*WorkItem, error)
	}{
		{
			name: "anti-entropy detect",
			detect: func(h *SessionFinalizeHandler, ledgerPath string, _ int) ([]*WorkItem, error) {
				return h.Detect(ledgerPath)
			},
		},
		{
			name: "orphan detect for the agent",
			detect: func(h *SessionFinalizeHandler, ledgerPath string, pid int) ([]*WorkItem, error) {
				return h.DetectOrphanedForAgent(ledgerPath, agentID, pid), nil
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ledgerPath := t.TempDir()
			sessionDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", "2026-09-29T10-00-user-OxJRNL")
			rawPath, journalPath := writeInterruptedBatch(t, sessionDir)
			pid := deadPID(t)
			writeRecordingState(t, filepath.Join(sessionDir, recordingMarker), session.RecordingState{
				AgentID:      agentID,
				StartedAt:    time.Now().Add(-25 * time.Hour),
				ParentPID:    pid,
				SourceOffset: journalOldCursor,
			})

			items, err := tt.detect(NewSessionFinalizeHandler(slog.Default()), ledgerPath, pid)
			require.NoError(t, err)
			require.Len(t, items, 1, "the abandoned recording is still reclaimed")
			assertBatchSettled(t, rawPath, journalPath, false, false)
		})
	}
}

func TestStageSessionInLedger_LeavesAppendJournalBehind(t *testing.T) {
	ledgerPath := t.TempDir()
	sessionName := "2026-09-29T10-00-user-OxJRNL"
	cacheDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", sessionName)
	_, journalPath := writeInterruptedBatch(t, cacheDir)
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "summary.md"), []byte("summary"), 0o644))

	payload := &SessionFinalizePayload{SessionDir: cacheDir, LedgerPath: ledgerPath}
	_, err := NewSessionFinalizeHandler(slog.Default()).stageSessionInLedger(payload)
	require.NoError(t, err)

	dest := filepath.Join(ledgerPath, "sessions", sessionName)
	assert.FileExists(t, filepath.Join(dest, artifactRaw))
	assert.FileExists(t, filepath.Join(dest, "summary.md"))
	assert.NoFileExists(t, filepath.Join(dest, filepath.Base(journalPath)), "the journal must not enter the Ledger")
}
