package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFetchTeamContextViaAPI_WritesAllFiles — Failure prevented: an HTTP
// team-context response decodes correctly but files don't land on disk
// where the loader expects them, so ephemeral prime sees no context
// even after a successful fetch.
func TestFetchTeamContextViaAPI_WritesAllFiles(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	teamCtxDir := filepath.Join(tmpDir, "team-ctx", "team_abc")

	resp := &api.TeamContextContentResponse{
		TeamID:   "team_abc",
		TeamName: "Test Team",
		Docs: []api.TeamContextDoc{
			{Name: "onboarding.md", Title: "Onboarding", Content: "# Onboarding\n"},
			{Name: "guides/architecture.md", Title: "Arch", Content: "# Arch\n"},
		},
		AgentsMD: "# AGENTS\n",
		ClaudeMD: "# CLAUDE\n",
		Memory:   "# MEMORY\n",
	}

	require.NoError(t, fetchTeamContextViaAPI(teamCtxDir, resp))

	cases := map[string]string{
		filepath.Join(teamCtxDir, "AGENTS.md"):                         "# AGENTS\n",
		filepath.Join(teamCtxDir, "CLAUDE.md"):                         "# CLAUDE\n",
		filepath.Join(teamCtxDir, "MEMORY.md"):                         "# MEMORY\n",
		filepath.Join(teamCtxDir, "docs", "onboarding.md"):             "# Onboarding\n",
		filepath.Join(teamCtxDir, "docs", "guides", "architecture.md"): "# Arch\n",
	}
	for path, want := range cases {
		got, err := os.ReadFile(path)
		require.NoError(t, err, "expected file %s", path)
		assert.Equal(t, want, string(got), "content mismatch for %s", path)
	}
}

// TestFetchTeamContextViaAPI_SkipsEmptyRootFiles — Failure prevented: writing
// an empty AGENTS.md/CLAUDE.md/MEMORY.md masks the actual absence of
// those files, breaking downstream loaders that branch on file existence.
func TestFetchTeamContextViaAPI_SkipsEmptyRootFiles(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	teamCtxDir := filepath.Join(tmpDir, "team-ctx", "team_empty")

	resp := &api.TeamContextContentResponse{
		TeamID:   "team_empty",
		TeamName: "Empty",
		Docs:     []api.TeamContextDoc{{Name: "x.md", Content: "x"}},
		// AgentsMD/ClaudeMD/Memory intentionally blank
	}
	require.NoError(t, fetchTeamContextViaAPI(teamCtxDir, resp))

	for _, name := range []string{"AGENTS.md", "CLAUDE.md", "MEMORY.md"} {
		_, err := os.Stat(filepath.Join(teamCtxDir, name))
		assert.True(t, os.IsNotExist(err), "%s must NOT exist when empty in response", name)
	}
	got, err := os.ReadFile(filepath.Join(teamCtxDir, "docs", "x.md"))
	require.NoError(t, err)
	assert.Equal(t, "x", string(got))
}

// TestFetchTeamContextViaAPI_RefusesPathEscape — Failure prevented: a
// malicious or buggy server returns a doc with name="../../etc/passwd"
// and we happily write outside the team-context dir. The defensive check
// keeps writes scoped.
func TestFetchTeamContextViaAPI_RefusesPathEscape(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	teamCtxDir := filepath.Join(tmpDir, "team-ctx", "team_evil")

	resp := &api.TeamContextContentResponse{
		TeamID: "team_evil",
		Docs: []api.TeamContextDoc{
			{Name: "../escape.md", Content: "bad"},
			{Name: "/abs/path.md", Content: "bad"},
			{Name: "ok.md", Content: "good"},
		},
	}
	require.NoError(t, fetchTeamContextViaAPI(teamCtxDir, resp))

	// the safe doc landed
	got, err := os.ReadFile(filepath.Join(teamCtxDir, "docs", "ok.md"))
	require.NoError(t, err)
	assert.Equal(t, "good", string(got))

	// nothing landed outside teamCtxDir
	escaped := filepath.Join(tmpDir, "team-ctx", "escape.md")
	_, err = os.Stat(escaped)
	assert.True(t, os.IsNotExist(err), "escape.md must not exist outside teamCtxDir")
}

// TestAtomicWriteFile_OverwritesExisting — Failure prevented: atomic
// writes leak temp files or fail to replace an existing file with new
// content.
func TestAtomicWriteFile_OverwritesExisting(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "f.txt")
	require.NoError(t, os.WriteFile(path, []byte("old"), 0o644))

	require.NoError(t, atomicWriteFile(path, []byte("new"), 0o644))

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "new", string(got))

	// no leftover temp files
	entries, err := os.ReadDir(tmpDir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), ".tmp-", "temp file leaked: %s", e.Name())
	}
}

// TestDiscoverTeamContextWithFallback_RefreshesExistingLocalCopy — Failure
// prevented: after the first ephemeral HTTP fetch creates a local team-context
// directory, later primes stop refreshing it and keep serving stale rules/docs.
func TestDiscoverTeamContextWithFallback_RefreshesExistingLocalCopy(t *testing.T) {
	t.Setenv("OX_EPHEMERAL", "1")
	t.Setenv("OX_PERSIST_DISK", "0") // pin: non-persistent environment forces HTTP refresh
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	runtime.Reset()
	t.Cleanup(runtime.Reset)

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		assert.Equal(t, "/api/v1/teams/team_abc/context", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"team_id":"team_abc",
			"team_name":"Team ABC",
			"docs":[{"name":"guide.md","title":"Guide","content":"fresh"}],
			"agents_md":"fresh agents",
			"claude_md":"",
			"memory":""
		}`)
	}))
	defer server.Close()

	projectRoot := createInitializedProjectWithConfig(t, &config.ProjectConfig{
		ProjectID:   "test_project",
		WorkspaceID: "test_workspace",
		TeamID:      "team_abc",
		TeamName:    "Team ABC",
		Endpoint:    server.URL,
	})

	teamCtxDir := paths.TeamContextDir("team_abc", server.URL)
	require.NoError(t, os.MkdirAll(teamCtxDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(teamCtxDir, "AGENTS.md"), []byte("stale agents"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(teamCtxDir, "docs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(teamCtxDir, "docs", "guide.md"), []byte("stale"), 0o644))

	info := discoverTeamContextWithFallback(projectRoot, "", true)
	require.NotNil(t, info)
	assert.Equal(t, int32(1), requests.Load(), "ephemeral discovery should refresh over HTTP even when a local copy already exists")

	agents, err := os.ReadFile(filepath.Join(teamCtxDir, "AGENTS.md"))
	require.NoError(t, err)
	assert.Equal(t, "fresh agents", string(agents))

	doc, err := os.ReadFile(filepath.Join(teamCtxDir, "docs", "guide.md"))
	require.NoError(t, err)
	assert.Equal(t, "fresh", string(doc))
}

// TestFetchTeamContextViaAPI_RemovesStaleDocsAndRootFiles — Failure prevented:
// when the upstream team-context omits a doc or root file that was present
// on a prior fetch, the old copy lingers on disk and the agent keeps seeing
// deleted instructions. After this fix, files no longer in the response are
// removed.
func TestFetchTeamContextViaAPI_RemovesStaleDocsAndRootFiles(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	teamCtxDir := filepath.Join(tmpDir, "team-ctx", "team_stale")

	// First fetch: seed AGENTS.md + CLAUDE.md + MEMORY.md + two docs.
	first := &api.TeamContextContentResponse{
		TeamID: "team_stale",
		Docs: []api.TeamContextDoc{
			{Name: "keep.md", Content: "keep-v1"},
			{Name: "drop.md", Content: "drop-me"},
			{Name: "subdir/old.md", Content: "old-nested"},
		},
		AgentsMD: "agents-v1",
		ClaudeMD: "claude-v1",
		Memory:   "memory-v1",
	}
	require.NoError(t, fetchTeamContextViaAPI(teamCtxDir, first))

	// Sanity-check the seed landed.
	for _, p := range []string{
		filepath.Join(teamCtxDir, "AGENTS.md"),
		filepath.Join(teamCtxDir, "CLAUDE.md"),
		filepath.Join(teamCtxDir, "MEMORY.md"),
		filepath.Join(teamCtxDir, "docs", "keep.md"),
		filepath.Join(teamCtxDir, "docs", "drop.md"),
		filepath.Join(teamCtxDir, "docs", "subdir", "old.md"),
	} {
		_, err := os.Stat(p)
		require.NoError(t, err, "seed file missing: %s", p)
	}

	// Second fetch: upstream removed drop.md, subdir/old.md, CLAUDE.md, MEMORY.md.
	second := &api.TeamContextContentResponse{
		TeamID: "team_stale",
		Docs: []api.TeamContextDoc{
			{Name: "keep.md", Content: "keep-v2"},
		},
		AgentsMD: "agents-v2",
		// ClaudeMD + Memory intentionally blank — upstream removed them
	}
	require.NoError(t, fetchTeamContextViaAPI(teamCtxDir, second))

	// Files that should still exist with refreshed content
	for path, want := range map[string]string{
		filepath.Join(teamCtxDir, "AGENTS.md"):       "agents-v2",
		filepath.Join(teamCtxDir, "docs", "keep.md"): "keep-v2",
	} {
		got, err := os.ReadFile(path)
		require.NoError(t, err, "%s should be kept", path)
		assert.Equal(t, want, string(got), "%s content mismatch", path)
	}

	// Files that should be GONE
	for _, p := range []string{
		filepath.Join(teamCtxDir, "CLAUDE.md"),
		filepath.Join(teamCtxDir, "MEMORY.md"),
		filepath.Join(teamCtxDir, "docs", "drop.md"),
		filepath.Join(teamCtxDir, "docs", "subdir", "old.md"),
	} {
		_, err := os.Stat(p)
		assert.True(t, os.IsNotExist(err), "stale file must be removed: %s (err=%v)", p, err)
	}
}

// TestDiscoverTeamContextWithFallback_CIDoesNotForceHTTPRefresh ensures the
// HTTP fallback is keyed to non-persistent storage, not the broader derived
// IsEphemeral() predicate. Failure prevented: CI=true forces a redundant HTTP
// rewrite of an already-durable local team context on every prime.
func TestDiscoverTeamContextWithFallback_CIDoesNotForceHTTPRefresh(t *testing.T) {
	t.Setenv("CI", "true")
	t.Setenv("OX_PERSIST_DISK", "1") // pin: CI has durable local state, must not force HTTP refresh
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	runtime.Reset()
	t.Cleanup(runtime.Reset)

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))
	defer server.Close()

	projectRoot := createInitializedProjectWithConfig(t, &config.ProjectConfig{
		ProjectID:   "test_project",
		WorkspaceID: "test_workspace",
		TeamID:      "team_abc",
		TeamName:    "Team ABC",
		Endpoint:    server.URL,
	})

	teamCtxDir := paths.TeamContextDir("team_abc", server.URL)
	require.NoError(t, os.MkdirAll(filepath.Join(teamCtxDir, "docs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(teamCtxDir, "AGENTS.md"), []byte("local agents"), 0o644))

	info := discoverTeamContextWithFallback(projectRoot, "", true)
	require.NotNil(t, info)
	assert.Equal(t, int32(0), requests.Load(), "durable CI environments should use the local team-context path")
}
