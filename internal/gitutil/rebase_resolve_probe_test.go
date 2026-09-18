package gitutil

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- What an index READ FAILURE must look like to every caller ---
//
// #962's root cause was a probe failure wearing a conflict's clothes. The two
// functions below are the only two doors between `git ls-files --unmerged` and
// the daemon, so both must hand back the ErrConflictProbeFailed tag rather than
// an error that reads like a verdict about the index.
//
// A corrupt .git/index is the fixture because it is the one failure a test can
// produce deterministically and locally: git rejects it on the signature/version
// header, in microseconds, every time.

// HasUnmergedEntries is the fact-finding call the daemon's confirmation loop
// re-reads the index with. A `false, err` return MUST NOT be mistaken for
// "clean": the bool is meaningless when the read never happened.
func TestHasUnmergedEntries_UnreadableIndexIsATaggedFailure(t *testing.T) {
	t.Parallel()
	repo := newCorruptIndexRepo(t)

	conflicted, err := HasUnmergedEntries(context.Background(), repo)

	require.Error(t, err, "an index git cannot read is not an index with no conflicts")
	assert.ErrorIs(t, err, ErrConflictProbeFailed,
		"callers branch on this tag to tell a failed probe from a probe result")
	assert.False(t, conflicted, "the bool carries nothing when err != nil")
	assert.NotContains(t, err.Error(), "unresolved conflict",
		"the message must not assert a conclusion the probe never reached")
}

// ResolveRebaseAcceptTheirs is the other door: it lists the unmerged entries of
// the current rebase step before resolving anything. When that listing fails it
// must propagate the tag unwrapped — the caller aborts the rebase on error, and
// an error that claims a conflict would send it down the manual-resolution path
// for a repo whose conflicts were never enumerated.
func TestResolveRebaseAcceptTheirs_UnreadableIndexIsATaggedFailure(t *testing.T) {
	t.Parallel()
	repo := newCorruptIndexRepo(t)

	err := ResolveRebaseAcceptTheirs(context.Background(), repo, []string{"data/"})

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrConflictProbeFailed)
	assert.NotContains(t, err.Error(), "not under safe auto-resolve prefixes",
		"nothing was enumerated, so no path can have failed the safety check")
}

// newCorruptIndexRepo returns a one-commit repo whose .git/index git refuses to
// parse. Garbage bytes suffice: the header check fails before any content is
// read, so the failure is instant and identical on every git version.
func newCorruptIndexRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	gitInRepo(t, repo, "init", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "notes.txt"), []byte("base\n"), 0o644))
	gitInRepo(t, repo, "add", "notes.txt")
	gitInRepo(t, repo, "commit", "-m", "base")
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".git", "index"), []byte("invalid index"), 0o644))
	return repo
}
