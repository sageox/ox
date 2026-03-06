package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQuery_Success(t *testing.T) {
	t.Parallel()
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/api/v1/query", r.URL.Path)
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		body, _ := io.ReadAll(r.Body)
		var req QueryRequest
		require.NoError(t, json.Unmarshal(body, &req))
		assert.Equal(t, "architecture decisions", req.Query)
		assert.Equal(t, "hybrid", req.Mode)
		assert.Equal(t, 5, req.K)
		assert.Equal(t, []string{"team-1"}, req.Teams)
		assert.Equal(t, []string{"repo-1"}, req.Repos)

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"results": [
				{
					"score": 0.95,
					"text": "We decided to use PostgreSQL for all persistent storage.",
					"doc_type": "discussion",
					"file_path": "discussions/2025-01-10-architecture.md",
					"source_type": "team_context",
					"source_id": "team-1",
					"created_at": "2025-01-10T14:30:00Z"
				}
			],
			"latency_ms": {
				"embed": 12,
				"search": 45,
				"total": 57
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

	resp, err := client.Query(&QueryRequest{
		Query: "architecture decisions",
		Mode:  "hybrid",
		K:     5,
		Teams: []string{"team-1"},
		Repos: []string{"repo-1"},
	})

	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Len(t, resp.Results, 1)
	assert.InDelta(t, 0.95, resp.Results[0].Score, 0.001)
	assert.Equal(t, "discussion", resp.Results[0].DocType)
	assert.Equal(t, "team_context", resp.Results[0].SourceType)
	assert.Equal(t, int64(57), resp.LatencyMs.Total)
}

func TestQuery_Unauthorized(t *testing.T) {
	t.Parallel()
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error": "invalid token"}`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "bad-token",
	}

	resp, err := client.Query(&QueryRequest{
		Query: "test",
		Teams: []string{"team-1"},
	})

	assert.Nil(t, resp)
	assert.ErrorIs(t, err, ErrUnauthorized)
}

func TestQuery_ServerError(t *testing.T) {
	t.Parallel()
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "test-token",
	}

	resp, err := client.Query(&QueryRequest{
		Query: "test",
		Teams: []string{"team-1"},
	})

	assert.Nil(t, resp)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}

func TestQuery_NetworkError(t *testing.T) {
	t.Parallel()
	client := &RepoClient{
		baseURL:    "http://localhost:99999",
		httpClient: &http.Client{Timeout: 2 * time.Second},
		version:    "test-version",
		authToken:  "test-token",
	}

	resp, err := client.Query(&QueryRequest{
		Query: "test",
		Teams: []string{"team-1"},
	})

	assert.Nil(t, resp)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "network error")
}

func TestQuery_MalformedJSON(t *testing.T) {
	t.Parallel()
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`not valid json`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "test-token",
	}

	resp, err := client.Query(&QueryRequest{
		Query: "test",
		Teams: []string{"team-1"},
	})

	assert.Nil(t, resp)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode")
}

func TestQuery_EmptyResults(t *testing.T) {
	t.Parallel()
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"results": [], "latency_ms": {"embed": 5, "search": 10, "total": 15}}`))
	}))
	defer mockServer.Close()

	client := &RepoClient{
		baseURL:    mockServer.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    "test-version",
		authToken:  "test-token",
	}

	resp, err := client.Query(&QueryRequest{
		Query: "nonexistent topic",
		Teams: []string{"team-1"},
	})

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Empty(t, resp.Results)
	assert.Equal(t, int64(15), resp.LatencyMs.Total)
}
