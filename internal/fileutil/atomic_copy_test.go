package fileutil

import (
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAtomicCopyFileLargeBoundedMemory(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	destination := filepath.Join(dir, "destination")
	file, err := os.Create(source)
	require.NoError(t, err)
	_, err = file.WriteString("first complete conversation bytes\n")
	require.NoError(t, err)
	require.NoError(t, file.Truncate(65*1024*1024))
	require.NoError(t, file.Close())
	require.NoError(t, os.WriteFile(destination, []byte("old"), 0644))
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	require.NoError(t, AtomicCopyFile(destination, source, 0600))
	runtime.ReadMemStats(&after)
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(4*1024*1024))
	digest := func(path string) []byte {
		file, err := os.Open(path)
		require.NoError(t, err)
		defer file.Close()
		hash := sha256.New()
		_, err = io.Copy(hash, file)
		require.NoError(t, err)
		return hash.Sum(nil)
	}
	require.Equal(t, digest(source), digest(destination))
	info, err := os.Stat(destination)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	require.Equal(t, int64(65*1024*1024), info.Size())
	t.Logf("65MiB copy total allocated bytes=%d", after.TotalAlloc-before.TotalAlloc)
}

func TestAtomicCopyFilePreservesSymlinkAndFailedDestination(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	require.NoError(t, os.WriteFile(source, []byte("new content"), 0600))
	require.NoError(t, os.WriteFile(target, []byte("old"), 0600))
	require.NoError(t, os.Symlink(target, link))
	require.NoError(t, AtomicCopyFile(link, source, 0600))
	info, err := os.Lstat(link)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSymlink)
	data, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "new content", string(data))
	require.Error(t, AtomicCopyFile(link, dir, 0600))
	data, err = os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "new content", string(data))
}
