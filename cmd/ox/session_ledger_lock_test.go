package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Session-stop Ledger writers vs. the daemon's sync rebase (sageox/ox#1541) ---
//
// The session upload and push-summary commits used to run without the repo
// lock, so they could commit to main while a daemon's `pull --rebase` replayed
// — the race that wedged the Ledger — and could stage into a rebase that was
// already wedged.

// wedgeLedgerRebase leaves ledgerPath halted mid-rebase with HEAD detached,
// the state a sync rebase that lost its race leaves behind.
func wedgeLedgerRebase(t *testing.T, ledgerPath string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(ledgerPath, "local.txt"), []byte("local"), 0o644))
	runGit(t, ledgerPath, "add", "local.txt")
	runGit(t, ledgerPath, "commit", "--no-verify", "-m", "local")
	_, err := runGitCombined(ledgerPath, "rebase", "-x", "false", "HEAD~1")
	require.Error(t, err, "the failing exec must halt the rebase")
	require.True(t, gitutil.IsRebaseInProgress(ledgerPath))
}

// TestSessionLedgerWriters_RefuseToStageIntoWedgedRebase: under the repo lock no
// sync rebase is live, so one on disk is a wedge. Failure prevented: a session
// stop staging its files into the wedged rebase (marking its conflicts
// resolved) or committing onto the rebase's detached HEAD, where the next
// recovery discards it.
func TestSessionLedgerWriters_RefuseToStageIntoWedgedRebase(t *testing.T) {
	writers := []struct {
		name  string
		write func(t *testing.T, ledgerPath, sessionName, sessionDir string) error
	}{
		{"session upload", func(t *testing.T, ledgerPath, sessionName, _ string) error {
			return commitAndPushLedger(ledgerPath, sessionName)
		}},
		{"push-summary", func(t *testing.T, _, _, sessionDir string) error {
			summary := filepath.Join(t.TempDir(), "raw-summary.json")
			require.NoError(t, os.WriteFile(summary, []byte(`{"title":"Fix ledger sync","summary":"Recovered the ledger sync after a lost rebase race.","outcome":"success"}`), 0o644))
			out := pushSummaryToLedger(summary, sessionDir)
			if out.Success {
				return nil
			}
			return &pushSummaryError{out.Error}
		}},
	}
	for _, w := range writers {
		t.Run(w.name, func(t *testing.T) {
			_, clonePath := createBareAndClone(t)
			isolatePushEnv(t, clonePath)
			sessionName := "2026-01-01T00-00-testuser-OxWdg1"
			sessionDir := writeSessionFiles(t, clonePath, sessionName)
			wedgeLedgerRebase(t, clonePath)
			headBefore := runGit(t, clonePath, "rev-parse", "HEAD")

			err := w.write(t, clonePath, sessionName, sessionDir)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "broken rebase state")
			assert.Empty(t, runGit(t, clonePath, "diff", "--cached", "--name-only"), "nothing may be staged into the wedged rebase")
			assert.Equal(t, headBefore, runGit(t, clonePath, "rev-parse", "HEAD"), "nothing may be committed onto the rebase's HEAD")
		})
	}
}

// runGitCombined runs git for a step that may fail on purpose.
func runGitCombined(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

type pushSummaryError struct{ msg string }

func (e *pushSummaryError) Error() string { return e.msg }

// TestCommitAndPushLedger_QueuesBehindRepoLock: while a sync holds the repo
// lock, a session upload must wait rather than commit. A writer that skips the
// lock commits within milliseconds, so the watch window only bounds the check.
// Failure prevented: the upload moving main under a live sync rebase, which
// makes git refuse to finish it.
func TestCommitAndPushLedger_QueuesBehindRepoLock(t *testing.T) {
	barePath, clonePath := createBareAndClone(t)
	isolatePushEnv(t, clonePath)
	sessionName := "2026-01-01T00-00-testuser-OxLck1"
	writeSessionFiles(t, clonePath, sessionName)
	headBefore := runGit(t, clonePath, "rev-parse", "HEAD")

	held, release := make(chan struct{}), make(chan struct{})
	syncDone := make(chan error, 1)
	go func() {
		syncDone <- gitutil.WithRepoLock(context.Background(), clonePath, func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	uploadDone := make(chan error, 1)
	go func() { uploadDone <- commitAndPushLedger(clonePath, sessionName) }()

	assert.Never(t, func() bool {
		head, err := runGitCombined(clonePath, "rev-parse", "HEAD")
		return err == nil && head != headBefore
	}, 500*time.Millisecond, 25*time.Millisecond, "the upload must not commit while a sync holds the repo lock")
	close(release)
	require.NoError(t, <-syncDone)
	require.NoError(t, <-uploadDone)

	_, err := os.Stat(filepath.Join(cloneBare(t, barePath), "sessions", sessionName, "meta.json"))
	assert.NoError(t, err, "the queued upload must still publish once the lock is free")
}
