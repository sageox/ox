package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/api"
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
		filepath.Join(teamCtxDir, "AGENTS.md"):                       "# AGENTS\n",
		filepath.Join(teamCtxDir, "CLAUDE.md"):                       "# CLAUDE\n",
		filepath.Join(teamCtxDir, "MEMORY.md"):                       "# MEMORY\n",
		filepath.Join(teamCtxDir, "docs", "onboarding.md"):           "# Onboarding\n",
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
