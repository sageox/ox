package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/manifest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- #962: a failed conflict probe is not a conflict ---
//
// A few minutes of dead DNS made `git ls-files --unmerged` die on its context
// deadline. pullManagedRepo treated every non-nil error from autostash
// recovery as a confirmed merge conflict, so two provably clean clones were
// reported as "has unresolved conflicts: ... context deadline exceeded" and
// pinned behind RequiresConfirm — a gate nothing clears once the network
// returns.

func TestIsUndeterminedIndexState(t *testing.T) {
	t.Parallel()
	probeFailure := fmt.Errorf("%w: git ls-files --unmerged: %w",
		gitutil.ErrConflictProbeFailed, errors.New("exit status 128"))

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"probe sentinel", probeFailure, true},
		{"probe sentinel joined with a pull failure", errors.Join(errors.New("pull failed"), probeFailure), true},
		// Verbatim shape from the incident report.
		{"deadline through the probe", fmt.Errorf("%w: git ls-files --unmerged: git ls-files: : %w",
			gitutil.ErrConflictProbeFailed, context.DeadlineExceeded), true},
		// Cancellation can also strike AFTER the probe succeeded, deeper in
		// ResolveAutostashConflicts, where the wrapper names the step rather
		// than the cause. Still undetermined, still retryable.
		{"cancellation reading a conflict stage", fmt.Errorf("read conflict stage for sessions/x/meta.json: %w", context.Canceled), true},
		{"bare deadline", context.DeadlineExceeded, true},

		// Genuine conflict verdicts MUST stay eligible for an issue — a human
		// really does have to adjudicate these.
		{"unresolved conflict", errors.New("unresolved conflict in notes.txt requires manual resolution"), false},
		{"field differs", errors.New("field title differs in sessions/x/meta.json; manual resolution required"), false},
		{"merge head present", errors.New("cannot recover autostash while MERGE_HEAD is present or unreadable"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, isUndeterminedIndexState(tc.err))
		})
	}
}

// The end-to-end shape of the bug, driven through the real decision path in
// pullManagedRepo rather than the helper it calls — a helper-level test passes
// even when the classification is never wired into the failure branch.
//
// An unreadable index is the same class as the incident's timeout: the probe
// could not run, so nothing is known about the index. Reverting the
// classification turns every assertion below red.
func TestPullManagedRepo_UnreadableIndexDoesNotMintConflictIssue(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git index states")
	}
	repo := newProbeTestRepo(t, "notes.txt")
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".git", "index"), []byte("invalid index"), 0o644))

	result := newTestScheduler(t.TempDir()).pullManagedRepo(context.Background(), ManagedRepoPullOpts{
		RepoPath:     repo,
		RepoName:     "team_qdur30tb4b",
		ResolveRules: []manifest.ResolveRule{{Mode: manifest.ResolveModeAuto, Path: "data/"}},
		Logger:       discardLogger(),
	})

	assert.Nil(t, result.Issue, "an undetermined index state must not mint an issue of any type")
	assert.NoError(t, result.Err,
		"an undetermined index state must not count toward the consecutive-failure backoff")
}

// The other half: a probe that SUCCEEDED and found an unmerged index the
// resolver cannot repair must still reach the user as a confirm-gated merge
// conflict. Without this, the #962 fix would suppress real conflicts too.
func TestPullManagedRepo_GenuineUnmergedIndexStillMintsConflictIssue(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git index states")
	}
	repo := newProbeTestRepo(t, "notes.txt")
	writeUnmergedIndex(t, repo, "notes.txt")

	result := newTestScheduler(t.TempDir()).pullManagedRepo(context.Background(), ManagedRepoPullOpts{
		RepoPath:     repo,
		RepoName:     "ledger",
		ResolveRules: []manifest.ResolveRule{{Mode: manifest.ResolveModeAuto, Path: "data/"}},
		Logger:       discardLogger(),
	})

	require.NotNil(t, result.Issue)
	assert.Equal(t, IssueTypeMergeConflict, result.Issue.Type)
	assert.True(t, result.Issue.RequiresConfirm, "a real conflict is exactly what human gating is for")
	assert.Equal(t, "ledger", result.Issue.Repo)
	require.Error(t, result.Err)
	assert.ErrorContains(t, result.Err, "requires manual resolution")
}

// newProbeTestRepo returns a one-commit repo with no remote. These tests never
// reach fetch — autostash recovery runs first and returns before it — so the
// fixture deliberately skips the bare-remote scaffolding other tests need.
func newProbeTestRepo(t *testing.T, path string) string {
	t.Helper()
	repo := t.TempDir()
	out, err := runGitOut(t, repo, "init", "-b", "main", repo)
	require.NoError(t, err, out)
	require.NoError(t, os.WriteFile(filepath.Join(repo, path), []byte("base\n"), 0o644))
	out, err = runGitOut(t, repo, "add", path)
	require.NoError(t, err, out)
	out, err = runGitOut(t, repo, "commit", "-m", "base")
	require.NoError(t, err, out)
	return repo
}

// writeUnmergedIndex stages all three merge stages for path directly.
// update-index --index-info is the only way to produce the unmerged index
// `git ls-files --unmerged` reports without a remote, a rebase, or a network.
func writeUnmergedIndex(t *testing.T, repo, path string) {
	t.Helper()
	hash := func(content string) string {
		cmd := exec.Command("git", "hash-object", "-w", "--stdin") // safe: git in a temp dir
		cmd.Dir = repo
		cmd.Env = gitEnv()
		cmd.Stdin = strings.NewReader(content)
		out, err := cmd.Output()
		require.NoError(t, err)
		return strings.TrimSpace(string(out))
	}
	stages := fmt.Sprintf("0 %s\t%s\n100644 %s 1\t%s\n100644 %s 2\t%s\n100644 %s 3\t%s\n",
		strings.Repeat("0", 40), path,
		hash("base\n"), path, hash("ours\n"), path, hash("theirs\n"), path)

	cmd := exec.Command("git", "update-index", "--index-info") // safe: git in a temp dir
	cmd.Dir = repo
	cmd.Env = gitEnv()
	cmd.Stdin = strings.NewReader(stages)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}
