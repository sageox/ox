package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// --- Trailer-ratio scan ---

// TestScanTrailerRatio_CountsBothURLForms verifies the doctor's session-link
// coverage signal counts the trailer by KEY, so legacy name-based URLs and
// the /c/ universal form both register during the migration era.
// Failure prevented: a "tighten the match to the URL shape" refactor zeroing
// the ratio for /c/ commits and warning on every healthy repo (or silently
// uncounting legacy history).
func TestScanTrailerRatio_CountsBothURLForms(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test User")

	commit := func(n, message string) {
		t.Helper()
		require.NoError(t, os.WriteFile(filepath.Join(dir, n), []byte(n), 0o644))
		run("add", n)
		run("commit", "-q", "-m", message)
	}

	commit("a.txt", "feat: legacy form\n\nSageOx-Session: https://sageox.ai/repo/repo_01x/sessions/2026-01-01T00-00-user-OxA1/view")
	commit("b.txt", "feat: universal form\n\nSageOx-Session: https://sageox.ai/c/ses_01890a5d-ac96-774b-bcce-b302099a8057")
	commit("c.txt", "feat: no trailer")

	total, with, err := scanTrailerRatio(dir, 50)
	require.NoError(t, err)
	require.Equal(t, 3, total)
	require.Equal(t, 2, with, "both URL forms must count toward the ratio")
}

func TestScanTrailerRatio_ExcludesMergeCommits(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test User")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base"), 0o644))
	run("add", "base.txt")
	run("commit", "-q", "-m", "base")
	run("checkout", "-q", "-b", "feature")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("feature"), 0o644))
	run("add", "feature.txt")
	run("commit", "-q", "-m", "feature\n\nSageOx-Session: https://sageox.ai/c/ses_test")
	run("checkout", "-q", "master")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.txt"), []byte("main"), 0o644))
	run("add", "main.txt")
	run("commit", "-q", "-m", "main")
	run("merge", "--no-ff", "feature", "-m", "merge feature")

	total, with, err := scanTrailerRatio(dir, 50)
	require.NoError(t, err)
	require.Equal(t, 3, total)
	require.Equal(t, 1, with)
}
