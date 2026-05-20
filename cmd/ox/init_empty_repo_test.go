//go:build !short

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/constants"
	"github.com/sageox/ox/internal/repotools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initEmptyGitRepo creates a bare git init (no commits) in a temp directory.
// Returns the path to the repo root.
func initEmptyGitRepo(t *testing.T) string {
	t.Helper()

	tmpDir := t.TempDir()

	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	require.NoError(t, cmd.Run(), "git init failed")

	// configure git user in the temp repo so commits work
	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = tmpDir
	require.NoError(t, cmd.Run(), "git config user.name failed")

	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = tmpDir
	require.NoError(t, cmd.Run(), "git config user.email failed")

	return tmpDir
}

func TestHasCommits_EmptyRepo(t *testing.T) {
	repoDir := initEmptyGitRepo(t)

	assert.False(t, hasCommits(repoDir), "empty repo should have no commits")
}

func TestHasCommits_RepoWithCommit(t *testing.T) {
	repoDir := initEmptyGitRepo(t)

	// create a file and commit it
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "hello.txt"), []byte("hi"), 0644))

	cmd := exec.Command("git", "add", "hello.txt")
	cmd.Dir = repoDir
	require.NoError(t, cmd.Run())

	cmd = exec.Command("git", "commit", "-m", "first")
	cmd.Dir = repoDir
	require.NoError(t, cmd.Run())

	assert.True(t, hasCommits(repoDir), "repo with a commit should return true")
}

func TestEnsureInitialCommit_EmptyRepo(t *testing.T) {
	repoDir := initEmptyGitRepo(t)

	// precondition: no commits
	require.False(t, hasCommits(repoDir))

	// run the function under test
	err := ensureInitialCommit(repoDir)
	require.NoError(t, err, "ensureInitialCommit should succeed on empty repo")

	// postcondition: repo now has commits
	assert.True(t, hasCommits(repoDir), "repo should have commits after ensureInitialCommit")

	// verify .sageox/README.md exists
	readmePath := filepath.Join(repoDir, ".sageox", "README.md")
	require.FileExists(t, readmePath)

	// verify README content
	content, err := os.ReadFile(readmePath)
	require.NoError(t, err)
	assert.Contains(t, string(content), "SageOx Configuration")
	assert.Contains(t, string(content), "https://sageox.com")

	// verify the file is tracked in git (committed, not just staged)
	cmd := exec.Command("git", "log", "--oneline", "-1")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	require.NoError(t, err)
	assert.Contains(t, string(out), "Initialize SageOx configuration")
}

func TestEnsureInitialCommit_RepoWithExistingCommits(t *testing.T) {
	repoDir := initEmptyGitRepo(t)

	// create a commit
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "existing.txt"), []byte("data"), 0644))
	cmd := exec.Command("git", "add", "existing.txt")
	cmd.Dir = repoDir
	require.NoError(t, cmd.Run())
	cmd = exec.Command("git", "commit", "-m", "pre-existing commit")
	cmd.Dir = repoDir
	require.NoError(t, cmd.Run())

	// ensureInitialCommit should be a no-op
	err := ensureInitialCommit(repoDir)
	require.NoError(t, err, "should be a no-op for repos with commits")

	// verify no .sageox/ directory was created (it's a no-op)
	sageoxDir := filepath.Join(repoDir, ".sageox")
	_, statErr := os.Stat(sageoxDir)
	assert.True(t, os.IsNotExist(statErr), ".sageox/ should not exist after no-op")

	// verify only one commit exists
	cmd = exec.Command("git", "rev-list", "--count", "HEAD")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	require.NoError(t, err)
	assert.Equal(t, "1", strings.TrimSpace(string(out)), "should still have only 1 commit")
}

func TestEnsureInitialCommit_WithoutGitUserConfig(t *testing.T) {
	tmpDir := t.TempDir()

	// This test validates that ensureInitialCommit's SageOx fallback identity
	// is used when the user has no git identity configured anywhere. The
	// testenv package (loaded process-wide) supplies GIT_AUTHOR_NAME/EMAIL +
	// GIT_COMMITTER_NAME/EMAIL so cascade tests don't fail on hostile dev
	// machines — but those env vars MASK the fallback path this test exists
	// to exercise. Unset them entirely for the duration of the test so the
	// production code's identity-resolution falls through to SageOx defaults.
	// (t.Setenv to "" is NOT equivalent — git sees empty-string as "explicit
	// empty identity" and errors with `empty ident name`. We need the var
	// actually absent from the env so git falls through to its config /
	// command-line `-c` identity resolution.)
	for _, k := range []string{
		"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL",
		"GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL",
	} {
		if v, ok := os.LookupEnv(k); ok {
			t.Cleanup(func() { _ = os.Setenv(k, v) })
		} else {
			t.Cleanup(func() { _ = os.Unsetenv(k) })
		}
		_ = os.Unsetenv(k)
	}

	// init repo WITHOUT configuring user.name/user.email
	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	require.NoError(t, cmd.Run())

	// explicitly unset any inherited git config by using local-only scope
	cmd = exec.Command("git", "config", "--local", "--unset-all", "user.name")
	cmd.Dir = tmpDir
	_ = cmd.Run() // ignore error if key doesn't exist

	cmd = exec.Command("git", "config", "--local", "--unset-all", "user.email")
	cmd.Dir = tmpDir
	_ = cmd.Run()

	// ensureInitialCommit should still work using the fallback identity
	err := ensureInitialCommit(tmpDir)
	require.NoError(t, err, "should succeed without git user config using fallback identity")

	assert.True(t, hasCommits(tmpDir), "repo should have commits")

	// verify the commit author uses the fallback
	cmd = exec.Command("git", "log", "--format=%an <%ae>", "-1")
	cmd.Dir = tmpDir
	out, err := cmd.Output()
	require.NoError(t, err)
	assert.Contains(t, string(out), constants.SageOxGitName)
	assert.Contains(t, string(out), constants.SageOxGitEmail)
}

func TestEnsureInitialCommit_Idempotent(t *testing.T) {
	repoDir := initEmptyGitRepo(t)

	// call twice - second call should be a no-op
	require.NoError(t, ensureInitialCommit(repoDir))
	require.NoError(t, ensureInitialCommit(repoDir))

	// verify exactly one commit
	cmd := exec.Command("git", "rev-list", "--count", "HEAD")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	require.NoError(t, err)
	assert.Equal(t, "1", strings.TrimSpace(string(out)), "should have exactly 1 commit after two calls")
}

func TestEnsureInitialCommit_PreservesExistingSageoxDir(t *testing.T) {
	repoDir := initEmptyGitRepo(t)

	// create .sageox/ with a pre-existing file before calling ensureInitialCommit
	sageoxDir := filepath.Join(repoDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	preExistingFile := filepath.Join(sageoxDir, "config.json")
	require.NoError(t, os.WriteFile(preExistingFile, []byte(`{"version":"1"}`), 0644))

	err := ensureInitialCommit(repoDir)
	require.NoError(t, err)

	// the pre-existing file should still be there
	require.FileExists(t, preExistingFile)
	content, err := os.ReadFile(preExistingFile)
	require.NoError(t, err)
	assert.Equal(t, `{"version":"1"}`, string(content), "pre-existing config should be preserved")

	// README.md should also exist (written by ensureInitialCommit)
	readmePath := filepath.Join(sageoxDir, "README.md")
	require.FileExists(t, readmePath)
}

func TestInitialCommitReadmeContent(t *testing.T) {
	// verify the constant has the expected content
	assert.Contains(t, initialCommitReadmeContent, "SageOx Configuration")
	assert.Contains(t, initialCommitReadmeContent, "https://sageox.com")
	assert.True(t, strings.HasPrefix(initialCommitReadmeContent, "# SageOx Configuration"))
}

// TestEnsureInitialCommit_EnablesFingerprinting verifies the full integration:
// git init (no commits) → ensureInitialCommit → ComputeFingerprint succeeds.
//
// Bug being caught: Before ensureInitialCommit, running 'ox init' on a freshly
// 'git init'd repo would fail because ComputeFingerprint requires at least one
// commit. This test catches regressions in that flow.
func TestEnsureInitialCommit_EnablesFingerprinting(t *testing.T) {
	repoDir := initEmptyGitRepo(t)

	origDir, _ := os.Getwd()
	require.NoError(t, os.Chdir(repoDir))
	defer func() { _ = os.Chdir(origDir) }()

	// precondition: fingerprinting fails on empty repo (git log returns exit 128)
	_, err := repotools.ComputeFingerprint()
	require.Error(t, err, "ComputeFingerprint should fail on empty repo (no commits)")

	// run ensureInitialCommit (this is what ox init does)
	require.NoError(t, ensureInitialCommit(repoDir))

	// postcondition: fingerprinting now succeeds because there's a commit
	fp, err := repotools.ComputeFingerprint()
	require.NoError(t, err, "ComputeFingerprint should succeed after ensureInitialCommit")
	require.NotNil(t, fp, "fingerprint should not be nil after ensureInitialCommit")
	assert.NotEmpty(t, fp.FirstCommit, "fingerprint should have a first commit hash")
}
