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

// TestLedgerWriters_QueueBehindRepoLock: while a sync holds the repo lock,
// every ox writer that moves a Ledger branch must wait rather than commit. A
// writer that skips the lock commits within milliseconds, so the watch window
// only bounds the check. Failure prevented: the commit moving main under a
// live sync rebase, which makes git refuse to finish it, and landing inside a
// recovery's re-anchor-to-abort window, where the abort rewinds past it.
func TestLedgerWriters_QueueBehindRepoLock(t *testing.T) {
	committedSession := func(t *testing.T, clonePath, name string) {
		writeSessionFiles(t, clonePath, name)
		runGit(t, clonePath, "add", "-A")
		runGit(t, clonePath, "commit", "--no-verify", "-m", "seed session")
		runGit(t, clonePath, "push")
	}
	writers := []struct {
		name    string
		prepare func(t *testing.T, clonePath, name string) func() error
	}{
		{"session upload", func(t *testing.T, clonePath, name string) func() error {
			writeSessionFiles(t, clonePath, name)
			return func() error { return commitAndPushLedger(clonePath, name) }
		}},
		{"session delete", func(t *testing.T, clonePath, name string) func() error {
			committedSession(t, clonePath, name)
			return func() error {
				return deleteSessionFromLedger(clonePath, name, filepath.Join(clonePath, "sessions", name))
			}
		}},
		{"session remove", func(t *testing.T, clonePath, name string) func() error {
			committedSession(t, clonePath, name)
			return func() error {
				_, err := batchDeleteSessionsFromLedger(clonePath, []string{name})
				return err
			}
		}},
		{"murmur delete", func(t *testing.T, clonePath, _ string) func() error {
			const murmur = "data/murmurs/m1.json"
			require.NoError(t, os.MkdirAll(filepath.Join(clonePath, filepath.Dir(murmur)), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(clonePath, murmur), []byte(`{"content":"wip"}`), 0o644))
			runGit(t, clonePath, "add", murmur)
			runGit(t, clonePath, "commit", "--no-verify", "-m", "seed murmur")
			return func() error { return commitMurmurDeletion(context.Background(), clonePath, murmur, "m1") }
		}},
		{"session-ID backfill", func(t *testing.T, clonePath, name string) func() error {
			committedSession(t, clonePath, name)
			meta := filepath.Join(clonePath, "sessions", name, "meta.json")
			require.NoError(t, os.WriteFile(meta, []byte(`{"session_name":"`+name+`","session_id":"ses_test"}`), 0o644))
			return func() error { return commitAndPushSessionIDBackfill(clonePath, []string{name}) }
		}},
	}
	for _, w := range writers {
		t.Run(w.name, func(t *testing.T) {
			_, clonePath := createBareAndClone(t)
			isolatePushEnv(t, clonePath)
			write := w.prepare(t, clonePath, "2026-01-01T00-00-testuser-OxLck1")
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
			writeDone := make(chan error, 1)
			go func() { writeDone <- write() }()

			assert.Never(t, func() bool {
				head, err := runGitCombined(clonePath, "rev-parse", "HEAD")
				return err == nil && head != headBefore
			}, 500*time.Millisecond, 25*time.Millisecond, "the writer must not commit while a sync holds the repo lock")
			close(release)
			require.NoError(t, <-syncDone)
			require.NoError(t, <-writeDone)
			assert.NotEqual(t, headBefore, runGit(t, clonePath, "rev-parse", "HEAD"), "the queued write must still land once the lock is free")
		})
	}
}

// TestPushSummary_LeavesUnrelatedStagedEntriesStaged: the summary commit goes
// through the validated Ledger snapshot, which never publishes data/plans.
// Failure prevented: a bare `git commit` sweeping a coworker's staged plan
// edit, unvalidated, into the session's summary commit (#1442).
func TestPushSummary_LeavesUnrelatedStagedEntriesStaged(t *testing.T) {
	_, clonePath := createBareAndClone(t)
	isolatePushEnv(t, clonePath)
	sessionName := "2026-01-01T00-00-testuser-OxSnp1"
	sessionDir := writeSessionFiles(t, clonePath, sessionName)
	const plan = "data/plans/pln_test/plan.md"
	require.NoError(t, os.MkdirAll(filepath.Join(clonePath, filepath.Dir(plan)), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(clonePath, plan), []byte("# draft plan\n"), 0o644))
	runGit(t, clonePath, "add", plan)
	summary := filepath.Join(t.TempDir(), "raw-summary.json")
	require.NoError(t, os.WriteFile(summary, []byte(`{"title":"Fix ledger sync","summary":"Recovered the ledger sync after a lost rebase race.","outcome":"success"}`), 0o644))

	out := pushSummaryToLedger(summary, sessionDir)

	require.True(t, out.Success, out.Error)
	_, err := runGitCombined(clonePath, "cat-file", "-e", "HEAD:sessions/"+sessionName+"/summary.json")
	assert.NoError(t, err, "the summary must be committed")
	_, err = runGitCombined(clonePath, "cat-file", "-e", "HEAD:"+plan)
	assert.Error(t, err, "the staged plan must not ride along in the summary commit")
	assert.Contains(t, runGit(t, clonePath, "diff", "--cached", "--name-only"), plan, "the plan edit must stay staged")
}

// pushSummaryFixture returns a summary file push-summary accepts.
func pushSummaryFixture(t *testing.T) string {
	t.Helper()
	summary := filepath.Join(t.TempDir(), "raw-summary.json")
	require.NoError(t, os.WriteFile(summary, []byte(`{"title":"Fix ledger sync","summary":"Recovered the ledger sync after a lost rebase race.","outcome":"success"}`), 0o644))
	return summary
}

// TestPushSummary_RetryAfterCommitSucceeds: a hook that re-runs push-summary
// for a summary already in the Ledger must get success, not a commit error.
// Failure prevented: the snapshot's "nothing to commit" (committed=false)
// reading as a failure, so a retried session stop reports a broken upload.
func TestPushSummary_RetryAfterCommitSucceeds(t *testing.T) {
	_, clonePath := createBareAndClone(t)
	isolatePushEnv(t, clonePath)
	sessionDir := writeSessionFiles(t, clonePath, "2026-01-01T00-00-testuser-OxRty1")
	summary := pushSummaryFixture(t)
	first := pushSummaryToLedger(summary, sessionDir)
	require.True(t, first.Success, first.Error)
	head := runGit(t, clonePath, "rev-parse", "HEAD")

	retry := pushSummaryToLedger(summary, sessionDir)

	require.True(t, retry.Success, retry.Error)
	assert.Equal(t, "summary pushed to ledger", retry.Message)
	assert.Equal(t, head, runGit(t, clonePath, "rev-parse", "HEAD"), "a retry must not add a commit")
}

// TestPushSummary_UnmergedIndexPointsAtDoctor: an index left unmerged outside a
// rebase (an autostash or merge conflict) blocks the snapshot commit. Failure
// prevented: the coworker getting a raw snapshot error with no way forward
// instead of the `ox doctor --fix` guidance (bd ox-v30g).
func TestPushSummary_UnmergedIndexPointsAtDoctor(t *testing.T) {
	_, clonePath := createBareAndClone(t)
	isolatePushEnv(t, clonePath)
	sessionDir := writeSessionFiles(t, clonePath, "2026-01-01T00-00-testuser-OxUnm1")
	conflicted := filepath.Join(clonePath, "notes.txt")
	require.NoError(t, os.WriteFile(conflicted, []byte("base\n"), 0o644))
	runGit(t, clonePath, "add", "notes.txt")
	runGit(t, clonePath, "commit", "--no-verify", "-m", "base")
	branch := runGit(t, clonePath, "branch", "--show-current")
	runGit(t, clonePath, "checkout", "-q", "-b", "other")
	require.NoError(t, os.WriteFile(conflicted, []byte("other\n"), 0o644))
	runGit(t, clonePath, "commit", "--no-verify", "-qam", "other")
	runGit(t, clonePath, "checkout", "-q", branch)
	require.NoError(t, os.WriteFile(conflicted, []byte("ours\n"), 0o644))
	runGit(t, clonePath, "commit", "--no-verify", "-qam", "ours")
	_, err := runGitCombined(clonePath, "merge", "other")
	require.Error(t, err, "the merge must conflict")
	require.False(t, gitutil.IsRebaseInProgress(clonePath))

	out := pushSummaryToLedger(pushSummaryFixture(t), sessionDir)

	require.False(t, out.Success)
	assert.Contains(t, out.Error, "ox doctor --fix")
}
