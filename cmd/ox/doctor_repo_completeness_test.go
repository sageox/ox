package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scaffoldRepoForDoctor creates a temp git repo and chdir's into it so
// findGitRoot() resolves. Returns the root path.
func scaffoldRepoForDoctor(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	resolved, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	root = resolved

	gi := exec.Command("git", "init", "-q", "-b", "main")
	gi.Dir = root
	require.NoError(t, gi.Run())
	// Identity for commits.
	for _, kv := range [][]string{{"user.name", "test"}, {"user.email", "test@sageox.ai"}} {
		c := exec.Command("git", "config", kv[0], kv[1])
		c.Dir = root
		require.NoError(t, c.Run())
	}

	prev, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(prev) })

	return root
}

func commitFileRepoCompleteness(t *testing.T, dir, name, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
	c1 := exec.Command("git", "add", name)
	c1.Dir = dir
	require.NoError(t, c1.Run())
	c2 := exec.Command("git", "commit", "-q", "-m", "add "+name)
	c2.Dir = dir
	c2.Env = append(os.Environ(), // safe: git CLI in temp dir, not ox subprocess
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@sageox.ai",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@sageox.ai")
	out, err := c2.CombinedOutput()
	require.NoError(t, err, "git commit: %s", out)
}

func TestCheckRepoCompleteness_NormalRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	root := scaffoldRepoForDoctor(t)
	commitFileRepoCompleteness(t, root, "f1.txt", "v1")

	res := checkRepoCompleteness(false)
	assert.True(t, res.passed, "normal repo: %+v", res)
}

func TestCheckRepoCompleteness_Shallow(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	// Build a source with multiple commits, then clone shallow.
	src := t.TempDir()
	gi := exec.Command("git", "init", "-q", "-b", "main")
	gi.Dir = src
	require.NoError(t, gi.Run())
	for i := 0; i < 3; i++ {
		require.NoError(t, os.WriteFile(filepath.Join(src, "f"+string(rune('a'+i))+".txt"), []byte("v"), 0o644))
		add := exec.Command("git", "add", ".")
		add.Dir = src
		require.NoError(t, add.Run())
		ci := exec.Command("git", "commit", "-q", "-m", "c")
		ci.Dir = src
		ci.Env = append(os.Environ(), // safe: git CLI in temp dir, not ox subprocess
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		require.NoError(t, ci.Run())
	}

	parent := t.TempDir()
	dst := filepath.Join(parent, "shallow")
	clone := exec.Command("git", "clone", "-q", "--depth", "1", "--no-local", "file://"+src, dst)
	out, err := clone.CombinedOutput()
	require.NoError(t, err, "%s", out)

	resolved, err := filepath.EvalSymlinks(dst)
	require.NoError(t, err)
	prev, _ := os.Getwd()
	require.NoError(t, os.Chdir(resolved))
	t.Cleanup(func() { _ = os.Chdir(prev) })

	res := checkRepoCompleteness(false)
	assert.True(t, res.warning, "shallow clone should warn, got %+v", res)
	assert.Contains(t, res.message, "shallow")
}

func TestCheckRepoCompleteness_NotARepo(t *testing.T) {
	dir := t.TempDir()
	prev, _ := os.Getwd()
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(prev) })

	res := checkRepoCompleteness(false)
	assert.True(t, res.skipped, "non-repo should skip, got %+v", res)
}

func TestCheckGitAlternates_NoAlternates(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	root := scaffoldRepoForDoctor(t)
	commitFileRepoCompleteness(t, root, "f1.txt", "v1")

	res := checkGitAlternates(false)
	assert.True(t, res.passed, "%+v", res)
}

func TestCheckGitAlternates_WithSharedClone(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	src := t.TempDir()
	gi := exec.Command("git", "init", "-q", "-b", "main")
	gi.Dir = src
	require.NoError(t, gi.Run())
	require.NoError(t, os.WriteFile(filepath.Join(src, "f.txt"), []byte("v"), 0o644))
	add := exec.Command("git", "add", ".")
	add.Dir = src
	require.NoError(t, add.Run())
	ci := exec.Command("git", "commit", "-q", "-m", "c")
	ci.Dir = src
	ci.Env = append(os.Environ(), // safe: git CLI in temp dir, not ox subprocess
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	require.NoError(t, ci.Run())

	parent := t.TempDir()
	dst := filepath.Join(parent, "shared")
	clone := exec.Command("git", "clone", "-q", "--shared", "--no-checkout", src, dst)
	out, err := clone.CombinedOutput()
	require.NoError(t, err, "%s", out)

	resolved, err := filepath.EvalSymlinks(dst)
	require.NoError(t, err)
	prev, _ := os.Getwd()
	require.NoError(t, os.Chdir(resolved))
	t.Cleanup(func() { _ = os.Chdir(prev) })

	res := checkGitAlternates(false)
	assert.True(t, res.warning || !res.passed, "shared clone should not pass cleanly: %+v", res)
	assert.True(t, strings.Contains(res.detail, "go-git") || strings.Contains(res.message, "alternate"),
		"detail should mention codedb/go-git impact: %q / %q", res.message, res.detail)
}
