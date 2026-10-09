package gitutil

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPushWithRetry_CollapsesLargeBacklogAfterLosingRace proves that a push
// which loses the non-fast-forward race with a large local backlog lands as a
// single snapshot commit holding every file, with the pre-collapse tip kept
// under refs/ox-backup/.
//
// Failure prevented (#1261): replaying hundreds of commits took longer than
// the interval between coworkers' pushes, so every retry lost the same race
// and the Ledger never pushed.
func TestPushWithRetry_CollapsesLargeBacklogAfterLosingRace(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git push with retry")
	}
	repo, bare := initBareRemoteRepo(t)
	base := strings.TrimSpace(gitOut(t, bare, "rev-parse", "HEAD"))

	second := t.TempDir() + "/second"
	run(t, "", "git", "clone", "--quiet", bare, second)
	run(t, second, "git", "config", "user.email", "test@test.local")
	run(t, second, "git", "config", "user.name", "Test")
	addCommit(t, second, "coworker.txt", "theirs", "coworker commit")
	run(t, second, "git", "push", "--quiet")
	coworkerTip := strings.TrimSpace(gitOut(t, bare, "rev-parse", "HEAD"))

	backlog := collapseBacklogAbove + 5
	for i := 0; i < backlog; i++ {
		addCommit(t, repo, fmt.Sprintf("draft-%03d.txt", i), "x", fmt.Sprintf("session-draft: %d", i))
	}

	err := PushWithRetry(context.Background(), repo, PushOpts{MaxRetries: 3, OpTimeout: 60 * time.Second})
	require.NoError(t, err)

	landed, err := strconv.Atoi(strings.TrimSpace(gitOut(t, bare, "rev-list", "--count", base+"..HEAD")))
	require.NoError(t, err)
	assert.Equal(t, 2, landed, "the coworker's commit plus one collapsed snapshot must land, not the whole backlog")

	tree := gitOut(t, bare, "ls-tree", "-r", "--name-only", "HEAD")
	assert.Equal(t, backlog, strings.Count(tree, "draft-"), "every drafted file must survive the collapse")
	assert.Contains(t, tree, "coworker.txt")

	backup := strings.TrimSpace(gitOut(t, repo, "for-each-ref", "--format=%(refname)", "refs/ox-backup/"))
	require.NotEmpty(t, backup, "the pre-collapse tip must be kept under refs/ox-backup/")
	// the backup tip is the rebased history: every draft on top of the coworker's commit
	kept, err := strconv.Atoi(strings.TrimSpace(gitOut(t, repo, "rev-list", "--count", coworkerTip+".."+backup)))
	require.NoError(t, err)
	assert.Equal(t, backlog, kept, "the backup ref must hold the full pre-collapse history")
}

// TestPushWithRetry_SmallBacklogIsNotCollapsed keeps the per-commit history
// for ordinary backlogs: collapsing is a last resort for a lost race, not the
// default shape of a push.
func TestPushWithRetry_SmallBacklogIsNotCollapsed(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git push with retry")
	}
	repo, bare := initBareRemoteRepo(t)
	base := strings.TrimSpace(gitOut(t, bare, "rev-parse", "HEAD"))
	second := t.TempDir() + "/second"
	run(t, "", "git", "clone", "--quiet", bare, second)
	run(t, second, "git", "config", "user.email", "test@test.local")
	run(t, second, "git", "config", "user.name", "Test")
	addCommit(t, second, "coworker.txt", "theirs", "coworker commit")
	run(t, second, "git", "push", "--quiet")
	for i := 0; i < 3; i++ {
		addCommit(t, repo, fmt.Sprintf("draft-%d.txt", i), "x", fmt.Sprintf("session-draft: %d", i))
	}

	require.NoError(t, PushWithRetry(context.Background(), repo, PushOpts{MaxRetries: 3, OpTimeout: 60 * time.Second}))
	landed, err := strconv.Atoi(strings.TrimSpace(gitOut(t, bare, "rev-list", "--count", base+"..HEAD")))
	require.NoError(t, err)
	assert.Equal(t, 4, landed)
	assert.Empty(t, strings.TrimSpace(gitOut(t, repo, "for-each-ref", "refs/ox-backup/")))
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := RunGit(context.Background(), dir, args...)
	require.NoError(t, err, "git %v", args)
	return out
}
