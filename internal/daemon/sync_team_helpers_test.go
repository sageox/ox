package daemon

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitserver"
	"github.com/stretchr/testify/require"
)

// isolateCredentials sets up credential isolation for tests by redirecting
// credential storage to a temp directory and forcing file-based storage.
// Shared across daemon test files (referenced by sync_integration_test.go).
func isolateCredentials(t *testing.T) {
	t.Helper()
	prevConfigDir := gitserver.TestSetConfigDirOverride(t.TempDir())
	prevForceFile := gitserver.TestSetForceFileStorage(true)
	// Daemon tests routinely clone from a local bare repo via `file://` to
	// simulate a remote. Production hardens against this transport
	// (`-c protocol.file.allow=never` in gitserver.TwoPhaseClone and
	// sync.go's bubble-clone path) as CVE-2017-1000117 defense-in-depth, so
	// tests must opt back in. The override is restored on cleanup so it
	// can't leak into tests that don't need it.
	prevAllowFile := gitserver.TestAllowFileTransport
	gitserver.TestAllowFileTransport = true
	t.Cleanup(func() {
		gitserver.TestSetConfigDirOverride(prevConfigDir)
		gitserver.TestSetForceFileStorage(prevForceFile)
		gitserver.TestAllowFileTransport = prevAllowFile
	})
}

// setupProjectWithConfig creates a temp project directory with .sageox/config.local.toml
// and a project config.json pointing to a fake endpoint.
func setupProjectWithConfig(t *testing.T, localConfigContent string) string {
	t.Helper()
	projectDir := t.TempDir()
	sageoxDir := filepath.Join(projectDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	require.NoError(t, os.WriteFile(
		filepath.Join(sageoxDir, "config.local.toml"),
		[]byte(localConfigContent),
		0644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(sageoxDir, "config.json"),
		[]byte(`{"endpoint":"https://fake.test.invalid"}`),
		0644,
	))
	return projectDir
}

// newTestScheduler creates a SyncScheduler configured for testing.
func newTestScheduler(projectDir string) *SyncScheduler {
	cfg := DefaultConfig()
	cfg.ProjectRoot = projectDir
	cfg.TeamContextSyncInterval = time.Minute
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewSyncScheduler(cfg, logger)
}

// --- Two-phase clone tests ---

// setupTeamContextBareRepo creates a bare git repo populated with team context
// files (manifest, SOUL.md, TEAM.md, memory/, and optionally a large denied dir).
// Returns the bare repo path suitable for cloning with file:// URL.
func setupTeamContextBareRepo(t *testing.T, manifestContent string, extraFiles map[string]string) string {
	t.Helper()
	// Bootstrap (init --bare, allowfilter, clone, gitConfig) shared with kb
	// tests via initBareRepo (sync_test_helpers_managed_test.go).
	bareDir, workDir := initBareRepo(t, "team")

	// create .sageox/ directory
	sageoxDir := filepath.Join(workDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	if manifestContent != "" {
		require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "sync.manifest"), []byte(manifestContent), 0644))
	}

	// create core files
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "SOUL.md"), []byte("# Soul\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "TEAM.md"), []byte("# Team\n"), 0644))
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, "memory"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "memory", "notes.md"), []byte("notes\n"), 0644))

	// create extra files (e.g., denied directories)
	for path, content := range extraFiles {
		full := filepath.Join(workDir, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0644))
	}

	// commit and push
	require.NoError(t, exec.Command("git", "-C", workDir, "add", ".").Run())
	require.NoError(t, exec.Command("git", "-C", workDir, "commit", "-m", "initial").Run())
	require.NoError(t, exec.Command("git", "-C", workDir, "push", "origin", "HEAD:main").Run())

	return bareDir
}
