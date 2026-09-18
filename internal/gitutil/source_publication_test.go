package gitutil

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRefuseSourcePublicationRebaseAllowsPullWithoutUpstream prevents the
// refusal from turning a repo with no configured upstream into a repo that can
// never pull. `git diff @{upstream}...HEAD` exits 128 there, and treating that
// as "publication pending" blocks every managed clone in that state
// permanently -- including team-context repos, which hold no sessions at all.
// Failure prevented: daemon sync wedged for the lifetime of the clone.
func TestRefuseSourcePublicationRebaseAllowsPullWithoutUpstream(t *testing.T) {
	repo := t.TempDir()
	run(t, repo, "git", "init", "--quiet", "--initial-branch=main")
	run(t, repo, "git", "config", "user.email", "test@test.local")
	run(t, repo, "git", "config", "user.name", "Test")
	run(t, repo, "git", "config", "commit.gpgsign", "false")
	addCommit(t, repo, "AGENTS.md", "team\n", "seed")
	// A remote exists but no branch tracks it -- the state the fixture in
	// internal/daemon's post-pull suspension test reproduces.
	run(t, repo, "git", "remote", "add", "origin", "https://127.0.0.1:1/x.git")

	_, err := RunGit(context.Background(), repo, "diff", "--name-only", "@{upstream}...HEAD")
	require.Error(t, err, "fixture must actually lack an upstream, or this test proves nothing")

	require.NoError(t, RefuseSourcePublicationRebase(context.Background(), repo),
		"no upstream means nothing is pending against it; refusing would wedge the pull forever")
}

// TestRefuseSourcePublicationRebaseErrorsWhenUpstreamConfiguredButUnresolvable
// pins the other half of the no-upstream escape hatch. A branch that HAS
// branch.*.remote/branch.*.merge but whose tracking ref cannot be resolved
// (never fetched, refs pruned) fails `rev-parse @{upstream}` with the same
// exit status as having no upstream at all. Treating it as "nothing to
// compare" would silently skip this entire refusal and let a pending sourced
// session be auto-rebased.
// Failure prevented: source-bearing sessions rebased without reconciliation.
func TestRefuseSourcePublicationRebaseErrorsWhenUpstreamConfiguredButUnresolvable(t *testing.T) {
	repo := t.TempDir()
	run(t, repo, "git", "init", "--quiet", "--initial-branch=main")
	run(t, repo, "git", "config", "user.email", "test@test.local")
	run(t, repo, "git", "config", "user.name", "Test")
	run(t, repo, "git", "config", "commit.gpgsign", "false")
	addCommit(t, repo, "AGENTS.md", "team\n", "seed")
	run(t, repo, "git", "remote", "add", "origin", "https://127.0.0.1:1/x.git")
	// Upstream configured, but origin/main was never fetched, so the tracking
	// ref does not exist locally.
	run(t, repo, "git", "config", "branch.main.remote", "origin")
	run(t, repo, "git", "config", "branch.main.merge", "refs/heads/main")

	err := RefuseSourcePublicationRebase(context.Background(), repo)
	require.Error(t, err,
		"a configured upstream that cannot be resolved must propagate, not silently skip the refusal")
	require.ErrorContains(t, err, "inspect pending source publication")
}

func TestSourcePublicationDoesNotRebaseAfterRemoteDeletion(t *testing.T) {
	repo, remote := initBareRemoteRepo(t)
	dir := filepath.Join(repo, "sessions", "test")
	require.NoError(t, os.MkdirAll(dir, 0700))
	addCommit(t, repo, "sessions/test/meta.json", `{"source":{"native_session_id":"native"}}`, "publish")
	run(t, repo, "git", "push", "--quiet")
	other := filepath.Join(t.TempDir(), "other")
	run(t, "", "git", "clone", "--quiet", remote, other)
	run(t, other, "git", "config", "user.email", "test@test.local")
	run(t, other, "git", "config", "user.name", "Test")
	// Only the derived file changes locally; the existing source receipt has
	// no delta. Checking source-record paths alone would miss this resurrection.
	addCommit(t, repo, "sessions/test/summary.json", `{}`, "summary")
	run(t, other, "git", "rm", "-r", "sessions/test")
	run(t, other, "git", "commit", "-m", "delete", "--no-verify", "--quiet")
	run(t, other, "git", "push", "--quiet")
	before, err := RunGit(context.Background(), repo, "rev-parse", "HEAD")
	require.NoError(t, err)
	require.ErrorContains(t, PushWithRetry(context.Background(), repo, PushOpts{}), "automatic rebase is disabled")
	after, err := RunGit(context.Background(), repo, "rev-parse", "HEAD")
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.ErrorContains(t, CheckSourcePublication(context.Background(), repo), "remote advanced")
	_, err = RunGit(context.Background(), remote, "show", "HEAD:sessions/test/summary.json")
	require.Error(t, err, "pending summary must never recreate deleted remote content")
}
