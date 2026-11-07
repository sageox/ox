package fileutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// Predictable private paths must not follow planted symlinks or retain broad
// permissions from an older installation.
func TestPrivatePathsRejectSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no Unix permission bits, and os.Symlink needs privileges there")
	}
	base := t.TempDir()
	dir := filepath.Join(base, "state")
	require.NoError(t, os.Mkdir(dir, 0755))
	require.NoError(t, EnsurePrivateDir(dir))
	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0700), info.Mode().Perm())
	target := filepath.Join(base, "target")
	require.NoError(t, os.WriteFile(target, []byte("untouched"), 0600))
	log := filepath.Join(dir, "trace.log")
	require.NoError(t, os.Symlink(target, log))
	_, err = OpenPrivateLog(log)
	require.ErrorContains(t, err, "not a regular file")
	content, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "untouched", string(content))
	link := filepath.Join(base, "linked-state")
	require.NoError(t, os.Symlink(dir, link))
	require.ErrorContains(t, EnsurePrivateDir(link), "not a real directory")
	require.NoError(t, os.Remove(log))
	require.NoError(t, os.WriteFile(log, nil, 0644)) // left broad by an older install
	file, err := OpenPrivateLog(log)
	require.NoError(t, err)
	require.NoError(t, file.Close())
	info, err = os.Stat(log)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
}
