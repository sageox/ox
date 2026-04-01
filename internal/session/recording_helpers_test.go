package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// setupRecordingTest creates a proper project context for recording tests.
// Returns projectRoot and sets up XDG cache to point to the given cacheDir.
func setupRecordingTest(t *testing.T, cacheDir string) string {
	t.Helper()
	projectRoot, _ := setupRecordingTestWithSessionsBase(t, cacheDir)
	return projectRoot
}

// setupRecordingTestWithSessionsBase creates a properly initialized project and
// returns both the project root and the sessions base path (in XDG cache).
// Session data must be placed under the returned sessionsBase, never under projectRoot.
func setupRecordingTestWithSessionsBase(t *testing.T, cacheDir string) (projectRoot, sessionsBase string) {
	t.Helper()
	projectRoot = t.TempDir()
	repoID := "test-repo-id"

	// create .sageox/config.json with repo_id (canonical format)
	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	configContent := `{"config_version":"2","repo_id":"` + repoID + `"}`
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte(configContent), 0644))

	// set up environment
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("HOME", cacheDir)
	t.Setenv("XDG_CACHE_HOME", cacheDir)

	// compute sessions base path (matches what sessionsSearchPaths resolves)
	sessionsBase = filepath.Join(GetContextPath(repoID), "sessions")
	return projectRoot, sessionsBase
}

// createTestSessionProject creates a project root with .sageox/ and XDG cache
// suitable for concurrent agent tests. Sessions are stored via repo_id -> XDG cache
// so that LoadAllRecordingStates, IsRecordingForAgent, etc. can find them.
func createTestSessionProject(t *testing.T) (string, string) {
	t.Helper()
	cacheDir := t.TempDir()
	projectRoot := setupRecordingTest(t, cacheDir)
	// second return value is empty string — tests should NOT pass RepoContextPath,
	// letting sessions flow through the XDG cache path that load functions search
	return projectRoot, ""
}
