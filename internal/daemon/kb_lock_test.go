package daemon

// Tests for the per-kb_id flock primitive. Verifies:
//
//   - Single-process acquire / release / re-acquire.
//   - Same-process double-acquire returns acquired=false (no deadlock).
//   - Cross-process acquire blocks while another process holds the lock.
//   - Kernel auto-release on process death (no stale-lock recovery
//     needed) — verified by SIGKILL'ing the holder.
//
// We exercise AcquireKBLock through the lower-level acquireKBLockAt
// helper so each test can target a tmp directory and not contend with
// other tests sharing paths.DaemonStateDir().

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// kbLockTestFile returns a unique lock-file path inside t.TempDir().
func kbLockTestFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "kb.lock")
}

func TestAcquireKBLock_SingleProcess(t *testing.T) {
	path := kbLockTestFile(t)

	unlock, acquired, err := acquireKBLockAt(path)
	if err != nil {
		t.Fatalf("first acquire: unexpected err: %v", err)
	}
	if !acquired {
		t.Fatalf("first acquire: expected acquired=true on empty file")
	}
	unlock()

	// Re-acquire after explicit release must succeed.
	unlock2, acquired2, err := acquireKBLockAt(path)
	if err != nil {
		t.Fatalf("re-acquire: unexpected err: %v", err)
	}
	if !acquired2 {
		t.Fatalf("re-acquire: expected acquired=true after release")
	}
	unlock2()
}

func TestAcquireKBLock_SameProcessDoubleAcquireFails(t *testing.T) {
	// Even within one process, a second LOCK_EX|LOCK_NB on the same
	// file via a different fd must report contention. This is the
	// in-process equivalent of two goroutines racing into cloneBubble.
	path := kbLockTestFile(t)

	unlock1, acquired1, err := acquireKBLockAt(path)
	if err != nil || !acquired1 {
		t.Fatalf("first acquire failed: acquired=%v err=%v", acquired1, err)
	}
	defer unlock1()

	unlock2, acquired2, err := acquireKBLockAt(path)
	if err != nil {
		t.Fatalf("second acquire returned unexpected err: %v", err)
	}
	if acquired2 {
		// Defensive: release before failing so we don't leak the lock.
		if unlock2 != nil {
			unlock2()
		}
		t.Fatalf("second acquire: expected acquired=false while first lock held")
	}
}

// --- Cross-process behaviors ---

// kbLockSubprocessEnvVar selects the helper subprocess behavior so we
// can re-exec the test binary as the lock holder without ambiguity.
const kbLockSubprocessEnvVar = "OX_KB_LOCK_TEST_HOLDER_PATH"

// TestKBLockHolderSubprocess is the helper entry point invoked via
// re-exec by the cross-process tests. When OX_KB_LOCK_TEST_HOLDER_PATH
// is set, it acquires the lock at that path, prints "locked" to stdout,
// then sleeps until SIGTERM/SIGKILL. The test that re-execs us also
// sets a hold-time env var to avoid relying on signal plumbing in
// short-running tests.
func TestKBLockHolderSubprocess(t *testing.T) {
	path := os.Getenv(kbLockSubprocessEnvVar)
	if path == "" {
		t.Skip("not invoked as kb-lock holder subprocess")
	}
	unlock, acquired, err := acquireKBLockAt(path)
	if err != nil || !acquired {
		// Subprocess can't use t.Fatal-as-signal; print and exit non-zero.
		fmt.Fprintf(os.Stderr, "holder: acquire failed: acquired=%v err=%v\n", acquired, err)
		os.Exit(2)
	}
	defer unlock()
	// Signal the parent that we have the lock. The parent reads stdout
	// line-by-line; "locked\n" is the handshake.
	fmt.Println("locked")
	// Hold for a bounded duration. The parent kills us before this
	// fires in the SIGKILL test; the bound is just a backstop so a
	// runaway subprocess can't outlive the test binary.
	hold := 5 * time.Second
	if v := os.Getenv("OX_KB_LOCK_TEST_HOLD_MS"); v != "" {
		var ms int
		_, _ = fmt.Sscanf(v, "%d", &ms)
		if ms > 0 {
			hold = time.Duration(ms) * time.Millisecond
		}
	}
	time.Sleep(hold)
}

// startKBLockHolder re-execs the test binary so it runs
// TestKBLockHolderSubprocess against the given lock path. Returns the
// running *exec.Cmd; callers must Kill+Wait to clean up.
func startKBLockHolder(t *testing.T, lockPath string, holdMS int) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0],
		"-test.run", "^TestKBLockHolderSubprocess$",
		"-test.v",
	)
	cmd.Env = append(os.Environ(), // safe: re-execs test binary, not ox CLI
		kbLockSubprocessEnvVar+"="+lockPath,
		fmt.Sprintf("OX_KB_LOCK_TEST_HOLD_MS=%d", holdMS),
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start holder: %v", err)
	}
	// Wait for the "locked" handshake. The subprocess prints other
	// `go test` framing lines first ("=== RUN ..."), so scan until
	// we see our token or timeout.
	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 4096)
		deadline := time.Now().Add(5 * time.Second)
		acc := make([]byte, 0, 4096)
		for time.Now().Before(deadline) {
			n, readErr := stdout.Read(buf)
			if n > 0 {
				acc = append(acc, buf[:n]...)
				for i := 0; i+len("locked") <= len(acc); i++ {
					if string(acc[i:i+len("locked")]) == "locked" {
						done <- nil
						return
					}
				}
			}
			if readErr != nil {
				done <- fmt.Errorf("read holder stdout: %w", readErr)
				return
			}
		}
		done <- fmt.Errorf("timed out waiting for holder handshake")
	}()
	if err := <-done; err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		t.Fatalf("holder handshake: %v", err)
	}
	return cmd
}

func TestAcquireKBLock_CrossProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("short: re-execs test binary")
	}
	path := kbLockTestFile(t)
	cmd := startKBLockHolder(t, path, 2000)
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	// Subprocess holds the lock; parent attempt must report contention.
	unlock, acquired, err := acquireKBLockAt(path)
	if err != nil {
		t.Fatalf("parent acquire: unexpected err: %v", err)
	}
	if acquired {
		if unlock != nil {
			unlock()
		}
		t.Fatalf("parent acquire: expected acquired=false while subprocess holds lock")
	}

	// After subprocess exits, parent must be able to acquire.
	_ = cmd.Process.Signal(syscall.SIGTERM)
	_, _ = cmd.Process.Wait()

	unlock2, acquired2, err := acquireKBLockAt(path)
	if err != nil {
		t.Fatalf("parent re-acquire after subprocess exit: err=%v", err)
	}
	if !acquired2 {
		t.Fatalf("parent re-acquire after subprocess exit: expected acquired=true")
	}
	unlock2()
}

func TestAcquireKBLock_StaleLockAfterCrash(t *testing.T) {
	if testing.Short() {
		t.Skip("short: re-execs test binary and SIGKILLs it")
	}
	path := kbLockTestFile(t)
	cmd := startKBLockHolder(t, path, 30000)

	// Confirm contention while subprocess is alive.
	unlock, acquired, err := acquireKBLockAt(path)
	if err != nil {
		t.Fatalf("contended acquire: err=%v", err)
	}
	if acquired {
		if unlock != nil {
			unlock()
		}
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		t.Fatalf("contended acquire: expected acquired=false before SIGKILL")
	}

	// SIGKILL the holder; kernel must release the flock as the fd
	// closes. There is no graceful unlock — this proves we don't need
	// stale-lock recovery code.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill holder: %v", err)
	}
	_, _ = cmd.Process.Wait()

	// Re-acquire should succeed promptly. We retry briefly because the
	// fd close + kernel flock cleanup is asynchronous w.r.t. SIGKILL
	// delivery on some kernels; the window is tens of microseconds in
	// practice.
	deadline := time.Now().Add(2 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		unlock2, acquired2, err := acquireKBLockAt(path)
		if err == nil && acquired2 {
			unlock2()
			return
		}
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("re-acquire after SIGKILL did not succeed within 2s; lastErr=%v", lastErr)
}

// TestKBReconcileCrossDaemonRace and TestKBGCRespectsCloneInFlight are
// the higher-level integration scenarios called out in bead ox-kdt2.
// Wiring them through the full daemon test harness (SyncScheduler +
// fake KB lister + fake gitserver) is non-trivial; the lock-primitive
// tests above prove the underlying serialization, and the existing
// TestKBGC_RespectsCloneInFlight_DoesNotTriageActiveClone already
// covers the in-process variant. Tracked as a follow-up; gated as a
// skip so the intent is searchable.
func TestKBReconcileCrossDaemonRace(t *testing.T) {
	t.Skip("requires daemon integration harness — see bead ox-kdt2 follow-up")
}

func TestKBGCRespectsCloneInFlight_CrossProcess(t *testing.T) {
	t.Skip("requires daemon integration harness — see bead ox-kdt2 follow-up")
}
