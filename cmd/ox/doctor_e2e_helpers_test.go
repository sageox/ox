//go:build integration

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/testguard"
	"github.com/stretchr/testify/require"
)

// FreshInstallReport captures everything doctor finds after a fresh ox init.
// This is the primary deliverable -- it tells us exactly what breaks.
type FreshInstallReport struct {
	InitOutput   string
	InitExitCode int
	InitDuration time.Duration

	DoctorOutput   string
	DoctorExitCode int
	DoctorDuration time.Duration

	DoctorJSON *JSONDoctorOutput

	Warnings []ReportCheck
	Failures []ReportCheck
	Skipped  []ReportCheck
	Passed   []ReportCheck
}

// ReportCheck is a single doctor check result for the report.
type ReportCheck struct {
	Category string
	Name     string
	Status   string
	Priority string
	FixLevel string
	Message  string
	Detail   string
}

// buildOxBinary compiles the ox binary from source and returns its path.
func buildOxBinary(t *testing.T) string {
	t.Helper()

	// find project root by walking up from the test file
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "failed to get caller info")
	cmdOxDir := filepath.Dir(thisFile)
	projectRoot := filepath.Dir(filepath.Dir(cmdOxDir))

	return testguard.BuildOxBinary(t, projectRoot)
}

// cloneTestRepo does a shallow clone of a public git repo and returns the path.
func cloneTestRepo(t *testing.T, repoURL string) string {
	t.Helper()

	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")

	cmd := testguard.OxCmd(t, "git", tmpDir, nil, "clone", "--depth=1", repoURL, repoDir)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "failed to clone %s: %s", repoURL, string(out))

	return repoDir
}

// setupIsolatedAuth creates an isolated auth environment for subprocess tests.
// Returns environment variables to pass to testguard.RunOx.
func setupIsolatedAuth(t *testing.T, endpointURL string) []string {
	t.Helper()

	// create isolated config directory structure
	configDir := filepath.Join(t.TempDir(), "sageox-config")
	require.NoError(t, os.MkdirAll(configDir, 0700))

	// write a mock auth token
	authDir := filepath.Join(configDir, "sageox")
	require.NoError(t, os.MkdirAll(authDir, 0700))

	token := map[string]any{
		"tokens": map[string]any{
			endpointURL: map[string]any{
				"access_token":  "test-access-token-fresh-install",
				"refresh_token": "test-refresh-token",
				"expires_at":    time.Now().Add(24 * time.Hour).Format(time.RFC3339),
				"token_type":    "Bearer",
				"scope":         "user:profile sageox:write",
				"user_info": map[string]any{
					"user_id": "user_test123",
					"email":   "test@example.com",
					"name":    "Test User",
				},
			},
		},
	}
	tokenBytes, err := json.Marshal(token)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(authDir, "auth.json"), tokenBytes, 0600))

	// XDG overrides isolate from real config; testguard.MinimalEnv adds OX_NO_DAEMON=1
	return []string{
		"OX_XDG_ENABLE=1",
		fmt.Sprintf("XDG_CONFIG_HOME=%s", configDir),
		fmt.Sprintf("XDG_DATA_HOME=%s", filepath.Join(t.TempDir(), "data")),
		fmt.Sprintf("XDG_STATE_HOME=%s", filepath.Join(t.TempDir(), "state")),
		fmt.Sprintf("XDG_CACHE_HOME=%s", filepath.Join(t.TempDir(), "cache")),
		fmt.Sprintf("XDG_RUNTIME_DIR=%s", filepath.Join(t.TempDir(), "run")),
		fmt.Sprintf("SAGEOX_ENDPOINT=%s", endpointURL),
	}
}

// setupRealAuth creates auth environment using a real test token.
// Returns environment variables to pass to testguard.RunOx.
func setupRealAuth(t *testing.T, endpointURL, accessToken string) []string {
	t.Helper()

	configDir := filepath.Join(t.TempDir(), "sageox-config")
	require.NoError(t, os.MkdirAll(configDir, 0700))

	authDir := filepath.Join(configDir, "sageox")
	require.NoError(t, os.MkdirAll(authDir, 0700))

	token := map[string]any{
		"tokens": map[string]any{
			endpointURL: map[string]any{
				"access_token":  accessToken,
				"refresh_token": "",
				"expires_at":    time.Now().Add(24 * time.Hour).Format(time.RFC3339),
				"token_type":    "Bearer",
				"scope":         "user:profile sageox:write",
				"user_info": map[string]any{
					"user_id": "user_test",
					"email":   "test@example.com",
					"name":    "Test User",
				},
			},
		},
	}
	tokenBytes, err := json.Marshal(token)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(authDir, "auth.json"), tokenBytes, 0600))

	return []string{
		"OX_XDG_ENABLE=1",
		fmt.Sprintf("XDG_CONFIG_HOME=%s", configDir),
		fmt.Sprintf("XDG_DATA_HOME=%s", filepath.Join(t.TempDir(), "data")),
		fmt.Sprintf("XDG_STATE_HOME=%s", filepath.Join(t.TempDir(), "state")),
		fmt.Sprintf("XDG_CACHE_HOME=%s", filepath.Join(t.TempDir(), "cache")),
		fmt.Sprintf("XDG_RUNTIME_DIR=%s", filepath.Join(t.TempDir(), "run")),
		fmt.Sprintf("SAGEOX_ENDPOINT=%s", endpointURL),
	}
}

// startMockSageoxAPI creates a mock server that handles the API endpoints
// needed for ox init and ox doctor. Uses testguard.SafeMockServer to validate
// that responses never contain production URLs.
func startMockSageoxAPI(t *testing.T) *testguard.MockServer {
	t.Helper()

	mux := http.NewServeMux()

	// POST /api/v1/repo/init -- repo registration
	mux.HandleFunc("/api/v1/repo/init", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"repo_id":      "repo_01test000000000000000000",
			"team_id":      "team_test123",
			"web_base_url": "",
		})
	})

	// GET /api/v1/cli/repos -- team context repos + git credentials
	// git URLs use localhost:1 (unreachable) to avoid hitting real hosts
	mux.HandleFunc("/api/v1/cli/repos", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"token":      "mock-git-token",
			"server_url": "https://localhost:1",
			"username":   "test-user",
			"expires_at": time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"repos": map[string]any{
				"test-team-context": map[string]any{
					"name":    "test-team-context",
					"url":     "https://localhost:1/test-team-context.git",
					"type":    "team-context",
					"team_id": "team_test123",
				},
			},
			"teams": []map[string]any{
				{
					"id":   "team_test123",
					"name": "Test Team",
					"role": "owner",
				},
			},
		})
	})

	// GET /api/v1/cli/repos/{repo_id} -- repo detail
	mux.HandleFunc("/api/v1/cli/repos/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"visibility":   "private",
			"access_level": "member",
			"ledger": map[string]any{
				"status":   "ready",
				"repo_url": "https://localhost:1/ledger-test.git",
			},
			"team_contexts": []map[string]any{
				{
					"team_id":  "team_test123",
					"name":     "test-team-context",
					"repo_url": "https://localhost:1/test-team-context.git",
				},
			},
		})
	})

	// GET /api/v1/repo/{repo_id}/doctor -- cloud doctor
	mux.HandleFunc("/api/v1/repo/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/doctor") && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"issues":     []any{},
				"checked_at": time.Now().Format(time.RFC3339),
			})
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	})

	// GET /api/v1/teams/{id} -- team info
	mux.HandleFunc("/api/v1/teams/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":   "team_test123",
			"name": "Test Team",
			"slug": "test-team",
		})
	})

	return testguard.SafeMockServer(t, mux)
}

// parseDoctorJSON parses the JSON output from ox doctor --json.
// Output may include non-JSON lines (debug logs), so we try multiple strategies.
func parseDoctorJSON(t *testing.T, output string) *JSONDoctorOutput {
	t.Helper()

	// fast path: full output is clean JSON
	var result JSONDoctorOutput
	if err := json.Unmarshal([]byte(output), &result); err == nil {
		return &result
	}

	// fallback: scan line by line for a JSON object
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "{") {
			continue
		}
		if err := json.Unmarshal([]byte(trimmed), &result); err == nil {
			return &result
		}
	}

	// last resort: json.Decoder for streaming/multiline JSON
	dec := json.NewDecoder(strings.NewReader(output))
	for dec.More() {
		if err := dec.Decode(&result); err == nil {
			return &result
		} else {
			break // avoid infinite loop on malformed input
		}
	}

	t.Logf("no valid JSON found in doctor output:\n%s", output)
	return nil
}

// catalogReport categorizes all checks from the doctor JSON output into a report.
func catalogReport(doctorJSON *JSONDoctorOutput) *FreshInstallReport {
	report := &FreshInstallReport{
		DoctorJSON: doctorJSON,
	}

	if doctorJSON == nil {
		return report
	}

	for _, cat := range doctorJSON.Categories {
		catalogChecks(cat.Name, cat.Checks, report)
	}

	return report
}

// catalogChecks recursively categorizes checks into the report.
func catalogChecks(category string, checks []JSONCheckResult, report *FreshInstallReport) {
	for _, check := range checks {
		rc := ReportCheck{
			Category: category,
			Name:     check.Name,
			Status:   check.Status,
			Priority: check.Priority,
			FixLevel: check.FixLevel,
			Message:  check.Message,
			Detail:   check.Detail,
		}

		switch check.Status {
		case "passed":
			report.Passed = append(report.Passed, rc)
		case "warning":
			report.Warnings = append(report.Warnings, rc)
		case "failed":
			report.Failures = append(report.Failures, rc)
		case "skipped":
			report.Skipped = append(report.Skipped, rc)
		}

		if len(check.Children) > 0 {
			catalogChecks(category, check.Children, report)
		}
	}
}

// logReport logs a human-readable fresh install doctor report.
func logReport(t *testing.T, report *FreshInstallReport) {
	t.Helper()

	var b strings.Builder

	b.WriteString("\n")
	b.WriteString("========================================\n")
	b.WriteString("  FRESH INSTALL DOCTOR REPORT\n")
	b.WriteString("========================================\n")
	b.WriteString(fmt.Sprintf("  Init: exit=%d duration=%s\n", report.InitExitCode, report.InitDuration))
	b.WriteString(fmt.Sprintf("  Doctor: exit=%d duration=%s\n", report.DoctorExitCode, report.DoctorDuration))
	b.WriteString("========================================\n")

	if len(report.Failures) > 0 {
		b.WriteString(fmt.Sprintf("\nFAILURES (%d):\n", len(report.Failures)))
		for _, f := range report.Failures {
			b.WriteString(fmt.Sprintf("  [%s] %s > %s\n", f.Priority, f.Category, f.Name))
			b.WriteString(fmt.Sprintf("    message: %s\n", f.Message))
			if f.FixLevel != "" {
				b.WriteString(fmt.Sprintf("    fix: %s\n", f.FixLevel))
			}
			if f.Detail != "" {
				b.WriteString(fmt.Sprintf("    detail: %s\n", f.Detail))
			}
		}
	}

	if len(report.Warnings) > 0 {
		b.WriteString(fmt.Sprintf("\nWARNINGS (%d):\n", len(report.Warnings)))
		for _, w := range report.Warnings {
			b.WriteString(fmt.Sprintf("  [%s] %s > %s\n", w.Priority, w.Category, w.Name))
			b.WriteString(fmt.Sprintf("    message: %s\n", w.Message))
			if w.FixLevel != "" {
				b.WriteString(fmt.Sprintf("    fix: %s\n", w.FixLevel))
			}
			if w.Detail != "" {
				b.WriteString(fmt.Sprintf("    detail: %s\n", w.Detail))
			}
		}
	}

	if len(report.Skipped) > 0 {
		b.WriteString(fmt.Sprintf("\nSKIPPED (%d):\n", len(report.Skipped)))
		for _, s := range report.Skipped {
			b.WriteString(fmt.Sprintf("  %s > %s: %s\n", s.Category, s.Name, s.Message))
		}
	}

	b.WriteString(fmt.Sprintf("\nPASSED (%d):\n", len(report.Passed)))
	for _, p := range report.Passed {
		b.WriteString(fmt.Sprintf("  %s > %s: %s\n", p.Category, p.Name, p.Message))
	}

	b.WriteString("\n========================================\n")
	b.WriteString(fmt.Sprintf("  Summary: %d passed, %d warnings, %d failures, %d skipped\n",
		len(report.Passed), len(report.Warnings), len(report.Failures), len(report.Skipped)))
	b.WriteString("========================================\n")

	t.Log(b.String())

	if report.InitOutput != "" {
		t.Logf("\n--- RAW INIT OUTPUT ---\n%s\n--- END INIT OUTPUT ---\n", report.InitOutput)
	}
	if report.DoctorOutput != "" {
		t.Logf("\n--- RAW DOCTOR OUTPUT ---\n%s\n--- END DOCTOR OUTPUT ---\n", report.DoctorOutput)
	}
}
