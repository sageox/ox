package gitutil

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// strandAutostash reproduces what a pull killed after "Created autostash"
// leaves behind: the working-tree change lives only in a stash commit that
// MERGE_AUTOSTASH points at, and the tree itself is clean.
func strandAutostash(t *testing.T, repo, file, content string) string {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(repo, file), []byte(content), 0o644))
	sha := strings.TrimSpace(gitOut(t, repo, "stash", "create"))
	require.NotEmpty(t, sha)
	run(t, repo, "git", "update-ref", "MERGE_AUTOSTASH", sha)
	run(t, repo, "git", "checkout", "--", file)
	return sha
}

// A pull killed by its deadline after creating its autostash leaves
// MERGE_AUTOSTASH behind, and every later `pull --autostash` then fails with
// "cannot lock ref 'MERGE_AUTOSTASH': reference already exists" (monorepo
// Ledger, 2026-10-08 11:29-11:30). The resolver restores the stranded change
// and clears the ref, and the next pull works.
func TestResolveAutostashConflicts_RestoresALeftoverAutostash(t *testing.T) {
	repo, bare := initBareRemoteRepo(t)
	addCommit(t, repo, "notes.txt", "v1\n", "notes")
	run(t, repo, "git", "push", "--quiet")
	strandAutostash(t, repo, "notes.txt", "v1\nlocal edit\n")
	require.Equal(t, "v1\n", string(mustRead(t, filepath.Join(repo, "notes.txt"))), "precondition: the edit lives only in the stash")

	// a coworker moves the remote so the next pull has work to do
	second := filepath.Join(t.TempDir(), "second")
	run(t, "", "git", "clone", "--quiet", bare, second)
	run(t, second, "git", "config", "user.email", "test@test.local")
	run(t, second, "git", "config", "user.name", "Test")
	addCommit(t, second, "other.txt", "theirs", "coworker")
	run(t, second, "git", "push", "--quiet")

	_, err := ResolveAutostashConflicts(context.Background(), repo, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "v1\nlocal edit\n", string(mustRead(t, filepath.Join(repo, "notes.txt"))), "the stranded edit is back in the tree")
	_, refErr := RunGit(context.Background(), repo, "rev-parse", "-q", "--verify", "MERGE_AUTOSTASH")
	assert.Error(t, refErr, "the stale ref is gone")

	run(t, repo, "git", "pull", "--rebase", "--autostash", "--quiet")
	assert.FileExists(t, filepath.Join(repo, "other.txt"))
	assert.Equal(t, "v1\nlocal edit\n", string(mustRead(t, filepath.Join(repo, "notes.txt"))), "the edit survives the pull's own autostash")
}

// When the stranded change no longer applies cleanly, nothing is thrown away:
// it is kept in the stash list, the ref is cleared so pulls can run again, and
// the conflict is reported the way any other unresolved conflict is.
func TestResolveAutostashConflicts_LeftoverThatConflictsIsKeptInStashList(t *testing.T) {
	repo, _ := initBareRemoteRepo(t)
	addCommit(t, repo, "notes.txt", "v1\n", "notes")
	sha := strandAutostash(t, repo, "notes.txt", "mine\n")
	addCommit(t, repo, "notes.txt", "theirs\n", "conflicting head")

	_, err := ResolveAutostashConflicts(context.Background(), repo, nil, nil)
	require.ErrorContains(t, err, "requires manual resolution")
	_, refErr := RunGit(context.Background(), repo, "rev-parse", "-q", "--verify", "MERGE_AUTOSTASH")
	assert.Error(t, refErr, "the stale ref is gone")
	assert.Contains(t, gitOut(t, repo, "stash", "list", "--format=%H"), sha, "the change is kept in the stash list")
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return b
}
