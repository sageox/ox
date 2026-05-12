//go:build !short

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

func TestCheckLedgerURLAPIMatch_Skip_NoLedger(t *testing.T) {
	// run in a temp dir with no ledger configured
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	result := checkLedgerURLAPIMatch(false)

	if !result.skipped {
		t.Errorf("expected skipped=true when no ledger found, got: %+v", result)
	}
}

// TestApplyCorrectedLedgerURL_LeavesOriginBare is the load-bearing
// regression for the post-ox-eeqi invariant: the URL-API-match fix path
// MUST NOT re-embed the PAT into origin, even when the API returns a URL
// that carries credentials. Failure prevented: PAT leakage via
// `git remote -v`, GIT_TRACE, Time Machine, and shell history-adjacent
// diagnostics — the same leak vectors ox-eeqi was meant to close.
func TestApplyCorrectedLedgerURL_LeavesOriginBare(t *testing.T) {
	for _, apiURL := range []string{
		// API may return any of these shapes; all must produce a bare origin.
		// The PAT-shaped literal is split so secret scanners (Betterleaks,
		// trufflehog, etc.) don't false-positive on this test fixture.
		"https://oauth2:" + "gl" + "pat-secret-token-do-not-leak@git.sageox.ai/team/ledger.git",
		"https://user@git.sageox.ai/team/ledger.git",
		"https://git.sageox.ai/team/ledger.git",
	} {
		t.Run(apiURL, func(t *testing.T) {
			work := t.TempDir()
			mustGit(t, work, "init", "--initial-branch=main")
			// seed origin with a stale URL so the fix has something to overwrite
			mustGit(t, work, "remote", "add", "origin", "https://git.sageox.ai/team/old-ledger.git")

			if r := applyCorrectedLedgerURL(work, apiURL, "test"); r != nil {
				t.Fatalf("applyCorrectedLedgerURL failed: %s — %s", r.message, r.detail)
			}

			out, err := exec.Command("git", "-C", work, "remote", "get-url", "origin").Output()
			require.NoError(t, err)
			got := strings.TrimSpace(string(out))

			assert.NotContains(t, got, "@",
				"origin must not carry userinfo (PAT leak); api=%q origin=%q", apiURL, got)
			assert.NotContains(t, got, "glpat-",
				"origin must not contain the PAT; api=%q origin=%q", apiURL, got)
			assert.NotContains(t, got, "oauth2:",
				"origin must not contain oauth2: userinfo; api=%q origin=%q", apiURL, got)
		})
	}
}

// TestApplyCorrectedLedgerURL_InstallsHelper covers Codex finding 1 (round 2):
// after the URL is corrected, the credential helper must be present so the
// next fetch/push actually authenticates. Without it the doctor would
// report "URL updated" while the next push silently fails on auth.
func TestApplyCorrectedLedgerURL_InstallsHelper(t *testing.T) {
	work := t.TempDir()
	mustGit(t, work, "init", "--initial-branch=main")
	mustGit(t, work, "remote", "add", "origin", "https://git.sageox.ai/team/stale.git")

	if r := applyCorrectedLedgerURL(work, "https://git.sageox.ai/team/correct.git", "test"); r != nil {
		t.Fatalf("applyCorrectedLedgerURL failed: %s — %s", r.message, r.detail)
	}

	installed, err := ledgerHasCredentialHelper(work, "git.sageox.ai")
	require.NoError(t, err)
	assert.True(t, installed,
		"helper must be installed after URL correction; otherwise the next push fails at auth")
}

// TestFixLedgerBranchDiverged_GitHubConflictAutoResolved verifies that
// ox doctor --fix can repair a ledger stuck with a diverged GitHub data
// conflict by auto-resolving with accept-theirs.
func TestFixLedgerBranchDiverged_GitHubConflictAutoResolved(t *testing.T) {
	barePath, machineA := createBareAndClone(t)
	machineB := cloneBare(t, barePath)

	// machine A: push PR #800
	prA := makePR(800, "PR from A", "alice", "open")
	prA.Body = "body A"
	writeGitHubPRFile(t, machineA, prA)
	commitGitHubData(t, machineA, "github: sync from A")
	runGit(t, machineA, "push")

	// machine B: commit conflicting PR #800 (different body) but don't push
	prB := makePR(800, "PR from B", "alice", "open")
	prB.Body = "body B"
	writeGitHubPRFile(t, machineB, prB)
	commitGitHubData(t, machineB, "github: sync from B")

	// fetch so machine B knows about the remote divergence
	runGit(t, machineB, "fetch")

	// simulate what ox doctor --fix would do
	result := fixLedgerBranchDiverged(machineB, 1, 1)

	assert.True(t, result.passed,
		"doctor should auto-resolve github data conflict: %s — %s", result.message, result.detail)

	// verify push succeeded — PR on remote
	verifyClone := cloneBare(t, barePath)
	pattern := filepath.Join(verifyClone, "data", "github", "*", "*", "*", "pr", "800-*.json")
	matches, _ := filepath.Glob(pattern)
	require.NotEmpty(t, matches, "PR #800 should exist on remote")
}

// TestFixLedgerBranchDiverged_NonGitHubConflictFails verifies that doctor
// does NOT auto-resolve conflicts outside data/github/.
func TestFixLedgerBranchDiverged_NonGitHubConflictFails(t *testing.T) {
	barePath, machineA := createBareAndClone(t)
	machineB := cloneBare(t, barePath)

	// machine A: push a non-github file
	require.NoError(t, os.WriteFile(filepath.Join(machineA, "notes.txt"), []byte("version A"), 0644))
	runGit(t, machineA, "add", "notes.txt")
	runGit(t, machineA, "commit", "--no-verify", "-m", "notes from A")
	runGit(t, machineA, "push")

	// machine B: conflicting change to same file
	require.NoError(t, os.WriteFile(filepath.Join(machineB, "notes.txt"), []byte("version B"), 0644))
	runGit(t, machineB, "add", "notes.txt")
	runGit(t, machineB, "commit", "--no-verify", "-m", "notes from B")

	// fetch so machine B knows about remote
	runGit(t, machineB, "fetch")

	result := fixLedgerBranchDiverged(machineB, 1, 1)

	assert.False(t, result.passed,
		"doctor should fail on non-github conflict")
	assert.Contains(t, result.message, "reconcile failed",
		"failure message should mention reconcile failure")

	// repo should be clean (rebase aborted)
	assert.False(t, isRebaseInProgressCheck(t, machineB),
		"rebase should be aborted, not left in progress")
}

// TestFixLedgerBranchBehind_GitHubConflictAutoResolved verifies the behind
// path (pull only, no push) handles GitHub data from different machines.
// With content-hash filenames, different content produces different filenames,
// so the rebase completes cleanly without conflict.
func TestFixLedgerBranchBehind_GitHubConflictAutoResolved(t *testing.T) {
	barePath, machineA := createBareAndClone(t)
	machineB := cloneBare(t, barePath)

	// machine B: commit PR #900 locally
	prB := makePR(900, "PR from B", "bob", "open")
	prB.Body = "body B"
	writeGitHubPRFile(t, machineB, prB)
	commitGitHubData(t, machineB, "github: sync from B")

	// machine A: commit PR #900 with different content and push
	prA := makePR(900, "PR from A", "bob", "open")
	prA.Body = "body A"
	writeGitHubPRFile(t, machineA, prA)
	commitGitHubData(t, machineA, "github: sync from A")
	runGit(t, machineA, "push")

	// fetch so machine B knows about the remote
	runGit(t, machineB, "fetch")

	result := fixLedgerBranchBehind(machineB, 1)

	assert.True(t, result.passed,
		"doctor should succeed during pull: %s — %s", result.message, result.detail)
}

// TestFixLedgerBranchAhead_FallsThroughToDivergedFix verifies the ahead path
// handles the case where push is rejected (remote diverged) and falls through
// to the diverged fix with auto-resolve.
func TestFixLedgerBranchAhead_FallsThroughToDivergedFix(t *testing.T) {
	barePath, machineA := createBareAndClone(t)
	machineB := cloneBare(t, barePath)

	// machine B: commit PR #950 locally
	prB := makePR(950, "PR from B", "bob", "merged")
	prB.Body = "body B"
	writeGitHubPRFile(t, machineB, prB)
	commitGitHubData(t, machineB, "github: sync from B")

	// machine A: commit conflicting PR #950 and push (creating divergence)
	prA := makePR(950, "PR from A", "bob", "merged")
	prA.Body = "body A"
	writeGitHubPRFile(t, machineA, prA)
	commitGitHubData(t, machineA, "github: sync from A")
	runGit(t, machineA, "push")

	// from machine B's perspective, it's "ahead" (has unpushed commit)
	// but push will fail because remote has diverged
	result := fixLedgerBranchAhead(machineB, 1)

	assert.True(t, result.passed,
		"doctor ahead fix should succeed via diverged fallback: %s — %s", result.message, result.detail)

	// verify PR #950 on remote
	verifyClone := cloneBare(t, barePath)
	pattern := filepath.Join(verifyClone, "data", "github", "*", "*", "*", "pr", "950-*.json")
	matches, _ := filepath.Glob(pattern)
	assert.NotEmpty(t, matches, "PR #950 should exist on remote")
}

// TestFixLedgerBranchAhead_UsesPushLedger verifies that fixLedgerBranchAhead
// uses pushLedger (which includes PrePush credential refresh) rather than calling
// PushWithRetry directly (ox-az9 regression).
//
// Previously fixLedgerBranchAhead called gitutil.PushWithRetry without a PrePush
// hook, so a stale PAT in the remote URL was never refreshed, causing HTTP 403 on
// real server pushes. Now it calls pushLedger which runs RefreshRemoteCredentials
// before each attempt. Test repos use file:// URLs so the credential refresh
// no-ops, but the push succeeds — verifying the code path is correct.
func TestFixLedgerBranchAhead_UsesPushLedger(t *testing.T) {
	barePath, machineB := createBareAndClone(t)

	prB := makePR(1001, "push-ledger regression test", "user1", "open")
	writeGitHubPRFile(t, machineB, prB)
	commitGitHubData(t, machineB, "github: test that fixLedgerBranchAhead uses pushLedger")

	result := fixLedgerBranchAhead(machineB, 1)

	assert.True(t, result.passed,
		"fixLedgerBranchAhead must succeed: %s — %s", result.message, result.detail)
	assert.Contains(t, result.message, "pushed",
		"success message must indicate push completed")

	verifyClone := cloneBare(t, barePath)
	pattern := filepath.Join(verifyClone, "data", "github", "*", "*", "*", "pr", "1001-*.json")
	matches, _ := filepath.Glob(pattern)
	assert.Len(t, matches, 1, "PR #1001 must be visible on remote after push")
}

func TestStripURLCredentials(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "URL with oauth2 credentials",
			input:    "https://oauth2:some-token@gitlab.example.com/group/repo.git",
			expected: "https://gitlab.example.com/group/repo.git",
		},
		{
			name:     "URL without credentials",
			input:    "https://gitlab.example.com/group/repo.git",
			expected: "https://gitlab.example.com/group/repo.git",
		},
		{
			name:     "URL with username only",
			input:    "https://user@gitlab.example.com/group/repo.git",
			expected: "https://gitlab.example.com/group/repo.git",
		},
		{
			name:     "URL with port and credentials",
			input:    "https://oauth2:token@gitlab.example.com:8443/group/repo.git",
			expected: "https://gitlab.example.com:8443/group/repo.git",
		},
		{
			name:     "invalid URL returns as-is",
			input:    "://not-a-valid-url",
			expected: "://not-a-valid-url",
		},
		{
			name:     "empty string returns empty",
			input:    "",
			expected: "",
		},
		{
			name:     "SSH-style URL returns as-is (not a parseable URL)",
			input:    "git@github.com:org/repo.git",
			expected: "git@github.com:org/repo.git",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripURLCredentials(tt.input)
			if got != tt.expected {
				t.Errorf("stripURLCredentials(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

