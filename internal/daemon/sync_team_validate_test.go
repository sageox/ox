package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/manifest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateGCClone_PassesWithCoreFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	repoDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".git"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".sageox"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "SOUL.md"), []byte("# Soul\n"), 0644))

	assert.True(t, scheduler.validateGCClone(repoDir, nil))
}

func TestValidateGCClone_FailsWithoutGitDir(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	repoDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".sageox"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "SOUL.md"), []byte("# Soul\n"), 0644))

	assert.False(t, scheduler.validateGCClone(repoDir, nil))
}

func TestValidateGCClone_FailsWithoutCoreFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	repoDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".git"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".sageox"), 0755))

	assert.False(t, scheduler.validateGCClone(repoDir, nil))
}

func TestValidateGCClone_FailsWithDeniedPaths(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	repoDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".git"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".sageox"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "SOUL.md"), []byte("# Soul\n"), 0644))

	// create a denied path that should not be materialized
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, "data"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "data", "leak.bin"), []byte("leaked"), 0644))

	cfg := &manifest.ManifestConfig{
		Denies: []string{"data"},
	}

	assert.False(t, scheduler.validateGCClone(repoDir, cfg),
		"validation should fail when denied paths are materialized")
}

func TestValidateGCClone_FailsWithoutSageoxDir(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	repoDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".git"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "SOUL.md"), []byte("# Soul\n"), 0644))
	// no .sageox directory

	assert.False(t, scheduler.validateGCClone(repoDir, nil),
		"validation should fail without .sageox directory")
}

func TestValidateGCClone_PassesWithDeniesNotMaterialized(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	repoDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".git"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".sageox"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "SOUL.md"), []byte("# Soul\n"), 0644))

	cfg := &manifest.ManifestConfig{
		Denies: []string{"data", "sessions", "coworkers"},
	}

	assert.True(t, scheduler.validateGCClone(repoDir, cfg),
		"validation should pass when denied paths don't exist")
}
