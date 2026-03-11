package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/ledger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustParseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// writeGitHubPRFile writes a PR JSON file to the ledger's data/github/ directory.
// Returns the file path relative to ledgerPath.
func writeGitHubPRFile(t *testing.T, ledgerPath string, pr *ledger.PRFile) string {
	t.Helper()
	require.NoError(t, ledger.WriteGitHubPR(ledgerPath, pr))
	dir := ledger.DateDir(ledgerPath, pr.CreatedAt, "pr")
	return filepath.Join(dir, fmt.Sprintf("%d.json", pr.Number))
}

// writeGitHubIssueFile writes an issue JSON file to the ledger's data/github/ directory.
func writeGitHubIssueFile(t *testing.T, ledgerPath string, issue *ledger.IssueFile) string {
	t.Helper()
	require.NoError(t, ledger.WriteGitHubIssue(ledgerPath, issue))
	dir := ledger.DateDir(ledgerPath, issue.CreatedAt, "issue")
	return filepath.Join(dir, fmt.Sprintf("%d.json", issue.Number))
}

// commitGitHubData stages and commits GitHub data files in the ledger clone.
func commitGitHubData(t *testing.T, ledgerPath string, msg string) {
	t.Helper()
	dataDir := ledger.GitHubDataDir(ledgerPath)
	runGit(t, ledgerPath, "add", "--all", dataDir)
	runGit(t, ledgerPath, "commit", "--no-verify", "-m", msg)
}

// makePR creates a PRFile with sensible defaults for testing.
func makePR(number int, title, author, state string) *ledger.PRFile {
	return &ledger.PRFile{
		Number:    number,
		Title:     title,
		Author:    author,
		State:     state,
		CreatedAt: mustParseTime("2026-03-10T10:00:00Z"),
		UpdatedAt: mustParseTime("2026-03-10T10:00:00Z"),
		URL:       fmt.Sprintf("https://github.com/org/repo/pull/%d", number),
	}
}

// makeIssue creates an IssueFile with sensible defaults for testing.
func makeIssue(number int, title, author, state string) *ledger.IssueFile {
	return &ledger.IssueFile{
		Number:    number,
		Title:     title,
		Author:    author,
		State:     state,
		CreatedAt: mustParseTime("2026-03-10T10:00:00Z"),
		UpdatedAt: mustParseTime("2026-03-10T10:00:00Z"),
		URL:       fmt.Sprintf("https://github.com/org/repo/issues/%d", number),
	}
}

// TestMultiMachine_DifferentPRFiles tests two machines each syncing different PRs
// and pushing independently. This is the most common real-world scenario.
func TestMultiMachine_DifferentPRFiles(t *testing.T) {
	barePath, machineA := createBareAndClone(t)
	machineB := cloneBare(t, barePath)
	isolatePushEnv(t, machineA)

	// machine A: sync PR #100
	writeGitHubPRFile(t, machineA, makePR(100, "Add feature X", "alice", "merged"))
	commitGitHubData(t, machineA, "github: sync 1 PRs from org/repo")

	// machine B: sync PR #101 (independently, before pulling A's changes)
	writeGitHubPRFile(t, machineB, makePR(101, "Fix bug Y", "bob", "open"))
	commitGitHubData(t, machineB, "github: sync 1 PRs from org/repo")

	// machine A pushes first — should succeed
	err := pushLedger(context.Background(), machineA)
	require.NoError(t, err, "machine A push should succeed (no conflict)")

	// machine B pushes second — should succeed after rebase
	// (needs its own CWD isolation for pushLedger's findGitRoot)
	oldWd, _ := os.Getwd()
	require.NoError(t, os.Chdir(machineB))
	defer func() { _ = os.Chdir(oldWd) }()
	t.Setenv("SAGEOX_ENDPOINT", "https://test-only-no-creds.invalid")

	err = pushLedger(context.Background(), machineB)
	require.NoError(t, err, "machine B push should succeed after rebase (different files)")

	// verify both PRs exist on remote
	verifyClone := cloneBare(t, barePath)
	for _, num := range []int{100, 101} {
		pattern := filepath.Join(verifyClone, "data", "github", "*", "*", "*", "pr", fmt.Sprintf("%d.json", num))
		matches, _ := filepath.Glob(pattern)
		assert.Len(t, matches, 1, "PR #%d should exist on remote", num)
	}
}

// TestMultiMachine_SamePRSameContent tests two machines syncing the same PR
// with identical content. The rebase should produce a clean merge since the
// file content is identical.
func TestMultiMachine_SamePRSameContent(t *testing.T) {
	barePath, machineA := createBareAndClone(t)
	machineB := cloneBare(t, barePath)
	isolatePushEnv(t, machineA)

	pr := makePR(200, "Same PR from both", "alice", "open")

	// both machines write identical content for PR #200
	writeGitHubPRFile(t, machineA, pr)
	commitGitHubData(t, machineA, "github: sync 1 PRs from org/repo")

	writeGitHubPRFile(t, machineB, pr)
	commitGitHubData(t, machineB, "github: sync 1 PRs from org/repo")

	// machine A pushes first
	err := pushLedger(context.Background(), machineA)
	require.NoError(t, err)

	// machine B pushes — rebase should succeed because content is identical
	oldWd, _ := os.Getwd()
	require.NoError(t, os.Chdir(machineB))
	defer func() { _ = os.Chdir(oldWd) }()
	t.Setenv("SAGEOX_ENDPOINT", "https://test-only-no-creds.invalid")

	err = pushLedger(context.Background(), machineB)
	require.NoError(t, err, "same file, same content should rebase cleanly")

	// verify only one copy of PR #200 exists
	verifyClone := cloneBare(t, barePath)
	pattern := filepath.Join(verifyClone, "data", "github", "*", "*", "*", "pr", "200.json")
	matches, _ := filepath.Glob(pattern)
	assert.Len(t, matches, 1, "exactly one copy of PR #200 should exist")

	// verify content is correct
	data, err := os.ReadFile(matches[0])
	require.NoError(t, err)
	var got ledger.PRFile
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, 200, got.Number)
	assert.Equal(t, "Same PR from both", got.Title)
}

// TestMultiMachine_SamePRDifferentContent tests two machines syncing the same PR
// but with different content (e.g., different comment sets due to timing).
// The rebase conflict is auto-resolved with accept-theirs (last-write-wins)
// because data/github/ files are derived from the API and re-fetched on next sync.
func TestMultiMachine_SamePRDifferentContent(t *testing.T) {
	barePath, machineA := createBareAndClone(t)
	machineB := cloneBare(t, barePath)
	isolatePushEnv(t, machineA)

	// machine A: PR #300 with no comments
	prA := makePR(300, "Conflicting PR", "alice", "open")
	prA.Body = "body from machine A"
	writeGitHubPRFile(t, machineA, prA)
	commitGitHubData(t, machineA, "github: sync 1 PRs from org/repo")

	// machine B: PR #300 with a comment (different content)
	prB := makePR(300, "Conflicting PR", "alice", "open")
	prB.Body = "body from machine B"
	prB.Comments = []ledger.PRComment{
		{Author: "bob", Body: "looks good", CreatedAt: mustParseTime("2026-03-10T11:00:00Z")},
	}
	writeGitHubPRFile(t, machineB, prB)
	commitGitHubData(t, machineB, "github: sync 1 PRs from org/repo")

	// machine A pushes first
	err := pushLedger(context.Background(), machineA)
	require.NoError(t, err)

	// machine B pushes — conflict auto-resolved with accept-theirs
	oldWd, _ := os.Getwd()
	require.NoError(t, os.Chdir(machineB))
	defer func() { _ = os.Chdir(oldWd) }()
	t.Setenv("SAGEOX_ENDPOINT", "https://test-only-no-creds.invalid")

	err = pushLedger(context.Background(), machineB)
	require.NoError(t, err, "conflict in data/github/ should be auto-resolved with accept-theirs")

	// verify machine B's repo is clean
	assert.False(t, isRebaseInProgressCheck(t, machineB),
		"no rebase should be in progress")

	// during rebase, "ours" = the branch being rebased onto (remote/machine A),
	// "theirs" = the commit being replayed (local/machine B).
	// accept-theirs means machine B's version wins (the later writer).
	// this is fine: both fetched from the same API, next sync re-fetches anyway.
	verifyClone := cloneBare(t, barePath)
	pattern := filepath.Join(verifyClone, "data", "github", "*", "*", "*", "pr", "300.json")
	matches, _ := filepath.Glob(pattern)
	require.Len(t, matches, 1)
	data, readErr := os.ReadFile(matches[0])
	require.NoError(t, readErr)
	assert.Contains(t, string(data), "body from machine B",
		"remote should have machine B's version (accept-theirs keeps the replayed commit)")
}

// TestMultiMachine_NonGitHubConflictStillFails verifies that conflicts in files
// OUTSIDE data/github/ are NOT auto-resolved — only GitHub data files get
// last-write-wins treatment.
func TestMultiMachine_NonGitHubConflictStillFails(t *testing.T) {
	barePath, machineA := createBareAndClone(t)
	machineB := cloneBare(t, barePath)
	isolatePushEnv(t, machineA)

	// both machines modify the same non-github file (at repo root, not in data/github/)
	require.NoError(t, os.WriteFile(filepath.Join(machineA, "conflict.txt"), []byte("version A"), 0644))
	runGit(t, machineA, "add", "conflict.txt")
	runGit(t, machineA, "commit", "--no-verify", "-m", "local change A")

	require.NoError(t, os.WriteFile(filepath.Join(machineB, "conflict.txt"), []byte("version B"), 0644))
	runGit(t, machineB, "add", "conflict.txt")
	runGit(t, machineB, "commit", "--no-verify", "-m", "local change B")

	// A pushes first
	err := pushLedger(context.Background(), machineA)
	require.NoError(t, err)

	// B pushes — conflict outside data/github/ should NOT be auto-resolved
	oldWd, _ := os.Getwd()
	require.NoError(t, os.Chdir(machineB))
	defer func() { _ = os.Chdir(oldWd) }()
	t.Setenv("SAGEOX_ENDPOINT", "https://test-only-no-creds.invalid")

	err = pushLedger(context.Background(), machineB)
	require.Error(t, err, "non-github conflict should not be auto-resolved")
	assert.Contains(t, err.Error(), "rebase")

	// repo should be clean (rebase aborted)
	assert.False(t, isRebaseInProgressCheck(t, machineB))
}

// TestMultiMachine_MixedPRsAndIssues tests two machines where one syncs PRs
// and the other syncs issues. Different directories — no conflict.
func TestMultiMachine_MixedPRsAndIssues(t *testing.T) {
	barePath, machineA := createBareAndClone(t)
	machineB := cloneBare(t, barePath)
	isolatePushEnv(t, machineA)

	// machine A: sync PRs
	writeGitHubPRFile(t, machineA, makePR(400, "PR from machine A", "alice", "merged"))
	commitGitHubData(t, machineA, "github: sync 1 PRs from org/repo")

	// machine B: sync issues
	writeGitHubIssueFile(t, machineB, makeIssue(50, "Issue from machine B", "bob", "open"))
	commitGitHubData(t, machineB, "github: sync 1 issues from org/repo")

	// both push
	err := pushLedger(context.Background(), machineA)
	require.NoError(t, err)

	oldWd, _ := os.Getwd()
	require.NoError(t, os.Chdir(machineB))
	defer func() { _ = os.Chdir(oldWd) }()
	t.Setenv("SAGEOX_ENDPOINT", "https://test-only-no-creds.invalid")

	err = pushLedger(context.Background(), machineB)
	require.NoError(t, err, "PR + issue to different dirs should not conflict")

	// verify both exist on remote
	verifyClone := cloneBare(t, barePath)
	prPattern := filepath.Join(verifyClone, "data", "github", "*", "*", "*", "pr", "400.json")
	issuePattern := filepath.Join(verifyClone, "data", "github", "*", "*", "*", "issue", "50.json")
	prMatches, _ := filepath.Glob(prPattern)
	issueMatches, _ := filepath.Glob(issuePattern)
	assert.Len(t, prMatches, 1, "PR #400 should exist on remote")
	assert.Len(t, issueMatches, 1, "Issue #50 should exist on remote")
}

// TestMultiMachine_ThreeMachinesSequentialPush tests three machines all syncing
// different PRs and pushing sequentially. Each subsequent push requires a rebase.
func TestMultiMachine_ThreeMachinesSequentialPush(t *testing.T) {
	barePath, machineA := createBareAndClone(t)
	machineB := cloneBare(t, barePath)
	machineC := cloneBare(t, barePath)

	// all three machines independently sync different PRs
	writeGitHubPRFile(t, machineA, makePR(500, "PR from A", "alice", "merged"))
	commitGitHubData(t, machineA, "github: sync 1 PRs from org/repo")

	writeGitHubPRFile(t, machineB, makePR(501, "PR from B", "bob", "open"))
	commitGitHubData(t, machineB, "github: sync 1 PRs from org/repo")

	writeGitHubPRFile(t, machineC, makePR(502, "PR from C", "carol", "closed"))
	commitGitHubData(t, machineC, "github: sync 1 PRs from org/repo")

	// push sequentially — each subsequent push needs a rebase
	for i, machine := range []string{machineA, machineB, machineC} {
		oldWd, _ := os.Getwd()
		require.NoError(t, os.Chdir(machine))
		t.Setenv("SAGEOX_ENDPOINT", "https://test-only-no-creds.invalid")

		err := pushLedger(context.Background(), machine)
		require.NoError(t, err, "machine %d push should succeed", i)

		_ = os.Chdir(oldWd)
	}

	// verify all three PRs on remote
	verifyClone := cloneBare(t, barePath)
	for _, num := range []int{500, 501, 502} {
		pattern := filepath.Join(verifyClone, "data", "github", "*", "*", "*", "pr", fmt.Sprintf("%d.json", num))
		matches, _ := filepath.Glob(pattern)
		assert.Len(t, matches, 1, "PR #%d should exist on remote", num)
	}

	// verify total commits: initial + 3 syncs = 4
	count := commitCount(t, verifyClone)
	assert.Equal(t, 4, count, "should have initial + 3 machine commits")
}

// TestMultiMachine_ConcurrentPushRace tests rapid sequential pushes from
// multiple independent clones, simulating machines pushing near-simultaneously.
// True goroutine concurrency isn't testable here because pushLedger depends on
// process-global CWD for findGitRoot(). In production, each machine is a
// separate process with its own CWD.
func TestMultiMachine_ConcurrentPushRace(t *testing.T) {
	barePath, _ := createBareAndClone(t)

	const numMachines = 4
	machines := make([]string, numMachines)
	for i := 0; i < numMachines; i++ {
		machines[i] = cloneBare(t, barePath)
		writeGitHubPRFile(t, machines[i], makePR(600+i, fmt.Sprintf("PR from machine %d", i), "user", "open"))
		commitGitHubData(t, machines[i], fmt.Sprintf("github: sync 1 PRs from machine %d", i))
	}

	// push from each machine sequentially (rapid-fire, no delay between)
	// each push after the first will encounter non-fast-forward and need rebase
	for i, machine := range machines {
		oldWd, _ := os.Getwd()
		require.NoError(t, os.Chdir(machine))
		t.Setenv("SAGEOX_ENDPOINT", "https://test-only-no-creds.invalid")

		err := pushLedger(context.Background(), machine)
		require.NoError(t, err, "machine %d push should succeed after rebase", i)

		_ = os.Chdir(oldWd)
	}

	// verify remote integrity
	cmd := exec.Command("git", "-C", barePath, "fsck", "--no-dangling")
	out, err := cmd.CombinedOutput()
	assert.NoError(t, err, "git fsck should pass: %s", string(out))

	// verify all PRs exist on remote
	verifyClone := cloneBare(t, barePath)
	for i := 0; i < numMachines; i++ {
		pattern := filepath.Join(verifyClone, "data", "github", "*", "*", "*", "pr", fmt.Sprintf("%d.json", 600+i))
		matches, _ := filepath.Glob(pattern)
		assert.Len(t, matches, 1, "PR #%d should exist on remote", 600+i)
	}

	// verify commit count: initial + 4 machines = 5
	count := commitCount(t, verifyClone)
	assert.Equal(t, 5, count, "should have initial + 4 machine commits")
}

// TestMultiMachine_CommitAndPushGitHubData_Integration tests the full
// commitAndPushGitHubData path with diverged remote.
func TestMultiMachine_CommitAndPushGitHubData_Integration(t *testing.T) {
	barePath, machineA := createBareAndClone(t)
	machineB := cloneBare(t, barePath)
	isolatePushEnv(t, machineA)

	// machine B pushes a PR first
	writeGitHubPRFile(t, machineB, makePR(700, "PR from B", "bob", "open"))
	runGit(t, machineB, "add", "--all", ledger.GitHubDataDir(machineB))
	runGit(t, machineB, "commit", "--no-verify", "-m", "github: sync 1 PRs from org/repo")
	runGit(t, machineB, "push")

	// machine A uses commitAndPushGitHubData (the real function)
	writeGitHubPRFile(t, machineA, makePR(701, "PR from A", "alice", "merged"))
	result := &githubSyncResult{prTotal: 1}
	err := commitAndPushGitHubData(machineA, "org", "repo", result)
	require.NoError(t, err, "commitAndPushGitHubData should handle diverged remote")

	// verify both PRs on remote
	verifyClone := cloneBare(t, barePath)
	for _, num := range []int{700, 701} {
		pattern := filepath.Join(verifyClone, "data", "github", "*", "*", "*", "pr", fmt.Sprintf("%d.json", num))
		matches, _ := filepath.Glob(pattern)
		assert.Len(t, matches, 1, "PR #%d should exist on remote", num)
	}
}

// isRebaseInProgressCheck checks if a rebase is in progress without using gitutil.
func isRebaseInProgressCheck(t *testing.T, dir string) bool {
	t.Helper()
	for _, marker := range []string{"rebase-merge", "rebase-apply"} {
		gitDir := filepath.Join(dir, ".git", marker)
		if _, err := os.Stat(gitDir); err == nil {
			return true
		}
	}
	return false
}
