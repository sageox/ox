package autofix

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedSession writes a minimal session dir into ledger/sessions/<name>
// with the given meta. Returns the session path. summaryTitle, if
// non-empty, is written into a real summary.json so the recovery
// path can pick it up.
func seedSession(t *testing.T, sessionsDir, name string, m *lfs.SessionMeta, summaryTitle string) string {
	t.Helper()
	dir := filepath.Join(sessionsDir, name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	if m.SessionName == "" {
		m.SessionName = name
	}
	if m.Files == nil {
		m.Files = make(map[string]lfs.FileRef)
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	if m.Version == "" {
		m.Version = "1.0"
	}
	require.NoError(t, lfs.WriteSessionMetaOnly(dir, m))
	if summaryTitle != "" {
		body, err := json.Marshal(map[string]string{"title": summaryTitle})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "summary.json"), body, 0o644))
	}
	return dir
}

// TestRepairLedgerSessionTitles_HealthyLedgerIsClean is the
// idempotency floor for the autofix check. A ledger whose sessions all
// have populated titles must emit StatusClean — anything else would
// mean the autofix scheduler keeps re-flagging healthy state.
func TestRepairLedgerSessionTitles_HealthyLedgerIsClean(t *testing.T) {
	ledger := t.TempDir()
	sessionsDir := filepath.Join(ledger, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o755))
	seedSession(t, sessionsDir, "2026-05-01T10-00-test-OxAAAA", &lfs.SessionMeta{Title: "Real one"}, "")
	seedSession(t, sessionsDir, "2026-05-01T11-00-test-OxBBBB", &lfs.SessionMeta{Title: "Another real"}, "")

	res := repairLedgerSessionTitles(sessionsDir, "/fake/repo")
	assert.Equal(t, StatusClean, res.Status, "all-healthy ledger must report clean")
}

// TestRepairLedgerSessionTitles_PendingLedgerIsClean verifies that repeated
// doctor passes leave an in-flight summary untouched and report clean.
func TestRepairLedgerSessionTitles_PendingLedgerIsClean(t *testing.T) {
	sessionsDir := filepath.Join(t.TempDir(), "sessions")
	dir := seedSession(t, sessionsDir, "2026-09-17T04-58-test-OxPEND",
		&lfs.SessionMeta{SummaryStatus: "pending", SummaryAttempts: lfs.MaxSummaryAttempts - 1}, "")
	metaPath := filepath.Join(dir, "meta.json")
	before, err := os.ReadFile(metaPath)
	require.NoError(t, err)

	for range lfs.MaxSummaryAttempts + 1 {
		res := repairLedgerSessionTitles(sessionsDir, "/fake/repo")
		assert.Equal(t, StatusClean, res.Status, "pending summary belongs to the summarization path")
		after, err := os.ReadFile(metaPath)
		require.NoError(t, err)
		assert.Equal(t, before, after, "autofix must not consume summary attempts")
	}
}

// TestRepairLedgerSessionTitles_RecoversFromSummaryJSON is the happy
// path proof that the daemon's autofix scheduler can fix the user's
// existing broken sessions on its own once a clean summary.json
// exists alongside the empty-title meta.
func TestRepairLedgerSessionTitles_RecoversFromSummaryJSON(t *testing.T) {
	ledger := t.TempDir()
	sessionsDir := filepath.Join(ledger, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o755))
	dir := seedSession(t, sessionsDir, "2026-05-01T10-00-test-OxRECO",
		&lfs.SessionMeta{Title: "", SummaryStatus: "failed_validation", SummaryAttempts: 1},
		"Recovered Title From summary.json")

	res := repairLedgerSessionTitles(sessionsDir, "/fake/repo")
	assert.Equal(t, StatusFixed, res.Status, "recovery must surface as Fixed")
	assert.Contains(t, res.Summary, "recovered=1")

	got, err := lfs.ReadSessionMeta(dir)
	require.NoError(t, err)
	assert.Equal(t, "Recovered Title From summary.json", got.Title)
	assert.Equal(t, "ok", got.SummaryStatus)
	assert.Equal(t, 0, got.SummaryAttempts)
}

// TestRepairLedgerSessionTitles_NeverCountsSummaryAttempts: a failed summary
// with nothing to recover is left for the summarizer, however many times the
// check runs. Failure prevented: the check used to add an attempt per pass and
// mark the session unrecoverable about an hour after one real failure, as an
// uncommitted edit on every clone (GH #1107).
func TestRepairLedgerSessionTitles_NeverCountsSummaryAttempts(t *testing.T) {
	for attempts := range lfs.MaxSummaryAttempts {
		t.Run(fmt.Sprintf("attempts=%d", attempts), func(t *testing.T) {
			sessionsDir := t.TempDir()
			dir := seedSession(t, sessionsDir, "2026-05-01T10-00-test-OxFAIL",
				&lfs.SessionMeta{SummaryStatus: "failed_validation", SummaryAttempts: attempts,
					ValidationError: "content validation failed: title too short"},
				"") // no summary.json title: nothing to recover
			before, err := os.ReadFile(filepath.Join(dir, "meta.json"))
			require.NoError(t, err)

			for range lfs.MaxSummaryAttempts + 1 {
				res := repairLedgerSessionTitles(sessionsDir, "/fake/repo")
				assert.Equal(t, StatusClean, res.Status)
			}

			after, err := os.ReadFile(filepath.Join(dir, "meta.json"))
			require.NoError(t, err)
			assert.Equal(t, string(before), string(after), "meta.json must stay byte-identical")
		})
	}
}

// TestRepairLedgerSessionTitles_MovesLeakedDiagnostic: the tidy-up that
// survives the attempt counter's removal still runs, without counting.
func TestRepairLedgerSessionTitles_MovesLeakedDiagnostic(t *testing.T) {
	sessionsDir := t.TempDir()
	dir := filepath.Join(sessionsDir, "2026-05-01T10-00-test-OxLEAK")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	const leaked = "Summary failed content validation: title too short"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"),
		[]byte(`{"version":"1.0","session_name":"2026-05-01T10-00-test-OxLEAK","summary":"`+leaked+`","summary_status":"failed_validation","summary_attempts":1}`), 0o644))

	res := repairLedgerSessionTitles(sessionsDir, "/fake/repo")
	assert.Equal(t, StatusFixed, res.Status)
	assert.Contains(t, res.Summary, "moved_diagnostic=1")

	got, err := lfs.ReadSessionMeta(dir)
	require.NoError(t, err)
	assert.Empty(t, got.Summary)
	assert.Equal(t, leaked, got.ValidationError)
	assert.Equal(t, "failed_validation", got.SummaryStatus)
	assert.Equal(t, 1, got.SummaryAttempts, "moving a diagnostic is not a summary attempt")
	assert.Equal(t, StatusClean, repairLedgerSessionTitles(sessionsDir, "/fake/repo").Status, "second pass must be a no-op")
}

// TestRepairLedgerSessionTitles_MissingDirIsClean covers the common
// case during clone or before any session has been finalized. We must
// NOT surface "read sessions dir: no such file" as an error every
// 30 minutes for every workspace whose ledger isn't ready yet.
func TestRepairLedgerSessionTitles_MissingDirIsClean(t *testing.T) {
	res := repairLedgerSessionTitles(filepath.Join(t.TempDir(), "no-such-dir"), "/fake/repo")
	assert.Equal(t, StatusClean, res.Status, "missing sessions dir is normal during clone — must not error")
}

// Successful repairs must not clear the warning for remaining corrupt sessions.
func TestRepairLedgerSessionTitles_PartialRepairKeepsWarning(t *testing.T) {
	sessionsDir := t.TempDir()
	seedSession(t, sessionsDir, "recoverable", &lfs.SessionMeta{}, "Recovered title")
	corruptDir := filepath.Join(sessionsDir, "corrupt")
	require.NoError(t, os.Mkdir(corruptDir, 0o700))
	corruptPath := filepath.Join(corruptDir, "meta.json")
	const corrupt = "{\n<<<<<<< HEAD\n"
	require.NoError(t, os.WriteFile(corruptPath, []byte(corrupt), 0o600))

	res := repairLedgerSessionTitles(sessionsDir, "/fake/repo")
	assert.Equal(t, StatusFound, res.Status, "one successful repair must not hide another session's error")
	assert.Contains(t, res.Summary, "recovered=1")
	assert.Contains(t, res.Summary, "errored=1")
	assert.Equal(t, StatusFound, repairLedgerSessionTitles(sessionsDir, "/fake/repo").Status)
	meta, err := lfs.ReadSessionMeta(filepath.Join(sessionsDir, "recoverable"))
	require.NoError(t, err)
	assert.Equal(t, "Recovered title", meta.Title)
	bytes, err := os.ReadFile(corruptPath)
	require.NoError(t, err)
	assert.Equal(t, corrupt, string(bytes))
}
