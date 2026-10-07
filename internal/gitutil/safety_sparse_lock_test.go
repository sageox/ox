package gitutil

import (
	"os"
	"path/filepath"
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

	old := time.Now().Add(-(StaleLockAge + time.Second))
	require.NoError(t, os.Chtimes(lock, old, old))
	removed, errs = RemoveStaleLockFiles(gitDir)
	assert.Empty(t, errs)
	assert.Equal(t, []string{"info/sparse-checkout.lock"}, removed)
	assert.Empty(t, HasLockFiles(gitDir))
}
