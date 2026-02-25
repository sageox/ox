package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createBareAndClone creates a bare git repo (simulating remote) and a clone of it.
// Returns (barePath, clonePath). Both are inside t.TempDir() and auto-cleaned.
func createBareAndClone(t *testing.T) (string, string) {
	t.Helper()

	base := t.TempDir()
	barePath := filepath.Join(base, "remote.git")
	clonePath := filepath.Join(base, "local")

	// create bare repo
	runGit(t, base, "init", "--bare", barePath)

	// clone it
	runGit(t, base, "clone", barePath, clonePath)

	// configure git identity in clone (isolated to temp dir)
	runGit(t, clonePath, "config", "user.email", "test@example.com")
	runGit(t, clonePath, "config", "user.name", "Test")

	// create initial commit so main branch exists
	initFile := filepath.Join(clonePath, ".gitkeep")
	require.NoError(t, os.WriteFile(initFile, []byte(""), 0644))
	runGit(t, clonePath, "add", ".gitkeep")
	runGit(t, clonePath, "commit", "--no-verify", "-m", "initial")
	runGit(t, clonePath, "push")

	return barePath, clonePath
}

// runGit runs a git command in the given directory and fails the test on error.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s failed in %s: %s", strings.Join(args, " "), dir, string(out))
	return strings.TrimSpace(string(out))
}

// writeSessionFiles creates a session directory with meta.json and .gitignore
// in the ledger, ready for commitAndPushLedger.
func writeSessionFiles(t *testing.T, ledgerPath, sessionName string) string {
	t.Helper()

	sessionsDir := filepath.Join(ledgerPath, "sessions")
	sessionDir := filepath.Join(sessionsDir, sessionName)
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	// write meta.json (the file that gets git-tracked)
	meta := fmt.Sprintf(`{"session_name":"%s","agent_id":"OxTest","created_at":"2026-01-01T00:00:00Z"}`, sessionName)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "meta.json"), []byte(meta), 0644))

	// ensure .gitignore exists
	require.NoError(t, ensureSessionsGitignore(sessionsDir))

	return sessionDir
}

// cloneBare creates a second clone of the bare repo (simulating another team member).
func cloneBare(t *testing.T, barePath string) string {
	t.Helper()
	base := t.TempDir()
	clonePath := filepath.Join(base, "other")
	runGit(t, base, "clone", barePath, clonePath)
	runGit(t, clonePath, "config", "user.email", "other@example.com")
	runGit(t, clonePath, "config", "user.name", "Other")
	return clonePath
}

// commitCount returns the number of commits on the current branch.
func commitCount(t *testing.T, dir string) int {
	t.Helper()
	out := runGit(t, dir, "rev-list", "--count", "HEAD")
	var count int
	_, err := fmt.Sscanf(out, "%d", &count)
	require.NoError(t, err)
	return count
}

// isolatePushEnv changes CWD to the clone and sets SAGEOX_ENDPOINT to a fake
// value so pushLedger's RefreshRemoteCredentials finds no real credentials
// and doesn't rewrite the local file:// remote URL.
func isolatePushEnv(t *testing.T, clonePath string) {
	t.Helper()
	oldWd, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	require.NoError(t, os.Chdir(clonePath))

	t.Setenv("SAGEOX_ENDPOINT", "https://test-only-no-creds.invalid")
}

func TestCommitAndPushLedger_Success(t *testing.T) {
	barePath, clonePath := createBareAndClone(t)
	isolatePushEnv(t, clonePath)

	sessionName := "2026-01-01T00-00-testuser-OxAbc1"
	writeSessionFiles(t, clonePath, sessionName)

	err := commitAndPushLedger(clonePath, sessionName)
	require.NoError(t, err)

	// verify the commit landed on the remote by cloning fresh and checking
	verifyClone := cloneBare(t, barePath)
	metaPath := filepath.Join(verifyClone, "sessions", sessionName, "meta.json")
	_, statErr := os.Stat(metaPath)
	assert.NoError(t, statErr, "meta.json should exist on remote after push")

	// verify commit message format
	msg := runGit(t, verifyClone, "log", "-1", "--format=%s")
	assert.Equal(t, fmt.Sprintf("session: %s", sessionName), msg)
}

func TestCommitAndPushLedger_NothingToCommit(t *testing.T) {
	_, clonePath := createBareAndClone(t)

	// git add will fail since the session files don't exist
	err := commitAndPushLedger(clonePath, "nonexistent-session")
	assert.Error(t, err, "should error when session files don't exist")
}

func TestPushLedger_ConflictWithDivergedRemote(t *testing.T) {
	barePath, clonePath := createBareAndClone(t)
	isolatePushEnv(t, clonePath)

	// local: write and commit a session (but don't push yet)
	sessionName := "2026-01-01T00-00-testuser-OxDiv1"
	writeSessionFiles(t, clonePath, sessionName)
	runGit(t, clonePath, "add", filepath.Join(clonePath, "sessions"))
	runGit(t, clonePath, "commit", "--no-verify", "-m", "session: "+sessionName)

	// remote: push a different commit from another clone to create divergence
	otherClone := cloneBare(t, barePath)
	otherFile := filepath.Join(otherClone, "other-file.txt")
	require.NoError(t, os.WriteFile(otherFile, []byte("diverge"), 0644))
	runGit(t, otherClone, "add", "other-file.txt")
	runGit(t, otherClone, "commit", "--no-verify", "-m", "concurrent commit")
	runGit(t, otherClone, "push")

	// now push from local — should handle the non-fast-forward with rebase retry
	err := pushLedger(context.Background(), clonePath)
	require.NoError(t, err, "pushLedger should succeed after rebase retry on diverged remote")

	// verify both commits exist on remote
	verifyClone := cloneBare(t, barePath)
	count := commitCount(t, verifyClone)
	// initial + concurrent + session = 3
	assert.Equal(t, 3, count, "remote should have all 3 commits after rebase")

	// verify our session file made it
	metaPath := filepath.Join(verifyClone, "sessions", sessionName, "meta.json")
	_, statErr := os.Stat(metaPath)
	assert.NoError(t, statErr, "session meta.json should be on remote after rebase+push")
}

func TestPushLedger_AuthFailureSurfacesClearError(t *testing.T) {
	_, clonePath := createBareAndClone(t)
	isolatePushEnv(t, clonePath)

	// write and commit a file so there's something to push
	sessionName := "2026-01-01T00-00-testuser-OxAuth"
	writeSessionFiles(t, clonePath, sessionName)
	runGit(t, clonePath, "add", filepath.Join(clonePath, "sessions"))
	runGit(t, clonePath, "commit", "--no-verify", "-m", "session: "+sessionName)

	// point the remote to a URL that will produce "could not read Username"
	// which is one of the permanent failure patterns in pushLedger
	runGit(t, clonePath, "remote", "set-url", "origin", "https://github.com/nonexistent-org-12345/nonexistent-repo-67890.git")

	// disable git credential helpers so git fails immediately with "could not read Username"
	runGit(t, clonePath, "config", "credential.helper", "")
	// also set GIT_TERMINAL_PROMPT=0 to prevent interactive prompt
	t.Setenv("GIT_TERMINAL_PROMPT", "0")

	err := pushLedger(context.Background(), clonePath)
	require.Error(t, err, "should fail on auth error")
	assert.Contains(t, err.Error(), "not retryable",
		"auth errors should be flagged as not retryable")

	// verify the local commit is preserved (not lost due to the failed push)
	log := runGit(t, clonePath, "log", "--oneline")
	assert.Contains(t, log, sessionName,
		"local commit should still exist after failed push")
}

func TestPushLedger_RetryExhaustion(t *testing.T) {
	_, clonePath := createBareAndClone(t)
	isolatePushEnv(t, clonePath)

	// write and commit
	sessionName := "2026-01-01T00-00-testuser-OxRetry"
	writeSessionFiles(t, clonePath, sessionName)
	runGit(t, clonePath, "add", filepath.Join(clonePath, "sessions"))
	runGit(t, clonePath, "commit", "--no-verify", "-m", "session: "+sessionName)

	// point to a nonexistent local path — git push fails with a transient-looking
	// error ("does not appear to be a git repository") that doesn't match permanent patterns
	runGit(t, clonePath, "remote", "set-url", "origin", "/nonexistent/bare/repo.git")

	err := pushLedger(context.Background(), clonePath)
	require.Error(t, err, "should fail after retry exhaustion")
	assert.Contains(t, err.Error(), "failed after 3 attempts",
		"error should indicate retry exhaustion")

	// the critical invariant: local commit must survive even though push failed
	log := runGit(t, clonePath, "log", "--oneline")
	assert.Contains(t, log, sessionName,
		"local commit MUST be preserved after push retry exhaustion")
}

func TestConcurrentSessionUploads(t *testing.T) {
	barePath, clonePath := createBareAndClone(t)
	isolatePushEnv(t, clonePath)

	// sequential uploads to same repo — the real-world pattern is sequential
	// per repo (CLI serializes), but we verify concurrent writes to different
	// session dirs don't corrupt each other
	const numSessions = 3
	errs := make([]error, numSessions)

	for i := 0; i < numSessions; i++ {
		sessionName := fmt.Sprintf("2026-01-01T00-%02d-user%d-Ox%04d", i, i, i)
		writeSessionFiles(t, clonePath, sessionName)
		errs[i] = commitAndPushLedger(clonePath, sessionName)
	}

	// all sequential uploads should succeed
	for i, err := range errs {
		assert.NoError(t, err, "session %d should succeed", i)
	}

	// verify all sessions reached remote
	verifyClone := cloneBare(t, barePath)
	for i := 0; i < numSessions; i++ {
		sessionName := fmt.Sprintf("2026-01-01T00-%02d-user%d-Ox%04d", i, i, i)
		metaPath := filepath.Join(verifyClone, "sessions", sessionName, "meta.json")
		_, statErr := os.Stat(metaPath)
		assert.NoError(t, statErr, "session %s should exist on remote", sessionName)
	}

	// verify total commit count: initial + 3 sessions = 4
	count := commitCount(t, verifyClone)
	assert.Equal(t, 4, count, "should have initial + 3 session commits")
}

func TestConcurrentSessionUploads_Parallel(t *testing.T) {
	barePath, clonePath := createBareAndClone(t)
	isolatePushEnv(t, clonePath)

	// true concurrent uploads — some will fail due to git index.lock contention
	// but the ones that succeed must not corrupt the repo
	const numSessions = 4
	var wg sync.WaitGroup
	errs := make([]error, numSessions)

	for i := 0; i < numSessions; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sessionName := fmt.Sprintf("2026-01-01T01-%02d-user%d-Ox%04d", idx, idx, idx)
			writeSessionFiles(t, clonePath, sessionName)
			errs[idx] = commitAndPushLedger(clonePath, sessionName)
		}(i)
	}
	wg.Wait()

	var succeeded int
	for _, err := range errs {
		if err == nil {
			succeeded++
		}
	}

	// at least one should succeed; git lock contention may fail others
	assert.Greater(t, succeeded, 0, "at least one concurrent session should succeed")

	// verify remote integrity: successful sessions must be present
	verifyClone := cloneBare(t, barePath)
	for i := 0; i < numSessions; i++ {
		sessionName := fmt.Sprintf("2026-01-01T01-%02d-user%d-Ox%04d", i, i, i)
		metaPath := filepath.Join(verifyClone, "sessions", sessionName, "meta.json")
		if errs[i] == nil {
			_, statErr := os.Stat(metaPath)
			assert.NoError(t, statErr, "session %s succeeded but missing from remote", sessionName)
		}
	}

	// verify no corruption: git fsck on the bare repo
	cmd := exec.Command("git", "-C", barePath, "fsck", "--no-dangling")
	out, err := cmd.CombinedOutput()
	assert.NoError(t, err, "git fsck should pass (no corruption): %s", string(out))
}

func TestEnsureSessionsGitignore(t *testing.T) {
	t.Run("creates gitignore when missing", func(t *testing.T) {
		dir := t.TempDir()
		sessionsDir := filepath.Join(dir, "sessions")
		require.NoError(t, os.MkdirAll(sessionsDir, 0755))

		err := ensureSessionsGitignore(sessionsDir)
		require.NoError(t, err)

		content, err := os.ReadFile(filepath.Join(sessionsDir, ".gitignore"))
		require.NoError(t, err)
		assert.Contains(t, string(content), "*.jsonl", "should exclude jsonl files")
		assert.Contains(t, string(content), "!meta.json", "should include meta.json")
	})

	t.Run("idempotent when gitignore exists", func(t *testing.T) {
		dir := t.TempDir()
		sessionsDir := filepath.Join(dir, "sessions")
		require.NoError(t, os.MkdirAll(sessionsDir, 0755))

		require.NoError(t, ensureSessionsGitignore(sessionsDir))
		require.NoError(t, ensureSessionsGitignore(sessionsDir))
	})
}

func TestCommitAndPushLedgerWithExtras_IncludesSummary(t *testing.T) {
	barePath, clonePath := createBareAndClone(t)
	isolatePushEnv(t, clonePath)

	sessionName := "2026-01-01T00-00-testuser-OxExtr"

	// write session files plus summary.json
	sessionDir := writeSessionFiles(t, clonePath, sessionName)
	summaryJSON := `{"key_decisions":["used table-driven tests"]}`
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "summary.json"), []byte(summaryJSON), 0644))

	err := commitAndPushLedgerWithExtras(clonePath, sessionName, true)
	require.NoError(t, err)

	// verify summary.json reached remote
	verifyClone := cloneBare(t, barePath)
	summaryPath := filepath.Join(verifyClone, "sessions", sessionName, "summary.json")
	data, readErr := os.ReadFile(summaryPath)
	require.NoError(t, readErr, "summary.json should exist on remote")
	assert.Contains(t, string(data), "key_decisions")
}

func TestCommitAndPushLedgerWithExtras_WithoutSummary(t *testing.T) {
	barePath, clonePath := createBareAndClone(t)
	isolatePushEnv(t, clonePath)

	sessionName := "2026-01-01T00-00-testuser-OxNoSm"
	writeSessionFiles(t, clonePath, sessionName)

	// includeSummary=false should still commit meta.json and .gitignore
	err := commitAndPushLedgerWithExtras(clonePath, sessionName, false)
	require.NoError(t, err)

	verifyClone := cloneBare(t, barePath)
	metaPath := filepath.Join(verifyClone, "sessions", sessionName, "meta.json")
	_, statErr := os.Stat(metaPath)
	assert.NoError(t, statErr, "meta.json should exist on remote")

	// summary.json should NOT be present
	summaryPath := filepath.Join(verifyClone, "sessions", sessionName, "summary.json")
	_, statErr = os.Stat(summaryPath)
	assert.True(t, os.IsNotExist(statErr), "summary.json should not exist on remote when not included")
}

func TestFindGitRootFrom(t *testing.T) {
	t.Run("finds root from nested dir", func(t *testing.T) {
		dir := t.TempDir()
		runGit(t, dir, "init")

		nested := filepath.Join(dir, "a", "b", "c")
		require.NoError(t, os.MkdirAll(nested, 0755))

		root, err := findGitRootFrom(nested)
		require.NoError(t, err)
		// resolve symlinks for macOS /private/var/folders vs /var/folders
		expected, _ := filepath.EvalSymlinks(dir)
		got, _ := filepath.EvalSymlinks(root)
		assert.Equal(t, expected, got)
	})

	t.Run("errors when no git root", func(t *testing.T) {
		dir := t.TempDir()
		_, err := findGitRootFrom(dir)
		assert.Error(t, err, "should error when no .git exists")
	})
}
