package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOSFileSystemReadWriteFile(t *testing.T) {
	dir := t.TempDir()
	fs := OSFileSystem{}
	path := filepath.Join(dir, "test.txt")
	content := []byte("hello world")

	if err := fs.WriteFile(path, content, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := fs.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	if string(got) != "hello world" {
		t.Errorf("got %q, want %q", string(got), "hello world")
	}
}

func TestOSFileSystemWriteFileAtomicallyReplacesExistingContent(t *testing.T) {
	dir := t.TempDir()
	fsys := OSFileSystem{}
	path := filepath.Join(dir, "raw.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("prior"), 0o644))

	require.NoError(t, fsys.WriteFile(path, []byte("complete replacement"), 0o644))

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, []byte("complete replacement"), got)
	matches, err := filepath.Glob(filepath.Join(dir, ".tmp-*"))
	require.NoError(t, err)
	assert.Empty(t, matches, "successful atomic copy must not leave temporary files")
}

func TestOSFileSystemMkdirAll(t *testing.T) {
	dir := t.TempDir()
	fs := OSFileSystem{}
	nested := filepath.Join(dir, "a", "b", "c")

	if err := fs.MkdirAll(nested, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	info, err := os.Stat(nested)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !info.IsDir() {
		t.Error("expected directory")
	}
}

func TestOSFileSystemStat(t *testing.T) {
	dir := t.TempDir()
	fs := OSFileSystem{}

	info, err := fs.Stat(dir)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !info.IsDir() {
		t.Error("expected directory")
	}

	_, err = fs.Stat(filepath.Join(dir, "nonexistent"))
	if err == nil {
		t.Error("expected error for nonexistent path")
	}
}

func TestOSFileSystemRemove(t *testing.T) {
	dir := t.TempDir()
	fs := OSFileSystem{}
	path := filepath.Join(dir, "removeme.txt")

	if err := os.WriteFile(path, []byte("bye"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := fs.Remove(path); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("file should have been removed")
	}
}

func TestOSFileSystemRemoveAll(t *testing.T) {
	dir := t.TempDir()
	fs := OSFileSystem{}
	nested := filepath.Join(dir, "removedir", "sub")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "file.txt"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := fs.RemoveAll(filepath.Join(dir, "removedir")); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "removedir")); !os.IsNotExist(err) {
		t.Error("directory should have been removed")
	}
}
