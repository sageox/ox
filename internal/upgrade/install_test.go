package upgrade

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeBin(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
}

func TestInstallFromDir(t *testing.T) {
	src, dest := t.TempDir(), t.TempDir()
	writeBin(t, src, "ox", "new-ox")
	writeBin(t, src, "ox-adapter-codex", "new-codex")
	writeBin(t, src, "README.md", "not a binary")
	require.NoError(t, os.Mkdir(filepath.Join(src, "ox-adapter-dir"), 0o755))
	writeBin(t, dest, "ox", "old-ox")

	names, err := InstallFromDir(src, dest)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"ox", "ox-adapter-codex"}, names)

	for name, want := range map[string]string{"ox": "new-ox", "ox-adapter-codex": "new-codex"} {
		got, err := os.ReadFile(filepath.Join(dest, name))
		require.NoError(t, err)
		assert.Equal(t, want, string(got))
		info, err := os.Stat(filepath.Join(dest, name))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())
	}
	_, err = os.Stat(filepath.Join(dest, "README.md"))
	assert.True(t, os.IsNotExist(err))

	entries, err := os.ReadDir(dest)
	require.NoError(t, err)
	assert.Len(t, entries, 2, "no staged or backup files left behind")
}

func TestInstallFromDirErrors(t *testing.T) {
	t.Run("missing source", func(t *testing.T) {
		_, err := InstallFromDir(filepath.Join(t.TempDir(), "nope"), t.TempDir())
		require.Error(t, err)
	})
	t.Run("no ox binary", func(t *testing.T) {
		src := t.TempDir()
		writeBin(t, src, "ox-adapter-codex", "x")
		_, err := InstallFromDir(src, t.TempDir())
		require.ErrorContains(t, err, "no ox binary")
	})
	t.Run("destination not writable", func(t *testing.T) {
		src := t.TempDir()
		writeBin(t, src, "ox", "x")
		_, err := InstallFromDir(src, filepath.Join(t.TempDir(), "missing"))
		require.ErrorIs(t, err, ErrNotWritable)
	})
	t.Run("unreadable source binary", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("root reads any file")
		}
		src, dest := t.TempDir(), t.TempDir()
		writeBin(t, src, "ox", "x")
		require.NoError(t, os.Chmod(filepath.Join(src, "ox"), 0o000))
		_, err := InstallFromDir(src, dest)
		require.ErrorContains(t, err, "stage ox")
		entries, _ := os.ReadDir(dest)
		assert.Empty(t, entries)
	})
}
