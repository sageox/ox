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

// TestListTeamRoster_Success — Failure prevented: a well-formed roster response
// fails to decode, so `ox team members` shows nothing even when the server
// answered correctly.
func TestListTeamRoster_Success(t *testing.T) {
	t.Parallel()
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.True(t, strings.HasSuffix(r.URL.Path, "/api/v1/teams/team_abc/roster"))
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"team_id": "team_abc",
			"total": 2,
			"members": [
				{"principal_id": "usr_ryan", "name": "Ryan Snodgrass", "type": "human", "role": "owner", "aliases": ["rsnodgrass"]},
				{"principal_id": "agt_rip", "name": "Rip", "type": "ai", "role": "member"}
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

	resp, err := client.ListTeamRoster(context.Background(), "team_abc")
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "team_abc", resp.TeamID)
	require.Len(t, resp.Members, 2)
	assert.Equal(t, "usr_ryan", resp.Members[0].PrincipalID)
	assert.Equal(t, "human", resp.Members[0].Type)
	assert.Equal(t, []string{"rsnodgrass"}, resp.Members[0].Aliases)
	assert.Equal(t, "agt_rip", resp.Members[1].PrincipalID)
	assert.Equal(t, "ai", resp.Members[1].Type)
}

// TestListTeamRoster_NotFound — Failure prevented: a 404 (feature flag off or
// route not deployed) is treated as a hard error instead of the graceful
// ErrTeamRosterUnsupported sentinel the command degrades on.
func TestListTeamRoster_NotFound(t *testing.T) {
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

	resp, err := client.ListTeamRoster(context.Background(), "team_missing")
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.True(t, errors.Is(err, ErrTeamRosterUnsupported),
		"404 must return ErrTeamRosterUnsupported sentinel, got %v", err)
}

// TestListTeamRoster_Forbidden — Failure prevented: a non-member's 403 leaks as
// a generic error instead of the canonical ErrForbidden.
func TestListTeamRoster_Forbidden(t *testing.T) {
	t.Parallel()
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden"}`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "test-token",
	}

	resp, err := client.ListTeamRoster(context.Background(), "team_abc")
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.True(t, errors.Is(err, ErrForbidden), "403 must return ErrForbidden, got %v", err)
}

// TestListTeamRoster_MalformedJSON — Failure prevented: a 200 with a body that
// isn't valid JSON is swallowed into an empty roster instead of surfacing a
// decode error.
func TestListTeamRoster_MalformedJSON(t *testing.T) {
	t.Parallel()
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{not valid json`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "test-token",
	}
	resp, err := client.ListTeamRoster(context.Background(), "team_abc")
	require.Error(t, err)
	assert.Nil(t, resp)
}

// TestListTeamRoster_ServerError — Failure prevented: a 5xx is silently treated
// as the graceful ErrTeamRosterUnsupported sentinel and reported as "feature
// unavailable, exit 0" instead of a real error.
func TestListTeamRoster_ServerError(t *testing.T) {
	t.Parallel()
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "test-token",
	}
	resp, err := client.ListTeamRoster(context.Background(), "team_abc")
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.False(t, errors.Is(err, ErrTeamRosterUnsupported), "5xx must not be swallowed as unsupported")
}

// TestListTeamRoster_EmptyRef — Failure prevented: an empty team ref silently
// issues a request to /api/v1/teams//roster.
func TestListTeamRoster_EmptyRef(t *testing.T) {
	t.Parallel()
	client := &RepoClient{
		baseURL:    "http://localhost:0",
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "test-token",
	}
	resp, err := client.ListTeamRoster(context.Background(), "")
	require.Error(t, err)
	assert.Nil(t, resp)
}
