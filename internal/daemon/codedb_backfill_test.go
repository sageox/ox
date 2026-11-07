package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/codedb"
	"github.com/sageox/ox/internal/codedb/index"
)

// --- Two-pass indexing: tip first, history in the background ---
//
// Failure class: a deep repo indexed oldest-first has nothing searchable at the
// tip for hours, and a daemon restart in that window forgot the rest was owed.

// oldestSentinel lives in a file added by the repo's second commit and still
// present at the tip. Finding it after pass 1 proves the tip's content is
// searchable although the commit that introduced it is not indexed yet.
const oldestSentinel = "oldest_sentinel_marker"

// backfillGit runs git with the host's config shut out (identity, signing
// hooks), the way seedGitRepo does.
func backfillGit(t *testing.T, repoDir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(),
		"HOME="+t.TempDir(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=ox-test", "GIT_AUTHOR_EMAIL=ox@test",
		"GIT_COMMITTER_NAME=ox-test", "GIT_COMMITTER_EMAIL=ox@test",
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
}

// backfillCommitFile adds one commit that writes name.
func backfillCommitFile(t *testing.T, repoDir, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, name), []byte(content), 0o600))
	backfillGit(t, repoDir, "add", name)
	backfillGit(t, repoDir, "commit", "-q", "-m", "add "+name)
}

// seedLinearHistory builds a repo of exactly commits commits on main.
func seedLinearHistory(t *testing.T, commits int) string {
	t.Helper()
	require.GreaterOrEqual(t, commits, 2)
	repoDir := t.TempDir()
	seedGitRepo(t, repoDir) // commit 1
	backfillCommitFile(t, repoDir, "oldest.go", "package main\n// "+oldestSentinel+"\n")
	for i := 3; i <= commits; i++ {
		backfillCommitFile(t, repoDir, fmt.Sprintf("file_%d.go", i), fmt.Sprintf("package main\n// change %d\n", i))
	}
	return repoDir
}

// passRecord is one call of the worktree index, with the index as it stood when
// the call began.
type passRecord struct {
	opts           index.IndexOptions
	commitsBefore  int
	fileRevsBefore int
	sentinelHits   int
	pendingOnDisk  bool
}

// recordPasses wraps both passes of the worktree index and returns what each one
// saw. fail, when non-nil, may return an error to replace the pass.
func recordPasses(t *testing.T, mgr *CodeDBManager, dataDir string, fail func(opts index.IndexOptions) error) (records func() []passRecord) {
	t.Helper()
	var mu sync.Mutex
	var passes []passRecord
	mgr.worktreeIndexRepoFn = func(ctx context.Context, db *codedb.DB, repoPath string, opts index.IndexOptions) error {
		rec := passRecord{opts: opts}
		require.NoError(t, db.Store().QueryRow("SELECT COUNT(*) FROM commits").Scan(&rec.commitsBefore))
		require.NoError(t, db.Store().QueryRow("SELECT COUNT(*) FROM file_revs").Scan(&rec.fileRevsBefore))
		hits, err := db.Search(ctx, oldestSentinel)
		require.NoError(t, err)
		rec.sentinelHits = len(hits)
		rec.pendingOnDisk = loadIndexState(mgr.logger, dataDir).HistoryPending
		mu.Lock()
		passes = append(passes, rec)
		mu.Unlock()
		if fail != nil {
			if err := fail(opts); err != nil {
				return err
			}
		}
		return db.IndexLocalRepo(ctx, repoPath, opts)
	}
	return func() []passRecord {
		mu.Lock()
		defer mu.Unlock()
		return append([]passRecord(nil), passes...)
	}
}

func newBackfillManager(t *testing.T, repoDir string, depth int) (*CodeDBManager, string) {
	t.Helper()
	dataDir := t.TempDir()
	mgr := NewCodeDBManager(repoDir, codedbTestLogger(), nil)
	mgr.dataDir = dataDir
	mgr.priorityDepthOverride = depth
	return mgr, dataDir
}

func indexCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

// TestBackfill_TipFirstThenBackfill covers the boundary of the depth limit: a
// repo that fits in pass 1 must not run pass 2, one commit more must.
// Failure prevented: the tip stays unsearchable until the whole history is
// walked, or a small repo pays for a pointless second pass.
func TestBackfill_TipFirstThenBackfill(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: real git + SQLite + Bleve indexing")
	}

	const depth = 5
	tests := []struct {
		name      string
		commits   int
		wantPass2 bool
	}{
		{name: "fits in pass 1", commits: 3},
		{name: "exactly the depth: nothing left over", commits: depth},
		{name: "one commit over the depth", commits: depth + 1, wantPass2: true},
		{name: "deep history", commits: 3*depth + 2, wantPass2: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repoDir := seedLinearHistory(t, tt.commits)
			mgr, dataDir := newBackfillManager(t, repoDir, depth)
			records := recordPasses(t, mgr, dataDir, nil)

			result, err := mgr.Index(indexCtx(t), CodeIndexPayload{}, nil)
			require.NoError(t, err)
			require.NotNil(t, result)

			passes := records()
			wantPasses := 1
			if tt.wantPass2 {
				wantPasses = 2
			}
			require.Len(t, passes, wantPasses)

			pass1 := passes[0]
			assert.Equal(t, depth, pass1.opts.MaxHistoryDepth, "pass 1 must be depth-limited")
			assert.False(t, pass1.opts.BackfillHistory)
			assert.Nil(t, pass1.opts.Pace, "pass 1 is not paced")
			assert.NotNil(t, pass1.opts.OnHistoryTruncated)

			if tt.wantPass2 {
				pass2 := passes[1]
				assert.True(t, pass2.opts.BackfillHistory)
				assert.Zero(t, pass2.opts.MaxHistoryDepth, "pass 2 walks all of history")
				assert.NotNil(t, pass2.opts.Pace, "a background pass 2 yields to the machine")
				assert.Equal(t, depth, pass2.commitsBefore, "pass 1 indexes exactly the newest depth commits")
				assert.Positive(t, pass2.fileRevsBefore, "the tip's files exist before pass 2 starts")
				assert.Positive(t, pass2.sentinelHits, "tip content is searchable although its commit is not indexed yet")
				assert.True(t, pass2.pendingOnDisk, "history_pending is on disk before pass 2 starts")
			}

			assert.Equal(t, tt.commits, countCommits(t, dataDir), "the whole history is indexed in the end")
			state := loadIndexState(mgr.logger, dataDir)
			assert.False(t, state.HistoryPending, "nothing is owed once the run finishes")
			assert.Equal(t, readHeadFingerprint(context.Background(), repoDir), state.Head)
		})
	}
}

// TestBackfill_Pass2PacedOnlyInBackground: a person waiting on `ox index` must
// not be slowed by the pacer that protects background runs.
func TestBackfill_Pass2PacedOnlyInBackground(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: real git + SQLite + Bleve indexing")
	}

	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	go func() { _, _ = io.Copy(io.Discard, server) }()

	tests := []struct {
		name     string
		pw       *ProgressWriter
		wantPace bool
	}{
		{name: "background run", pw: nil, wantPace: true},
		{name: "interactive run", pw: &ProgressWriter{conn: client}, wantPace: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repoDir := seedLinearHistory(t, 8)
			mgr, dataDir := newBackfillManager(t, repoDir, 3)
			records := recordPasses(t, mgr, dataDir, nil)

			_, err := mgr.Index(indexCtx(t), CodeIndexPayload{}, tt.pw)
			require.NoError(t, err)

			passes := records()
			require.Len(t, passes, 2, "interactive runs still backfill; they just are not paced")
			assert.Equal(t, tt.wantPace, passes[1].opts.Pace != nil)
		})
	}
}

// TestBackfill_Pass2FailureKeepsPass1Index: the backfill is an enrichment of a
// usable index, so however it ends the tip must stay searchable and the debt
// must stay on disk for the next run.
// Failure prevented: one slow or failed backfill hides a usable index behind a
// failed run, or forgets that history is still owed.
func TestBackfill_Pass2FailureKeepsPass1Index(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: real git + SQLite + Bleve indexing")
	}

	const depth = 4
	tests := []struct {
		name string
		err  error
	}{
		{name: "plain failure", err: errors.New("disk hiccup")},
		{name: "out of time (maxIndexDuration)", err: fmt.Errorf("walk: %w", context.DeadlineExceeded)},
		{name: "canceled (daemon shutdown)", err: fmt.Errorf("walk: %w", context.Canceled)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repoDir := seedLinearHistory(t, 10)
			mgr, dataDir := newBackfillManager(t, repoDir, depth)
			failBackfill := func(opts index.IndexOptions) error {
				if opts.BackfillHistory {
					return tt.err
				}
				return nil
			}
			records := recordPasses(t, mgr, dataDir, failBackfill)

			result, err := mgr.Index(indexCtx(t), CodeIndexPayload{}, nil)
			require.NoError(t, err, "a stopped backfill is not a failed run")
			require.NotNil(t, result, "pass 1's result survives")

			assert.Equal(t, depth, countCommits(t, dataDir), "pass 1's commits are still there")
			stats := mgr.Stats()
			assert.Empty(t, stats.LastError, "the status must not report the usable index as failed")
			assert.True(t, loadIndexState(mgr.logger, dataDir).HistoryPending, "the debt stays on disk")

			// the tip is searchable
			db, err := codedb.Open(dataDir)
			require.NoError(t, err)
			hits, err := db.Search(context.Background(), oldestSentinel)
			require.NoError(t, err)
			require.NoError(t, db.Close())
			assert.NotEmpty(t, hits)

			// and the next run pays the debt, whatever stopped this one
			mgr.worktreeIndexRepoFn = nil
			_, err = mgr.Index(indexCtx(t), CodeIndexPayload{}, nil)
			require.NoError(t, err)
			assert.Equal(t, 10, countCommits(t, dataDir))
			assert.False(t, loadIndexState(mgr.logger, dataDir).HistoryPending)
			assert.Len(t, records(), 2, "the retry replaced the recording hook")
		})
	}
}

// TestBackfill_Pass2CorruptionDiscardsIndex pins the one backfill failure that
// does cost the index: damage would hit every later pass identically.
func TestBackfill_Pass2CorruptionDiscardsIndex(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: real git + SQLite + Bleve indexing")
	}

	repoDir := seedLinearHistory(t, 8)
	mgr, dataDir := newBackfillManager(t, repoDir, 3)
	recordPasses(t, mgr, dataDir, func(opts index.IndexOptions) error {
		if opts.BackfillHistory {
			return fmt.Errorf("walk: %w", index.ErrBleveCorrupt)
		}
		return nil
	})

	_, err := mgr.Index(indexCtx(t), CodeIndexPayload{}, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, index.ErrBleveCorrupt)

	assert.NoFileExists(t, indexStatePath(dataDir), "a discarded index must not leave a state file vouching for it")
	mgr.mu.Lock()
	head := mgr.lastIndexedHead
	mgr.mu.Unlock()
	assert.Empty(t, head, "freshness is forgotten so the next check rebuilds")
}

// TestBackfill_HistoryPendingPersistedBeforePass1Commits: the crash window.
// If pass 1's commits land and the daemon dies before the debt is written, the
// restart finds every recent commit known, sees no truncation, and never learns
// that older history is missing.
func TestBackfill_HistoryPendingPersistedBeforePass1Commits(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: real git + SQLite + Bleve indexing")
	}

	const depth = 4
	repoDir := seedLinearHistory(t, 9)
	mgr, dataDir := newBackfillManager(t, repoDir, depth)

	var atTruncation struct {
		pending bool
		commits int
		fired   int
	}
	mgr.worktreeIndexRepoFn = func(ctx context.Context, db *codedb.DB, repoPath string, opts index.IndexOptions) error {
		if !opts.BackfillHistory {
			orig := opts.OnHistoryTruncated
			opts.OnHistoryTruncated = func(ref string) {
				orig(ref)
				atTruncation.fired++
				atTruncation.pending = loadIndexState(mgr.logger, dataDir).HistoryPending
				_ = db.Store().QueryRow("SELECT COUNT(*) FROM commits").Scan(&atTruncation.commits)
			}
			// the process dies right after pass 1's commits are durable
			if err := db.IndexLocalRepo(ctx, repoPath, opts); err != nil {
				return err
			}
			return errors.New("killed after pass 1 commit")
		}
		return db.IndexLocalRepo(ctx, repoPath, opts)
	}

	_, err := mgr.Index(indexCtx(t), CodeIndexPayload{}, nil)
	require.Error(t, err)
	require.Equal(t, 1, atTruncation.fired)
	assert.True(t, atTruncation.pending, "history_pending must be on disk the moment truncation is reported")
	assert.Zero(t, atTruncation.commits, "...before any pass 1 commit is durable")
	assert.Equal(t, depth, countCommits(t, dataDir), "pass 1's commits did land")

	// restart: a fresh manager on the same data dir. Every recent commit is known now,
	// so only the persisted debt can bring pass 2 back.
	restarted := NewCodeDBManager(repoDir, codedbTestLogger(), nil)
	restarted.dataDir = dataDir
	restarted.priorityDepthOverride = depth
	restarted.CheckFreshness(indexCtx(t))
	waitForIndexingDone(t, restarted)

	assert.Equal(t, 9, countCommits(t, dataDir), "the restarted daemon finished the backfill")
	assert.False(t, loadIndexState(restarted.logger, dataDir).HistoryPending)
}

// TestBackfill_HealRetryReportsTruncationAgain: when pass 1 hits a corrupt cache,
// OpenIndexWithHeal wipes the data dir (and the pending mark with it) and runs
// pass 1 again. The second attempt must persist the debt anew.
// Failure prevented: a heal retry swallows its own truncation report because the
// first attempt already "had" it, and the rebuilt index never gets its backfill.
func TestBackfill_HealRetryReportsTruncationAgain(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: real git + SQLite + Bleve indexing")
	}

	repoDir := seedLinearHistory(t, 9)
	mgr, dataDir := newBackfillManager(t, repoDir, 3)

	attempts := 0
	pendingAfterRetryReport := false
	mgr.worktreeIndexRepoFn = func(ctx context.Context, db *codedb.DB, repoPath string, opts index.IndexOptions) error {
		if opts.BackfillHistory {
			return db.IndexLocalRepo(ctx, repoPath, opts)
		}
		attempts++
		if attempts == 1 {
			opts.OnHistoryTruncated("refs/heads/main")
			return fmt.Errorf("walk: %w", index.ErrBleveCorrupt)
		}
		orig := opts.OnHistoryTruncated
		opts.OnHistoryTruncated = func(ref string) {
			orig(ref)
			pendingAfterRetryReport = loadIndexState(mgr.logger, dataDir).HistoryPending
		}
		return db.IndexLocalRepo(ctx, repoPath, opts)
	}

	_, err := mgr.Index(indexCtx(t), CodeIndexPayload{}, nil)
	require.NoError(t, err)
	require.Equal(t, 2, attempts, "the corrupt first attempt was retried on a clean cache")
	assert.True(t, pendingAfterRetryReport, "the retry must persist history_pending itself")
	assert.Equal(t, 9, countCommits(t, dataDir))
}

// TestBackfill_FullReindexClearsOwedHistory: --full wipes the index, so a debt
// recorded against the wiped index is void. A depth-limited rebuild owes its own.
func TestBackfill_FullReindexClearsOwedHistory(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: real git + SQLite + Bleve indexing")
	}

	tests := []struct {
		name       string
		commits    int
		wantPasses int
	}{
		{name: "fits in pass 1: stale debt is not paid again", commits: 3, wantPasses: 1},
		{name: "deeper than pass 1: the rebuild owes its own", commits: 8, wantPasses: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repoDir := seedLinearHistory(t, tt.commits)
			mgr, dataDir := newBackfillManager(t, repoDir, 4)
			require.NoError(t, saveIndexState(dataDir, codeDBIndexState{Head: "stale", HistoryPending: true}))
			records := recordPasses(t, mgr, dataDir, nil)

			_, err := mgr.Index(indexCtx(t), CodeIndexPayload{Full: true}, nil)
			require.NoError(t, err)

			assert.Len(t, records(), tt.wantPasses)
			assert.Equal(t, tt.commits, countCommits(t, dataDir))
			state := loadIndexState(mgr.logger, dataDir)
			assert.False(t, state.HistoryPending)
			assert.NotEqual(t, "stale", state.Head)
		})
	}
}

// TestCheckFreshness_AlternatesRepoIsNotRetriedEveryCheck: a repo that cannot be
// indexed at all still records its HEAD, as the end-of-run cache update used to,
// so the 15-minute check does not reopen the cache each time.
func TestCheckFreshness_AlternatesRepoIsNotRetriedEveryCheck(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: real git + SQLite + Bleve indexing")
	}

	repoDir := seedLinearHistory(t, 3)
	alternates := filepath.Join(repoDir, ".git", "objects", "info", "alternates")
	require.NoError(t, os.MkdirAll(filepath.Dir(alternates), 0o755))
	require.NoError(t, os.WriteFile(alternates, []byte(filepath.Join(t.TempDir(), "objects")+"\n"), 0o600))

	mgr, _ := newBackfillManager(t, repoDir, 100)
	var runs atomic.Int64
	mgr.testHook = func() { runs.Add(1) }

	mgr.CheckFreshness(indexCtx(t))
	waitForIndexingDone(t, mgr)
	mgr.CheckFreshness(indexCtx(t))
	waitForIndexingDone(t, mgr)

	assert.Equal(t, int64(1), runs.Load(), "the second check must be skipped")
}

// TestBackfill_RecordsHeadFromBeforeTheRun: pass 2 can run for hours. A commit
// made meanwhile has to read as "changed" at the next check; recording the HEAD
// read at the end would mark it indexed although its symbols never were parsed.
func TestBackfill_RecordsHeadFromBeforeTheRun(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: real git + SQLite + Bleve indexing")
	}

	repoDir := seedLinearHistory(t, 8)
	mgr, dataDir := newBackfillManager(t, repoDir, 3)
	headBefore := readHeadFingerprint(context.Background(), repoDir)
	require.NotEmpty(t, headBefore)

	mgr.worktreeIndexRepoFn = func(ctx context.Context, db *codedb.DB, repoPath string, opts index.IndexOptions) error {
		if opts.BackfillHistory {
			backfillCommitFile(t, repoDir, "landed_during_backfill.go", "package main\n")
		}
		return db.IndexLocalRepo(ctx, repoPath, opts)
	}
	// through CheckFreshness: that is where the end-of-run HEAD used to be re-read
	mgr.CheckFreshness(indexCtx(t))
	waitForIndexingDone(t, mgr)

	headAfter := readHeadFingerprint(context.Background(), repoDir)
	require.NotEqual(t, headBefore, headAfter, "the commit landed")
	assert.Equal(t, headBefore, loadIndexState(mgr.logger, dataDir).Head, "persisted head is the one the run started from")
	mgr.mu.Lock()
	inMemory := mgr.lastIndexedHead
	mgr.mu.Unlock()
	assert.Equal(t, headBefore, inMemory)

	var runs int
	mgr.testHook = func() { runs++ }
	mgr.CheckFreshness(indexCtx(t))
	waitForIndexingDone(t, mgr)
	assert.Equal(t, 1, runs, "the next check re-runs for the commit that landed mid-run")
}

// TestBackfill_StateFileFailureNeverFailsIndexing: the state file only saves work
// after a restart. A directory squatting on its path makes every read and write
// of it fail on all platforms.
func TestBackfill_StateFileFailureNeverFailsIndexing(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: real git + SQLite + Bleve indexing")
	}

	repoDir := seedLinearHistory(t, 6)
	mgr, dataDir := newBackfillManager(t, repoDir, 2)
	blocker := indexStatePath(dataDir)
	require.NoError(t, os.MkdirAll(filepath.Join(blocker, "keep"), 0o755))

	result, err := mgr.Index(indexCtx(t), CodeIndexPayload{}, nil)
	require.NoError(t, err, "an unwritable state file must not fail the index")
	require.NotNil(t, result)
	assert.Equal(t, 6, countCommits(t, dataDir), "the pipeline still ran to the end, backfill included")
}

// TestBackfill_URLIndexLeavesHistoryStateAlone: a URL index puts another repo's
// commits in the same db; it must neither be depth-limited nor touch the
// worktree's freshness and history state.
func TestBackfill_URLIndexLeavesHistoryStateAlone(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: real git + SQLite + Bleve indexing")
	}

	remote := seedLinearHistory(t, 8)
	repoDir := seedLinearHistory(t, 3)
	mgr, dataDir := newBackfillManager(t, repoDir, 2)
	// IndexRepo has no seam; a depth limit would show as missing commits
	_, err := mgr.Index(indexCtx(t), CodeIndexPayload{URL: remote}, nil)
	require.NoError(t, err)

	assert.Equal(t, 8, countCommits(t, dataDir), "a URL index is never depth-limited")
	assert.NoFileExists(t, indexStatePath(dataDir), "a URL index records nothing about the worktree")
	mgr.mu.Lock()
	head := mgr.lastIndexedHead
	mgr.mu.Unlock()
	assert.Empty(t, head)
}

// --- Progress visible in the periodic log ---

// captureLogger returns a manager logger that keeps INFO lines.
func captureLogger() (*slog.Logger, *lockedBuffer) {
	buf := &lockedBuffer{}
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})), buf
}

// TestBackfill_ProgressLogCarriesLastProgress: background runs have no
// ProgressWriter, so before this the 30s line said only how long a run had
// been going.
// Failure prevented: a stuck run and a slow run look identical in the log.
func TestBackfill_ProgressLogCarriesLastProgress(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: real git + SQLite + Bleve indexing")
	}

	repoDir := seedLinearHistory(t, 3)
	mgr, _ := newBackfillManager(t, repoDir, 100)
	logger, logs := captureLogger()
	mgr.logger = logger
	mgr.progressLogInterval = 5 * time.Millisecond

	const message = "Ref 1/1: refs/heads/main - 3 new commits"
	wantField := fmt.Sprintf("last_progress=%q", message)
	mgr.worktreeIndexRepoFn = func(ctx context.Context, db *codedb.DB, repoPath string, opts index.IndexOptions) error {
		require.NotNil(t, opts.Progress)
		opts.Progress(message)
		// hold the pass open until the ticker has had a chance to log it
		require.Eventually(t, func() bool {
			return strings.Contains(logs.String(), wantField)
		}, 10*time.Second, 5*time.Millisecond, "the progress message never reached the log")
		return db.IndexLocalRepo(ctx, repoPath, opts)
	}

	_, err := mgr.Index(indexCtx(t), CodeIndexPayload{}, nil) // pw == nil: a background run
	require.NoError(t, err)

	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if strings.Contains(line, wantField) {
			assert.Contains(t, line, "codedb indexing in progress", "the message rides on the periodic line")
			assert.Contains(t, line, "elapsed=")
			return
		}
	}
	t.Fatal("no log line carried the message")
}

// TestBackfill_ProgressMessageDoesNotLeakAcrossRuns: a message from the previous
// run says nothing about this one.
func TestBackfill_ProgressMessageDoesNotLeakAcrossRuns(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: real git + SQLite + Bleve indexing")
	}

	repoDir := seedLinearHistory(t, 2)
	mgr, _ := newBackfillManager(t, repoDir, 100)
	stale := "left over from the previous run"
	mgr.lastProgress.Store(&stale)

	var seenAtStart string
	mgr.worktreeIndexRepoFn = func(ctx context.Context, db *codedb.DB, repoPath string, opts index.IndexOptions) error {
		seenAtStart = mgr.progressSnapshot()
		return db.IndexLocalRepo(ctx, repoPath, opts)
	}
	_, err := mgr.Index(indexCtx(t), CodeIndexPayload{}, nil)
	require.NoError(t, err)
	assert.Empty(t, seenAtStart)
}
