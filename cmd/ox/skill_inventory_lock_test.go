//go:build unix

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/skillmanager"
)

// TestReconcileSkillInventoryIfStale_SkipsWhenAnotherProcessHoldsTheLock covers
// the interleaving that this feature newly makes likely: reconcile moved onto the
// session hot path, so two agent sessions starting together in one repository —
// or a session starting while the daemon's autofix tick runs — now race on the
// same managed files.
//
// The required behavior is skip, not fail and not wait. The other holder is doing
// this exact work, so losing the race is a correct outcome; blocking would stall a
// session start behind a background process.
//
// The lock is taken here the same way the implementation takes it — flock(2) on
// .sageox/cache/skills-apply.lock — because flock is per-open-file-description,
// so a second descriptor in this same process contends exactly as another process
// would.
func TestReconcileSkillInventoryIfStale_SkipsWhenAnotherProcessHoldsTheLock(t *testing.T) {
	repoRoot, managedFile := installSkillsForTest(t)
	removeManaged(t, managedFile)
	setLockRevision(t, repoRoot, "revision-from-an-older-release")

	lockPath := filepath.Join(repoRoot, ".sageox", "cache", "skills-apply.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatalf("mkdir lock dir: %v", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open lock: %v", err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("hold lock: %v", err)
	}

	// Stale revision, so without contention this would definitely do work.
	if changed := reconcileSkillInventoryIfStale(repoRoot); changed != 0 {
		t.Errorf("reconcile proceeded while another holder had the apply lock: changed=%d", changed)
	}
	if managedExists(t, managedFile) {
		t.Errorf("a contended reconcile still wrote managed files, which is the interleaving the lock exists to prevent")
	}

	// Releasing the lock must restore normal behavior — proving the skip was
	// contention and not a permanently broken path.
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	if changed := reconcileSkillInventoryIfStale(repoRoot); changed == 0 {
		t.Errorf("reconcile did not resume after the lock was released")
	}
	if !managedExists(t, managedFile) {
		t.Errorf("managed file not restored after the lock was released")
	}
}

// TestReconcileSkillInventoryIfStale_NeverWaitsOnTheReconcileLock is the
// latency contract for the session hot path.
//
// The apply lock alone was not enough: ReconcileUpdate serializes its
// read-modify-write behind fileutil.WithFileLock, which POLLS for up to ten
// seconds. With that on the prime path, a daemon tick or a concurrent `ox init`
// holding the lock would stall every agent session start in the repository for
// ten seconds — and stall silently, because the caller only logs at debug.
//
// Failure prevented: "ox made my agent take ten seconds to start, sometimes."
func TestReconcileSkillInventoryIfStale_NeverWaitsOnTheReconcileLock(t *testing.T) {
	repoRoot, managedFile := installSkillsForTest(t)
	removeManaged(t, managedFile)
	setLockRevision(t, repoRoot, "revision-from-an-older-release")

	// Hold the same sidecar lock ReconcileUpdate uses.
	lockPath := fileutil.LockPath(skillmanager.LockPath(repoRoot))
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatalf("mkdir lock dir: %v", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open sidecar lock: %v", err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("hold sidecar lock: %v", err)
	}

	start := time.Now()
	changed := reconcileSkillInventoryIfStale(repoRoot)
	elapsed := time.Since(start)

	if changed != 0 {
		t.Errorf("reconcile proceeded while the sidecar lock was held: changed=%d", changed)
	}
	// fileutil.LockTimeout is 10s; anything near it means prime is polling.
	if elapsed > 2*time.Second {
		t.Errorf("prime waited %v on a held reconcile lock; it must fail fast, not poll", elapsed)
	}
}
