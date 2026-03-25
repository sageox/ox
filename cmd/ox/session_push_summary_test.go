package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindGitRootFrom_ValidRepo(t *testing.T) {
	// create a temp dir with .git
	repoDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".git"), 0755))

	// nested dir inside repo
	nested := filepath.Join(repoDir, "sessions", "2026-01-15", "data")
	require.NoError(t, os.MkdirAll(nested, 0755))

	root, err := findGitRootFrom(nested)
	require.NoError(t, err)
	assert.Equal(t, repoDir, root)
}

func TestFindGitRootFrom_DirectGitDir(t *testing.T) {
	repoDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".git"), 0755))

	root, err := findGitRootFrom(repoDir)
	require.NoError(t, err)
	assert.Equal(t, repoDir, root)
}

func TestFindGitRootFrom_NoGitDir(t *testing.T) {
	// temp dir with no .git anywhere
	dir := t.TempDir()

	_, err := findGitRootFrom(dir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no git root found")
}

func TestPushSummaryToLedger_InvalidJSON(t *testing.T) {
	// write a non-JSON file
	tmpFile := filepath.Join(t.TempDir(), "bad.json")
	require.NoError(t, os.WriteFile(tmpFile, []byte("not json"), 0644))

	sessionDir := t.TempDir()
	result := pushSummaryToLedger(tmpFile, sessionDir)

	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "not valid JSON")
}

func TestPushSummaryToLedger_MissingFile(t *testing.T) {
	result := pushSummaryToLedger("/nonexistent/file.json", t.TempDir())

	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "read summary file")
}

func TestPushSummaryToLedger_MissingSessionDir(t *testing.T) {
	// valid JSON file but session dir doesn't exist
	tmpFile := filepath.Join(t.TempDir(), "summary.json")
	summaryData := map[string]any{
		"title":         "Test session",
		"summary":       "A test",
		"quality_score": 0.8,
	}
	data, _ := json.Marshal(summaryData)
	require.NoError(t, os.WriteFile(tmpFile, data, 0644))

	result := pushSummaryToLedger(tmpFile, "/nonexistent/session/dir")

	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "session directory does not exist")
}

func TestPushSummaryToLedger_DiscardLowQuality(t *testing.T) {
	// summary with quality_score below discard threshold (default 0.1)
	tmpFile := filepath.Join(t.TempDir(), "summary.json")
	summaryData := map[string]any{
		"title":         "Trivial session",
		"summary":       "Just a greeting",
		"quality_score": 0.05,
		"score_reason":  "single greeting message",
	}
	data, _ := json.Marshal(summaryData)
	require.NoError(t, os.WriteFile(tmpFile, data, 0644))

	sessionDir := t.TempDir()

	result := pushSummaryToLedger(tmpFile, sessionDir)

	assert.True(t, result.Success)
	assert.Equal(t, "discard", result.Disposition)
	assert.Equal(t, 0.05, result.QualityScore)
}

func TestPushSummaryToLedger_NoGitRoot(t *testing.T) {
	// valid JSON, session dir exists, but no .git parent
	tmpFile := filepath.Join(t.TempDir(), "summary.json")
	summaryData := map[string]any{
		"title":         "Test session",
		"summary":       "A test",
		"quality_score": 0.8,
	}
	data, _ := json.Marshal(summaryData)
	require.NoError(t, os.WriteFile(tmpFile, data, 0644))

	// session dir exists but has no .git parent
	sessionDir := filepath.Join(t.TempDir(), "sessions", "test-session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	result := pushSummaryToLedger(tmpFile, sessionDir)

	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "git root")
}
