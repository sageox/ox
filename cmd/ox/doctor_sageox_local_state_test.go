//go:build !short

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/doctor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const recoveryMarkerRel = ".sageox/.session-recovery.json"

// newMarkerTestRepo returns a scratch repo that is the cwd, so cwd-bound doctor checks never touch a real checkout.
func newMarkerTestRepo(t *testing.T) string {
	t.Helper()
	gitRoot, cleanup := setupTempGitRepo(t)
	t.Cleanup(cleanup)
	t.Cleanup(changeToDir(t, gitRoot))
	want, err := filepath.EvalSymlinks(gitRoot)
	require.NoError(t, err)
	got, err := filepath.EvalSymlinks(findGitRoot())
	require.NoError(t, err)
	require.Equal(t, want, got, "doctor checks must resolve to the scratch repo")
	requireSageoxDir(t, gitRoot)
	return gitRoot
}

func markerGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git %s: %s", strings.Join(args, " "), out)
	return string(out)
}

// writeRecoveryMarker uses the same writer `ox agent <id> session stop` uses when session discovery fails.
func writeRecoveryMarker(t *testing.T, gitRoot string) {
	t.Helper()
	require.NoError(t, doctor.SetSessionRecoveryInfo(gitRoot, doctor.SessionRecoveryInfo{
		AgentID:       "OxTest1",
		AdapterName:   "claude",
		StartedAt:     time.Now().Add(-time.Hour),
		WorkspacePath: gitRoot,
		FailedAt:      time.Now(),
		Error:         "session file not found at stop time",
	}))
	require.FileExists(t, filepath.Join(gitRoot, recoveryMarkerRel))
}

// TestSessionRecoveryMarker_NotStagedOrNudgedByDoctor: without it, a plain `ox doctor`
// force-stages a per-machine file holding absolute local paths and says "Run 'git commit'" (GH #1062).
func TestSessionRecoveryMarker_NotStagedOrNudgedByDoctor(t *testing.T) {
	gitRoot := newMarkerTestRepo(t)
	require.NoError(t, createSageoxGitignore(filepath.Join(gitRoot, ".sageox", ".gitignore")))
	require.NoError(t, os.WriteFile(filepath.Join(gitRoot, ".sageox", "config.json"), []byte("{}\n"), 0o644))
	markerGit(t, gitRoot, "add", ".sageox")
	markerGit(t, gitRoot, "commit", "-q", "-m", "init sageox")

	writeRecoveryMarker(t, gitRoot)
	assert.True(t, gitPathIsIgnored(gitRoot, recoveryMarkerRel), "marker must be ignored by .sageox/.gitignore")

	// plain `ox doctor` (no --fix) runs this check with fix=true
	checkSageoxFilesTracked(doctorOptions{}.shouldFix(CheckSlugGitignore))

	assert.Empty(t, markerGit(t, gitRoot, "status", "--porcelain", "--untracked-files=all"), "doctor must not stage the marker")
	assert.Equal(t, "committed and up to date", checkGitRepoState().message)
}

// TestSessionRecoveryMarker_AlreadyStagedIsFlaggedNotNudgedToCommit: without it, a marker
// staged before the fix keeps getting "Run 'git commit' to persist config" (GH #1062).
func TestSessionRecoveryMarker_AlreadyStagedIsFlaggedNotNudgedToCommit(t *testing.T) {
	gitRoot := newMarkerTestRepo(t)
	require.NoError(t, createSageoxGitignore(filepath.Join(gitRoot, ".sageox", ".gitignore")))
	markerGit(t, gitRoot, "add", ".sageox")
	markerGit(t, gitRoot, "commit", "-q", "-m", "init sageox")

	writeRecoveryMarker(t, gitRoot)
	markerGit(t, gitRoot, "add", "-f", recoveryMarkerRel)

	result := checkGitRepoState()
	assert.True(t, result.warning, "an already-staged marker must be a warning, not info")
	assert.NotContains(t, result.detail, "git commit")
	assert.Contains(t, result.detail, "git rm --cached "+recoveryMarkerRel)
}

// TestSessionRecoveryMarker_IgnoredAfterDoctorUpgradesOldGitignore: without it, repos whose
// .sageox/.gitignore predates the rule keep leaking the marker until someone re-inits.
func TestSessionRecoveryMarker_IgnoredAfterDoctorUpgradesOldGitignore(t *testing.T) {
	gitRoot := newMarkerTestRepo(t)
	gitignorePath := filepath.Join(gitRoot, ".sageox", ".gitignore")
	legacy := strings.ReplaceAll(sageoxGitignoreContent, ".session-recovery.json\n", "")
	require.NoError(t, os.WriteFile(gitignorePath, []byte(legacy), 0o644))
	markerGit(t, gitRoot, "add", ".sageox")
	markerGit(t, gitRoot, "commit", "-q", "-m", "old sageox gitignore")

	writeRecoveryMarker(t, gitRoot)

	// plain `ox doctor` (no --fix) auto-merges required entries
	checkSageoxGitignore(doctorOptions{}.shouldFix(CheckSlugSageoxGitignore))

	assert.True(t, gitPathIsIgnored(gitRoot, recoveryMarkerRel), "doctor must add the marker rule to an existing .sageox/.gitignore")
	assert.NotContains(t, markerGit(t, gitRoot, "status", "--porcelain", "--untracked-files=all"), recoveryMarkerRel)
}
