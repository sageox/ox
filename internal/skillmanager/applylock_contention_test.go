package skillmanager

import (
	"os"
	"path/filepath"
	"testing"
)

// The apply lock is what stops two ox processes — a session starting while the
// daemon ticks, or two sessions starting together — from interleaving a
// multi-step mutation. Both plan against the same on-disk digests, so the loser
// can write a file the winner just retired.

// TestAcquireApplyLock_SecondHolderIsTurnedAwayWithoutBlocking is the whole
// contract: NON-blocking. Prime must never wait on this, and a doctor run that
// silently blocked would look like a hang.
func TestAcquireApplyLock_SecondHolderIsTurnedAwayWithoutBlocking(t *testing.T) {
	repo := t.TempDir()

	unlock, acquired, err := acquireApplyLock(repo)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if !acquired {
		t.Fatal("the first caller did not get an uncontended lock")
	}

	// flock is per-open-file-description, so a second acquire in this same process
	// is turned away exactly as a second process would be.
	unlock2, acquired2, err := acquireApplyLock(repo)
	if err != nil {
		t.Fatalf("second acquire returned an error instead of standing down: %v", err)
	}
	if acquired2 {
		if unlock2 != nil {
			unlock2()
		}
		t.Fatal("two callers held the apply lock at once; interleaved applies can undo each other")
	}

	unlock()

	unlock3, acquired3, err := acquireApplyLock(repo)
	if err != nil {
		t.Fatalf("third acquire: %v", err)
	}
	if !acquired3 {
		t.Fatal("the lock was not released; every later reconcile in this repo would be skipped")
	}
	unlock3()
}

// TestAcquireApplyLock_LivesUnderTheIgnoredCacheDir: the lock file must never
// become a tracked file. .sageox/cache/ is already gitignored; anywhere else and
// every reconcile would dirty the user's `git status`.
func TestAcquireApplyLock_LivesUnderTheIgnoredCacheDir(t *testing.T) {
	repo := t.TempDir()

	unlock, acquired, err := acquireApplyLock(repo)
	if err != nil || !acquired {
		t.Fatalf("acquire: acquired=%v err=%v", acquired, err)
	}
	defer unlock()

	want := filepath.Join(repo, ".sageox", "cache", "skills-apply.lock")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("lock file is not under the ignored cache dir: %v", err)
	}
}

// TestAcquireApplyLock_RefusesASymlinkedLockPath: a symlinked lock file would
// have ox flock — and create — something outside the repository.
func TestAcquireApplyLock_RefusesASymlinkedLockPath(t *testing.T) {
	repo := t.TempDir()
	cacheDir := filepath.Join(repo, ".sageox", "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "elsewhere.lock")
	if err := os.Symlink(outside, filepath.Join(cacheDir, "skills-apply.lock")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	unlock, acquired, err := acquireApplyLock(repo)
	if acquired {
		if unlock != nil {
			unlock()
		}
		t.Fatal("ox took a lock through a symlink pointing outside the repository")
	}
	if err == nil {
		t.Error("a symlinked lock path was refused silently instead of reported")
	}
	if _, statErr := os.Stat(outside); statErr == nil {
		t.Error("ox created a lock file outside the repository")
	}
}
