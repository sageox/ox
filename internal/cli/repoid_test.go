package cli

import (
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/useragent"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newProjectWithRepoID creates an initialized project whose config carries
// repoID, and points ox at it for the duration of the test.
func newProjectWithRepoID(t *testing.T, repoID string) string {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, config.SaveProjectConfig(dir, &config.ProjectConfig{RepoID: repoID}))
	t.Setenv(config.EnvProjectRoot, dir)
	// Keep the bootstrap offline: NewContext starts a telemetry client.
	t.Setenv("DO_NOT_TRACK", "1")
	return dir
}

// TestNewContext_TagsRequestsWithRepoID is the wiring regression test for the
// cli_activity.repo_id gap. The server can only derive a repo from routes
// whose URL embeds one, so unless the CLI attaches the repo it is working in,
// per-repo activity analytics see only repo-detail fetches.
//
// Failure prevented: the bootstrap silently not tagging requests, which sends
// repo_id straight back to NULL on every route but GET /cli/repos/{repo_id}.
func TestNewContext_TagsRequestsWithRepoID(t *testing.T) {
	useragent.ResetForTesting()
	t.Cleanup(useragent.ResetForTesting)

	newProjectWithRepoID(t, "repo_01hxyz9abc")

	cmd := &cobra.Command{Use: "status"}
	_, err := NewContext(cmd, nil)
	require.NoError(t, err)

	assert.Equal(t, "repo_01hxyz9abc", useragent.RepoID())
}

// TestNewContext_DaemonIsNotTaggedWithRepoID is the more important half.
//
// The daemon syncs every workspace on the machine from one process, so a
// process-global repo ID would stamp all of its traffic with whichever repo it
// happened to start in. That is worse than the bug being fixed: NULL is
// missing data, but a wrong repo ID is data that looks right and is not.
//
// Failure prevented: someone hoisting SetRepoID above the service-process
// guard "so the daemon gets attribution too".
func TestNewContext_DaemonIsNotTaggedWithRepoID(t *testing.T) {
	useragent.ResetForTesting()
	t.Cleanup(useragent.ResetForTesting)

	newProjectWithRepoID(t, "repo_01hxyz9abc")

	cmd := &cobra.Command{
		Use:         "start",
		Annotations: map[string]string{AnnotationLongRunning: "flag:foreground"},
	}
	cmd.Flags().Bool("foreground", false, "run in foreground")
	require.NoError(t, cmd.ParseFlags([]string{"--foreground"}))

	ctx, err := NewContext(cmd, nil)
	require.NoError(t, err)
	require.True(t, ctx.LongRunning, "daemon invocation must classify as a service")

	assert.Empty(t, useragent.RepoID(),
		"the daemon serves many repos — tagging its traffic with one is worse than tagging it with none")
}

// TestNewContext_NoProjectLeavesRepoIDUnset covers running ox outside a
// SageOx project (ox login, ox init before it writes config). There is no
// repo, so no claim should be made.
func TestNewContext_NoProjectLeavesRepoIDUnset(t *testing.T) {
	useragent.ResetForTesting()
	t.Cleanup(useragent.ResetForTesting)

	// Run from a directory with no .sageox anywhere above it. The env
	// override is only honored for an *initialized* root, so pointing it at a
	// bare temp dir is not enough — FindProjectRoot falls back to walking up
	// from cwd, which under `go test` is inside the real ox checkout and
	// would hand back this repo's own ID.
	t.Chdir(t.TempDir())
	t.Setenv(config.EnvProjectRoot, "")
	t.Setenv("DO_NOT_TRACK", "1")

	cmd := &cobra.Command{Use: "login"}
	_, err := NewContext(cmd, nil)
	require.NoError(t, err)

	assert.Empty(t, useragent.RepoID())
}
