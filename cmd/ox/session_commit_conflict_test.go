package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newSessionCommitProject makes a project repo with one committed session and chdirs into it.
func newSessionCommitProject(t *testing.T) (string, string) {
	t.Helper()
	skipIntegration(t)
	project := t.TempDir()
	t.Setenv("HOME", project)
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	mustRunGit(t, project, "init", "--initial-branch=main")
	mustRunGit(t, project, "config", "user.name", "Test")
	mustRunGit(t, project, "config", "user.email", "test@example.com")
	mustRunGit(t, project, "config", "commit.gpgsign", "false")
	require.NoError(t, os.WriteFile(filepath.Join(project, "README.md"), []byte("repo\n"), 0o644))
	meta := filepath.Join(project, ".sageox", "sessions", "2026-09-24T10-00-alice-OxAAAA", "meta.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(meta), 0o755))
	require.NoError(t, os.WriteFile(meta, []byte("{\n  \"title\": \"base\"\n}\n"), 0o644))
	mustRunGit(t, project, "add", "-A")
	mustRunGit(t, project, "commit", "-m", "base")
	t.Chdir(project)
	return project, meta
}

// TestRunSessionCommit_RefusesUnmergedSessionFile covers `ox session commit` on a stash-pop conflict.
// Without it, `git add` marks the conflict resolved and the commit publishes the markers.
func TestRunSessionCommit_RefusesUnmergedSessionFile(t *testing.T) {
	project, meta := newSessionCommitProject(t)
	require.NoError(t, os.WriteFile(meta, []byte("{\n  \"title\": \"local\"\n}\n"), 0o644))
	mustRunGit(t, project, "stash", "push", "-m", "autostash")
	require.NoError(t, os.WriteFile(meta, []byte("{\n  \"title\": \"upstream\"\n}\n"), 0o644))
	mustRunGit(t, project, "commit", "-am", "upstream")
	out, err := runIsolatedGit(t, project, "stash", "pop")
	require.Error(t, err, "stash pop must conflict: %s", out)
	before, _ := runIsolatedGit(t, project, "log", "--oneline")

	err = runSessionCommit(sessionCommitCmd, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unresolved conflict")
	after, _ := runIsolatedGit(t, project, "log", "--oneline")
	assert.Equal(t, before, after, "no commit may be created")
	unmerged, _ := runIsolatedGit(t, project, "ls-files", "-u")
	assert.NotEmpty(t, unmerged, "the conflict must stay unmerged")
}

// TestRunSessionCommit_RefusesStagedConflictMarkers covers a conflict someone already `git add`ed by hand.
// Without it, the stage-0 file passes the unmerged check and its markers are committed.
func TestRunSessionCommit_RefusesStagedConflictMarkers(t *testing.T) {
	project, meta := newSessionCommitProject(t)
	require.NoError(t, os.WriteFile(meta, []byte("{\n<<<<<<< Updated upstream\n  \"title\": \"upstream\"\n=======\n  \"title\": \"local\"\n>>>>>>> Stashed changes\n}\n"), 0o644))
	mustRunGit(t, project, "add", ".sageox/sessions")
	fresh := filepath.Join(project, ".sageox", "sessions", "2026-09-24T12-00-carol-OxCCCC", "raw.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(fresh), 0o755))
	require.NoError(t, os.WriteFile(fresh, []byte("{}\n"), 0o644))
	before, _ := runIsolatedGit(t, project, "log", "--oneline")

	err := runSessionCommit(sessionCommitCmd, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "conflict markers")
	after, _ := runIsolatedGit(t, project, "log", "--oneline")
	assert.Equal(t, before, after, "no commit may be created")
}

// TestRunSessionCommit_CommitsCleanSessionOnly guards the normal path and the sessions scope.
// Without it, the guard could block healthy sessions or sweep the user's other staged files into the commit.
func TestRunSessionCommit_CommitsCleanSessionOnly(t *testing.T) {
	project, _ := newSessionCommitProject(t)
	other := filepath.Join(project, ".sageox", "sessions", "2026-09-24T11-00-bob-OxBBBB", "meta.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(other), 0o755))
	require.NoError(t, os.WriteFile(other, []byte(`{"title":"b"}`+"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(project, "wip.go"), []byte("package wip\n"), 0o644))
	mustRunGit(t, project, "add", "wip.go")

	require.NoError(t, runSessionCommit(sessionCommitCmd, nil))

	subject, _ := runIsolatedGit(t, project, "log", "-1", "--format=%s")
	assert.Contains(t, subject, "Update sessions")
	status, _ := runIsolatedGit(t, project, "status", "--porcelain=v1")
	assert.Equal(t, "A  wip.go", status, "session committed, unrelated staged file left alone")
}
