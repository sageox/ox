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

// ============================================================================
// checkEndpointNormalization Tests
// ============================================================================

func TestCheckEndpointNormalization_AllClean(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create config with normalized endpoint
	requireSageoxDir(t, gitRoot)
	cfg := &config.ProjectConfig{
		Endpoint:      "https://sageox.ai",
		ConfigVersion: config.CurrentConfigVersion,
	}
	if err := config.SaveProjectConfig(gitRoot, cfg); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	// create marker with normalized endpoint
	markerData := map[string]string{
		"repo_id":  "repo_abc123",
		"endpoint": "https://sageox.ai",
	}
	markerJSON, _ := json.MarshalIndent(markerData, "", "  ")
	markerPath := filepath.Join(gitRoot, ".sageox", ".repo_abc123")
	if err := os.WriteFile(markerPath, markerJSON, 0644); err != nil {
		t.Fatalf("failed to write marker: %v", err)
	}

	result := checkEndpointNormalization(false)

	if !result.passed {
		t.Errorf("expected passed=true when all endpoints normalized, got: %+v", result)
	}
	if result.warning {
		t.Error("expected warning=false when all clean")
	}
	if result.message != "all endpoints normalized" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckEndpointNormalization_DetectsConfigPrefix(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// write config with prefixed endpoint directly (bypass SaveProjectConfig normalization)
	requireSageoxDir(t, gitRoot)
	rawConfig := map[string]any{
		"config_version":         config.CurrentConfigVersion,
		"endpoint":               "https://api.sageox.ai",
		"update_frequency_hours": 24,
	}
	data, _ := json.MarshalIndent(rawConfig, "", "  ")
	configPath := filepath.Join(gitRoot, ".sageox", "config.json")
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	result := checkEndpointNormalization(false)

	// prefixed endpoints pass with a warning (not a hard failure)
	if !result.warning {
		t.Errorf("expected warning=true for prefixed endpoint, got: %+v", result)
	}
	if !strings.Contains(result.detail, "config.json") {
		t.Errorf("expected detail to mention config.json, got: %s", result.detail)
	}
}

func TestCheckEndpointNormalization_FixesConfigPrefix(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// write config with prefixed endpoint directly
	requireSageoxDir(t, gitRoot)
	rawConfig := map[string]any{
		"config_version":         config.CurrentConfigVersion,
		"endpoint":               "https://api.sageox.ai",
		"update_frequency_hours": 24,
	}
	data, _ := json.MarshalIndent(rawConfig, "", "  ")
	configPath := filepath.Join(gitRoot, ".sageox", "config.json")
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	result := checkEndpointNormalization(true)

	if !result.passed {
		t.Errorf("expected passed=true after fix, got: %+v", result)
	}

	// verify config was rewritten
	reloaded, err := config.LoadProjectConfig(gitRoot)
	if err != nil {
		t.Fatalf("failed to reload config: %v", err)
	}
	if reloaded.Endpoint != "https://sageox.ai" {
		t.Errorf("config endpoint not normalized after fix: %s", reloaded.Endpoint)
	}
}

func TestCheckEndpointNormalization_DetectsMarkerPrefix(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)

	// create marker with prefixed endpoint
	markerData := map[string]string{
		"repo_id":  "repo_abc123",
		"endpoint": "https://www.sageox.ai",
	}
	markerJSON, _ := json.MarshalIndent(markerData, "", "  ")
	markerPath := filepath.Join(gitRoot, ".sageox", ".repo_abc123")
	if err := os.WriteFile(markerPath, markerJSON, 0644); err != nil {
		t.Fatalf("failed to write marker: %v", err)
	}

	result := checkEndpointNormalization(false)

	// prefixed marker endpoints pass with a warning (not a hard failure)
	if !result.warning {
		t.Errorf("expected warning=true for prefixed marker, got: %+v", result)
	}
	if !strings.Contains(result.detail, ".repo_abc123") {
		t.Errorf("expected detail to mention marker file, got: %s", result.detail)
	}
}

func TestCheckEndpointNormalization_FixesMarkerPrefix(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)

	// create marker with prefixed endpoint and extra fields to verify preservation
	markerData := map[string]any{
		"repo_id":  "repo_abc123",
		"endpoint": "https://api.sageox.ai",
		"extra":    "preserved",
	}
	markerJSON, _ := json.MarshalIndent(markerData, "", "  ")
	markerPath := filepath.Join(gitRoot, ".sageox", ".repo_abc123")
	if err := os.WriteFile(markerPath, markerJSON, 0644); err != nil {
		t.Fatalf("failed to write marker: %v", err)
	}

	result := checkEndpointNormalization(true)

	if !result.passed {
		t.Errorf("expected passed=true after fix, got: %+v", result)
	}

	// verify marker was rewritten with normalized endpoint
	reloadedData, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("failed to read marker: %v", err)
	}
	var reloaded map[string]any
	if err := json.Unmarshal(reloadedData, &reloaded); err != nil {
		t.Fatalf("failed to parse marker: %v", err)
	}
	if reloaded["endpoint"] != "https://sageox.ai" {
		t.Errorf("marker endpoint not normalized: %v", reloaded["endpoint"])
	}
	if reloaded["extra"] != "preserved" {
		t.Errorf("marker extra field lost after fix: %v", reloaded["extra"])
	}
}

func TestCheckEndpointNormalization_FixesMarkerPrefixedEndpoint(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)

	// create marker with prefixed endpoint
	markerData := map[string]string{
		"repo_id":  "repo_abc123",
		"endpoint": "https://api.sageox.ai",
	}
	markerJSON, _ := json.MarshalIndent(markerData, "", "  ")
	markerPath := filepath.Join(gitRoot, ".sageox", ".repo_abc123")
	if err := os.WriteFile(markerPath, markerJSON, 0644); err != nil {
		t.Fatalf("failed to write marker: %v", err)
	}

	result := checkEndpointNormalization(true)

	if !result.passed {
		t.Errorf("expected passed=true after fix, got: %+v", result)
	}

	// verify endpoint was normalized
	reloadedData, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("failed to read marker: %v", err)
	}
	var reloaded map[string]any
	if err := json.Unmarshal(reloadedData, &reloaded); err != nil {
		t.Fatalf("failed to parse marker: %v", err)
	}
	if reloaded["endpoint"] != "https://sageox.ai" {
		t.Errorf("marker endpoint not normalized: %v", reloaded["endpoint"])
	}
}

func TestCheckEndpointNormalization_MultipleIssues(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)

	// prefixed config
	rawConfig := map[string]any{
		"config_version":         config.CurrentConfigVersion,
		"endpoint":               "https://www.sageox.ai",
		"update_frequency_hours": 24,
	}
	configData, _ := json.MarshalIndent(rawConfig, "", "  ")
	configPath := filepath.Join(gitRoot, ".sageox", "config.json")
	if err := os.WriteFile(configPath, configData, 0600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// prefixed marker
	markerData := map[string]string{
		"repo_id":  "repo_abc123",
		"endpoint": "https://app.sageox.ai",
	}
	markerJSON, _ := json.MarshalIndent(markerData, "", "  ")
	markerPath := filepath.Join(gitRoot, ".sageox", ".repo_abc123")
	if err := os.WriteFile(markerPath, markerJSON, 0644); err != nil {
		t.Fatalf("failed to write marker: %v", err)
	}

	// fix all
	result := checkEndpointNormalization(true)

	if !result.passed {
		t.Errorf("expected passed=true after fixing multiple issues, got: %+v", result)
	}
	if !strings.Contains(result.message, "normalized") {
		t.Errorf("expected message to mention 'normalized', got: %s", result.message)
	}
}
