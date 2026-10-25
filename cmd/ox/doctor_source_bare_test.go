package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runGitForBareTest runs git in dir with a deterministic identity.
func runGitForBareTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), // safe: git CLI in a temp dir needs inherited PATH
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@sageox.ai",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@sageox.ai",
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// newProjectRepo builds an ox-initialized git checkout (a .git repo + a minimal
// .sageox/config.json so config.IsInitialized is satisfied). If bare != "",
// core.bare is set to it — "true" reproduces the #819 wedged state.
func newProjectRepo(t *testing.T, bare string) string {
	t.Helper()
	root := t.TempDir()
	runGitForBareTest(t, root, "init")
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".sageox"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".sageox", "config.json"), []byte("{}\n"), 0o644))
	if bare != "" {
		runGitForBareTest(t, root, "config", "core.bare", bare)
	}
	return root
}

// TestRepairSourceRepoBare_RepairsFlippedCoreBare is the #819 safety net: a
// managed checkout whose core.bare got flipped to true must be auto-repaired
// back to false so work-tree git commands recover. Fails if the repair is
// removed (core.bare stays true).
func TestRepairSourceRepoBare_RepairsFlippedCoreBare(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git config ops")
	}
	root := newProjectRepo(t, "true")

	res := repairSourceRepoBare(root)

	assert.True(t, res.warning, "a repaired corruption must surface as a warning")
	assert.Contains(t, res.message, "repaired")

	got, set := gitConfigBoolAt(root, "core.bare")
	require.True(t, set, "core.bare should still be set (to false)")
	assert.False(t, got, "core.bare must be repaired to false")
}

// TestRepairSourceRepoBare_LeavesHealthyRepoAlone guards against false
// positives: a normal checkout (core.bare unset) must pass untouched.
func TestRepairSourceRepoBare_LeavesHealthyRepoAlone(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git config ops")
	}
	root := newProjectRepo(t, "") // core.bare not set

	res := repairSourceRepoBare(root)

	assert.True(t, res.passed && !res.warning, "a healthy checkout must pass cleanly")
	// git init writes core.bare=false explicitly; the check must leave it alone.
	got, set := gitConfigBoolAt(root, "core.bare")
	require.True(t, set, "git init sets core.bare")
	assert.False(t, got, "healthy repo's core.bare must remain false (untouched)")
}

// TestRepairSourceRepoBare_SkipsNonProject: a git repo with no .sageox is not
// ours to touch — even if its core.bare is true, we must not modify it.
func TestRepairSourceRepoBare_SkipsNonProject(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git config ops")
	}
	root := t.TempDir()
	runGitForBareTest(t, root, "init")
	runGitForBareTest(t, root, "config", "core.bare", "true")

	res := repairSourceRepoBare(root)

	assert.True(t, res.skipped, "non-ox repo must be skipped")
	got, _ := gitConfigBoolAt(root, "core.bare")
	assert.True(t, got, "non-ox repo's core.bare must NOT be touched")
}
