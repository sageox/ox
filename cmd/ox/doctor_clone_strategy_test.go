package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: reporting every ox clone as a full clone forever. This
// check read `extensions.partialClone`, which git 2.50 does not write for
// `clone --filter` (it records remote.<name>.promisor instead), so it told
// users "full clone (not partial) — will be upgraded on next reclone" about an
// already-partial clone, an upgrade that could never register as done.
func TestTeamContextCloneStrategyResults(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	src := filepath.Join(root, "src")
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	require.NoError(t, os.MkdirAll(src, 0o755))
	run(root, "init", "-q", "--bare", "--initial-branch=main", origin)
	run(root, "-C", origin, "config", "uploadpack.allowFilter", "true")
	run(src, "init", "-q", "--initial-branch=main")
	run(src, "config", "user.name", "Test")
	run(src, "config", "user.email", "clone-strategy-test@test.sageox.ai")
	run(src, "config", "commit.gpgsign", "false")
	require.NoError(t, os.WriteFile(filepath.Join(src, "AGENTS.md"), []byte("team\n"), 0o600))
	run(src, "add", "AGENTS.md")
	run(src, "commit", "-qm", "seed")
	run(src, "remote", "add", "origin", origin)
	run(src, "push", "-q", "origin", "main")

	partial := filepath.Join(root, "partial")
	full := filepath.Join(root, "full")
	run(root, "clone", "-q", "--filter=blob:none", origin, partial)
	run(root, "clone", "-q", origin, full)

	results := teamContextCloneStrategyResults([]config.TeamContext{
		{TeamName: "Partial", Path: partial},
		{TeamName: "Full", Path: full},
		// Skipped: no path, and a path that is not a git repo.
		{TeamName: "NoPath"},
		{TeamName: "NotARepo", Path: root},
	})

	require.Len(t, results, 2, "only real git repos are reported")

	byName := map[string]checkResult{}
	for _, r := range results {
		byName[r.name] = r
	}

	p, ok := byName["Team Partial clone strategy"]
	require.True(t, ok)
	assert.Equal(t, "partial clone", p.message,
		"a --filter clone must be recognized as partial")
	assert.True(t, p.passed, "a partial clone is not a finding")

	f, ok := byName["Team Full clone strategy"]
	require.True(t, ok)
	assert.Equal(t, "full clone (not partial)", f.message,
		"a genuine full clone must still be reported as full")
}
