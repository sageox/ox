package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/manifest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. Diverged branch detection ---
// These tests verify that detectDivergedBranches correctly distinguishes
// normal fast-forward pushes from force-pushed/diverged histories.
// Failure prevented: silent data loss when remote is force-pushed and
// the daemon doesn't notice, or false-positive diverge warnings on normal pushes.

func TestDetectDivergedBranches_NormalPush(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}

	tmpDir := t.TempDir()
	cloneDir := filepath.Join(tmpDir, "clone")
	require.NoError(t, os.MkdirAll(cloneDir, 0755))
	setupGitRepo(t, cloneDir)
	bareDir := bareRepoPath(cloneDir)

	// push a normal commit from a separate clone
	pushFromSeparateClone(t, bareDir, "remote.txt", "remote content")

	// fetch in our clone so we see the new commit
	gitCmd(t, cloneDir, "fetch", "origin")

	s := newPullTestScheduler(t, cloneDir)
	assert.False(t, s.detectDivergedBranches(context.Background()),
		"normal fast-forward should not be detected as diverged")
}

func TestDetectDivergedBranches_Diverged(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}

	tmpDir := t.TempDir()
	cloneDir := filepath.Join(tmpDir, "clone")
	require.NoError(t, os.MkdirAll(cloneDir, 0755))
	setupGitRepo(t, cloneDir)
	bareDir := bareRepoPath(cloneDir)

	// make a local commit
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "local.txt"), []byte("local"), 0o644))
	gitCmd(t, cloneDir, "add", "local.txt")
	gitCmd(t, cloneDir, "commit", "-m", "local commit")

	// force push a rewritten history from a separate clone
	tmpClone := filepath.Join(t.TempDir(), "force-clone")
	gitCmd(t, t.TempDir(), "clone", bareDir, tmpClone)
	gitCmd(t, tmpClone, "config", "user.name", "test")
	gitCmd(t, tmpClone, "config", "user.email", "test@test.com")
	require.NoError(t, os.WriteFile(filepath.Join(tmpClone, "rewritten.txt"), []byte("rewritten"), 0o644))
	gitCmd(t, tmpClone, "add", "rewritten.txt")
	gitCmd(t, tmpClone, "commit", "-m", "rewritten history")
	gitCmd(t, tmpClone, "push", "--force", "origin", "HEAD")

	// fetch the force-pushed changes
	gitCmd(t, cloneDir, "fetch", "origin")

	s := newPullTestScheduler(t, cloneDir)
	assert.True(t, s.detectDivergedBranches(context.Background()),
		"diverged branches should be detected as force push")
}

func TestDetectDivergedBranches_NoRemote(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	dir := t.TempDir()
	gitCmd(t, dir, "init")
	gitCmd(t, dir, "config", "user.name", "test")
	gitCmd(t, dir, "config", "user.email", "test@test.com")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file.txt"), []byte("data"), 0o644))
	gitCmd(t, dir, "add", "file.txt")
	gitCmd(t, dir, "commit", "-m", "init")

	s := newPullTestScheduler(t, dir)
	assert.False(t, s.detectDivergedBranches(context.Background()),
		"repo with no remote should return false gracefully")
}

// --- B. Rebase and conflict handling ---
// These tests verify that doPull correctly handles diverged ledger repos:
// successful rebase, auto-resolve in safe paths, abort on unsafe conflicts.
// Failure prevented: data loss from unresolved conflicts, stuck rebase state,
// or failure to report issues when auto-resolve cannot fix conflicts.

func TestDoPull_DivergedLedger_RebasesSuccessfully(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}

	tmpDir := t.TempDir()
	cloneDir := filepath.Join(tmpDir, "clone")
	require.NoError(t, os.MkdirAll(cloneDir, 0755))
	setupGitRepo(t, cloneDir)
	bareDir := bareRepoPath(cloneDir)

	// create a local commit (simulates CLI session upload)
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "local-session.txt"), []byte("session data"), 0o644))
	gitCmd(t, cloneDir, "add", "local-session.txt")
	gitCmd(t, cloneDir, "commit", "-m", "local session")

	// push a different file from a separate clone (simulates cloud/github sync)
	pushFromSeparateClone(t, bareDir, "remote-data.txt", "github sync data")

	s := newPullTestScheduler(t, cloneDir)

	err := s.doPull(context.Background(), nil, true, true)
	assert.NoError(t, err, "doPull should succeed by rebasing diverged branches")

	// verify both files exist (rebase landed both)
	assert.FileExists(t, filepath.Join(cloneDir, "local-session.txt"))
	assert.FileExists(t, filepath.Join(cloneDir, "remote-data.txt"))

	// verify no diverged or merge conflict issue was set
	for _, issue := range s.issues.GetIssues() {
		assert.NotEqual(t, IssueTypeDiverged, issue.Type,
			"no diverged issue should be set after successful rebase")
		assert.NotEqual(t, IssueTypeMergeConflict, issue.Type,
			"no merge conflict issue should be set after clean rebase")
	}
}

func TestDoPull_DivergedLedger_ConflictInSafePath_AutoResolves(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}

	tmpDir := t.TempDir()
	cloneDir := filepath.Join(tmpDir, "clone")
	require.NoError(t, os.MkdirAll(cloneDir, 0755))
	setupGitRepo(t, cloneDir)
	bareDir := bareRepoPath(cloneDir)

	// local commit: modify a file under data/ (safe auto-resolve path)
	require.NoError(t, os.MkdirAll(filepath.Join(cloneDir, "data", "github"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "data", "github", "prs.json"), []byte(`{"local":true}`), 0o644))
	gitCmd(t, cloneDir, "add", "data/github/prs.json")
	gitCmd(t, cloneDir, "commit", "-m", "local github data")

	// push conflicting change from separate clone
	tmpClone := filepath.Join(t.TempDir(), "conflict-clone")
	gitCmd(t, t.TempDir(), "clone", bareDir, tmpClone)
	gitCmd(t, tmpClone, "config", "user.name", "test")
	gitCmd(t, tmpClone, "config", "user.email", "test@test.com")
	require.NoError(t, os.MkdirAll(filepath.Join(tmpClone, "data", "github"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpClone, "data", "github", "prs.json"), []byte(`{"remote":true}`), 0o644))
	gitCmd(t, tmpClone, "add", "data/github/prs.json")
	gitCmd(t, tmpClone, "commit", "-m", "remote github data")
	gitCmd(t, tmpClone, "push", "origin", "HEAD")

	s := newPullTestScheduler(t, cloneDir)

	err := s.doPull(context.Background(), nil, true, true)
	assert.NoError(t, err, "doPull should auto-resolve conflict in data/github/")

	// verify no issues set
	issues := s.issues.GetIssues()
	assert.Empty(t, issues, "no issues should remain after auto-resolve")
}

func TestDoPull_ConflictInUnsafePath_ReportsIssueAndAborts(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}

	tmpDir := t.TempDir()
	cloneDir := filepath.Join(tmpDir, "clone")
	require.NoError(t, os.MkdirAll(cloneDir, 0755))
	setupGitRepo(t, cloneDir)
	bareDir := bareRepoPath(cloneDir)

	// local commit: modify SOUL.md (not under any safe auto-resolve prefix)
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "SOUL.md"), []byte("local team soul"), 0o644))
	gitCmd(t, cloneDir, "add", "SOUL.md")
	gitCmd(t, cloneDir, "commit", "-m", "local soul edit")

	// push conflicting change from separate clone
	tmpClone := filepath.Join(t.TempDir(), "conflict-clone")
	gitCmd(t, t.TempDir(), "clone", bareDir, tmpClone)
	gitCmd(t, tmpClone, "config", "user.name", "test")
	gitCmd(t, tmpClone, "config", "user.email", "test@test.com")
	require.NoError(t, os.WriteFile(filepath.Join(tmpClone, "SOUL.md"), []byte("remote team soul"), 0o644))
	gitCmd(t, tmpClone, "add", "SOUL.md")
	gitCmd(t, tmpClone, "commit", "-m", "remote soul edit")
	gitCmd(t, tmpClone, "push", "origin", "HEAD")

	s := newPullTestScheduler(t, cloneDir)

	err := s.doPull(context.Background(), nil, true, true)
	assert.Error(t, err, "doPull should fail when conflict is in unsafe path")

	// rebase should have been aborted (not left in progress)
	rebaseMerge := filepath.Join(cloneDir, ".git", "rebase-merge")
	_, statErr := os.Stat(rebaseMerge)
	assert.True(t, os.IsNotExist(statErr), "rebase should be aborted, not left in progress")

	// diverged+failed pull should report IssueTypeDiverged
	issues := s.issues.GetIssues()
	foundDiverged := false
	for _, issue := range issues {
		if issue.Type == IssueTypeDiverged {
			foundDiverged = true
			assert.Contains(t, issue.Summary, "diverged")
			assert.Contains(t, issue.Summary, "ox doctor --fix")
			break
		}
	}
	assert.True(t, foundDiverged, "should report IssueTypeDiverged after auto-resolve fails on unsafe path")
}

func TestDoPull_DivergedWithMixedConflicts_AbortsRebase(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}

	tmpDir := t.TempDir()
	cloneDir := filepath.Join(tmpDir, "clone")
	require.NoError(t, os.MkdirAll(cloneDir, 0755))
	setupGitRepo(t, cloneDir)
	bareDir := bareRepoPath(cloneDir)

	// local commit: modify both a safe and unsafe file
	require.NoError(t, os.MkdirAll(filepath.Join(cloneDir, "data", "github"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "data", "github", "prs.json"), []byte(`{"local":true}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "AGENTS.md"), []byte("local agents"), 0o644))
	gitCmd(t, cloneDir, "add", ".")
	gitCmd(t, cloneDir, "commit", "-m", "local mixed changes")

	// push conflicting changes to both files from remote
	tmpClone := filepath.Join(t.TempDir(), "conflict-clone")
	gitCmd(t, t.TempDir(), "clone", bareDir, tmpClone)
	gitCmd(t, tmpClone, "config", "user.name", "test")
	gitCmd(t, tmpClone, "config", "user.email", "test@test.com")
	require.NoError(t, os.MkdirAll(filepath.Join(tmpClone, "data", "github"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpClone, "data", "github", "prs.json"), []byte(`{"remote":true}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpClone, "AGENTS.md"), []byte("remote agents"), 0o644))
	gitCmd(t, tmpClone, "add", ".")
	gitCmd(t, tmpClone, "commit", "-m", "remote mixed changes")
	gitCmd(t, tmpClone, "push", "origin", "HEAD")

	s := newPullTestScheduler(t, cloneDir)

	err := s.doPull(context.Background(), nil, true, true)
	assert.Error(t, err, "doPull should fail when mixed safe/unsafe conflicts exist")

	// rebase must be aborted
	rebaseMerge := filepath.Join(cloneDir, ".git", "rebase-merge")
	_, statErr := os.Stat(rebaseMerge)
	assert.True(t, os.IsNotExist(statErr), "rebase should be aborted after mixed conflict failure")
}

// --- C. Successful pull ---
// Verifies the happy-path pull works end-to-end.
// Failure prevented: regression in basic pull functionality.

func TestDoPull_SuccessfulPull(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}

	tmpDir := t.TempDir()
	cloneDir := filepath.Join(tmpDir, "clone")
	require.NoError(t, os.MkdirAll(cloneDir, 0755))
	setupGitRepo(t, cloneDir)
	bareDir := bareRepoPath(cloneDir)

	// push a change from a separate clone
	pushFromSeparateClone(t, bareDir, "new-file.txt", "new content")

	s := newPullTestScheduler(t, cloneDir)

	err := s.doPull(context.Background(), nil, true, true)
	assert.NoError(t, err)

	// verify the file was pulled
	data, err := os.ReadFile(filepath.Join(cloneDir, "new-file.txt"))
	assert.NoError(t, err)
	assert.Equal(t, "new content", string(data))
}

// --- D. Team context pull conflicts ---
// Verifies pullTeamContext handles diverged branches and conflicts.
// Failure prevented: team context sync silently failing or leaving
// repos in broken rebase state.

func TestPullTeamContext_DivergedBranches_RebasesSuccessfully(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}

	tmpDir := t.TempDir()
	cloneDir := filepath.Join(tmpDir, "clone")
	require.NoError(t, os.MkdirAll(cloneDir, 0755))
	setupGitRepo(t, cloneDir)
	bareDir := bareRepoPath(cloneDir)

	// local commit (simulates daemon EnsureCheckoutGitignore or user edit)
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "local-doc.md"), []byte("local docs"), 0o644))
	gitCmd(t, cloneDir, "add", "local-doc.md")
	gitCmd(t, cloneDir, "commit", "-m", "local documentation")

	// push a different file from remote (simulates new discussion synced)
	pushFromSeparateClone(t, bareDir, "remote-discussion.md", "architecture discussion")

	s := newPullTestScheduler(t, cloneDir)

	err := s.pullTeamContext(context.Background(), cloneDir)
	assert.NoError(t, err, "pullTeamContext should rebase diverged branches")

	// both files should exist after rebase
	assert.FileExists(t, filepath.Join(cloneDir, "local-doc.md"))
	assert.FileExists(t, filepath.Join(cloneDir, "remote-discussion.md"))

	// no diverged or conflict issues
	for _, issue := range s.issues.GetIssues() {
		assert.NotEqual(t, IssueTypeDiverged, issue.Type)
		assert.NotEqual(t, IssueTypeMergeConflict, issue.Type)
	}
}

func TestPullTeamContext_ConflictReportsIssue(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}

	tmpDir := t.TempDir()
	cloneDir := filepath.Join(tmpDir, "clone")
	require.NoError(t, os.MkdirAll(cloneDir, 0755))
	setupGitRepo(t, cloneDir)
	bareDir := bareRepoPath(cloneDir)

	// local commit: edit SOUL.md
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "SOUL.md"), []byte("local soul"), 0o644))
	gitCmd(t, cloneDir, "add", "SOUL.md")
	gitCmd(t, cloneDir, "commit", "-m", "local soul")

	// push conflicting SOUL.md from remote
	tmpClone := filepath.Join(t.TempDir(), "conflict-clone")
	gitCmd(t, t.TempDir(), "clone", bareDir, tmpClone)
	gitCmd(t, tmpClone, "config", "user.name", "test")
	gitCmd(t, tmpClone, "config", "user.email", "test@test.com")
	require.NoError(t, os.WriteFile(filepath.Join(tmpClone, "SOUL.md"), []byte("remote soul"), 0o644))
	gitCmd(t, tmpClone, "add", "SOUL.md")
	gitCmd(t, tmpClone, "commit", "-m", "remote soul")
	gitCmd(t, tmpClone, "push", "origin", "HEAD")

	s := newPullTestScheduler(t, cloneDir)

	err := s.pullTeamContext(context.Background(), cloneDir)
	assert.Error(t, err, "pullTeamContext should fail on conflict (no auto-resolve prefixes for SOUL.md)")

	// team context uses fallback manifest which includes data/ auto-resolve,
	// but SOUL.md is NOT under data/ so it should fail with IssueTypeDiverged
	issues := s.issues.GetIssues()
	foundDiverged := false
	for _, issue := range issues {
		if issue.Type == IssueTypeDiverged {
			foundDiverged = true
			assert.Contains(t, issue.Summary, "diverged")
			assert.Contains(t, issue.Summary, "ox doctor --fix")
			break
		}
	}
	assert.True(t, foundDiverged, "should report IssueTypeDiverged for team context conflict")
}

// verifyAutoResolvePaths confirms the ledger default resolve rules include data/.
// This is a guard against accidental removal of the auto-resolve config that
// would break the ConflictInSafePath test above.
func TestLedgerDefaultResolveRules_IncludeDataDir(t *testing.T) {
	paths := manifest.AutoResolvePaths(ledger.DefaultResolveRules)
	found := false
	for _, p := range paths {
		if strings.HasPrefix("data/github/prs.json", p) {
			found = true
			break
		}
	}
	assert.True(t, found, "ledger default resolve rules should cover data/ prefix")
}
