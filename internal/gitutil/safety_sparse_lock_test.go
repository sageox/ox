package gitutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// HasLockFiles and RemoveStaleLockFiles must agree on info/sparse-checkout.lock:
// a lock one detects and the other cannot clear is a permanent wedge.
func TestSparseCheckoutLock_DetectedAndClearedAfterStaleAge(t *testing.T) {
	gitDir := filepath.Join(t.TempDir(), ".git")
	lock := filepath.Join(gitDir, "info", "sparse-checkout.lock")
	require.NoError(t, os.MkdirAll(filepath.Dir(lock), 0o755))
	require.NoError(t, os.WriteFile(lock, nil, 0o644))
	assert.Equal(t, []string{"info/sparse-checkout.lock"}, HasLockFiles(gitDir))

	removed, errs := RemoveStaleLockFiles(gitDir)
	assert.Empty(t, errs)
	assert.Empty(t, removed, "a fresh lock is left alone")

	// past StaleLockAge but inside the sparse-checkout window: a live
	// sparse-checkout on a huge checkout may still hold it
	mid := time.Now().Add(-(StaleLockAge + time.Minute))
	require.NoError(t, os.Chtimes(lock, mid, mid))
	removed, errs = RemoveStaleLockFiles(gitDir)
	assert.Empty(t, errs)
	assert.Empty(t, removed, "a lock older than StaleLockAge but younger than SparseCheckoutLockAge is retained")

	old := time.Now().Add(-(SparseCheckoutLockAge + time.Second))
	require.NoError(t, os.Chtimes(lock, old, old))
	removed, errs = RemoveStaleLockFiles(gitDir)
	assert.Empty(t, errs)
	assert.Equal(t, []string{"info/sparse-checkout.lock"}, removed)
	assert.Empty(t, HasLockFiles(gitDir))
}

// A lock judged abandoned can be unlinked and recreated by a live writer before
// the sweep removes it. The sweep must remove only the exact file it inspected.
func TestRemoveStaleLockFiles_NeverRemovesLockRecreatedAfterInspection(t *testing.T) {
	gitDir := filepath.Join(t.TempDir(), ".git")
	lock := filepath.Join(gitDir, "index.lock")
	require.NoError(t, os.MkdirAll(gitDir, 0o755))
	require.NoError(t, os.WriteFile(lock, []byte("old"), 0o644))
	old := time.Now().Add(-(AbandonedLockAge + time.Minute))
	require.NoError(t, os.Chtimes(lock, old, old))

	beforeLockRemoveHook = func(path string) {
		require.NoError(t, os.Remove(path))
		require.NoError(t, os.WriteFile(path, []byte("live writer"), 0o644))
	}
	t.Cleanup(func() { beforeLockRemoveHook = nil })

	removed, errs := RemoveStaleLockFiles(gitDir)

	assert.Empty(t, errs)
	assert.Empty(t, removed, "a recreated lock belongs to a live writer")
	data, err := os.ReadFile(lock)
	require.NoError(t, err)
	assert.Equal(t, "live writer", string(data))
}

// A lock that disappears between the inspection and the re-stat is simply gone:
// nothing is removed, nothing is reported as an error.
func TestRemoveStaleLockFiles_LockVanishesBeforeRemoval(t *testing.T) {
	gitDir := t.TempDir()
	lock := filepath.Join(gitDir, "index.lock")
	require.NoError(t, os.WriteFile(lock, nil, 0o644))
	old := time.Now().Add(-2 * AbandonedLockAge)
	require.NoError(t, os.Chtimes(lock, old, old))
	saved := beforeLockRemoveHook
	beforeLockRemoveHook = func(path string) { _ = os.Remove(path) }
	t.Cleanup(func() { beforeLockRemoveHook = saved })
	removed, errs := RemoveStaleLockFiles(gitDir)
	assert.Empty(t, removed)
	assert.Empty(t, errs)
}

// A re-stat that fails for any reason other than "gone" is reported and the
// lock is left alone: an unreadable answer is never treated as abandonment.
func TestRemoveStaleLockFiles_RestatErrorIsReportedNotRemoved(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("relies on directory permissions denying stat")
	}
	gitDir := t.TempDir()
	lock := filepath.Join(gitDir, "index.lock")
	require.NoError(t, os.WriteFile(lock, nil, 0o644))
	old := time.Now().Add(-2 * AbandonedLockAge)
	require.NoError(t, os.Chtimes(lock, old, old))
	saved := beforeLockRemoveHook
	beforeLockRemoveHook = func(string) { require.NoError(t, os.Chmod(gitDir, 0o000)) }
	t.Cleanup(func() {
		beforeLockRemoveHook = saved
		_ = os.Chmod(gitDir, 0o755)
	})
	removed, errs := RemoveStaleLockFiles(gitDir)
	require.NoError(t, os.Chmod(gitDir, 0o755))
	assert.Empty(t, removed)
	require.Len(t, errs, 1)
	assert.ErrorContains(t, errs[0], "re-stat")
	assert.FileExists(t, lock)
}
