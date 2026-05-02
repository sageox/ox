package autofix

import (
	"encoding/json"
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

// TestRepairLedgerSessionTitles_FlipsToTerminalAtCap drives a session
// from one-shy-of-cap to the unrecoverable terminal state and proves
// the autofix loop will short-circuit on the next pass. This is the
// guard against unbounded LLM retry spend on permanently-broken
// sessions.
func TestRepairLedgerSessionTitles_FlipsToTerminalAtCap(t *testing.T) {
	ledger := t.TempDir()
	sessionsDir := filepath.Join(ledger, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o755))
	dir := seedSession(t, sessionsDir, "2026-05-01T10-00-test-OxCAPP",
		&lfs.SessionMeta{Title: "", SummaryStatus: "failed_validation", SummaryAttempts: lfs.MaxSummaryAttempts - 1},
		"") // empty summary.json title — no recovery available

	res := repairLedgerSessionTitles(sessionsDir, "/fake/repo")
	assert.Equal(t, StatusFixed, res.Status, "flipping a session to terminal counts as a fix (loop closes)")
	assert.Contains(t, res.Summary, "flipped_terminal=1")

	got, err := lfs.ReadSessionMeta(dir)
	require.NoError(t, err)
	assert.Equal(t, "unrecoverable", got.SummaryStatus)

	// Next pass — terminal session must be skipped, not re-flagged.
	res2 := repairLedgerSessionTitles(sessionsDir, "/fake/repo")
	assert.Equal(t, StatusClean, res2.Status, "terminal sessions must short-circuit subsequent autofix passes")
}

// TestRepairLedgerSessionTitles_MissingDirIsClean covers the common
// case during clone or before any session has been finalized. We must
// NOT surface "read sessions dir: no such file" as an error every
// 30 minutes for every workspace whose ledger isn't ready yet.
func TestRepairLedgerSessionTitles_MissingDirIsClean(t *testing.T) {
	res := repairLedgerSessionTitles(filepath.Join(t.TempDir(), "no-such-dir"), "/fake/repo")
	assert.Equal(t, StatusClean, res.Status, "missing sessions dir is normal during clone — must not error")
}

// TestRepairLedgerSessionTitles_BumpsOnlyReportFound proves the
// observability split: a pass that only bumps attempt counters
// (no recoveries, no terminal flips) reports as StatusFound, not
// StatusFixed. We didn't actually fix anything yet, but a human/ops
// dashboard should still see the activity.
func TestRepairLedgerSessionTitles_BumpsOnlyReportFound(t *testing.T) {
	ledger := t.TempDir()
	sessionsDir := filepath.Join(ledger, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o755))
	seedSession(t, sessionsDir, "2026-05-01T10-00-test-OxBUMP",
		&lfs.SessionMeta{Title: "", SummaryStatus: "failed_validation", SummaryAttempts: 0},
		"")

	res := repairLedgerSessionTitles(sessionsDir, "/fake/repo")
	assert.Equal(t, StatusFound, res.Status, "pure-bump pass must surface as Found, not Fixed")
	assert.Contains(t, res.Summary, "bumped=1")
}
