package paths

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// -----------------------------------------------------------------------------
// copyFile Tests
// -----------------------------------------------------------------------------

func TestCopyFile_Success(t *testing.T) {
	tempDir := t.TempDir()
	srcFile := filepath.Join(tempDir, "source.txt")
	dstFile := filepath.Join(tempDir, "dest.txt")

	require.NoError(t, os.WriteFile(srcFile, []byte("hello world"), 0644))

	err := copyFile(srcFile, dstFile)
	require.NoError(t, err)

	assert.FileExists(t, dstFile)

	content, _ := os.ReadFile(dstFile)
	assert.Equal(t, "hello world", string(content))
}

func TestCopyFile_PreservesMode(t *testing.T) {
	tempDir := t.TempDir()
	srcFile := filepath.Join(tempDir, "script.sh")
	dstFile := filepath.Join(tempDir, "dest.sh")

	require.NoError(t, os.WriteFile(srcFile, []byte("#!/bin/bash"), 0755))

	err := copyFile(srcFile, dstFile)
	require.NoError(t, err)

	srcInfo, _ := os.Stat(srcFile)
	dstInfo, _ := os.Stat(dstFile)

	assert.Equal(t, srcInfo.Mode(), dstInfo.Mode())
}

func TestCopyFile_SourceNotExists(t *testing.T) {
	tempDir := t.TempDir()
	srcFile := filepath.Join(tempDir, "nonexistent.txt")
	dstFile := filepath.Join(tempDir, "dest.txt")

	err := copyFile(srcFile, dstFile)
	assert.Error(t, err)
}

func TestCopyFile_BinaryContent(t *testing.T) {
	tempDir := t.TempDir()
	srcFile := filepath.Join(tempDir, "binary.bin")
	dstFile := filepath.Join(tempDir, "dest.bin")

	// binary content including null bytes
	binaryData := []byte{0x00, 0xFF, 0x01, 0xFE, 0x02, 0xFD}
	require.NoError(t, os.WriteFile(srcFile, binaryData, 0644))

	err := copyFile(srcFile, dstFile)
	require.NoError(t, err)

	content, _ := os.ReadFile(dstFile)
	assert.Equal(t, binaryData, content)
}

func TestCopyFile_EmptyFile(t *testing.T) {
	tempDir := t.TempDir()
	srcFile := filepath.Join(tempDir, "empty.txt")
	dstFile := filepath.Join(tempDir, "dest.txt")

	require.NoError(t, os.WriteFile(srcFile, []byte{}, 0644))

	err := copyFile(srcFile, dstFile)
	require.NoError(t, err)

	content, _ := os.ReadFile(dstFile)
	assert.Empty(t, content)
}

func TestCopyFile_LargeFile(t *testing.T) {
	tempDir := t.TempDir()
	srcFile := filepath.Join(tempDir, "large.bin")
	dstFile := filepath.Join(tempDir, "dest.bin")

	// create a 1MB file
	largeData := make([]byte, 1024*1024)
	for i := range largeData {
		largeData[i] = byte(i % 256)
	}
	require.NoError(t, os.WriteFile(srcFile, largeData, 0644))

	err := copyFile(srcFile, dstFile)
	require.NoError(t, err)

	content, _ := os.ReadFile(dstFile)
	assert.Equal(t, largeData, content)
}

func TestCopyFile_OverwritesExisting(t *testing.T) {
	tempDir := t.TempDir()
	srcFile := filepath.Join(tempDir, "source.txt")
	dstFile := filepath.Join(tempDir, "dest.txt")

	require.NoError(t, os.WriteFile(srcFile, []byte("new content"), 0644))
	require.NoError(t, os.WriteFile(dstFile, []byte("old content"), 0644))

	err := copyFile(srcFile, dstFile)
	require.NoError(t, err)

	content, _ := os.ReadFile(dstFile)
	assert.Equal(t, "new content", string(content))
}

// -----------------------------------------------------------------------------
// copyDir Tests
// -----------------------------------------------------------------------------

func TestCopyDir_Success(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "src")
	dstDir := filepath.Join(tempDir, "dst")

	// create source with nested structure
	require.NoError(t, os.MkdirAll(filepath.Join(srcDir, "a", "b", "c"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "root.txt"), []byte("root"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "a", "level1.txt"), []byte("level1"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "a", "b", "level2.txt"), []byte("level2"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "a", "b", "c", "level3.txt"), []byte("level3"), 0644))

	err := copyDir(srcDir, dstDir)
	require.NoError(t, err)

	// verify all files copied
	assert.FileExists(t, filepath.Join(dstDir, "root.txt"))
	assert.FileExists(t, filepath.Join(dstDir, "a", "level1.txt"))
	assert.FileExists(t, filepath.Join(dstDir, "a", "b", "level2.txt"))
	assert.FileExists(t, filepath.Join(dstDir, "a", "b", "c", "level3.txt"))
}

func TestCopyDir_EmptyDir(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "src")
	dstDir := filepath.Join(tempDir, "dst")

	require.NoError(t, os.MkdirAll(srcDir, 0755))

	err := copyDir(srcDir, dstDir)
	require.NoError(t, err)

	assert.DirExists(t, dstDir)
}

func TestCopyDir_SourceNotExists(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "nonexistent")
	dstDir := filepath.Join(tempDir, "dst")

	err := copyDir(srcDir, dstDir)
	assert.Error(t, err)
}

func TestCopyDir_PreservesDirectoryMode(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "src")
	dstDir := filepath.Join(tempDir, "dst")

	require.NoError(t, os.MkdirAll(srcDir, 0755))

	err := copyDir(srcDir, dstDir)
	require.NoError(t, err)

	srcInfo, _ := os.Stat(srcDir)
	dstInfo, _ := os.Stat(dstDir)

	assert.Equal(t, srcInfo.Mode(), dstInfo.Mode())
}

func TestCopyDir_MultipleFiles(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "src")
	dstDir := filepath.Join(tempDir, "dst")

	require.NoError(t, os.MkdirAll(srcDir, 0755))

	// create multiple files
	for i := 0; i < 10; i++ {
		filename := filepath.Join(srcDir, "file"+string(rune('0'+i))+".txt")
		require.NoError(t, os.WriteFile(filename, []byte("content"+string(rune('0'+i))), 0644))
	}

	err := copyDir(srcDir, dstDir)
	require.NoError(t, err)

	// verify all files copied
	for i := 0; i < 10; i++ {
		filename := filepath.Join(dstDir, "file"+string(rune('0'+i))+".txt")
		assert.FileExists(t, filename)
		content, _ := os.ReadFile(filename)
		assert.Equal(t, "content"+string(rune('0'+i)), string(content))
	}
}

func TestCopyDir_DoesNotDeleteSource(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "src")
	dstDir := filepath.Join(tempDir, "dst")

	require.NoError(t, os.MkdirAll(srcDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "file.txt"), []byte("content"), 0644))

	err := copyDir(srcDir, dstDir)
	require.NoError(t, err)

	// source should still exist (copyDir does not remove source, migrateDirectory does)
	assert.DirExists(t, srcDir)
	assert.FileExists(t, filepath.Join(srcDir, "file.txt"))
}

// -----------------------------------------------------------------------------
// MigrationResult Struct Tests
// -----------------------------------------------------------------------------

func TestMigrationResult_Struct(t *testing.T) {
	result := MigrationResult{
		ConfigMigrated:        true,
		GuidanceCacheMigrated: false,
		SessionCacheMigrated:  true,
		Errors:                nil,
	}

	assert.True(t, result.ConfigMigrated)
	assert.False(t, result.GuidanceCacheMigrated)
	assert.True(t, result.SessionCacheMigrated)
	assert.Empty(t, result.Errors)
}

func TestCopyFile_SpecialPermissions(t *testing.T) {
	tempDir := t.TempDir()
	srcFile := filepath.Join(tempDir, "source.txt")
	dstFile := filepath.Join(tempDir, "dest.txt")

	// test with read-only permission
	require.NoError(t, os.WriteFile(srcFile, []byte("read only"), 0444))

	err := copyFile(srcFile, dstFile)
	require.NoError(t, err)

	dstInfo, _ := os.Stat(dstFile)
	assert.Equal(t, os.FileMode(0444), dstInfo.Mode().Perm())
}

func TestCopyDir_SymlinksSkipped(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "src")
	dstDir := filepath.Join(tempDir, "dst")
	targetFile := filepath.Join(tempDir, "target.txt")

	require.NoError(t, os.MkdirAll(srcDir, 0755))
	require.NoError(t, os.WriteFile(targetFile, []byte("target"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "regular.txt"), []byte("regular"), 0644))

	// create symlink in source (on systems that support it)
	symlinkPath := filepath.Join(srcDir, "link.txt")
	err := os.Symlink(targetFile, symlinkPath)
	if err != nil {
		t.Skip("symlinks not supported on this system")
	}

	// copyDir should handle this gracefully (might copy symlink or skip it)
	err = copyDir(srcDir, dstDir)
	require.NoError(t, err)

	// regular file should be copied
	assert.FileExists(t, filepath.Join(dstDir, "regular.txt"))
}
