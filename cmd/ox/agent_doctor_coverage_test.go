package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- checkLedgerGitStatus ---
// uses real git, so we create a temp git repo

func TestCheckLedgerGitStatus_NotAGitRepo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	staged, commitNeeded, pushNeeded, err := checkLedgerGitStatus(dir)
	require.Error(t, err)
	assert.Equal(t, 0, staged)
	assert.False(t, commitNeeded)
	assert.False(t, pushNeeded)
}

func TestCheckLedgerGitStatus_CleanRepo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// init a git repo with an initial commit
	initDoctorTestGitRepo(t, dir)

	staged, commitNeeded, pushNeeded, err := checkLedgerGitStatus(dir)
	require.NoError(t, err)
	assert.Equal(t, 0, staged)
	assert.False(t, commitNeeded)
	assert.False(t, pushNeeded)
}

func TestCheckLedgerGitStatus_StagedFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	initDoctorTestGitRepo(t, dir)

	// add a staged file
	require.NoError(t, os.WriteFile(filepath.Join(dir, "newfile.txt"), []byte("content"), 0o644))
	runDoctorGitInDir(t, dir, "add", "newfile.txt")

	staged, commitNeeded, _, err := checkLedgerGitStatus(dir)
	require.NoError(t, err)
	assert.Equal(t, 1, staged)
	assert.True(t, commitNeeded)
}

func TestCheckLedgerGitStatus_UnstagedChanges(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	initDoctorTestGitRepo(t, dir)

	// create a tracked file, commit, then modify (without staging the change)
	fpath := filepath.Join(dir, "tracked.txt")
	require.NoError(t, os.WriteFile(fpath, []byte("v1"), 0o644))
	runDoctorGitInDir(t, dir, "add", "tracked.txt")
	runDoctorGitInDir(t, dir, "commit", "-m", "add tracked")
	require.NoError(t, os.WriteFile(fpath, []byte("v2"), 0o644))

	_, commitNeeded, _, err := checkLedgerGitStatus(dir)
	require.NoError(t, err)
	// modified tracked file means there's work to commit
	assert.True(t, commitNeeded, "modified tracked file means commit needed")
}

// --- buildNextSteps with combined state ---

func TestBuildNextSteps_AllFlags(t *testing.T) {
	t.Parallel()
	output := &AgentDoctorOutput{
		IncompleteSessions: []IncompleteSessionInfo{
			{
				SessionID: "s1",
				Missing:   []string{"summary", "summary_json"},
			},
			{
				SessionID: "s2",
				Missing:   []string{"summary"},
			},
		},
		CommitNeeded: true,
		PushNeeded:   true,
	}

	steps := buildNextSteps(output)

	// should have: 2 from s1 + 1 from s2 + commit + push = 5
	assert.Len(t, steps, 5)

	// verify ordering: incomplete sessions first, then git ops
	lastSessionIdx := -1
	commitIdx := -1
	pushIdx := -1
	for i, step := range steps {
		if step == "ox session commit" {
			commitIdx = i
		}
		if step == "git push (in ledger)" {
			pushIdx = i
		}
		if containsString([]string{
			"Generate summary for session s1",
			"Generate HTML for session s1",
			"Generate summary JSON for session s1",
			"Generate summary for session s2",
		}, step) {
			lastSessionIdx = i
		}
	}

	assert.Greater(t, commitIdx, lastSessionIdx, "commit should come after session steps")
	assert.Greater(t, pushIdx, commitIdx, "push should come after commit")
}

func TestBuildNextSteps_SessionMdNotIncluded(t *testing.T) {
	t.Parallel()
	// session_md is not handled by buildNextSteps
	output := &AgentDoctorOutput{
		IncompleteSessions: []IncompleteSessionInfo{
			{SessionID: "s1", Missing: []string{"session_md"}},
		},
	}
	steps := buildNextSteps(output)
	assert.Empty(t, steps, "session_md is not a step buildNextSteps generates")
}

// --- helper ---

func initDoctorTestGitRepo(t *testing.T, dir string) {
	t.Helper()
	runDoctorGitInDir(t, dir, "init")
	runDoctorGitInDir(t, dir, "config", "user.email", "test@example.com")
	runDoctorGitInDir(t, dir, "config", "user.name", "Test")
	// initial commit so repo is not bare
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitkeep"), []byte{}, 0o644))
	runDoctorGitInDir(t, dir, "add", ".gitkeep")
	runDoctorGitInDir(t, dir, "commit", "-m", "init")
}

func runDoctorGitInDir(t *testing.T, dir string, args ...string) {
	t.Helper()
	output, err := runAgentDoctorGitCommand(dir, args...)
	if err != nil {
		t.Fatalf("git %v failed: %v\noutput: %s", args, err, output)
	}
}
