package fileutil

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	dstPath := filepath.Join(dir, "dest.txt")

	content := "test file content\nwith multiple lines\n"
	require.NoError(t, os.WriteFile(srcPath, []byte(content), 0644))

	require.NoError(t, CopyFile(srcPath, dstPath))

	got, err := os.ReadFile(dstPath)
	require.NoError(t, err)
	assert.Equal(t, content, string(got))

	// verify mode is preserved
	srcInfo, _ := os.Stat(srcPath)
	dstInfo, _ := os.Stat(dstPath)
	assert.Equal(t, srcInfo.Mode(), dstInfo.Mode())
}

func TestCopyFile_BinaryContent(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "binary.bin")
	dstPath := filepath.Join(dir, "copy.bin")

	data := make([]byte, 256)
	for i := range 256 {
		data[i] = byte(i)
	}
	require.NoError(t, os.WriteFile(srcPath, data, 0755))

	require.NoError(t, CopyFile(srcPath, dstPath))

	got, err := os.ReadFile(dstPath)
	require.NoError(t, err)
	assert.Equal(t, data, got)
}

func TestCopyDir(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := filepath.Join(t.TempDir(), "copy")

	// create a nested structure
	require.NoError(t, os.MkdirAll(filepath.Join(srcDir, "sub", "deep"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "root.txt"), []byte("root"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "sub", "mid.txt"), []byte("mid"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "sub", "deep", "leaf.txt"), []byte("leaf"), 0644))

	require.NoError(t, CopyDir(srcDir, dstDir))

	// verify all files copied
	got, err := os.ReadFile(filepath.Join(dstDir, "root.txt"))
	require.NoError(t, err)
	assert.Equal(t, "root", string(got))

	got, err = os.ReadFile(filepath.Join(dstDir, "sub", "mid.txt"))
	require.NoError(t, err)
	assert.Equal(t, "mid", string(got))

	got, err = os.ReadFile(filepath.Join(dstDir, "sub", "deep", "leaf.txt"))
	require.NoError(t, err)
	assert.Equal(t, "leaf", string(got))
}

func TestCopyDir_EmptyDir(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := filepath.Join(t.TempDir(), "empty-copy")

	require.NoError(t, CopyDir(srcDir, dstDir))
	assert.DirExists(t, dstDir)
}

// Failure prevented: a symlink in a copied tree is dereferenced, so a link to a
// host file (e.g. /etc/shadow) lands that file's content in a checkout.
func TestCopyFile_RecreatesSymlinkWithoutReadingItsTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs a privilege Windows test runners lack")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "secret")
	require.NoError(t, os.WriteFile(target, []byte("host file"), 0600))
	link := filepath.Join(dir, "link")
	require.NoError(t, os.Symlink(target, link))
	dst := filepath.Join(dir, "copy")

	require.NoError(t, CopyFile(link, dst))

	info, err := os.Lstat(dst)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSymlink, "the copy is a link, not the target's bytes")
	got, err := os.Readlink(dst)
	require.NoError(t, err)
	assert.Equal(t, target, got)
}

// Failure prevented: copying a socket, FIFO, or device blocks or reads without
// end instead of being skipped.
func TestCopyFile_SkipsNonRegularFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix domain socket files are the non-regular file this test uses")
	}
	// A socket path must fit sockaddr_un (104 bytes on macOS), which a
	// t.TempDir path can exceed.
	dir, err := os.MkdirTemp("", "cp")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	listener, err := net.Listen("unix", sock)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	dst := filepath.Join(dir, "copy")

	require.NoError(t, CopyFile(sock, dst))
	_, err = os.Lstat(dst)
	assert.True(t, os.IsNotExist(err), "nothing is written for a non-regular file")
}

// Failure prevented: a copy that could not read its source or write its
// destination reports success, and the caller deletes the original.
func TestCopyFile_ReportsEveryFailure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T, dir string) (src, dst string)
	}{
		{"source missing", func(t *testing.T, dir string) (string, string) {
			return filepath.Join(dir, "nonexistent"), filepath.Join(dir, "dst")
		}},
		{"source unreadable", func(t *testing.T, dir string) (string, string) {
			if runtime.GOOS == "windows" || os.Geteuid() == 0 {
				t.Skip("file permissions must reject reads for this failure injection")
			}
			src := filepath.Join(dir, "src")
			require.NoError(t, os.WriteFile(src, []byte("data"), 0000))
			return src, filepath.Join(dir, "dst")
		}},
		{"destination directory missing", func(t *testing.T, dir string) (string, string) {
			src := filepath.Join(dir, "src")
			require.NoError(t, os.WriteFile(src, []byte("data"), 0600))
			return src, filepath.Join(dir, "missing", "dst")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src, dst := tc.prepare(t, t.TempDir())
			assert.Error(t, CopyFile(src, dst))
		})
	}
}

func TestCopyDir_MissingSrc(t *testing.T) {
	dir := t.TempDir()
	assert.Error(t, CopyDir(filepath.Join(dir, "missing"), filepath.Join(dir, "copy")))
}
