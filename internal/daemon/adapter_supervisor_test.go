package daemon

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

// supBuildTestAdapter compiles the test adapter binary into a temp directory.
// Returns the directory containing the binary. Skips in short mode.
func supBuildTestAdapter(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("short: requires building test adapter binary")
	}

	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "ox-adapter-test")

	repoRoot := supFindRepoRoot(t)
	cmd := exec.Command("go", "build", "-o", binaryPath, "./cmd/ox-adapter-test")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0") // safe: building test binary, needs full env for go toolchain

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build test adapter: %v\n%s", err, out)
	}

	return dir
}

// supFindRepoRoot walks up from cwd to find the repo root (has go.mod).
func supFindRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (go.mod)")
		}
		dir = parent
	}
}

func supFindSessionParams(agentID string) adapterprotocol.FindSessionParams {
	return adapterprotocol.FindSessionParams{
		AgentID:  agentID,
		RepoRoot: "/tmp/test-repo",
		RepoID:   "repo-1",
		Since:    time.Now().UTC().Format(time.RFC3339),
	}
}

// supCaptureSlogOutput returns a logger that writes to a buffer and the buffer itself.
func supCaptureSlogOutput(t *testing.T) (*slog.Logger, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return logger, &buf
}

// --- A. Lazy spawn ---
// Verifies that adapter processes are only started when the first request arrives.
// Failure prevented: processes spawned eagerly waste resources and may fail before needed.

func TestSupervisor_LazySpawnOnFirstRequest(t *testing.T) {
	adapterDir := supBuildTestAdapter(t)
	sup := NewAdapterSupervisor(testLogger(), []string{adapterDir})
	defer sup.Shutdown(context.Background())

	if got := sup.ProcessCount(); got != 0 {
		t.Fatalf("expected 0 processes before request, got %d", got)
	}

	ctx := context.Background()
	resp, err := sup.SendRequest(ctx, "test", "agent-1", adapterprotocol.MethodFindSession, supFindSessionParams("agent-1"))
	if err != nil {
		t.Fatalf("first request failed: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("adapter returned error: %s: %s", resp.Error.Code, resp.Error.Message)
	}

	if got := sup.ProcessCount(); got != 1 {
		t.Fatalf("expected 1 process after request, got %d", got)
	}
}

// --- B. Session tracking ---
// Verifies that sessions are tracked and removed correctly.
// Failure prevented: leaked session state prevents idle shutdown.

func TestSupervisor_SessionTracking(t *testing.T) {
	adapterDir := supBuildTestAdapter(t)
	sup := NewAdapterSupervisor(testLogger(), []string{adapterDir})
	defer sup.Shutdown(context.Background())

	ctx := context.Background()

	for _, agentID := range []string{"agent-a", "agent-b"} {
		_, err := sup.SendRequest(ctx, "test", agentID, adapterprotocol.MethodFindSession, supFindSessionParams(agentID))
		if err != nil {
			t.Fatalf("request for %s failed: %v", agentID, err)
		}
	}

	if got := sup.ActiveSessions(); got != 2 {
		t.Fatalf("expected 2 active sessions, got %d", got)
	}

	if err := sup.EndSession("test", "agent-a"); err != nil {
		t.Fatalf("end session failed: %v", err)
	}

	if got := sup.ActiveSessions(); got != 1 {
		t.Fatalf("expected 1 active session after end, got %d", got)
	}
}

// --- C. End-session removes state ---
// Verifies that ending the last session triggers idle tracking.
// Failure prevented: process lingers forever after all sessions end.

func TestSupervisor_EndSessionRemovesState(t *testing.T) {
	adapterDir := supBuildTestAdapter(t)
	sup := NewAdapterSupervisor(testLogger(), []string{adapterDir})
	sup.idleDuration = 50 * time.Millisecond
	defer sup.Shutdown(context.Background())

	ctx := context.Background()
	_, err := sup.SendRequest(ctx, "test", "agent-x", adapterprotocol.MethodFindSession, supFindSessionParams("agent-x"))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	if err := sup.EndSession("test", "agent-x"); err != nil {
		t.Fatalf("end session failed: %v", err)
	}

	if got := sup.ActiveSessions(); got != 0 {
		t.Fatalf("expected 0 active sessions after end, got %d", got)
	}
}

// --- D. Idle shutdown ---
// Verifies that adapter processes shut down after the idle timeout when no sessions remain.
// Failure prevented: orphaned adapter processes waste system resources indefinitely.

func TestSupervisor_IdleShutdown(t *testing.T) {
	adapterDir := supBuildTestAdapter(t)
	sup := NewAdapterSupervisor(testLogger(), []string{adapterDir})
	sup.idleDuration = 100 * time.Millisecond
	defer sup.Shutdown(context.Background())

	ctx := context.Background()
	_, err := sup.SendRequest(ctx, "test", "agent-idle", adapterprotocol.MethodFindSession, supFindSessionParams("agent-idle"))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	if got := sup.ProcessCount(); got != 1 {
		t.Fatalf("expected 1 process, got %d", got)
	}

	if err := sup.EndSession("test", "agent-idle"); err != nil {
		t.Fatalf("end session failed: %v", err)
	}

	// wait for idle timer to fire
	time.Sleep(300 * time.Millisecond)

	if got := sup.ProcessCount(); got != 0 {
		t.Fatalf("expected 0 processes after idle shutdown, got %d", got)
	}
}

// --- E. Idle timer canceled by new session ---
// Verifies that a new request arriving during idle countdown prevents shutdown.
// Failure prevented: adapter killed while a new session is starting.

func TestSupervisor_IdleTimerCancelledByNewSession(t *testing.T) {
	adapterDir := supBuildTestAdapter(t)
	sup := NewAdapterSupervisor(testLogger(), []string{adapterDir})
	sup.idleDuration = 200 * time.Millisecond
	defer sup.Shutdown(context.Background())

	ctx := context.Background()

	_, err := sup.SendRequest(ctx, "test", "agent-1", adapterprotocol.MethodFindSession, supFindSessionParams("agent-1"))
	if err != nil {
		t.Fatal(err)
	}

	if err := sup.EndSession("test", "agent-1"); err != nil {
		t.Fatal(err)
	}

	// before idle fires, send a new request
	time.Sleep(50 * time.Millisecond)
	_, err = sup.SendRequest(ctx, "test", "agent-2", adapterprotocol.MethodFindSession, supFindSessionParams("agent-2"))
	if err != nil {
		t.Fatal(err)
	}

	// wait past original idle window
	time.Sleep(300 * time.Millisecond)

	// process should still be alive because new session arrived
	if got := sup.ProcessCount(); got != 1 {
		t.Fatalf("expected 1 process (idle canceled), got %d", got)
	}
}

// --- F. Shutdown sends shutdown to all processes ---
// Verifies that Shutdown cleans up all running adapter processes.
// Failure prevented: zombie adapter processes after daemon exits.

func TestSupervisor_ShutdownAll(t *testing.T) {
	adapterDir := supBuildTestAdapter(t)
	sup := NewAdapterSupervisor(testLogger(), []string{adapterDir})

	ctx := context.Background()
	_, err := sup.SendRequest(ctx, "test", "agent-s", adapterprotocol.MethodFindSession, supFindSessionParams("agent-s"))
	if err != nil {
		t.Fatal(err)
	}

	if got := sup.ProcessCount(); got != 1 {
		t.Fatalf("expected 1 process, got %d", got)
	}

	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sup.Shutdown(shutCtx); err != nil {
		t.Fatalf("shutdown failed: %v", err)
	}

	if got := sup.ProcessCount(); got != 0 {
		t.Fatalf("expected 0 processes after shutdown, got %d", got)
	}
}

// --- G. Requests after shutdown return error ---
// Verifies that the supervisor rejects requests after shutdown.
// Failure prevented: spawning new processes after daemon is shutting down.

func TestSupervisor_RequestAfterShutdown(t *testing.T) {
	adapterDir := supBuildTestAdapter(t)
	sup := NewAdapterSupervisor(testLogger(), []string{adapterDir})
	sup.Shutdown(context.Background())

	_, err := sup.SendRequest(context.Background(), "test", "agent-x", adapterprotocol.MethodFindSession, nil)
	if err == nil {
		t.Fatal("expected error after shutdown, got nil")
	}
	if err != ErrSupervisorShutdown {
		t.Fatalf("expected ErrSupervisorShutdown, got: %v", err)
	}
}

// --- H. Binary not found ---
// Verifies that requesting an unknown adapter type returns a clear error.
// Failure prevented: confusing error messages when adapter binaries are missing.

func TestSupervisor_BinaryNotFound(t *testing.T) {
	sup := NewAdapterSupervisor(testLogger(), []string{t.TempDir()})
	defer sup.Shutdown(context.Background())

	_, err := sup.SendRequest(context.Background(), "nonexistent", "agent-1", "find-session", nil)
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected 'not found' error, got: %v", err)
	}
}

// --- I. Multiple adapter types run independently ---
// Verifies that different adapter types get separate processes.
// Failure prevented: adapter types sharing a process and interfering with each other.

func TestSupervisor_MultipleAdapterTypes(t *testing.T) {
	adapterDir := supBuildTestAdapter(t)

	// symlink simulates a second adapter type
	originalBinary := filepath.Join(adapterDir, "ox-adapter-test")
	secondBinary := filepath.Join(adapterDir, "ox-adapter-test2")
	if err := os.Symlink(originalBinary, secondBinary); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	sup := NewAdapterSupervisor(testLogger(), []string{adapterDir})
	defer sup.Shutdown(context.Background())

	ctx := context.Background()
	for _, adapterType := range []string{"test", "test2"} {
		_, err := sup.SendRequest(ctx, adapterType, "agent-1", adapterprotocol.MethodFindSession, supFindSessionParams("agent-1"))
		if err != nil {
			t.Fatalf("request to %s failed: %v", adapterType, err)
		}
	}

	if got := sup.ProcessCount(); got != 2 {
		t.Fatalf("expected 2 processes for 2 adapter types, got %d", got)
	}
}

// --- J. Crash detection and respawn ---
// Verifies that crashed adapters are detected and respawned up to the limit.
// Failure prevented: a single crash permanently disabling an adapter type.

func TestSupervisor_CrashAndRespawn(t *testing.T) {
	adapterDir := supBuildTestAdapter(t)
	sup := NewAdapterSupervisor(testLogger(), []string{adapterDir})
	defer sup.Shutdown(context.Background())

	ctx := context.Background()

	// make the adapter crash after first read-from-offset
	t.Setenv("OX_TEST_CRASH_AFTER", "1")

	// find-session succeeds before crash
	_, err := sup.SendRequest(ctx, "test", "agent-crash", adapterprotocol.MethodFindSession, supFindSessionParams("agent-crash"))
	if err != nil {
		t.Fatalf("find-session failed: %v", err)
	}

	// read-from-offset triggers crash
	_, err = sup.SendRequest(ctx, "test", "agent-crash", adapterprotocol.MethodReadFromOffset, adapterprotocol.ReadFromOffsetParams{
		AgentID:     "agent-crash",
		SessionFile: "/tmp/test",
		Offset:      0,
	})
	if err == nil {
		t.Log("read-from-offset succeeded (adapter may have responded before crashing)")
	}

	// clear crash env so respawned adapter works
	t.Setenv("OX_TEST_CRASH_AFTER", "")

	// next request triggers respawn
	resp, err := sup.SendRequest(ctx, "test", "agent-crash", adapterprotocol.MethodFindSession, supFindSessionParams("agent-crash"))
	if err != nil {
		t.Fatalf("request after respawn failed: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("respawned adapter returned error: %s", resp.Error.Message)
	}
}

// --- K. Respawn limit ---
// Verifies that adapters that crash repeatedly are not respawned indefinitely.
// Failure prevented: infinite respawn loop consuming resources.

func TestSupervisor_RespawnLimit(t *testing.T) {
	sup := NewAdapterSupervisor(testLogger(), []string{t.TempDir()})
	defer sup.Shutdown(context.Background())

	// manually inject a process at the respawn limit
	sup.mu.Lock()
	sup.processes["bad-adapter"] = &AdapterProcess{
		adapterType:  "bad-adapter",
		sessions:     make(map[string]bool),
		crashed:      true,
		respawnCount: maxRespawnAttempts,
	}
	sup.mu.Unlock()

	_, err := sup.SendRequest(context.Background(), "bad-adapter", "agent-1", "find-session", nil)
	if err == nil {
		t.Fatal("expected error at respawn limit")
	}
	if !strings.Contains(err.Error(), "exceeded max respawn") {
		t.Fatalf("expected respawn limit error, got: %v", err)
	}
}

// --- L. Concurrent requests to same adapter ---
// Verifies that concurrent requests are serialized per process without deadlock.
// Failure prevented: deadlock or data corruption under concurrent use.

func TestSupervisor_ConcurrentRequests(t *testing.T) {
	adapterDir := supBuildTestAdapter(t)
	sup := NewAdapterSupervisor(testLogger(), []string{adapterDir})
	defer sup.Shutdown(context.Background())

	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 5)

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			agentID := fmt.Sprintf("agent-%d", n)
			_, err := sup.SendRequest(ctx, "test", agentID, adapterprotocol.MethodFindSession, supFindSessionParams(agentID))
			if err != nil {
				errs <- fmt.Errorf("agent-%d: %w", n, err)
			}
		}(i)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}

	if got := sup.ProcessCount(); got != 1 {
		t.Fatalf("expected 1 process, got %d", got)
	}
}

// --- M. EndSession on nonexistent adapter type is a no-op ---
// Failure prevented: panic or error when ending a session for an adapter that was never started.

func TestSupervisor_EndSessionNonexistent(t *testing.T) {
	sup := NewAdapterSupervisor(testLogger(), []string{t.TempDir()})
	defer sup.Shutdown(context.Background())

	err := sup.EndSession("nonexistent", "agent-1")
	if err != nil {
		t.Fatalf("expected nil error for nonexistent adapter, got: %v", err)
	}
}

// --- N. Lifecycle events: session started/ended ---
// Verifies that structured lifecycle logs are emitted for session start and end.
// Failure prevented: adapter sessions invisible to operators debugging issues.

func TestSupervisor_LifecycleEventsSessionStartEnd(t *testing.T) {
	adapterDir := supBuildTestAdapter(t)
	logger, buf := supCaptureSlogOutput(t)
	sup := NewAdapterSupervisor(logger, []string{adapterDir})
	defer sup.Shutdown(context.Background())

	ctx := context.Background()

	// session start triggers lifecycle log
	_, err := sup.SendRequest(ctx, "test", "agent-lifecycle", adapterprotocol.MethodFindSession, supFindSessionParams("agent-lifecycle"))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	logOutput := buf.String()
	if !strings.Contains(logOutput, "adapter session started") {
		t.Errorf("expected 'adapter session started' log, got:\n%s", logOutput)
	}
	if !strings.Contains(logOutput, "agent-lifecycle") {
		t.Errorf("expected agent_id in log output, got:\n%s", logOutput)
	}

	// duplicate request for same agent should NOT emit a second "session started"
	beforeLen := buf.Len()
	_, err = sup.SendRequest(ctx, "test", "agent-lifecycle", adapterprotocol.MethodFindSession, supFindSessionParams("agent-lifecycle"))
	if err != nil {
		t.Fatalf("second request failed: %v", err)
	}
	afterLog := buf.String()[beforeLen:]
	if strings.Contains(afterLog, "adapter session started") {
		t.Errorf("duplicate 'adapter session started' log for same agent:\n%s", afterLog)
	}

	// end session triggers lifecycle log
	if err := sup.EndSession("test", "agent-lifecycle"); err != nil {
		t.Fatalf("end session failed: %v", err)
	}

	logOutput = buf.String()
	if !strings.Contains(logOutput, "adapter session ended") {
		t.Errorf("expected 'adapter session ended' log, got:\n%s", logOutput)
	}
}

// --- O. Lifecycle events: spawn and shutdown ---
// Verifies that structured lifecycle logs are emitted for spawn, shutdown, and idle shutdown.
// Failure prevented: adapter process lifecycle invisible to operators.

func TestSupervisor_LifecycleEventsSpawnAndShutdown(t *testing.T) {
	adapterDir := supBuildTestAdapter(t)
	logger, buf := supCaptureSlogOutput(t)
	sup := NewAdapterSupervisor(logger, []string{adapterDir})

	ctx := context.Background()
	_, err := sup.SendRequest(ctx, "test", "agent-spawn", adapterprotocol.MethodFindSession, supFindSessionParams("agent-spawn"))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	logOutput := buf.String()
	if !strings.Contains(logOutput, "adapter spawned") {
		t.Errorf("expected 'adapter spawned' log, got:\n%s", logOutput)
	}

	// shutdown triggers "adapter shutdown" with reason "daemon-stop"
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sup.Shutdown(shutCtx); err != nil {
		t.Fatalf("shutdown failed: %v", err)
	}

	logOutput = buf.String()
	if !strings.Contains(logOutput, "adapter shutdown") {
		t.Errorf("expected 'adapter shutdown' log, got:\n%s", logOutput)
	}
	if !strings.Contains(logOutput, "daemon-stop") {
		t.Errorf("expected 'daemon-stop' reason in shutdown log, got:\n%s", logOutput)
	}
}

// --- P. Lifecycle events: degraded state ---
// Verifies that the "adapter degraded" warning is logged when respawn limit is exceeded.
// Failure prevented: degraded adapters silently failing without operator visibility.

func TestSupervisor_LifecycleEventsDegraded(t *testing.T) {
	logger, buf := supCaptureSlogOutput(t)
	sup := NewAdapterSupervisor(logger, []string{t.TempDir()})
	defer sup.Shutdown(context.Background())

	// inject a process at the respawn limit to trigger degraded state
	sup.mu.Lock()
	sup.processes["failing-adapter"] = &AdapterProcess{
		adapterType:  "failing-adapter",
		sessions:     make(map[string]bool),
		crashed:      true,
		respawnCount: maxRespawnAttempts,
		lastActivity: time.Now(),
	}
	sup.mu.Unlock()

	_, _ = sup.SendRequest(context.Background(), "failing-adapter", "agent-1", "find-session", nil)

	logOutput := buf.String()
	if !strings.Contains(logOutput, "adapter degraded") {
		t.Errorf("expected 'adapter degraded' log, got:\n%s", logOutput)
	}
	if !strings.Contains(logOutput, "max_respawns_exceeded") {
		t.Errorf("expected 'max_respawns_exceeded' reason in degraded log, got:\n%s", logOutput)
	}
}

// --- Q. Stderr ring buffer captures output ---
// Verifies that the ring buffer captures and rotates stderr lines correctly.
// Failure prevented: no stderr available when debugging adapter crashes.

func TestStderrRingBuffer_Basic(t *testing.T) {
	ring := newStderrRingBuffer(3)

	ring.Write([]byte("line1\nline2\nline3\n"))

	lines := ring.Lines()
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %v", len(lines), lines)
	}
	if lines[0] != "line1" || lines[1] != "line2" || lines[2] != "line3" {
		t.Fatalf("unexpected lines: %v", lines)
	}
}

func TestStderrRingBuffer_Rotation(t *testing.T) {
	ring := newStderrRingBuffer(2)

	ring.Write([]byte("line1\nline2\nline3\nline4\n"))

	lines := ring.Lines()
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines after rotation, got %d: %v", len(lines), lines)
	}
	// should keep the last 2 lines
	if lines[0] != "line3" || lines[1] != "line4" {
		t.Fatalf("expected [line3 line4], got %v", lines)
	}
}

func TestStderrRingBuffer_PartialLine(t *testing.T) {
	ring := newStderrRingBuffer(5)

	ring.Write([]byte("complete\npartial"))

	lines := ring.Lines()
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines (including partial), got %d: %v", len(lines), lines)
	}
	if lines[0] != "complete" || lines[1] != "partial" {
		t.Fatalf("unexpected lines: %v", lines)
	}
}

func TestStderrRingBuffer_IncrementalWrites(t *testing.T) {
	ring := newStderrRingBuffer(5)

	// simulate chunked stderr output
	ring.Write([]byte("hel"))
	ring.Write([]byte("lo\nwor"))
	ring.Write([]byte("ld\n"))

	lines := ring.Lines()
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %v", len(lines), lines)
	}
	if lines[0] != "hello" || lines[1] != "world" {
		t.Fatalf("unexpected lines: %v", lines)
	}
}

func TestStderrRingBuffer_WindowsLineEndings(t *testing.T) {
	ring := newStderrRingBuffer(5)

	ring.Write([]byte("line1\r\nline2\r\n"))

	lines := ring.Lines()
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %v", len(lines), lines)
	}
	if lines[0] != "line1" || lines[1] != "line2" {
		t.Fatalf("unexpected lines (should strip \\r): %v", lines)
	}
}

// --- R. Status returns correct state for running adapters ---
// Failure prevented: status queries returning stale or missing adapter info.

func TestSupervisor_StatusRunning(t *testing.T) {
	adapterDir := supBuildTestAdapter(t)
	sup := NewAdapterSupervisor(testLogger(), []string{adapterDir})
	defer sup.Shutdown(context.Background())

	// empty status before any spawns
	status := sup.Status()
	if len(status) != 0 {
		t.Fatalf("expected empty status before spawning, got %d entries", len(status))
	}

	ctx := context.Background()
	_, err := sup.SendRequest(ctx, "test", "agent-status", adapterprotocol.MethodFindSession, supFindSessionParams("agent-status"))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	status = sup.Status()
	if len(status) != 1 {
		t.Fatalf("expected 1 status entry, got %d", len(status))
	}

	st, ok := status["test"]
	if !ok {
		t.Fatal("expected 'test' adapter in status")
	}
	if st.State != "running" {
		t.Errorf("expected state 'running', got %q", st.State)
	}
	if st.PID == 0 {
		t.Error("expected non-zero PID")
	}
	if len(st.Sessions) != 1 || st.Sessions[0] != "agent-status" {
		t.Errorf("expected sessions [agent-status], got %v", st.Sessions)
	}
	if st.RespawnCount != 0 {
		t.Errorf("expected respawn_count 0, got %d", st.RespawnCount)
	}
	if st.LastActivity.IsZero() {
		t.Error("expected non-zero last_activity")
	}
}

// --- S. Status returns correct state for degraded adapters ---
// Failure prevented: degraded adapters invisible in status output after being removed from process map.

func TestSupervisor_StatusDegraded(t *testing.T) {
	sup := NewAdapterSupervisor(testLogger(), []string{t.TempDir()})
	defer sup.Shutdown(context.Background())

	// inject a process at the respawn limit
	sup.mu.Lock()
	sup.processes["degraded-type"] = &AdapterProcess{
		adapterType:  "degraded-type",
		sessions:     make(map[string]bool),
		crashed:      true,
		respawnCount: maxRespawnAttempts,
		lastActivity: time.Now(),
		stderrRing:   newStderrRingBuffer(5),
	}
	// write some stderr before it gets removed
	sup.processes["degraded-type"].stderrRing.Write([]byte("error: something broke\n"))
	sup.mu.Unlock()

	// trigger handleCrash which should record degraded state
	_, _ = sup.SendRequest(context.Background(), "degraded-type", "agent-1", "find-session", nil)

	status := sup.Status()
	st, ok := status["degraded-type"]
	if !ok {
		t.Fatal("expected 'degraded-type' in status after hitting respawn limit")
	}
	if st.State != "degraded" {
		t.Errorf("expected state 'degraded', got %q", st.State)
	}
	if st.RespawnCount != maxRespawnAttempts {
		t.Errorf("expected respawn_count %d, got %d", maxRespawnAttempts, st.RespawnCount)
	}
	if len(st.StderrTail) == 0 {
		t.Error("expected stderr_tail to be captured for degraded adapter")
	}
}

// --- T. Status returns correct state for crashed adapters ---
// Failure prevented: crashed adapter state not visible before respawn attempt.

func TestSupervisor_StatusCrashed(t *testing.T) {
	sup := NewAdapterSupervisor(testLogger(), []string{t.TempDir()})
	defer sup.Shutdown(context.Background())

	// inject a crashed process (not yet at respawn limit)
	ring := newStderrRingBuffer(5)
	ring.Write([]byte("segfault\n"))

	sup.mu.Lock()
	sup.processes["crash-type"] = &AdapterProcess{
		adapterType:  "crash-type",
		sessions:     map[string]bool{"agent-a": true},
		crashed:      true,
		respawnCount: 1,
		lastActivity: time.Now(),
		stderrRing:   ring,
	}
	sup.mu.Unlock()

	status := sup.Status()
	st, ok := status["crash-type"]
	if !ok {
		t.Fatal("expected 'crash-type' in status")
	}
	if st.State != "crashed" {
		t.Errorf("expected state 'crashed', got %q", st.State)
	}
	if len(st.StderrTail) != 1 || st.StderrTail[0] != "segfault" {
		t.Errorf("expected stderr_tail [segfault], got %v", st.StderrTail)
	}
	if len(st.Sessions) != 1 {
		t.Errorf("expected 1 session in crashed status, got %d", len(st.Sessions))
	}
}

// --- U. StderrTail returns lines for active and degraded adapters ---
// Failure prevented: stderr output lost when adapter transitions between states.

func TestSupervisor_StderrTail(t *testing.T) {
	sup := NewAdapterSupervisor(testLogger(), []string{t.TempDir()})
	defer sup.Shutdown(context.Background())

	// inject a running process with stderr
	ring := newStderrRingBuffer(5)
	ring.Write([]byte("warning: something\nerror: fatal\n"))

	sup.mu.Lock()
	sup.processes["stderr-type"] = &AdapterProcess{
		adapterType:  "stderr-type",
		sessions:     make(map[string]bool),
		lastActivity: time.Now(),
		stderrRing:   ring,
	}
	sup.mu.Unlock()

	lines := sup.StderrTail("stderr-type")
	if len(lines) != 2 {
		t.Fatalf("expected 2 stderr lines, got %d: %v", len(lines), lines)
	}

	// nonexistent type returns nil
	lines = sup.StderrTail("nonexistent")
	if lines != nil {
		t.Fatalf("expected nil for nonexistent type, got %v", lines)
	}
}

// --- V. Lifecycle events: respawn logged ---
// Verifies that respawn attempts emit structured "adapter respawning" logs.
// Failure prevented: respawn activity invisible during crash recovery.

func TestSupervisor_LifecycleEventsRespawn(t *testing.T) {
	adapterDir := supBuildTestAdapter(t)
	logger, buf := supCaptureSlogOutput(t)
	sup := NewAdapterSupervisor(logger, []string{adapterDir})
	defer sup.Shutdown(context.Background())

	ctx := context.Background()

	// make the adapter crash after first read-from-offset
	t.Setenv("OX_TEST_CRASH_AFTER", "1")

	_, err := sup.SendRequest(ctx, "test", "agent-respawn", adapterprotocol.MethodFindSession, supFindSessionParams("agent-respawn"))
	if err != nil {
		t.Fatalf("find-session failed: %v", err)
	}

	// trigger crash
	_, _ = sup.SendRequest(ctx, "test", "agent-respawn", adapterprotocol.MethodReadFromOffset, adapterprotocol.ReadFromOffsetParams{
		AgentID:     "agent-respawn",
		SessionFile: "/tmp/test",
		Offset:      0,
	})

	// clear crash env so respawned adapter works
	t.Setenv("OX_TEST_CRASH_AFTER", "")

	// trigger respawn
	_, _ = sup.SendRequest(ctx, "test", "agent-respawn", adapterprotocol.MethodFindSession, supFindSessionParams("agent-respawn"))

	logOutput := buf.String()
	if !strings.Contains(logOutput, "adapter respawning") {
		t.Errorf("expected 'adapter respawning' log, got:\n%s", logOutput)
	}
	if !strings.Contains(logOutput, "adapter crashed") {
		t.Errorf("expected 'adapter crashed' log, got:\n%s", logOutput)
	}
}
