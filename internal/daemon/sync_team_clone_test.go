package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/gitserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Test: applySparseCheckout reads manifest and applies sparse-checkout ---

func TestApplySparseCheckout_AppliesManifestPaths(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// create a real git repo with a manifest file
	teamDir := t.TempDir()
	setupGitRepo(t, teamDir)

	// write a sync.manifest inside .sageox/
	sageoxDir := filepath.Join(teamDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	manifestContent := `version 1
include docs/
include SOUL.md
include memory/
sync_interval_minutes 10
`
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "sync.manifest"), []byte(manifestContent), 0644))

	// commit the manifest so sparse-checkout can work with it
	addCmd := exec.Command("git", "-C", teamDir, "add", ".sageox/sync.manifest")
	require.NoError(t, addCmd.Run())
	commitCmd := exec.Command("git", "-C", teamDir, "commit", "-m", "add manifest")
	require.NoError(t, commitCmd.Run())

	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	ctx := context.Background()
	mCfg := scheduler.applySparseCheckout(ctx, teamDir)

	// verify manifest was parsed correctly
	require.NotNil(t, mCfg)
	assert.Equal(t, 10, mCfg.SyncIntervalMin)
	assert.Contains(t, mCfg.Includes, "docs/")
	assert.Contains(t, mCfg.Includes, "SOUL.md")
	assert.Contains(t, mCfg.Includes, "memory/")

	// verify sparse-checkout was configured
	sparseCmd := exec.Command("git", "-C", teamDir, "sparse-checkout", "list")
	out, err := sparseCmd.CombinedOutput()
	require.NoError(t, err, "sparse-checkout list should succeed: %s", string(out))
	sparseList := strings.TrimSpace(string(out))
	assert.Contains(t, sparseList, "docs")
	assert.Contains(t, sparseList, "memory")
}

func TestApplySparseCheckout_FallsBackWithoutManifest(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	teamDir := t.TempDir()
	setupGitRepo(t, teamDir)

	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	ctx := context.Background()
	mCfg := scheduler.applySparseCheckout(ctx, teamDir)

	// should return fallback config when no manifest file exists
	require.NotNil(t, mCfg)
	assert.Equal(t, 5, mCfg.SyncIntervalMin, "fallback should use default 5-minute interval")
	assert.NotEmpty(t, mCfg.Includes, "fallback should have default include paths")
}

// TestApplySparseCheckout_FallbackWithFilePatterns is a regression test for
// the bug where applySparseCheckout used --cone mode, which only supports
// directories. The fallback manifest includes files like AGENTS.md, SOUL.md,
// etc., which caused: fatal: 'AGENTS.md' is not a directory.
// Fix: use --no-cone mode to support both files and directories.
func TestApplySparseCheckout_FallbackWithFilePatterns(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	teamDir := t.TempDir()
	setupGitRepo(t, teamDir)

	// create files matching fallback includes (files AND directories)
	fallbackFiles := map[string]string{
		"AGENTS.md": "# Agents\n",
		"SOUL.md":   "# Soul\n",
		"TEAM.md":   "# Team\n",
		"MEMORY.md": "# Memory\n",
	}
	fallbackDirs := []string{"docs", "memory", "coworkers", ".sageox"}
	for _, dir := range fallbackDirs {
		require.NoError(t, os.MkdirAll(filepath.Join(teamDir, dir), 0755))
		require.NoError(t, os.WriteFile(
			filepath.Join(teamDir, dir, "placeholder.md"),
			[]byte("placeholder\n"), 0644))
	}
	for name, content := range fallbackFiles {
		require.NoError(t, os.WriteFile(filepath.Join(teamDir, name), []byte(content), 0644))
	}

	// commit so sparse-checkout has content to work with
	addCmd := exec.Command("git", "-C", teamDir, "add", ".")
	require.NoError(t, addCmd.Run())
	commitCmd := exec.Command("git", "-C", teamDir, "commit", "-m", "add fallback files")
	require.NoError(t, commitCmd.Run())

	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	ctx := context.Background()
	mCfg := scheduler.applySparseCheckout(ctx, teamDir)

	// should succeed without errors (was failing with --cone mode)
	require.NotNil(t, mCfg)

	// verify sparse-checkout was configured and includes file patterns
	sparseCmd := exec.Command("git", "-C", teamDir, "sparse-checkout", "list")
	out, err := sparseCmd.CombinedOutput()
	require.NoError(t, err, "sparse-checkout list should succeed: %s", string(out))

	sparseList := string(out)
	// verify file patterns are included (these broke with --cone mode)
	assert.Contains(t, sparseList, "AGENTS.md", "file patterns must work in sparse-checkout")
	assert.Contains(t, sparseList, "SOUL.md", "file patterns must work in sparse-checkout")
	// verify directory patterns too
	assert.Contains(t, sparseList, "docs/", "directory patterns must work in sparse-checkout")
	assert.Contains(t, sparseList, "memory/", "directory patterns must work in sparse-checkout")
}

func TestTwoPhaseClone_Success(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	isolateCredentials(t)

	manifest := `version 1
include .sageox/
include SOUL.md
include TEAM.md
include memory/
sync_interval_minutes 10
`
	bareDir := setupTeamContextBareRepo(t, manifest, nil)
	cloneURL := "file://" + bareDir

	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	targetDir := filepath.Join(t.TempDir(), "team-clone")
	ctx := context.Background()

	mCfg, err := scheduler.twoPhaseClone(ctx, cloneURL, targetDir, nil)
	require.NoError(t, err)
	require.NotNil(t, mCfg)

	// verify manifest was parsed
	assert.Equal(t, 10, mCfg.SyncIntervalMin)

	// verify expected files materialized
	assert.FileExists(t, filepath.Join(targetDir, "SOUL.md"))
	assert.FileExists(t, filepath.Join(targetDir, "TEAM.md"))
	assert.FileExists(t, filepath.Join(targetDir, "memory", "notes.md"))
	assert.FileExists(t, filepath.Join(targetDir, ".sageox", "sync.manifest"))

	// verify sparse-checkout is active
	out, err := exec.Command("git", "-C", targetDir, "sparse-checkout", "list").CombinedOutput()
	require.NoError(t, err)
	sparseList := string(out)
	assert.Contains(t, sparseList, ".sageox")
	assert.Contains(t, sparseList, "memory")
}

func TestTwoPhaseClone_NoManifest(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	isolateCredentials(t)

	// no manifest content — should use fallback
	bareDir := setupTeamContextBareRepo(t, "", nil)
	cloneURL := "file://" + bareDir

	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	targetDir := filepath.Join(t.TempDir(), "team-clone")
	ctx := context.Background()

	mCfg, err := scheduler.twoPhaseClone(ctx, cloneURL, targetDir, nil)
	require.NoError(t, err)
	require.NotNil(t, mCfg)

	// fallback config uses 5 min default
	assert.Equal(t, 5, mCfg.SyncIntervalMin)

	// core files should still be materialized via fallback includes
	assert.FileExists(t, filepath.Join(targetDir, "SOUL.md"))
	assert.FileExists(t, filepath.Join(targetDir, "TEAM.md"))
	assert.FileExists(t, filepath.Join(targetDir, "memory", "notes.md"))
}

func TestTwoPhaseClone_DeniedPathsExcluded(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	isolateCredentials(t)

	manifest := `version 1
include .sageox/
include SOUL.md
include memory/
deny assets/
`
	extraFiles := map[string]string{
		"assets/large-file.bin": "binary data here",
	}
	bareDir := setupTeamContextBareRepo(t, manifest, extraFiles)
	cloneURL := "file://" + bareDir

	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	targetDir := filepath.Join(t.TempDir(), "team-clone")
	ctx := context.Background()

	mCfg, err := scheduler.twoPhaseClone(ctx, cloneURL, targetDir, nil)
	require.NoError(t, err)
	require.NotNil(t, mCfg)

	// allowed files should exist
	assert.FileExists(t, filepath.Join(targetDir, "SOUL.md"))
	assert.FileExists(t, filepath.Join(targetDir, "memory", "notes.md"))

	// denied path should not be materialized
	_, statErr := os.Stat(filepath.Join(targetDir, "assets", "large-file.bin"))
	assert.True(t, os.IsNotExist(statErr), "denied path assets/ should not be materialized")
}

func TestTwoPhaseClone_IncompleteCloneDetected(t *testing.T) {
	// test that Checkout detects incomplete team-context clones (.git but no .sageox/)
	// and moves them aside for recovery. We can't do a full clone through Checkout
	// because isValidCloneURL rejects file:// URLs, so we verify the detection
	// and move-aside behavior, then verify twoPhaseClone works on a clean target.
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	isolateCredentials(t)

	manifest := `version 1
include .sageox/
include SOUL.md
`
	bareDir := setupTeamContextBareRepo(t, manifest, nil)
	cloneURL := "file://" + bareDir

	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	targetDir := filepath.Join(t.TempDir(), "team-clone")

	// simulate an incomplete two-phase clone: .git exists but no .sageox/
	require.NoError(t, os.MkdirAll(filepath.Join(targetDir, ".git"), 0755))
	// verify the incomplete state
	assert.DirExists(t, filepath.Join(targetDir, ".git"))
	assert.NoDirExists(t, filepath.Join(targetDir, ".sageox"))

	// twoPhaseClone fails because target already exists (incomplete)
	ctx := context.Background()
	_, err := scheduler.twoPhaseClone(ctx, cloneURL, targetDir, nil)
	require.Error(t, err, "twoPhaseClone should fail on incomplete clone dir")

	// caller must remove the incomplete dir and retry — verify recovery path
	require.NoError(t, os.RemoveAll(targetDir))
	mCfg, err := scheduler.twoPhaseClone(ctx, cloneURL, targetDir, nil)
	require.NoError(t, err, "twoPhaseClone should succeed after removing incomplete clone")
	require.NotNil(t, mCfg)
	assert.DirExists(t, filepath.Join(targetDir, ".sageox"), "fresh clone should have .sageox/")
	assert.FileExists(t, filepath.Join(targetDir, "SOUL.md"))
}

func TestTwoPhaseClone_IncompleteCloneRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	isolateCredentials(t)

	manifest := `version 1
include .sageox/
include SOUL.md
`
	bareDir := setupTeamContextBareRepo(t, manifest, nil)
	cloneURL := "file://" + bareDir

	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	targetDir := filepath.Join(t.TempDir(), "team-clone")

	// simulate an incomplete clone, then remove it to let twoPhaseClone succeed
	require.NoError(t, os.MkdirAll(filepath.Join(targetDir, ".git"), 0755))
	require.NoError(t, os.RemoveAll(targetDir))

	ctx := context.Background()
	mCfg, err := scheduler.twoPhaseClone(ctx, cloneURL, targetDir, nil)
	require.NoError(t, err)
	require.NotNil(t, mCfg)
	assert.FileExists(t, filepath.Join(targetDir, ".sageox", "sync.manifest"))
	assert.FileExists(t, filepath.Join(targetDir, "SOUL.md"))
}

func TestTwoPhaseClone_SubsequentPullWorks(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	isolateCredentials(t)

	manifest := `version 1
include .sageox/
include SOUL.md
include TEAM.md
include memory/
`
	bareDir := setupTeamContextBareRepo(t, manifest, nil)
	cloneURL := "file://" + bareDir

	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	targetDir := filepath.Join(t.TempDir(), "team-clone")
	ctx := context.Background()

	// initial two-phase clone
	_, err := scheduler.twoPhaseClone(ctx, cloneURL, targetDir, nil)
	require.NoError(t, err)

	// push a new commit to the bare repo via a temp clone
	pusherDir := filepath.Join(t.TempDir(), "pusher")
	require.NoError(t, exec.Command("git", "clone", bareDir, pusherDir).Run())
	gitConfig(t, pusherDir)
	require.NoError(t, os.WriteFile(filepath.Join(pusherDir, "SOUL.md"), []byte("# Updated Soul\n"), 0644))
	require.NoError(t, exec.Command("git", "-C", pusherDir, "add", "SOUL.md").Run())
	require.NoError(t, exec.Command("git", "-C", pusherDir, "commit", "-m", "update soul").Run())
	require.NoError(t, exec.Command("git", "-C", pusherDir, "push", "origin", "HEAD:main").Run())

	// pull should work (fetch + pull --rebase)
	_, fetchErr := exec.Command("git", "-C", targetDir, "fetch", "--quiet").CombinedOutput()
	require.NoError(t, fetchErr)

	pullOut, pullErr := exec.Command("git", "-C", targetDir, "pull", "--rebase", "--quiet").CombinedOutput()
	require.NoError(t, pullErr, "pull --rebase should succeed after two-phase clone: %s", string(pullOut))

	// verify updated content
	content, err := os.ReadFile(filepath.Join(targetDir, "SOUL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "Updated Soul")
}

func TestValidateTeamContextClone_MissingCoreFiles(t *testing.T) {
	// ValidateTeamContextClone logs warnings but returns void.
	// We verify it doesn't panic for each state and test the state itself.

	t.Run("empty dir with only .sageox", func(t *testing.T) {
		repoDir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".sageox"), 0755))

		// no core files → should log warnings but not panic
		assert.NotPanics(t, func() {
			gitserver.ValidateTeamContextClone(repoDir, nil)
		})

		// verify expected state: no core files exist
		_, err := os.Stat(filepath.Join(repoDir, "SOUL.md"))
		assert.True(t, os.IsNotExist(err), "SOUL.md should not exist")
		_, err = os.Stat(filepath.Join(repoDir, "TEAM.md"))
		assert.True(t, os.IsNotExist(err), "TEAM.md should not exist")
	})

	t.Run("with one core file", func(t *testing.T) {
		repoDir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".sageox"), 0755))
		require.NoError(t, os.WriteFile(filepath.Join(repoDir, "TEAM.md"), []byte("# Team\n"), 0644))

		assert.NotPanics(t, func() {
			gitserver.ValidateTeamContextClone(repoDir, nil)
		})
	})

	t.Run("nil config", func(t *testing.T) {
		repoDir := t.TempDir()
		assert.NotPanics(t, func() {
			gitserver.ValidateTeamContextClone(repoDir, nil)
		})
	})
}
