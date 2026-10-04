package trace

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/selfexec"
	"github.com/stretchr/testify/require"
)

func isolateProcess(t *testing.T) {
	t.Helper()
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	return port
}

func startProcess(t *testing.T, port int) <-chan error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, port, nil) }()
	t.Cleanup(cancel)
	readyCtx, readyCancel := context.WithTimeout(context.Background(), time.Second)
	defer readyCancel()
	require.NoError(t, waitReady(readyCtx, port))
	return done
}

// Concurrent startup calls must not replace a healthy receiver or start one on
// a second port; a stale pidfile must never prevent fresh capture.
func TestReceiverSingletonAndStaleState(t *testing.T) {
	isolateProcess(t)
	require.NoError(t, os.MkdirAll(StateDir(), 0700))
	require.NoError(t, os.WriteFile(pidPath(), []byte(`{"pid":99999999,"port":1}`), 0600))
	port := freePort(t)
	done := startProcess(t, port)
	original, err := Health(context.Background(), port)
	require.NoError(t, err)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() { errs <- EnsureRunning(context.Background(), port) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.NoError(t, Run(context.Background(), freePort(t), nil))
	now, err := Health(context.Background(), port)
	require.NoError(t, err)
	require.Equal(t, original, now)
	require.ErrorContains(t, EnsureRunning(context.Background(), freePort(t)), "already runs on port")
	require.NoError(t, Stop(context.Background(), port))
	require.NoError(t, <-done)
	_, err = os.Stat(pidPath())
	require.ErrorIs(t, err, os.ErrNotExist)
}

// Disabling stops the owned receiver and purge deletes only its local payloads.
func TestReceiverStopAndPurge(t *testing.T) {
	for _, purge := range []bool{false, true} {
		t.Run(strconv.FormatBool(purge), func(t *testing.T) {
			isolateProcess(t)
			port := freePort(t)
			done := startProcess(t, port)
			require.NoError(t, os.MkdirAll(SpoolDir(), 0700))
			file := filepath.Join(SpoolDir(), "payload.json")
			require.NoError(t, os.WriteFile(file, []byte(`{}`), 0600))
			if purge {
				require.NoError(t, Purge(context.Background(), port))
			} else {
				require.NoError(t, Stop(context.Background(), port))
			}
			require.NoError(t, <-done)
			_, err := os.Stat(file)
			if purge {
				require.ErrorIs(t, err, os.ErrNotExist)
			} else {
				require.NoError(t, err)
			}
			require.NoError(t, Stop(context.Background(), port))
		})
	}
}

// An unrelated listener or a recycled PID must never be killed by disable.
func TestUnrelatedListenerAndStalePIDAreUntouched(t *testing.T) {
	isolateProcess(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"status":"ok"}`)) }))
	defer server.Close()
	port := server.Listener.Addr().(*net.TCPAddr).Port
	_, err := Health(context.Background(), port)
	require.Error(t, err)
	require.ErrorContains(t, EnsureRunning(context.Background(), port), "unavailable")
	require.NoError(t, os.MkdirAll(StateDir(), 0700))
	require.NoError(t, fileutil.AtomicWriteJSON(pidPath(), processState{HealthInfo: HealthInfo{PID: os.Getpid(), InstanceID: "stale"}, Port: port}, 0600))
	require.NoError(t, Stop(context.Background(), port))
	response, err := http.Get(server.URL)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusOK, response.StatusCode)
}

// A matching-looking endpoint cannot authorize shutdown when process identity
// differs from the private state record.
func TestStopRefusesUnverifiedLiveReceiver(t *testing.T) {
	isolateProcess(t)
	shutdown := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/shutdown" {
			shutdown = true
		}
		_ = json.NewEncoder(w).Encode(HealthInfo{Service: "ox-trace", Version: "1", PID: os.Getpid(), InstanceID: "different"})
	}))
	defer server.Close()
	port := server.Listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, os.MkdirAll(StateDir(), 0700))
	require.NoError(t, fileutil.AtomicWriteJSON(pidPath(), processState{HealthInfo: HealthInfo{Service: "ox-trace", Version: "1", PID: os.Getpid(), InstanceID: "expected"}, Port: port, ShutdownToken: "secret"}, 0600))
	lock := processLock()
	held, err := lock.TryLock()
	require.NoError(t, err)
	require.True(t, held)
	defer lock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	require.ErrorContains(t, Stop(ctx, port), "cannot verify or stop")
	require.False(t, shutdown)
}

// Health must never forward local probes through a configured HTTP proxy or a
// redirect supplied by an unrelated local service.
func TestHealthRejectsRedirectsAndProxy(t *testing.T) {
	isolateProcess(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("unexpected redirected or proxied request")
		w.WriteHeader(http.StatusTeapot)
	}))
	defer target.Close()
	t.Setenv("HTTP_PROXY", target.URL)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer redirect.Close()
	_, err := Health(context.Background(), redirect.Listener.Addr().(*net.TCPAddr).Port)
	require.ErrorContains(t, err, "HTTP 302")
}

// Feature availability is inherited only by the spawned child, and a hook that
// notices disable after waiting for the startup lock must not spawn anything.
func TestEnsureRunningOptInAndSelfExecGuard(t *testing.T) {
	isolateProcess(t)
	port := freePort(t)
	require.NoError(t, EnsureRunningIf(context.Background(), port, func() bool { return false }))
	err := EnsureRunning(context.Background(), port)
	require.True(t, errors.Is(err, selfexec.ErrUnderTest), "%v", err)
	require.Equal(t, []string{"OTHER=1", "FEATURE_TRACE=1"}, traceChildEnv([]string{"FEATURE_TRACE=0", "OTHER=1"}))
}

func TestInvalidReceiverPorts(t *testing.T) {
	for _, port := range []int{-1, 0, 65536} {
		t.Run(strconv.Itoa(port), func(t *testing.T) {
			_, err := Health(context.Background(), port)
			require.Error(t, err)
			require.Error(t, EnsureRunning(context.Background(), port))
			require.Error(t, Run(context.Background(), port, nil))
			require.Error(t, Stop(context.Background(), port))
			require.Error(t, Purge(context.Background(), port))
		})
	}
}

// Run in a separate OS process to exercise advisory lock release on hard exit.
func TestTraceReceiverProcessHelper(t *testing.T) {
	if os.Getenv("TRACE_PROCESS_HELPER") != "1" {
		return
	}
	port, err := strconv.Atoi(os.Getenv("TRACE_PROCESS_TEST_PORT"))
	require.NoError(t, err)
	require.NoError(t, Run(context.Background(), port, nil))
}

// Crashing a receiver must release its lifetime lock even though its pidfile
// survives, allowing a fresh receiver to recover without signaling a stale PID.
func TestReceiverRecoversAfterProcessCrash(t *testing.T) {
	if testing.Short() {
		t.Skip("short: receiver subprocess crash and restart")
	}
	isolateProcess(t)
	port := freePort(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestTraceReceiverProcessHelper$", "-test.timeout=10s")
	cmd.Env = append(os.Environ(), "TRACE_PROCESS_HELPER=1", "TRACE_PROCESS_TEST_PORT="+strconv.Itoa(port)) // safe: fixed test helper only serves loopback with isolated XDG state/cache; it never invokes ox or reads credentials.
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, waitReady(ctx, port))
	info, err := Health(ctx, port)
	require.NoError(t, err)
	require.Equal(t, cmd.Process.Pid, info.PID)
	require.NoError(t, cmd.Process.Kill())
	require.Error(t, cmd.Wait())
	_, err = os.Stat(pidPath())
	require.NoError(t, err, "hard exit should leave stale state for recovery")
	done := startProcess(t, port)
	replacement, err := Health(context.Background(), port)
	require.NoError(t, err)
	require.NotEqual(t, info.InstanceID, replacement.InstanceID)
	require.NoError(t, Stop(context.Background(), port))
	require.NoError(t, <-done)
}

// A stuck startup is bounded and cannot cause repeated detached launches.
func TestStartingReceiverWaitIsBounded(t *testing.T) {
	isolateProcess(t)
	require.NoError(t, os.MkdirAll(StateDir(), 0700))
	require.NoError(t, os.WriteFile(pidPath(), []byte("incomplete stale state"), 0600))
	lock := processLock()
	held, err := lock.TryLock()
	require.NoError(t, err)
	require.True(t, held)
	defer lock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	require.ErrorContains(t, EnsureRunning(ctx, freePort(t)), "did not become ready")
}

// A broken state directory fails without binding a port or deleting payloads.
func TestProcessStateUnavailable(t *testing.T) {
	isolateProcess(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(StateDir()), 0700))
	require.NoError(t, os.WriteFile(StateDir(), []byte("not a directory"), 0600))
	port := freePort(t)
	require.ErrorContains(t, Run(context.Background(), port, nil), "create trace state directory")
	require.ErrorContains(t, EnsureRunning(context.Background(), port), "create trace state directory")
	require.ErrorContains(t, Stop(context.Background(), port), "create trace state directory")
}

// A held launch lock must honor cancellation before evaluating any opt-in or
// spawning a process, so shutdown cannot be trapped behind a hung caller.
func TestProcessStartupLockCancellation(t *testing.T) {
	isolateProcess(t)
	require.NoError(t, os.MkdirAll(StateDir(), 0700))
	lock := flock.New(filepath.Join(StateDir(), "launch.lock"))
	require.NoError(t, lock.Lock())
	defer lock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	require.ErrorContains(t, EnsureRunningIf(ctx, freePort(t), func() bool { t.Error("evaluated opt-in without lock"); return false }), "lock trace startup")
}

func TestHealthRejectsMalformedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("not JSON")) }))
	defer server.Close()
	_, err := Health(context.Background(), server.Listener.Addr().(*net.TCPAddr).Port)
	require.ErrorContains(t, err, "decode trace receiver health")
}

// Manual foreground startup must preserve another service already on the port.
func TestForegroundRefusesOccupiedPort(t *testing.T) {
	isolateProcess(t)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	require.ErrorContains(t, Run(context.Background(), listener.Addr().(*net.TCPAddr).Port, nil), "listen for traces")
}

// Predictable private paths must not follow planted symlinks or retain broad
// permissions from an older receiver installation.
func TestPrivateReceiverPathsRejectSymlinks(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "state")
	require.NoError(t, os.Mkdir(dir, 0755))
	require.NoError(t, ensurePrivateDir(dir))
	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0700), info.Mode().Perm())
	target := filepath.Join(base, "target")
	require.NoError(t, os.WriteFile(target, []byte("untouched"), 0600))
	log := filepath.Join(dir, "trace.log")
	require.NoError(t, os.Symlink(target, log))
	_, err = openPrivateLog(log)
	require.ErrorContains(t, err, "not a regular file")
	content, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "untouched", string(content))
	link := filepath.Join(base, "linked-state")
	require.NoError(t, os.Symlink(dir, link))
	require.ErrorContains(t, ensurePrivateDir(link), "not a real directory")
	require.NoError(t, os.Remove(log))
	file, err := openPrivateLog(log)
	require.NoError(t, err)
	require.NoError(t, file.Close())
	info, err = os.Stat(log)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
}
