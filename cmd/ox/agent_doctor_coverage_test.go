package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- findIncompleteSessions ---

func TestFindIncompleteSessions_NonexistentDir(t *testing.T) {
	t.Parallel()
	result := findIncompleteSessions("/nonexistent/path/sessions", "agent-1")
	assert.Empty(t, result)
}

func TestFindIncompleteSessions_EmptyDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	result := findIncompleteSessions(dir, "agent-1")
	assert.Empty(t, result)
}

func TestFindIncompleteSessions_SkipsLegacyDirs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// create legacy subdirs that should be skipped
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "raw"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "events"), 0o755))

	result := findIncompleteSessions(dir, "agent-1")
	assert.Empty(t, result, "legacy dirs 'raw' and 'events' should be skipped")
}

func TestFindIncompleteSessions_SkipsFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// regular files (not dirs) should be skipped
	require.NoError(t, os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("hi"), 0o644))

	result := findIncompleteSessions(dir, "agent-1")
	assert.Empty(t, result)
}

func TestFindIncompleteSessions_IdentifiesIncomplete(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// create a session dir with only raw.jsonl (missing other artifacts)
	sessionDir := filepath.Join(dir, "2026-03-15-user-abc1")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), []byte(`{}`), 0o644))

	result := findIncompleteSessions(dir, "agent-1")
	require.Len(t, result, 1)
	assert.Equal(t, "2026-03-15-user-abc1", result[0].SessionID)
	assert.Equal(t, sessionDir, result[0].Path)
	assert.NotEmpty(t, result[0].Missing)
	assert.NotEmpty(t, result[0].FinalizeCommands)
}

func TestFindIncompleteSessions_SkipsCompleteSession(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	sessionDir := filepath.Join(dir, "complete-session")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	for _, f := range []string{"raw.jsonl", "session.html", "summary.md", "session.md", "summary.json"} {
		require.NoError(t, os.WriteFile(filepath.Join(sessionDir, f), []byte("content"), 0o644))
	}

	result := findIncompleteSessions(dir, "agent-1")
	assert.Empty(t, result, "complete sessions should not appear in results")
}

func TestFindIncompleteSessions_SkipsSessionWithoutRaw(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// session dir with HTML but no raw.jsonl = invalid session, not incomplete
	sessionDir := filepath.Join(dir, "no-raw-session")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "session.html"), []byte("<html>"), 0o644))

	result := findIncompleteSessions(dir, "agent-1")
	assert.Empty(t, result, "session without raw.jsonl should be treated as invalid, not incomplete")
}

// --- checkLedgerGitStatus ---
// uses real git, so we create a temp git repo

func TestCheckLedgerGitStatus_NotAGitRepo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	staged, commitNeeded, pushNeeded := checkLedgerGitStatus(dir)
	assert.Equal(t, 0, staged)
	assert.False(t, commitNeeded)
	assert.False(t, pushNeeded)
}

func TestCheckLedgerGitStatus_CleanRepo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// init a git repo with an initial commit
	initDoctorTestGitRepo(t, dir)

	staged, commitNeeded, pushNeeded := checkLedgerGitStatus(dir)
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

	staged, commitNeeded, _ := checkLedgerGitStatus(dir)
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

	_, commitNeeded, _ := checkLedgerGitStatus(dir)
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
