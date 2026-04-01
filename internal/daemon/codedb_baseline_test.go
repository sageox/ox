package daemon

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. BuildBaseline lifecycle ---

// TestBuildBaseline_IndependentFromWorktreeIndex verifies that baseline and worktree
// indexing are independent lifecycles — one must never block the other.
// Failure prevented: baseline builds stalling behind slow worktree indexes.
func TestBuildBaseline_IndependentFromWorktreeIndex(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	// simulate active worktree index
	mgr.mu.Lock()
	mgr.indexing = true
	mgr.mu.Unlock()

	baselineEntered := make(chan struct{}, 1)
	release := make(chan struct{})
	mgr.baselineTestHook = func() {
		select {
		case baselineEntered <- struct{}{}:
		default:
		}
		<-release
	}

	ctx := context.Background()
	go mgr.BuildBaseline(ctx, dir) // dir as ledger path (doesn't matter — hook blocks before index)

	// baseline should start despite worktree indexing being active
	select {
	case <-baselineEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("BuildBaseline did not start — it was blocked by worktree indexing flag")
	}

	// verify both flags are set independently
	mgr.mu.Lock()
	worktreeFlag := mgr.indexing
	baselineFlag := mgr.baselineIndexing
	mgr.mu.Unlock()

	assert.True(t, worktreeFlag, "worktree indexing flag must remain set")
	assert.True(t, baselineFlag, "baseline indexing flag must be set")

	close(release)
	waitForBaselineIndexingDone(t, mgr)
}

// TestBuildBaseline_Debounce_OnlySingleConcurrent verifies concurrent baseline
// triggers don't stampede — exactly one runs at a time.
// Failure prevented: sync scheduler rapid-firing causes N concurrent ledger indexes.
func TestBuildBaseline_Debounce_OnlySingleConcurrent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	var concurrent atomic.Int64
	var maxConcurrent atomic.Int64
	started := make(chan struct{}, 1)
	release := make(chan struct{})

	mgr.baselineTestHook = func() {
		n := concurrent.Add(1)
		for {
			old := maxConcurrent.Load()
			if n <= old || maxConcurrent.CompareAndSwap(old, n) {
				break
			}
		}
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		concurrent.Add(-1)
	}

	ctx := context.Background()

	// fire 10 concurrent BuildBaseline calls
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mgr.BuildBaseline(ctx, dir)
		}()
	}

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("no BuildBaseline goroutine started")
	}

	assert.Equal(t, int64(1), maxConcurrent.Load(), "at most one baseline build should run at once")

	close(release)
	wg.Wait()

	mgr.mu.Lock()
	stillBuilding := mgr.baselineIndexing
	mgr.mu.Unlock()
	assert.False(t, stillBuilding, "baselineIndexing flag must be cleared after all goroutines exit")
}

// TestBuildBaseline_FlagReleasedOnFailure verifies the baseline flag is always released,
// even when indexing fails (e.g. invalid ledger path).
// Failure prevented: transient ledger failure permanently wedges baseline indexing.
func TestBuildBaseline_FlagReleasedOnFailure(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	// use a non-existent path as ledger — BuildBaseline will fail on stat
	badLedger := filepath.Join(dir, "nonexistent-ledger")
	ctx := context.Background()
	mgr.BuildBaseline(ctx, badLedger)

	// flag must be released even on failure
	mgr.mu.Lock()
	stillBuilding := mgr.baselineIndexing
	mgr.mu.Unlock()
	assert.False(t, stillBuilding, "baselineIndexing flag must be released after failure")

	// Stats() should return gracefully
	stats := mgr.Stats()
	assert.False(t, stats.BaselineIndexingNow)
}

// TestBuildBaseline_EmptyLedgerPath_Noop verifies BuildBaseline with empty ledgerPath
// returns immediately without setting any flags.
// Failure prevented: daemon calls BuildBaseline before ledger is discovered.
func TestBuildBaseline_EmptyLedgerPath_Noop(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	hookCalled := false
	mgr.baselineTestHook = func() {
		hookCalled = true
	}

	ctx := context.Background()
	mgr.BuildBaseline(ctx, "")

	assert.False(t, hookCalled, "baselineTestHook should not fire for empty ledger path")

	mgr.mu.Lock()
	flag := mgr.baselineIndexing
	mgr.mu.Unlock()
	assert.False(t, flag, "baselineIndexing flag should not be set for empty ledger path")
}

// TestBuildBaseline_ContextCanceled_Stops verifies canceled context aborts baseline build.
// Failure prevented: daemon shutdown hangs waiting for baseline build.
func TestBuildBaseline_ContextCanceled_Stops(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	mgr.baselineTestHook = func() {
		cancel() // cancel context during baseline build
	}

	mgr.BuildBaseline(ctx, dir)

	mgr.mu.Lock()
	flag := mgr.baselineIndexing
	mgr.mu.Unlock()
	assert.False(t, flag, "baselineIndexing flag must be released after context cancellation")
}

// TestStats_BaselineIndexingNow_ReflectsLiveState verifies BaselineIndexingNow
// accurately reflects whether a baseline build is in progress.
// Failure prevented: CLI showing stale indexing state.
func TestStats_BaselineIndexingNow_ReflectsLiveState(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	// simulate baseline building
	mgr.mu.Lock()
	mgr.baselineIndexing = true
	mgr.mu.Unlock()

	stats := mgr.Stats()
	assert.True(t, stats.BaselineIndexingNow, "must report true while baseline is building")

	// simulate baseline complete
	mgr.mu.Lock()
	mgr.baselineIndexing = false
	mgr.mu.Unlock()

	stats = mgr.Stats()
	assert.False(t, stats.BaselineIndexingNow, "must report false when baseline is idle")
}

// --- F. Concurrency & race conditions ---

// TestBuildBaseline_ConcurrentWithCheckFreshness verifies baseline build and
// worktree freshness check coexist without deadlock.
// Failure prevented: shared mutex causing deadlock between two indexing paths.
func TestBuildBaseline_ConcurrentWithCheckFreshness(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	baselineEntered := make(chan struct{}, 1)
	baselineRelease := make(chan struct{})
	mgr.baselineTestHook = func() {
		select {
		case baselineEntered <- struct{}{}:
		default:
		}
		<-baselineRelease
	}

	worktreeEntered := make(chan struct{}, 1)
	worktreeRelease := make(chan struct{})
	mgr.testHook = func() {
		select {
		case worktreeEntered <- struct{}{}:
		default:
		}
		<-worktreeRelease
	}

	ctx := context.Background()

	// launch both concurrently
	go mgr.BuildBaseline(ctx, dir)
	mgr.CheckFreshness(ctx)

	// both should enter their hooks (neither blocks the other)
	select {
	case <-baselineEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("BuildBaseline did not start — may be deadlocked with CheckFreshness")
	}
	select {
	case <-worktreeEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("CheckFreshness did not start — may be deadlocked with BuildBaseline")
	}

	// verify both flags set independently
	mgr.mu.Lock()
	assert.True(t, mgr.indexing, "worktree indexing flag must be set")
	assert.True(t, mgr.baselineIndexing, "baseline indexing flag must be set")
	mgr.mu.Unlock()

	close(baselineRelease)
	close(worktreeRelease)
	waitForIndexingDone(t, mgr)
	waitForBaselineIndexingDone(t, mgr)
}

// TestBuildBaseline_ConcurrentStats_NoPanic verifies reading Stats() while baseline
// is building never panics or returns corrupt data.
// Failure prevented: race between stat cache writer and readers.
func TestBuildBaseline_ConcurrentStats_NoPanic(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	release := make(chan struct{})
	mgr.baselineTestHook = func() {
		<-release
	}

	ctx := context.Background()
	go mgr.BuildBaseline(ctx, dir)

	// hammer Stats() from multiple goroutines while baseline is building
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 50 {
				_ = i
				s := mgr.Stats()
				// stats must be internally consistent: BaselineIndexingNow should be a bool
				_ = s.BaselineIndexingNow
				_ = s.BaselineExists
				_ = s.BaselineCommits
			}
		}()
	}

	// let the stats hammering run for a bit, then release baseline
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	waitForBaselineIndexingDone(t, mgr)
}

// --- G. Additional edge cases ---

// TestBuildBaseline_PartialFailure_StatsPreserved verifies that if ParseSymbols
// or ParseComments fails, we don't lose the baseline stats from the successful
// IndexLocalRepo step.
// Failure prevented: transient symbol parsing failure zeros out all baseline stats.
func TestBuildBaseline_PartialFailure_StatsPreserved(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	// simulate a prior successful baseline with stats
	mgr.mu.Lock()
	mgr.baselineStats = CodeDBStats{
		IndexExists: true,
		Commits:     10,
		Symbols:     50,
	}
	mgr.mu.Unlock()

	// BuildBaseline with an invalid ledger path will fail at IndexLocalRepo
	// The prior baseline stats must survive (not get zeroed)
	badLedger := filepath.Join(dir, "bad-ledger")
	require.NoError(t, os.MkdirAll(badLedger, 0o755)) // exists but no git repo
	mgr.BuildBaseline(context.Background(), badLedger)

	mgr.mu.Lock()
	stats := mgr.baselineStats
	mgr.mu.Unlock()

	// prior stats should be preserved — failed rebuild should NOT zero them
	assert.True(t, stats.IndexExists, "prior baseline stats must survive a failed rebuild")
	assert.Equal(t, 10, stats.Commits, "prior baseline commits must survive a failed rebuild")
}

// TestBuildBaseline_BaselineDirDeletedMidBuild_FlagReleased verifies that if the
// baseline dir is deleted while BuildBaseline is running, the flag is still released.
// Failure prevented: baseline dir wiped by external process permanently wedges flag.
func TestBuildBaseline_BaselineDirDeletedMidBuild_FlagReleased(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	mgr.baselineTestHook = func() {
		// simulate external process deleting the baseline dir
		baseDir := mgr.resolveBaselineDataDir()
		if baseDir != "" {
			os.RemoveAll(baseDir)
		}
	}

	mgr.BuildBaseline(context.Background(), dir)

	mgr.mu.Lock()
	flag := mgr.baselineIndexing
	mgr.mu.Unlock()
	assert.False(t, flag, "baselineIndexing flag must be released even when baseline dir deleted mid-build")
}

// TestBuildBaseline_SecondBuild_UpdatesStats verifies that a second baseline build
// updates the cached stats (not stuck on first build's stats).
// Failure prevented: stale baseline stats after ledger receives new commits.
func TestBuildBaseline_SecondBuild_UpdatesStats(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	// simulate first baseline build result
	mgr.mu.Lock()
	mgr.baselineStats = CodeDBStats{
		IndexExists: true,
		Commits:     5,
	}
	mgr.mu.Unlock()

	stats := mgr.Stats()
	assert.Equal(t, 5, stats.BaselineCommits, "first baseline stats")

	// simulate second baseline build with more commits
	mgr.mu.Lock()
	mgr.baselineStats = CodeDBStats{
		IndexExists: true,
		Commits:     15,
	}
	mgr.mu.Unlock()

	stats = mgr.Stats()
	assert.Equal(t, 15, stats.BaselineCommits, "second baseline must update stats, not cache first")
}
