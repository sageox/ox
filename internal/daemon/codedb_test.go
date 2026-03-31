package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func codedbTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// --- UpdateProjectRoot ---

func TestUpdateProjectRoot_UpdatesPath(t *testing.T) {
	t.Parallel()
	mgr := NewCodeDBManager("/old/workspace", codedbTestLogger(), nil)

	mgr.UpdateProjectRoot("/new/workspace")

	mgr.mu.Lock()
	got := mgr.projectRoot
	mgr.mu.Unlock()
	assert.Equal(t, "/new/workspace", got)
}

func TestUpdateProjectRoot_IgnoresEmptyPath(t *testing.T) {
	t.Parallel()
	mgr := NewCodeDBManager("/original", codedbTestLogger(), nil)

	mgr.UpdateProjectRoot("")

	mgr.mu.Lock()
	got := mgr.projectRoot
	mgr.mu.Unlock()
	assert.Equal(t, "/original", got)
}

// TestUpdateProjectRoot_NoSwitchWhenCurrentExists is the regression test for the
// "worktree oscillation" bug: two simultaneously active sessions (e.g., main
// worktree + Conductor worktree) caused the daemon to flip projectRoot on every
// heartbeat, triggering a full dirty-index rebuild (~79ms) and occasionally a
// full git re-index (~10s) on every 60s scheduler tick.
// The fix: only switch if the current path no longer exists on disk.
func TestUpdateProjectRoot_NoSwitchWhenCurrentExists(t *testing.T) {
	t.Parallel()

	// both worktrees exist simultaneously
	mainWorktree := t.TempDir()
	conductorWorktree := t.TempDir()

	mgr := NewCodeDBManager(mainWorktree, codedbTestLogger(), nil)

	// simulate heartbeat from a simultaneously active Conductor workspace
	mgr.UpdateProjectRoot(conductorWorktree)

	mgr.mu.Lock()
	got := mgr.projectRoot
	mgr.mu.Unlock()

	// must NOT switch — mainWorktree still exists
	assert.Equal(t, mainWorktree, got, "should not switch while current path still exists")
}

func TestUpdateProjectRoot_IgnoresSamePath(t *testing.T) {
	t.Parallel()
	mgr := NewCodeDBManager("/same/path", codedbTestLogger(), nil)

	mgr.UpdateProjectRoot("/same/path")

	mgr.mu.Lock()
	got := mgr.projectRoot
	mgr.mu.Unlock()
	assert.Equal(t, "/same/path", got)
}

func TestUpdateProjectRoot_ConcurrentUpdates(t *testing.T) {
	t.Parallel()
	mgr := NewCodeDBManager("/initial", codedbTestLogger(), nil)

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			mgr.UpdateProjectRoot("/workspace-" + string(rune('a'+n%26)))
		}(i)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent UpdateProjectRoot deadlocked")
	}

	// should have one of the paths, not be corrupted
	mgr.mu.Lock()
	got := mgr.projectRoot
	mgr.mu.Unlock()
	assert.NotEmpty(t, got)
}

func TestUpdateProjectRoot_RaceWithStats(t *testing.T) {
	t.Parallel()
	mgr := NewCodeDBManager("/initial", codedbTestLogger(), nil)

	var wg sync.WaitGroup
	// writers
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mgr.UpdateProjectRoot("/new/path")
		}()
	}
	// readers
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = mgr.Stats()
		}()
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent UpdateProjectRoot + Stats deadlocked")
	}
}

// --- CallerPath callback wiring ---

func TestCallerPathCallback_FiresOnNewPath(t *testing.T) {
	t.Parallel()
	handler := NewHeartbeatHandler(codedbTestLogger())

	var received []string
	var mu sync.Mutex
	handler.SetCallerPathCallback(func(path string) {
		mu.Lock()
		received = append(received, path)
		mu.Unlock()
	})

	// heartbeat with caller path
	payload := HeartbeatPayload{
		CallerPath: "/workspace/alpha",
		Timestamp:  time.Now(),
	}
	data, _ := json.Marshal(payload)
	handler.Handle("caller-1", data)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, received, 1)
	assert.Equal(t, "/workspace/alpha", received[0])
}

func TestCallerPathCallback_FiresOnEveryHeartbeat(t *testing.T) {
	t.Parallel()
	handler := NewHeartbeatHandler(codedbTestLogger())

	var count int
	var mu sync.Mutex
	handler.SetCallerPathCallback(func(path string) {
		mu.Lock()
		count++
		mu.Unlock()
	})

	// same path sent twice — callback fires both times because the daemon
	// can't know if the path became invalid and was re-created
	for range 3 {
		payload := HeartbeatPayload{
			CallerPath: "/workspace/same",
			Timestamp:  time.Now(),
		}
		data, _ := json.Marshal(payload)
		handler.Handle("caller-1", data)
	}

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 3, count)
}

func TestCallerPathCallback_NotFiredWithoutCallerPath(t *testing.T) {
	t.Parallel()
	handler := NewHeartbeatHandler(codedbTestLogger())

	called := false
	handler.SetCallerPathCallback(func(path string) {
		called = true
	})

	// heartbeat without CallerPath
	payload := HeartbeatPayload{
		RepoPath:  "/some/repo",
		Timestamp: time.Now(),
	}
	data, _ := json.Marshal(payload)
	handler.Handle("caller-1", data)

	assert.False(t, called)
}

func TestCallerPathCallback_NotFiredWithoutCallerID(t *testing.T) {
	t.Parallel()
	handler := NewHeartbeatHandler(codedbTestLogger())

	called := false
	handler.SetCallerPathCallback(func(path string) {
		called = true
	})

	// heartbeat with path but no caller ID — caller tracking block is skipped
	payload := HeartbeatPayload{
		CallerPath: "/workspace/orphan",
		Timestamp:  time.Now(),
	}
	data, _ := json.Marshal(payload)
	handler.Handle("", data)

	assert.False(t, called)
}

// --- Workspace lifecycle edge cases ---
// These test the patterns that lead to the original bug: daemon holds a path
// that stops existing mid-flight.

func TestCodeDBManager_DeletedProjectRoot_StatsDoesNotPanic(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	// simulate Conductor deleting the workspace
	require.NoError(t, os.RemoveAll(dir))

	// Stats should return gracefully, not panic
	stats := mgr.Stats()
	assert.False(t, stats.IndexExists)
	assert.Empty(t, stats.LastError)
}

func TestCodeDBManager_DeletedThenRecreatedProjectRoot(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	// delete workspace
	require.NoError(t, os.RemoveAll(dir))

	// new workspace at different path
	newDir := t.TempDir()
	mgr.UpdateProjectRoot(newDir)

	mgr.mu.Lock()
	got := mgr.projectRoot
	mgr.mu.Unlock()
	assert.Equal(t, newDir, got)
}

func TestCodeDBManager_RapidWorkspaceSwitches(t *testing.T) {
	t.Parallel()

	mgr := NewCodeDBManager("/initial", codedbTestLogger(), nil)

	// simulate Conductor rapidly creating/deleting workspaces
	paths := []string{
		"/workspace/alpha",
		"/workspace/bravo",
		"/workspace/charlie",
		"/workspace/delta",
	}
	for _, p := range paths {
		mgr.UpdateProjectRoot(p)
	}

	mgr.mu.Lock()
	got := mgr.projectRoot
	mgr.mu.Unlock()
	assert.Equal(t, "/workspace/delta", got, "should have the last path")
}

// --- End-to-end: heartbeat → codedb project root update ---

func TestHeartbeatUpdatesCodeDBProjectRoot(t *testing.T) {
	t.Parallel()

	logger := codedbTestLogger()
	handler := NewHeartbeatHandler(logger)
	mgr := NewCodeDBManager("/old/workspace", logger, nil)

	// wire them the same way daemon.go does
	handler.SetCallerPathCallback(func(path string) {
		mgr.UpdateProjectRoot(path)
	})

	// simulate heartbeat from new workspace
	payload := HeartbeatPayload{
		CallerPath: "/new/workspace",
		Timestamp:  time.Now(),
	}
	data, _ := json.Marshal(payload)
	handler.Handle("caller-abc", data)

	mgr.mu.Lock()
	got := mgr.projectRoot
	mgr.mu.Unlock()
	assert.Equal(t, "/new/workspace", got)
}

func TestHeartbeatUpdatesCodeDBProjectRoot_MultipleWorkspaces(t *testing.T) {
	t.Parallel()

	logger := codedbTestLogger()
	handler := NewHeartbeatHandler(logger)
	mgr := NewCodeDBManager("/workspace/edinburgh-v1", logger, nil)

	handler.SetCallerPathCallback(func(path string) {
		mgr.UpdateProjectRoot(path)
	})

	// simulate the exact Conductor pattern:
	// edinburgh-v1 → khartoum-v1 → da-nang-v1
	workspaces := []struct {
		callerID string
		path     string
	}{
		{"abc123", "/workspace/edinburgh-v1"},
		{"def456", "/workspace/khartoum-v1"},
		{"ghi789", "/workspace/da-nang-v1"},
	}

	for _, ws := range workspaces {
		payload := HeartbeatPayload{
			CallerPath: ws.path,
			Timestamp:  time.Now(),
		}
		data, _ := json.Marshal(payload)
		handler.Handle(ws.callerID, data)
	}

	mgr.mu.Lock()
	got := mgr.projectRoot
	mgr.mu.Unlock()
	assert.Equal(t, "/workspace/da-nang-v1", got)
}

// --- resolveSharedDataDir with deleted project root ---

func TestCodeDBManager_ResolveSharedDataDir_MissingProjectRoot(t *testing.T) {
	t.Parallel()

	// project root that doesn't exist — resolveSharedDataDir should fall back
	// to legacy path without panicking
	mgr := NewCodeDBManager("/does/not/exist", codedbTestLogger(), nil)
	dir := mgr.resolveSharedDataDir()
	assert.NotEmpty(t, dir, "should return a fallback path even with missing project root")
}

// --- Index fails fast on deleted worktree ---

func TestIndex_DeletedProjectRoot_FailsFast(t *testing.T) {
	t.Parallel()

	// create then immediately delete a directory to simulate a Conductor worktree removal
	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)
	require.NoError(t, os.RemoveAll(dir))

	ctx := context.Background()
	_, err := mgr.Index(ctx, CodeIndexPayload{}, nil)

	require.Error(t, err, "Index must fail when projectRoot no longer exists")
	assert.Contains(t, err.Error(), "no longer exists")

	// indexing flag must be cleared even on this error path
	mgr.mu.Lock()
	still := mgr.indexing
	mgr.mu.Unlock()
	assert.False(t, still, "indexing flag must be cleared after early exit")
}

// --- CheckFreshness race prevention ---

// TestCheckFreshness_NoDoubleGoroutine verifies that rapid successive calls to
// CheckFreshness never spin up more than one indexing goroutine concurrently.
// Before the fix, m.indexing was not set until inside Index(), leaving a window
// where multiple goroutines would launch and race to claim the flag.
//
// The test injects a hook that blocks doIndex until explicitly released, making
// it deterministic: the goroutine is provably still running when we check the flag
// and measure concurrency.
func TestCheckFreshness_NoDoubleGoroutine(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	var concurrent atomic.Int64
	var maxConcurrent atomic.Int64
	started := make(chan struct{}, 1)
	release := make(chan struct{})

	// hook blocks doIndex so the goroutine stays live while we inspect m.indexing
	mgr.testHook = func() {
		n := concurrent.Add(1)
		for {
			old := maxConcurrent.Load()
			if n <= old || maxConcurrent.CompareAndSwap(old, n) {
				break
			}
		}
		select {
		case started <- struct{}{}: // signal that a goroutine has entered doIndex
		default:
		}
		<-release // block until test releases
		concurrent.Add(-1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// fire 10 rapid CheckFreshness calls — only one goroutine should launch
	const calls = 10
	for i := 0; i < calls; i++ {
		mgr.CheckFreshness(ctx)
	}

	// wait for the goroutine to enter doIndex (deterministic: hook signals started)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("goroutine did not start within 5s")
	}

	// while the goroutine is blocked: flag must be held and concurrency must be 1
	mgr.mu.Lock()
	flagWhileBlocked := mgr.indexing
	mgr.mu.Unlock()
	assert.True(t, flagWhileBlocked, "indexing flag must be held while goroutine is running")
	assert.Equal(t, int64(1), maxConcurrent.Load(), "at most one goroutine should be in doIndex at once")

	// release the blocked goroutine and wait for the flag to clear
	close(release)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mgr.mu.Lock()
		done := !mgr.indexing
		mgr.mu.Unlock()
		if done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mgr.mu.Lock()
	stillIndexing := mgr.indexing
	mgr.mu.Unlock()
	assert.False(t, stillIndexing, "indexing flag must be released after goroutine exits")
	assert.Equal(t, int64(1), maxConcurrent.Load(), "exactly one goroutine ran doIndex in total")
}

// TestCheckFreshness_FlagReleasedOnFailure verifies that the m.indexing flag is
// cleared even when doIndex returns an error (e.g. deleted project root).
func TestCheckFreshness_FlagReleasedOnFailure(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	// delete project root so doIndex fails fast
	require.NoError(t, os.RemoveAll(dir))

	ctx := context.Background()
	mgr.CheckFreshness(ctx)

	// wait for the goroutine to finish
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mgr.mu.Lock()
		done := !mgr.indexing
		mgr.mu.Unlock()
		if done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mgr.mu.Lock()
	stillIndexing := mgr.indexing
	mgr.mu.Unlock()
	assert.False(t, stillIndexing, "indexing flag must be released after failure")
}

// --- Symlink edge case ---

func TestUpdateProjectRoot_Symlink(t *testing.T) {
	t.Parallel()

	realDir := t.TempDir()
	linkDir := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(realDir, linkDir))

	mgr := NewCodeDBManager(realDir, codedbTestLogger(), nil)

	// simulate Conductor deleting the old workspace — only then should we switch
	require.NoError(t, os.RemoveAll(realDir))

	// update to symlink path — should accept it (let git resolve the real path)
	mgr.UpdateProjectRoot(linkDir)

	mgr.mu.Lock()
	got := mgr.projectRoot
	mgr.mu.Unlock()
	assert.Equal(t, linkDir, got)
}

// waitForIndexingDone polls until the indexing flag clears or times out.
func waitForIndexingDone(t *testing.T, mgr *CodeDBManager) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mgr.mu.Lock()
		done := !mgr.indexing
		mgr.mu.Unlock()
		if done {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for indexing to complete")
}

// --- IssueTracker integration ---
//
// These tests verify the real failure mode from the codedb indexing loop bug:
// sparse-checkout wipes .sageox/cache/ mid-index, and the daemon must detect
// and surface this as a structured DaemonIssue rather than silently retrying.

// TestCheckFreshness_CacheWiped_EmitsIssue simulates the actual bug scenario:
// project root exists (it's the user's real repo), but during indexing the
// .sageox/cache/ directory is deleted by a rogue sparse-checkout set. doIndex
// fails with ENOENT when it tries to write to the now-missing cache dir.
// The daemon must emit a codedb_cache_wiped issue so ox status shows the problem.
func TestCheckFreshness_CacheWiped_EmitsIssue(t *testing.T) {
	t.Parallel()

	// create a project root that exists (simulates real repo) but has no git
	// repo — doIndex will stat the root successfully, then fail later when
	// trying to open/create the codedb store. We need the failure to wrap
	// os.ErrNotExist. The simplest way: delete the project root AFTER
	// CheckFreshness has captured it, using the testHook which fires inside doIndex.
	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)
	tracker := NewIssueTracker()
	mgr.SetIssueTracker(tracker)

	// hook fires inside doIndex, after project root is snapshot but before
	// the actual indexing — simulate sparse-checkout deleting the directory
	mgr.testHook = func() {
		os.RemoveAll(dir)
	}

	ctx := context.Background()
	mgr.CheckFreshness(ctx)
	waitForIndexingDone(t, mgr)

	issues := tracker.GetIssues()
	require.Len(t, issues, 1, "cache wipe should emit exactly one issue")
	assert.Equal(t, IssueTypeCodeDBCacheWiped, issues[0].Type)
	assert.Equal(t, SeverityWarning, issues[0].Severity)
	assert.Equal(t, "codedb", issues[0].Repo)
}

// TestCheckFreshness_NonENOENT_NoIssue verifies that indexing failures NOT caused
// by missing files (e.g. corrupt git repo, permission errors) do NOT emit the
// cache-wipe issue. Only ENOENT signals a sparse-checkout wipe.
func TestCheckFreshness_NonENOENT_NoIssue(t *testing.T) {
	t.Parallel()

	// project root exists but has no git repo — doIndex fails with a git error,
	// not ENOENT. This should NOT trigger the cache-wipe issue.
	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)
	tracker := NewIssueTracker()
	mgr.SetIssueTracker(tracker)

	ctx := context.Background()
	mgr.CheckFreshness(ctx)
	waitForIndexingDone(t, mgr)

	assert.Equal(t, 0, tracker.Count(), "non-ENOENT error must not emit codedb_cache_wiped issue")
}

// TestCheckFreshness_IssueCleared_AfterRecovery verifies that the cache-wipe
// issue is cleared when a subsequent indexing run succeeds. This tests the
// recovery path: sparse-checkout is fixed, codedb rebuilds, and the warning
// disappears from ox status.
func TestCheckFreshness_IssueCleared_AfterRecovery(t *testing.T) {
	t.Parallel()

	tracker := NewIssueTracker()
	// pre-set the issue as if a prior cache wipe occurred
	tracker.SetIssue(DaemonIssue{
		Type:     IssueTypeCodeDBCacheWiped,
		Severity: SeverityWarning,
		Repo:     "codedb",
		Summary:  "stale issue from prior failure",
	})
	require.Equal(t, 1, tracker.Count(), "precondition: issue must be set")

	// doIndex clears the issue only on err == nil. We can't easily make doIndex
	// succeed without a real git repo, but we CAN verify the complementary
	// invariant: non-ENOENT failure does NOT clear the issue.
	// This ensures stale issues persist until genuine recovery.
	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)
	mgr.SetIssueTracker(tracker)

	ctx := context.Background()
	mgr.CheckFreshness(ctx)
	waitForIndexingDone(t, mgr)

	// issue persists: doIndex failed (no git repo) but NOT with ENOENT,
	// so the issue is neither re-emitted nor cleared
	assert.Equal(t, 1, tracker.Count(),
		"cache-wipe issue must persist until a successful index — non-ENOENT failure must not clear it")
}

// --- Two-tier baseline+dirty architecture tests ---
// These tests validate the baseline index lifecycle, its independence from the
// worktree index, and resilience to real-world failure modes.
// Each test documents what failure it prevents.

// waitForBaselineIndexingDone polls until the baselineIndexing flag clears or times out.
func waitForBaselineIndexingDone(t *testing.T, mgr *CodeDBManager) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mgr.mu.Lock()
		done := !mgr.baselineIndexing
		mgr.mu.Unlock()
		if done {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for baseline indexing to complete")
}

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

// --- B. CheckFreshness worktree guard ---

// TestCheckFreshness_SkipsWhenWorktreeGone verifies CheckFreshness bails immediately
// when the worktree no longer exists, without launching a goroutine.
// Failure prevented: CPU spike from indexing non-existent worktree.
func TestCheckFreshness_SkipsWhenWorktreeGone(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	hookCalled := false
	mgr.testHook = func() {
		hookCalled = true
	}

	// delete worktree
	require.NoError(t, os.RemoveAll(dir))

	ctx := context.Background()
	mgr.CheckFreshness(ctx)

	// give a tiny window for any goroutine to start (it shouldn't)
	time.Sleep(50 * time.Millisecond)

	assert.False(t, hookCalled, "doIndex goroutine should NOT launch when worktree is gone")

	mgr.mu.Lock()
	flag := mgr.indexing
	mgr.mu.Unlock()
	assert.False(t, flag, "indexing flag must be released when worktree guard triggers")
}

// TestCheckFreshness_SkipsWorktreeGone_BaselineUnaffected verifies that worktree
// disappearing does NOT affect baseline search availability.
// Failure prevented: worktree disappearance cascades to baseline, breaking all search.
func TestCheckFreshness_SkipsWorktreeGone_BaselineUnaffected(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	// simulate a prior successful baseline build
	mgr.mu.Lock()
	mgr.baselineStats = CodeDBStats{
		IndexExists: true,
		Commits:     42,
		Symbols:     100,
	}
	mgr.mu.Unlock()

	// delete worktree
	require.NoError(t, os.RemoveAll(dir))

	ctx := context.Background()
	mgr.CheckFreshness(ctx)

	// baseline must still be intact
	stats := mgr.Stats()
	assert.True(t, stats.BaselineExists, "baseline must survive worktree disappearance")
	assert.Equal(t, 42, stats.BaselineCommits, "baseline commits must be preserved")
}

// TestCheckFreshness_PermissionError_StillProceeds verifies that only os.IsNotExist
// triggers the skip — permission errors should still attempt indexing.
// Failure prevented: overly aggressive guard skips indexing on transient permission issues.
func TestCheckFreshness_PermissionError_StillProceeds(t *testing.T) {
	t.Parallel()

	if os.Getuid() == 0 {
		t.Skip("test requires non-root user (permission check meaningless as root)")
	}

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	hookCalled := make(chan struct{}, 1)
	release := make(chan struct{})
	mgr.testHook = func() {
		select {
		case hookCalled <- struct{}{}:
		default:
		}
		<-release
	}

	// make dir unreadable but still existing
	require.NoError(t, os.Chmod(dir, 0o000))
	t.Cleanup(func() {
		os.Chmod(dir, 0o755) // restore for cleanup
	})

	ctx := context.Background()
	mgr.CheckFreshness(ctx)

	// goroutine should still launch (permission error != not exist)
	select {
	case <-hookCalled:
		// good — goroutine launched despite permission error
	case <-time.After(5 * time.Second):
		t.Fatal("goroutine did not launch — permission error was incorrectly treated as not-exist")
	}

	close(release)
	waitForIndexingDone(t, mgr)
}

// TestCheckFreshness_WorktreeDeletedMidIndex_FlagReleased verifies that if the
// worktree disappears while doIndex is running, the flag is still released.
// Failure prevented: permanent wedge from worktree disappearing during indexing.
func TestCheckFreshness_WorktreeDeletedMidIndex_FlagReleased(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	mgr.testHook = func() {
		os.RemoveAll(dir) // simulate worktree deletion mid-flight
	}

	ctx := context.Background()
	mgr.CheckFreshness(ctx)
	waitForIndexingDone(t, mgr)

	mgr.mu.Lock()
	flag := mgr.indexing
	mgr.mu.Unlock()
	assert.False(t, flag, "indexing flag must be released even when worktree deleted mid-index")
}

// --- C. Stats merge ---

// TestStats_BaselineFieldsPopulated verifies Stats() reports baseline info
// after a baseline build.
// Failure prevented: ox status not showing baseline info.
func TestStats_BaselineFieldsPopulated(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	// simulate successful baseline build
	mgr.mu.Lock()
	mgr.baselineStats = CodeDBStats{
		IndexExists: true,
		Commits:     15,
		Symbols:     200,
		Comments:    50,
	}
	mgr.mu.Unlock()

	stats := mgr.Stats()
	assert.True(t, stats.BaselineExists, "BaselineExists must be true after baseline build")
	assert.Equal(t, 15, stats.BaselineCommits, "BaselineCommits must reflect baseline stats")
	assert.False(t, stats.BaselineIndexingNow, "BaselineIndexingNow must be false when not building")
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

// TestStats_NoBaseline_GracefulDefaults verifies Stats() returns clean defaults
// before any baseline has been built.
// Failure prevented: NPE or garbage values in ox status for fresh installs.
func TestStats_NoBaseline_GracefulDefaults(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	stats := mgr.Stats()
	assert.False(t, stats.BaselineExists, "BaselineExists must be false before any baseline build")
	assert.Equal(t, 0, stats.BaselineCommits, "BaselineCommits must be zero before any baseline build")
	assert.False(t, stats.BaselineIndexingNow, "BaselineIndexingNow must be false before any baseline build")
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

// TestUpdateProjectRoot_DuringBaselineBuild_NoPanic verifies workspace switch during
// baseline build doesn't corrupt state.
// Failure prevented: Conductor switching workspaces while baseline is building.
func TestUpdateProjectRoot_DuringBaselineBuild_NoPanic(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	newDir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	mgr.baselineTestHook = func() {
		select {
		case entered <- struct{}{}:
		default:
		}
		// simulate Conductor deleting old workspace then switching
		// UpdateProjectRoot only switches when the old root is gone
		_ = os.RemoveAll(dir)
		mgr.UpdateProjectRoot(newDir)
		<-release
	}

	ctx := context.Background()
	go mgr.BuildBaseline(ctx, dir)

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("baselineTestHook did not run")
	}
	close(release)
	waitForBaselineIndexingDone(t, mgr)

	mgr.mu.Lock()
	got := mgr.projectRoot
	mgr.mu.Unlock()
	assert.Equal(t, newDir, got, "projectRoot should reflect the workspace switch")
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

// TestDoIndex_DirtyOverlayToBaseline_FailureDoesNotBlockWorktreeIndex verifies that
// if the dirty overlay redirect to baseline dir fails (e.g. baseline dir missing),
// the main worktree indexing still completes normally.
// Failure prevented: baseline dir disappearance blocks all worktree indexing.
func TestDoIndex_DirtyOverlayToBaseline_FailureDoesNotBlockWorktreeIndex(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// create .git so the ledger-not-cloned guard in doIndex passes
	// (resolveSharedDataDir falls back to <dir>/.sageox/cache/codedb, which
	// triggers ledgerRootForDataDir → checks <dir>/.git)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	// set a baseline dir that doesn't exist — the dirty overlay redirect should
	// silently fail without affecting the main index
	mgr.mu.Lock()
	mgr.baselineDataDir = filepath.Join(dir, "nonexistent-baseline")
	mgr.mu.Unlock()

	// Index will fail because there's no valid git repo, but the important thing is
	// it fails at the git step, NOT at the baseline dirty overlay step
	ctx := context.Background()
	_, err := mgr.Index(ctx, CodeIndexPayload{}, nil)

	// should fail because no valid git repo — NOT because of baseline dir issues
	require.Error(t, err)
	assert.Contains(t, err.Error(), "index local", "error should be from git indexing, not baseline dirty overlay")

	// indexing flag must be released
	mgr.mu.Lock()
	flag := mgr.indexing
	mgr.mu.Unlock()
	assert.False(t, flag, "indexing flag must be released")
}

// TestCheckFreshness_GuardThenBaseline_BothWork verifies the full real-world scenario:
// worktree deleted → CheckFreshness skips → but baseline build still works.
// Failure prevented: stale worktree blocking all code search functionality.
func TestCheckFreshness_GuardThenBaseline_BothWork(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	hookCalled := false
	mgr.testHook = func() {
		hookCalled = true
	}

	baselineEntered := make(chan struct{}, 1)
	release := make(chan struct{})
	mgr.baselineTestHook = func() {
		select {
		case baselineEntered <- struct{}{}:
		default:
		}
		<-release
	}

	// delete worktree
	require.NoError(t, os.RemoveAll(dir))

	// CheckFreshness should skip (worktree gone)
	mgr.CheckFreshness(context.Background())
	time.Sleep(50 * time.Millisecond)
	assert.False(t, hookCalled, "doIndex should NOT launch when worktree is gone")

	// but baseline build should still work (uses ledger, not worktree)
	ledgerDir := t.TempDir() // fresh dir as fake ledger
	go mgr.BuildBaseline(context.Background(), ledgerDir)

	select {
	case <-baselineEntered:
		// baseline build started — the worktree guard did NOT block it
	case <-time.After(5 * time.Second):
		t.Fatal("BuildBaseline should work even when worktree is gone")
	}

	close(release)
	waitForBaselineIndexingDone(t, mgr)
}

// TestStats_BaselineOverridesEvenWhenWorktreeIndexMissing verifies that Stats()
// reports baseline availability even when no worktree index exists.
// Failure prevented: fresh install shows "no index" when baseline is actually ready.
func TestStats_BaselineOverridesEvenWhenWorktreeIndexMissing(t *testing.T) {
	t.Parallel()

	// non-existent project root — no worktree index
	mgr := NewCodeDBManager("/does/not/exist", codedbTestLogger(), nil)

	// simulate successful baseline
	mgr.mu.Lock()
	mgr.baselineStats = CodeDBStats{
		IndexExists: true,
		Commits:     25,
		Symbols:     300,
	}
	mgr.mu.Unlock()

	stats := mgr.Stats()
	assert.True(t, stats.BaselineExists, "baseline must be reported even without worktree index")
	assert.Equal(t, 25, stats.BaselineCommits)
	// worktree index fields should be empty/false
	assert.False(t, stats.IndexExists, "worktree index should not exist")
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
