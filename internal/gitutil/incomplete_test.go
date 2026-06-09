package gitutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initRepoWithCommits sets up a temp git repo with N commits and returns
// (path, tipSHA). Uses the same env-isolated pattern as other ox tests so
// it's safe under -p N parallel runs.
func initRepoWithCommits(t *testing.T, n int) (string, string) {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.name", "test")
	runGit(t, dir, "config", "user.email", "test@sageox.ai")
	for i := 1; i <= n; i++ {
		name := filepath.Join(dir, "f"+itoaTest(i)+".txt")
		require.NoError(t, os.WriteFile(name, []byte("v"+itoaTest(i)), 0o644))
		runGit(t, dir, "add", ".")
		runGit(t, dir, "commit", "-m", "c"+itoaTest(i))
	}
	out := captureGit(t, dir, "rev-parse", "HEAD")
	return dir, strings.TrimSpace(out)
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), // safe: git CLI in temp dir, not ox subprocess
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@sageox.ai",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@sageox.ai")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
}

func captureGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return string(out)
}

func itoaTest(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoaTest(n/10) + itoaTest(n%10)
}

func TestInspectRepo(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git clone operations")
	}

	tests := []struct {
		name       string
		setup      func(t *testing.T) string
		wantShall  bool
		wantPart   bool
		wantReason string
	}{
		{
			name: "normal_repo",
			setup: func(t *testing.T) string {
				dir, _ := initRepoWithCommits(t, 2)
				return dir
			},
		},
		{
			name: "shallow_clone",
			setup: func(t *testing.T) string {
				src, _ := initRepoWithCommits(t, 5)
				dst := filepath.Join(t.TempDir(), "shallow")
				// file:// + --no-local forces the dumb-protocol path that
				// honors --depth. A plain path clone hardlinks instead.
				runGit(t, t.TempDir(), "clone", "--depth", "1", "--no-local", "file://"+src, dst)
				return dst
			},
			wantShall:  true,
			wantReason: "shallow clone",
		},
		{
			name: "partial_blob_none",
			setup: func(t *testing.T) string {
				src, _ := initRepoWithCommits(t, 3)
				dst := filepath.Join(t.TempDir(), "partial")
				runGit(t, t.TempDir(), "clone", "--filter=blob:none", "--no-local", "file://"+src, dst)
				return dst
			},
			wantPart:   true,
			wantReason: "partial clone",
		},
		{
			name: "shallow_and_partial",
			setup: func(t *testing.T) string {
				src, _ := initRepoWithCommits(t, 5)
				dst := filepath.Join(t.TempDir(), "both")
				runGit(t, t.TempDir(), "clone", "--depth", "1", "--filter=blob:none", "--no-local", "file://"+src, dst)
				return dst
			},
			wantShall:  true,
			wantPart:   true,
			wantReason: "shallow + partial clone",
		},
		{
			name: "worktree_inherits_shallow",
			setup: func(t *testing.T) string {
				src, _ := initRepoWithCommits(t, 5)
				main := filepath.Join(t.TempDir(), "main")
				runGit(t, t.TempDir(), "clone", "--depth", "1", "--no-local", "file://"+src, main)
				wt := filepath.Join(t.TempDir(), "wt")
				runGit(t, main, "worktree", "add", "-b", "feat", wt)
				return wt
			},
			wantShall:  true,
			wantReason: "shallow clone",
		},
		{
			name: "not_a_repo",
			setup: func(t *testing.T) string {
				return t.TempDir()
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := tc.setup(t)
			state, err := InspectRepo(path)
			require.NoError(t, err)
			assert.Equal(t, tc.wantShall, state.Shallow, "Shallow")
			assert.Equal(t, tc.wantPart, state.Partial, "Partial")
			assert.Equal(t, tc.wantReason, state.Reason, "Reason")
			assert.Equal(t, tc.wantShall || tc.wantPart, state.Incomplete(), "Incomplete()")
		})
	}
}

func TestInspectRepo_EmptyPath(t *testing.T) {
	t.Parallel()
	_, err := InspectRepo("")
	assert.Error(t, err)
}

// TestDeepenUntilAncestor verifies the deepen-loop succeeds when the
// commit exists in upstream history but isn't reachable at the initial
// shallow depth.
func TestDeepenUntilAncestor(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git clone operations")
	}

	src, _ := initRepoWithCommits(t, 5)
	// We want to test that DeepenUntilAncestor can prove the OLD commit is
	// an ancestor of HEAD after deepening, starting from a shallow clone.
	// Capture an old commit SHA from src BEFORE cloning.
	oldSHA := strings.TrimSpace(captureGit(t, src, "rev-parse", "HEAD~3"))
	require.NotEmpty(t, oldSHA)

	dst := filepath.Join(t.TempDir(), "shallow")
	runGit(t, t.TempDir(), "clone", "--depth", "1", "--no-local", "file://"+src, dst)

	state, err := InspectRepo(dst)
	require.NoError(t, err)
	require.True(t, state.Shallow, "expected shallow clone setup")

	ctx := context.Background()
	ok, err := DeepenUntilAncestor(ctx, dst, oldSHA, "HEAD", 2, 4)
	require.NoError(t, err)
	assert.True(t, ok, "deepen loop should reveal old commit is ancestor of HEAD")
}
