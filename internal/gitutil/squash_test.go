package gitutil

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// A branch that is behind its upstream must be pulled, never squashed: HEAD's
// tree committed onto the upstream tip would land over the coworker's commits
// and silently revert them.
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

// When Ledger validation refuses the collapsed commit, HEAD stays at the
// original tip, and the backup ref points at that tip too.
func TestSquashUnpushed_KeepsHeadWhenCommitIsRefused(t *testing.T) {
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
	assert.Equal(t, original, strings.TrimSpace(gitOut(t, repo, "rev-parse", "HEAD")), "HEAD stays put")
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

// The snapshot commit validates every blob the collapsed delta touches, one
// `git cat-file` each. A flat budget killed that validation on a 6,282-file
// delta ("git cat-file: signal: terminated"), rolled the squash back, and left
// the Ledger wedged. The budget must grow with the delta and stay bounded.
func TestSquashBudget_ScalesWithDeltaSize(t *testing.T) {
	assert.Equal(t, 30*time.Second, squashBudget(0), "floor for an empty delta")
	assert.Equal(t, 30*time.Second+100*50*time.Millisecond, squashBudget(100))
	assert.Greater(t, squashBudget(6282), 5*time.Minute, "the measured delta gets minutes, not seconds")
	assert.Equal(t, 15*time.Minute, squashBudget(1_000_000), "capped")
}

// The caller's context dying just before the squash commit must not stop it:
// on 2026-10-08 02:35 the GitHub-sync context expired mid-validation and
// killed the commit. The validation and commit run detached from the caller's
// cancellation, on their own budget.
func TestSquashUnpushed_FinishesWhenCallerContextDiesMidSquash(t *testing.T) {
	repo, _ := initBareRemoteRepo(t)
	addCommit(t, repo, "one.txt", "1", "one")
	addCommit(t, repo, "two.txt", "2", "two")
	original := strings.TrimSpace(gitOut(t, repo, "rev-parse", "HEAD"))
	treeBefore := strings.TrimSpace(gitOut(t, repo, "rev-parse", "HEAD^{tree}"))

	ctx, cancel := context.WithCancel(context.Background())
	saved := beforeSquashCommitHook
	beforeSquashCommitHook = cancel
	t.Cleanup(func() { beforeSquashCommitHook = saved; cancel() })

	err := SquashUnpushed(ctx, repo, "squash")
	require.NoError(t, err)
	head := strings.TrimSpace(gitOut(t, repo, "rev-parse", "HEAD"))
	assert.NotEqual(t, original, head, "the squash commit landed")
	assert.Equal(t, "1", strings.TrimSpace(gitOut(t, repo, "rev-list", "--count", "@{upstream}..HEAD")))
	assert.Equal(t, treeBefore, strings.TrimSpace(gitOut(t, repo, "rev-parse", "HEAD^{tree}")), "same tree")
	assert.Empty(t, strings.TrimSpace(gitOut(t, repo, "diff", "--cached", "--name-only")), "nothing left merely staged")
}
