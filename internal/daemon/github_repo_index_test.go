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
	"sync/atomic"
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

func TestGitHubRepoIndexRejectsIncompleteSnapshotsAndBacksOff(t *testing.T) {
	id := strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, kind, status, body, want string
		httpStatus                     int
	}{
		{"unavailable catalog", "pr", "ready", "", "HTTP 503", http.StatusServiceUnavailable},
		{"pending entry", "pr", "pending", "", "unavailable entries", 0},
		{"invalid PR shape", "pr", "ready", `[]`, "cannot unmarshal", 0},
		{"PR number mismatch", "pr", "ready", `{"number":8}`, "PR number mismatch", 0},
		{"invalid issue shape", "issue", "ready", `[]`, "cannot unmarshal", 0},
		{"issue number mismatch", "issue", "ready", `{"number":8}`, "issue number mismatch", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reads atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reads.Add(1)
				if tc.httpStatus != 0 {
					w.WriteHeader(tc.httpStatus)
					return
				}
				if r.URL.Query().Get("view") == "catalog" {
					fmt.Fprintf(w, `{"complete":true,"entries":[{"id":%q,"kind":%q,"number":7,"status":%q}]}`, id, tc.kind, tc.status)
					return
				}
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			t.Setenv("SAGEOX_ENDPOINT", server.URL)
			t.Setenv("SAGEOX_TOKEN", "test-token")
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			root := t.TempDir()
			require.NoError(t, config.SaveProjectConfig(root, &config.ProjectConfig{RepoID: "repo_1", TeamID: "team_secondary", Endpoint: server.URL}))
			db, err := codedb.Open(t.TempDir())
			require.NoError(t, err)
			defer db.Close()
			m := NewCodeDBManager(root, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
			err = m.indexGitHubRepo(context.Background(), db)
			require.ErrorContains(t, err, tc.want)
			m.mu.Lock()
			lastRead, retryAfter := m.lastGitHubRepoRead, m.githubRepoRetryAfter
			m.mu.Unlock()
			require.True(t, lastRead.IsZero(), "failed read must not mark the index fresh")
			require.True(t, retryAfter.After(time.Now()), "failed read schedules retry backoff")
			var prs, issues int
			require.NoError(t, db.Store().QueryRow("SELECT COUNT(*) FROM pull_requests").Scan(&prs))
			require.NoError(t, db.Store().QueryRow("SELECT COUNT(*) FROM issues").Scan(&issues))
			require.Zero(t, prs)
			require.Zero(t, issues)
			before := reads.Load()
			require.ErrorContains(t, m.indexGitHubRepo(context.Background(), db), "retry backoff")
			require.Equal(t, before, reads.Load(), "backoff must avoid another network read")
		})
	}
}

func TestGitHubRepoIndexMakesIssueAndCommentsSearchable(t *testing.T) {
	id := strings.Repeat("b", 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("view") == "catalog" {
			fmt.Fprintf(w, `{"complete":true,"entries":[{"id":%q,"kind":"issue","number":42,"status":"ready"}]}`, id)
			return
		}
		fmt.Fprint(w, `{"number":42,"title":"fixture issue","body":"searchable issue body","author":"author","state":"open","created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-02T00:00:00Z","comments":[{"author":"reviewer","body":"helpful response","created_at":"2026-10-02T00:00:00Z"}]}`)
	}))
	defer server.Close()
	t.Setenv("SAGEOX_ENDPOINT", server.URL)
	t.Setenv("SAGEOX_TOKEN", "test-token")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	require.NoError(t, config.SaveProjectConfig(root, &config.ProjectConfig{RepoID: "repo_1", TeamID: "team_secondary", Endpoint: server.URL}))
	db, err := codedb.Open(t.TempDir())
	require.NoError(t, err)
	defer db.Close()
	m := NewCodeDBManager(root, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	require.NoError(t, m.indexGitHubRepo(context.Background(), db))
	var title, body, source, comment string
	require.NoError(t, db.Store().QueryRow("SELECT i.title,i.body,i.source_path,c.body FROM issues i JOIN issue_comments c ON c.issue_id=i.id WHERE i.number=42").Scan(&title, &body, &source, &comment))
	require.Equal(t, "fixture issue", title)
	require.Equal(t, "searchable issue body", body)
	require.Equal(t, "helpful response", comment)
	require.Equal(t, "github-repo:repo_1:"+id, source)
	m.mu.Lock()
	lastRead, retryAfter := m.lastGitHubRepoRead, m.githubRepoRetryAfter
	m.mu.Unlock()
	require.False(t, lastRead.IsZero())
	require.True(t, retryAfter.IsZero())
}

func TestGitHubRepoIndexContinuesAfterBadSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name, kind, wantError, table, badBody string
		failInsert                            bool
	}{
		{"pr mismatch", "pr", "PR number mismatch", "pull_requests", `{"number":999,"title":"bad","state":"open"}`, false},
		{"issue mismatch", "issue", "issue number mismatch", "issues", `{"number":999,"title":"bad","state":"open"}`, false},
		{"pr malformed", "pr", "cannot unmarshal", "pull_requests", `[]`, false},
		{"issue malformed", "issue", "cannot unmarshal", "issues", `[]`, false},
		{"pr index failure", "pr", "fixture insert rejected", "pull_requests", `{"number":7,"title":"bad","state":"open"}`, true},
		{"issue index failure", "issue", "fixture insert rejected", "issues", `{"number":7,"title":"bad","state":"open"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			badID, goodID := strings.Repeat("a", 64), strings.Repeat("b", 64)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("view") == "catalog" {
					fmt.Fprintf(w, `{"complete":true,"entries":[{"id":%q,"kind":%q,"number":7,"status":"ready"},{"id":%q,"kind":%q,"number":8,"status":"ready"}]}`, badID, tc.kind, goodID, tc.kind)
					return
				}
				if strings.HasSuffix(r.URL.Path, badID) {
					fmt.Fprint(w, tc.badBody)
					return
				}
				fmt.Fprint(w, `{"number":8,"title":"healthy snapshot","body":"retained context","state":"open","created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-02T00:00:00Z"}`)
			}))
			defer server.Close()
			t.Setenv("SAGEOX_ENDPOINT", server.URL)
			t.Setenv("SAGEOX_TOKEN", "test-token")
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			root := t.TempDir()
			require.NoError(t, config.SaveProjectConfig(root, &config.ProjectConfig{RepoID: "repo_1", TeamID: "team_secondary", Endpoint: server.URL}))
			db, err := codedb.Open(t.TempDir())
			require.NoError(t, err)
			defer db.Close()
			if tc.failInsert {
				_, err = db.Store().Exec("CREATE TRIGGER reject_fixture BEFORE INSERT ON " + tc.table + " WHEN NEW.number = 7 BEGIN SELECT RAISE(FAIL, 'fixture insert rejected'); END")
				require.NoError(t, err)
			}
			m := NewCodeDBManager(root, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
			err = m.indexGitHubRepo(context.Background(), db)
			require.ErrorContains(t, err, tc.wantError)
			var title, source string
			require.NoError(t, db.Store().QueryRow("SELECT title,source_path FROM "+tc.table+" WHERE number=8").Scan(&title, &source), "a bad item must not starve later snapshots")
			require.Equal(t, "healthy snapshot", title)
			require.Equal(t, "github-repo:repo_1:"+goodID, source)
			m.mu.Lock()
			lastRead, retryAfter := m.lastGitHubRepoRead, m.githubRepoRetryAfter
			m.mu.Unlock()
			require.True(t, lastRead.IsZero(), "partial failure must not report a fresh index")
			require.True(t, retryAfter.After(time.Now()), "partial failure retains the normal retry backoff")
			require.ErrorContains(t, m.indexGitHubRepo(context.Background(), db), "retry backoff")
		})
	}
}
