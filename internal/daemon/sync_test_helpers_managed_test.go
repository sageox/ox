package daemon

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// initBareRepo creates a bare repo with partial-clone support enabled and
// a working clone next to it. Returns both paths. The caller writes
// files into workDir, commits, and pushes to populate the bare. This is
// the shared bootstrap step duplicated across kb and team-context test
// helpers; consolidating it here keeps the bare-repo configuration
// (especially uploadpack.allowfilter) consistent so tests asserting
// shallow / partial-clone behavior stay accurate as the repo recipe
// evolves.
//
// Use directly when writing new tests that don't fit makeBareRepo (one
// file at root) or setupTeamContextBareRepo (team-context bootstrap)
// shapes — e.g., the sparse-checkout drift regression test that needs
// multiple directories committed.
func initBareRepo(t *testing.T, name string) (bareDir, workDir string) {
	t.Helper()
	tmp := t.TempDir()
	bareDir = filepath.Join(tmp, name+".bare")
	workDir = filepath.Join(tmp, name+".work")
	require.NoError(t, exec.Command("git", "init", "--bare", "-b", "main", bareDir).Run())
	// uploadpack.allowfilter is required for TwoPhaseClone's --filter=blob:none
	// to be honored; without it git silently downgrades to a full clone and
	// any partial-filter assertion in the test passes vacuously.
	require.NoError(t, exec.Command("git", "-C", bareDir, "config", "uploadpack.allowfilter", "true").Run())
	require.NoError(t, exec.Command("git", "clone", bareDir, workDir).Run())
	gitConfig(t, workDir)
	return bareDir, workDir
}
