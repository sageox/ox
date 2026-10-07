//go:build !windows

package daemon

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func singleflightEnv(t *testing.T) {
	t.Helper()
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", recoveryRuntimeDir(t, "ox-sf-"))
}

func newPingServer() *Server {
	server := NewServer(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	server.SetHandlers(func() error { return nil }, func() {}, func() *StatusData { return &StatusData{Running: true} })
	return server
}

// stubSpawnWithServer replaces the exec seam with an in-process IPC server that
// comes up shortly after "spawn", and counts spawns.
func stubSpawnWithServer(t *testing.T, bootDelay time.Duration) *atomic.Int32 {
	t.Helper()
	var spawns atomic.Int32
	orig := spawnDaemonFn
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	t.Cleanup(func() {
		spawnDaemonFn = orig
		cancel()
		wg.Wait()
	})
	spawnDaemonFn = func(supersede bool) (int, error) {
		spawns.Add(1)
		server := newPingServer()
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(bootDelay)
			_ = server.Start(ctx)
		}()
		return os.Getpid(), nil
	}
	return &spawns
}

// holdStartLock simulates another process holding the workspace start lock.
func holdStartLock(t *testing.T) {
	t.Helper()
	release := make(chan struct{})
	held := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- fileutil.WithFileLockTimeout(context.Background(), startLockTarget(CurrentWorkspaceID()), time.Second, func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	t.Cleanup(func() {
		close(release)
		require.NoError(t, <-done)
	})
}

func TestEnsureDaemonImpl_ConcurrentCallersSpawnOnce(t *testing.T) {
	singleflightEnv(t)
	spawns := stubSpawnWithServer(t, 300*time.Millisecond)

	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- ensureDaemonImpl(true)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		assert.NoError(t, err)
	}
	assert.Equal(t, int32(1), spawns.Load(), "exactly one daemon spawn expected")
}

// a start lock held elsewhere (another process) means this caller never spawns;
// it returns nil once the holder's daemon is Running.
func TestEnsureDaemonImpl_LockHeldElsewhereNeverSpawns(t *testing.T) {
	singleflightEnv(t)
	spawns := stubSpawnWithServer(t, 0)
	holdStartLock(t)

	// the lock holder's daemon comes up while the caller is waiting
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(400 * time.Millisecond)
		_ = newPingServer().Start(ctx)
	}()
	t.Cleanup(func() { cancel(); wg.Wait() })

	require.NoError(t, ensureDaemonImpl(true))
	assert.Equal(t, int32(0), spawns.Load(), "caller must not spawn while the start lock is held")
}

func TestEnsureDaemonImpl_NoWaitReturnsWhenLockHeld(t *testing.T) {
	singleflightEnv(t)
	spawns := stubSpawnWithServer(t, 0)
	holdStartLock(t)

	require.NoError(t, ensureDaemonImpl(false))
	assert.Equal(t, int32(0), spawns.Load())
}

func TestMarkDaemonStarting_ReportsStartingForLivePID(t *testing.T) {
	singleflightEnv(t)
	require.Equal(t, DaemonStateStopped, GetState())
	markDaemonStarting(CurrentWorkspaceID(), os.Getpid())
	assert.Equal(t, DaemonStateStarting, GetState())
}
