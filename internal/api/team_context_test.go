package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetTeamContextContent_Success — Failure prevented: HTTP fallback for
// ephemeral mode fails to decode a well-formed response, leaving primes
// with no team context even when the cloud endpoint responded correctly.
func TestGetTeamContextContent_Success(t *testing.T) {
	t.Parallel()
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.True(t, strings.HasSuffix(r.URL.Path, "/api/v1/teams/team_abc/context"))
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"team_id": "team_abc",
			"team_name": "Test Team",
			"docs": [
				{"name": "guides/onboarding.md", "title": "Onboarding", "content": "# Hello"}
			],
			"agents_md": "# AGENTS\n",
			"claude_md": "# CLAUDE\n",
			"memory": "# MEMORY\n"
		}`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "test-token",
	}

	resp, err := client.GetTeamContextContent(context.Background(), "team_abc")
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "team_abc", resp.TeamID)
	assert.Equal(t, "Test Team", resp.TeamName)
	require.Len(t, resp.Docs, 1)
	assert.Equal(t, "guides/onboarding.md", resp.Docs[0].Name)
	assert.Equal(t, "# Hello", resp.Docs[0].Content)
	assert.Equal(t, "# AGENTS\n", resp.AgentsMD)
	assert.Equal(t, "# CLAUDE\n", resp.ClaudeMD)
	assert.Equal(t, "# MEMORY\n", resp.Memory)
}

// TestGetTeamContextContent_NotFound — Failure prevented: 404 (endpoint
// not yet deployed) is treated as a hard error instead of a graceful
// fall-through sentinel, breaking prime in ephemeral mode against older
// servers.
func TestGetTeamContextContent_NotFound(t *testing.T) {
	t.Parallel()
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "test-token",
	}

	resp, err := client.GetTeamContextContent(context.Background(), "team_missing")
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.True(t, errors.Is(err, ErrTeamContextEndpointUnavailable),
		"404 must return ErrTeamContextEndpointUnavailable sentinel, got %v", err)
}

// TestGetTeamContextContent_Unauthorized — Failure prevented: 401 leaks as
// a generic error instead of the canonical ErrUnauthorized, so the CLI
// can't distinguish "log in" from real failures.
func TestGetTeamContextContent_Unauthorized(t *testing.T) {
	t.Parallel()
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "bad-token",
	}

	resp, err := client.GetTeamContextContent(context.Background(), "team_abc")
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.True(t, errors.Is(err, ErrUnauthorized),
		"401 must return ErrUnauthorized, got %v", err)
}

// TestGetTeamContextContent_EmptyTeamID — Failure prevented: an empty
// team_id silently issues a request to /api/v1/teams//context, which
// some servers will misinterpret as a list-all call.
func TestGetTeamContextContent_EmptyTeamID(t *testing.T) {
	t.Parallel()
	client := &RepoClient{
		baseURL:    "http://localhost:0",
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "test-token",
	}
	resp, err := client.GetTeamContextContent(context.Background(), "")
	require.Error(t, err)
	assert.Nil(t, resp)
}
