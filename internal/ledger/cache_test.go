package ledger

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// Failure prevented: a reclone or an adopted path loses the code index because
// preservation skipped a cache that was there, or reported a restore it never
// made — and the caller then deleted the only copy.
func TestPreserveAndRestoreCache(t *testing.T) {
	src := t.TempDir()
	backup := filepath.Join(t.TempDir(), "backup")
	require.NoError(t, PreserveCache(src, backup), "no cache is nothing to preserve")
	require.NoDirExists(t, backup)
	require.NoError(t, RestoreCache(backup, t.TempDir()), "no backup is nothing to restore")

	index := filepath.Join(".sageox", "cache", "codedb", "metadata.db")
	writeReadTestFile(t, filepath.Join(src, index), "sqlite-data")
	require.NoError(t, PreserveCache(src, backup))
	dst := t.TempDir()
	require.NoError(t, RestoreCache(backup, dst))
	restored, err := os.ReadFile(filepath.Join(dst, index))
	require.NoError(t, err)
	require.Equal(t, "sqlite-data", string(restored))
}

// Failure prevented: RestoreCache reports success without restoring, and the
// caller deletes the backup it still needed.
func TestRestoreCacheReportsWhatItCouldNotRestore(t *testing.T) {
	backup := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(backup, "index.db"), []byte("data"), 0600))

	blocked := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(blocked, ".sageox"), []byte("a file"), 0600))
	require.Error(t, RestoreCache(backup, blocked), "a destination that cannot hold the cache")

	if runtime.GOOS == "windows" {
		t.Skip("a path through a file reads as missing on Windows, so it cannot fail Stat here")
	}
	parent := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(parent, []byte("a file"), 0600))
	require.Error(t, RestoreCache(filepath.Join(parent, "backup"), t.TempDir()), "a backup that cannot be inspected is not a missing one")
}
