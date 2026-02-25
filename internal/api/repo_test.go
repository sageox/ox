package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// RegisterRepo Tests
// ======

func TestRegisterRepo_HTTP500_ReturnsErrorWithStatusAndURL(t *testing.T) {
	t.Parallel()
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"success":false,"error":"internal server error"}`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
	}

	req := &RepoInitRequest{
		RepoID: "repo_test123",
		Type:   "git",
		InitAt: "2025-01-01T00:00:00Z",
	}

	resp, err := client.RegisterRepo(req)

	// must return error
	require.Error(t, err)

	// must return nil response
	assert.Nil(t, resp)

	// error must include HTTP status code
	assert.Contains(t, err.Error(), "500")

	// error must include the URL
	assert.Contains(t, err.Error(), mockServer.URL)

	// error must include response body
	assert.Contains(t, err.Error(), "internal server error")
}

func TestRegisterRepo_HTTP400_ReturnsErrorWithResponseBody(t *testing.T) {
	t.Parallel()
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"success":false,"error":"invalid repo_id format"}`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
	}

	req := &RepoInitRequest{
		RepoID: "bad-id",
		Type:   "git",
		InitAt: "2025-01-01T00:00:00Z",
	}

	resp, err := client.RegisterRepo(req)

	require.Error(t, err)
	assert.Nil(t, resp)

	// error must include the API error message
	assert.Contains(t, err.Error(), "invalid repo_id format")
}

func TestRegisterRepo_HTTP404_ReturnsNilNil(t *testing.T) {
	t.Parallel()
	// 404 means endpoint not deployed - graceful degradation
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
	}

	req := &RepoInitRequest{
		RepoID: "repo_test123",
		Type:   "git",
		InitAt: "2025-01-01T00:00:00Z",
	}

	resp, err := client.RegisterRepo(req)

	// 404 should return (nil, nil) for graceful degradation
	assert.NoError(t, err)
	assert.Nil(t, resp)
}

func TestRegisterRepo_NetworkError_ReturnsDescriptiveError(t *testing.T) {
	t.Parallel()
	client := &RepoClient{
		baseURL:    "http://localhost:99999", // invalid port
		httpClient: &http.Client{Timeout: 1 * time.Second},
		version:    "test-version",
	}

	req := &RepoInitRequest{
		RepoID: "repo_test123",
		Type:   "git",
		InitAt: "2025-01-01T00:00:00Z",
	}

	resp, err := client.RegisterRepo(req)

	require.Error(t, err)
	assert.Nil(t, resp)

	// error should indicate network issue
	assert.Contains(t, err.Error(), "network error")
}

func TestRegisterRepo_Success_ReturnsResponse(t *testing.T) {
	t.Parallel()
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// verify request
		assert.Equal(t, "POST", r.Method)
		assert.True(t, strings.HasSuffix(r.URL.Path, "/api/v1/repo/init"))

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"repo_id":"repo_abc123","team_id":"team_xyz"}`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
	}

	req := &RepoInitRequest{
		RepoID: "repo_test123",
		Type:   "git",
		InitAt: "2025-01-01T00:00:00Z",
	}

	resp, err := client.RegisterRepo(req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "repo_abc123", resp.RepoID)
	assert.Equal(t, "team_xyz", resp.TeamID)
}

// ============================================================================
// GetDoctorIssues Tests
// ======

func TestGetDoctorIssues_Success_ReturnsIssues(t *testing.T) {
	t.Parallel()
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// verify request
		assert.Equal(t, "GET", r.Method)
		assert.True(t, strings.Contains(r.URL.Path, "/api/v1/public/repos/") && strings.HasSuffix(r.URL.Path, "/doctor"))

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"issues": [
				{
					"type": "merge_pending",
					"severity": "warning",
					"title": "Merge pending",
					"description": "Multiple teams working on this repo"
				}
			],
			"checked_at": "2025-01-01T00:00:00Z"
		}`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
	}

	resp, err := client.GetDoctorIssues("repo_test123")

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Len(t, resp.Issues, 1)
	assert.Equal(t, "merge_pending", resp.Issues[0].Type)
}

func TestGetDoctorIssues_HTTP500_ReturnsNilNil(t *testing.T) {
	t.Parallel()
	// GetDoctorIssues uses graceful degradation - 500 returns (nil, nil)
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"internal error"}`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
	}

	resp, err := client.GetDoctorIssues("repo_test123")

	// graceful degradation: 500 returns (nil, nil), NOT an error
	assert.NoError(t, err, "graceful degradation")
	assert.Nil(t, resp, "graceful degradation")
}

func TestGetDoctorIssues_HTTP404_ReturnsNilNil(t *testing.T) {
	t.Parallel()
	// 404 means endpoint not deployed - graceful degradation
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
	}

	resp, err := client.GetDoctorIssues("repo_test123")

	assert.NoError(t, err)
	assert.Nil(t, resp)
}

func TestGetDoctorIssues_NetworkError_ReturnsNilNil(t *testing.T) {
	t.Parallel()
	// graceful degradation for network errors
	client := &RepoClient{
		baseURL:    "http://localhost:99999", // invalid port
		httpClient: &http.Client{Timeout: 1 * time.Second},
		version:    "test-version",
	}

	resp, err := client.GetDoctorIssues("repo_test123")

	// graceful degradation: network error returns (nil, nil)
	assert.NoError(t, err, "graceful degradation")
	assert.Nil(t, resp)
}

func TestGetDoctorIssues_MalformedJSON_ReturnsNilNil(t *testing.T) {
	t.Parallel()
	// graceful degradation for malformed response
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`not valid json`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
	}

	resp, err := client.GetDoctorIssues("repo_test123")

	// graceful degradation: malformed response returns (nil, nil)
	assert.NoError(t, err, "graceful degradation")
	assert.Nil(t, resp)
}

func TestGetDoctorIssues_EmptyIssues_ReturnsEmptyList(t *testing.T) {
	t.Parallel()
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"issues":[],"checked_at":"2025-01-01T00:00:00Z"}`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
	}

	resp, err := client.GetDoctorIssues("repo_test123")

	require.NoError(t, err)
	require.NotNil(t, resp, "want response with empty issues")
	assert.Empty(t, resp.Issues)
}

func TestGetDoctorIssues_WithAuthToken(t *testing.T) {
	t.Parallel()
	var receivedAuth string

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"issues":[],"checked_at":"2025-01-01T00:00:00Z"}`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "test-token-123",
	}

	_, err := client.GetDoctorIssues("repo_test123")

	require.NoError(t, err)

	expectedAuth := "Bearer test-token-123"
	assert.Equal(t, expectedAuth, receivedAuth)
}

func TestNotifyUninstall_Success(t *testing.T) {
	t.Parallel()
	var receivedRepoID string
	var receivedRepoSalt string
	var receivedAuth string

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// extract repo_id from path: /api/v1/repo/{repo_id}/uninstall
		// path parts: ["", "api", "v1", "repo", "{repo_id}", "uninstall"]
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) >= 5 {
			receivedRepoID = parts[4]
		}
		receivedAuth = r.Header.Get("Authorization")

		// read body
		body, _ := io.ReadAll(r.Body)
		var req RepoUninstallRequest
		json.Unmarshal(body, &req)
		receivedRepoSalt = req.RepoSalt

		w.WriteHeader(http.StatusOK)
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "test-auth-token",
	}

	err := client.NotifyUninstall("repo_test123", "abc123hash")

	require.NoError(t, err)
	assert.Equal(t, "repo_test123", receivedRepoID)
	assert.Equal(t, "abc123hash", receivedRepoSalt)
	assert.Equal(t, "Bearer test-auth-token", receivedAuth)
}

func TestNotifyUninstall_EmptyRepoID(t *testing.T) {
	t.Parallel()

	client := &RepoClient{
		baseURL:    "https://example.com",
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
	}

	err := client.NotifyUninstall("", "abc123hash")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "repo_id is required")
}

func TestNotifyUninstall_Unauthorized(t *testing.T) {
	t.Parallel()

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
	}

	err := client.NotifyUninstall("repo_test123", "abc123hash")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "authentication required")
}

func TestNotifyUninstall_Forbidden(t *testing.T) {
	t.Parallel()

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "valid-token",
	}

	err := client.NotifyUninstall("repo_test123", "abc123hash")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not authorized")
}

func TestNotifyUninstall_NotFound(t *testing.T) {
	t.Parallel()

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "valid-token",
	}

	err := client.NotifyUninstall("repo_nonexistent", "abc123hash")

	// 404 should not be an error - repo might not have been registered
	require.NoError(t, err)
}

func TestNotifyUninstall_ServerError(t *testing.T) {
	t.Parallel()

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "valid-token",
	}

	err := client.NotifyUninstall("repo_test123", "abc123hash")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "server error")
}

