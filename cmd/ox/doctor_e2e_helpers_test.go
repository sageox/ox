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
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
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

type mockSageoxAPI struct {
	*testguard.MockServer
	cloudDoctorAuthedCalls   atomic.Int32
	cloudDoctorLegacyCalls   atomic.Int32
	cloudDoctorSingularCalls atomic.Int32
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

	// XDG overrides isolate from real config; testguard.MinimalEnv adds OX_NO_DAEMON=1.
	// Route non-local HTTP through a closed port so this mock acceptance test
	// cannot silently depend on GitHub or another real service.
	return []string{
		"OX_XDG_ENABLE=1",
		"HTTP_PROXY=http://127.0.0.1:1",
		"HTTPS_PROXY=http://127.0.0.1:1",
		"NO_PROXY=127.0.0.1,localhost",
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
func startMockSageoxAPI(t *testing.T) *mockSageoxAPI {
	t.Helper()

	mux := http.NewServeMux()
	mock := &mockSageoxAPI{}

	// GET /api/v1/auth/introspect -- current CLI authentication contract.
	// Keeping this in the acceptance server prevents a stale hand-written
	// auth.json from masquerading as a valid cloud credential.
	mux.HandleFunc("/api/v1/auth/introspect", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer test-access-token-fresh-install" {
			http.Error(w, `{"error":"invalid_token"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"active":         true,
			"principal_kind": "user",
			"scope":          "user:profile sageox:write",
			"token_type":     "Bearer",
			"expires_at":     nil,
			"user": map[string]string{
				"id":    "user_test123",
				"email": "test@example.com",
				"name":  "Test User",
				"tier":  "test",
			},
		})
	})

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

	// GET /api/v1/repos/{repo_id}/doctor -- cloud doctor. Route-specific
	// counters ensure the acceptance test cannot green on a legacy fallback.
	cloudDoctorHandler := func(calls *atomic.Int32, requireAuth bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if requireAuth && r.Header.Get("Authorization") != "Bearer test-access-token-fresh-install" {
				http.Error(w, `{"error":"invalid_token"}`, http.StatusUnauthorized)
				return
			}
			if r.Method == http.MethodGet {
				switch {
				case strings.HasSuffix(r.URL.Path, "/ledger-status"):
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(api.LedgerStatusResponse{
						Status:     "ready",
						RepoURL:    "https://localhost:1/ledger-test.git",
						RepoID:     123,
						CreatedAt:  time.Now().Format(time.RFC3339),
						Visibility: "private",
					})
					return
				case strings.HasSuffix(r.URL.Path, "/doctor"):
					calls.Add(1)
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]any{
						"issues":     []any{},
						"checked_at": time.Now().Format(time.RFC3339),
					})
					return
				}
			}
			http.Error(w, "not found", http.StatusNotFound)
		}
	}
	mux.HandleFunc("/api/v1/repo/", cloudDoctorHandler(&mock.cloudDoctorSingularCalls, false))
	mux.HandleFunc("/api/v1/repos/", cloudDoctorHandler(&mock.cloudDoctorAuthedCalls, true))
	mux.HandleFunc("/api/v1/public/repos/", cloudDoctorHandler(&mock.cloudDoctorLegacyCalls, false))

	// GET /api/v1/teams/{id} -- team info
	mux.HandleFunc("/api/v1/teams/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":   "team_test123",
			"name": "Test Team",
			"slug": "test-team",
		})
	})

	mock.MockServer = testguard.SafeMockServer(t, mux)
	return mock
}

// parseDoctorJSON parses the JSON output from ox doctor --json. The compiled
// binary may emit logs before or after a pretty-printed JSON payload.
func parseDoctorJSON(t *testing.T, output string) *JSONDoctorOutput {
	t.Helper()

	result, err := parseDoctorJSONOutput(output)
	if err != nil {
		t.Logf("no valid JSON found in doctor output:\n%s", output)
		return nil
	}
	return result
}

func parseDoctorJSONOutput(output string) (*JSONDoctorOutput, error) {
	for offset := 0; offset < len(output); {
		relative := strings.IndexByte(output[offset:], '{')
		if relative < 0 {
			break
		}
		offset += relative

		var candidate JSONDoctorOutput
		if err := json.NewDecoder(strings.NewReader(output[offset:])).Decode(&candidate); err == nil && len(candidate.Categories) > 0 {
			return &candidate, nil
		}
		offset++
	}

	return nil, fmt.Errorf("doctor output contained no JSON result with categories")
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
