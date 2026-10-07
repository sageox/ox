package gitutil

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// an ownerless sparse lock can outlive any age threshold while Git updates the
// worktree; sweeping must preserve its identity and bytes.
func TestSparseCheckoutLock_DetectedButNeverRemovedByAge(t *testing.T) {
	gitDir := filepath.Join(t.TempDir(), ".git")
	lock := filepath.Join(gitDir, "info", "sparse-checkout.lock")
	require.NoError(t, os.MkdirAll(filepath.Dir(lock), 0o755))
	require.NoError(t, os.WriteFile(lock, []byte("live sparse patterns\n"), 0o644))
	assert.Equal(t, []string{"info/sparse-checkout.lock"}, HasLockFiles(gitDir))

	removed, errs := RemoveStaleLockFiles(gitDir)
	assert.Empty(t, errs)
	assert.Empty(t, removed, "a fresh lock is left alone")

	old := time.Now().Add(-(AbandonedLockAge * 24))
	require.NoError(t, os.Chtimes(lock, old, old))
	removed, errs = RemoveStaleLockFiles(gitDir)
	assert.Empty(t, errs)
	assert.Empty(t, removed)
	assert.Equal(t, []string{"info/sparse-checkout.lock"}, HasLockFiles(gitDir))
	content, err := os.ReadFile(lock)
	require.NoError(t, err)
	assert.Equal(t, "live sparse patterns\n", string(content))
}
