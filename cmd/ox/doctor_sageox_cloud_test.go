//go:build !short

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/config"
)

// ============================================================================
// checkCloudDoctor Tests (Cloud API Integration)
// ============================================================================

func TestCheckCloudDoctor_Success_ReturnsIssues(t *testing.T) {
	// setup mock server that returns doctor issues
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/doctor") {
			w.WriteHeader(http.StatusOK)
			resp := api.DoctorResponse{
				Issues: []api.DoctorIssue{
					{
						Type:        "merge_pending",
						Severity:    "warning",
						Title:       "Merge pending",
						Description: "Multiple teams working on this repo",
						ActionURL:   "https://app.sageox.ai/merge/123",
						ActionLabel: "Resolve merge",
					},
				},
				CheckedAt: "2025-01-01T00:00:00Z",
			}
			json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	// set endpoint to mock server
	t.Setenv("SAGEOX_ENDPOINT", mockServer.URL)

	// setup temp git repo with config
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create config with repo_id
	requireSageoxDir(t, gitRoot)

	cfg := &config.ProjectConfig{
		RepoID:        "repo_test123",
		ConfigVersion: config.CurrentConfigVersion,
	}
	if err := config.SaveProjectConfig(gitRoot, cfg); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	results := checkCloudDoctor()

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	result := results[0]
	if result.name != "Merge pending" {
		t.Errorf("expected name 'Merge pending', got %q", result.name)
	}
	if !result.warning {
		t.Error("expected warning=true for warning severity")
	}
}

func TestCheckCloudDoctor_HTTP500_ReturnsWarning(t *testing.T) {
	// setup mock server that returns 500
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"internal error"}`))
	}))
	defer mockServer.Close()

	t.Setenv("SAGEOX_ENDPOINT", mockServer.URL)

	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox dir: %v", err)
	}

	cfg := &config.ProjectConfig{
		RepoID:        "repo_test123",
		ConfigVersion: config.CurrentConfigVersion,
	}
	if err := config.SaveProjectConfig(gitRoot, cfg); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	results := checkCloudDoctor()

	// should return a warning about cloud doctor being unavailable
	if len(results) != 1 {
		t.Fatalf("expected 1 result (warning), got %d", len(results))
	}

	result := results[0]
	if result.name != "Cloud doctor" {
		t.Errorf("expected name 'Cloud doctor', got %q", result.name)
	}
	if !result.warning {
		t.Error("expected warning=true for unavailable cloud doctor")
	}
	if !strings.Contains(result.message, "skipped") {
		t.Errorf("expected message to mention 'skipped', got %q", result.message)
	}
}

func TestCheckCloudDoctor_NetworkError_ReturnsWarning(t *testing.T) {
	// point to invalid endpoint
	t.Setenv("SAGEOX_ENDPOINT", "http://localhost:99999")

	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox dir: %v", err)
	}

	cfg := &config.ProjectConfig{
		RepoID:        "repo_test123",
		ConfigVersion: config.CurrentConfigVersion,
	}
	if err := config.SaveProjectConfig(gitRoot, cfg); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	results := checkCloudDoctor()

	// should return a warning about cloud doctor being unavailable
	if len(results) != 1 {
		t.Fatalf("expected 1 result (warning), got %d", len(results))
	}

	result := results[0]
	if result.name != "Cloud doctor" {
		t.Errorf("expected name 'Cloud doctor', got %q", result.name)
	}
	if !result.warning {
		t.Error("expected warning=true for network error")
	}
}

func TestCheckCloudDoctor_NoConfig_ReturnsNil(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// no config file created

	results := checkCloudDoctor()

	// should silently skip when no config
	if results != nil {
		t.Errorf("expected nil results when no config, got %d results", len(results))
	}
}

func TestCheckCloudDoctor_EmptyIssues_ReturnsNil(t *testing.T) {
	// setup mock server that returns empty issues
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/doctor") {
			w.WriteHeader(http.StatusOK)
			resp := api.DoctorResponse{
				Issues:    []api.DoctorIssue{},
				CheckedAt: "2025-01-01T00:00:00Z",
			}
			json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	t.Setenv("SAGEOX_ENDPOINT", mockServer.URL)

	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox dir: %v", err)
	}

	cfg := &config.ProjectConfig{
		RepoID:        "repo_test123",
		ConfigVersion: config.CurrentConfigVersion,
	}
	if err := config.SaveProjectConfig(gitRoot, cfg); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	results := checkCloudDoctor()

	// should silently return nil when no issues
	if results != nil {
		t.Errorf("expected nil results when no issues, got %d results", len(results))
	}
}

func TestCheckCloudDoctor_MultipleSeverities(t *testing.T) {
	// setup mock server that returns issues with different severities
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/doctor") {
			w.WriteHeader(http.StatusOK)
			resp := api.DoctorResponse{
				Issues: []api.DoctorIssue{
					{
						Type:        "critical_issue",
						Severity:    "error",
						Title:       "Critical Issue",
						Description: "This is critical",
					},
					{
						Type:        "minor_issue",
						Severity:    "warning",
						Title:       "Minor Issue",
						Description: "This is a warning",
					},
					{
						Type:        "info_issue",
						Severity:    "info",
						Title:       "Info Issue",
						Description: "This is informational",
					},
				},
				CheckedAt: "2025-01-01T00:00:00Z",
			}
			json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	t.Setenv("SAGEOX_ENDPOINT", mockServer.URL)

	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox dir: %v", err)
	}

	cfg := &config.ProjectConfig{
		RepoID:        "repo_test123",
		ConfigVersion: config.CurrentConfigVersion,
	}
	if err := config.SaveProjectConfig(gitRoot, cfg); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	results := checkCloudDoctor()

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// verify error severity
	if results[0].passed {
		t.Error("error severity should have passed=false")
	}

	// verify warning severity
	if !results[1].passed || !results[1].warning {
		t.Error("warning severity should have passed=true, warning=true")
	}

	// verify info severity
	if !results[2].passed || results[2].warning {
		t.Error("info severity should have passed=true, warning=false")
	}
}
