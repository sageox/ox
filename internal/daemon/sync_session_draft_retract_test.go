package daemon

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The daemon is the only automatic reclaimer of a committed draft placeholder
// whose recording died: a crashed agent never reaches the clean-stop/abort paths
// that would retract it, and without this sweep the ledger accumulates orphaned
// "in progress" /c/ pages that also show as phantom rows in `ox session list`.
//
// The asymmetry that shapes every case: a false negative leaves a stale page; a
// false positive git-removes a LIVE session's placeholder. So each guard — age,
// local recording — gets a negative control.

func newRetractScheduler(t *testing.T) (*SyncScheduler, string) {
	t.Helper()
	ledger := t.TempDir()
	gitInit(t, ledger)

	cfg := DefaultConfig()
	cfg.ProjectRoot = ledger
	cfg.LedgerPath = ledger
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewSyncScheduler(cfg, logger, WithGitRunner(gitutil.DefaultRunner())), ledger
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	mustGit(t, dir, "-c", "init.defaultBranch=main", "init", "--quiet")
	mustGit(t, dir, "config", "user.email", "test@example.com")
	mustGit(t, dir, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("x\n"), 0o644))
	mustGit(t, dir, "add", "README.md")
	mustGit(t, dir, "commit", "--quiet", "-m", "init")

	// A bare upstream so draftPublishedOnRemote can resolve @{upstream}: the
	// reaper only retracts drafts already present on the remote.
	remote := t.TempDir()
	mustGit(t, remote, "-c", "init.defaultBranch=main", "init", "--quiet", "--bare")
	mustGit(t, dir, "remote", "add", "origin", remote)
	mustGit(t, dir, "push", "-u", "--quiet", "origin", "main")
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// writeDraftCommit writes and commits a draft placeholder whose updated_at
// heartbeat is `age` in the past. When push is true it is also pushed to the
// bare upstream, mirroring a real published draft.
func writeDraftCommit(t *testing.T, ledger, name string, age time.Duration, push bool) {
	t.Helper()
	dir := filepath.Join(ledger, "sessions", name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	updated := time.Now().Add(-age).UTC()
	require.NoError(t, lfs.WriteSessionMetaOnly(dir, &lfs.SessionMeta{
		Version: "1.0", SessionName: name, SessionID: "ses_retract_test",
		AgentID: "OxOrphan", CreatedAt: updated, Draft: true, TurnCount: 2,
		UpdatedAt: &updated, Files: map[string]lfs.FileRef{},
	}))
	mustGit(t, ledger, "add", "-A", "sessions/")
	mustGit(t, ledger, "commit", "--quiet", "-m", "session-draft: "+name)
	if push {
		mustGit(t, ledger, "push", "--quiet", "origin", "main")
	}
}

// commitStaleDraft writes, commits, and PUSHES a stale draft — the normal
// published-then-abandoned case the reaper cleans.
func commitStaleDraft(t *testing.T, ledger, name string, age time.Duration) {
	t.Helper()
	writeDraftCommit(t, ledger, name, age, true)
}

// TestRetractOrphanedDrafts_RemovesStaleCommittedDraft is the core behavior: a
// stale committed draft with no recording is git-removed, and the removal lands
// as a session-draft: commit so pushSessionDraftCommits carries it to the remote.
func TestRetractOrphanedDrafts_RemovesStaleCommittedDraft(t *testing.T) {
	s, ledger := newRetractScheduler(t)
	const name = "2026-01-01T00-00-testuser-OxDead1"
	commitStaleDraft(t, ledger, name, 120*time.Hour)

	s.retractOrphanedDrafts(context.Background(), ledger)

	assert.NoDirExists(t, filepath.Join(ledger, "sessions", name),
		"a stale committed draft with no recording must be git-removed")
	subj := mustGit(t, ledger, "log", "-1", "--format=%s")
	assert.Equal(t, "session-draft: retract "+name, subj,
		"retraction must land as a session-draft: commit so the daemon push carries it")
}

// TestRetractOrphanedDrafts_SkipsUnpushedDraft — a stale draft whose publish
// commit is still LOCAL must never be retracted. Stacking a retraction on an
// unpushed publish would make pushSessionDraftCommits ship both, manufacturing a
// publish the daemon should never send to the remote.
func TestRetractOrphanedDrafts_SkipsUnpushedDraft(t *testing.T) {
	s, ledger := newRetractScheduler(t)
	const name = "2026-01-01T00-00-testuser-OxUnpub"
	writeDraftCommit(t, ledger, name, 120*time.Hour, false) // committed but NOT pushed

	s.retractOrphanedDrafts(context.Background(), ledger)

	assert.DirExists(t, filepath.Join(ledger, "sessions", name),
		"an unpushed draft belongs to the publish flow — the reaper must not touch it")
	subj := mustGit(t, ledger, "log", "-1", "--format=%s")
	assert.NotEqual(t, "session-draft: retract "+name, subj,
		"no retraction commit may be created for an unpublished draft")
}

// TestRetractOrphanedDrafts_KeepsFreshDraft — a draft whose heartbeat is recent
// is a live session (possibly on another machine, via the shared updated_at).
func TestRetractOrphanedDrafts_KeepsFreshDraft(t *testing.T) {
	s, ledger := newRetractScheduler(t)
	const name = "2026-01-01T00-00-testuser-OxLive1"
	commitStaleDraft(t, ledger, name, 1*time.Minute)

	s.retractOrphanedDrafts(context.Background(), ledger)

	assert.DirExists(t, filepath.Join(ledger, "sessions", name),
		"a fresh draft is a live session and must never be retracted")
}

// A cross-machine heartbeat can land after the reaper's optimistic scan but
// before it acquires the repo lock. The locked revalidation must observe that
// refresh instead of deleting a session that is live on another machine.
func TestRetractOrphanedDrafts_RevalidatesHeartbeatAfterLockWait(t *testing.T) {
	s, ledger := newRetractScheduler(t)
	const name = "2026-01-01T00-00-testuser-OxRefresh"
	commitStaleDraft(t, ledger, name, 120*time.Hour)

	// entered proves the reaper actually reached the locked closure and ran
	// its revalidation, rather than merely returning early because
	// WithRepoLock timed out on the contended lock (both produce the same
	// externally-observable "the session dir still exists" outcome).
	entered := make(chan struct{})
	s.retractLockedTestHook = func() { close(entered) }

	locked := make(chan struct{})
	release := make(chan struct{})
	lockDone := make(chan error, 1)
	go func() {
		lockDone <- gitutil.WithRepoLock(context.Background(), ledger, func() error {
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked

	retractDone := make(chan struct{})
	go func() {
		s.retractOrphanedDrafts(context.Background(), ledger)
		close(retractDone)
	}()
	require.Eventually(t, func() bool {
		if s.ledgerMu.TryLock() {
			s.ledgerMu.Unlock()
			return false
		}
		return true
	}, time.Second, time.Millisecond, "reaper must finish its optimistic scan and wait on the repo lock")

	dir := filepath.Join(ledger, "sessions", name)
	meta, err := lfs.ReadSessionMeta(dir)
	require.NoError(t, err)
	refreshedAt := time.Now().UTC()
	meta.UpdatedAt = &refreshedAt
	require.NoError(t, lfs.WriteSessionMetaOnly(dir, meta))
	mustGit(t, ledger, "add", "--sparse", "sessions/"+name+"/meta.json")
	mustGit(t, ledger, "commit", "--quiet", "-m", "session-draft: refresh "+name)
	mustGit(t, ledger, "push", "--quiet", "origin", "main")

	close(release)
	require.NoError(t, <-lockDone)
	<-retractDone

	select {
	case <-entered:
	default:
		require.Fail(t, "reaper never entered the locked closure — it timed out on the repo lock instead")
	}

	assert.DirExists(t, dir, "a heartbeat refreshed while waiting for the lock must survive")
	assert.Equal(t, "session-draft: refresh "+name, mustGit(t, ledger, "log", "-1", "--format=%s"))
}

// TestRetractOrphanedDrafts_KeepsDraftWithLocalRecording — a cached transcript is
// recoverable work owned by upload-retry, not the reaper, even when stale.
func TestRetractOrphanedDrafts_KeepsDraftWithLocalRecording(t *testing.T) {
	s, ledger := newRetractScheduler(t)
	const name = "2026-01-01T00-00-testuser-OxRec1"
	commitStaleDraft(t, ledger, name, 120*time.Hour)
	cache := filepath.Join(ledger, ".sageox", "cache", "sessions", name)
	require.NoError(t, os.MkdirAll(cache, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cache, "raw.jsonl"),
		[]byte(`{"type":"header"}`+"\n"), 0o644))

	s.retractOrphanedDrafts(context.Background(), ledger)

	assert.DirExists(t, filepath.Join(ledger, "sessions", name),
		"a stale draft with a recoverable local recording must not be retracted")
}

// Stale draft metadata must never turn existing session content or local edits
// into permission to delete a session.
func TestRetractOrphanedDrafts_PreservesSessionData(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real Git repositories and multiple retraction attempts")
	}
	for _, state := range []string{"committed content", "untracked content", "content after scan", "unstaged metadata", "staged metadata", "locally deleted content"} {
		t.Run(state, func(t *testing.T) {
			s, ledger := newRetractScheduler(t)
			const name = "2026-01-01T00-00-testuser-OxData"
			commitStaleDraft(t, ledger, name, 120*time.Hour)
			dir := filepath.Join(ledger, "sessions", name)
			metaPath := filepath.Join(dir, "meta.json")
			contentPath := filepath.Join(dir, "summary.json")
			writeContent := func() {
				require.NoError(t, os.WriteFile(contentPath, []byte(`{"summary":"synthetic retained work"}`), 0o644))
			}
			switch state {
			case "committed content", "locally deleted content":
				writeContent()
				mustGit(t, ledger, "add", "--sparse", "sessions/"+name)
				mustGit(t, ledger, "commit", "--quiet", "-m", "session artifacts")
				mustGit(t, ledger, "push", "--quiet", "origin", "main")
				if state == "locally deleted content" {
					require.NoError(t, os.Remove(contentPath))
				}
			case "untracked content":
				writeContent()
			case "content after scan":
				s.retractLockedTestHook = writeContent
			case "unstaged metadata", "staged metadata":
				meta, err := lfs.ReadSessionMeta(dir)
				require.NoError(t, err)
				meta.TurnCount++
				require.NoError(t, lfs.WriteSessionMetaOnly(dir, meta))
				if state == "staged metadata" {
					mustGit(t, ledger, "add", "--sparse", "sessions/"+name+"/meta.json")
				}
			}
			beforeHead := mustGit(t, ledger, "rev-parse", "HEAD")
			beforeIndex := mustGit(t, ledger, "write-tree")
			beforeMeta, err := os.ReadFile(metaPath)
			require.NoError(t, err)

			s.retractOrphanedDrafts(context.Background(), ledger)

			assert.Equal(t, beforeHead, mustGit(t, ledger, "rev-parse", "HEAD"))
			assert.Equal(t, beforeIndex, mustGit(t, ledger, "write-tree"))
			afterMeta, err := os.ReadFile(metaPath)
			require.NoError(t, err)
			assert.Equal(t, beforeMeta, afterMeta)
			if state == "committed content" || state == "untracked content" || state == "content after scan" {
				content, err := os.ReadFile(contentPath)
				require.NoError(t, err)
				assert.Equal(t, `{"summary":"synthetic retained work"}`, string(content))
			}
		})
	}
}

type cancelRetractionRunner struct {
	gitutil.GitRunner
	cancel context.CancelFunc
	after  string
}

func (r *cancelRetractionRunner) RunGit(ctx context.Context, repoPath string, args ...string) (string, error) {
	out, err := r.GitRunner.RunGit(ctx, repoPath, args...)
	if err == nil && args[0] == r.after {
		r.cancel()
	}
	return out, err
}

// Cancellation after removing the placeholder must restore both the worktree
// and index, even when it prevents the subsequent commit from starting.
func TestRetractOrphanedDrafts_CancellationRestoresAndRetries(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real Git repositories and recovery retries")
	}
	for _, after := range []string{"rm", "diff"} {
		t.Run("cancel after "+after, func(t *testing.T) {
			s, ledger := newRetractScheduler(t)
			const name = "2026-01-01T00-00-testuser-OxRetry"
			commitStaleDraft(t, ledger, name, 120*time.Hour)
			metaPath := filepath.Join(ledger, "sessions", name, "meta.json")
			beforeMeta, err := os.ReadFile(metaPath)
			require.NoError(t, err)
			beforeHead := mustGit(t, ledger, "rev-parse", "HEAD")
			beforeIndex := mustGit(t, ledger, "write-tree")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s.git = &cancelRetractionRunner{GitRunner: gitutil.DefaultRunner(), cancel: cancel, after: after}

			s.retractOrphanedDrafts(ctx, ledger)

			require.ErrorIs(t, ctx.Err(), context.Canceled, "must reach the post-removal cancellation")
			assert.Equal(t, beforeHead, mustGit(t, ledger, "rev-parse", "HEAD"))
			assert.Equal(t, beforeIndex, mustGit(t, ledger, "write-tree"))
			afterMeta, err := os.ReadFile(metaPath)
			require.NoError(t, err)
			assert.Equal(t, beforeMeta, afterMeta)
			assert.Empty(t, mustGit(t, ledger, "status", "--porcelain"))

			s.git = gitutil.DefaultRunner()
			s.retractOrphanedDrafts(context.Background(), ledger)
			assert.NoFileExists(t, metaPath)
			assert.Equal(t, "session-draft: retract "+name, mustGit(t, ledger, "log", "-1", "--format=%s"))
			assert.Empty(t, mustGit(t, ledger, "status", "--porcelain"))
		})
	}
}
