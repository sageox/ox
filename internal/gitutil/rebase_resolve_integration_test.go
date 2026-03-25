package gitutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gitInRepo runs a git command in the given directory with isolated config.
func gitInRepo(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), // safe: git subprocess in temp dir, not ox CLI
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@test.com",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=commit.gpgsign", "GIT_CONFIG_VALUE_0=false",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// setupDivergentRepos creates a bare repo, two clones, commits conflicting
// changes to the same file in both, and starts a rebase in "ours" so it's
// in a conflicted rebase state. Returns (bareDir, oursClone).
func setupDivergentRepos(t *testing.T, filename, oursContent, theirsContent string) (string, string) {
	t.Helper()

	bareDir := filepath.Join(t.TempDir(), "bare.git")
	oursClone := filepath.Join(t.TempDir(), "ours")
	theirsClone := filepath.Join(t.TempDir(), "theirs")

	gitInRepo(t, t.TempDir(), "init", "--bare", "--initial-branch=main", bareDir)

	// initial commit via theirs clone
	gitInRepo(t, t.TempDir(), "clone", bareDir, theirsClone)
	gitInRepo(t, theirsClone, "config", "user.name", "test")
	gitInRepo(t, theirsClone, "config", "user.email", "test@test.com")

	// ensure parent dirs
	dir := filepath.Dir(filepath.Join(theirsClone, filename))
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(theirsClone, filename), []byte("initial"), 0o644))
	gitInRepo(t, theirsClone, "add", filename)
	gitInRepo(t, theirsClone, "commit", "-m", "initial")
	gitInRepo(t, theirsClone, "push", "origin", "HEAD")

	// clone ours
	gitInRepo(t, t.TempDir(), "clone", bareDir, oursClone)
	gitInRepo(t, oursClone, "config", "user.name", "test")
	gitInRepo(t, oursClone, "config", "user.email", "test@test.com")

	// commit conflicting change in ours
	require.NoError(t, os.WriteFile(filepath.Join(oursClone, filename), []byte(oursContent), 0o644))
	gitInRepo(t, oursClone, "add", filename)
	gitInRepo(t, oursClone, "commit", "-m", "local change")

	// commit conflicting change in theirs and push
	require.NoError(t, os.WriteFile(filepath.Join(theirsClone, filename), []byte(theirsContent), 0o644))
	gitInRepo(t, theirsClone, "add", filename)
	gitInRepo(t, theirsClone, "commit", "-m", "remote change")
	gitInRepo(t, theirsClone, "push", "origin", "HEAD")

	// fetch and start rebase in ours (will conflict)
	gitInRepo(t, oursClone, "fetch", "origin")
	cmd := exec.Command("git", "rebase", "origin/main")
	cmd.Dir = oursClone
	cmd.Env = append(os.Environ(), // safe: git subprocess in temp dir, not ox CLI
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=commit.gpgsign", "GIT_CONFIG_VALUE_0=false",
	)
	_ = cmd.Run() // expected to fail with conflict

	require.True(t, IsRebaseInProgress(oursClone), "rebase should be in progress")

	return bareDir, oursClone
}

func TestResolveRebaseAcceptTheirs_SafePathResolves(t *testing.T) {
	_, repo := setupDivergentRepos(t, "data/github/prs.json", `{"local":true}`, `{"remote":true}`)

	err := ResolveRebaseAcceptTheirs(context.Background(), repo, []string{"data/github/"})
	assert.NoError(t, err)

	// rebase should be complete (no longer in progress)
	assert.False(t, IsRebaseInProgress(repo), "rebase should have completed")

	// during rebase, "theirs" = the commits being replayed (local),
	// so accept-theirs keeps local content. For idempotent data like
	// data/github/, either side winning is correct since the next sync
	// cycle re-fetches anyway.
	content, err := os.ReadFile(filepath.Join(repo, "data", "github", "prs.json"))
	require.NoError(t, err)
	assert.Equal(t, `{"local":true}`, string(content))
}

func TestResolveRebaseAcceptTheirs_UnsafePathRejects(t *testing.T) {
	_, repo := setupDivergentRepos(t, "sessions/important.jsonl", "local data", "remote data")

	err := ResolveRebaseAcceptTheirs(context.Background(), repo, []string{"data/github/"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not under safe auto-resolve prefixes")
	assert.Contains(t, err.Error(), "sessions/important.jsonl")

	// rebase should still be in progress (caller must abort)
	assert.True(t, IsRebaseInProgress(repo), "rebase should remain in progress for caller to handle")
}

func TestResolveRebaseAcceptTheirs_MixedSafeAndUnsafe(t *testing.T) {
	// create a repo with two conflicting files: one safe, one unsafe
	bareDir := filepath.Join(t.TempDir(), "bare.git")
	oursClone := filepath.Join(t.TempDir(), "ours")
	theirsClone := filepath.Join(t.TempDir(), "theirs")

	gitInRepo(t, t.TempDir(), "init", "--bare", "--initial-branch=main", bareDir)

	// initial commit
	gitInRepo(t, t.TempDir(), "clone", bareDir, theirsClone)
	gitInRepo(t, theirsClone, "config", "user.name", "test")
	gitInRepo(t, theirsClone, "config", "user.email", "test@test.com")
	require.NoError(t, os.MkdirAll(filepath.Join(theirsClone, "data", "github"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(theirsClone, "data/github/prs.json"), []byte("base"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(theirsClone, "SOUL.md"), []byte("base"), 0o644))
	gitInRepo(t, theirsClone, "add", ".")
	gitInRepo(t, theirsClone, "commit", "-m", "initial")
	gitInRepo(t, theirsClone, "push", "origin", "HEAD")

	// clone ours and make conflicting changes to both files
	gitInRepo(t, t.TempDir(), "clone", bareDir, oursClone)
	gitInRepo(t, oursClone, "config", "user.name", "test")
	gitInRepo(t, oursClone, "config", "user.email", "test@test.com")
	require.NoError(t, os.WriteFile(filepath.Join(oursClone, "data/github/prs.json"), []byte("local-safe"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(oursClone, "SOUL.md"), []byte("local-unsafe"), 0o644))
	gitInRepo(t, oursClone, "add", ".")
	gitInRepo(t, oursClone, "commit", "-m", "local changes")

	// push conflicting remote changes
	require.NoError(t, os.WriteFile(filepath.Join(theirsClone, "data/github/prs.json"), []byte("remote-safe"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(theirsClone, "SOUL.md"), []byte("remote-unsafe"), 0o644))
	gitInRepo(t, theirsClone, "add", ".")
	gitInRepo(t, theirsClone, "commit", "-m", "remote changes")
	gitInRepo(t, theirsClone, "push", "origin", "HEAD")

	// fetch and start rebase (will conflict on both files)
	gitInRepo(t, oursClone, "fetch", "origin")
	cmd := exec.Command("git", "rebase", "origin/main")
	cmd.Dir = oursClone
	cmd.Env = append(os.Environ(), // safe: git subprocess in temp dir, not ox CLI
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=commit.gpgsign", "GIT_CONFIG_VALUE_0=false",
	)
	_ = cmd.Run()
	require.True(t, IsRebaseInProgress(oursClone))

	// should reject because SOUL.md is outside safe prefix
	err := ResolveRebaseAcceptTheirs(context.Background(), oursClone, []string{"data/github/"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "SOUL.md")
	assert.Contains(t, err.Error(), "not under safe auto-resolve prefixes")

	// rebase still in progress — nothing was resolved
	assert.True(t, IsRebaseInProgress(oursClone))
}

func TestResolveRebaseAcceptTheirs_NoConflicts(t *testing.T) {
	// call on a repo that is NOT in a rebase state
	dir := t.TempDir()
	gitInRepo(t, dir, "init", "--initial-branch=main")
	gitInRepo(t, dir, "config", "user.name", "test")
	gitInRepo(t, dir, "config", "user.email", "test@test.com")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file.txt"), []byte("data"), 0o644))
	gitInRepo(t, dir, "add", "file.txt")
	gitInRepo(t, dir, "commit", "-m", "initial")

	err := ResolveRebaseAcceptTheirs(context.Background(), dir, []string{"data/"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no conflicted files found")
}

func TestResolveRebaseAcceptTheirs_MultipleSafeFiles(t *testing.T) {
	// two safe-path files conflict, both should be resolved
	bareDir := filepath.Join(t.TempDir(), "bare.git")
	oursClone := filepath.Join(t.TempDir(), "ours")
	theirsClone := filepath.Join(t.TempDir(), "theirs")

	gitInRepo(t, t.TempDir(), "init", "--bare", "--initial-branch=main", bareDir)

	gitInRepo(t, t.TempDir(), "clone", bareDir, theirsClone)
	gitInRepo(t, theirsClone, "config", "user.name", "test")
	gitInRepo(t, theirsClone, "config", "user.email", "test@test.com")
	require.NoError(t, os.MkdirAll(filepath.Join(theirsClone, "data", "github"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(theirsClone, "data/github/prs.json"), []byte("base-prs"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(theirsClone, "data/github/issues.json"), []byte("base-issues"), 0o644))
	gitInRepo(t, theirsClone, "add", ".")
	gitInRepo(t, theirsClone, "commit", "-m", "initial")
	gitInRepo(t, theirsClone, "push", "origin", "HEAD")

	gitInRepo(t, t.TempDir(), "clone", bareDir, oursClone)
	gitInRepo(t, oursClone, "config", "user.name", "test")
	gitInRepo(t, oursClone, "config", "user.email", "test@test.com")
	require.NoError(t, os.WriteFile(filepath.Join(oursClone, "data/github/prs.json"), []byte("local-prs"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(oursClone, "data/github/issues.json"), []byte("local-issues"), 0o644))
	gitInRepo(t, oursClone, "add", ".")
	gitInRepo(t, oursClone, "commit", "-m", "local")

	require.NoError(t, os.WriteFile(filepath.Join(theirsClone, "data/github/prs.json"), []byte("remote-prs"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(theirsClone, "data/github/issues.json"), []byte("remote-issues"), 0o644))
	gitInRepo(t, theirsClone, "add", ".")
	gitInRepo(t, theirsClone, "commit", "-m", "remote")
	gitInRepo(t, theirsClone, "push", "origin", "HEAD")

	gitInRepo(t, oursClone, "fetch", "origin")
	cmd := exec.Command("git", "rebase", "origin/main")
	cmd.Dir = oursClone
	cmd.Env = append(os.Environ(), // safe: git subprocess in temp dir, not ox CLI
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=commit.gpgsign", "GIT_CONFIG_VALUE_0=false",
	)
	_ = cmd.Run()
	require.True(t, IsRebaseInProgress(oursClone))

	err := ResolveRebaseAcceptTheirs(context.Background(), oursClone, []string{"data/github/"})
	assert.NoError(t, err)
	assert.False(t, IsRebaseInProgress(oursClone))

	// during rebase, "theirs" = local commits being replayed
	prs, _ := os.ReadFile(filepath.Join(oursClone, "data/github/prs.json"))
	issues, _ := os.ReadFile(filepath.Join(oursClone, "data/github/issues.json"))
	assert.Equal(t, "local-prs", string(prs))
	assert.Equal(t, "local-issues", string(issues))
}

func TestResolveRebaseAcceptTheirs_EmptyPrefixes(t *testing.T) {
	_, repo := setupDivergentRepos(t, "data/github/prs.json", `{"local":true}`, `{"remote":true}`)

	// empty prefixes = nothing is safe
	err := ResolveRebaseAcceptTheirs(context.Background(), repo, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not under safe auto-resolve prefixes")

	assert.True(t, IsRebaseInProgress(repo))
}

func TestResolveRebaseAcceptTheirs_DenyPrefixBlocksResolution(t *testing.T) {
	// conflict in data/proprietary/ which is denied even though data/ is allowed
	_, repo := setupDivergentRepos(t, "data/proprietary/secrets.json", `{"local":true}`, `{"remote":true}`)

	err := ResolveRebaseAcceptTheirs(context.Background(), repo,
		[]string{"data/"}, []string{"data/proprietary/"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not under safe auto-resolve prefixes")
	assert.True(t, IsRebaseInProgress(repo), "rebase should still be in progress")
}

func TestResolveRebaseAcceptTheirs_DenyPrefixAllowsSiblingPath(t *testing.T) {
	// conflict in data/github/ should resolve even when data/proprietary/ is denied
	_, repo := setupDivergentRepos(t, "data/github/prs.json", `{"local":true}`, `{"remote":true}`)

	err := ResolveRebaseAcceptTheirs(context.Background(), repo,
		[]string{"data/"}, []string{"data/proprietary/"})
	assert.NoError(t, err)
	assert.False(t, IsRebaseInProgress(repo), "rebase should be complete")
}

func TestResolveRebaseAcceptTheirs_ThreeLevelNesting(t *testing.T) {
	// data/ = auto, data/proprietary/ = none, data/proprietary/public/ = auto
	// conflict in public subdir should resolve
	_, repo := setupDivergentRepos(t, "data/proprietary/public/readme.md", "local", "remote")

	err := ResolveRebaseAcceptTheirs(context.Background(), repo,
		[]string{"data/", "data/proprietary/public/"}, []string{"data/proprietary/"})
	assert.NoError(t, err)
	assert.False(t, IsRebaseInProgress(repo), "rebase should be complete")
}
