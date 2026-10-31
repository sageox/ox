package daemon

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/codedb"
	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/require"
)

func TestGitHubRepoRefreshSkipsNonGitHubAndMissingRepo(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, config.SaveProjectConfig(root, &config.ProjectConfig{RepoID: "repo_1"}))
	m := NewCodeDBManager(root, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	m.refreshGitHubRepoIfDue(context.Background(), t.TempDir())
	m.mu.Lock()
	require.False(t, m.indexing)
	require.True(t, m.lastGitHubRepoRead.IsZero())
	m.mu.Unlock()

	m = NewCodeDBManager(t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	m.githubRepo = "acme/api"
	m.refreshGitHubRepoIfDue(context.Background(), t.TempDir())
	m.mu.Lock()
	require.False(t, m.indexing)
	require.True(t, m.lastGitHubRepoRead.IsZero())
	m.mu.Unlock()
}

func TestGitHubRepoRefreshThrottlesFailures(t *testing.T) {
	t.Setenv(config.EnvGitHubSync, "enabled")
	root := t.TempDir()
	require.NoError(t, config.SaveProjectConfig(root, &config.ProjectConfig{RepoID: "repo_1"}))
	m := NewCodeDBManager(root, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	m.githubRepo = "acme/api"
	m.refreshGitHubRepoIfDue(context.Background(), filepath.Join(t.TempDir(), "missing", "index"))
	require.Eventually(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return !m.indexing && !m.lastGitHubRepoRead.IsZero() }, 5*time.Second, 10*time.Millisecond)
	m.mu.Lock()
	first := m.lastGitHubRepoRead
	m.mu.Unlock()
	m.refreshGitHubRepoIfDue(context.Background(), t.TempDir())
	m.mu.Lock()
	require.False(t, m.indexing)
	require.Equal(t, first, m.lastGitHubRepoRead)
	m.mu.Unlock()
}

func TestGitHubRepoIndexWithSecondaryTeamCheckoutUsesRepoAccess(t *testing.T) {
	// Disabling this user's producer must not hide snapshots shared by coworkers.
	t.Setenv(config.EnvGitHubSync, "disabled")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("SAGEOX_TOKEN", "")
	id := strings.Repeat("a", 64)
	catalogReads, blobReads := 0, 0
	busy := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("view") == "catalog" {
			catalogReads++
			if busy {
				http.Error(w, "busy", http.StatusServiceUnavailable)
				return
			}
			fmt.Fprintf(w, `{"entries":[{"id":"%s","kind":"pr","number":7,"status":"ready"}],"complete":true}`, id)
			return
		}
		blobReads++
		fmt.Fprint(w, `{"number":7,"title":"repo access marker","state":"merged","author":"dev","created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-02T00:00:00Z","merge_commit":"merge","commits":[{"sha":"abc","author":"dev","date":"2026-10-01T00:00:00Z","message":"change"}]}`)
	}))
	defer server.Close()
	t.Setenv("SAGEOX_ENDPOINT", server.URL)
	root := t.TempDir()
	require.NoError(t, config.SaveProjectConfig(root, &config.ProjectConfig{RepoID: "repo_1", TeamID: "team_secondary", Endpoint: server.URL}))
	require.NoError(t, config.SaveLocalConfig(root, &config.LocalConfig{TeamContexts: []config.TeamContext{{TeamID: "team_secondary", Path: t.TempDir()}}}))
	require.NoError(t, auth.SaveTokenForEndpoint(server.URL, &auth.StoredToken{AccessToken: "test-token", ExpiresAt: time.Now().Add(time.Hour)}))
	db, err := codedb.Open(t.TempDir())
	require.NoError(t, err)
	defer db.Close()
	m := NewCodeDBManager(root, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	m.githubRepo = "acme/api"
	m.indexGitHubBoard(context.Background(), db, nil)
	var title, source string
	require.NoError(t, db.Store().QueryRow("SELECT title,source_path FROM pull_requests WHERE number=7").Scan(&title, &source))
	require.Equal(t, "repo access marker", title)
	require.Equal(t, "github-repo:repo_1:"+id, source)
	var sha string
	require.NoError(t, db.Store().QueryRow("SELECT sha FROM pr_commits").Scan(&sha))
	require.Equal(t, "abc", sha)
	// A locally indexed post with the selected digest also needs no blob fetch.
	boardPath := filepath.Join(t.TempDir(), "bulletin", "github", "posts", "acme-api-pr-7-"+id+".md")
	_, err = db.Store().Exec("UPDATE pull_requests SET source_path = ? WHERE number = 7", boardPath)
	require.NoError(t, err)
	m.indexGitHubBoard(context.Background(), db, nil)
	require.Equal(t, 2, catalogReads)
	require.Equal(t, 1, blobReads)
	busy = true
	m.indexGitHubBoard(context.Background(), db, nil)
	require.Equal(t, 3, catalogReads)
	m.indexGitHubBoard(context.Background(), db, nil)
	require.Equal(t, 3, catalogReads, "a transient failure must not retry before the cadence")
}

func TestGitHubRepoFallbackWhenConfiguredTeamCheckoutMissing(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, config.SaveProjectConfig(root, &config.ProjectConfig{RepoID: "repo_1", TeamID: "team_owner"}))
	require.NoError(t, config.SaveLocalConfig(root, &config.LocalConfig{TeamContexts: []config.TeamContext{{TeamID: "team_owner", Path: filepath.Join(root, "not-cloned")}}}))
	m := NewCodeDBManager(root, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	require.Empty(t, m.boardPostsDir())
}
