package gitutil

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/sacred"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Automatic Ledger writers must publish exactly the bytes they validated. Every
// case here drives the real engine against a real repository, because the
// failure it prevents — bytes reaching the ledger that no validator saw — only
// exists at the seam between git's index, worktree, and object store.

const cleanMeta = `{"title":"Ready"}` + "\n"

// Confirming one session's deletion must not authorize a neighboring staged wipe.
func TestCommitLedgerSessionDeletion_ExemptsOnlyConfirmedSessions(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repository")
	}
	repo := newSnapshotRepo(t)
	for _, name := range []string{"selected", "selected-neighbor"} {
		for i := range sacred.MassDeleteThreshold + 1 {
			writeGitutilFixture(t, repo, "sessions/"+name+"/part-"+string(rune('a'+i))+".md", "recorded work\n")
		}
	}
	gitInRepo(t, repo, "add", "sessions")
	gitInRepo(t, repo, "commit", "-m", "seed sessions")
	writeGitutilFixture(t, repo, "sessions/staged-only/meta.json", cleanMeta)
	gitInRepo(t, repo, "add", "sessions/staged-only")
	gitInRepo(t, repo, "rm", "-r", "--force", "sessions/selected", "sessions/selected-neighbor", "sessions/staged-only")
	writeGitutilFixture(t, repo, "sessions/recovered/meta.json", cleanMeta)
	writeGitutilFixture(t, repo, "data/plans/draft/plan.md", "unsaved plan\n")
	gitInRepo(t, repo, "add", "sessions/recovered", "data/plans")
	head := gitInRepo(t, repo, "rev-parse", "HEAD")
	otherStaging := gitInRepo(t, repo, "diff", "--cached", "--binary", "--", ":/", ":(exclude)sessions/selected")
	ctx := context.Background()
	require.NoError(t, WithRepoLock(ctx, repo, func() error {
		committed, err := CommitLedgerSessionDeletion(ctx, repo, "delete selected", []string{"selected", "staged-only"}, ":/", ":(exclude)data/plans")
		require.ErrorContains(t, err, "sacred")
		assert.False(t, committed)
		return nil
	}))
	assert.Equal(t, head, gitInRepo(t, repo, "rev-parse", "HEAD"))
	assert.Empty(t, gitInRepo(t, repo, "diff", "HEAD", "--", "sessions/selected"), "the refused deletion must be restored")
	assert.FileExists(t, filepath.Join(repo, "sessions/selected/part-a.md"))
	assert.Equal(t, otherStaging, gitInRepo(t, repo, "diff", "--cached", "--binary", "--", ":/", ":(exclude)sessions/selected"))

	gitInRepo(t, repo, "restore", "--source=HEAD", "--staged", "--worktree", "sessions/selected-neighbor")
	gitInRepo(t, repo, "rm", "-r", "sessions/selected")
	require.NoError(t, WithRepoLock(ctx, repo, func() error {
		committed, err := CommitLedgerSessionDeletion(ctx, repo, "delete selected", []string{"selected"}, ":/", ":(exclude)data/plans")
		require.NoError(t, err)
		assert.True(t, committed)
		return nil
	}))
	assert.False(t, headHasPath(t, repo, "sessions/selected"))
	assert.True(t, headHasPath(t, repo, "sessions/selected-neighbor"))
	assert.True(t, headHasPath(t, repo, "sessions/recovered/meta.json"))
	assert.Equal(t, "data/plans/draft/plan.md", gitInRepo(t, repo, "diff", "--cached", "--name-only"))

	gitInRepo(t, repo, "rm", "-r", "sessions/selected-neighbor")
	require.NoError(t, WithRepoLock(ctx, repo, func() error {
		committed, err := CommitLedgerSnapshot(ctx, repo, "automatic snapshot", ":/", ":(exclude)data/plans")
		require.ErrorContains(t, err, "sacred")
		assert.False(t, committed)
		return nil
	}))
}

func TestCommitLedgerSessionDeletion_CancellationRestoresSelectedSession(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repository")
	}
	repo := newSnapshotRepo(t)
	const path = "sessions/selected/meta.json"
	writeGitutilFixture(t, repo, path, cleanMeta)
	gitInRepo(t, repo, "add", path)
	gitInRepo(t, repo, "commit", "-m", "seed session")
	// A host's LFS filter must not run during rollback: a failed download
	// would leave the selected session deleted and block later snapshots.
	writeGitutilFixture(t, repo, ".git/info/attributes", "sessions/** filter=lfs\n")
	gitInRepo(t, repo, "config", "filter.lfs.process", "exit 1")
	gitInRepo(t, repo, "config", "filter.lfs.clean", "cat")
	gitInRepo(t, repo, "config", "filter.lfs.smudge", "exit 1")
	gitInRepo(t, repo, "config", "filter.lfs.required", "true")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, WithRepoLock(context.Background(), repo, func() error {
		_, err := RunGit(context.Background(), repo, "rm", path)
		require.NoError(t, err)
		committed, err := CommitLedgerSessionDeletion(ctx, repo, "delete", []string{"selected"})
		require.ErrorIs(t, err, context.Canceled)
		assert.False(t, committed)
		return nil
	}))
	status, err := RunGit(context.Background(), repo, "status", "--porcelain")
	require.NoError(t, err)
	assert.Empty(t, status)
	content, err := os.ReadFile(filepath.Join(repo, path))
	require.NoError(t, err)
	assert.Equal(t, cleanMeta, string(content))
}

func TestCommitLedgerSessionDeletion_RequiresSessionNames(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repository")
	}
	for _, names := range [][]string{nil, {""}, {"."}, {".."}, {"../neighbor"}, {"a/b"}, {`a\b`}, {"a\x00b"}} {
		t.Run(strings.Join(names, ","), func(t *testing.T) {
			repo := newSnapshotRepo(t)
			writeGitutilFixture(t, repo, "sessions/ready/meta.json", cleanMeta)
			gitInRepo(t, repo, "add", "sessions/ready")
			head := gitInRepo(t, repo, "rev-parse", "HEAD")
			ctx := context.Background()
			require.NoError(t, WithRepoLock(ctx, repo, func() error {
				committed, err := CommitLedgerSessionDeletion(ctx, repo, "delete", names)
				assert.Error(t, err)
				assert.False(t, committed)
				return nil
			}))
			assert.Equal(t, head, gitInRepo(t, repo, "rev-parse", "HEAD"), "invalid names must not commit staged work")
		})
	}
}

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

// TestStageLedgerBlob_CommitsTheFrozenBytes: a writer that froze a record's
// bytes stages exactly those, whatever the worktree file holds, and a path
// git refuses is an error, not a silent no-op. Failure prevented: a commit of
// a record publishes bytes edited after they were frozen.
func TestStageLedgerBlob_CommitsTheFrozenBytes(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repository")
	}
	repo := newSnapshotRepo(t)
	const path = "data/plans/p/feedback/round-x.json"
	writeGitutilFixture(t, repo, path, "edited after it was frozen\n")

	oid, err := StageLedgerBlob(context.Background(), repo, path, []byte(cleanMeta))
	require.NoError(t, err)
	committed, err := CommitLedgerSnapshot(context.Background(), repo, "plan review: p", path)
	require.NoError(t, err)
	require.True(t, committed)
	assert.Equal(t, cleanMeta, headBlob(t, repo, path), "the commit must carry the frozen bytes")
	assert.Equal(t, oid, gitInRepo(t, repo, "rev-parse", "HEAD:"+path))

	_, err = StageLedgerBlob(context.Background(), repo, "../outside.json", []byte(cleanMeta))
	assert.Error(t, err, "git refuses a path outside the repository")
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

	require.NoError(t, commitTreeToBranch(ctx, repo, tree, []string{parent}, parent, "session: x", nil))
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

	err = commitTreeToBranch(ctx, repo, tree, []string{parent}, parent, "ours", nil)
	require.ErrorContains(t, err, "concurrent ledger commit")
	assert.Equal(t, theirs, gitInRepo(t, repo, "rev-parse", "HEAD"), "the concurrent commit must survive")
}

// TestCommitTreeToBranch_RefusesWhenUnbornBranchWasBorn — the CAS must hold
// on a first commit too: an empty old value makes update-ref require the ref
// to still be absent, so a concurrent first commit is not overwritten.
func TestCommitTreeToBranch_RefusesWhenUnbornBranchWasBorn(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repository")
	}
	ctx := context.Background()
	repo := t.TempDir()
	gitInRepo(t, repo, "init", "-b", "main")
	gitInRepo(t, repo, "config", "user.name", "Test")
	gitInRepo(t, repo, "config", "user.email", "test@example.com")
	writeGitutilFixture(t, repo, "sessions/x/meta.json", cleanMeta)
	gitInRepo(t, repo, "add", "--sparse", "sessions/x/meta.json")
	tree, err := writeIndexTree(ctx, repo, nil)
	require.NoError(t, err)
	parent, err := currentBranchTip(ctx, repo)
	require.NoError(t, err)
	require.Empty(t, parent, "fixture must start on an unborn branch")

	writeGitutilFixture(t, repo, "other.txt", "theirs\n")
	gitInRepo(t, repo, "add", "other.txt")
	gitInRepo(t, repo, "commit", "-m", "theirs")
	theirs := gitInRepo(t, repo, "rev-parse", "HEAD")

	err = commitTreeToBranch(ctx, repo, tree, nil, parent, "ours", nil)
	require.ErrorContains(t, err, "concurrent ledger commit")
	assert.Equal(t, theirs, gitInRepo(t, repo, "rev-parse", "HEAD"), "the concurrent first commit must survive")
}

func TestAmendLedgerSnapshot_PreservesHistoryAndOtherStagedWork(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repository")
	}
	for _, shape := range []string{"root", "child", "merge"} {
		t.Run(shape, func(t *testing.T) {
			repo := newSnapshotRepo(t)
			base := gitInRepo(t, repo, "rev-parse", "HEAD")
			writeGitutilFixture(t, repo, "notes.txt", "original\n")
			gitInRepo(t, repo, "add", "notes.txt")
			tree := gitInRepo(t, repo, "write-tree")
			args := []string{"-c", "user.name=Original author", "-c", "user.email=original@example.com", "commit-tree", tree, "-m", "holding commit\n\nOriginal details."}
			if shape != "root" {
				args = append(args, "-p", base)
			}
			if shape == "merge" {
				side := gitInRepo(t, repo, "commit-tree", tree, "-p", base, "-m", "side")
				args = append(args, "-p", side)
			}
			tip := gitInRepo(t, repo, args...)
			gitInRepo(t, repo, "update-ref", "HEAD", tip)
			identity := gitInRepo(t, repo, "show", "-s", "--format=%P%n%an%n%ae%n%aI%n%B", "HEAD")
			writeGitutilFixture(t, repo, "notes.txt", "repaired\n")
			gitInRepo(t, repo, "add", "notes.txt")
			writeGitutilFixture(t, repo, "notes.txt", "later working edit\n")
			writeGitutilFixture(t, repo, "data/plans/p/plan.md", "unpublished\n")
			gitInRepo(t, repo, "add", "--sparse", "data/plans/p/plan.md")

			committed, err := AmendLedgerSnapshot(context.Background(), repo, "notes.txt")
			require.NoError(t, err)
			require.True(t, committed)
			assert.Equal(t, "repaired\n", headBlob(t, repo, "notes.txt"))
			assert.Equal(t, identity, gitInRepo(t, repo, "show", "-s", "--format=%P%n%an%n%ae%n%aI%n%B", "HEAD"))
			assert.False(t, headHasPath(t, repo, "data/plans/p/plan.md"))
			assert.Equal(t, "data/plans/p/plan.md", gitInRepo(t, repo, "diff", "--cached", "--name-only"))
			worktree, err := os.ReadFile(filepath.Join(repo, "notes.txt"))
			require.NoError(t, err)
			assert.Equal(t, "later working edit\n", string(worktree))
		})
	}
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

// TestCommitLedgerSnapshot_UnbornBranch: a scope holds on the first commit,
// whether it names a path or excludes one, as `ox doctor` excludes data/plans/.
// Failure prevented: the first commit publishes what its scope left out, or an
// exclude pathspec fails it outright.
func TestCommitLedgerSnapshot_UnbornBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repository")
	}
	for _, pathspecs := range [][]string{{"sessions/x/"}, {":/", ":(exclude)data/murmurs"}} {
		t.Run(strings.Join(pathspecs, " "), func(t *testing.T) {
			repo := t.TempDir()
			gitInRepo(t, repo, "init", "-b", "main")
			gitInRepo(t, repo, "config", "user.name", "Test")
			gitInRepo(t, repo, "config", "user.email", "test@example.com")
			writeGitutilFixture(t, repo, "sessions/x/meta.json", cleanMeta)
			writeGitutilFixture(t, repo, "data/murmurs/m.json", "{}\n")
			gitInRepo(t, repo, "add", "--sparse", ".")

			committed, err := CommitLedgerSnapshot(context.Background(), repo, "first", pathspecs...)
			require.NoError(t, err)
			require.True(t, committed)
			assert.True(t, headHasPath(t, repo, "sessions/x/meta.json"))
			assert.False(t, headHasPath(t, repo, "data/murmurs/m.json"), "scope must hold on the first commit too")
			assert.Equal(t, "main", strings.TrimSpace(gitInRepo(t, repo, "branch", "--show-current")))
		})
	}
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
	// Exercise the unmerged-index check even without an active merge marker.
	gitInRepo(t, repo, "merge", "--quit")
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

// seedSacredSessionFiles commits n tracked files under sessions/ and returns
// their ledger-relative paths.
func seedSacredSessionFiles(t *testing.T, repo string, n int) []string {
	t.Helper()
	var paths []string
	for i := 0; i < n; i++ {
		path := filepath.ToSlash(filepath.Join("sessions", "s"+string(rune('a'+i)), "raw.jsonl"))
		writeGitutilFixture(t, repo, path, "recording "+path+"\n")
		paths = append(paths, path)
	}
	gitInRepo(t, repo, "add", "--sparse", "sessions/")
	gitInRepo(t, repo, "commit", "-m", "seed sessions")
	return paths
}

func writeCacheCopy(t *testing.T, repo, path, content string) {
	t.Helper()
	writeGitutilFixture(t, repo, filepath.ToSlash(filepath.Join(".sageox", "cache", path)), content)
}

// TestCommitLedgerSnapshotPreserving_ExemptsOnlyCacheVerifiedPaths covers the
// narrow replacement for the removed override env var: a bulk untrack commits
// only when every deleted path's exact bytes sit in .sageox/cache.
func TestCommitLedgerSnapshotPreserving_ExemptsOnlyCacheVerifiedPaths(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repository")
	}
	exact := func(p string) (string, bool) { return "recording " + p + "\n", true }
	tests := []struct {
		name       string
		cacheFor   func(path string) (string, bool)
		preserved  func(paths []string) []string
		wantCommit bool
	}{
		{
			name:       "cache holds exact bytes for every preserved path",
			cacheFor:   exact,
			preserved:  func(paths []string) []string { return paths },
			wantCommit: true,
		},
		{
			name:       "same deletion without cache proof is refused",
			cacheFor:   func(string) (string, bool) { return "", false },
			preserved:  func(paths []string) []string { return paths },
			wantCommit: false,
		},
		{
			name:       "cache holds different bytes",
			cacheFor:   func(p string) (string, bool) { return "recording " + p + " (short)\n", true },
			preserved:  func(paths []string) []string { return paths },
			wantCommit: false,
		},
		{
			name:       "cache is right but paths were not declared",
			cacheFor:   exact,
			preserved:  func([]string) []string { return nil },
			wantCommit: false,
		},
		{
			name:       "only some deleted paths are preserved",
			cacheFor:   exact,
			preserved:  func(paths []string) []string { return paths[:3] },
			wantCommit: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newSnapshotRepo(t)
			paths := seedSacredSessionFiles(t, repo, sacred.MassDeleteThreshold+4)
			for _, p := range paths {
				if content, ok := tt.cacheFor(p); ok {
					writeCacheCopy(t, repo, p, content)
				}
			}
			before := headBlob(t, repo, paths[0])
			gitInRepo(t, repo, "rm", "-q", "--cached", "--sparse", "-r", "sessions")

			committed, err := CommitLedgerSnapshotPreserving(context.Background(), repo, "doctor: untrack", tt.preserved(paths), "sessions")

			if tt.wantCommit {
				require.NoError(t, err)
				assert.True(t, committed)
				assert.False(t, headHasPath(t, repo, paths[0]))
				return
			}
			require.Error(t, err)
			assert.False(t, committed)
			assert.Equal(t, before, headBlob(t, repo, paths[0]), "a refused commit leaves every sacred blob in HEAD")
		})
	}
}

// TestCommitLedgerSnapshot_NoOverrideEnv proves the retired env var no longer
// bypasses the guard, even when set.
func TestCommitLedgerSnapshot_NoOverrideEnv(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repository")
	}
	repo := newSnapshotRepo(t)
	paths := seedSacredSessionFiles(t, repo, sacred.MassDeleteThreshold+2)
	gitInRepo(t, repo, "rm", "-q", "--cached", "--sparse", "-r", "sessions")
	t.Setenv("OX_ALLOW_SACRED_MASS_DELETE", "1")

	committed, err := CommitLedgerSnapshot(context.Background(), repo, "bulk removal")

	require.Error(t, err)
	assert.False(t, committed)
	assert.True(t, headHasPath(t, repo, paths[0]))
}

// An empty or unchanged scope must never consume another writer's staging;
// an unborn branch has no holding commit to amend.
func TestAmendLedgerSnapshot_RequiresExistingScopedWork(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repositories")
	}
	for _, tc := range []struct {
		name, wantErr string
		paths         []string
		unborn        bool
	}{
		{name: "explicit paths required", wantErr: "explicit paths"},
		{name: "unborn branch", paths: []string{"base.txt"}, unborn: true, wantErr: "unborn"},
		{name: "unchanged selected path", paths: []string{"base.txt"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newSnapshotRepo(t)
			head := gitInRepo(t, repo, "rev-parse", "HEAD")
			writeGitutilFixture(t, repo, "data/plans/p/plan.md", "pending plan\n")
			gitInRepo(t, repo, "add", "--sparse", "data/plans")
			if tc.unborn {
				gitInRepo(t, repo, "update-ref", "-d", "refs/heads/main")
			}
			index := gitInRepo(t, repo, "ls-files", "--stage")
			committed, err := AmendLedgerSnapshot(context.Background(), repo, tc.paths...)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.False(t, committed)
			assert.Equal(t, index, gitInRepo(t, repo, "ls-files", "--stage"))
			if tc.unborn {
				_, err := cleanGitOutput(context.Background(), repo, "rev-parse", "--verify", "HEAD")
				assert.Error(t, err, "an amendment cannot create the first commit")
			} else {
				assert.Equal(t, head, gitInRepo(t, repo, "rev-parse", "HEAD"))
				assert.False(t, headHasPath(t, repo, "data/plans/p/plan.md"))
			}
		})
	}
}

// Refused session bytes stay staged for correction; the same scoped amendment
// must make progress once the metadata or LFS pointer is repaired.
func TestAmendLedgerSnapshot_InvalidSessionBytesRemainRecoverable(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repositories")
	}
	for _, tc := range []struct {
		name, path, original, invalid, repaired, wantErr string
	}{
		{"metadata", "sessions/x/meta.json", cleanMeta, `{"title":`, `{"title":"Repaired"}` + "\n", "invalid JSON"},
		{"LFS artifact", "sessions/x/raw.jsonl", testLFSPointer, `{"type":"user","content":"pending"}` + "\n", strings.Replace(testLFSPointer, "size 42", "size 43", 1), "LFS pointer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newSnapshotRepo(t)
			writeGitutilFixture(t, repo, tc.path, tc.original)
			gitInRepo(t, repo, "add", "--sparse", tc.path)
			gitInRepo(t, repo, "commit", "-m", "holding session")
			head := gitInRepo(t, repo, "rev-parse", "HEAD")
			writeGitutilFixture(t, repo, tc.path, tc.invalid)
			gitInRepo(t, repo, "add", "--sparse", tc.path)

			committed, err := AmendLedgerSnapshot(context.Background(), repo, tc.path)
			require.ErrorContains(t, err, tc.wantErr)
			assert.False(t, committed)
			assert.Equal(t, head, gitInRepo(t, repo, "rev-parse", "HEAD"))
			staged, err := cleanGitOutput(context.Background(), repo, "show", ":"+tc.path)
			require.NoError(t, err)
			assert.Equal(t, tc.invalid, string(staged), "a refusal must preserve the staged repair for recovery")

			writeGitutilFixture(t, repo, tc.path, tc.repaired)
			gitInRepo(t, repo, "add", "--sparse", tc.path)
			committed, err = AmendLedgerSnapshot(context.Background(), repo, tc.path)
			require.NoError(t, err)
			assert.True(t, committed)
			assert.Equal(t, tc.repaired, headBlob(t, repo, tc.path))
			assert.Empty(t, gitInRepo(t, repo, "diff", "--cached", "--name-only"))
		})
	}
}

// Repairing another path must not reject a session deletion already confirmed
// through the deletion writer. New unconfirmed deletions still need the guard.
func TestAmendLedgerSnapshot_PreservesConfirmedDeletion(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repository")
	}
	repo := newSnapshotRepo(t)
	for _, name := range []string{"selected", "remaining"} {
		for i := range sacred.MassDeleteThreshold + 1 {
			writeGitutilFixture(t, repo, "sessions/"+name+"/part-"+string(rune('a'+i))+".md", "recorded work\n")
		}
	}
	gitInRepo(t, repo, "add", "sessions")
	gitInRepo(t, repo, "commit", "-m", "seed sessions")
	ctx := context.Background()
	require.NoError(t, WithRepoLock(ctx, repo, func() error {
		gitInRepo(t, repo, "rm", "-r", "sessions/selected")
		_, err := CommitLedgerSessionDeletion(ctx, repo, "delete selected", []string{"selected"})
		return err
	}))
	writeGitutilFixture(t, repo, "repair.txt", "repaired\n")
	gitInRepo(t, repo, "add", "repair.txt")
	require.NoError(t, WithRepoLock(ctx, repo, func() error {
		committed, err := AmendLedgerSnapshot(ctx, repo, "repair.txt")
		require.NoError(t, err)
		assert.True(t, committed)
		return nil
	}))
	assert.False(t, headHasPath(t, repo, "sessions/selected"))
	assert.True(t, headHasPath(t, repo, "sessions/remaining"))
	assert.Equal(t, "repaired\n", headBlob(t, repo, "repair.txt"))

	head := gitInRepo(t, repo, "rev-parse", "HEAD")
	gitInRepo(t, repo, "rm", "-r", "sessions/remaining")
	require.NoError(t, WithRepoLock(ctx, repo, func() error {
		committed, err := AmendLedgerSnapshot(ctx, repo, "sessions/remaining")
		require.ErrorContains(t, err, "sacred")
		assert.False(t, committed)
		return nil
	}))
	assert.Equal(t, head, gitInRepo(t, repo, "rev-parse", "HEAD"))
}
