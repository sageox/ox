//go:build !short

package main

import (
	"os"
	"path/filepath"
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
	pattern := filepath.Join(verifyClone, "data", "github", "*", "*", "*", "pr", "800.json")
	matches, _ := filepath.Glob(pattern)
	require.Len(t, matches, 1, "PR #800 should exist on remote")
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
	assert.Contains(t, result.message, "rebase failed",
		"failure message should mention rebase")

	// repo should be clean (rebase aborted)
	assert.False(t, isRebaseInProgressCheck(t, machineB),
		"rebase should be aborted, not left in progress")
}

// TestFixLedgerBranchBehind_GitHubConflictAutoResolved verifies the behind
// path (pull only, no push) also handles GitHub data conflicts.
func TestFixLedgerBranchBehind_GitHubConflictAutoResolved(t *testing.T) {
	barePath, machineA := createBareAndClone(t)
	machineB := cloneBare(t, barePath)

	// machine B: commit PR #900 locally
	prB := makePR(900, "PR from B", "bob", "open")
	prB.Body = "body B"
	writeGitHubPRFile(t, machineB, prB)
	commitGitHubData(t, machineB, "github: sync from B")

	// machine A: commit conflicting PR #900 and push
	prA := makePR(900, "PR from A", "bob", "open")
	prA.Body = "body A"
	writeGitHubPRFile(t, machineA, prA)
	commitGitHubData(t, machineA, "github: sync from A")
	runGit(t, machineA, "push")

	// fetch so machine B knows about the remote
	runGit(t, machineB, "fetch")

	result := fixLedgerBranchBehind(machineB, 1)

	assert.True(t, result.passed,
		"doctor should auto-resolve github data conflict during pull: %s — %s", result.message, result.detail)
	assert.Contains(t, result.message, "auto-resolved",
		"success message should mention auto-resolution")
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
	pattern := filepath.Join(verifyClone, "data", "github", "*", "*", "*", "pr", "950.json")
	matches, _ := filepath.Glob(pattern)
	assert.Len(t, matches, 1, "PR #950 should exist on remote")
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
