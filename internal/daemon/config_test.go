package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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
