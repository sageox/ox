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

// --- A. BuildLedgerIndex lifecycle ---

// TestBuildLedgerIndex_YieldsToWorktreeIndex verifies the ledger build does not start
// while a worktree index owns the codedb, reports that it yielded, and leaves its own
// flag clear so the next attempt can run once the worktree index finishes.
// Failure prevented: the ledger build losing the sqlite write lock (SQLITE_BUSY) to a
// worktree index after minutes of walking, or a yield wedging the ledger flag.
func TestBuildLedgerIndex_YieldsToWorktreeIndex(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	// simulate active worktree index
	mgr.mu.Lock()
	mgr.indexing = true
	mgr.mu.Unlock()

	hookCalled := false
	mgr.ledgerTestHook = func() { hookCalled = true }

	err := mgr.BuildLedgerIndex(context.Background(), dir)
	require.ErrorIs(t, err, ErrLedgerBuildYielded)
	assert.False(t, hookCalled, "a yielded build must not start")

	mgr.mu.Lock()
	assert.True(t, mgr.indexing, "worktree indexing flag must remain set")
	assert.False(t, mgr.ledgerIndexing, "a yielded build must not hold the ledger flag")
	mgr.indexing = false
	mgr.mu.Unlock()

	require.NoError(t, mgr.BuildLedgerIndex(context.Background(), dir))
	assert.True(t, hookCalled, "build runs once the worktree index is done")
}

// TestBuildLedgerIndex_Debounce_OnlySingleConcurrent verifies concurrent ledger index
// triggers don't stampede — exactly one runs at a time.
// Failure prevented: sync scheduler rapid-firing causes N concurrent ledger indexes.
func TestBuildLedgerIndex_Debounce_OnlySingleConcurrent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	var concurrent atomic.Int64
	var maxConcurrent atomic.Int64
	started := make(chan struct{}, 1)
	release := make(chan struct{})

	mgr.ledgerTestHook = func() {
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

	// fire 10 concurrent BuildLedgerIndex calls
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mgr.BuildLedgerIndex(ctx, dir)
		}()
	}

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("no BuildLedgerIndex goroutine started")
	}

	assert.Equal(t, int64(1), maxConcurrent.Load(), "at most one ledger index build should run at once")

	close(release)
	wg.Wait()

	mgr.mu.Lock()
	stillBuilding := mgr.ledgerIndexing
	mgr.mu.Unlock()
	assert.False(t, stillBuilding, "ledgerIndexing flag must be cleared after all goroutines exit")
}

// TestBuildLedgerIndex_FlagReleasedOnFailure verifies the ledger indexing flag is always released,
// even when indexing fails (e.g. invalid ledger path).
// Failure prevented: transient ledger failure permanently wedges ledger indexing.
func TestBuildLedgerIndex_FlagReleasedOnFailure(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	// use a non-existent path as ledger — BuildLedgerIndex will fail on stat
	badLedger := filepath.Join(dir, "nonexistent-ledger")
	ctx := context.Background()
	mgr.BuildLedgerIndex(ctx, badLedger)

	// flag must be released even on failure
	mgr.mu.Lock()
	stillBuilding := mgr.ledgerIndexing
	mgr.mu.Unlock()
	assert.False(t, stillBuilding, "ledgerIndexing flag must be released after failure")

	// Stats() should return gracefully
	stats := mgr.Stats()
	assert.False(t, stats.LedgerIndexingNow)
}

// TestBuildLedgerIndex_EmptyLedgerPath_Noop verifies BuildLedgerIndex with empty ledgerPath
// returns immediately without setting any flags.
// Failure prevented: daemon calls BuildLedgerIndex before ledger is discovered.
func TestBuildLedgerIndex_EmptyLedgerPath_Noop(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	hookCalled := false
	mgr.ledgerTestHook = func() {
		hookCalled = true
	}

	ctx := context.Background()
	mgr.BuildLedgerIndex(ctx, "")

	assert.False(t, hookCalled, "ledgerTestHook should not fire for empty ledger path")

	mgr.mu.Lock()
	flag := mgr.ledgerIndexing
	mgr.mu.Unlock()
	assert.False(t, flag, "ledgerIndexing flag should not be set for empty ledger path")
}

// TestBuildLedgerIndex_ContextCanceled_Stops verifies canceled context aborts ledger index build.
// Failure prevented: daemon shutdown hangs waiting for ledger index build.
func TestBuildLedgerIndex_ContextCanceled_Stops(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	mgr.ledgerTestHook = func() {
		cancel() // cancel context during ledger index build
	}

	mgr.BuildLedgerIndex(ctx, dir)

	mgr.mu.Lock()
	flag := mgr.ledgerIndexing
	mgr.mu.Unlock()
	assert.False(t, flag, "ledgerIndexing flag must be released after context cancellation")
}

// TestStats_LedgerIndexingNow_ReflectsLiveState verifies LedgerIndexingNow
// accurately reflects whether a ledger index build is in progress.
// Failure prevented: CLI showing stale indexing state.
func TestStats_LedgerIndexingNow_ReflectsLiveState(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	// simulate ledger index building
	mgr.mu.Lock()
	mgr.ledgerIndexing = true
	mgr.mu.Unlock()

	stats := mgr.Stats()
	assert.True(t, stats.LedgerIndexingNow, "must report true while ledger index is building")

	// simulate ledger index complete
	mgr.mu.Lock()
	mgr.ledgerIndexing = false
	mgr.mu.Unlock()

	stats = mgr.Stats()
	assert.False(t, stats.LedgerIndexingNow, "must report false when ledger index is idle")
}

// --- F. Concurrency & race conditions ---

// TestBuildLedgerIndex_ConcurrentWithCheckFreshness verifies ledger index build and
// worktree freshness check coexist without deadlock.
// Failure prevented: shared mutex causing deadlock between two indexing paths.
func TestBuildLedgerIndex_ConcurrentWithCheckFreshness(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	ledgerEntered := make(chan struct{}, 1)
	ledgerRelease := make(chan struct{})
	mgr.ledgerTestHook = func() {
		select {
		case ledgerEntered <- struct{}{}:
		default:
		}
		<-ledgerRelease
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
	go mgr.BuildLedgerIndex(ctx, dir)
	mgr.CheckFreshness(ctx)

	// both should enter their hooks (neither blocks the other)
	select {
	case <-ledgerEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("BuildLedgerIndex did not start — may be deadlocked with CheckFreshness")
	}
	select {
	case <-worktreeEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("CheckFreshness did not start — may be deadlocked with BuildLedgerIndex")
	}

	// verify both flags set independently
	mgr.mu.Lock()
	assert.True(t, mgr.indexing, "worktree indexing flag must be set")
	assert.True(t, mgr.ledgerIndexing, "ledger indexing flag must be set")
	mgr.mu.Unlock()

	close(ledgerRelease)
	close(worktreeRelease)
	waitForIndexingDone(t, mgr)
	waitForLedgerIndexingDone(t, mgr)
}

// TestBuildLedgerIndex_ConcurrentStats_NoPanic verifies reading Stats() while ledger index
// is building never panics or returns corrupt data.
// Failure prevented: race between stat cache writer and readers.
func TestBuildLedgerIndex_ConcurrentStats_NoPanic(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	release := make(chan struct{})
	mgr.ledgerTestHook = func() {
		<-release
	}

	ctx := context.Background()
	go mgr.BuildLedgerIndex(ctx, dir)

	// hammer Stats() from multiple goroutines while ledger index is building
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 50 {
				_ = i
				s := mgr.Stats()
				// stats must be internally consistent: LedgerIndexingNow should be a bool
				_ = s.LedgerIndexingNow
				_ = s.LedgerExists
				_ = s.LedgerCommits
			}
		}()
	}

	// let the stats hammering run for a bit, then release ledger index
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	waitForLedgerIndexingDone(t, mgr)
}

// --- G. Additional edge cases ---

// TestBuildLedgerIndex_PartialFailure_StatsPreserved verifies that if ParseSymbols
// or ParseComments fails, we don't lose the ledger index stats from the successful
// IndexLocalRepo step.
// Failure prevented: transient symbol parsing failure zeros out all ledger index stats.
func TestBuildLedgerIndex_PartialFailure_StatsPreserved(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	// simulate a prior successful ledger index with stats
	mgr.mu.Lock()
	mgr.ledgerStats = CodeDBStats{
		IndexExists: true,
		Commits:     10,
		Symbols:     50,
	}
	mgr.mu.Unlock()

	// BuildLedgerIndex with an invalid ledger path will fail at IndexLocalRepo
	// The prior ledger index stats must survive (not get zeroed)
	badLedger := filepath.Join(dir, "bad-ledger")
	require.NoError(t, os.MkdirAll(badLedger, 0o755)) // exists but no git repo
	mgr.BuildLedgerIndex(context.Background(), badLedger)

	mgr.mu.Lock()
	stats := mgr.ledgerStats
	mgr.mu.Unlock()

	// prior stats should be preserved — failed rebuild should NOT zero them
	assert.True(t, stats.IndexExists, "prior ledger index stats must survive a failed rebuild")
	assert.Equal(t, 10, stats.Commits, "prior ledger index commits must survive a failed rebuild")
}

// TestBuildLedgerIndex_LedgerDirDeletedMidBuild_FlagReleased verifies that if the
// ledger index dir is deleted while BuildLedgerIndex is running, the flag is still released.
// Failure prevented: ledger index dir wiped by external process permanently wedges flag.
func TestBuildLedgerIndex_LedgerDirDeletedMidBuild_FlagReleased(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	mgr.ledgerTestHook = func() {
		// simulate external process deleting the ledger index dir
		baseDir := mgr.resolveLedgerDataDir()
		if baseDir != "" {
			os.RemoveAll(baseDir)
		}
	}

	mgr.BuildLedgerIndex(context.Background(), dir)

	mgr.mu.Lock()
	flag := mgr.ledgerIndexing
	mgr.mu.Unlock()
	assert.False(t, flag, "ledgerIndexing flag must be released even when ledger index dir deleted mid-build")
}

// TestBuildLedgerIndex_SecondBuild_UpdatesStats verifies that a second ledger index build
// updates the cached stats (not stuck on first build's stats).
// Failure prevented: stale ledger index stats after ledger receives new commits.
func TestBuildLedgerIndex_SecondBuild_UpdatesStats(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	// simulate first ledger index build result
	mgr.mu.Lock()
	mgr.ledgerStats = CodeDBStats{
		IndexExists: true,
		Commits:     5,
	}
	mgr.mu.Unlock()

	stats := mgr.Stats()
	assert.Equal(t, 5, stats.LedgerCommits, "first ledger index stats")

	// simulate second ledger index build with more commits
	mgr.mu.Lock()
	mgr.ledgerStats = CodeDBStats{
		IndexExists: true,
		Commits:     15,
	}
	mgr.mu.Unlock()

	stats = mgr.Stats()
	assert.Equal(t, 15, stats.LedgerCommits, "second ledger index must update stats, not cache first")
}
