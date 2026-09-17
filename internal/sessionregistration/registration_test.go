package sessionregistration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/require"
)

func TestRegistrationRetainsJobUntilCanonicalLayersReady(t *testing.T) {
	var state atomic.Value
	state.Store("pending")
	var posts atomic.Int64
	headers := make(chan string, 16)
	t.Cleanup(func() {
		close(headers)
		for header := range headers {
			require.Equal(t, "Bearer test-registration", header)
		}
	})
	job := Job{RepoID: "repo_019c6d2e-27b0-798d-aaed-b036114dc63a", SessionName: "2026-09-01-test"}
	meta := lfs.SessionMeta{RepoID: job.RepoID, SessionName: job.SessionName}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers <- r.Header.Get("Authorization")
		if r.Method == http.MethodPost {
			posts.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if state.Load().(string) == "deleted" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"repoId": job.RepoID, "sessionId": job.SessionName, "id": meta.EffectiveSessionID(), "processing_status": state.Load().(string)})
	}))
	defer server.Close()
	job.Endpoint = server.URL
	t.Setenv("SAGEOX_TOKEN", "test-registration")
	t.Setenv("SAGEOX_ENDPOINT", server.URL)
	ledger := t.TempDir()
	require.NoError(t, Enqueue(ledger, job))
	path, err := jobPath(ledger, job.SessionName)
	require.NoError(t, err)
	for range 2 {
		done, err := Retry(context.Background(), ledger, job)
		require.NoError(t, err)
		require.False(t, done, "a successful notification is not indexing evidence")
		require.FileExists(t, path)
	}
	require.Equal(t, int64(2), posts.Load())
	state.Store("deleted")
	_, err = Retry(context.Background(), ledger, job)
	require.ErrorContains(t, err, "404")
	require.Equal(t, int64(2), posts.Load(), "deleted content must not be re-registered")
	require.FileExists(t, path)
	state.Store("ready")
	done, err := Retry(context.Background(), ledger, job)
	require.NoError(t, err)
	require.True(t, done)
	require.NoFileExists(t, path)
}

func TestRegistrationRejectsOldReadyRevisionAndRetainsConcurrentJob(t *testing.T) {
	job := Job{RepoID: "repo_019c6d2e-27b0-798d-aaed-b036114dc63a", SessionName: "2026-09-01-test", SourceDigest: strings.Repeat("a", 64)}
	meta := lfs.SessionMeta{RepoID: job.RepoID, SessionName: job.SessionName}
	var digest atomic.Value
	digest.Store(strings.Repeat("b", 64))
	var posts atomic.Int64
	headers := make(chan string, 16)
	failures := make(chan error, 16)
	t.Cleanup(func() {
		close(headers)
		for header := range headers {
			require.Equal(t, "no-cache", header)
		}
		close(failures)
		for err := range failures {
			require.NoError(t, err)
		}
	})
	ledger := t.TempDir()
	var replace atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		headers <- r.Header.Get("Cache-Control")
		if replace.Load() {
			newer := job
			newer.SourceDigest = strings.Repeat("c", 64)
			failures <- Enqueue(ledger, newer)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"repoId": job.RepoID, "sessionId": job.SessionName, "id": meta.EffectiveSessionID(), "processing_status": "ready", "source": map[string]string{"snapshot_digest": digest.Load().(string)}})
	}))
	defer server.Close()
	job.Endpoint = server.URL
	t.Setenv("SAGEOX_TOKEN", "test-registration")
	t.Setenv("SAGEOX_ENDPOINT", server.URL)
	require.NoError(t, Enqueue(ledger, job))
	path, err := jobPath(ledger, job.SessionName)
	require.NoError(t, err)
	done, err := Retry(context.Background(), ledger, job)
	require.NoError(t, err)
	require.False(t, done, "the old ready revision does not complete an extension")
	require.Equal(t, int64(1), posts.Load())
	require.FileExists(t, path)
	digest.Store(job.SourceDigest)
	replace.Store(true)
	done, err = Retry(context.Background(), ledger, job)
	require.NoError(t, err)
	require.False(t, done, "a matching but slow lookup cannot delete a newer job")
	require.FileExists(t, path)
}
