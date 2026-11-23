package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- checkPRAttributionCoverage (ox-5r5v) ---
//
// Commit-level SageOx attribution is written by a deterministic git hook
// (hooks_commit_msg.go) — it cannot silently regress without a code change.
// PR-body attribution has no such backstop: it exists only as guidance an
// AI teammate is expected to remember and apply on every `gh pr
// create`/`gh pr edit`. checkPRAttributionCoverage is the only thing that
// catches a miss before merge, when the PR body becomes the permanent
// squash-merge record. These tests prove it actually catches that miss,
// doesn't false-positive on PRs with no SageOx involvement, and doesn't
// false-positive on branches with no open PR yet (the common case).

func runGitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
}

// --- A. branchHasSageOxCommitTrailer: pure git plumbing, no gh needed ---

// TestBranchHasSageOxCommitTrailer_DetectsPresenceAndAbsence proves the
// range-based trailer scan correctly distinguishes "no commit ahead of
// base carries the trailer" from "one does", using a real branch history.
// Failure prevented: a scan that greps the wrong range (e.g. all of HEAD's
// history instead of just what's ahead of base) would misreport every
// branch as attributed, or a scan that never matches would never fire.
func TestBranchHasSageOxCommitTrailer_DetectsPresenceAndAbsence(t *testing.T) {
	dir := t.TempDir()
	runGitCmd(t, dir, "init", "-b", "main")
	runGitCmd(t, dir, "config", "user.email", "test@test.sageox.ai")
	runGitCmd(t, dir, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base"), 0o644))
	runGitCmd(t, dir, "add", "-A")
	runGitCmd(t, dir, "commit", "-m", "base commit")
	runGitCmd(t, dir, "checkout", "-b", "feature")

	const trailer = "Co-Authored-By: SageOx <ox@sageox.ai>"

	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644))
	runGitCmd(t, dir, "add", "-A")
	runGitCmd(t, dir, "commit", "-m", "add a, no trailer")

	has, err := branchHasSageOxCommitTrailer(dir, "main", trailer)
	require.NoError(t, err)
	assert.False(t, has, "no commit yet carries the trailer")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0o644))
	runGitCmd(t, dir, "add", "-A")
	runGitCmd(t, dir, "commit", "-m", "add b\n\n"+trailer)

	has, err = branchHasSageOxCommitTrailer(dir, "main", trailer)
	require.NoError(t, err)
	assert.True(t, has, "a commit ahead of base now carries the trailer")
}

// --- B. checkPRAttributionCoverage: full dispatch, gh faked ---

// fakeGh installs a fake `gh` executable on PATH that unconditionally
// prints jsonOutput to stdout regardless of arguments. Tests the
// parsing/decision logic in checkPRAttributionCoverage without touching
// the network or a real GitHub repo.
func fakeGh(t *testing.T, jsonOutput string) {
	t.Helper()
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "response.json")
	require.NoError(t, os.WriteFile(jsonPath, []byte(jsonOutput), 0o644))
	scriptPath := filepath.Join(dir, "gh")
	script := "#!/bin/sh\ncat " + jsonPath + "\n"
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// setupPRAttributionFixture builds a real initialized project + git repo on
// a "feature" branch one commit ahead of "main", and fakes `gh pr list` to
// return a single open PR (#42, base "main") with the given body. When
// withSageOxCommit is false, the feature commit carries no trailer at all
// (simulating a PR with no SageOx involvement).
func setupPRAttributionFixture(t *testing.T, prBody string, withSageOxCommit bool) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	dir := createInitializedProjectWithConfig(t, &config.ProjectConfig{
		ProjectID:   "test_project",
		WorkspaceID: "test_workspace",
	})

	runGitCmd(t, dir, "init", "-b", "main")
	runGitCmd(t, dir, "config", "user.email", "test@test.sageox.ai")
	runGitCmd(t, dir, "config", "user.name", "Test")
	runGitCmd(t, dir, "add", "-A")
	runGitCmd(t, dir, "commit", "-m", "init project")

	runGitCmd(t, dir, "checkout", "-b", "feature")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "work.txt"), []byte("work"), 0o644))
	runGitCmd(t, dir, "add", "-A")
	msg := "do the work"
	if withSageOxCommit {
		msg += "\n\nCo-Authored-By: SageOx <ox@sageox.ai>"
	}
	runGitCmd(t, dir, "commit", "-m", msg)

	prJSON := fmt.Sprintf(`[{"number": 42, "baseRefName": "main", "body": %q}]`, prBody)
	fakeGh(t, prJSON)

	origDir, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	require.NoError(t, os.Chdir(dir))

	return dir
}

// TestCheckPRAttributionCoverage_WarnsWhenBodyMissingTrailer is the actual
// bug this check exists to catch: a PR with a SageOx-attributed commit
// whose body has no attribution trailer at all — exactly what happened on
// PR #703 in production.
func TestCheckPRAttributionCoverage_WarnsWhenBodyMissingTrailer(t *testing.T) {
	setupPRAttributionFixture(t, "Just a plain PR description, no attribution anywhere.", true)

	result := checkPRAttributionCoverage()

	assert.True(t, result.warning, "must be flagged as a warning")
	assert.False(t, result.skipped)
	assert.Contains(t, result.message, "#42")
	assert.Contains(t, result.message, "missing the trailer")
}

// TestCheckPRAttributionCoverage_PassesWhenBodyHasTrailer proves a
// correctly-attributed PR body doesn't get flagged.
func TestCheckPRAttributionCoverage_PassesWhenBodyHasTrailer(t *testing.T) {
	setupPRAttributionFixture(t, "Some PR body.\n\nCo-Authored-By: [SageOx](https://github.com/SageOx)", true)

	result := checkPRAttributionCoverage()

	assert.False(t, result.warning)
	assert.False(t, result.skipped)
	assert.Contains(t, result.message, "#42")
}

// TestCheckPRAttributionCoverage_PassesWhenNoSageOxCommits proves a PR with
// zero SageOx-attributed commits is never flagged, regardless of its body —
// there's nothing to attribute.
func TestCheckPRAttributionCoverage_PassesWhenNoSageOxCommits(t *testing.T) {
	setupPRAttributionFixture(t, "A PR with no SageOx involvement at all.", false)

	result := checkPRAttributionCoverage()

	assert.False(t, result.warning)
	assert.False(t, result.skipped)
	assert.Contains(t, result.message, "no commits")
}

// TestCheckPRAttributionCoverage_SkipsWhenNoOpenPR proves the common case
// (a branch with no PR opened yet) is a silent skip, not a false warning.
func TestCheckPRAttributionCoverage_SkipsWhenNoOpenPR(t *testing.T) {
	dir := setupPRAttributionFixture(t, "unused", true)
	fakeGh(t, `[]`) // overwrite: gh reports no open PR for this branch
	require.NoError(t, os.Chdir(dir))

	result := checkPRAttributionCoverage()

	assert.True(t, result.skipped)
	assert.False(t, result.warning)
}
