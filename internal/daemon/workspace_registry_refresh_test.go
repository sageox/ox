package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initTestGitRepo creates a minimal directory that gitutil.IsGitRepo recognizes
// as a valid git repo (.git/HEAD must exist).
func initTestGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	require.NoError(t, os.MkdirAll(gitDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644))
	return dir
}

// --------------------------------------------------------------------------
// RefreshExists
// --------------------------------------------------------------------------

func TestRefreshExists_PathExists_MarksTrue(t *testing.T) {
	// real-world failure prevented: a workspace cloned successfully but with
	// Exists=false would be re-cloned unnecessarily, wasting bandwidth and
	// potentially losing local changes
	t.Parallel()

	dir := initTestGitRepo(t)

	reg := &WorkspaceRegistry{
		workspaces: make(map[string]*WorkspaceState),
	}
	reg.workspaces["ws1"] = &WorkspaceState{
		ID:     "ws1",
		Path:   dir,
		Exists: false, // deliberately wrong — RefreshExists should fix
	}

	reg.RefreshExists()

	assert.True(t, reg.workspaces["ws1"].Exists,
		"workspace pointing at existing git repo must have Exists=true after RefreshExists")
}

func TestRefreshExists_PathDeleted_MarksFalse(t *testing.T) {
	// real-world failure prevented: if a workspace directory is deleted but
	// Exists stays true, the daemon tries to pull into a missing directory
	// instead of triggering a re-clone via anti-entropy
	t.Parallel()

	dir := initTestGitRepo(t)

	reg := &WorkspaceRegistry{
		workspaces: make(map[string]*WorkspaceState),
	}
	reg.workspaces["ws1"] = &WorkspaceState{
		ID:     "ws1",
		Path:   dir,
		Exists: true,
	}

	require.NoError(t, os.RemoveAll(dir))

	reg.RefreshExists()

	assert.False(t, reg.workspaces["ws1"].Exists,
		"workspace pointing at deleted directory must have Exists=false after RefreshExists")
}

func TestRefreshExists_MixedWorkspaces(t *testing.T) {
	// real-world failure prevented: RefreshExists must update ALL workspaces
	// independently; a bug that short-circuits after the first would leave
	// stale Exists values on the rest
	t.Parallel()

	existingDir := initTestGitRepo(t)

	deletedDir := initTestGitRepo(t)
	require.NoError(t, os.RemoveAll(deletedDir))

	reg := &WorkspaceRegistry{
		workspaces: make(map[string]*WorkspaceState),
	}
	reg.workspaces["exists"] = &WorkspaceState{
		ID: "exists", Path: existingDir, Exists: false,
	}
	reg.workspaces["deleted"] = &WorkspaceState{
		ID: "deleted", Path: deletedDir, Exists: true,
	}
	reg.workspaces["never-existed"] = &WorkspaceState{
		ID: "never-existed", Path: "/nonexistent/path/xyz", Exists: true,
	}

	reg.RefreshExists()

	assert.True(t, reg.workspaces["exists"].Exists,
		"workspace with valid git repo on disk must be Exists=true")
	assert.False(t, reg.workspaces["deleted"].Exists,
		"workspace whose directory was deleted must be Exists=false")
	assert.False(t, reg.workspaces["never-existed"].Exists,
		"workspace with path that never existed must be Exists=false")
}
