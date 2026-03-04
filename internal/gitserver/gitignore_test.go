package gitserver

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupGitRepoWithSageox creates a git repo with a committed .sageox/sync.manifest.
func setupGitRepoWithSageox(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, exec.Command("git", "init", "--initial-branch=main", dir).Run())
	require.NoError(t, exec.Command("git", "-C", dir, "config", "user.name", "test").Run())
	require.NoError(t, exec.Command("git", "-C", dir, "config", "user.email", "test@test.com").Run())

	sageoxDir := filepath.Join(dir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "sync.manifest"), []byte("includes: [\"*\"]"), 0644))
	require.NoError(t, exec.Command("git", "-C", dir, "add", ".sageox/sync.manifest").Run())
	require.NoError(t, exec.Command("git", "-C", dir, "commit", "-m", "initial").Run())
	return dir
}

func TestEnsureCheckoutGitignore_CreatesAndCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := setupGitRepoWithSageox(t)

	require.NoError(t, EnsureCheckoutGitignore(dir))

	content, err := os.ReadFile(filepath.Join(dir, ".sageox", ".gitignore"))
	require.NoError(t, err)

	for _, entry := range checkoutRequiredEntries {
		assert.Contains(t, string(content), entry,
			"gitignore should contain required entry: %s", entry)
	}

	// verify it was committed (not untracked)
	output, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(string(output)),
		"gitignore should be committed, not untracked")
}

func TestEnsureCheckoutGitignore_Idempotent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := setupGitRepoWithSageox(t)

	// run twice
	require.NoError(t, EnsureCheckoutGitignore(dir))
	require.NoError(t, EnsureCheckoutGitignore(dir))

	content, err := os.ReadFile(filepath.Join(dir, ".sageox", ".gitignore"))
	require.NoError(t, err)

	// each entry should appear exactly once
	for _, entry := range checkoutRequiredEntries {
		count := strings.Count(string(content), entry)
		assert.Equal(t, 1, count,
			"entry %q should appear exactly once, found %d", entry, count)
	}
}

func TestEnsureCheckoutGitignore_PreservesExisting(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := setupGitRepoWithSageox(t)

	// write an old-style gitignore with custom entries, then commit
	existing := "# Custom\nmy-custom-file.txt\ncheckout.json\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".sageox", ".gitignore"), []byte(existing), 0644))
	require.NoError(t, exec.Command("git", "-C", dir, "add", ".sageox/.gitignore").Run())
	require.NoError(t, exec.Command("git", "-C", dir, "commit", "-m", "existing gitignore").Run())

	require.NoError(t, EnsureCheckoutGitignore(dir))

	content, err := os.ReadFile(filepath.Join(dir, ".sageox", ".gitignore"))
	require.NoError(t, err)

	// custom entries preserved
	assert.Contains(t, string(content), "my-custom-file.txt")
	assert.Contains(t, string(content), "checkout.json")
	// required entries added
	assert.Contains(t, string(content), "*")
	assert.Contains(t, string(content), "!.gitignore")
	assert.Contains(t, string(content), "!sync.manifest")
}

func TestEnsureCheckoutGitignore_NoSageoxDir(t *testing.T) {
	dir := t.TempDir()
	// no .sageox/ — should be a no-op
	require.NoError(t, EnsureCheckoutGitignore(dir))

	_, err := os.Stat(filepath.Join(dir, ".sageox", ".gitignore"))
	assert.True(t, os.IsNotExist(err), "should not create .sageox/.gitignore when .sageox/ doesn't exist")
}

func TestEnsureCheckoutGitignore_CommittedFilesReIncluded(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// The .sageox/.gitignore uses * to ignore everything, then !sync.manifest
	// to re-include it. Files already committed (like sync.manifest) remain
	// tracked regardless, but the re-include ensures new clones can also
	// add them without the gitignore blocking the operation.
	dir := setupGitRepoWithSageox(t)

	require.NoError(t, EnsureCheckoutGitignore(dir))

	content, err := os.ReadFile(filepath.Join(dir, ".sageox", ".gitignore"))
	require.NoError(t, err)

	// sync.manifest must be re-included via negation pattern
	assert.Contains(t, string(content), "!sync.manifest",
		"gitignore must re-include sync.manifest")

	// verify sync.manifest is still tracked
	lsOutput, err := exec.Command("git", "-C", dir, "ls-files", ".sageox/sync.manifest").Output()
	require.NoError(t, err)
	assert.Contains(t, string(lsOutput), "sync.manifest",
		"sync.manifest should still be tracked by git")
}

// TestEnsureCheckoutGitignore_GitStatusClean verifies that after writing
// the gitignore and creating daemon cache files, git status --porcelain
// does not report .sageox/cache/ files as untracked. This is the core
// regression test for the GC reclone blocking bug.
func TestEnsureCheckoutGitignore_GitStatusClean(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := setupGitRepoWithSageox(t)

	// apply the gitignore (simulates what clone does — writes + commits)
	require.NoError(t, EnsureCheckoutGitignore(dir))

	// simulate daemon writing cache files
	cacheDir := filepath.Join(dir, ".sageox", "cache")
	require.NoError(t, os.MkdirAll(cacheDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "sync-state.json"), []byte(`{"last_sync":"2024-01-01T00:00:00Z"}`), 0600))

	// git status should be clean — cache/ is ignored by .sageox/.gitignore
	output, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(string(output)),
		"git status should be clean after gitignore excludes cache/, got: %s", output)
}

// TestEnsureCheckoutGitignore_ForceCommittedFilesStayTracked verifies that
// files which MUST be committed in .sageox/ (like sync.manifest) remain
// tracked even after the gitignore is applied. This is a regression guard
// ensuring the gitignore doesn't accidentally hide committed files.
func TestEnsureCheckoutGitignore_ForceCommittedFilesStayTracked(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := setupGitRepoWithSageox(t)

	// apply gitignore
	require.NoError(t, EnsureCheckoutGitignore(dir))

	// these files MUST remain tracked after gitignore is applied
	mustBeTracked := []string{
		".sageox/sync.manifest",
		".sageox/.gitignore", // the gitignore itself should be committed
	}

	for _, file := range mustBeTracked {
		output, err := exec.Command("git", "-C", dir, "ls-files", file).Output()
		require.NoError(t, err)
		assert.NotEmpty(t, strings.TrimSpace(string(output)),
			"%s must remain tracked by git after gitignore is applied", file)
	}

	// these files should NOT be tracked (daemon-written, ignored by gitignore)
	mustNotBeTracked := []string{
		".sageox/cache/sync-state.json",
		".sageox/checkout.json",
		".sageox/workspaces.jsonl",
	}

	// create them first so we can verify they're ignored
	for _, file := range mustNotBeTracked {
		full := filepath.Join(dir, file)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0755))
		require.NoError(t, os.WriteFile(full, []byte("test"), 0644))
	}

	// verify git status doesn't show them
	output, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(string(output)),
		"daemon-written files should be ignored, got: %s", output)
}

func TestCheckoutGitignoreNeedsFix_NoSageoxDir(t *testing.T) {
	dir := t.TempDir()
	assert.False(t, CheckoutGitignoreNeedsFix(dir))
}

func TestCheckoutGitignoreNeedsFix_MissingFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".sageox"), 0755))
	assert.True(t, CheckoutGitignoreNeedsFix(dir))
}

func TestCheckoutGitignoreNeedsFix_OldStyleFile(t *testing.T) {
	dir := t.TempDir()
	sageoxDir := filepath.Join(dir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	// old-style gitignore that enumerates files — missing * and re-includes
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, ".gitignore"),
		[]byte("checkout.json\nworkspaces.jsonl\ncache/\n"), 0644))
	assert.True(t, CheckoutGitignoreNeedsFix(dir), "should detect old-style gitignore missing * and re-includes")
}

func TestCheckoutGitignoreNeedsFix_IncompleteReIncludes(t *testing.T) {
	dir := t.TempDir()
	sageoxDir := filepath.Join(dir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	// has wildcard but missing !sync.manifest
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, ".gitignore"),
		[]byte("*\n!.gitignore\n"), 0644))
	assert.True(t, CheckoutGitignoreNeedsFix(dir), "should detect missing !sync.manifest")
}

func TestCheckoutGitignoreNeedsFix_Complete(t *testing.T) {
	dir := t.TempDir()
	sageoxDir := filepath.Join(dir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, ".gitignore"),
		[]byte("*\n!.gitignore\n!sync.manifest\n"), 0644))
	assert.False(t, CheckoutGitignoreNeedsFix(dir), "should not need fix when all entries present")
}
