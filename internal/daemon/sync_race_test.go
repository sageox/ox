package daemon

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConcurrentSyncWithProgress launches multiple goroutines calling
// SyncWithProgress simultaneously to verify pullInProgress correctly
// deduplicates and no data races occur under -race.
func TestConcurrentSyncWithProgress(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	tmpDir := t.TempDir()
	ledgerDir := tmpDir + "/ledger"
	require.NoError(t, os.MkdirAll(ledgerDir, 0755))
	setupGitRepo(t, ledgerDir)

	cfg := DefaultConfig()
	cfg.LedgerPath = ledgerDir
	cfg.SyncIntervalRead = 1 * time.Second
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)

	const goroutines = 5
	var wg sync.WaitGroup
	wg.Add(goroutines)

	// track how many actually ran vs were deduplicated
	var completed atomic.Int32

	for range goroutines {
		go func() {
			defer wg.Done()
			// SyncWithProgress should never panic, even under contention
			_ = scheduler.SyncWithProgress(nil)
			completed.Add(1)
		}()
	}

	wg.Wait()

	// all goroutines must return without panic
	assert.Equal(t, int32(goroutines), completed.Load(), "all goroutines should complete")

	// pullInProgress must be cleared after all calls finish
	scheduler.mu.Lock()
	inProgress := scheduler.pullInProgress
	scheduler.mu.Unlock()
	assert.False(t, inProgress, "pullInProgress should be false after all syncs complete")
}

// TestTriggerSyncUnderLoad fires TriggerSync from many goroutines to verify
// the non-blocking channel send never blocks or panics.
func TestTriggerSyncUnderLoad(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	scheduler := NewSyncScheduler(cfg, nil)

	const goroutines = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for range goroutines {
		go func() {
			defer wg.Done()
			scheduler.TriggerSync()
		}()
	}

	// must complete without blocking - if TriggerSync blocks, test times out
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// all goroutines returned without blocking
	case <-time.After(5 * time.Second):
		t.Fatal("TriggerSync blocked under concurrent load")
	}
}

// TestSyncDuringActiveClone starts a clone operation (fills the semaphore),
// then fires SyncWithProgress to verify no deadlock occurs. The sync should
// proceed (it uses pullInProgress, not cloneSem) while clone slots are full.
func TestSyncDuringActiveClone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	tmpDir := t.TempDir()
	ledgerDir := tmpDir + "/ledger"
	require.NoError(t, os.MkdirAll(ledgerDir, 0755))
	setupGitRepo(t, ledgerDir)

	cfg := DefaultConfig()
	cfg.LedgerPath = ledgerDir
	cfg.SyncIntervalRead = 1 * time.Second
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)

	// fill all clone slots to simulate active clones
	for range maxConcurrentClones {
		scheduler.cloneSem <- struct{}{}
	}

	// SyncWithProgress uses pullInProgress (not cloneSem), so it should
	// complete even when all clone slots are occupied
	done := make(chan error, 1)
	go func() {
		done <- scheduler.SyncWithProgress(nil)
	}()

	select {
	case err := <-done:
		// sync completed (may error on git ops, but did not deadlock)
		_ = err
	case <-time.After(10 * time.Second):
		t.Fatal("SyncWithProgress deadlocked while clone slots were full")
	}

	// drain clone slots
	for range maxConcurrentClones {
		<-scheduler.cloneSem
	}
}

// TestRapidSequentialSyncs calls SyncWithProgress many times in a tight loop
// to verify each returns successfully and shared state stays consistent.
func TestRapidSequentialSyncs(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	tmpDir := t.TempDir()
	ledgerDir := tmpDir + "/ledger"
	require.NoError(t, os.MkdirAll(ledgerDir, 0755))
	setupGitRepo(t, ledgerDir)

	cfg := DefaultConfig()
	cfg.LedgerPath = ledgerDir
	cfg.SyncIntervalRead = 1 * time.Second
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)

	const iterations = 10
	for i := range iterations {
		err := scheduler.SyncWithProgress(nil)
		// each call should succeed (remote unchanged, dedup kicks in)
		assert.NoError(t, err, "iteration %d failed", i)
	}

	// verify shared state is consistent after rapid calls
	scheduler.mu.Lock()
	inProgress := scheduler.pullInProgress
	scheduler.mu.Unlock()
	assert.False(t, inProgress, "pullInProgress should be false after all syncs")

	// lastSync should be set (at least one sync verified the remote)
	lastSync := scheduler.LastSync()
	assert.False(t, lastSync.IsZero(), "lastSync should be set after successful syncs")
}

// TestBackgroundTickerPlusManualSyncRace starts the scheduler's main loop
// and simultaneously calls SyncWithProgress to verify they don't race on
// shared state (pullInProgress, lastSync, metrics, etc).
func TestBackgroundTickerPlusManualSyncRace(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	tmpDir := t.TempDir()
	ledgerDir := tmpDir + "/ledger"
	require.NoError(t, os.MkdirAll(ledgerDir, 0755))
	setupGitRepo(t, ledgerDir)

	cfg := DefaultConfig()
	cfg.LedgerPath = ledgerDir
	cfg.SyncIntervalRead = 50 * time.Millisecond // fast ticks to maximize race window
	cfg.TeamContextSyncInterval = 0               // disable team context sync
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// start the background scheduler loop
	schedulerDone := make(chan struct{})
	go func() {
		scheduler.Start(ctx)
		close(schedulerDone)
	}()

	// give the scheduler a moment to start its ticker
	time.Sleep(20 * time.Millisecond)

	// fire manual syncs concurrently with the background ticker
	const manualSyncs = 5
	var wg sync.WaitGroup
	wg.Add(manualSyncs)
	for range manualSyncs {
		go func() {
			defer wg.Done()
			_ = scheduler.SyncWithProgress(nil)
		}()
	}

	wg.Wait()

	// also read shared state concurrently to expose races on readers
	var readersWg sync.WaitGroup
	readersWg.Add(4)
	go func() { defer readersWg.Done(); _ = scheduler.LastSync() }()
	go func() { defer readersWg.Done(); _ = scheduler.SyncHistory() }()
	go func() { defer readersWg.Done(); _ = scheduler.SyncStats() }()
	go func() { defer readersWg.Done(); _ = scheduler.RecentErrorCount() }()
	readersWg.Wait()

	cancel()
	<-schedulerDone
}

// TestConcurrentMetricsRecording verifies SyncMetrics is safe under concurrent
// writes and reads, since the background scheduler and manual sync can both
// record metrics simultaneously.
func TestConcurrentMetricsRecording(t *testing.T) {
	t.Parallel()

	metrics := NewSyncMetrics()

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := range goroutines {
		go func(id int) {
			defer wg.Done()
			for j := range 50 {
				d := time.Duration(id*50+j) * time.Millisecond
				switch id % 4 {
				case 0:
					metrics.RecordPullSuccess(d)
				case 1:
					metrics.RecordPullFailure()
				case 2:
					metrics.RecordConflict()
				case 3:
					metrics.RecordTeamSync()
				}
				// concurrent read while others write
				_ = metrics.Snapshot()
			}
		}(i)
	}

	wg.Wait()

	snap := metrics.Snapshot()
	// sanity: at least some operations were recorded
	total := snap.PullSuccessCount + snap.PullFailureCount + snap.ConflictCount + snap.TeamSyncCount
	assert.Greater(t, total, int64(0), "metrics should have recorded operations")
}

// TestConcurrentWorkspaceRegistryAccess exercises the workspace registry's
// backoff state from multiple goroutines to verify its mutex protects shared maps.
func TestConcurrentWorkspaceRegistryAccess(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)
	registry := scheduler.WorkspaceRegistry()

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := range goroutines {
		go func(id int) {
			defer wg.Done()
			wsID := "workspace-test"
			for range 50 {
				switch id % 5 {
				case 0:
					registry.RecordSyncFailure(wsID)
				case 1:
					registry.ClearSyncFailures(wsID)
				case 2:
					_ = registry.ShouldSync(wsID)
				case 3:
					_, _ = registry.GetSyncRetryInfo(wsID)
				case 4:
					registry.SetWorkspaceError(wsID, "test error")
					registry.ClearWorkspaceError(wsID)
				}
			}
		}(i)
	}

	wg.Wait()
}

// TestConcurrentRecordSyncAndHistory exercises recordSync and SyncHistory/SyncStats
// concurrently to verify the mutex protects the syncHistory slice.
func TestConcurrentRecordSyncAndHistory(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	scheduler := NewSyncScheduler(cfg, nil)

	const goroutines = 10
	var wg sync.WaitGroup
	wg.Add(goroutines * 2) // half writers, half readers

	// writers
	for i := range goroutines {
		go func(id int) {
			defer wg.Done()
			for j := range 20 {
				d := time.Duration(id*20+j) * time.Millisecond
				scheduler.recordSync("pull", "ledger", d, id)
			}
		}(i)
	}

	// readers
	for range goroutines {
		go func() {
			defer wg.Done()
			for range 20 {
				_ = scheduler.SyncHistory()
				_ = scheduler.SyncStats()
			}
		}()
	}

	wg.Wait()

	history := scheduler.SyncHistory()
	assert.Greater(t, len(history), 0, "should have recorded sync events")
	// max history is 100, verify cap is respected
	assert.LessOrEqual(t, len(history), scheduler.maxSyncHistory)
}

// TestConcurrentActivityCallback verifies SetActivityCallback and recordActivity
// don't race when called from multiple goroutines.
func TestConcurrentActivityCallback(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	scheduler := NewSyncScheduler(cfg, nil)

	var callCount atomic.Int64
	scheduler.SetActivityCallback(func() {
		callCount.Add(1)
	})

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for range goroutines {
		go func() {
			defer wg.Done()
			for range 10 {
				scheduler.recordActivity()
			}
		}()
	}

	wg.Wait()

	assert.Equal(t, int64(goroutines*10), callCount.Load(),
		"every recordActivity call should invoke the callback exactly once")
}

// TestConcurrentLastSyncReads verifies LastSync is safe to read while
// doPull updates it concurrently.
func TestConcurrentLastSyncReads(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	tmpDir := t.TempDir()
	ledgerDir := tmpDir + "/ledger"
	require.NoError(t, os.MkdirAll(ledgerDir, 0755))
	setupGitRepo(t, ledgerDir)

	cfg := DefaultConfig()
	cfg.LedgerPath = ledgerDir
	cfg.SyncIntervalRead = 1 * time.Second
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)

	const readers = 10
	var wg sync.WaitGroup
	wg.Add(readers + 1)

	// one writer doing syncs
	go func() {
		defer wg.Done()
		for range 5 {
			_ = scheduler.SyncWithProgress(nil)
		}
	}()

	// many concurrent readers
	for range readers {
		go func() {
			defer wg.Done()
			for range 20 {
				// must not panic or produce torn reads
				ts := scheduler.LastSync()
				_ = ts
			}
		}()
	}

	wg.Wait()
}
