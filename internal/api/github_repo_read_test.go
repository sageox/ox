package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGitHubRepoSnapshotsPollsAndSkipsKnown(t *testing.T) {
	id := strings.Repeat("a", 40)
	catalogs, blobs := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		if r.URL.Query().Get("view") == "catalog" {
			catalogs++
			fmt.Fprintf(w, `{"entries":[{"id":"%s","kind":"pr","number":7,"status":"ready"}],"complete":%t}`, id, catalogs > 1)
			return
		}
		blobs++
		fmt.Fprint(w, `{"number":7,"title":"thread"}`)
	}))
	defer server.Close()
	c := NewRepoClientWithEndpoint(server.URL).WithAuthToken("test-token")
	raw, entries, err := c.GitHubRepoSnapshots(context.Background(), "repo_1", nil)
	require.NoError(t, err)
	require.Len(t, raw, 1)
	require.Len(t, entries, 1)
	require.Equal(t, 2, catalogs)
	require.Equal(t, 1, blobs)
	_, entries, err = c.GitHubRepoSnapshots(context.Background(), "repo_1", map[string]bool{"pr:" + id: true})
	require.NoError(t, err)
	require.Empty(t, entries)
	require.Equal(t, 1, blobs)
}

func TestGitHubRepoSnapshotsUnavailableIsNotEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "busy", http.StatusServiceUnavailable) }))
	defer server.Close()
	c := NewRepoClientWithEndpoint(server.URL)
	_, _, err := c.GitHubRepoSnapshots(context.Background(), "repo_1", nil)
	require.ErrorContains(t, err, "HTTP 503")
}
