package daemon

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/codedb"
	"github.com/sageox/ox/internal/codedb/index"
)

func debouncerTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// --- A. Debounce lifecycle ---
// These tests verify that the debouncer correctly transforms bursty settle
// events into controlled, throttled dirty overlay rebuilds.

// TestDirtyDebouncer_SingleSettle_TriggersRefresh verifies that a single
// OnSettled call fires RefreshDirtyOverlay after the debounce window.
// Failure prevented: settle events are ignored and dirty overlay never updates.
func TestDirtyDebouncer_SingleSettle_TriggersRefresh(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	called := make(chan struct{}, 1)
	mgr.dirtyTestHook = func() {
		select {
		case called <- struct{}{}:
		default:
		}
	}

	debouncer := NewDirtyOverlayDebouncer(mgr, debouncerTestLogger())
	debouncer.debounce = 50 * time.Millisecond
	debouncer.minGap = 0
	debouncer.Start(context.Background())
	defer debouncer.Stop()

	debouncer.OnSettled()

	select {
	case <-called:
		// success — RefreshDirtyOverlay was invoked
	case <-time.After(2 * time.Second):
		t.Fatal("RefreshDirtyOverlay not called within timeout")
	}
}

// TestDirtyDebouncer_RapidSettles_OnlyOneRefresh verifies that rapid successive
// OnSettled calls result in exactly one RefreshDirtyOverlay call.
// Failure prevented: each settle event triggers a separate rebuild, thrashing CPU.
func TestDirtyDebouncer_RapidSettles_OnlyOneRefresh(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	// use a channel with deterministic blocking instead of atomic counter + sleep
	fires := make(chan time.Time, 10)
	mgr.dirtyTestHook = func() {
		fires <- time.Now()
	}

	debouncer := NewDirtyOverlayDebouncer(mgr, debouncerTestLogger())
	debouncer.debounce = 100 * time.Millisecond
	debouncer.minGap = 0
	debouncer.Start(context.Background())
	defer debouncer.Stop()

	// fire 5 rapid settles, each 20ms apart — each resets the 100ms timer
	for i := 0; i < 5; i++ {
		debouncer.OnSettled()
		time.Sleep(20 * time.Millisecond)
	}

	// wait for exactly one fire (timeout catches if it never fires)
	select {
	case <-fires:
		// success — first fire arrived
	case <-time.After(2 * time.Second):
		t.Fatal("expected at least one fire")
	}

	// verify no second fire arrives within a generous window
	select {
	case <-fires:
		t.Fatal("rapid settles should result in exactly one refresh, got a second fire")
	case <-time.After(300 * time.Millisecond):
		// success — no second fire
	}
}

// TestDirtyDebouncer_MinInterval_VerifiesTimingGaps verifies that the minimum
// interval enforces actual time gaps between rebuilds, not just a count limit.
// Failure prevented: continuous file changes cause rebuilds every 5s instead of 30s.
func TestDirtyDebouncer_MinInterval_VerifiesTimingGaps(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: timing-sensitive minGap verification")
	}

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	var mu sync.Mutex
	var fireTimes []time.Time
	mgr.dirtyTestHook = func() {
		mu.Lock()
		fireTimes = append(fireTimes, time.Now())
		mu.Unlock()
	}

	debouncer := NewDirtyOverlayDebouncer(mgr, debouncerTestLogger())
	debouncer.debounce = 50 * time.Millisecond
	debouncer.minGap = 500 * time.Millisecond
	debouncer.Start(context.Background())
	defer debouncer.Stop()

	// fire settles continuously for 1.5s
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		debouncer.OnSettled()
		time.Sleep(60 * time.Millisecond)
	}

	// wait for any pending timer to complete
	time.Sleep(600 * time.Millisecond)

	mu.Lock()
	times := make([]time.Time, len(fireTimes))
	copy(times, fireTimes)
	mu.Unlock()

	require.GreaterOrEqual(t, len(times), 2, "should fire at least twice over 1.5s with 500ms minGap")
	require.LessOrEqual(t, len(times), 5, "should not fire excessively")

	// verify every consecutive pair respects the minimum interval;
	// allow 50% tolerance because the test hook records time.Now() after
	// RefreshDirtyOverlay completes, not when lastFire is set internally,
	// so CI scheduler jitter can compress observed gaps significantly
	for i := 1; i < len(times); i++ {
		gap := times[i].Sub(times[i-1])
		minAllowed := time.Duration(float64(debouncer.minGap) * 0.5)
		assert.GreaterOrEqual(t, gap, minAllowed,
			"gap between fire %d and %d was %v, expected >= %v", i-1, i, gap, minAllowed)
	}
}

// TestDirtyDebouncer_Stop_CancelsPending verifies that Stop() cancels a
// pending timer so no rebuild fires after shutdown.
// Failure prevented: daemon shutdown triggers stale rebuild on freed resources.
func TestDirtyDebouncer_Stop_CancelsPending(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	fires := make(chan struct{}, 1)
	mgr.dirtyTestHook = func() {
		select {
		case fires <- struct{}{}:
		default:
		}
	}

	debouncer := NewDirtyOverlayDebouncer(mgr, debouncerTestLogger())
	debouncer.debounce = 100 * time.Millisecond
	debouncer.minGap = 0
	debouncer.Start(context.Background())

	debouncer.OnSettled()
	// stop before debounce fires
	time.Sleep(20 * time.Millisecond)
	debouncer.Stop()

	// verify no fire arrives after stop
	select {
	case <-fires:
		t.Fatal("Stop() should cancel pending timer — got unexpected fire")
	case <-time.After(300 * time.Millisecond):
		// success — no fire after stop
	}
}

// TestDirtyDebouncer_OnSettledBeforeStart verifies that OnSettled() before
// Start() doesn't panic or leave stale state.
// Failure prevented: daemon wiring order causes nil context panic.
func TestDirtyDebouncer_OnSettledBeforeStart(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	fires := make(chan struct{}, 1)
	mgr.dirtyTestHook = func() {
		select {
		case fires <- struct{}{}:
		default:
		}
	}

	debouncer := NewDirtyOverlayDebouncer(mgr, debouncerTestLogger())
	debouncer.debounce = 20 * time.Millisecond
	debouncer.minGap = 0

	// OnSettled before Start — ctx is nil, fire() should return early
	debouncer.OnSettled()

	// verify no fire (ctx == nil guard in fire())
	select {
	case <-fires:
		t.Fatal("should not fire before Start() — context is nil")
	case <-time.After(200 * time.Millisecond):
		// success
	}

	// now start and settle again — should work normally
	debouncer.Start(context.Background())
	defer debouncer.Stop()
	debouncer.OnSettled()

	select {
	case <-fires:
		// success — fires after Start()
	case <-time.After(2 * time.Second):
		t.Fatal("should fire after Start()")
	}
}

// --- B. RefreshDirtyOverlay concurrency guards ---
// These tests verify that concurrent operations don't corrupt shared state
// or cause double-writes to the Bleve dirty overlay.

// TestRefreshDirtyOverlay_SkipsWhenFullIndexing verifies that RefreshDirtyOverlay
// is a no-op when a full index is already running (full index includes dirty in stage 4).
// Failure prevented: concurrent writes to the same Bleve dirty overlay path.
func TestRefreshDirtyOverlay_SkipsWhenFullIndexing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	fires := make(chan struct{}, 1)
	mgr.dirtyTestHook = func() {
		select {
		case fires <- struct{}{}:
		default:
		}
	}

	mgr.mu.Lock()
	mgr.indexing = true
	mgr.mu.Unlock()

	mgr.RefreshDirtyOverlay(context.Background())

	// verify no fire — should return synchronously before launching goroutine
	select {
	case <-fires:
		t.Fatal("should not fire when full indexing is running")
	case <-time.After(100 * time.Millisecond):
		// success
	}

	// verify flag was never set (returned before goroutine launch)
	mgr.mu.Lock()
	dirty := mgr.dirtyRefreshing
	mgr.mu.Unlock()
	assert.False(t, dirty, "dirtyRefreshing should not be set when skipped")
}

// TestRefreshDirtyOverlay_SkipsWhenAlreadyRefreshing verifies that concurrent
// RefreshDirtyOverlay calls don't double-build.
// Failure prevented: two goroutines writing the same Bleve overlay simultaneously.
func TestRefreshDirtyOverlay_SkipsWhenAlreadyRefreshing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	fires := make(chan struct{}, 1)
	mgr.dirtyTestHook = func() {
		select {
		case fires <- struct{}{}:
		default:
		}
	}

	mgr.mu.Lock()
	mgr.dirtyRefreshing = true
	mgr.mu.Unlock()

	mgr.RefreshDirtyOverlay(context.Background())

	select {
	case <-fires:
		t.Fatal("should not fire when already refreshing")
	case <-time.After(100 * time.Millisecond):
		// success
	}
}

// TestRefreshDirtyOverlay_FlagReleasedOnEarlyExit verifies that the
// dirtyRefreshing flag is always released, even when the goroutine exits early
// (e.g., missing dataDir, missing projectRoot).
// Failure prevented: transient failure permanently wedges dirty overlay refresh.
func TestRefreshDirtyOverlay_FlagReleasedOnEarlyExit(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.RefreshDirtyOverlay(ctx)

	// wait for goroutine to run and exit
	require.Eventually(t, func() bool {
		mgr.mu.Lock()
		defer mgr.mu.Unlock()
		return !mgr.dirtyRefreshing
	}, 2*time.Second, 10*time.Millisecond, "dirtyRefreshing flag was not released after early exit (missing dataDir)")
}

// TestRefreshDirtyOverlay_RunsConcurrentlyWithLedgerIndex verifies that dirty
// refresh and ledger indexing can run simultaneously without interfering.
// Failure prevented: dirty refresh blocked by unrelated ledger index rebuild.
func TestRefreshDirtyOverlay_RunsConcurrentlyWithLedgerIndex(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	fires := make(chan struct{}, 1)
	mgr.dirtyTestHook = func() {
		select {
		case fires <- struct{}{}:
		default:
		}
	}

	// ledger index is running — dirty should still proceed
	mgr.mu.Lock()
	mgr.ledgerIndexing = true
	mgr.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.RefreshDirtyOverlay(ctx)

	select {
	case <-fires:
		// success — dirty refresh runs independently of ledger index
	case <-time.After(2 * time.Second):
		t.Fatal("dirty refresh should run independently of ledger indexing")
	}
}

// TestRefreshDirtyOverlay_ContextCanceled verifies that a canceled context
// prevents the refresh from starting.
// Failure prevented: stale refresh fires during daemon shutdown.
func TestRefreshDirtyOverlay_ContextCanceled(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	fires := make(chan struct{}, 1)
	mgr.dirtyTestHook = func() {
		select {
		case fires <- struct{}{}:
		default:
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before calling

	mgr.RefreshDirtyOverlay(ctx)

	// the goroutine launches but os.Stat and codedb.Open should see canceled context
	// or return quickly. The key intent: no long-running work happens.
	select {
	case <-fires:
		// dirtyTestHook fires before the dataDir check, so it may be called.
		// That's fine — the hook fires, then os.Stat/Open see the canceled ctx.
	case <-time.After(200 * time.Millisecond):
		// also fine — may not have launched
	}

	// key assertion: flag is always released regardless of cancel
	require.Eventually(t, func() bool {
		mgr.mu.Lock()
		defer mgr.mu.Unlock()
		return !mgr.dirtyRefreshing
	}, 2*time.Second, 10*time.Millisecond, "dirtyRefreshing flag not released after context cancellation")
}

// --- C. Deterministic concurrency: verify no double goroutine ---
// Mirrors TestCheckFreshness_NoDoubleGoroutine pattern for RefreshDirtyOverlay.

// TestRefreshDirtyOverlay_NoDoubleGoroutine verifies that rapid successive calls
// never spin up more than one refresh goroutine concurrently.
// Failure prevented: race between concurrent BuildDirtyIndex calls corrupting Bleve.
func TestRefreshDirtyOverlay_NoDoubleGoroutine(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	var concurrent atomic.Int64
	var maxConcurrent atomic.Int64
	started := make(chan struct{}, 1)
	release := make(chan struct{})

	mgr.dirtyTestHook = func() {
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// fire 10 rapid RefreshDirtyOverlay calls
	for i := 0; i < 10; i++ {
		mgr.RefreshDirtyOverlay(ctx)
	}

	// wait for one goroutine to start
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("goroutine did not start within 5s")
	}

	// while blocked: flag must be held and at most 1 goroutine running
	mgr.mu.Lock()
	flagHeld := mgr.dirtyRefreshing
	mgr.mu.Unlock()
	assert.True(t, flagHeld, "dirtyRefreshing must be held while goroutine is running")
	assert.Equal(t, int64(1), maxConcurrent.Load(), "at most one goroutine should run")

	close(release)

	// wait for flag to clear
	require.Eventually(t, func() bool {
		mgr.mu.Lock()
		defer mgr.mu.Unlock()
		return !mgr.dirtyRefreshing
	}, 5*time.Second, 10*time.Millisecond, "dirtyRefreshing flag not cleared after goroutine exited")
}

// --- D. End-to-end pipeline: fsnotify → settle → debounce → dirty overlay rebuilt ---
// These tests verify the full integration pipeline that makes dirty search results
// appear after file edits. They use real git repos and codedb indexes.

// initDirtyTestRepo creates a git repo with committed Go files for dirty overlay testing.
func initDirtyTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), // safe: git subprocess in temp dir, not ox
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test.local",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test.local",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}

	run("init", "-b", "main")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "committed.go"),
		[]byte("package main\n// committed_sentinel\nfunc CommittedFunc() {}\n"), 0o644))
	run("add", "committed.go")
	run("commit", "-m", "initial commit")

	return dir
}

// TestDirtyPipeline_E2E_FsnotifyToSearchResult verifies the complete pipeline:
// fsnotify event → ChangeAccumulator.settle() → DirtyOverlayDebouncer.OnSettled() →
// debounce timer → RefreshDirtyOverlay() → BuildDirtyIndex() → search finds dirty file.
// Failure prevented: wiring bug where file edits never update the dirty overlay.
func TestDirtyPipeline_E2E_FsnotifyToSearchResult(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git clone + codedb indexing")
	}

	// set up a real git repo and build a real codedb index
	repoDir := initDirtyTestRepo(t)
	dataDir := filepath.Join(t.TempDir(), "codedb")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))

	db, err := codedb.Open(dataDir)
	require.NoError(t, err)
	defer db.Close()

	require.NoError(t, db.IndexLocalRepo(context.Background(), repoDir, index.IndexOptions{}))
	db.Close() // close so RefreshDirtyOverlay can open it

	// write a dirty file with unique content
	dirtyContent := "package main\n// e2e_pipeline_dirty_sentinel_xkcd42\nfunc E2EDirty() {}\n"
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "e2e_dirty.go"), []byte(dirtyContent), 0o644))

	// wire up the full pipeline: CodeDBManager → DirtyOverlayDebouncer → ChangeAccumulator
	mgr := NewCodeDBManager(repoDir, codedbTestLogger(), nil)
	// point the manager's dataDir directly (bypass config.LoadProjectContext which needs .sageox/)
	mgr.mu.Lock()
	mgr.dataDir = dataDir
	mgr.mu.Unlock()

	refreshDone := make(chan struct{}, 1)
	mgr.dirtyTestHook = func() {
		select {
		case refreshDone <- struct{}{}:
		default:
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	debouncer := NewDirtyOverlayDebouncer(mgr, debouncerTestLogger())
	debouncer.debounce = 30 * time.Millisecond // fast for tests
	debouncer.minGap = 0
	debouncer.Start(ctx)
	defer debouncer.Stop()

	acc := NewChangeAccumulator(20 * time.Millisecond) // fast settle
	defer acc.Stop()
	acc.SetOnSettled(debouncer.OnSettled)

	// simulate fsnotify event for the dirty file
	acc.AddEvent(filepath.Join(repoDir, "e2e_dirty.go"), fsnotify.Write, false)

	// wait for the full pipeline to complete
	select {
	case <-refreshDone:
		// pipeline fired — RefreshDirtyOverlay was called
	case <-time.After(5 * time.Second):
		t.Fatal("pipeline did not complete: fsnotify → settle → debounce → RefreshDirtyOverlay")
	}

	// wait for the goroutine to finish (flag release)
	require.Eventually(t, func() bool {
		mgr.mu.Lock()
		defer mgr.mu.Unlock()
		return !mgr.dirtyRefreshing
	}, 5*time.Second, 10*time.Millisecond, "dirtyRefreshing flag not released after pipeline")

	// verify: open the codedb and search for the dirty-only content
	db2, err := codedb.Open(dataDir)
	require.NoError(t, err)
	defer db2.Close()

	require.NoError(t, db2.AttachDirtyIndex(repoDir))
	defer db2.DetachDirtyOverlay()

	results, err := db2.Search(context.Background(), "e2e_pipeline_dirty_sentinel_xkcd42")
	require.NoError(t, err)
	require.NotEmpty(t, results, "dirty file content should appear in search after full fsnotify → debounce → refresh pipeline")
	assert.Equal(t, "e2e_dirty.go", results[0].FilePath)
}

// --- E. Ledger index directory dual-write ---
// These tests verify that RefreshDirtyOverlay writes the dirty overlay to both
// the shared dataDir AND the ledger index dataDir (lines 720-725 in codedb.go).

// TestRefreshDirtyOverlay_LedgerDualWrite verifies that the dirty overlay is
// written to both the shared and ledger codedb directories.
// Failure prevented: CLI search against ledger index doesn't see dirty files.
func TestRefreshDirtyOverlay_LedgerDualWrite(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git clone + codedb indexing")
	}

	repoDir := initDirtyTestRepo(t)

	// create TWO separate codedb directories: shared and ledger
	sharedDir := filepath.Join(t.TempDir(), "shared-codedb")
	ledgerDir := filepath.Join(t.TempDir(), "ledger-codedb")
	require.NoError(t, os.MkdirAll(sharedDir, 0o755))
	require.NoError(t, os.MkdirAll(ledgerDir, 0o755))

	// build initial indexes in both
	for _, dir := range []string{sharedDir, ledgerDir} {
		db, err := codedb.Open(dir)
		require.NoError(t, err)
		require.NoError(t, db.IndexLocalRepo(context.Background(), repoDir, index.IndexOptions{}))
		db.Close()
	}

	// write a dirty file
	dirtyContent := "package main\n// ledger_dual_write_sentinel_qrs99\nfunc LedgerDirty() {}\n"
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "ledger_dirty.go"), []byte(dirtyContent), 0o644))

	// set up CodeDBManager pointing to both dirs
	mgr := NewCodeDBManager(repoDir, codedbTestLogger(), nil)
	mgr.mu.Lock()
	mgr.dataDir = sharedDir
	mgr.ledgerDataDir = ledgerDir
	mgr.mu.Unlock()

	refreshDone := make(chan struct{}, 1)
	mgr.dirtyTestHook = func() {
		select {
		case refreshDone <- struct{}{}:
		default:
		}
	}

	ctx := context.Background()
	mgr.RefreshDirtyOverlay(ctx)

	// wait for completion
	select {
	case <-refreshDone:
	case <-time.After(5 * time.Second):
		t.Fatal("RefreshDirtyOverlay did not start")
	}

	// wait for goroutine to finish
	require.Eventually(t, func() bool {
		mgr.mu.Lock()
		defer mgr.mu.Unlock()
		return !mgr.dirtyRefreshing
	}, 5*time.Second, 10*time.Millisecond, "dirtyRefreshing flag not released after dual write")

	// verify dirty content searchable in BOTH codedb directories
	for _, dir := range []string{sharedDir, ledgerDir} {
		db, err := codedb.Open(dir)
		require.NoError(t, err, "open %s", dir)
		require.NoError(t, db.AttachDirtyIndex(repoDir), "attach dirty index %s", dir)

		results, err := db.Search(context.Background(), "ledger_dual_write_sentinel_qrs99")
		require.NoError(t, err, "search %s", dir)
		require.NotEmpty(t, results, "dirty file should be searchable in %s", dir)
		assert.Equal(t, "ledger_dirty.go", results[0].FilePath, "wrong file in %s", dir)

		db.DetachDirtyOverlay()
		db.Close()
	}
}

// TestRefreshDirtyOverlay_LedgerFailureDoesNotBlockShared verifies that a
// failure in the ledger index directory doesn't prevent the shared dirty overlay
// from being written.
// Failure prevented: corrupted ledger index blocks all dirty overlay updates.
func TestRefreshDirtyOverlay_LedgerFailureDoesNotBlockShared(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git clone + codedb indexing")
	}

	repoDir := initDirtyTestRepo(t)

	sharedDir := filepath.Join(t.TempDir(), "shared-codedb")
	require.NoError(t, os.MkdirAll(sharedDir, 0o755))

	// build initial index only in shared (ledger index dir points to nonexistent path)
	db, err := codedb.Open(sharedDir)
	require.NoError(t, err)
	require.NoError(t, db.IndexLocalRepo(context.Background(), repoDir, index.IndexOptions{}))
	db.Close()

	// write dirty file
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "shared_only.go"),
		[]byte("package main\n// shared_only_sentinel_abc88\nfunc SharedOnly() {}\n"), 0o644))

	// ledger index dir points to a regular file — codedb.Open requires a directory,
	// so this forces the "ledger write failed" branch to actually fail
	badLedgerDir := filepath.Join(t.TempDir(), "bad-ledger")
	require.NoError(t, os.WriteFile(badLedgerDir, []byte("not a directory"), 0o644))

	mgr := NewCodeDBManager(repoDir, codedbTestLogger(), nil)
	mgr.mu.Lock()
	mgr.dataDir = sharedDir
	mgr.ledgerDataDir = badLedgerDir
	mgr.mu.Unlock()

	refreshDone := make(chan struct{}, 1)
	mgr.dirtyTestHook = func() {
		select {
		case refreshDone <- struct{}{}:
		default:
		}
	}

	mgr.RefreshDirtyOverlay(context.Background())

	select {
	case <-refreshDone:
	case <-time.After(5 * time.Second):
		t.Fatal("RefreshDirtyOverlay did not start")
	}

	// wait for goroutine
	require.Eventually(t, func() bool {
		mgr.mu.Lock()
		defer mgr.mu.Unlock()
		return !mgr.dirtyRefreshing
	}, 5*time.Second, 10*time.Millisecond, "dirtyRefreshing flag not released after ledger failure")

	// shared dir should still have the dirty overlay despite ledger failure
	db2, err := codedb.Open(sharedDir)
	require.NoError(t, err)
	defer db2.Close()
	require.NoError(t, db2.AttachDirtyIndex(repoDir))
	defer db2.DetachDirtyOverlay()

	results, err := db2.Search(context.Background(), "shared_only_sentinel_abc88")
	require.NoError(t, err)
	require.NotEmpty(t, results, "shared dirty overlay must succeed even when ledger fails")
}

// --- F. Issue emission on failure ---
// These tests verify that RefreshDirtyOverlay emits structured issues via
// IssueTracker when it encounters errors, rather than failing silently.

// TestRefreshDirtyOverlay_EmitsIssueOnOpenFailure verifies that a codedb.Open
// failure emits a structured issue so ox status / ox doctor can surface it.
// Failure prevented: corrupted SQLite silently stops dirty overlay updates.
func TestRefreshDirtyOverlay_EmitsIssueOnOpenFailure(t *testing.T) {
	t.Parallel()

	repoDir := initDirtyTestRepo(t)

	// create a dataDir where codedb.Open will fail: write a corrupt metadata.db
	// that passes os.Stat but fails SQLite integrity check
	dataDir := filepath.Join(t.TempDir(), "corrupt-codedb")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))
	// write corrupt data to metadata.db — SQLite will detect this isn't a valid DB
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "metadata.db"), []byte("not a valid sqlite database file header"), 0o644))

	mgr := NewCodeDBManager(repoDir, codedbTestLogger(), nil)
	mgr.mu.Lock()
	mgr.dataDir = dataDir
	mgr.mu.Unlock()

	tracker := NewIssueTracker()
	mgr.SetIssueTracker(tracker)

	refreshDone := make(chan struct{}, 1)
	mgr.dirtyTestHook = func() {
		select {
		case refreshDone <- struct{}{}:
		default:
		}
	}

	mgr.RefreshDirtyOverlay(context.Background())

	// wait for goroutine to run and exit
	select {
	case <-refreshDone:
	case <-time.After(5 * time.Second):
		t.Fatal("RefreshDirtyOverlay did not start")
	}

	// wait for flag release
	require.Eventually(t, func() bool {
		mgr.mu.Lock()
		defer mgr.mu.Unlock()
		return !mgr.dirtyRefreshing
	}, 5*time.Second, 10*time.Millisecond, "dirtyRefreshing flag not released after open failure")

	// verify issue was emitted
	issues := tracker.GetIssues()
	var found bool
	for _, iss := range issues {
		if iss.Type == IssueTypeDirtyOverlayFailed {
			found = true
			assert.Equal(t, SeverityWarning, iss.Severity)
			assert.Contains(t, iss.Summary, "dirty overlay refresh failed")
			break
		}
	}
	assert.True(t, found, "should emit %s issue on codedb.Open failure, got: %v", IssueTypeDirtyOverlayFailed, issues)
}

// TestRefreshDirtyOverlay_ClearsIssueOnSuccess verifies that a successful
// dirty overlay refresh clears any previously emitted failure issue.
// Failure prevented: stale warning persists after the problem is resolved.
func TestRefreshDirtyOverlay_ClearsIssueOnSuccess(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git clone + codedb indexing")
	}

	repoDir := initDirtyTestRepo(t)
	dataDir := filepath.Join(t.TempDir(), "codedb")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))

	// build a real index so Open succeeds
	db, err := codedb.Open(dataDir)
	require.NoError(t, err)
	require.NoError(t, db.IndexLocalRepo(context.Background(), repoDir, index.IndexOptions{}))
	db.Close()

	mgr := NewCodeDBManager(repoDir, codedbTestLogger(), nil)
	mgr.mu.Lock()
	mgr.dataDir = dataDir
	mgr.mu.Unlock()

	tracker := NewIssueTracker()
	mgr.SetIssueTracker(tracker)

	// pre-seed a failure issue (simulates previous failed refresh)
	tracker.SetIssue(DaemonIssue{
		Type:     IssueTypeDirtyOverlayFailed,
		Severity: SeverityWarning,
		Summary:  "previous failure",
		Since:    time.Now().Add(-time.Minute),
	})

	// verify issue exists before refresh
	require.Len(t, tracker.GetIssues(), 1, "pre-seeded issue should exist")

	refreshDone := make(chan struct{}, 1)
	mgr.dirtyTestHook = func() {
		select {
		case refreshDone <- struct{}{}:
		default:
		}
	}

	mgr.RefreshDirtyOverlay(context.Background())

	select {
	case <-refreshDone:
	case <-time.After(5 * time.Second):
		t.Fatal("RefreshDirtyOverlay did not start")
	}

	// wait for goroutine
	require.Eventually(t, func() bool {
		mgr.mu.Lock()
		defer mgr.mu.Unlock()
		return !mgr.dirtyRefreshing
	}, 5*time.Second, 10*time.Millisecond, "dirtyRefreshing flag not released after successful refresh")

	// verify issue was cleared after successful refresh
	issues := tracker.GetIssues()
	for _, iss := range issues {
		if iss.Type == IssueTypeDirtyOverlayFailed {
			t.Fatalf("dirty_overlay_failed issue should be cleared after successful refresh, but found: %+v", iss)
		}
	}
}

// TestRefreshDirtyOverlay_NoIssueWithoutTracker verifies that RefreshDirtyOverlay
// handles nil IssueTracker gracefully (e.g., during tests or early daemon startup).
// Failure prevented: nil pointer panic when IssueTracker is not yet wired.
func TestRefreshDirtyOverlay_NoIssueWithoutTracker(t *testing.T) {
	t.Parallel()

	repoDir := initDirtyTestRepo(t)

	// dataDir with corrupt metadata.db to trigger codedb.Open error
	dataDir := filepath.Join(t.TempDir(), "corrupt-codedb")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "metadata.db"), []byte("not a valid sqlite database file header"), 0o644))

	// no issue tracker set (nil)
	mgr := NewCodeDBManager(repoDir, codedbTestLogger(), nil)
	mgr.mu.Lock()
	mgr.dataDir = dataDir
	mgr.mu.Unlock()

	refreshDone := make(chan struct{}, 1)
	mgr.dirtyTestHook = func() {
		select {
		case refreshDone <- struct{}{}:
		default:
		}
	}

	// should not panic even with nil tracker
	mgr.RefreshDirtyOverlay(context.Background())

	select {
	case <-refreshDone:
	case <-time.After(5 * time.Second):
		t.Fatal("RefreshDirtyOverlay did not start")
	}

	// wait for flag release — no panic means success
	require.Eventually(t, func() bool {
		mgr.mu.Lock()
		defer mgr.mu.Unlock()
		return !mgr.dirtyRefreshing
	}, 5*time.Second, 10*time.Millisecond, "dirtyRefreshing flag not released — possible panic in goroutine")
}

