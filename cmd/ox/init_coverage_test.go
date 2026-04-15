package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
)

func TestGetSageoxReadmeContent_WithConfig(t *testing.T) {
	t.Parallel()

	cfg := &config.ProjectConfig{
		RepoID:   "repo_abc123",
		TeamID:   "team_def456",
		Endpoint: "https://sageox.ai",
	}

	content := GetSageoxReadmeContent(cfg)

	assert.Contains(t, content, "# SageOx")
	assert.Contains(t, content, "Context is the scarcest resource")
	assert.Contains(t, content, "repo_abc123")
	assert.Contains(t, content, "team_def456")
	assert.Contains(t, content, "SageOx Links")
}

func TestGetSageoxReadmeContent_NilConfig(t *testing.T) {
	t.Parallel()

	content := GetSageoxReadmeContent(nil)

	assert.Contains(t, content, "# SageOx")
	assert.NotContains(t, content, "SageOx Links")
}

func TestGetSageoxReadmeContent_EmptyConfig(t *testing.T) {
	t.Parallel()

	cfg := &config.ProjectConfig{}
	content := GetSageoxReadmeContent(cfg)

	assert.Contains(t, content, "# SageOx")
	assert.NotContains(t, content, "SageOx Links")
}

func TestGetSageoxReadmeContent_OnlyRepoID(t *testing.T) {
	t.Parallel()

	cfg := &config.ProjectConfig{
		RepoID:   "repo_abc",
		Endpoint: "https://sageox.ai",
	}
	content := GetSageoxReadmeContent(cfg)

	assert.Contains(t, content, "SageOx Links")
	assert.Contains(t, content, "Repository Dashboard")
	assert.NotContains(t, content, "Team Dashboard")
}

func TestGetSageoxReadmeContent_OnlyTeamID(t *testing.T) {
	t.Parallel()

	cfg := &config.ProjectConfig{
		TeamID:   "team_xyz",
		Endpoint: "https://sageox.ai",
	}
	content := GetSageoxReadmeContent(cfg)

	assert.Contains(t, content, "SageOx Links")
	assert.Contains(t, content, "Team Dashboard")
	assert.NotContains(t, content, "Repository Dashboard")
}

func TestFileExists_Coverage(t *testing.T) {
	t.Parallel()

	t.Run("existing file", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "exists.txt")
		assert.NoError(t, os.WriteFile(path, []byte("content"), 0o644))
		assert.True(t, fileExists(path))
	})

	t.Run("nonexistent file", func(t *testing.T) {
		t.Parallel()
		assert.False(t, fileExists("/nonexistent/path/file.txt"))
	})

	t.Run("empty path", func(t *testing.T) {
		t.Parallel()
		assert.False(t, fileExists(""))
	})

	t.Run("directory exists", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		assert.True(t, fileExists(dir))
	})
}

func TestConfigResultConstants(t *testing.T) {
	t.Parallel()

	// verify the constants have distinct values
	assert.NotEqual(t, configCreated, configUpgraded)
	assert.NotEqual(t, configCreated, configPreserved)
	assert.NotEqual(t, configCreated, configError)
	assert.NotEqual(t, configUpgraded, configPreserved)
	assert.NotEqual(t, configUpgraded, configError)
	assert.NotEqual(t, configPreserved, configError)
}

// TestInitialCommitReadmeContent already declared in init_empty_repo_test.go
