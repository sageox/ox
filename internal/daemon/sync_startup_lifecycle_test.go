package daemon

import (
	"context"
	"io"
	"log/slog"
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
