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

// Automatic Ledger writers must publish exactly the bytes they validated. Every
// case here drives the real engine against a real repository, because the
// failure it prevents — bytes reaching the ledger that no validator saw — only
// exists at the seam between git's index, worktree, and object store.

const cleanMeta = `{"title":"Ready"}` + "\n"

func newSnapshotRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	gitInRepo(t, repo, "init", "-b", "main")
	gitInRepo(t, repo, "config", "user.name", "Test")
	gitInRepo(t, repo, "config", "user.email", "test@example.com")
	writeGitutilFixture(t, repo, "base.txt", "base\n")
	gitInRepo(t, repo, "add", "base.txt")
	gitInRepo(t, repo, "commit", "-m", "base")
	return repo
}

func headBlob(t *testing.T, repo, path string) string {
	t.Helper()
	out, err := cleanGitOutput(context.Background(), repo, "show", "HEAD:"+path)
	require.NoError(t, err, "HEAD must contain %s", path)
	return string(out)
}

func headHasPath(t *testing.T, repo, path string) bool {
	t.Helper()
	_, err := cleanGitOutput(context.Background(), repo, "cat-file", "-e", "HEAD:"+path)
	return err == nil
}

// TestCommitLedgerSnapshot_CommitsStagedBlobNotWorktree is the regression for
// the PR #910 review finding: `git commit -- <pathspec>` reads the WORKTREE, so
// an unlocked writer that rewrites meta.json after validation gets its bytes
// published under the daemon's message. The engine must commit the validated
// index blob and leave the worktree edit where it is.
func TestCommitLedgerSnapshot_CommitsStagedBlobNotWorktree(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repository")
	}
	repo := newSnapshotRepo(t)
	const path = "sessions/example/meta.json"
	writeGitutilFixture(t, repo, path, cleanMeta)
	gitInRepo(t, repo, "add", "--sparse", path)

	// An unlocked writer replaces the file after it was staged and validated.
	writeGitutilFixture(t, repo, path, conflictMarkerFixture)

	committed, err := CommitLedgerSnapshot(context.Background(), repo, "finalize session example", "sessions/example/")
	require.NoError(t, err)
	require.True(t, committed)
	assert.Equal(t, cleanMeta, headBlob(t, repo, path), "the commit must carry the validated index blob")
	worktree, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(path)))
	require.NoError(t, err)
	assert.Equal(t, conflictMarkerFixture, string(worktree), "the engine never touches the worktree")
	assert.Equal(t, "finalize session example", gitInRepo(t, repo, "log", "-1", "--format=%s"))
}

// TestCommitLedgerSnapshot_PathScopeLeavesOtherStagedEntries protects the
// isolation callers rely on: a session finalize must not sweep another
// session's staged files into its own commit, and must leave them staged.
func TestCommitLedgerSnapshot_PathScopeLeavesOtherStagedEntries(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repository")
	}
	repo := newSnapshotRepo(t)
	writeGitutilFixture(t, repo, "sessions/a/meta.json", cleanMeta)
	writeGitutilFixture(t, repo, "sessions/b/meta.json", cleanMeta)
	gitInRepo(t, repo, "add", "--sparse", "sessions/")

	committed, err := CommitLedgerSnapshot(context.Background(), repo, "finalize session a", "sessions/a/")
	require.NoError(t, err)
	require.True(t, committed)
	assert.True(t, headHasPath(t, repo, "sessions/a/meta.json"))
	assert.False(t, headHasPath(t, repo, "sessions/b/meta.json"), "a path-scoped commit must not publish a neighbor")
	assert.Equal(t, "sessions/b/meta.json", gitInRepo(t, repo, "diff", "--cached", "--name-only"),
		"the neighbor must remain staged for its own commit")
}

// TestCommitLedgerSnapshot_PathScopedDeletion — a retraction is a staged
// deletion; the scoped snapshot must carry it, not resurrect HEAD's copy.
func TestCommitLedgerSnapshot_PathScopedDeletion(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repository")
	}
	repo := newSnapshotRepo(t)
	writeGitutilFixture(t, repo, "sessions/a/meta.json", cleanMeta)
	writeGitutilFixture(t, repo, "sessions/b/meta.json", cleanMeta)
	gitInRepo(t, repo, "add", "--sparse", "sessions/")
	gitInRepo(t, repo, "commit", "-m", "two drafts")
	gitInRepo(t, repo, "rm", "-q", "-r", "sessions/a")
	gitInRepo(t, repo, "rm", "-q", "-r", "sessions/b") // staged but out of scope

	committed, err := CommitLedgerSnapshot(context.Background(), repo, "session-draft: retract a", "sessions/a")
	require.NoError(t, err)
	require.True(t, committed)
	assert.False(t, headHasPath(t, repo, "sessions/a/meta.json"), "the scoped deletion must land")
	assert.True(t, headHasPath(t, repo, "sessions/b/meta.json"), "an out-of-scope deletion must not")
}

// TestCommitLedgerSnapshot_IgnoresBlobStagedAfterSnapshot reproduces the PR
// #811 interleaving: a second writer stages a conflict-marker blob AFTER the
// index is snapshotted but before the commit. The immutable-tree commit must
// persist only the snapshot.
func TestCommitLedgerSnapshot_IgnoresBlobStagedAfterSnapshot(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repository")
	}
	ctx := context.Background()
	repo := newSnapshotRepo(t)
	writeGitutilFixture(t, repo, "sessions/x/meta.json", cleanMeta)
	gitInRepo(t, repo, "add", "--sparse", "sessions/x/meta.json")

	tree, err := writeIndexTree(ctx, repo, nil)
	require.NoError(t, err)
	parent, err := currentBranchTip(ctx, repo)
	require.NoError(t, err)

	writeGitutilFixture(t, repo, "sessions/y/meta.json", conflictMarkerFixture)
	gitInRepo(t, repo, "add", "--sparse", "sessions/y/meta.json")

	require.NoError(t, commitTreeToBranch(ctx, repo, tree, parent, "session: x"))
	assert.Equal(t, cleanMeta, headBlob(t, repo, "sessions/x/meta.json"))
	assert.False(t, headHasPath(t, repo, "sessions/y/meta.json"), "a blob staged after the snapshot must not be committed")
}

// TestCommitTreeToBranch_RefusesWhenBranchAdvanced — the compare-and-swap on
// update-ref is what turns a concurrent raw-git commit into a retry instead of
// a silent overwrite.
func TestCommitTreeToBranch_RefusesWhenBranchAdvanced(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repository")
	}
	ctx := context.Background()
	repo := newSnapshotRepo(t)
	writeGitutilFixture(t, repo, "sessions/x/meta.json", cleanMeta)
	gitInRepo(t, repo, "add", "--sparse", "sessions/x/meta.json")
	tree, err := writeIndexTree(ctx, repo, nil)
	require.NoError(t, err)
	parent, err := currentBranchTip(ctx, repo)
	require.NoError(t, err)

	writeGitutilFixture(t, repo, "other.txt", "theirs\n")
	gitInRepo(t, repo, "add", "other.txt")
	gitInRepo(t, repo, "commit", "-m", "theirs")
	theirs := gitInRepo(t, repo, "rev-parse", "HEAD")

	err = commitTreeToBranch(ctx, repo, tree, parent, "ours")
	require.ErrorContains(t, err, "concurrent ledger commit")
	assert.Equal(t, theirs, gitInRepo(t, repo, "rev-parse", "HEAD"), "the concurrent commit must survive")
}

func TestCommitLedgerSnapshot_NothingToCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repository")
	}
	repo := newSnapshotRepo(t)
	before := gitInRepo(t, repo, "rev-parse", "HEAD")

	committed, err := CommitLedgerSnapshot(context.Background(), repo, "noop", "sessions/none/")
	require.NoError(t, err)
	assert.False(t, committed)
	committed, err = CommitLedgerSnapshot(context.Background(), repo, "noop")
	require.NoError(t, err)
	assert.False(t, committed)
	assert.Equal(t, before, gitInRepo(t, repo, "rev-parse", "HEAD"))
}

func TestCommitLedgerSnapshot_RefusesInvalidBlob(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repository")
	}
	for _, tc := range []struct {
		name    string
		path    string
		content string
		wantErr string
	}{
		{name: "conflict markers", path: "sessions/x/summary.md", content: conflictMarkerFixture, wantErr: "unresolved conflict"},
		{name: "malformed session metadata", path: "sessions/x/meta.json", content: `{"title":`, wantErr: "invalid JSON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newSnapshotRepo(t)
			before := gitInRepo(t, repo, "rev-parse", "HEAD")
			writeGitutilFixture(t, repo, tc.path, tc.content)
			gitInRepo(t, repo, "add", "--sparse", tc.path)

			committed, err := CommitLedgerSnapshot(context.Background(), repo, "bad", "sessions/x/")
			require.ErrorContains(t, err, tc.wantErr)
			assert.False(t, committed)
			assert.Equal(t, before, gitInRepo(t, repo, "rev-parse", "HEAD"), "validation failure must not advance HEAD")
			assert.Equal(t, tc.path, gitInRepo(t, repo, "diff", "--cached", "--name-only"), "the staged entry must survive for recovery")
		})
	}
}

func TestCommitLedgerSnapshot_UnbornBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repository")
	}
	repo := t.TempDir()
	gitInRepo(t, repo, "init", "-b", "main")
	gitInRepo(t, repo, "config", "user.name", "Test")
	gitInRepo(t, repo, "config", "user.email", "test@example.com")
	writeGitutilFixture(t, repo, "sessions/x/meta.json", cleanMeta)
	writeGitutilFixture(t, repo, "data/murmurs/m.json", "{}\n")
	gitInRepo(t, repo, "add", "--sparse", ".")

	committed, err := CommitLedgerSnapshot(context.Background(), repo, "first", "sessions/x/")
	require.NoError(t, err)
	require.True(t, committed)
	assert.True(t, headHasPath(t, repo, "sessions/x/meta.json"))
	assert.False(t, headHasPath(t, repo, "data/murmurs/m.json"), "scope must hold on the first commit too")
	assert.Equal(t, "main", strings.TrimSpace(gitInRepo(t, repo, "branch", "--show-current")))
}

// TestCommitLedgerSnapshot_RefusesUnmergedIndexOutsideScope — git refuses every
// commit while any stage is unresolved; the scoped snapshot must not hide a
// live conflict elsewhere in the index behind a temporary index.
func TestCommitLedgerSnapshot_RefusesUnmergedIndexOutsideScope(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repository")
	}
	repo := newSnapshotRepo(t)
	writeGitutilFixture(t, repo, "shared.txt", "base\n")
	gitInRepo(t, repo, "add", "shared.txt")
	gitInRepo(t, repo, "commit", "-m", "shared base")
	gitInRepo(t, repo, "checkout", "-b", "other")
	writeGitutilFixture(t, repo, "shared.txt", "other\n")
	gitInRepo(t, repo, "commit", "-am", "other change")
	gitInRepo(t, repo, "checkout", "main")
	writeGitutilFixture(t, repo, "shared.txt", "main\n")
	gitInRepo(t, repo, "commit", "-am", "main change")
	_, mergeErr := RunGit(context.Background(), repo, "merge", "other")
	require.Error(t, mergeErr, "fixture must leave an unmerged index")
	writeGitutilFixture(t, repo, "sessions/x/meta.json", cleanMeta)
	gitInRepo(t, repo, "add", "--sparse", "sessions/x/meta.json")
	before := gitInRepo(t, repo, "rev-parse", "HEAD")

	committed, err := CommitLedgerSnapshot(context.Background(), repo, "during merge", "sessions/x/")
	require.ErrorContains(t, err, "unresolved conflict in index")
	assert.False(t, committed)
	assert.Equal(t, before, gitInRepo(t, repo, "rev-parse", "HEAD"))
}

// TestCommitLedgerSnapshot_SparseCheckout — ledger clones use cone-mode
// sparse-checkout; the temporary index must stage and commit paths outside
// the cone exactly like `git add --sparse` does.
func TestCommitLedgerSnapshot_SparseCheckout(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repository")
	}
	repo := newSnapshotRepo(t)
	writeGitutilFixture(t, repo, "data/plans/p.md", "plan\n")
	writeGitutilFixture(t, repo, "sessions/x/meta.json", cleanMeta)
	gitInRepo(t, repo, "add", "--sparse", ".")
	gitInRepo(t, repo, "commit", "-m", "seed")
	gitInRepo(t, repo, "sparse-checkout", "set", "--cone", "data/plans")
	require.NoFileExists(t, filepath.Join(repo, "sessions", "x", "meta.json"), "fixture must exclude sessions/ from the cone")

	updated := `{"title":"Updated"}` + "\n"
	writeGitutilFixture(t, repo, "sessions/x/meta.json", updated)
	gitInRepo(t, repo, "add", "--sparse", "sessions/x/meta.json")

	committed, err := CommitLedgerSnapshot(context.Background(), repo, "finalize outside cone", "sessions/x/")
	require.NoError(t, err)
	require.True(t, committed)
	assert.Equal(t, updated, headBlob(t, repo, "sessions/x/meta.json"))
	assert.Equal(t, "plan\n", headBlob(t, repo, "data/plans/p.md"), "paths outside the pathspec must be preserved")
}
