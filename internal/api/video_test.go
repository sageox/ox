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

func newTestClient(url string) *RepoClient {
	return &RepoClient{
		baseURL:    url,
		httpClient: &http.Client{Timeout: 5 * time.Second},
		authToken:  "test-token",
		version:    "test",
	}
}

// ============================================================================
// ImportVideoURL Tests
// ============================================================================

func TestImportVideoURL_Success(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/api/v1/teams/team-123/recordings/import/url", r.URL.Path)
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))

		body, _ := io.ReadAll(r.Body)
		var req ImportVideoURLRequest
		require.NoError(t, json.Unmarshal(body, &req))
		assert.Equal(t, "https://example.com/meeting.mp4", req.URL)
		assert.Equal(t, "Team Standup", req.Title)

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(ImportVideoURLResponse{
			RecordingID: "rec_abc123",
			Status:      "processing",
			Title:       "Team Standup",
		})
	}))
	defer server.Close()

	client := newTestClient(server.URL)
	resp, err := client.ImportVideoURL("team-123", &ImportVideoURLRequest{
		URL:   "https://example.com/meeting.mp4",
		Title: "Team Standup",
	})

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "rec_abc123", resp.RecordingID)
	assert.Equal(t, "processing", resp.Status)
	assert.Equal(t, "Team Standup", resp.Title)
}

func TestImportVideoURL_404_ReturnsNil(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := newTestClient(server.URL)
	resp, err := client.ImportVideoURL("team-123", &ImportVideoURLRequest{URL: "https://example.com/v.mp4"})

	assert.NoError(t, err)
	assert.Nil(t, resp)
}

func TestImportVideoURL_500_ReturnsError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("server error"))
	}))
	defer server.Close()

	client := newTestClient(server.URL)
	resp, err := client.ImportVideoURL("team-123", &ImportVideoURLRequest{URL: "https://example.com/v.mp4"})

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "500")
}

// ============================================================================
// GetVideoStatus Tests
// ============================================================================

func TestGetVideoStatus_Success(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/api/v1/teams/team-123/recordings/rec_abc", r.URL.Path)

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(VideoStatusResponse{
			ID:     "rec_abc",
			Title:  "Standup",
			Status: "ready",
		})
	}))
	defer server.Close()

	client := newTestClient(server.URL)
	resp, err := client.GetVideoStatus("team-123", "rec_abc")

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "rec_abc", resp.ID)
	assert.Equal(t, "ready", resp.Status)
}

func TestGetVideoStatus_404_ReturnsNil(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := newTestClient(server.URL)
	resp, err := client.GetVideoStatus("team-123", "rec_nonexistent")

	assert.NoError(t, err)
	assert.Nil(t, resp)
}

func TestGetVideoStatus_NetworkError_ReturnsError(t *testing.T) {
	t.Parallel()
	// use a closed server to simulate network error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close()

	client := newTestClient(server.URL)
	resp, err := client.GetVideoStatus("team-123", "rec_abc")

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "get video status")
}

func TestGetVideoStatus_500_ReturnsError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	}))
	defer server.Close()

	client := newTestClient(server.URL)
	resp, err := client.GetVideoStatus("team-123", "rec_abc")

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "500")
}

// ============================================================================
// ListVideos Tests
// ============================================================================

func TestListVideos_Success(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/api/v1/teams/team-123/recordings", r.URL.Path)
		assert.Equal(t, "50", r.URL.Query().Get("limit"))
		assert.Equal(t, "0", r.URL.Query().Get("offset"))

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(ListVideosResponse{
			Recordings: []RecordingListItem{
				{ID: "rec_1", Title: "Meeting A", Status: "ready"},
				{ID: "rec_2", Title: "Meeting B", Status: "processing"},
			},
			Pagination: PaginationResponse{Total: 2, Limit: 50, Offset: 0, HasMore: false},
		})
	}))
	defer server.Close()

	client := newTestClient(server.URL)
	resp, err := client.ListVideos("team-123", 50, 0)

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Len(t, resp.Recordings, 2)
	assert.Equal(t, "rec_1", resp.Recordings[0].ID)
	assert.Equal(t, 2, resp.Pagination.Total)
	assert.False(t, resp.Pagination.HasMore)
}

func TestListVideos_404_ReturnsNil(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := newTestClient(server.URL)
	resp, err := client.ListVideos("team-123", 50, 0)

	assert.NoError(t, err)
	assert.Nil(t, resp)
}

func TestListVideos_500_ReturnsError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	}))
	defer server.Close()

	client := newTestClient(server.URL)
	resp, err := client.ListVideos("team-123", 50, 0)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "500")
}

func TestListVideos_NetworkError_ReturnsError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close()

	client := newTestClient(server.URL)
	resp, err := client.ListVideos("team-123", 50, 0)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "list videos")
}

func TestListVideos_EmptyResponse(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(ListVideosResponse{
			Recordings: []RecordingListItem{},
			Pagination: PaginationResponse{Total: 0, Limit: 50, Offset: 0, HasMore: false},
		})
	}))
	defer server.Close()

	client := newTestClient(server.URL)
	resp, err := client.ListVideos("team-123", 50, 0)

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Empty(t, resp.Recordings)
	assert.Equal(t, 0, resp.Pagination.Total)
}
