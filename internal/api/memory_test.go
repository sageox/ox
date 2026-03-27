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

func TestDistillMemory_Success(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Contains(t, r.URL.Path, "/api/v1/teams/team_abc/memory/distill")
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))

		// verify request body
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		var req DistillRequest
		require.NoError(t, json.Unmarshal(body, &req))
		assert.Len(t, req.Observations, 2)
		assert.Equal(t, "obs1", req.Observations[0].Content)

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(DistillResponse{
			Summary:   "Distilled summary of observations",
			UpdatedAt: "2025-01-01T00:00:00Z",
		})
	}))
	defer srv.Close()

	client := &RepoClient{
		baseURL:    srv.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		authToken:  "test-token",
	}

	req := &DistillRequest{
		Observations: []DistillObservation{
			{Content: "obs1", RecordedAt: "2025-01-01T00:00:00Z"},
			{Content: "obs2", RecordedAt: "2025-01-02T00:00:00Z"},
		},
		DateRange: [2]string{"2025-01-01T00:00:00Z", "2025-01-02T00:00:00Z"},
	}

	resp, err := client.DistillMemory("team_abc", req)
	require.NoError(t, err)
	assert.Equal(t, "Distilled summary of observations", resp.Summary)
	assert.Equal(t, "2025-01-01T00:00:00Z", resp.UpdatedAt)
}

func TestDistillMemory_Unauthorized(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("invalid token"))
	}))
	defer srv.Close()

	client := &RepoClient{
		baseURL:    srv.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		authToken:  "bad-token",
	}

	_, err := client.DistillMemory("team_abc", &DistillRequest{})
	assert.ErrorIs(t, err, ErrUnauthorized)
}

func TestDistillMemory_ServerError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer srv.Close()

	client := &RepoClient{
		baseURL:    srv.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}

	_, err := client.DistillMemory("team_abc", &DistillRequest{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}

func TestDistillMemory_ServerErrorEmptyBody(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	client := &RepoClient{
		baseURL:    srv.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}

	_, err := client.DistillMemory("team_abc", &DistillRequest{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "502")
}

func TestDistillMemory_MalformedResponse(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not json"))
	}))
	defer srv.Close()

	client := &RepoClient{
		baseURL:    srv.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}

	_, err := client.DistillMemory("team_abc", &DistillRequest{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "decode response")
}

func TestDistillMemory_NetworkError(t *testing.T) {
	t.Parallel()

	client := &RepoClient{
		baseURL:    "http://127.0.0.1:1", // port that won't be listening
		httpClient: &http.Client{Timeout: 1 * time.Second},
	}

	_, err := client.DistillMemory("team_abc", &DistillRequest{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "network error")
}

func TestDistillMemory_NoAuthToken(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// verify no Authorization header sent
		assert.Empty(t, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(DistillResponse{Summary: "ok"})
	}))
	defer srv.Close()

	client := &RepoClient{
		baseURL:    srv.URL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		// no authToken set
	}

	resp, err := client.DistillMemory("team_abc", &DistillRequest{})
	require.NoError(t, err)
	assert.Equal(t, "ok", resp.Summary)
}
