package autofix

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitutil"
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

const rearmOwnSession = "2026-10-01T10-00-persona-OxOWN1"

func readRearmMeta(t *testing.T, ledger, name string) *lfs.SessionMeta {
	t.Helper()
	meta, err := lfs.ReadSessionMeta(filepath.Join(ledger, "sessions", name))
	require.NoError(t, err)
	return meta
}

// Failure prevented: restoreRearmedMetas runs `git checkout --` on a failed
// commit, which would destroy edits that existed before the re-arm.
func TestRearmInlineSummarySessions_SkipsMetaThatDiffersFromHead(t *testing.T) {
	ledger := newRearmLedger(t)
	metaFile := filepath.Join(ledger, "sessions", rearmOwnSession, "meta.json")
	data, err := os.ReadFile(metaFile)
	require.NoError(t, err)
	dirty := append(bytes.TrimRight(data, "\n"), []byte("\n\n")...)
	require.NoError(t, os.WriteFile(metaFile, dirty, 0o644))
	headBefore := afGit(t, ledger, "rev-parse", "HEAD")

	res := rearmInlineSummarySessions(context.Background(), ledger, rearmTestUser, nil)

	assert.Equal(t, StatusClean, res.Status, res.Summary)
	after, err := os.ReadFile(metaFile)
	require.NoError(t, err)
	assert.Equal(t, dirty, after, "pre-existing edit must be untouched")
	assert.Equal(t, headBefore, afGit(t, ledger, "rev-parse", "HEAD"))
	assert.Equal(t, "M sessions/"+rearmOwnSession+"/meta.json", afGit(t, ledger, "status", "--porcelain"))
}

// Failure prevented: a failed snapshot commit leaving rewritten meta.json files
// dirty in the tree, which breaks every later `git pull --rebase`.
func TestRearmInlineSummarySessions_FailedCommitRestoresMeta(t *testing.T) {
	ledger := newRearmLedger(t)
	// staging still works, but CommitLedgerSnapshot refuses via IsSafeForGitOps
	require.NoError(t, os.MkdirAll(filepath.Join(ledger, ".git", "rebase-merge"), 0o755))
	headBefore := afGit(t, ledger, "rev-parse", "HEAD")

	res := rearmInlineSummarySessions(context.Background(), ledger, rearmTestUser, nil)

	assert.Equal(t, StatusError, res.Status, res.Summary)
	own := readRearmMeta(t, ledger, rearmOwnSession)
	assert.Equal(t, "failed_validation", own.SummaryStatus)
	assert.Equal(t, 3, own.SummaryAttempts)
	assert.Equal(t, "title too short", own.ValidationError)
	assert.Empty(t, afGit(t, ledger, "status", "--porcelain"))
	assert.Equal(t, headBefore, afGit(t, ledger, "rev-parse", "HEAD"))
}

// wedgeLedgerPush trips the process-wide push breaker for the ledger the only
// way outside gitutil can: a real push rejected by a pre-receive hook that
// emits GitLab's LFS-missing message (mirrors gitutil's rejectingRemote).
func wedgeLedgerPush(t *testing.T, ledger string) {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "remote.git")
	afGit(t, ledger, "init", "--bare", "--quiet", bare)
	afGit(t, ledger, "remote", "add", "origin", bare)
	afGit(t, ledger, "push", "--quiet", "-u", "origin", "main")

	hook := "#!/bin/sh\necho 'remote: GitLab: LFS objects are missing. Ensure LFS is properly set up or try a manual \"git lfs push --all\".' >&2\nexit 1\n"
	require.NoError(t, os.MkdirAll(filepath.Join(bare, "hooks"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bare, "hooks", "pre-receive"), []byte(hook), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(ledger, "wedge.txt"), []byte("x"), 0o644))
	afGit(t, ledger, "add", "wedge.txt")
	afGit(t, ledger, "commit", "-m", "unpushable")

	err := gitutil.PushWithRetry(context.Background(), ledger, gitutil.PushOpts{
		MaxRetries:        1,
		OpTimeout:         20 * time.Second,
		Logger:            slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		SuspendWhenWedged: true,
		ReconcileLFS:      func(string) (bool, error) { return false, nil },
	})
	require.ErrorIs(t, err, gitutil.ErrPushWedged)
	_, wedged := gitutil.PushWedgedUntil(ledger)
	require.True(t, wedged)
}

// Failure prevented: re-arming while finalize is paused leaves sessions that
// can be neither summarized nor pushed, plus a commit nothing can ship.
func TestRearmInlineSummarySessions_DefersWhilePushSuspended(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pre-receive hook fixture needs a POSIX shell")
	}
	ledger := newRearmLedger(t)
	afGit(t, ledger, "config", "user.email", "test@test.local")
	afGit(t, ledger, "config", "user.name", "Test")
	wedgeLedgerPush(t, ledger)
	headBefore := afGit(t, ledger, "rev-parse", "HEAD")

	res := rearmInlineSummarySessions(context.Background(), ledger, rearmTestUser, nil)

	assert.Equal(t, StatusClean, res.Status, res.Summary)
	assert.Contains(t, res.Summary, "deferred")
	own := readRearmMeta(t, ledger, rearmOwnSession)
	assert.Equal(t, "failed_validation", own.SummaryStatus)
	assert.Equal(t, 3, own.SummaryAttempts)
	assert.Empty(t, afGit(t, ledger, "status", "--porcelain"))
	assert.Equal(t, headBefore, afGit(t, ledger, "rev-parse", "HEAD"))
}
