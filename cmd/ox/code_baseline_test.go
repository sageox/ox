package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveBaselineCodeDBDir_Exists(t *testing.T) {
	projectRoot := t.TempDir()
	xdgData := t.TempDir()

	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_DATA_HOME", xdgData)

	// create .sageox/config.json with repo_id and endpoint
	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	cfg := map[string]string{
		"repo_id":  "repo_01abc123",
		"endpoint": "https://sageox.ai",
	}
	cfgData, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), cfgData, 0644))

	// compute and create the baseline dir on disk
	baselineDir := paths.CodeDBBaselineDir("repo_01abc123", "https://sageox.ai")
	require.NotEmpty(t, baselineDir, "CodeDBBaselineDir must return a non-empty path")
	require.NoError(t, os.MkdirAll(baselineDir, 0755))

	got := resolveBaselineCodeDBDir(projectRoot)
	assert.Equal(t, baselineDir, got)
}

func TestResolveBaselineCodeDBDir_Missing_FallsBack(t *testing.T) {
	projectRoot := t.TempDir()
	xdgData := t.TempDir()

	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_DATA_HOME", xdgData)

	// create .sageox/config.json but do NOT create the baseline dir on disk
	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	cfg := map[string]string{
		"repo_id":  "repo_01abc123",
		"endpoint": "https://sageox.ai",
	}
	cfgData, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), cfgData, 0644))

	got := resolveBaselineCodeDBDir(projectRoot)
	assert.Empty(t, got, "should return empty when baseline dir does not exist on disk")
}

func TestResolveBaselineCodeDBDir_NoProjectConfig_FallsBack(t *testing.T) {
	// bare temp dir with no .sageox/ — not a SageOx project
	projectRoot := t.TempDir()

	got := resolveBaselineCodeDBDir(projectRoot)
	assert.Empty(t, got, "should return empty for non-SageOx project without panicking")
}
