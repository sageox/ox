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

// runGit runs a git command in the given directory and fails the test on error.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %s", args, string(out))
}

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

// TestEnsureGitignoreBeforeCommit_UntracksRootCodeDB is a regression test for the
// bug where codedb/ was committed to the ledger root because CodeDBSharedDir
// pointed to the ledger root instead of .sageox/cache/codedb/.
func TestEnsureGitignoreBeforeCommit_UntracksRootCodeDB(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := setupGitRepoWithSageox(t)

	// simulate: codedb/ was committed at the ledger root (the old bug)
	codedbDir := filepath.Join(dir, "codedb")
	require.NoError(t, os.MkdirAll(codedbDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(codedbDir, "index.db"), []byte("fake-db"), 0644))
	runGit(t, dir, "add", "codedb/")
	runGit(t, dir, "commit", "-m", "accidentally commit codedb")

	// verify codedb/ is tracked before the fix
	lsOut, err := exec.Command("git", "-C", dir, "ls-files", "codedb/").Output()
	require.NoError(t, err)
	assert.NotEmpty(t, strings.TrimSpace(string(lsOut)), "codedb/ should be tracked before fix")

	// run the pre-commit guard
	EnsureGitignoreBeforeCommit(dir)

	// verify codedb/ is no longer tracked
	lsOut, err = exec.Command("git", "-C", dir, "ls-files", "codedb/").Output()
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(string(lsOut)), "codedb/ should be untracked after fix")

	// verify codedb/ directory was deleted from disk (prevents git add -A re-staging)
	_, err = os.Stat(codedbDir)
	assert.True(t, os.IsNotExist(err), "codedb/ should be deleted from disk after fix")
}

// TestEnsureCheckoutGitignore_WithSparseCheckout is a regression test for the
// bug where git add .sageox/.gitignore fails during TwoPhaseClone because
// sparse-checkout blocks staging files outside the sparse definition.
// Without --sparse on git add, this test fails with:
//
//	"The following paths and/or pathspecs matched paths that exist
//	 outside of your sparse-checkout definition"
func TestEnsureCheckoutGitignore_WithSparseCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := setupGitRepoWithSageox(t)

	// enable sparse-checkout in --no-cone mode (same as TwoPhaseClone)
	runGit(t, dir, "sparse-checkout", "set", "--no-cone", ".sageox/")

	// this is the exact call that fails during TwoPhaseClone (line 106)
	require.NoError(t, EnsureCheckoutGitignore(dir))

	// verify file exists with required entries
	content, err := os.ReadFile(filepath.Join(dir, ".sageox", ".gitignore"))
	require.NoError(t, err)
	for _, entry := range checkoutRequiredEntries {
		assert.Contains(t, string(content), entry)
	}

	// verify committed (not untracked)
	output, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(string(output)),
		"gitignore should be committed, not untracked")
}

// TestEnsureGitignoreBeforeCommit_IgnoresAndUntracksRej is the regression for
// .rej patch-reject artifacts polluting ledger history. The class: any junk a
// broad `git add -A` can sweep in must be ignored repo-wide and untracked once
// detected. Failure prevented: blue-green GC `git apply --reject` artifacts
// committed into the ledger forever.
func TestEnsureGitignoreBeforeCommit_IgnoresAndUntracksRej(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	runGit(t, dir, "init", "--initial-branch=main")
	runGit(t, dir, "config", "user.name", "test")
	runGit(t, dir, "config", "user.email", "test@test.com")
	runGit(t, dir, "config", "commit.gpgsign", "false")

	// commit a .rej deep in a session dir, mirroring the real pollution path
	relRej := filepath.Join("sessions", "2026-01-01T00-00-x", "meta.json.rej")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, filepath.Dir(relRej)), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, relRej), []byte("<<<<<<< reject"), 0644))
	runGit(t, dir, "add", relRej)
	runGit(t, dir, "commit", "-m", "oops: committed a .rej")

	tracked, err := RejFilesTracked(dir)
	require.NoError(t, err)
	require.True(t, tracked, "fixture should leave a tracked .rej")

	EnsureGitignoreBeforeCommit(dir)

	// untracked from the index (local file may remain; doctor deletes it)
	tracked, err = RejFilesTracked(dir)
	require.NoError(t, err)
	assert.False(t, tracked, "EnsureGitignoreBeforeCommit must untrack .rej")

	// *.rej now ignored repo-wide, so future adds skip it
	out, err := exec.Command("git", "-C", dir, "check-ignore", relRej).CombinedOutput()
	require.NoError(t, err, "check-ignore should match: %s", out)
	assert.Equal(t, relRej, strings.TrimSpace(string(out)))

	// commit the untrack (clears the staged deletion), then drop a FRESH .rej
	// and prove a broad `git add -A` won't sweep it back into the index.
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "untrack .rej")
	require.NoError(t, os.WriteFile(filepath.Join(dir, relRej), []byte("new reject"), 0644))
	fresh := filepath.Join("sessions", "2026-02-02T00-00-y", "session.md.rej")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, filepath.Dir(fresh)), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, fresh), []byte("another"), 0644))

	runGit(t, dir, "add", "-A")
	staged, err := exec.Command("git", "-C", dir, "diff", "--cached", "--name-only").Output()
	require.NoError(t, err)
	assert.NotContains(t, string(staged), ".rej", "a broad add must not stage ignored .rej")
	tracked, err = RejFilesTracked(dir)
	require.NoError(t, err)
	assert.False(t, tracked, "no .rej should be tracked after the sweep")
}

// TestEnsureRootGitignoreEntry_Idempotent verifies the root-.gitignore writer
// appends once and is a no-op on repeat — so repeated daemon/doctor passes
// don't grow the file.
func TestEnsureRootGitignoreEntry_Idempotent(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, ensureRootGitignoreEntry(dir, "*.rej"))
	require.NoError(t, ensureRootGitignoreEntry(dir, "*.rej"))
	content, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(content), "*.rej"), "entry must be written exactly once")
}

// TestEnsureGitignoreBeforeCommit_StagesRootGitignore is the regression for the
// class: the guard WRITES a root .gitignore (the repo-wide *.rej ignore) that a
// caller staging NARROWLY — an explicit pathspec, not `git add -A` — never
// commits, so the file lingers untracked and the post-commit worktree is dirty.
// A dirty worktree here trips the pointer-commit clean-worktree (autostash-race)
// invariant that guards ledger sync. The guard must therefore stage what it
// writes, symmetric with its `git rm --cached *.rej` untrack, so the caller's
// commit tracks it regardless of how narrowly that caller stages.
//
// Adversarial design: this commits with a NARROW pathspec (an unrelated file),
// deliberately NOT `git add -A`. A `-A` commit would sweep up the untracked
// .gitignore and pass even with the fix reverted — that is exactly the theater
// this test avoids. Neutering the fix (dropping the guard's `git add .gitignore`)
// must turn this red at both the staged-index and clean-worktree assertions.
func TestEnsureGitignoreBeforeCommit_StagesRootGitignore(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	runGit(t, dir, "init", "--initial-branch=main")
	runGit(t, dir, "config", "user.name", "test")
	runGit(t, dir, "config", "user.email", "test@test.com")
	// commit directly here (not via gitutil.RunGit), so neutralize any global
	// signing config that would hang a non-interactive commit in this test.
	runGit(t, dir, "config", "commit.gpgsign", "false")

	// the caller's own artifact — staged with an explicit pathspec, mirroring
	// commitAndPushLedger (meta.json + specific files), NOT a broad `git add -A`.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "payload.txt"), []byte("x"), 0644))

	EnsureGitignoreBeforeCommit(dir)

	// 1. the guard must have STAGED the root .gitignore it wrote — present in the
	//    index, not merely sitting untracked on disk.
	staged, err := exec.Command("git", "-C", dir, "diff", "--cached", "--name-only").Output()
	require.NoError(t, err)
	assert.Contains(t, strings.Split(strings.TrimSpace(string(staged)), "\n"), ".gitignore",
		"guard must stage the root .gitignore it writes")

	// 2. the caller stages ONLY its own file, then commits the index. This is the
	//    narrow-staging path the bug lives on.
	runGit(t, dir, "add", "--sparse", "payload.txt")
	runGit(t, dir, "commit", "--no-verify", "-m", "narrow commit")

	// 3. worktree must be clean — no `?? .gitignore` left behind.
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(string(out)),
		"worktree must be clean after a narrow-pathspec commit; an untracked root .gitignore here trips the pointer-commit clean-worktree invariant, got: %s", out)

	// 4. and the root .gitignore is genuinely tracked now (belt to the status check).
	ls, err := exec.Command("git", "-C", dir, "ls-files", ".gitignore").Output()
	require.NoError(t, err)
	assert.NotEmpty(t, strings.TrimSpace(string(ls)), "root .gitignore must be tracked after the commit")
}
