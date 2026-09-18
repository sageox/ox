package agentwork

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSummaryResultCannotAttachAfterTranscriptReplacement(t *testing.T) {
	name := "2026-09-16-codex-resumed"
	ledger := createTestSession(t, name, nil)
	dir := filepath.Join(ledger, "sessions", name)
	raw := filepath.Join(dir, "raw.jsonl")
	handler := NewSessionFinalizeHandler(slog.Default())
	item := &WorkItem{Type: sessionFinalizeType, Payload: &SessionFinalizePayload{LedgerPath: ledger, SessionDir: dir, RawPath: raw, Missing: requiredArtifacts}}
	_, err := handler.BuildPrompt(item)
	require.NoError(t, err)
	data, err := os.ReadFile(raw)
	require.NoError(t, err)
	data = append(data, []byte("{\"type\":\"user\",\"content\":\"resumed conversation\"}\n")...)
	require.NoError(t, os.WriteFile(raw, data, 0600))
	err = handler.ProcessResult(item, &RunResult{Output: `{"title":"Old result"}`})
	require.ErrorContains(t, err, "summary input was replaced")
	require.NoFileExists(t, filepath.Join(dir, "summary.json"))
	got, err := os.ReadFile(raw)
	require.NoError(t, err)
	require.Equal(t, data, got)
}

func TestSummaryWorkerFailureRemainsRetryable(t *testing.T) {
	name := "2026-09-16-failed-worker"
	ledger := createTestSession(t, name, nil)
	dir := filepath.Join(ledger, "sessions", name)
	handler := NewSessionFinalizeHandler(slog.Default())
	item := &WorkItem{Type: sessionFinalizeType, Payload: &SessionFinalizePayload{LedgerPath: ledger, SessionDir: dir, RawPath: filepath.Join(dir, "raw.jsonl"), Missing: requiredArtifacts}}
	require.ErrorContains(t, handler.ProcessResult(item, &RunResult{ExitCode: 2}), "exited with status 2")
	require.NoFileExists(t, filepath.Join(dir, "summary.json"))
}

func TestRawPublicationMarkerMatchesExactBytes(t *testing.T) {
	dir := t.TempDir()
	raw := filepath.Join(dir, "raw.jsonl")
	require.NoError(t, os.WriteFile(raw, []byte("{broken\n"), 0600))
	digest, err := summarySourceDigest(raw)
	require.NoError(t, err)
	marker := filepath.Join(dir, ".raw-uploaded")
	require.NoError(t, os.WriteFile(marker, []byte(digest+"\n"), 0600))
	h := NewSessionFinalizeHandler(slog.Default())
	p := &SessionFinalizePayload{LedgerPath: t.TempDir(), SessionDir: dir, RawPath: raw}
	require.NoError(t, h.publishRawPending(p))
	require.NoError(t, os.WriteFile(raw, []byte("{changed and broken\n"), 0600))
	require.Error(t, h.publishRawPending(p), "changed bytes must not reuse the old receipt")
	require.NoError(t, os.WriteFile(marker, []byte("uploaded\n"), 0600))
	require.Error(t, h.publishRawPending(p), "legacy marker must not assert exact content coverage")
}

func TestSummaryPublicationFailureRemainsRetryable(t *testing.T) {
	name := "2026-09-16-upload-failure"
	ledger := createTestSession(t, name, nil)
	dir := filepath.Join(ledger, "sessions", name)
	h := NewSessionFinalizeHandler(slog.Default())
	h.projectRoot = t.TempDir()
	// This fixture has no Ledger remote or LFS credentials. Finalization must
	// propagate that upload failure instead of recording successful work.
	item := &WorkItem{Type: sessionFinalizeType, Payload: &SessionFinalizePayload{LedgerPath: ledger, SessionDir: dir, RawPath: filepath.Join(dir, "raw.jsonl"), Missing: requiredArtifacts}}
	err := h.ProcessResult(item, &RunResult{Output: `{"title":"Test Session","summary":"A useful session.","quality_score":0.8,"score_reason":"fine","outcome":"success","key_actions":["changed code"]}`})
	require.Error(t, err)
}
