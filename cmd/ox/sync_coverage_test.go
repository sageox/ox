package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckGitHasUncommittedChanges_CleanRepo(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	setupGitRepoWithCommit(t, tmp)

	hasChanges, err := checkGitHasUncommittedChanges(tmp)
	require.NoError(t, err)
	assert.False(t, hasChanges, "clean repo should have no uncommitted changes")
}

func TestCheckGitHasUncommittedChanges_WithChanges(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	setupGitRepoWithCommit(t, tmp)

	// create uncommitted file
	os.WriteFile(filepath.Join(tmp, "dirty.txt"), []byte("dirty"), 0o644)

	hasChanges, err := checkGitHasUncommittedChanges(tmp)
	require.NoError(t, err)
	assert.True(t, hasChanges, "should detect uncommitted file")
}

func TestCheckGitHasUncommittedChanges_NotAGitRepo(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()

	_, err := checkGitHasUncommittedChanges(tmp)
	assert.Error(t, err, "should fail for non-git directory")
}

func TestAutoStartDaemon_DisabledByEnv(t *testing.T) {
	t.Setenv("OX_NO_DAEMON", "1")

	err := autoStartDaemon()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OX_NO_DAEMON=1")
}

func TestSyncResultFields_Coverage(t *testing.T) {
	t.Parallel()

	// test zero-value result
	var result SyncResult
	assert.False(t, result.Success)
	assert.Empty(t, result.Mode)
	assert.Nil(t, result.Ledger)
	assert.Nil(t, result.TeamContexts)
	assert.Empty(t, result.Error)
}

func TestSyncLedgerResult_Fields(t *testing.T) {
	t.Parallel()

	lr := SyncLedgerResult{
		Path:   "/data/ledger",
		Status: "synced",
	}
	assert.Equal(t, "/data/ledger", lr.Path)
	assert.Equal(t, "synced", lr.Status)
	assert.Empty(t, lr.Error)

	// with error
	lr2 := SyncLedgerResult{
		Path:   "/data/ledger",
		Status: "error",
		Error:  "connection refused",
	}
	assert.Equal(t, "error", lr2.Status)
	assert.Equal(t, "connection refused", lr2.Error)
}

func TestTeamContextSyncResult_Fields(t *testing.T) {
	t.Parallel()

	tc := TeamContextSyncResult{
		TeamID:   "team-abc",
		TeamName: "Alpha Team",
		Path:     "/data/teams/team-abc",
		Status:   "synced",
	}
	assert.Equal(t, "team-abc", tc.TeamID)
	assert.Equal(t, "Alpha Team", tc.TeamName)
	assert.Equal(t, "synced", tc.Status)

	// with error
	tc2 := TeamContextSyncResult{
		TeamID: "team-xyz",
		Status: "error",
		Error:  "timeout",
	}
	assert.Equal(t, "error", tc2.Status)
	assert.Equal(t, "timeout", tc2.Error)
}

func TestSyncResult_WithMultipleTeamContexts(t *testing.T) {
	t.Parallel()

	result := SyncResult{
		Success: true,
		Mode:    "daemon",
		Ledger: &SyncLedgerResult{
			Path:   "/data/ledger",
			Status: "synced",
		},
		TeamContexts: []TeamContextSyncResult{
			{TeamID: "team-1", Status: "synced"},
			{TeamID: "team-2", Status: "error", Error: "not found"},
			{TeamID: "team-3", Status: "skipped"},
		},
	}

	assert.Len(t, result.TeamContexts, 3)
	assert.Equal(t, "synced", result.TeamContexts[0].Status)
	assert.Equal(t, "error", result.TeamContexts[1].Status)
	assert.Equal(t, "skipped", result.TeamContexts[2].Status)
}

func TestSyncPathExists_ExistingFile(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	f := filepath.Join(tmp, "exists.txt")
	os.WriteFile(f, []byte("data"), 0o644)

	assert.True(t, syncPathExists(f))
}

func TestCheckGitHasUncommittedChanges_StagedFile(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	setupGitRepoWithCommit(t, tmp)

	// create and stage a file without committing
	filePath := filepath.Join(tmp, "staged.txt")
	os.WriteFile(filePath, []byte("staged content"), 0o644)
	cmd := exec.Command("git", "-C", tmp, "add", "staged.txt")
	require.NoError(t, cmd.Run())

	hasChanges, err := checkGitHasUncommittedChanges(tmp)
	require.NoError(t, err)
	assert.True(t, hasChanges, "staged but uncommitted file should be detected")
}

// setupGitRepoWithCommit creates a real git repo with an initial commit
func setupGitRepoWithCommit(t *testing.T, dir string) {
	t.Helper()

	cmds := [][]string{
		{"git", "-C", dir, "init"},
		{"git", "-C", dir, "config", "user.email", "test@example.com"},
		{"git", "-C", dir, "config", "user.name", "Test"},
		{"git", "-C", dir, "commit", "--allow-empty", "-m", "init"},
	}

	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "command %v failed: %s", args, out)
	}
}
