//go:build !windows

package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
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

// a live child still booting (e.g. delayed by restart-loop throttling) is the
// in-flight startup: callers must neither kill it nor spawn a competitor, even
// after the short readiness wait expires.
func TestEnsureDaemonImpl_LiveStartingChildNeverRespawned(t *testing.T) {
	singleflightEnv(t)
	spawns := stubSpawnWithServer(t, 0)

	child := exec.Command("sleep", "30")
	require.NoError(t, child.Start())
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	markDaemonStarting(CurrentWorkspaceID(), child.Process.Pid)
	require.Equal(t, DaemonStateStarting, GetState())

	require.NoError(t, ensureDaemonImpl(false))
	require.Error(t, ensureDaemonImpl(true), "wait expiry must not become permission to spawn")
	assert.Equal(t, int32(0), spawns.Load())
	assert.NoError(t, child.Process.Signal(syscall.Signal(0)), "starting child must not be killed")
}

// a Starting marker whose process died is replaced, not waited on forever.
func TestEnsureDaemonImpl_DeadStartingPIDIsReplaced(t *testing.T) {
	singleflightEnv(t)
	spawns := stubSpawnWithServer(t, 0)

	child := exec.Command("true")
	require.NoError(t, child.Run())
	markDaemonStarting(CurrentWorkspaceID(), child.Process.Pid)

	require.NoError(t, ensureDaemonImpl(true))
	assert.Equal(t, int32(1), spawns.Load())
}

func TestMarkDaemonStarting_IgnoresNonPositivePID(t *testing.T) {
	singleflightEnv(t)
	markDaemonStarting(CurrentWorkspaceID(), 0)
	assert.Equal(t, DaemonStateStopped, GetState())
}

func TestMarkDaemonStarting_UnwritableDirIsBestEffort(t *testing.T) {
	singleflightEnv(t)
	pidPath := PidPathForWorkspace(CurrentWorkspaceID())
	// a regular file where the directory must be makes MkdirAll fail
	require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Dir(pidPath)), 0700))
	require.NoError(t, os.RemoveAll(filepath.Dir(pidPath)))
	require.NoError(t, os.WriteFile(filepath.Dir(pidPath), []byte("x"), 0600))
	t.Cleanup(func() { _ = os.Remove(filepath.Dir(pidPath)) })
	markDaemonStarting(CurrentWorkspaceID(), os.Getpid()) // must not panic

	// a directory where the file must be makes WriteFile fail
	require.NoError(t, os.Remove(filepath.Dir(pidPath)))
	require.NoError(t, os.MkdirAll(pidPath, 0700))
	markDaemonStarting(CurrentWorkspaceID(), os.Getpid())
}

func TestLiveDaemonRegistered_NoRegistryEntry(t *testing.T) {
	singleflightEnv(t)
	assert.False(t, liveDaemonRegistered(CurrentWorkspaceID()))
}

func TestStartLockTarget_LivesInDaemonDir(t *testing.T) {
	singleflightEnv(t)
	got := startLockTarget("abc")
	assert.Equal(t, "start-abc", filepath.Base(got))
	assert.Equal(t, filepath.Dir(PidPathForWorkspace("abc")), filepath.Dir(got))
}

const (
	helperEnvSpawnLog = "OX_SF_HELPER_SPAWN_LOG"
	helperEnvGoFile   = "OX_SF_HELPER_GO_FILE"
)

// TestHelperEnsureDaemonProcess is not a test: it is the body of the child
// processes launched by TestEnsureDaemonImpl_SeparateProcessesSpawnOnce. The
// spawn seam is replaced before anything else so a child can never exec a real
// daemon; it appends its pid to a shared file instead.
func TestHelperEnsureDaemonProcess(t *testing.T) {
	spawnLog := os.Getenv(helperEnvSpawnLog)
	if spawnLog == "" {
		t.Skip("helper process only")
	}
	spawnDaemonFn = func(bool) (int, error) {
		f, err := os.OpenFile(spawnLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return 0, err
		}
		defer f.Close()
		if _, err := fmt.Fprintf(f, "%d\n", os.Getpid()); err != nil {
			return 0, err
		}
		// the parent test outlives every helper, so it stands in for the live
		// "daemon" the Starting marker points at
		return os.Getppid(), nil
	}

	// release all helpers together so their start attempts actually collide
	goFile := os.Getenv(helperEnvGoFile)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(goFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("start signal never arrived")
		}
		time.Sleep(time.Millisecond)
	}
	if err := ensureDaemonImpl(false); err != nil {
		t.Fatalf("ensureDaemonImpl: %v", err)
	}
}

// real separate processes: the in-process file-lock gate cannot mask a broken
// cross-process flock here.
func TestEnsureDaemonImpl_SeparateProcessesSpawnOnce(t *testing.T) {
	singleflightEnv(t)
	dir := t.TempDir()
	spawnLog := filepath.Join(dir, "spawns")
	goFile := filepath.Join(dir, "go")

	const helpers = 8
	cmds := make([]*exec.Cmd, helpers)
	outputs := make([]*bytes.Buffer, helpers)
	for i := range cmds {
		outputs[i] = &bytes.Buffer{}
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelperEnsureDaemonProcess$", "-test.v")
		cmd.Env = append(os.Environ(), helperEnvSpawnLog+"="+spawnLog, helperEnvGoFile+"="+goFile) // safe: re-execs test binary with the spawn seam stubbed, not ox CLI
		cmd.Stdout = outputs[i]
		cmd.Stderr = outputs[i]
		require.NoError(t, cmd.Start())
		cmds[i] = cmd
	}
	require.NoError(t, os.WriteFile(goFile, nil, 0600))
	for i, cmd := range cmds {
		assert.NoError(t, cmd.Wait(), "helper %d output:\n%s", i, outputs[i].String())
	}

	data, err := os.ReadFile(spawnLog)
	require.NoError(t, err)
	lines := strings.Fields(string(data))
	assert.Len(t, lines, 1, "exactly one process may spawn; got pids %v", lines)
}

func TestEnsureDaemonImpl_AlreadyRunningDoesNothing(t *testing.T) {
	singleflightEnv(t)
	spawns := stubSpawnWithServer(t, 0)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = newPingServer().Start(ctx)
	}()
	t.Cleanup(func() { cancel(); <-done })
	require.Eventually(t, IsRunning, 5*time.Second, 20*time.Millisecond)

	require.NoError(t, ensureDaemonImpl(true))
	assert.Equal(t, int32(0), spawns.Load())
}

func TestEnsureDaemonImpl_LockHeldPastDeadlineTimesOut(t *testing.T) {
	singleflightEnv(t)
	spawns := stubSpawnWithServer(t, 0)
	orig := startLockTimeout
	startLockTimeout = 250 * time.Millisecond
	t.Cleanup(func() { startLockTimeout = orig })
	holdStartLock(t)

	err := ensureDaemonImpl(true)
	var lockErr *fileutil.ErrLockTimeout
	require.ErrorAs(t, err, &lockErr)
	assert.Equal(t, int32(0), spawns.Load())
}

// the in-flight child registers (Running) while a second caller is waiting.
func TestEnsureDaemonImpl_WaitsForStartingChildToRun(t *testing.T) {
	singleflightEnv(t)
	spawns := stubSpawnWithServer(t, 0)

	child := exec.Command("sleep", "30")
	require.NoError(t, child.Start())
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	markDaemonStarting(CurrentWorkspaceID(), child.Process.Pid)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(300 * time.Millisecond)
		_ = newPingServer().Start(ctx)
	}()
	t.Cleanup(func() { cancel(); <-done })

	require.NoError(t, ensureDaemonImpl(true))
	assert.Equal(t, int32(0), spawns.Load())
}

// the in-flight child dies mid-wait: the marker is dead, so it may be replaced.
func TestEnsureDaemonImpl_StartingChildDiesDuringWaitIsReplaced(t *testing.T) {
	singleflightEnv(t)
	spawns := stubSpawnWithServer(t, 0)

	child := exec.Command("sleep", "30")
	require.NoError(t, child.Start())
	markDaemonStarting(CurrentWorkspaceID(), child.Process.Pid)
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = child.Process.Kill()
		_ = child.Wait()
	}()

	require.NoError(t, ensureDaemonImpl(true))
	assert.Equal(t, int32(1), spawns.Load())
}

func TestEnsureDaemonImpl_SpawnErrorPropagatesAndLeavesStopped(t *testing.T) {
	singleflightEnv(t)
	orig := spawnDaemonFn
	t.Cleanup(func() { spawnDaemonFn = orig })
	spawnDaemonFn = func(bool) (int, error) { return 0, errors.New("exec failed") }

	err := ensureDaemonImpl(true)
	require.ErrorContains(t, err, "exec failed")
	assert.Equal(t, DaemonStateStopped, GetState())
}

func TestEnsureDaemonImpl_NoWaitReturnsAfterSpawn(t *testing.T) {
	singleflightEnv(t)
	var spawns atomic.Int32
	orig := spawnDaemonFn
	t.Cleanup(func() { spawnDaemonFn = orig })
	spawnDaemonFn = func(bool) (int, error) { spawns.Add(1); return os.Getpid(), nil }

	require.NoError(t, ensureDaemonImpl(false))
	assert.Equal(t, int32(1), spawns.Load())
	assert.Equal(t, DaemonStateStarting, GetState(), "spawned pid is recorded as the in-flight startup")
}

func TestEnsureDaemonImpl_SpawnedButNeverRespondsErrors(t *testing.T) {
	singleflightEnv(t)
	orig := spawnDaemonFn
	t.Cleanup(func() { spawnDaemonFn = orig })
	spawnDaemonFn = func(bool) (int, error) { return 0, nil } // no pid: nothing is marked

	require.ErrorContains(t, ensureDaemonImpl(true), "not responding")
}

func TestLiveDaemonRegistered(t *testing.T) {
	singleflightEnv(t)
	workspaceID := CurrentWorkspaceID()

	t.Run("unreadable registry is not live", func(t *testing.T) {
		require.NoError(t, os.MkdirAll(RegistryPath(), 0700)) // a directory: ReadFile fails
		t.Cleanup(func() { _ = os.RemoveAll(RegistryPath()) })
		assert.False(t, liveDaemonRegistered(workspaceID))
	})

	t.Run("live pid is live, dead pid is not", func(t *testing.T) {
		reg, err := LoadRegistry()
		require.NoError(t, err)
		require.NoError(t, reg.Register(DaemonInfo{WorkspaceID: workspaceID, PID: os.Getpid()}))
		assert.True(t, liveDaemonRegistered(workspaceID))

		dead := exec.Command("true")
		require.NoError(t, dead.Run())
		require.NoError(t, reg.Register(DaemonInfo{WorkspaceID: workspaceID, PID: dead.Process.Pid}))
		assert.False(t, liveDaemonRegistered(workspaceID))
	})
}

// a spawner that killed a live daemon marks its child with the supersede env
// var; that child must consume the marker and skip the restart-loop history.
func TestDaemonStart_SupersedeSkipsRestartHistory(t *testing.T) {
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(supersedeEnvVar, "1")

	d := New(nil, nil)
	d.running = true // makes Start return right after the restart bookkeeping
	require.ErrorContains(t, d.Start(), "already running")

	assert.Empty(t, os.Getenv(supersedeEnvVar), "marker must not leak to children")
	history, err := loadRestartHistory()
	require.NoError(t, err)
	assert.Empty(t, history.Restarts)
}

// a registered daemon that cannot be stopped must block the respawn: starting a
// second daemon on its socket is the failure single-flight exists to prevent.
func TestEnsureDaemonImpl_UnstoppableStaleDaemonBlocksSpawn(t *testing.T) {
	t.Setenv("OX_XDG_ENABLE", "1")
	var spawns atomic.Int32
	orig := spawnDaemonFn
	t.Cleanup(func() { spawnDaemonFn = orig })
	spawnDaemonFn = func(bool) (int, error) { spawns.Add(1); return 0, nil }

	pid, _ := startFakeOxDaemon(t, fakeDaemonExitsOnTerm)
	registerStaleDaemon(t, CurrentWorkspaceID(), pid)
	swallowSignals(t, sigTERM, sigKILL)

	err := ensureDaemonImpl(true)
	require.ErrorContains(t, err, "failed to stop stale daemon")
	assert.Equal(t, int32(0), spawns.Load())
}

// a restart history that cannot be written must not stop the daemon booting.
func TestDaemonStart_UnwritableRestartHistoryIsNonFatal(t *testing.T) {
	t.Setenv("OX_XDG_ENABLE", "1")
	notADir := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(notADir, []byte("x"), 0600))
	t.Setenv("XDG_CONFIG_HOME", notADir)

	d := New(nil, nil)
	d.running = true
	require.ErrorContains(t, d.Start(), "already running")
}

func TestStartDaemonProcess(t *testing.T) {
	// LogPath() derives from paths.TempDir(), which is keyed on $USER: without this
	// the subtests below RemoveAll the developer's REAL daemon log directory
	// (/tmp/<user>/sageox/logs) and every running daemon loses its log file
	// (2026-10-07: the directory vanished twice, each time while this package's
	// tests ran). Point the whole test at a throwaway identity and prove it.
	t.Setenv("USER", "ox-test-"+filepath.Base(t.TempDir()))
	t.Setenv("USERNAME", os.Getenv("USER"))
	require.Contains(t, LogPath(), "ox-test-", "LogPath must point at the isolated test identity, never the developer's")
	singleflightEnv(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	// stand-in daemon: records whether the supersede marker reached it
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	script := filepath.Join(dir, "fake-daemon")
	body := "#!/bin/sh\nprintf '%s' \"$" + supersedeEnvVar + "\" > " + marker + "\n"
	require.NoError(t, os.WriteFile(script, []byte(body), 0700))

	readMarker := func(t *testing.T) string {
		t.Helper()
		require.Eventually(t, func() bool { _, err := os.Stat(marker); return err == nil }, 5*time.Second, 10*time.Millisecond)
		time.Sleep(50 * time.Millisecond) // let the shell finish the write
		data, err := os.ReadFile(marker)
		require.NoError(t, err)
		require.NoError(t, os.Remove(marker))
		return string(data)
	}

	t.Run("supersede marker is passed to the child", func(t *testing.T) {
		pid, err := startDaemonProcess(script, true)
		require.NoError(t, err)
		assert.Positive(t, pid)
		assert.Equal(t, "1", readMarker(t))
	})

	t.Run("no marker when not superseding", func(t *testing.T) {
		t.Setenv(supersedeEnvVar, "")
		pid, err := startDaemonProcess(script, false)
		require.NoError(t, err)
		assert.Positive(t, pid)
		assert.Equal(t, "", readMarker(t))
	})

	t.Run("missing executable is an error", func(t *testing.T) {
		_, err := startDaemonProcess(filepath.Join(dir, "absent"), false)
		require.ErrorContains(t, err, "failed to start daemon")
	})

	t.Run("log path that is a directory is an error", func(t *testing.T) {
		logPath := LogPath()
		require.NoError(t, os.RemoveAll(logPath))
		require.NoError(t, os.MkdirAll(logPath, 0700))
		t.Cleanup(func() { _ = os.RemoveAll(logPath) })
		_, err := startDaemonProcess(script, false)
		require.ErrorContains(t, err, "failed to open log file")
	})

	t.Run("log directory that is a file is an error", func(t *testing.T) {
		logDir := filepath.Dir(LogPath())
		require.NoError(t, os.RemoveAll(logDir))
		require.NoError(t, os.WriteFile(logDir, []byte("x"), 0600))
		t.Cleanup(func() { _ = os.Remove(logDir) })
		_, err := startDaemonProcess(script, false)
		require.ErrorContains(t, err, "failed to create log directory")
	})
}
