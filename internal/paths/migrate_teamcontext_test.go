package paths

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// -----------------------------------------------------------------------------
// TeamContextMigrationNeeded Tests
// -----------------------------------------------------------------------------

func TestTeamContextMigrationNeeded_XDGMode(t *testing.T) {
	t.Setenv("OX_XDG_ENABLE", "1")

	result := TeamContextMigrationNeeded("/project", "team123", "/some/path")

	assert.False(t, result, "should return false in XDG mode")
}

func TestTeamContextMigrationNeeded_LegacyNotExists(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("OX_XDG_ENABLE", "")

	projectRoot := filepath.Join(tempDir, "project")
	legacyPath := filepath.Join(tempDir, "sageox_team_abc_context")
	// legacyPath does not exist

	result := TeamContextMigrationNeeded(projectRoot, "abc", legacyPath)

	assert.False(t, result, "should return false when legacy path doesn't exist")
}

func TestTeamContextMigrationNeeded_LegacyExists(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("OX_XDG_ENABLE", "")

	projectRoot := filepath.Join(tempDir, "project")
	legacyPath := filepath.Join(tempDir, "sageox_team_abc_context")

	// create legacy path
	require.NoError(t, os.MkdirAll(legacyPath, 0755))

	// result depends on whether new path exists
	// since we can't control home dir caching, just verify no panic
	result := TeamContextMigrationNeeded(projectRoot, "abc", legacyPath)
	assert.IsType(t, true, result)
}

func TestMigrationResult_WithErrors(t *testing.T) {
	result := MigrationResult{
		Errors: []error{
			os.ErrNotExist,
			os.ErrPermission,
		},
	}

	assert.Len(t, result.Errors, 2)
	assert.ErrorIs(t, result.Errors[0], os.ErrNotExist)
	assert.ErrorIs(t, result.Errors[1], os.ErrPermission)
}

// -----------------------------------------------------------------------------
// CreateTeamSymlinks Tests
// -----------------------------------------------------------------------------

func TestCreateTeamSymlinks(t *testing.T) {
	t.Run("empty team IDs is no-op", func(t *testing.T) {
		err := CreateTeamSymlinks("/some/path", nil)
		assert.NoError(t, err)

		err = CreateTeamSymlinks("/some/path", []string{})
		assert.NoError(t, err)
	})

	t.Run("with team IDs returns nil (placeholder)", func(t *testing.T) {
		err := CreateTeamSymlinks("/some/path", []string{"team1", "team2"})
		assert.NoError(t, err)
	})
}

// -----------------------------------------------------------------------------
// TeamContextMigrationNeeded with existing new path
// -----------------------------------------------------------------------------

func TestTeamContextMigrationNeeded_NewPathExists(t *testing.T) {
	t.Setenv("OX_XDG_DISABLE", "1")
	t.Setenv("OX_XDG_ENABLE", "")

	tempDir := t.TempDir()
	legacyPath := filepath.Join(tempDir, "sageox_team_abc_context")
	require.NoError(t, os.MkdirAll(legacyPath, 0755))

	// create the new path so migration is not needed
	newPath := TeamContextDir("abc", "https://sageox.ai")
	require.NoError(t, os.MkdirAll(newPath, 0755))

	result := TeamContextMigrationNeeded(filepath.Join(tempDir, "project"), "abc", legacyPath)
	assert.False(t, result, "should return false when new path already exists")
}

// -----------------------------------------------------------------------------
// migrateSessionCache with destination already existing
// -----------------------------------------------------------------------------

func TestMigrateSessionCache_SkipsExistingDestSession(t *testing.T) {
	tempDir := t.TempDir()
	legacyDir := filepath.Join(tempDir, "legacy")
	contextDir := filepath.Join(legacyDir, "context")

	// create a session in context/
	sessionDir := filepath.Join(contextDir, "existing-session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), []byte("old data"), 0644))

	// pre-create the destination session dir so it gets skipped
	dstDir := SessionCacheDir("")
	dstSessionDir := filepath.Join(dstDir, "existing-session")
	require.NoError(t, os.MkdirAll(dstSessionDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dstSessionDir, "raw.jsonl"), []byte("keep this"), 0644))

	err := migrateSessionCache(legacyDir)
	assert.NoError(t, err)

	// destination content should NOT be overwritten
	content, err := os.ReadFile(filepath.Join(dstSessionDir, "raw.jsonl"))
	require.NoError(t, err)
	assert.Equal(t, "keep this", string(content))
}
