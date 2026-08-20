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

// TestListTeamRoster_ServerError — Failure prevented: a 5xx (server up but
// failing) is reported as a hard error or as the "route doesn't exist" sentinel,
// instead of the graceful "server unreachable" tier the command degrades on.
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
	assert.True(t, errors.Is(err, ErrTeamRosterUnavailable), "5xx must map to the graceful unavailable sentinel, got %v", err)
	assert.False(t, errors.Is(err, ErrTeamRosterUnsupported), "5xx is not the same as a missing route")
}

// TestListTeamRoster_ServerDown — Failure prevented: a server that is down /
// unreachable (connection refused, DNS failure, timeout) surfaces as a raw
// "network error" hard failure instead of the graceful unavailable sentinel.
func TestListTeamRoster_ServerDown(t *testing.T) {
	t.Parallel()
	client := &RepoClient{
		baseURL:    "http://127.0.0.1:1", // nothing listening → connection refused
		httpClient: &http.Client{Timeout: 2 * time.Second},
		version:    "test-version",
		authToken:  "test-token",
	}
	resp, err := client.ListTeamRoster(context.Background(), "team_abc")
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.True(t, errors.Is(err, ErrTeamRosterUnavailable), "an unreachable server must map to the graceful unavailable sentinel, got %v", err)
}

// TestListTeamRoster_OversizedBody — Failure prevented: a body larger than the
// cap is silently truncated by io.LimitReader and then fails to decode with a
// confusing JSON error, instead of being rejected explicitly as too large.
func TestListTeamRoster_OversizedBody(t *testing.T) {
	t.Parallel()
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// One byte over the cap is enough to trip detection.
		_, _ = w.Write(make([]byte, maxRosterBodyBytes+1))
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
	assert.Contains(t, err.Error(), "too large", "oversized body must be rejected explicitly")
	assert.False(t, errors.Is(err, ErrTeamRosterUnavailable), "oversized is a misbehaving server, not an unreachable one")
}

// TestListTeamRoster_OversizedServerError — Failure prevented: a 5xx whose error
// body exceeds the cap is rejected as "too large" instead of degrading to the
// graceful unavailable sentinel (status must be classified before the body read).
func TestListTeamRoster_OversizedServerError(t *testing.T) {
	t.Parallel()
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write(make([]byte, maxRosterBodyBytes+1))
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
	assert.True(t, errors.Is(err, ErrTeamRosterUnavailable), "a 5xx must degrade regardless of body size, got %v", err)
	assert.NotContains(t, err.Error(), "too large", "5xx must be classified before the body read")
}

// TestListTeamRoster_RejectsDotSegments — Failure prevented: a "." / ".." / path
// -separator team ref reaches the wire, where a normalizing proxy can rewrite the
// route and the 404 masquerades as "roster unavailable".
func TestListTeamRoster_RejectsDotSegments(t *testing.T) {
	t.Parallel()
	client := &RepoClient{
		baseURL:    "http://127.0.0.1:1", // must never be dialed — validation rejects first
		httpClient: &http.Client{Timeout: 2 * time.Second},
		version:    "test-version",
		authToken:  "test-token",
	}
	for _, ref := range []string{".", "..", "a/b", "team\\x"} {
		resp, err := client.ListTeamRoster(context.Background(), ref)
		require.Error(t, err, "ref %q must be rejected", ref)
		assert.Nil(t, resp)
		assert.Contains(t, err.Error(), "invalid team reference", "ref %q", ref)
		assert.False(t, errors.Is(err, ErrTeamRosterUnavailable), "ref %q must not look like an availability failure", ref)
	}
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
