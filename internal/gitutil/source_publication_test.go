package gitutil

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

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
