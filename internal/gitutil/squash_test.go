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

func TestSquashUnpushed_RequiresUpstream(t *testing.T) {
	repo := t.TempDir()
	run(t, repo, "git", "init", "--quiet", "-b", "main")
	run(t, repo, "git", "config", "user.email", "test@test.local")
	run(t, repo, "git", "config", "user.name", "Test")
	addCommit(t, repo, "a.txt", "a", "a")
	addCommit(t, repo, "b.txt", "b", "b")

	err := SquashUnpushed(context.Background(), repo, "squash")
	require.ErrorContains(t, err, "no upstream tracking ref")
	assert.Equal(t, "2", strings.TrimSpace(gitOut(t, repo, "rev-list", "--count", "HEAD")), "history is untouched")
}

func TestSquashUnpushed_SingleCommitIsLeftAlone(t *testing.T) {
	repo, _ := initBareRemoteRepo(t)
	addCommit(t, repo, "only.txt", "x", "only")
	before := gitOut(t, repo, "rev-parse", "HEAD")

	require.NoError(t, SquashUnpushed(context.Background(), repo, "squash"))
	assert.Equal(t, before, gitOut(t, repo, "rev-parse", "HEAD"))
	assert.Empty(t, strings.TrimSpace(gitOut(t, repo, "for-each-ref", "refs/ox-backup/")), "no backup ref for a no-op")
}

// A branch that is behind its upstream must be pulled, never squashed: a soft
// reset to the upstream tip would commit the index back over the coworker's
// commits and silently revert them.
func TestSquashUnpushed_RefusesDivergedBranch(t *testing.T) {
	repo, bare := initBareRemoteRepo(t)
	second := filepath.Join(t.TempDir(), "second")
	run(t, "", "git", "clone", "--quiet", bare, second)
	run(t, second, "git", "config", "user.email", "test@test.local")
	run(t, second, "git", "config", "user.name", "Test")
	addCommit(t, second, "coworker.txt", "theirs", "coworker")
	run(t, second, "git", "push", "--quiet")
	addCommit(t, repo, "mine1.txt", "1", "mine 1")
	addCommit(t, repo, "mine2.txt", "2", "mine 2")
	run(t, repo, "git", "fetch", "--quiet")
	before := gitOut(t, repo, "rev-parse", "HEAD")

	err := SquashUnpushed(context.Background(), repo, "squash")
	require.ErrorContains(t, err, "branch diverged")
	assert.Equal(t, before, gitOut(t, repo, "rev-parse", "HEAD"))
}

// When the collapsed snapshot is refused by Ledger validation, HEAD goes back
// to the original tip so the unpushed commits are not left collapsed into the
// index, and the backup ref still points at that tip.
func TestSquashUnpushed_RestoresHeadWhenCommitIsRefused(t *testing.T) {
	repo, _ := initBareRemoteRepo(t)
	addCommit(t, repo, "fine.txt", "ok", "fine")
	sessionDir := filepath.Join(repo, "sessions", "2026-01-01T00-00-test-Ox0000")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	// plain content where the Ledger requires an LFS pointer: the snapshot validation refuses it
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), []byte("{\"plain\":true}\n"), 0o644))
	run(t, repo, "git", "add", "sessions")
	run(t, repo, "git", "commit", "--quiet", "--no-verify", "-m", "plain artifact")
	original := strings.TrimSpace(gitOut(t, repo, "rev-parse", "HEAD"))

	err := SquashUnpushed(context.Background(), repo, "squash")
	require.ErrorContains(t, err, "validate squash or commit")
	assert.Equal(t, original, strings.TrimSpace(gitOut(t, repo, "rev-parse", "HEAD")), "HEAD is restored")
	backup := strings.TrimSpace(gitOut(t, repo, "for-each-ref", "--format=%(objectname)", "refs/ox-backup/"))
	assert.Equal(t, original, backup, "the backup ref keeps the original tip")
}

// Every git step the squash depends on reports its failure and leaves HEAD
// where it was: a squash that silently did not happen would keep the push
// wedged with nothing in the log.
func TestSquashUnpushed_ReportsGitFailuresAndKeepsHead(t *testing.T) {
	cases := []struct{ mode, want string }{
		{"revlistfail", "count unpushed commits"},
		{"updatereffail", "keep pre-squash tip"},
		{"resetfail", "reset --soft"},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			repo, _ := initBareRemoteRepo(t)
			addCommit(t, repo, "one.txt", "1", "one")
			addCommit(t, repo, "two.txt", "2", "two")
			before := gitOut(t, repo, "rev-parse", "HEAD")
			installFakeGit(t, tc.mode)

			err := SquashUnpushed(context.Background(), repo, "squash")
			require.ErrorContains(t, err, tc.want)
			assert.Equal(t, before, gitOut(t, repo, "rev-parse", "HEAD"))
		})
	}
}
