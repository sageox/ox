package autofix

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const rearmTestUser = "Person A"

// seedFailedValidationSession writes a session stuck in the pre-0.7.2
// "title too short" failure, with a readable raw.jsonl so it is eligible.
func seedFailedValidationSession(t *testing.T, ledger, name, username string) {
	t.Helper()
	dir := seedSession(t, filepath.Join(ledger, "sessions"), name, &lfs.SessionMeta{
		Username:        username,
		SummaryStatus:   "failed_validation",
		SummaryAttempts: 3,
		ValidationError: "title too short",
	}, "")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte("{\"type\":\"user\"}\n"), 0o644))
}

func newRearmLedger(t *testing.T) string {
	t.Helper()
	ledger := t.TempDir()
	afGit(t, ledger, "init", "--initial-branch=main")
	// real ledgers ignore the machine-local marker via sessions/.gitignore
	require.NoError(t, os.MkdirAll(filepath.Join(ledger, "sessions"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(ledger, "sessions", ".gitignore"), []byte(".needs-summary\n"), 0o644))
	seedFailedValidationSession(t, ledger, "2026-10-01T10-00-persona-OxOWN1", rearmTestUser)
	seedFailedValidationSession(t, ledger, "2026-10-01T11-00-personb-OxTEAM", "Person B")
	afGit(t, ledger, "add", "sessions")
	afGit(t, ledger, "commit", "-m", "seed sessions")
	return ledger
}

func commitCount(t *testing.T, ledger string) int {
	t.Helper()
	n, err := strconv.Atoi(strings.TrimSpace(afGit(t, ledger, "rev-list", "--count", "HEAD")))
	require.NoError(t, err)
	return n
}

func TestRearmInlineSummarySessions_CommitsOwnSessionsOnly(t *testing.T) {
	ledger := newRearmLedger(t)
	headBefore := afGit(t, ledger, "rev-parse", "HEAD")
	countBefore := commitCount(t, ledger)

	res := rearmInlineSummarySessions(context.Background(), ledger, rearmTestUser, nil)

	assert.Equal(t, StatusFixed, res.Status, res.Summary)
	assert.Empty(t, afGit(t, ledger, "status", "--porcelain"), "tree must be clean after the re-arm")
	assert.NotEqual(t, headBefore, afGit(t, ledger, "rev-parse", "HEAD"), "HEAD must advance")
	assert.Equal(t, countBefore+1, commitCount(t, ledger), "exactly one commit")
	assert.Contains(t, afGit(t, ledger, "log", "-1", "--format=%s"), "re-arm 1 sessions")

	own, err := lfs.ReadSessionMeta(filepath.Join(ledger, "sessions", "2026-10-01T10-00-persona-OxOWN1"))
	require.NoError(t, err)
	assert.Empty(t, own.SummaryStatus)
	assert.Zero(t, own.SummaryAttempts)
	assert.Empty(t, own.ValidationError)

	team, err := lfs.ReadSessionMeta(filepath.Join(ledger, "sessions", "2026-10-01T11-00-personb-OxTEAM"))
	require.NoError(t, err)
	assert.Equal(t, "failed_validation", team.SummaryStatus, "teammate session must be untouched")
	assert.Equal(t, 3, team.SummaryAttempts)
	assert.Equal(t, "title too short", team.ValidationError)
}

func TestRearmInlineSummarySessions_NoIdentityTouchesNothing(t *testing.T) {
	ledger := newRearmLedger(t)
	res := rearmInlineSummarySessions(context.Background(), ledger, "", nil)
	assert.Equal(t, StatusClean, res.Status)
	assert.Empty(t, afGit(t, ledger, "status", "--porcelain"))
}
