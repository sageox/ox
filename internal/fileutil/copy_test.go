package fileutil

import (
	"os"
	"path/filepath"
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

func TestCopyFile_MissingSrc(t *testing.T) {
	dir := t.TempDir()
	err := CopyFile(filepath.Join(dir, "nonexistent"), filepath.Join(dir, "dst"))
	assert.Error(t, err)
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
