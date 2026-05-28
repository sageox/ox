package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetGitRepoStatus_ShallowSetsIncompleteHistory verifies the end-to-end
// flow from a shallow user repo through getGitRepoStatus to the GitRepoStatus
// struct: IncompleteHistory must be set, IncompleteReason populated, and
// ahead/behind counts left at zero (not falsely computed).
//
// Failure prevented: ox status quietly displaying "0 ahead, 0 behind" or
// inflated divergence numbers in CI environments where the user repo was
// shallow-cloned by actions/checkout. The plumbing through status.go must
// route shallow detection into the struct, not just internal helpers.
func TestGetGitRepoStatus_ShallowSetsIncompleteHistory(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}

	src := t.TempDir()
	runGitOrFail(t, src, "init", "-q", "-b", "main")
	runGitOrFail(t, src, "config", "user.name", "test")
	runGitOrFail(t, src, "config", "user.email", "test@sageox.ai")
	for i := 0; i < 4; i++ {
		require.NoError(t, os.WriteFile(filepath.Join(src, "f"+string(rune('a'+i))+".txt"), []byte("v"), 0o644))
		runGitOrFail(t, src, "add", ".")
		runGitOrFail(t, src, "commit", "-q", "-m", "c")
	}

	parent := t.TempDir()
	dst := filepath.Join(parent, "shallow")
	clone := exec.Command("git", "clone", "-q", "--depth", "1", "--no-local", "file://"+src, dst)
	out, err := clone.CombinedOutput()
	require.NoError(t, err, "%s", out)

	status := getGitRepoStatus(dst, time.Time{}, false)
	assert.True(t, status.IncompleteHistory,
		"shallow user repo must set IncompleteHistory; got %+v", status)
	assert.Equal(t, "shallow clone", status.IncompleteReason)
	assert.Equal(t, 0, status.AheadCount, "shallow repo must not report a confident AheadCount")
	assert.Equal(t, 0, status.BehindCount, "shallow repo must not report a confident BehindCount")
	// Not wedged: divergence-based wedge needs both counts > 0; rebase
	// detection still works independently.
	assert.False(t, status.IsWedged(), "shallow alone is not a wedge")
}

// TestGetGitRepoStatus_PartialClonePreservesDivergence verifies that
// `--filter=blob:none` partial clones — which keep the commit graph intact
// — still compute ahead/behind correctly. The Partial flag is informational
// only; suppressing divergence on Partial would be a regression.
func TestGetGitRepoStatus_PartialClonePreservesDivergence(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}

	src := t.TempDir()
	runGitOrFail(t, src, "init", "-q", "-b", "main")
	runGitOrFail(t, src, "config", "user.name", "test")
	runGitOrFail(t, src, "config", "user.email", "test@sageox.ai")
	require.NoError(t, os.WriteFile(filepath.Join(src, "f.txt"), []byte("v"), 0o644))
	runGitOrFail(t, src, "add", ".")
	runGitOrFail(t, src, "commit", "-q", "-m", "c")

	parent := t.TempDir()
	dst := filepath.Join(parent, "partial")
	clone := exec.Command("git", "clone", "-q", "--filter=blob:none", "--no-local", "file://"+src, dst)
	out, err := clone.CombinedOutput()
	require.NoError(t, err, "%s", out)

	status := getGitRepoStatus(dst, time.Time{}, false)
	assert.False(t, status.IncompleteHistory,
		"partial clone keeps commit graph; divergence is still computable")
}

func runGitOrFail(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), // safe: git CLI in temp dir, not ox subprocess
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@sageox.ai",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@sageox.ai")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
}
