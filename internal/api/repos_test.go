package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/useragent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ======
// GetRepos Tests - Critical path for credential fetching
// Called by: ox init, ox login, ox doctor --fix, daemon credential refresh
//
// NOTE: This API returns team context repos ONLY, not ledgers.
// Ledger URLs are fetched via GET /api/v1/repos/{repo_id}/ledger-status (project-scoped).
// ======

func TestGetRepos_Success(t *testing.T) {
	t.Parallel()
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// verify request
		assert.Equal(t, "GET", r.Method)
		assert.True(t, strings.HasSuffix(r.URL.Path, "/api/v1/cli/repos"))

		// verify auth header
		auth := r.Header.Get("Authorization")
		assert.Equal(t, "Bearer test-token", auth)

		// NOTE: This API only returns team contexts, not ledgers.
		// Ledger URLs come from GET /api/v1/repos/{repo_id}/ledger-status.
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"token": "git-pat-abc123",
			"server_url": "https://git.sageox.io",
			"username": "user@example.com",
			"expires_at": "2025-12-31T23:59:59Z",
			"repos": {
				"acme-team": {
					"name": "acme-team",
					"url": "https://git.sageox.io/teams/acme.git",
					"type": "team-context"
				},
				"other-team": {
					"name": "other-team",
					"url": "https://git.sageox.io/teams/other.git",
					"type": "team-context"
				}
			}
		}`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "test-token",
	}

	resp, err := client.GetRepos()

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "git-pat-abc123", resp.Token)
	assert.Equal(t, "https://git.sageox.io", resp.ServerURL)
	assert.Equal(t, "user@example.com", resp.Username)
	assert.Len(t, resp.Repos, 2)

	// verify team context repos (this API does NOT return ledgers)
	team, ok := resp.Repos["acme-team"]
	require.True(t, ok, "expected acme-team repo")
	assert.Equal(t, "team-context", team.Type)
	assert.Contains(t, team.URL, "acme.git")

	otherTeam, ok := resp.Repos["other-team"]
	require.True(t, ok, "expected other-team repo")
	assert.Equal(t, "team-context", otherTeam.Type)
}

func TestGetRepos_HTTP401_Unauthorized(t *testing.T) {
	t.Parallel()
	// 401 = expired token, should trigger re-auth flow
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"token expired"}`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "expired-token",
	}

	resp, err := client.GetRepos()

	require.Error(t, err)
	assert.Nil(t, resp)

	// error should indicate auth is needed
	assert.Contains(t, err.Error(), "authentication required")
	assert.Contains(t, err.Error(), "ox login")
}

func TestGetRepos_HTTP403_Forbidden(t *testing.T) {
	t.Parallel()
	// 403 = user doesn't have required permissions
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"insufficient permissions"}`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "valid-token",
	}

	resp, err := client.GetRepos()

	require.Error(t, err)
	assert.Nil(t, resp)

	// error should include status and message
	assert.Contains(t, err.Error(), "403")
	assert.Contains(t, err.Error(), "insufficient permissions")
}

func TestGetRepos_HTTP500_ServerError(t *testing.T) {
	t.Parallel()
	// 500 = server error, should fail gracefully
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"internal server error"}`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "valid-token",
	}

	resp, err := client.GetRepos()

	require.Error(t, err)
	assert.Nil(t, resp)

	// error should include status code
	assert.Contains(t, err.Error(), "500")
}

func TestGetRepos_NetworkError(t *testing.T) {
	t.Parallel()
	// network error - connection refused
	client := &RepoClient{
		baseURL:    "http://localhost:99999", // invalid port
		httpClient: &http.Client{Timeout: 1 * time.Second},
		version:    "test-version",
		authToken:  "valid-token",
	}

	resp, err := client.GetRepos()

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "network error")
}

func TestGetRepos_Timeout(t *testing.T) {
	t.Parallel()
	// server takes too long to respond
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second) // longer than client timeout
		w.WriteHeader(http.StatusOK)
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 100 * time.Millisecond},
		version:    "test-version",
		authToken:  "valid-token",
	}

	resp, err := client.GetRepos()

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "network error")

	// should contain timeout indicator
	errLower := strings.ToLower(err.Error())
	if !strings.Contains(errLower, "timeout") && !strings.Contains(errLower, "deadline") {
		t.Logf("note: error doesn't explicitly mention timeout: %v", err)
	}
}

func TestGetRepos_MalformedJSON(t *testing.T) {
	t.Parallel()
	// server returns invalid JSON - shouldn't panic
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`not valid json {{{`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "valid-token",
	}

	resp, err := client.GetRepos()

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "decode")
}

func TestGetRepos_EmptyReposList(t *testing.T) {
	t.Parallel()
	// empty repos list is valid - async provisioning scenario
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"token": "git-pat-abc123",
			"server_url": "https://git.sageox.io",
			"repos": {}
		}`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "valid-token",
	}

	resp, err := client.GetRepos()

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Empty(t, resp.Repos, "empty repos list is valid")
	assert.NotEmpty(t, resp.Token, "token should still be present")
}

func TestGetRepos_NullRepos(t *testing.T) {
	t.Parallel()
	// null repos field is valid
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"token": "git-pat-abc123",
			"server_url": "https://git.sageox.io",
			"repos": null
		}`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "valid-token",
	}

	resp, err := client.GetRepos()

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Nil(t, resp.Repos, "null repos is valid")
}

func TestGetRepos_NoAuthToken(t *testing.T) {
	t.Parallel()
	var receivedAuth string

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		// server would return 401 but we're testing header behavior
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		// no authToken
	}

	_, _ = client.GetRepos()

	// no auth header should be sent
	assert.Empty(t, receivedAuth, "no auth header when no token")
}

func TestGetRepos_UserAgentHeader(t *testing.T) {
	t.Parallel()
	var receivedUserAgent string

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedUserAgent = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"token":"t","server_url":"s","repos":{}}`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "1.2.3",
		authToken:  "token",
	}

	useragent.ResetForTesting()
	t.Cleanup(useragent.ResetForTesting)
	expectedUA := useragent.String()

	_, err := client.GetRepos()

	require.NoError(t, err)
	assert.Equal(t, expectedUA, receivedUserAgent)
}

func TestGetRepos_EmptyResponseBody(t *testing.T) {
	t.Parallel()
	// server returns 200 but empty body
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// empty body
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "valid-token",
	}

	resp, err := client.GetRepos()

	require.Error(t, err, "empty body should fail decode")
	assert.Nil(t, resp)
}

func TestGetRepos_HTTP500_EmptyBody(t *testing.T) {
	t.Parallel()
	// server returns 500 with no body
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		// no body
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "valid-token",
	}

	resp, err := client.GetRepos()

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "500")
}

func TestGetRepos_HandlesTrailingSlash(t *testing.T) {
	t.Parallel()
	var receivedPath string

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"token":"t","server_url":"s","repos":{}}`))
	}))
	defer mockServer.Close()

	// test with trailing slash in base URL
	client := &RepoClient{
		baseURL:    mockServer.URL + "/",
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "valid-token",
	}

	_, err := client.GetRepos()

	require.NoError(t, err)
	assert.Equal(t, "/api/v1/cli/repos", receivedPath, "should not have double slashes")
}

// ======
// GetTeamInfo Tests - Team context loading
// Called when: Lazy team loading from heartbeat, Team context setup
// ======

func TestGetTeamInfo_Success(t *testing.T) {
	t.Parallel()
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// verify request
		assert.Equal(t, "GET", r.Method)
		assert.Contains(t, r.URL.Path, "/api/v1/teams/")

		// verify auth header
		auth := r.Header.Get("Authorization")
		assert.Equal(t, "Bearer test-token", auth)

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id": "team_abc123",
			"name": "Acme Corp",
			"slug": "acme-corp",
			"repo_url": "https://git.sageox.io/teams/acme.git",
			"git_token": "team-git-pat"
		}`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "test-token",
	}

	resp, err := client.GetTeamInfo("team_abc123")

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "team_abc123", resp.ID)
	assert.Equal(t, "Acme Corp", resp.Name)
	assert.Equal(t, "acme-corp", resp.Slug)
	assert.Contains(t, resp.RepoURL, "acme.git")
	assert.NotEmpty(t, resp.GitToken)
}

func TestGetTeamInfo_HTTP404_NotFound(t *testing.T) {
	t.Parallel()
	// 404 = team not found, should return nil, nil (graceful)
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

	resp, err := client.GetTeamInfo("team_nonexistent")

	// graceful degradation: 404 returns (nil, nil)
	assert.NoError(t, err)
	assert.Nil(t, resp)
}

func TestGetTeamInfo_HTTP401_Unauthorized(t *testing.T) {
	t.Parallel()
	// 401 = expired token
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"token expired"}`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "expired-token",
	}

	resp, err := client.GetTeamInfo("team_abc123")

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "authentication required")
}

func TestGetTeamInfo_HTTP403_Forbidden(t *testing.T) {
	t.Parallel()
	// 403 = user is not a member of this team
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"not a team member"}`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "valid-token",
	}

	resp, err := client.GetTeamInfo("team_other")

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "access denied")
}

func TestGetTeamInfo_HTTP500_ServerError(t *testing.T) {
	t.Parallel()
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"database error"}`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "valid-token",
	}

	resp, err := client.GetTeamInfo("team_abc123")

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "500")
}

func TestGetTeamInfo_NetworkError(t *testing.T) {
	t.Parallel()
	client := &RepoClient{
		baseURL:    "http://localhost:99999",
		httpClient: &http.Client{Timeout: 1 * time.Second},
		version:    "test-version",
		authToken:  "valid-token",
	}

	resp, err := client.GetTeamInfo("team_abc123")

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "network error")
}

func TestGetTeamInfo_MalformedJSON(t *testing.T) {
	t.Parallel()
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`not json`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "valid-token",
	}

	resp, err := client.GetTeamInfo("team_abc123")

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "decode")
}

func TestGetTeamInfo_URLEncoding(t *testing.T) {
	t.Parallel()
	var receivedPath string

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"team_abc","name":"Test"}`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "valid-token",
	}

	_, err := client.GetTeamInfo("team_abc123")

	require.NoError(t, err)
	assert.Equal(t, "/api/v1/teams/team_abc123", receivedPath)
}

func TestGetTeamInfo_Timeout(t *testing.T) {
	t.Parallel()
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 100 * time.Millisecond},
		version:    "test-version",
		authToken:  "valid-token",
	}

	resp, err := client.GetTeamInfo("team_abc123")

	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestGetTeamInfo_MinimalResponse(t *testing.T) {
	t.Parallel()
	// server returns only required fields
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"team_abc","name":"Test"}`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "valid-token",
	}

	resp, err := client.GetTeamInfo("team_abc")

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "team_abc", resp.ID)
	assert.Equal(t, "Test", resp.Name)
	assert.Empty(t, resp.RepoURL, "optional field should be empty")
}

func TestGetTeamInfo_UserAgentHeader(t *testing.T) {
	t.Parallel()
	var receivedUserAgent string

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedUserAgent = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"t","name":"n"}`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "2.0.0",
		authToken:  "token",
	}

	useragent.ResetForTesting()
	t.Cleanup(useragent.ResetForTesting)
	expectedUA := useragent.String()

	_, err := client.GetTeamInfo("team_abc")

	require.NoError(t, err)
	assert.Equal(t, expectedUA, receivedUserAgent)
}

// ======
// GetRepoDetail Tests - Public repo support
// Called when: ox status, session upload access check, daemon sync
// Returns visibility + access level for both members and non-members on public repos
// ======

func TestGetRepoDetail_MemberOnPublicRepo(t *testing.T) {
	t.Parallel()
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Contains(t, r.URL.Path, "/api/v1/cli/repos/repo_abc123")
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"visibility": "public",
			"access_level": "member",
			"ledger": {
				"status": "ready",
				"repo_url": "https://git.example.com/org/ledger.git"
			},
			"team_contexts": [
				{
					"team_id": "team_abc",
					"name": "acme-corp",
					"visibility": "public",
					"access_level": "member",
					"repo_url": "https://git.example.com/teams/acme.git"
				}
			]
		}`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "test-token",
	}

	resp, err := client.GetRepoDetail("repo_abc123")

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "public", resp.Visibility)
	assert.Equal(t, "member", resp.AccessLevel)
	assert.False(t, resp.IsReadOnly())

	require.NotNil(t, resp.Ledger)
	assert.Equal(t, "ready", resp.Ledger.Status)
	assert.Contains(t, resp.Ledger.RepoURL, "ledger.git")

	require.Len(t, resp.TeamContexts, 1)
	assert.Equal(t, "team_abc", resp.TeamContexts[0].TeamID)
	assert.Equal(t, "acme-corp", resp.TeamContexts[0].Name)
	assert.Equal(t, "member", resp.TeamContexts[0].AccessLevel)
}

func TestGetRepoDetail_ViewerOnPublicRepo(t *testing.T) {
	t.Parallel()
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"visibility": "public",
			"access_level": "viewer",
			"ledger": {
				"status": "ready",
				"repo_url": "https://git.example.com/org/ledger.git"
			},
			"team_contexts": [
				{
					"team_id": "team_abc",
					"name": "acme-corp",
					"visibility": "public",
					"access_level": "viewer",
					"repo_url": "https://git.example.com/teams/acme.git"
				}
			]
		}`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "test-token",
	}

	resp, err := client.GetRepoDetail("repo_abc123")

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "viewer", resp.AccessLevel)
	assert.True(t, resp.IsReadOnly())
	assert.Equal(t, "viewer", resp.TeamContexts[0].AccessLevel)
}

func TestGetRepoDetail_ViewerNoTeamContexts(t *testing.T) {
	t.Parallel()
	// non-member on public repo but team context is private
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"visibility": "public",
			"access_level": "viewer",
			"ledger": {
				"status": "ready",
				"repo_url": "https://git.example.com/org/ledger.git"
			},
			"team_contexts": []
		}`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "test-token",
	}

	resp, err := client.GetRepoDetail("repo_abc123")

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.True(t, resp.IsReadOnly())
	assert.Empty(t, resp.TeamContexts)
}

func TestGetRepoDetail_HTTP403_PrivateRepo(t *testing.T) {
	t.Parallel()
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"access denied: you are not a member of this team"}`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "test-token",
	}

	resp, err := client.GetRepoDetail("repo_private")

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.ErrorIs(t, err, ErrForbidden)
}

func TestGetRepoDetail_HTTP404_EndpointNotImplemented(t *testing.T) {
	t.Parallel()
	// 404 = server hasn't implemented this endpoint yet — graceful fallback
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "test-token",
	}

	resp, err := client.GetRepoDetail("repo_abc123")

	// graceful: (nil, nil) so callers can fall back to GetLedgerStatus
	assert.NoError(t, err)
	assert.Nil(t, resp)
}

func TestGetRepoDetail_HTTP401_Unauthorized(t *testing.T) {
	t.Parallel()
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "expired-token",
	}

	resp, err := client.GetRepoDetail("repo_abc123")

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.ErrorIs(t, err, ErrUnauthorized)
}

func TestGetRepoDetail_LedgerPending(t *testing.T) {
	t.Parallel()
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"visibility": "public",
			"access_level": "member",
			"ledger": {
				"status": "pending",
				"repo_url": "",
				"message": "Ledger is being provisioned..."
			},
			"team_contexts": []
		}`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "test-token",
	}

	resp, err := client.GetRepoDetail("repo_abc123")

	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, resp.Ledger)
	assert.Equal(t, "pending", resp.Ledger.Status)
	assert.Empty(t, resp.Ledger.RepoURL)
	assert.Equal(t, "Ledger is being provisioned...", resp.Ledger.Message)
}

func TestGetRepoDetail_NullLedger(t *testing.T) {
	t.Parallel()
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"visibility": "public",
			"access_level": "member",
			"ledger": null,
			"team_contexts": []
		}`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "test-token",
	}

	resp, err := client.GetRepoDetail("repo_abc123")

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Nil(t, resp.Ledger)
}

func TestGetRepoDetail_NetworkError(t *testing.T) {
	t.Parallel()
	client := &RepoClient{
		baseURL:    "http://localhost:99999",
		httpClient: &http.Client{Timeout: 1 * time.Second},
		version:    "test-version",
		authToken:  "test-token",
	}

	resp, err := client.GetRepoDetail("repo_abc123")

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "network error")
}

func TestGetRepoDetail_URLPath(t *testing.T) {
	t.Parallel()
	var receivedPath string

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"visibility":"public","access_level":"member","team_contexts":[]}`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "test-token",
	}

	_, err := client.GetRepoDetail("repo_01jfk3mab")

	require.NoError(t, err)
	assert.Equal(t, "/api/v1/cli/repos/repo_01jfk3mab", receivedPath)
}

func TestRepoDetailResponse_IsReadOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		accessLevel string
		want        bool
	}{
		{"member", "member", false},
		{"viewer", "viewer", true},
		{"empty", "", false},
		{"unknown", "editor", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &RepoDetailResponse{AccessLevel: tt.accessLevel}
			assert.Equal(t, tt.want, r.IsReadOnly())
		})
	}
}

func TestLedgerStatusResponse_IsReadOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		accessLevel string
		want        bool
	}{
		{"member", "member", false},
		{"viewer", "viewer", true},
		{"empty (old server)", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &LedgerStatusResponse{AccessLevel: tt.accessLevel}
			assert.Equal(t, tt.want, r.IsReadOnly())
		})
	}
}
