package daemon

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startupWaitingContext signals that the startup task reached its timer wait.
// It lets cancellation exercise that path without relying on a scheduling sleep.
type startupWaitingContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (ctx *startupWaitingContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.waiting) })
	return ctx.Context.Done()
}

func awaitStartupCompletion(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("startup team sync did not finish")
	}
}

// A stopped scheduler must not leave its startup pull waiting for the delay,
// then accessing resources or test seams after its caller has torn them down.
func TestTeamSyncStartup_CancelWhileWaiting(t *testing.T) {
	base, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ctx := &startupWaitingContext{Context: base, waiting: make(chan struct{})}
	var calls atomic.Int32
	done := startDelayedTeamContextSync(ctx, time.Hour, func(context.Context) { calls.Add(1) })
	awaitStartupCompletion(t, ctx.waiting)
	cancel()
	awaitStartupCompletion(t, done)
	assert.Zero(t, calls.Load(), "cancellation before the delay must skip the pull")
}

// Cancellation must win when the startup timer is already ready; a stopped
// scheduler cannot start one last pull just because both select cases are runnable.
func TestTeamSyncStartup_AlreadyCanceledSkipsReadyTimer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls atomic.Int32
	done := startDelayedTeamContextSync(ctx, 0, func(context.Context) { calls.Add(1) })
	awaitStartupCompletion(t, done)
	assert.Zero(t, calls.Load(), "an already-canceled context must skip even an immediate pull")
}

// The startup callback must receive its caller's context and complete before
// its done signal, so shutdown can join the same lifetime instead of detached work.
func TestTeamSyncStartup_RunsPullAfterDelay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	called := make(chan context.Context, 1)
	done := startDelayedTeamContextSync(ctx, 0, func(pullCtx context.Context) { called <- pullCtx })
	awaitStartupCompletion(t, done)
	select {
	case pullCtx := <-called:
		assert.Same(t, ctx, pullCtx, "the pull must use the scheduler's context")
	default:
		t.Fatal("startup team pull never ran")
	}
}

// Cancellation cannot report completion while an in-flight startup pull still
// owns scheduler resources; Start joins this completion before returning.
func TestTeamSyncStartup_CancelDuringPullWaitsForCompletion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	done := startDelayedTeamContextSync(ctx, 0, func(pullCtx context.Context) {
		close(started)
		<-pullCtx.Done()
		close(canceled)
		<-release
	})
	t.Cleanup(func() {
		cancel()
		releaseOnce.Do(func() { close(release) })
		awaitStartupCompletion(t, done)
	})
	awaitStartupCompletion(t, started)
	cancel()
	awaitStartupCompletion(t, canceled)
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	select {
	case <-done:
		t.Fatal("startup completion was reported before the pull returned")
	default:
	}
	releaseOnce.Do(func() { close(release) })
	awaitStartupCompletion(t, done)
}

type startupPullGateHandler struct {
	slog.Handler
	gate func()
}

// Handle holds the real startup pull at its first log before Git work, making
// Start's completion join observable without guessing when the goroutine runs.
func (h *startupPullGateHandler) Handle(ctx context.Context, record slog.Record) error {
	if record.Message == "syncing team contexts" {
		h.gate()
	}
	return h.Handler.Handle(ctx, record)
}

// The helper's completion is useful only if Start actually joins it. Hold the
// real startup callback before its git work and prove Start cannot return until
// that callback is released, without guessing when another goroutine will run.
func TestSyncScheduler_StartJoinsCanceledStartupTeamSync(t *testing.T) {
	// Keep the startup credential lookup off the host keyring. Its DBus
	// reader is real I/O and would keep the synctest clock from advancing.
	t.Setenv("OX_GIT_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "git-credentials.json"))
	scheduler, _, _ := newTeamBudgetScheduler(t, 1)
	scheduler.config.SyncIntervalRead = time.Hour
	scheduler.config.TeamContextSyncInterval = time.Hour
	scheduler.config.VersionCheckInterval = 0
	scheduler.config.GCCheckInterval = 0
	scheduler.config.CodeDBCheckInterval = 0
	scheduler.config.LedgerCheckInterval = 0
	scheduler.config.GitHubSyncInterval = 0

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		started := make(chan struct{})
		canceled := make(chan struct{})
		release := make(chan struct{})
		done := make(chan struct{})
		var releaseOnce sync.Once
		// Every channel in Start's select must belong to the virtual-time
		// bubble; the constructor ran outside it to finish the git fixture.
		scheduler.triggerChan = make(chan struct{}, 1)
		scheduler.logger = slog.New(&startupPullGateHandler{
			Handler: slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}),
			gate: func() {
				close(started)
				<-ctx.Done()
				close(canceled)
				<-release
			},
		})
		t.Cleanup(func() {
			cancel()
			releaseOnce.Do(func() { close(release) })
			synctest.Wait()
		})
		begin := time.Now()
		go func() {
			scheduler.Start(ctx)
			close(done)
		}()
		select {
		case <-started:
		case <-time.After(10 * time.Second):
			t.Fatal("scheduler never ran its delayed startup team sync")
		}
		require.Equal(t, 5*time.Second, time.Since(begin), "this must be the startup pull, not the hourly ticker")
		cancel()
		<-canceled
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("Start returned while its startup team sync was still running")
		default:
		}
		releaseOnce.Do(func() { close(release) })
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("Start did not return after its startup team sync completed")
		}
	})
}

func TestSyncScheduler_WatcherPullContinuesDuringPeriodicTeamSync(t *testing.T) {
	if testing.Short() {
		t.Skip("short: waits for periodic Team Context sync")
	}
	t.Setenv("OX_GIT_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "git-credentials.json"))
	scheduler, _, _ := newTeamBudgetScheduler(t, 1)
	_, ledger, writer := setupRacingLedger(t)
	scheduler.config.LedgerPath = ledger
	scheduler.config.SyncIntervalRead = time.Hour
	scheduler.config.TeamContextSyncInterval = 6 * time.Second
	scheduler.config.VersionCheckInterval = 0
	scheduler.config.GCCheckInterval = 0
	scheduler.config.CodeDBCheckInterval = 0
	scheduler.config.LedgerCheckInterval = 0
	scheduler.config.GitHubSyncInterval = 0
	t.Setenv("OX_KB_DISABLE", "1")
	advanceRemote(t, writer, 0)

	var calls atomic.Int32
	pullStarted, pullCanceled, releasePull := make(chan struct{}), make(chan struct{}), make(chan struct{})
	overrideTeamBudgets(t, time.Minute, time.Minute, func(ctx context.Context, _ string) (teamPullOutcome, error) {
		if calls.Add(1) == 2 {
			close(pullStarted)
			<-ctx.Done()
			close(pullCanceled)
			<-releasePull
		}
		return teamPullOutcome{}, ctx.Err()
	})

	// Wait for the startup pull to finish before triggering the watcher.
	// The watcher must advance the Ledger while the team pull is blocked.
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() {
		cancel()
		releaseOnce.Do(func() { close(releasePull) })
		awaitStartupCompletion(t, done)
	})
	go func() {
		scheduler.Start(ctx)
		close(done)
	}()
	select {
	case <-pullStarted:
	case <-time.After(12 * time.Second):
		t.Fatal("periodic team pull did not start after the startup pull")
	}
	fetchHead := filepath.Join(ledger, ".git", "FETCH_HEAD")
	require.Eventually(t, func() bool {
		_, err := os.Stat(fetchHead)
		scheduler.mu.Lock()
		defer scheduler.mu.Unlock()
		return err == nil && !scheduler.pullInProgress
	}, 2*time.Second, 10*time.Millisecond, "initial ledger fetch must finish")
	advanceRemote(t, writer, 1)
	require.NoError(t, os.Remove(fetchHead), "allow the watcher pull to fetch immediately")
	scheduler.TriggerSync()
	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(ledger, "c001.txt"))
		return err == nil
	}, 2*time.Second, 10*time.Millisecond, "a slow Team Context pull blocked the Ledger watcher sync")
	require.EqualValues(t, 2, calls.Load())
	cancel()
	awaitStartupCompletion(t, pullCanceled)
	select {
	case <-done:
		t.Fatal("Start returned while a periodic team pull still held scheduler resources")
	default:
	}
	releaseOnce.Do(func() { close(releasePull) })
	awaitStartupCompletion(t, done)
}

func TestSyncScheduler_WatcherPullContinuesDuringTeamGC(t *testing.T) {
	if testing.Short() {
		t.Skip("short: waits for a Team Context reclone")
	}
	const manifest = "version 1\ninclude .sageox/\ninclude SOUL.md\n"
	bare, teamPath, scheduler := setupClonedTeamContext(t, manifest, nil)
	_, ledger, writer := setupRacingLedger(t)
	advanceRemote(t, writer, 0)
	scheduler.config.LedgerPath = ledger
	scheduler.config.SyncIntervalRead = time.Hour
	scheduler.config.TeamContextSyncInterval = 0
	scheduler.config.VersionCheckInterval = 0
	scheduler.config.GCCheckInterval = 100 * time.Millisecond
	scheduler.config.CodeDBCheckInterval = 0
	scheduler.config.LedgerCheckInterval = 0
	scheduler.config.GitHubSyncInterval = 0
	t.Setenv("OX_KB_DISABLE", "1")

	registry := scheduler.WorkspaceRegistry()
	registry.mu.Lock()
	registry.workspaces["team_gc"] = &WorkspaceState{
		ID: "team_gc", Type: WorkspaceTypeTeamContext, Path: teamPath, CloneURL: "file://" + bare, Exists: true,
	}
	primeConfigCacheLocked(registry)
	registry.mu.Unlock()

	swapStarted, releaseSwap := make(chan struct{}), make(chan struct{})
	var swapOnce sync.Once
	scheduler.gcSwapWindowTestHook = func() {
		swapOnce.Do(func() { close(swapStarted) })
		<-releaseSwap
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		close(releaseSwap)
		awaitStartupCompletion(t, done)
	})
	go func() {
		defer close(done)
		scheduler.Start(ctx)
	}()
	select {
	case <-swapStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("periodic Team Context GC did not reach the checkout swap")
	}
	fetchHead := filepath.Join(ledger, ".git", "FETCH_HEAD")
	require.Eventually(t, func() bool {
		_, err := os.Stat(fetchHead)
		scheduler.mu.Lock()
		defer scheduler.mu.Unlock()
		return err == nil && !scheduler.pullInProgress
	}, 3*time.Second, 10*time.Millisecond, "initial Ledger fetch must finish")
	advanceRemote(t, writer, 1)
	require.NoError(t, os.Remove(fetchHead))
	scheduler.TriggerSync()
	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(ledger, "c001.txt"))
		return err == nil
	}, 3*time.Second, 10*time.Millisecond, "Team Context GC blocked the Ledger watcher sync")
}

func TestSyncScheduler_WatcherWaitsForLedgerGC(t *testing.T) {
	isolateCredentials(t)
	bare, ledger, writer := setupRacingLedger(t)
	require.NoError(t, os.Mkdir(filepath.Join(writer, "sessions"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(writer, "sessions", ".gitkeep"), nil, 0o644))
	advanceRemote(t, writer, 0)
	scheduler := gcTestScheduler(t)
	scheduler.config.LedgerPath = ledger
	scheduler.config.SyncIntervalRead = time.Hour
	scheduler.config.TeamContextSyncInterval = 0
	scheduler.config.VersionCheckInterval = 0
	scheduler.config.GCCheckInterval = 100 * time.Millisecond
	scheduler.config.CodeDBCheckInterval = 0
	scheduler.config.LedgerCheckInterval = 0
	scheduler.config.GitHubSyncInterval = 0
	t.Setenv("OX_KB_DISABLE", "1")

	registry := scheduler.WorkspaceRegistry()
	registry.mu.Lock()
	registry.ledger = &WorkspaceState{
		ID: "ledger", Type: WorkspaceTypeLedger, Path: ledger, CloneURL: "file://" + bare, Exists: true,
	}
	registry.workspaces["ledger"] = registry.ledger
	primeConfigCacheLocked(registry)
	registry.mu.Unlock()

	swapStarted, releaseSwap := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseSwap) }) }
	var swapOnce sync.Once
	scheduler.gcSwapWindowTestHook = func() {
		swapOnce.Do(func() { close(swapStarted) })
		<-releaseSwap
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		release()
		awaitStartupCompletion(t, done)
	})
	go func() {
		defer close(done)
		scheduler.Start(ctx)
	}()
	awaitStartupCompletion(t, swapStarted)

	advanceRemote(t, writer, 1)
	scheduler.TriggerSync()
	require.Never(t, func() bool {
		return len(scheduler.triggerChan) == 0
	}, 250*time.Millisecond, 5*time.Millisecond, "watcher sync entered the Ledger during its swap")

	release()
	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(ledger, "c001.txt"))
		return err == nil
	}, 5*time.Second, 10*time.Millisecond, "queued watcher sync must run after the swap")
}
