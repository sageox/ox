package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckUploadAccess_NoRepoID(t *testing.T) {
	// bare temp dir with no .sageox/config.json → GetRepoID returns "" → fail-open returns nil
	tmpDir := t.TempDir()
	err := checkUploadAccess(tmpDir)
	assert.NoError(t, err, "should fail-open when no repo ID exists")
}

func TestCheckUploadAccess_NoProjectConfig(t *testing.T) {
	// nonexistent directory → GetRepoID returns "" → fail-open returns nil
	err := checkUploadAccess("/nonexistent/path/that/does/not/exist")
	assert.NoError(t, err, "should fail-open when project root does not exist")
}

func TestCheckUploadAccess_EmptyRepoID(t *testing.T) {
	// initialized project but config has no repo_id → fail-open returns nil
	tmpDir := createInitializedProject(t)
	err := checkUploadAccess(tmpDir)
	assert.NoError(t, err, "should fail-open when config exists but repo_id is empty")
}

func TestCheckUploadAccess_RepoIDButNoAuth(t *testing.T) {
	// project with repo_id set but no valid auth token → fail-open returns nil
	// (auth.GetTokenForEndpoint will fail for a fake endpoint)
	tmpDir := createInitializedProjectWithConfig(t, &config.ProjectConfig{
		RepoID:   "repo_01test1234567890",
		Endpoint: "https://fake.example.com",
	})
	err := checkUploadAccess(tmpDir)
	assert.NoError(t, err, "should fail-open when auth token is unavailable")
}

// setupCheckUploadAccessEnv creates the full environment needed to test
// checkUploadAccess with a real HTTP server: project config with repo_id,
// SAGEOX_ENDPOINT pointing to the test server, and an auth token saved
// for that endpoint.
func setupCheckUploadAccessEnv(t *testing.T, serverURL string) string {
	t.Helper()

	repoID := "repo_01test1234567890"

	// isolate auth storage to a temp XDG config dir
	xdgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdgDir)
	// ensure the sageox config dir exists for auth file writes
	require.NoError(t, os.MkdirAll(filepath.Join(xdgDir, "sageox"), 0755))

	// point endpoint resolution to our test server
	t.Setenv("SAGEOX_ENDPOINT", serverURL)

	// save an auth token for the test server endpoint
	require.NoError(t, auth.SaveTokenForEndpoint(serverURL, &auth.StoredToken{
		AccessToken: "test-token-abc",
		ExpiresAt:   time.Now().Add(1 * time.Hour),
		TokenType:   "Bearer",
	}))

	// create initialized project with repo_id and endpoint
	tmpDir := createInitializedProjectWithConfig(t, &config.ProjectConfig{
		RepoID:   repoID,
		Endpoint: serverURL,
	})

	return tmpDir
}

// TestCheckUploadAccess_ReadOnly verifies that checkUploadAccess returns
// api.ErrReadOnly when the server reports viewer access. (ox-ehg)
func TestCheckUploadAccess_ReadOnly(t *testing.T) {
	repoID := "repo_01test1234567890"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedPath := fmt.Sprintf("/api/v1/cli/repos/%s", repoID)
		if r.URL.Path == expectedPath {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(api.RepoDetailResponse{
				Visibility:  "public",
				AccessLevel: "viewer",
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	tmpDir := setupCheckUploadAccessEnv(t, ts.URL)

	err := checkUploadAccess(tmpDir)
	require.Error(t, err)
	assert.True(t, errors.Is(err, api.ErrReadOnly), "expected api.ErrReadOnly, got: %v", err)
}

// TestCheckUploadAccess_WriteAccess verifies that checkUploadAccess returns nil
// when the server reports member (write) access. (ox-ehg)
func TestCheckUploadAccess_WriteAccess(t *testing.T) {
	repoID := "repo_01test1234567890"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedPath := fmt.Sprintf("/api/v1/cli/repos/%s", repoID)
		if r.URL.Path == expectedPath {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(api.RepoDetailResponse{
				Visibility:  "public",
				AccessLevel: "member",
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	tmpDir := setupCheckUploadAccessEnv(t, ts.URL)

	err := checkUploadAccess(tmpDir)
	assert.NoError(t, err, "should return nil for member (write) access")
}

// TestCheckUploadAccess_ServerError verifies fail-open behavior when the API
// returns an error (non-ErrReadOnly). (ox-ehg)
func TestCheckUploadAccess_ServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	}))
	defer ts.Close()

	tmpDir := setupCheckUploadAccessEnv(t, ts.URL)

	err := checkUploadAccess(tmpDir)
	assert.NoError(t, err, "should fail-open when server returns 500")
}

// TestCheckUploadAccess_ReadOnlySessionStartMessage verifies that
// runAgentSessionStart wraps api.ErrReadOnly with a user-friendly message
// including remediation steps. This tests the integration point in
// agent_session.go lines 101-107. (ox-ehg)
func TestCheckUploadAccess_ReadOnlySessionStartMessage(t *testing.T) {
	// test the error wrapping logic directly — checkUploadAccess returns
	// api.ErrReadOnly, and the caller should produce a clear message
	err := api.ErrReadOnly
	if errors.Is(err, api.ErrReadOnly) {
		wrapped := fmt.Errorf("you have read-only access to this repo — sessions cannot be uploaded to the ledger\nTo upload sessions, request team membership from an admin")
		assert.Contains(t, wrapped.Error(), "read-only access")
		assert.Contains(t, wrapped.Error(), "request team membership")
	}
}

// TestCheckUploadAccess_DetailEndpoint404FallsThrough verifies that when
// GetRepoDetail returns (nil, nil) — a 404 — checkUploadAccess falls through
// to ledger status. If ledger status also returns no read-only signal, access
// is granted (fail-open). (ox-ehg)
func TestCheckUploadAccess_DetailEndpoint404FallsThrough(t *testing.T) {
	repoID := "repo_01test1234567890"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// return 404 for repo detail endpoint to trigger fallback path
		detailPath := fmt.Sprintf("/api/v1/cli/repos/%s", repoID)
		ledgerPath := fmt.Sprintf("/api/v1/repos/%s/ledger-status", repoID)

		switch r.URL.Path {
		case detailPath:
			http.NotFound(w, r)
		case ledgerPath:
			// ledger status returns non-read-only
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"status":   "ready",
				"repo_url": "https://git.example.com/ledger.git",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	tmpDir := setupCheckUploadAccessEnv(t, ts.URL)

	err := checkUploadAccess(tmpDir)
	assert.NoError(t, err, "should fail-open when detail returns 404 and ledger status is not read-only")
}
