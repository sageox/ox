package daemon

// Cross-process verification of the per-(user, endpoint) global-sync
// flock lease introduced for ox-6zme. The lease uses the same POSIX
// flock(2) primitive as kb_lock.go, so the test surface mirrors
// kb_lock_test.go: in-process contention, cross-endpoint independence,
// and a subprocess-based owner-death failover.
//
// All tests redirect the lease directory via t.Setenv on XDG_STATE_HOME
// (paths.DaemonStateDir resolves under StateDir → XDG_STATE_HOME) so the
// developer's real ~/.local/state/ox state is never touched.

import (
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// withIsolatedLeaseDir redirects paths.DaemonStateDir → tmp dir via
// XDG_STATE_HOME for the duration of the test. Also resets the
// package-level globalLeaseDirOnce so a fresh per-test directory is
// created instead of reusing a cached one from a previous test.
func withIsolatedLeaseDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	// reset memoized state so this test sees the redirected XDG path.
	globalLeaseDirOnce = sync.Once{}
	globalLeaseDirCached = ""
	globalLeaseDirErr = nil
	t.Cleanup(func() {
		// reset again after the test so the next test gets its own dir.
		globalLeaseDirOnce = sync.Once{}
		globalLeaseDirCached = ""
		globalLeaseDirErr = nil
	})
	return dir
}

// TestGlobalLeaseAcquireRelease verifies a single daemon can acquire,
// release, and re-acquire the lease for an endpoint. This is the
// "steady state on a single host" path.
func TestGlobalLeaseAcquireRelease(t *testing.T) {
	withIsolatedLeaseDir(t)

	l, err := AcquireGlobalSyncLease("sageox.ai")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if !l.IsHeld() {
		t.Fatal("lease should be held after acquire")
	}
	if l.Endpoint() != "sageox.ai" {
		t.Errorf("endpoint = %q, want sageox.ai", l.Endpoint())
	}

	if err := l.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if l.IsHeld() {
		t.Error("lease should not be held after release")
	}

	// Release is idempotent — second call must not error.
	if err := l.Release(); err != nil {
		t.Errorf("second release: %v", err)
	}

	// Re-acquire after release succeeds.
	l2, err := AcquireGlobalSyncLease("sageox.ai")
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	defer l2.Release()
	if !l2.IsHeld() {
		t.Error("re-acquired lease should be held")
	}
}

// TestGlobalLeaseInProcessContention verifies that a second acquire on
// the same endpoint while the first is still held returns ErrNotOwner.
// flock locks are per-open-file-description, so two opens of the same
// path in the same process still contend correctly.
func TestGlobalLeaseInProcessContention(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows lease backend is a no-op (ADR-017 §5)")
	}
	withIsolatedLeaseDir(t)

	owner, err := AcquireGlobalSyncLease("sageox.ai")
	if err != nil {
		t.Fatalf("owner acquire: %v", err)
	}
	defer owner.Release()

	follower, err := AcquireGlobalSyncLease("sageox.ai")
	if !errors.Is(err, ErrNotOwner) {
		t.Fatalf("follower err = %v, want ErrNotOwner", err)
	}
	if follower != nil {
		t.Error("follower lease should be nil on contention")
	}

	// Release owner — follower can now acquire.
	if err := owner.Release(); err != nil {
		t.Fatalf("owner release: %v", err)
	}
	follower2, err := AcquireGlobalSyncLease("sageox.ai")
	if err != nil {
		t.Fatalf("follower re-acquire after owner release: %v", err)
	}
	defer follower2.Release()
}

// TestGlobalLeasePerEndpoint verifies the lease is scoped per-endpoint:
// two daemons against different endpoints can both hold a lease at the
// same time. This is the multi-environment case (user logged into
// sageox.ai for $work and localhost for $devtest).
func TestGlobalLeasePerEndpoint(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows lease backend is a no-op (ADR-017 §5)")
	}
	withIsolatedLeaseDir(t)

	prod, err := AcquireGlobalSyncLease("sageox.ai")
	if err != nil {
		t.Fatalf("prod acquire: %v", err)
	}
	defer prod.Release()

	staging, err := AcquireGlobalSyncLease("staging.sageox.ai")
	if err != nil {
		t.Fatalf("staging acquire: %v", err)
	}
	defer staging.Release()

	local, err := AcquireGlobalSyncLease("localhost:8080")
	if err != nil {
		t.Fatalf("local acquire: %v", err)
	}
	defer local.Release()

	if !prod.IsHeld() || !staging.IsHeld() || !local.IsHeld() {
		t.Error("all three per-endpoint leases should be held simultaneously")
	}
}

func TestSyncScheduler_ReacquiresGlobalLeaseAfterOwnerExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows lease backend is a no-op (ADR-017 §5)")
	}
	withIsolatedLeaseDir(t)

	owner, err := AcquireGlobalSyncLease("sageox.ai")
	if err != nil {
		t.Fatalf("owner acquire: %v", err)
	}

	s := NewSyncScheduler(DefaultConfig(), nil)
	s.SetGlobalSyncLease("sageox.ai", nil)
	if s.IsGlobalSyncOwner() {
		t.Fatal("scheduler should remain follower while another daemon owns the lease")
	}

	if err := owner.Release(); err != nil {
		t.Fatalf("owner release: %v", err)
	}
	s.logger = slog.Default()
	if !s.IsGlobalSyncOwner() {
		t.Fatal("scheduler should acquire the lease once the previous owner exits")
	}
	s.ReleaseGlobalSyncLease()
}

// TestGlobalLeaseNormalizesEndpoint verifies prefix-equivalent endpoints
// (api.sageox.ai vs sageox.ai vs www.sageox.ai) collapse to the same
// lease file. Without this, a daemon configured with api.sageox.ai and
// another with sageox.ai would both think they own the lease.
func TestGlobalLeaseNormalizesEndpoint(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows lease backend is a no-op (ADR-017 §5)")
	}
	withIsolatedLeaseDir(t)

	owner, err := AcquireGlobalSyncLease("api.sageox.ai")
	if err != nil {
		t.Fatalf("acquire api.sageox.ai: %v", err)
	}
	defer owner.Release()

	// sageox.ai normalizes to the same slug — should contend.
	_, err = AcquireGlobalSyncLease("sageox.ai")
	if !errors.Is(err, ErrNotOwner) {
		t.Errorf("sageox.ai should contend with api.sageox.ai; got err = %v", err)
	}

	// www.sageox.ai also normalizes to sageox.ai.
	_, err = AcquireGlobalSyncLease("www.sageox.ai")
	if !errors.Is(err, ErrNotOwner) {
		t.Errorf("www.sageox.ai should contend with api.sageox.ai; got err = %v", err)
	}

	// https://app.sageox.ai/v1 normalizes to the same slug too.
	_, err = AcquireGlobalSyncLease("https://app.sageox.ai/v1")
	if !errors.Is(err, ErrNotOwner) {
		t.Errorf("https://app.sageox.ai/v1 should contend; got err = %v", err)
	}
}

// TestGlobalLeaseEmptyEndpoint guards against accidental flock of a
// "default" path when the daemon is misconfigured. Empty endpoint must
// return an error rather than silently succeed.
func TestGlobalLeaseEmptyEndpoint(t *testing.T) {
	withIsolatedLeaseDir(t)
	l, err := AcquireGlobalSyncLease("")
	if err == nil {
		t.Error("empty endpoint should error")
		_ = l.Release()
	}
}

// TestGlobalLeaseSubprocessFailover verifies that when the owning daemon
// process dies (here: a child subprocess that exits while holding the
// lock), the next acquire from the parent succeeds. The kernel auto-
// releases flock on process termination, so no explicit hand-off is
// needed.
func TestGlobalLeaseSubprocessFailover(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows lease backend is a no-op (ADR-017 §5)")
	}
	dir := withIsolatedLeaseDir(t)

	// The subprocess invokes the same test binary with a special env
	// var that triggers the holder loop in TestMain → holdLease branch.
	// Standard Go subprocess pattern (see net/http/httptest, exec_test).
	cmd := exec.Command(os.Args[0], "-test.run=TestGlobalLeaseHoldHelper")
	cmd.Env = append(os.Environ(), // safe: re-execs test binary, not ox CLI
		"OX_TEST_HOLD_LEASE=1",
		"OX_TEST_HOLD_ENDPOINT=sageox.ai",
		"XDG_STATE_HOME="+dir,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start subprocess: %v", err)
	}

	// Wait for the subprocess to have acquired the lease. The helper
	// writes a marker file when ready; polling that is more reliable
	// than a sleep.
	marker := filepath.Join(dir, "lease-acquired")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(marker); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("subprocess did not acquire lease in 5s")
	}

	// While the subprocess holds the lease, our acquire must fail.
	if _, err := AcquireGlobalSyncLease("sageox.ai"); !errors.Is(err, ErrNotOwner) {
		t.Errorf("expected ErrNotOwner while subprocess holds lease; got %v", err)
	}

	// Kill the subprocess — the kernel releases the flock on death.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill subprocess: %v", err)
	}
	_ = cmd.Wait()

	// Re-acquire should now succeed. Poll briefly because the kernel
	// flock release happens during process teardown, which is fast
	// but not instantaneous on macOS.
	deadline = time.Now().Add(2 * time.Second)
	var l *Lease
	var err error
	for time.Now().Before(deadline) {
		l, err = AcquireGlobalSyncLease("sageox.ai")
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("acquire after subprocess death: %v", err)
	}
	defer l.Release()
}

// TestGlobalLeaseHoldHelper is the subprocess entry point invoked by
// TestGlobalLeaseSubprocessFailover. Skips itself unless the magic env
// var is set, so normal test runs ignore it.
func TestGlobalLeaseHoldHelper(t *testing.T) {
	if os.Getenv("OX_TEST_HOLD_LEASE") != "1" {
		t.Skip("helper test — only runs as subprocess")
	}
	ep := os.Getenv("OX_TEST_HOLD_ENDPOINT")
	if ep == "" {
		t.Fatal("OX_TEST_HOLD_ENDPOINT not set")
	}
	// reset the memoized dir so the subprocess sees its own
	// XDG_STATE_HOME (the parent sets it via exec.Env).
	globalLeaseDirOnce = sync.Once{}
	globalLeaseDirCached = ""
	globalLeaseDirErr = nil

	l, err := AcquireGlobalSyncLease(ep)
	if err != nil {
		t.Fatalf("subprocess acquire: %v", err)
	}
	// signal the parent we have the lease.
	dir := os.Getenv("XDG_STATE_HOME")
	if err := os.WriteFile(filepath.Join(dir, "lease-acquired"), nil, 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	// hold forever (parent will kill us). Sleep in 100ms chunks so a
	// stray test timeout still wakes us to exit cleanly.
	for {
		time.Sleep(100 * time.Millisecond)
		_ = l // keep reference alive; the linter would otherwise flag the holder loop
	}
}

// TestGlobalLeasePathStableAcrossCalls is a regression guard: two
// successive globalLeasePath calls with the same endpoint must return
// the same path. If memoization or normalization drifted, two daemons
// for the same endpoint could end up with different lease files and
// neither would see the other's lock.
func TestGlobalLeasePathStableAcrossCalls(t *testing.T) {
	withIsolatedLeaseDir(t)

	p1, err := globalLeasePath("sageox.ai")
	if err != nil {
		t.Fatalf("first path: %v", err)
	}
	p2, err := globalLeasePath("sageox.ai")
	if err != nil {
		t.Fatalf("second path: %v", err)
	}
	if p1 != p2 {
		t.Errorf("path not stable: %q vs %q", p1, p2)
	}
	if !strings.Contains(p1, "global-sync-leases") {
		t.Errorf("path missing lease dir component: %q", p1)
	}
	if !strings.HasSuffix(p1, "global-sync.lease") {
		t.Errorf("path missing lease filename: %q", p1)
	}
}
