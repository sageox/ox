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
	gitInDir(t, bareDir, "config", "uploadpack.allowfilter", "true")
	require.NoError(t, exec.Command("git", "clone", bareDir, workDir).Run())
	gitConfig(t, workDir)
	return bareDir, workDir
}

// gitInDir runs `git <args...>` with cmd.Dir set to dir. Preferred over
// `exec.Command("git", "-C", dir, args...)` per the project test guideline
// (cmd.Dir is the Go-idiomatic way to set the working directory and avoids
// a global-state quirk where some git versions interpret -C differently
// when chained with other flags). Fails the test on non-zero exit.
func gitInDir(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed in %s: %s", args, dir, out)
}
