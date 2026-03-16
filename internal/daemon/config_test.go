package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	assert.Equal(t, 5*time.Minute, cfg.SyncIntervalRead)
	assert.Equal(t, 500*time.Millisecond, cfg.DebounceWindow)
	assert.True(t, cfg.AutoStart)
	assert.Empty(t, cfg.LedgerPath)
}

func TestWorkspaceID(t *testing.T) {
	// same path should give same ID
	id1 := WorkspaceID("/some/path")
	id2 := WorkspaceID("/some/path")
	assert.Equal(t, id1, id2)

	// different paths should give different IDs
	id3 := WorkspaceID("/other/path")
	assert.NotEqual(t, id1, id3)

	// ID should be 8 chars
	assert.Len(t, id1, 8)
}

func TestSocketPath(t *testing.T) {
	path := SocketPath()
	assert.NotEmpty(t, path)
	assert.Contains(t, path, "daemon-")
	assert.Contains(t, path, ".sock")
}

func TestSocketPath_DefaultMode(t *testing.T) {
	// XDG is now the default, so default mode uses XDG_RUNTIME_DIR
	tmpDir := t.TempDir()
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	workspaceID := CurrentWorkspaceID()
	path := SocketPath()
	expected := filepath.Join(tmpDir, "sageox", "daemon", "daemon-"+workspaceID+".sock")
	assert.Equal(t, expected, path)
}

func TestSocketPath_LegacyMode(t *testing.T) {
	t.Setenv("OX_XDG_DISABLE", "1")

	workspaceID := CurrentWorkspaceID()
	path := SocketPath()

	home, err := os.UserHomeDir()
	assert.NoError(t, err)
	expected := filepath.Join(home, ".sageox", "state", "daemon", "daemon-"+workspaceID+".sock")
	assert.Equal(t, expected, path)
}

func TestSocketPath_DefaultModeWithoutRuntime(t *testing.T) {
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("XDG_RUNTIME_DIR", "")

	workspaceID := CurrentWorkspaceID()
	path := SocketPath()
	// os.TempDir() returns platform-specific temp dir (e.g., /var/folders on macOS)
	expected := filepath.Join(os.TempDir(), "sageox", "daemon", "daemon-"+workspaceID+".sock")
	assert.Equal(t, expected, path)
}

func TestLogPath(t *testing.T) {
	path := LogPath()
	assert.NotEmpty(t, path)
	assert.Contains(t, path, "daemon_")
	assert.Contains(t, path, ".log")
}

func TestLogPath_DefaultMode(t *testing.T) {
	// Logs go to /tmp/<username>/sageox/logs (OS cleanup), regardless of XDG settings
	path := LogPath()
	assert.Contains(t, path, filepath.Join("sageox", "logs"))
	assert.Contains(t, path, "tmp")
}

func TestLogPath_LegacyMode(t *testing.T) {
	// Logs use /tmp/<username>/sageox even in legacy mode (OS cleanup)
	t.Setenv("OX_XDG_DISABLE", "1")

	path := LogPath()
	assert.Contains(t, path, filepath.Join("sageox", "logs"))
	assert.Contains(t, path, "tmp")
}

func TestPidPath(t *testing.T) {
	path := PidPath()
	assert.NotEmpty(t, path)
	assert.Contains(t, path, "daemon-")
	assert.Contains(t, path, ".pid")
}

func TestRegistryPath_DefaultMode(t *testing.T) {
	// XDG is now the default, so default mode uses XDG_RUNTIME_DIR
	tmpDir := t.TempDir()
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	path := RegistryPath()
	expected := filepath.Join(tmpDir, "sageox", "daemon", "registry.json")
	assert.Equal(t, expected, path)
}

func TestRegistryPath_LegacyMode(t *testing.T) {
	// Legacy mode uses ~/.sageox/ paths
	t.Setenv("OX_XDG_DISABLE", "1")

	path := RegistryPath()
	home, err := os.UserHomeDir()
	assert.NoError(t, err)
	expected := filepath.Join(home, ".sageox", "state", "daemon", "registry.json")
	assert.Equal(t, expected, path)
}

func TestSocketPathForWorkspace_DefaultMode(t *testing.T) {
	// XDG is now the default, so default mode uses XDG_RUNTIME_DIR
	tmpDir := t.TempDir()
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	path := SocketPathForWorkspace("abc12345")
	expected := filepath.Join(tmpDir, "sageox", "daemon", "daemon-abc12345.sock")
	assert.Equal(t, expected, path)
}

func TestSocketPathForWorkspace_LegacyMode(t *testing.T) {
	// Legacy mode uses ~/.sageox/ paths
	t.Setenv("OX_XDG_DISABLE", "1")

	path := SocketPathForWorkspace("abc12345")
	home, err := os.UserHomeDir()
	assert.NoError(t, err)
	expected := filepath.Join(home, ".sageox", "state", "daemon", "daemon-abc12345.sock")
	assert.Equal(t, expected, path)
}

func TestPidPathForWorkspace_DefaultMode(t *testing.T) {
	// XDG is now the default, so default mode uses XDG_RUNTIME_DIR
	tmpDir := t.TempDir()
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	path := PidPathForWorkspace("abc12345")
	expected := filepath.Join(tmpDir, "sageox", "daemon", "daemon-abc12345.pid")
	assert.Equal(t, expected, path)
}

func TestPidPathForWorkspace_LegacyMode(t *testing.T) {
	// Legacy mode uses ~/.sageox/ paths
	t.Setenv("OX_XDG_DISABLE", "1")

	path := PidPathForWorkspace("abc12345")
	home, err := os.UserHomeDir()
	assert.NoError(t, err)
	expected := filepath.Join(home, ".sageox", "state", "daemon", "daemon-abc12345.pid")
	assert.Equal(t, expected, path)
}

func TestLogPathForWorkspace_DefaultMode(t *testing.T) {
	// Logs go to /tmp/<username>/sageox/logs with composite ID
	repoID := "repo_test123"
	workspaceID := "abc12345"
	path := LogPathForWorkspace(repoID, workspaceID)
	// Path includes username and composite ID
	assert.Contains(t, path, "sageox")
	assert.Contains(t, path, "logs")
	assert.Contains(t, path, "daemon_repo_test123_abc12345.log")
	assert.Contains(t, path, "tmp")
}

func TestLogPathForWorkspace_LegacyMode(t *testing.T) {
	// Daemon logs use /tmp/<username>/sageox even in legacy mode
	t.Setenv("OX_XDG_DISABLE", "1")

	repoID := "repo_test123"
	workspaceID := "abc12345"
	path := LogPathForWorkspace(repoID, workspaceID)
	// Path includes username and composite ID
	assert.Contains(t, path, "sageox")
	assert.Contains(t, path, "logs")
	assert.Contains(t, path, "daemon_repo_test123_abc12345.log")
	assert.Contains(t, path, "tmp")
}

// --- Per-repo identity tests ---

// createInitializedDir creates a temp directory with .sageox/config.json containing the given repo_id.
func createInitializedDir(t *testing.T, repoID string) string {
	t.Helper()
	dir := t.TempDir()
	sageoxDir := filepath.Join(dir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	cfg := fmt.Sprintf(`{"repo_id": %q}`, repoID)
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte(cfg), 0644))
	return dir
}

func TestRepoBasedWorkspaceID(t *testing.T) {
	repoID := "repo_test123"
	dir := createInitializedDir(t, repoID)

	id := RepoBasedWorkspaceID(dir)

	// should be 8-char hex hash of the repo_id
	assert.Len(t, id, 8)
	expectedHash := sha256.Sum256([]byte(repoID))
	expectedID := hex.EncodeToString(expectedHash[:])[:8]
	assert.Equal(t, expectedID, id)
}

func TestRepoBasedWorkspaceID_SameRepoIDDifferentDirs(t *testing.T) {
	// independent clones with the same repo_id produce the same workspace ID
	repoID := "repo_shared_abc"
	dir1 := createInitializedDir(t, repoID)
	dir2 := createInitializedDir(t, repoID)

	id1 := RepoBasedWorkspaceID(dir1)
	id2 := RepoBasedWorkspaceID(dir2)

	assert.Equal(t, id1, id2, "same repo_id in different directories must produce the same workspace ID")

	// sanity: the directories themselves are different
	assert.NotEqual(t, dir1, dir2)
}

func TestRepoBasedWorkspaceID_NoConfig(t *testing.T) {
	// directory without .sageox/config.json falls back to path-based hash
	dir := t.TempDir()

	id := RepoBasedWorkspaceID(dir)
	pathID := WorkspaceID(dir)

	assert.Equal(t, pathID, id, "should fall back to path-based ID when no config exists")
	assert.Len(t, id, 8)
}

func TestRepoBasedWorkspaceID_EmptyRepoID(t *testing.T) {
	// config exists but repo_id is empty string
	dir := createInitializedDir(t, "")

	id := RepoBasedWorkspaceID(dir)
	pathID := WorkspaceID(dir)

	assert.Equal(t, pathID, id, "should fall back to path-based ID when repo_id is empty")
}

func TestRepoBasedWorkspaceID_Deterministic(t *testing.T) {
	tests := []struct {
		name   string
		repoID string
	}{
		{"uuid-style", "repo_01jfk3mab7e9qp2xz4nh8vwcdr"},
		{"short", "repo_abc"},
		{"long", "repo_this-is-a-very-long-repo-identifier-for-testing"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := createInitializedDir(t, tt.repoID)

			// calling twice produces the same result
			id1 := RepoBasedWorkspaceID(dir)
			id2 := RepoBasedWorkspaceID(dir)
			assert.Equal(t, id1, id2, "must be deterministic")
			assert.Len(t, id1, 8)
		})
	}

	// different repo_ids produce different IDs
	dirA := createInitializedDir(t, "repo_alpha")
	dirB := createInitializedDir(t, "repo_beta")
	idA := RepoBasedWorkspaceID(dirA)
	idB := RepoBasedWorkspaceID(dirB)
	assert.NotEqual(t, idA, idB, "different repo_ids must produce different workspace IDs")
}

func TestRepoBasedWorkspaceID_DiffersFromPathBased(t *testing.T) {
	// when repo_id is present, the ID should differ from the path-based one
	// (extremely unlikely to collide by chance)
	repoID := "repo_unique_id_xyz"
	dir := createInitializedDir(t, repoID)

	repoBasedID := RepoBasedWorkspaceID(dir)
	pathBasedID := WorkspaceID(dir)

	assert.NotEqual(t, repoBasedID, pathBasedID,
		"repo-based and path-based IDs should differ when repo_id is present")
}

// TestCurrentWorkspaceID_PrefersRepoID documents the sync.Once caching limitation.
// CurrentWorkspaceID() caches on first call, so we can't test multiple scenarios
// in the same process. Instead we verify the underlying logic:
// - RepoBasedWorkspaceID prefers repo_id when config exists
// - RepoBasedWorkspaceID falls back to path-based when config is absent
// The CurrentWorkspaceID function delegates to RepoBasedWorkspaceID(cwd),
// so if RepoBasedWorkspaceID is correct, CurrentWorkspaceID is correct.
func TestCurrentWorkspaceID_PrefersRepoID(t *testing.T) {
	// verify the preference logic through RepoBasedWorkspaceID
	repoID := "repo_prefer_test"
	dirWithConfig := createInitializedDir(t, repoID)
	dirWithoutConfig := t.TempDir()

	withConfig := RepoBasedWorkspaceID(dirWithConfig)
	withoutConfig := RepoBasedWorkspaceID(dirWithoutConfig)

	// with config: should be hash of repo_id
	expectedHash := sha256.Sum256([]byte(repoID))
	expectedID := hex.EncodeToString(expectedHash[:])[:8]
	assert.Equal(t, expectedID, withConfig)

	// without config: should be hash of path
	assert.Equal(t, WorkspaceID(dirWithoutConfig), withoutConfig)
}

// TestLegacyWorkspaceID verifies LegacyWorkspaceID always returns path-based hash.
// Note: LegacyWorkspaceID uses sync.Once caching tied to os.Getwd(), so we can
// only verify the concept through WorkspaceID (which it delegates to).
func TestLegacyWorkspaceID(t *testing.T) {
	// WorkspaceID is the underlying function LegacyWorkspaceID delegates to.
	// Verify it always produces path-based hashes regardless of config.
	repoID := "repo_legacy_test"
	dir := createInitializedDir(t, repoID)

	pathBasedID := WorkspaceID(dir)
	repoBasedID := RepoBasedWorkspaceID(dir)

	// path-based (legacy) should differ from repo-based when config exists
	assert.NotEqual(t, pathBasedID, repoBasedID,
		"legacy path-based ID should differ from repo-based ID")

	// path-based should be deterministic
	assert.Equal(t, pathBasedID, WorkspaceID(dir))
	assert.Len(t, pathBasedID, 8)
}
