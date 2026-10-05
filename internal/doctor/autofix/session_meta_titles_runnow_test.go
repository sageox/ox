package autofix

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunNow_LeavesFailedSummaryForSummarizer drives the path `ox doctor` and
// the daemon's autofix tick take: the real Default() registry through
// Scheduler.RunNow, against a workspace whose ledger holds a session with one
// failed summary attempt and nothing to recover.
//
// Failure prevented (GH #1107): session-meta-titles added one summary attempt
// per run and marked the session unrecoverable at the third, with no LLM
// involved. That spent the retry budget the recording machine needs and left
// an uncommitted meta.json edit on every clone.
//
// Red-first (verified while authoring): restore the attempt bump in
// lfs.RecoverEmptyTitleMeta → this fails on "meta.json must stay
// byte-identical", with summary_attempts rising and the status flipping to
// unrecoverable.
func TestRunNow_LeavesFailedSummaryForSummarizer(t *testing.T) {
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".sageox"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".sageox", "config.json"),
		[]byte(`{"config_version":"2","repo_id":"repo_01950000-0000-7000-8000-000000001107","endpoint":"https://sageox.ai"}`), 0o644))
	pctx, err := config.LoadProjectContext(root)
	require.NoError(t, err)
	ledgerPath := pctx.DefaultLedgerPath()
	require.NotEmpty(t, ledgerPath, "precondition: the workspace must resolve a ledger")

	sessionsDir := filepath.Join(ledgerPath, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o755))
	dir := seedSession(t, sessionsDir, "2026-09-24T23-03-riley-OxRiLy",
		&lfs.SessionMeta{SummaryStatus: "failed_validation", SummaryAttempts: 1,
			ValidationError: "richness validation failed: key_actions empty"},
		"")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "summary.json"), []byte(`{"title":""}`), 0o644))
	metaPath := filepath.Join(dir, "meta.json")
	before, err := os.ReadFile(metaPath)
	require.NoError(t, err)

	s := NewScheduler(Default(), nil, func() []string { return []string{root} }, nil)
	s.SetLedgerPaths(func() []string { return []string{ledgerPath} })
	for range lfs.MaxSummaryAttempts + 1 {
		var ran bool
		for _, r := range s.RunNow(context.Background()) {
			if r.Slug == "session-meta-titles" {
				ran = true
				assert.Equal(t, StatusClean, r.Status, r.Summary)
			}
		}
		require.True(t, ran, "precondition: session-meta-titles must run")
	}

	after, err := os.ReadFile(metaPath)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "meta.json must stay byte-identical")
}
