//go:build !short

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/config"
)

func TestCheckDuplicateRepoMarkers_SingleMarker(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")

	writeMarkerFile(t, sageoxDir, ".repo_abc123", map[string]string{
		"repo_id":       "repo_abc123",
		"endpoint":      "https://sageox.ai",
		"init_at":       "2026-02-20T10:00:00Z",
		"init_by_email": "alice@example.com",
	})

	result := checkDuplicateRepoMarkers(false)

	if !result.skipped {
		t.Errorf("expected skipped for single marker, got: passed=%v warning=%v message=%s",
			result.passed, result.warning, result.message)
	}
}

func TestCheckDuplicateRepoMarkers_TwoSameEndpoint_Detected(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")

	// config.json points to repo A (the "current" one)
	cfg := &config.ProjectConfig{
		RepoID:        "repo_aaa111",
		Endpoint:      "https://sageox.ai",
		ConfigVersion: config.CurrentConfigVersion,
	}
	if err := config.SaveProjectConfig(gitRoot, cfg); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	// marker A: the current user's registration
	writeMarkerFile(t, sageoxDir, ".repo_aaa111", map[string]string{
		"repo_id":       "repo_aaa111",
		"endpoint":      "https://sageox.ai",
		"init_at":       "2026-02-20T10:00:00Z",
		"init_by_email": "alice@example.com",
		"init_by_name":  "Person A",
	})

	// marker B: a teammate's registration
	writeMarkerFile(t, sageoxDir, ".repo_bbb222", map[string]string{
		"repo_id":       "repo_bbb222",
		"endpoint":      "https://sageox.ai",
		"init_at":       "2026-02-22T14:00:00Z",
		"init_by_email": "bob@example.com",
		"init_by_name":  "Person B",
	})

	result := checkDuplicateRepoMarkers(false)

	if result.passed || result.skipped {
		t.Fatalf("expected failed check, got: passed=%v skipped=%v", result.passed, result.skipped)
	}
	if !strings.Contains(result.message, "2 registrations") {
		t.Errorf("expected message to mention '2 registrations', got: %s", result.message)
	}
	if !strings.Contains(result.detail, "repo_aaa111") {
		t.Errorf("expected detail to contain repo_aaa111, got: %s", result.detail)
	}
	if !strings.Contains(result.detail, "repo_bbb222") {
		t.Errorf("expected detail to contain repo_bbb222, got: %s", result.detail)
	}
	if !strings.Contains(result.detail, "current") {
		t.Errorf("expected detail to mark current registration, got: %s", result.detail)
	}
	if !strings.Contains(result.detail, "Person A") {
		t.Errorf("expected detail to show creator name, got: %s", result.detail)
	}
}

func TestCheckDuplicateRepoMarkers_TwoDifferentEndpoints_Skipped(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")

	writeMarkerFile(t, sageoxDir, ".repo_aaa111", map[string]string{
		"repo_id":  "repo_aaa111",
		"endpoint": "https://sageox.ai",
	})

	writeMarkerFile(t, sageoxDir, ".repo_bbb222", map[string]string{
		"repo_id":  "repo_bbb222",
		"endpoint": "https://staging.sageox.ai",
	})

	result := checkDuplicateRepoMarkers(false)

	// different endpoints = not a duplicate (handled by checkMultipleEndpoints)
	if !result.skipped {
		t.Errorf("expected skipped for different endpoints, got: passed=%v message=%s",
			result.passed, result.message)
	}
}

func TestCleanupDuplicateMarkers_KeepCurrentRepo(t *testing.T) {
	// scenario: user picks THEIR repo (the one in config.json) as primary
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")

	// config initially points to repo A
	cfg := &config.ProjectConfig{
		RepoID:        "repo_aaa111",
		Endpoint:      "https://sageox.ai",
		ConfigVersion: config.CurrentConfigVersion,
	}
	if err := config.SaveProjectConfig(gitRoot, cfg); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	writeMarkerFile(t, sageoxDir, ".repo_aaa111", map[string]string{
		"repo_id":  "repo_aaa111",
		"endpoint": "https://sageox.ai",
		"init_at":  "2026-02-20T10:00:00Z",
	})
	writeMarkerFile(t, sageoxDir, ".repo_bbb222", map[string]string{
		"repo_id":  "repo_bbb222",
		"endpoint": "https://sageox.ai",
		"init_at":  "2026-02-22T14:00:00Z",
	})

	// user picks their own repo (A) as primary
	selected := duplicateMarker{
		filename: ".repo_aaa111",
		data:     repoMarkerFullData{RepoID: "repo_aaa111", Endpoint: "https://sageox.ai"},
	}
	all := []duplicateMarker{
		{filename: ".repo_aaa111", data: repoMarkerFullData{RepoID: "repo_aaa111"}},
		{filename: ".repo_bbb222", data: repoMarkerFullData{RepoID: "repo_bbb222"}},
	}

	cleanupDuplicateMarkers(gitRoot, sageoxDir, cfg, selected, all)

	// config.json should still have repo A (unchanged)
	updatedCfg, err := config.LoadProjectConfig(gitRoot)
	if err != nil {
		t.Fatalf("failed to load config after cleanup: %v", err)
	}
	if updatedCfg.RepoID != "repo_aaa111" {
		t.Errorf("expected config repo_id=repo_aaa111, got: %s", updatedCfg.RepoID)
	}

	// marker A should still exist
	if _, err := os.Stat(filepath.Join(sageoxDir, ".repo_aaa111")); os.IsNotExist(err) {
		t.Error("expected .repo_aaa111 to still exist after cleanup")
	}

	// marker B should be removed
	if _, err := os.Stat(filepath.Join(sageoxDir, ".repo_bbb222")); !os.IsNotExist(err) {
		t.Error("expected .repo_bbb222 to be removed after cleanup")
	}
}

func TestCleanupDuplicateMarkers_KeepOtherRepo(t *testing.T) {
	// scenario: user picks the OTHER person's repo as primary
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")

	// config initially points to repo A (our repo)
	cfg := &config.ProjectConfig{
		RepoID:        "repo_aaa111",
		Endpoint:      "https://sageox.ai",
		ConfigVersion: config.CurrentConfigVersion,
	}
	if err := config.SaveProjectConfig(gitRoot, cfg); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	writeMarkerFile(t, sageoxDir, ".repo_aaa111", map[string]string{
		"repo_id":  "repo_aaa111",
		"endpoint": "https://sageox.ai",
		"init_at":  "2026-02-20T10:00:00Z",
	})
	writeMarkerFile(t, sageoxDir, ".repo_bbb222", map[string]string{
		"repo_id":  "repo_bbb222",
		"endpoint": "https://sageox.ai",
		"init_at":  "2026-02-22T14:00:00Z",
	})

	// user picks the OTHER repo (B) as primary
	selected := duplicateMarker{
		filename: ".repo_bbb222",
		data:     repoMarkerFullData{RepoID: "repo_bbb222", Endpoint: "https://sageox.ai"},
	}
	all := []duplicateMarker{
		{filename: ".repo_aaa111", data: repoMarkerFullData{RepoID: "repo_aaa111"}},
		{filename: ".repo_bbb222", data: repoMarkerFullData{RepoID: "repo_bbb222"}},
	}

	cleanupDuplicateMarkers(gitRoot, sageoxDir, cfg, selected, all)

	// config.json should now point to repo B
	updatedCfg, err := config.LoadProjectConfig(gitRoot)
	if err != nil {
		t.Fatalf("failed to load config after cleanup: %v", err)
	}
	if updatedCfg.RepoID != "repo_bbb222" {
		t.Errorf("expected config repo_id=repo_bbb222 after picking other repo, got: %s", updatedCfg.RepoID)
	}

	// marker B should still exist
	if _, err := os.Stat(filepath.Join(sageoxDir, ".repo_bbb222")); os.IsNotExist(err) {
		t.Error("expected .repo_bbb222 to still exist after cleanup")
	}

	// marker A should be removed
	if _, err := os.Stat(filepath.Join(sageoxDir, ".repo_aaa111")); !os.IsNotExist(err) {
		t.Error("expected .repo_aaa111 to be removed after cleanup")
	}
}

// --- Duplicate repo markers tests ---

// writeMarkerFile creates a .repo_* marker JSON file in the .sageox/ directory.
func writeMarkerFile(t *testing.T, sageoxDir, filename string, data map[string]string) {
	t.Helper()
	markerJSON, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sageoxDir, filename), markerJSON, 0644); err != nil {
		t.Fatalf("failed to write marker %s: %v", filename, err)
	}
}
