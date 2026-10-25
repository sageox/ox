package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newLedgerTestRepo makes a real git repo with a base commit and repo-local
// identity, so the production commit plumbing (gitutil.CommitLedgerSnapshot,
// which reads repo/global config rather than the test's scrubbed env) has an
// author/committer.
func newLedgerTestRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	mustRunGit(t, repo, "init", "--initial-branch=main")
	mustRunGit(t, repo, "config", "user.name", "Test")
	mustRunGit(t, repo, "config", "user.email", "test@example.com")
	mustRunGit(t, repo, "config", "commit.gpgsign", "false")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0644))
	mustRunGit(t, repo, "add", "base.txt")
	mustRunGit(t, repo, "commit", "-m", "base")
	return repo
}

const conflictBlob = "{\n<<<<<<< ours\n  \"n\": 1,\n=======\n  \"n\": 2,\n>>>>>>> theirs\n}\n"

// TestCommitLedgerSnapshot_RefusesMarkerAlreadyStaged proves the snapshot scan
// catches staged marker content (the non-racy case): a resolved-but-marker blob
// already in the index is refused, and nothing is committed.
func TestCommitLedgerSnapshot_RefusesMarkerAlreadyStaged(t *testing.T) {
	skipIntegration(t)
	repo := newLedgerTestRepo(t)
	ctx := context.Background()

	require.NoError(t, os.WriteFile(filepath.Join(repo, "meta.json"), []byte(conflictBlob), 0644))
	mustRunGit(t, repo, "add", "meta.json")

	committed, err := commitLedgerSnapshot(ctx, repo, "session: X")
	require.Error(t, err)
	assert.False(t, committed)
	assert.Contains(t, err.Error(), "unresolved conflict")

	log, gerr := runIsolatedGit(t, repo, "log", "--oneline")
	require.NoError(t, gerr)
	assert.Equal(t, 1, countLines(log), "no commit should have been created")
}

// A marker-free meta.json can still be corrupt application data. The immutable
// snapshot path must enforce the same JSON contract as automatic daemon writers.
func TestCommitLedgerSnapshot_RefusesInvalidSessionMetadata(t *testing.T) {
	skipIntegration(t)
	repo := newLedgerTestRepo(t)
	path := filepath.Join(repo, "sessions", "example", "meta.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(`{"title":`), 0o644))
	mustRunGit(t, repo, "add", "--sparse", path)

	committed, err := commitLedgerSnapshot(context.Background(), repo, "session: X")
	require.ErrorContains(t, err, "invalid JSON")
	assert.False(t, committed)

	log, gerr := runIsolatedGit(t, repo, "log", "--oneline")
	require.NoError(t, gerr)
	assert.Equal(t, 1, countLines(log), "invalid metadata must not create a commit")
}

// TestCommitLedgerSnapshot_RefusesUnmergedIndex proves an unmerged (UU) index —
// a live conflict — is refused: `git write-tree` cannot snapshot unmerged entries,
// so the snapshot itself fails closed.
func TestCommitLedgerSnapshot_RefusesUnmergedIndex(t *testing.T) {
	skipIntegration(t)
	repo := newLedgerTestRepo(t)
	ctx := context.Background()

	mustRunGit(t, repo, "checkout", "-b", "feature")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "base.txt"), []byte("feature\n"), 0644))
	mustRunGit(t, repo, "commit", "-am", "feature change")
	mustRunGit(t, repo, "checkout", "main")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "base.txt"), []byte("mainline\n"), 0644))
	mustRunGit(t, repo, "commit", "-am", "main change")

	_, merr := runIsolatedGit(t, repo, "merge", "feature")
	require.Error(t, merr, "merge must conflict to produce the UU wedge")

	committed, err := commitLedgerSnapshot(ctx, repo, "session: X")
	require.Error(t, err)
	assert.False(t, committed)
}

// TestCommitLedgerSnapshot_NothingToCommit proves idempotency: a clean index
// identical to HEAD reports committed=false with no error and no new commit.
func TestCommitLedgerSnapshot_NothingToCommit(t *testing.T) {
	skipIntegration(t)
	repo := newLedgerTestRepo(t)

	committed, err := commitLedgerSnapshot(context.Background(), repo, "session: X")
	require.NoError(t, err)
	assert.False(t, committed)

	log, _ := runIsolatedGit(t, repo, "log", "--oneline")
	assert.Equal(t, 1, countLines(log))
}

// TestCommitLedgerSnapshot_CommitsCleanIndex proves the happy path: a clean
// staged change is committed with the given subject and advances the branch.
func TestCommitLedgerSnapshot_CommitsCleanIndex(t *testing.T) {
	skipIntegration(t)
	repo := newLedgerTestRepo(t)

	require.NoError(t, os.WriteFile(filepath.Join(repo, "new.txt"), []byte("hi\n"), 0644))
	mustRunGit(t, repo, "add", "new.txt")

	committed, err := commitLedgerSnapshot(context.Background(), repo, "session: abc")
	require.NoError(t, err)
	assert.True(t, committed)

	subj, _ := runIsolatedGit(t, repo, "log", "-1", "--format=%s")
	assert.Equal(t, "session: abc", subj)
	files, _ := runIsolatedGit(t, repo, "ls-tree", "-r", "--name-only", "HEAD")
	assert.Contains(t, files, "new.txt")
}

// TestCommitLedgerSnapshot_CleanRenameCommits proves a clean staged rename is not
// false-flagged as a conflict (diff-tree emits delete+add, both clean).
func TestCommitLedgerSnapshot_CleanRenameCommits(t *testing.T) {
	skipIntegration(t)
	repo := newLedgerTestRepo(t)

	mustRunGit(t, repo, "mv", "base.txt", "renamed.txt")

	committed, err := commitLedgerSnapshot(context.Background(), repo, "rename")
	require.NoError(t, err, "a clean rename must not be flagged as a conflict")
	assert.True(t, committed)

	files, _ := runIsolatedGit(t, repo, "ls-tree", "-r", "--name-only", "HEAD")
	assert.Contains(t, files, "renamed.txt")
	assert.NotContains(t, files, "base.txt")
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := 1
	for _, r := range s {
		if r == '\n' {
			n++
		}
	}
	return n
}
