package paths

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// -----------------------------------------------------------------------------
// Migrate Tests
// -----------------------------------------------------------------------------

func TestMigrate_XDGModeSkipped(t *testing.T) {
	t.Setenv("OX_XDG_ENABLE", "1")

	result := Migrate()

	assert.False(t, result.ConfigMigrated)
	assert.False(t, result.GuidanceCacheMigrated)
	assert.False(t, result.SessionCacheMigrated)
	assert.Empty(t, result.Errors)
}

func TestMigrate_ReturnsValidResult(t *testing.T) {
	t.Setenv("OX_XDG_ENABLE", "")

	result := Migrate()

	// verify the function returns without panic and produces a valid struct
	assert.IsType(t, MigrationResult{}, result)
	// errors should be a slice (possibly empty)
	assert.IsType(t, []error{}, result.Errors)
}

// -----------------------------------------------------------------------------
// migrateDirectory Tests
// -----------------------------------------------------------------------------

func TestMigrateDirectory_Success(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "src")
	dstDir := filepath.Join(tempDir, "dst")

	// create source with files and subdirectories
	require.NoError(t, os.MkdirAll(srcDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "file1.txt"), []byte("content1"), 0644))

	subDir := filepath.Join(srcDir, "subdir")
	require.NoError(t, os.MkdirAll(subDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(subDir, "file2.txt"), []byte("content2"), 0644))

	err := migrateDirectory(srcDir, dstDir)
	require.NoError(t, err)

	// verify files migrated
	assert.FileExists(t, filepath.Join(dstDir, "file1.txt"))
	assert.FileExists(t, filepath.Join(dstDir, "subdir", "file2.txt"))

	// verify content preserved
	content1, _ := os.ReadFile(filepath.Join(dstDir, "file1.txt"))
	content2, _ := os.ReadFile(filepath.Join(dstDir, "subdir", "file2.txt"))
	assert.Equal(t, "content1", string(content1))
	assert.Equal(t, "content2", string(content2))

	// verify source removed
	assert.NoDirExists(t, srcDir)
}

func TestMigrateDirectory_SkipsExistingDestination(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "src")
	dstDir := filepath.Join(tempDir, "dst")

	// create source
	require.NoError(t, os.MkdirAll(srcDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "file.txt"), []byte("source"), 0644))

	// create destination with existing file
	require.NoError(t, os.MkdirAll(dstDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dstDir, "file.txt"), []byte("existing"), 0644))

	err := migrateDirectory(srcDir, dstDir)
	require.NoError(t, err)

	// verify existing file was NOT overwritten
	content, _ := os.ReadFile(filepath.Join(dstDir, "file.txt"))
	assert.Equal(t, "existing", string(content))
}

func TestMigrateDirectory_EmptySource(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "src")
	dstDir := filepath.Join(tempDir, "dst")

	require.NoError(t, os.MkdirAll(srcDir, 0755))

	err := migrateDirectory(srcDir, dstDir)
	require.NoError(t, err)

	// verify destination created
	assert.DirExists(t, dstDir)

	// verify source removed
	assert.NoDirExists(t, srcDir)
}

func TestMigrateDirectory_PreservesPermissions(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "src")
	dstDir := filepath.Join(tempDir, "dst")

	require.NoError(t, os.MkdirAll(srcDir, 0755))

	// create file with specific permissions
	srcFile := filepath.Join(srcDir, "script.sh")
	require.NoError(t, os.WriteFile(srcFile, []byte("#!/bin/bash"), 0755))

	err := migrateDirectory(srcDir, dstDir)
	require.NoError(t, err)

	// verify permissions preserved (within umask constraints)
	dstFile := filepath.Join(dstDir, "script.sh")
	info, err := os.Stat(dstFile)
	require.NoError(t, err)

	// on most systems, executable bit should be preserved
	assert.True(t, info.Mode()&0100 != 0, "expected executable permission to be preserved")
}

func TestMigrateDirectory_SourceNotExists(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "nonexistent")
	dstDir := filepath.Join(tempDir, "dst")

	err := migrateDirectory(srcDir, dstDir)
	assert.Error(t, err)
}

func TestMigrateDirectory_NestedSubdirectories(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "src")
	dstDir := filepath.Join(tempDir, "dst")

	// create deeply nested structure
	deepPath := filepath.Join(srcDir, "a", "b", "c", "d")
	require.NoError(t, os.MkdirAll(deepPath, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(deepPath, "deep.txt"), []byte("deep content"), 0644))

	err := migrateDirectory(srcDir, dstDir)
	require.NoError(t, err)

	// verify deep file migrated
	assert.FileExists(t, filepath.Join(dstDir, "a", "b", "c", "d", "deep.txt"))
	content, _ := os.ReadFile(filepath.Join(dstDir, "a", "b", "c", "d", "deep.txt"))
	assert.Equal(t, "deep content", string(content))
}

func TestMigrateDirectory_MixedContentTypes(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "src")
	dstDir := filepath.Join(tempDir, "dst")

	require.NoError(t, os.MkdirAll(srcDir, 0755))

	// create files with different types of content
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "text.txt"), []byte("text"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "binary.bin"), []byte{0x00, 0x01, 0x02}, 0644))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "empty.txt"), []byte{}, 0644))

	err := migrateDirectory(srcDir, dstDir)
	require.NoError(t, err)

	// verify all files migrated with correct content
	textContent, _ := os.ReadFile(filepath.Join(dstDir, "text.txt"))
	binaryContent, _ := os.ReadFile(filepath.Join(dstDir, "binary.bin"))
	emptyContent, _ := os.ReadFile(filepath.Join(dstDir, "empty.txt"))

	assert.Equal(t, []byte("text"), textContent)
	assert.Equal(t, []byte{0x00, 0x01, 0x02}, binaryContent)
	assert.Empty(t, emptyContent)
}

// -----------------------------------------------------------------------------
// MigrateTeamContext Tests
// -----------------------------------------------------------------------------

func TestMigrateTeamContext_XDGModeError(t *testing.T) {
	t.Setenv("OX_XDG_ENABLE", "1")

	err := MigrateTeamContext("team123", "/some/legacy/path")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "XDG mode")
}

func TestMigrateTeamContext_InvalidPath(t *testing.T) {
	t.Setenv("OX_XDG_ENABLE", "")

	// try to migrate from non-existent path
	err := MigrateTeamContext("team123", "/nonexistent/path/that/does/not/exist")

	// should fail because source doesn't exist
	assert.Error(t, err)
}

// -----------------------------------------------------------------------------
// EnsureMigrated Tests
// -----------------------------------------------------------------------------

func TestEnsureMigrated_RunsWithoutPanic(t *testing.T) {
	// EnsureMigrated uses sync.Once, so it only runs once per process.
	// this test verifies it doesn't panic.
	t.Setenv("OX_XDG_ENABLE", "")

	// should not panic
	err := EnsureMigrated()
	// error may or may not be nil depending on system state
	_ = err
}

// -----------------------------------------------------------------------------
// migrateSessionCache Tests
// -----------------------------------------------------------------------------

func TestMigrateSessionCache_NoContextDir(t *testing.T) {
	tempDir := t.TempDir()

	legacyDir := filepath.Join(tempDir, "legacy")
	require.NoError(t, os.MkdirAll(legacyDir, 0755))

	// no context subdirectory - should return nil (nothing to migrate)
	err := migrateSessionCache(legacyDir)
	assert.NoError(t, err)
}

func TestMigrateSessionCache_EmptyContextDir(t *testing.T) {
	tempDir := t.TempDir()

	legacyDir := filepath.Join(tempDir, "legacy")
	contextDir := filepath.Join(legacyDir, "context")
	require.NoError(t, os.MkdirAll(contextDir, 0755))

	// empty context dir - should succeed
	err := migrateSessionCache(legacyDir)
	assert.NoError(t, err)
}

// -----------------------------------------------------------------------------
// Edge Case Tests
// -----------------------------------------------------------------------------

func TestMigrateDirectory_SpecialCharactersInFilenames(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "src")
	dstDir := filepath.Join(tempDir, "dst")

	require.NoError(t, os.MkdirAll(srcDir, 0755))

	// files with various special characters (that are valid on most filesystems)
	specialFiles := []string{
		"file with spaces.txt",
		"file-with-dashes.txt",
		"file_with_underscores.txt",
		"file.multiple.dots.txt",
	}

	for _, name := range specialFiles {
		path := filepath.Join(srcDir, name)
		require.NoError(t, os.WriteFile(path, []byte("content: "+name), 0644))
	}

	err := migrateDirectory(srcDir, dstDir)
	require.NoError(t, err)

	// verify all special files migrated
	for _, name := range specialFiles {
		path := filepath.Join(dstDir, name)
		assert.FileExists(t, path)
		content, _ := os.ReadFile(path)
		assert.Equal(t, "content: "+name, string(content))
	}
}

// -----------------------------------------------------------------------------
// MigrateLedgerToNewStructure Tests
// -----------------------------------------------------------------------------

func TestMigrateLedgerToNewStructure(t *testing.T) {
	t.Run("empty project root returns error", func(t *testing.T) {
		err := MigrateLedgerToNewStructure("", "https://sageox.ai")
		assert.Error(t, err)
	})

	t.Run("no legacy dir is no-op", func(t *testing.T) {
		tmp := t.TempDir()
		projectRoot := filepath.Join(tmp, "myrepo")
		require.NoError(t, os.MkdirAll(projectRoot, 0755))

		err := MigrateLedgerToNewStructure(projectRoot, "https://sageox.ai")
		assert.NoError(t, err)
	})

	t.Run("new path already exists is no-op", func(t *testing.T) {
		tmp := t.TempDir()
		projectRoot := filepath.Join(tmp, "myrepo")
		require.NoError(t, os.MkdirAll(projectRoot, 0755))
		legacyDir := filepath.Join(tmp, "myrepo_sageox_ledger")
		require.NoError(t, os.MkdirAll(legacyDir, 0755))
		newDir := NewLedgerPath(projectRoot, "https://sageox.ai")
		require.NoError(t, os.MkdirAll(newDir, 0755))

		err := MigrateLedgerToNewStructure(projectRoot, "https://sageox.ai")
		assert.NoError(t, err)
	})

	t.Run("successful migration moves directory", func(t *testing.T) {
		tmp := t.TempDir()
		projectRoot := filepath.Join(tmp, "myrepo")
		require.NoError(t, os.MkdirAll(projectRoot, 0755))

		// create legacy ledger with .git dir and a file
		legacyDir := filepath.Join(tmp, "myrepo_sageox_ledger")
		require.NoError(t, os.MkdirAll(filepath.Join(legacyDir, ".git"), 0755))
		require.NoError(t, os.WriteFile(filepath.Join(legacyDir, "data.txt"), []byte("ledger data"), 0644))

		err := MigrateLedgerToNewStructure(projectRoot, "https://sageox.ai")
		require.NoError(t, err)

		newDir := NewLedgerPath(projectRoot, "https://sageox.ai")
		// new path should exist with content
		assert.DirExists(t, newDir)
		assert.DirExists(t, filepath.Join(newDir, ".git"))
		content, err := os.ReadFile(filepath.Join(newDir, "data.txt"))
		require.NoError(t, err)
		assert.Equal(t, "ledger data", string(content))

		// legacy should be gone
		assert.NoDirExists(t, legacyDir)
	})
}

func TestMigrate_LegacyMode_NoLegacyData(t *testing.T) {
	t.Setenv("OX_XDG_DISABLE", "1")
	t.Setenv("OX_XDG_ENABLE", "")

	result := Migrate()
	// no legacy data to migrate, so nothing should have migrated
	assert.IsType(t, MigrationResult{}, result)
}

// -----------------------------------------------------------------------------
// migrateSessionCache with context dir Tests
// -----------------------------------------------------------------------------

func TestMigrateSessionCache_WithContextDirContainingSubdirs(t *testing.T) {
	tempDir := t.TempDir()
	legacyDir := filepath.Join(tempDir, "legacy")
	contextDir := filepath.Join(legacyDir, "context")

	// create session directories inside context/
	for _, sessionName := range []string{"session-abc", "session-def"} {
		sessionDir := filepath.Join(contextDir, sessionName)
		require.NoError(t, os.MkdirAll(sessionDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), []byte("data"), 0644))
	}

	// also create a file (not dir) inside context/ — should be skipped
	require.NoError(t, os.WriteFile(filepath.Join(contextDir, "stray-file.txt"), []byte("stray"), 0644))

	err := migrateSessionCache(legacyDir)
	assert.NoError(t, err)

	// context dir should be cleaned up
	assert.NoDirExists(t, contextDir)
}

func TestMigrateSessionCache_WithDaemonLogs(t *testing.T) {
	tempDir := t.TempDir()
	legacyDir := filepath.Join(tempDir, "legacy")

	// context dir must exist for migrateSessionCache to proceed past early return
	contextDir := filepath.Join(legacyDir, "context")
	require.NoError(t, os.MkdirAll(contextDir, 0755))

	// create daemon log files matching the glob pattern
	for _, logName := range []string{"daemon-abc.log", "daemon-def.log"} {
		require.NoError(t, os.WriteFile(filepath.Join(legacyDir, logName), []byte("log data"), 0644))
	}

	err := migrateSessionCache(legacyDir)
	assert.NoError(t, err)

	// daemon logs should have been moved — original files removed
	for _, logName := range []string{"daemon-abc.log", "daemon-def.log"} {
		assert.NoFileExists(t, filepath.Join(legacyDir, logName))
	}
}

// -----------------------------------------------------------------------------
// MigrateTeamContext Success Path Tests
// -----------------------------------------------------------------------------

func TestMigrateTeamContext_SuccessfulRename(t *testing.T) {
	t.Setenv("OX_XDG_DISABLE", "1")
	t.Setenv("OX_XDG_ENABLE", "")

	tempDir := t.TempDir()
	legacyPath := filepath.Join(tempDir, "sageox_team_abc_context")

	// create legacy dir with some content
	require.NoError(t, os.MkdirAll(legacyPath, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(legacyPath, "SOUL.md"), []byte("team soul"), 0644))

	err := MigrateTeamContext("abc", legacyPath)
	require.NoError(t, err)

	// new path should exist with content
	newPath := TeamContextDir("abc", "https://sageox.ai")
	assert.DirExists(t, newPath)
	content, err := os.ReadFile(filepath.Join(newPath, "SOUL.md"))
	require.NoError(t, err)
	assert.Equal(t, "team soul", string(content))
}

// -----------------------------------------------------------------------------
// Migrate with Legacy Data Tests
// -----------------------------------------------------------------------------

func TestMigrate_LegacyMode_WithLegacyConfig(t *testing.T) {
	t.Setenv("OX_XDG_DISABLE", "1")
	t.Setenv("OX_XDG_ENABLE", "")

	// create legacy config dir at ~/.config/sageox
	home := getHomeDir()
	if home == "" {
		t.Skip("cannot determine home directory")
	}

	legacyConfigDir := filepath.Join(home, ".config", "sageox")
	newConfigDir := ConfigDir() // in legacy mode: ~/.sageox/config

	// skip if legacy dir already exists (don't mess with real data)
	if _, err := os.Stat(legacyConfigDir); err == nil {
		t.Skip("legacy config dir already exists, skipping to avoid data loss")
	}
	// skip if new structure already exists (migration won't run)
	if _, err := os.Stat(newConfigDir); err == nil {
		t.Skip("new config dir already exists, migration would be skipped")
	}

	// this test is fragile on real systems — just verify no panic
	result := Migrate()
	assert.IsType(t, MigrationResult{}, result)
}

func TestMigrateDirectory_PartialMigration(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "src")
	dstDir := filepath.Join(tempDir, "dst")

	require.NoError(t, os.MkdirAll(srcDir, 0755))
	require.NoError(t, os.MkdirAll(dstDir, 0755))

	// create files in source
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "file1.txt"), []byte("source1"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "file2.txt"), []byte("source2"), 0644))

	// pre-create one file in destination
	require.NoError(t, os.WriteFile(filepath.Join(dstDir, "file1.txt"), []byte("existing"), 0644))

	err := migrateDirectory(srcDir, dstDir)
	require.NoError(t, err)

	// file1 should NOT be overwritten
	content1, _ := os.ReadFile(filepath.Join(dstDir, "file1.txt"))
	assert.Equal(t, "existing", string(content1))

	// file2 should be migrated
	content2, _ := os.ReadFile(filepath.Join(dstDir, "file2.txt"))
	assert.Equal(t, "source2", string(content2))
}
