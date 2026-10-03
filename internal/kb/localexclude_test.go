package kb

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initRealRepo creates a real git repo with a committed .sageox/sync.manifest
// so `git check-ignore` has an index to consult.
func initRealRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run("init", "--initial-branch=main")
	run("config", "user.name", "test")
	run("config", "user.email", "test@test.com")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".sageox"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".sageox", "sync.manifest"), []byte("version 1\ninclude .sageox/\n"), 0o644))
	run("add", ".sageox/sync.manifest")
	run("commit", "-m", "seed")
	return dir
}

// checkIgnored runs `git check-ignore -q` and reports whether git would
// ignore rel. Uses git itself rather than string-matching the rule file so
// the assertion is about behavior, not spelling.
func checkIgnored(t *testing.T, dir, rel string) bool {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "check-ignore", "-q", "--no-index", rel)
	err := cmd.Run()
	if err == nil {
		return true
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false
	}
	t.Fatalf("git check-ignore %s: %v", rel, err)
	return false
}

// TestEnsureLocalExcludes_CuratorPathsStayVisible is the customer promise:
// after the daemon installs its ignore rules, a Curator artifact under
// .sageox/curator/ is NOT ignored, while the daemon's own meta.json and
// cache/ are. Failure prevented: the server Curator's `git add -A` skipping
// its save-mark and re-driving synthesis every hour.
func TestEnsureLocalExcludes_CuratorPathsStayVisible(t *testing.T) {
	dir := initRealRepo(t)

	changed, err := EnsureLocalExcludes(dir)
	require.NoError(t, err)
	assert.True(t, changed, "first call must write the block")

	// never a committed ignore file
	_, statErr := os.Stat(filepath.Join(dir, ".sageox", ".gitignore"))
	assert.True(t, os.IsNotExist(statErr), ".sageox/.gitignore must not be created in a bubble checkout")

	assert.False(t, checkIgnored(t, dir, ".sageox/curator/marks/x.json"), "curator marks must not be ignored")
	assert.False(t, checkIgnored(t, dir, ".sageox/curator/synopses/y.md"), "curator synopses must not be ignored")
	assert.False(t, checkIgnored(t, dir, ".sageox/sync.manifest"), "sync.manifest must not be ignored")

	assert.True(t, checkIgnored(t, dir, ".sageox/meta.json"), "daemon meta.json must be ignored")
	assert.True(t, checkIgnored(t, dir, ".sageox/meta.json.tmp-123"), "meta.json temp file must be ignored")
	assert.True(t, checkIgnored(t, dir, ".sageox/cache/sync-state.json"), "cache/ must be ignored")

	// git status stays clean once the daemon writes its files
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".sageox", "meta.json"), []byte("{}"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".sageox", "cache"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".sageox", "cache", "x"), []byte("x"), 0o644))
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(string(out)), "daemon-written files must not appear in git status")

	// and a curator file DOES show up — i.e. nothing hides it
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".sageox", "curator", "marks"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".sageox", "curator", "marks", "a.json"), []byte("{}"), 0o644))
	out, err = exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	require.NoError(t, err)
	assert.Contains(t, string(out), ".sageox/curator/", "curator artifact must be visible to git")
}

// TestEnsureLocalExcludes is table-driven over the read-modify-write
// scenarios of the exclude file itself.
func TestEnsureLocalExcludes(t *testing.T) {
	cases := []struct {
		name             string
		failurePrevented string
		existing         string
		wantChanged      bool
		mustContain      []string
		mustNotContain   []string
	}{
		{
			name:             "creates_file_when_missing",
			failurePrevented: "fresh bubble clone shows meta.json as untracked forever",
			wantChanged:      true,
			mustContain:      append([]string{localExcludeHeader, localExcludeFooter}, LocalExcludePatterns...),
		},
		{
			name:             "preserves_user_content",
			failurePrevented: "a hand-added exclude rule is clobbered by the daemon",
			existing:         "my-local-scratch/\n",
			wantChanged:      true,
			mustContain:      []string{"my-local-scratch/\n", localExcludeHeader},
		},
		{
			name:             "replaces_stale_block",
			failurePrevented: "an older ox's rule set lingers next to the new one",
			existing:         "keep-me\n" + localExcludeHeader + "\n/.sageox/old-rule\n" + localExcludeFooter + "\ntail\n",
			wantChanged:      true,
			mustContain:      []string{"keep-me\n", "tail\n", "/.sageox/meta.json"},
			mustNotContain:   []string{"/.sageox/old-rule"},
		},
		{
			name:             "idempotent_when_current",
			failurePrevented: "every sync pass rewrites the file and churns mtime",
			existing:         renderLocalExcludeBlock(),
			wantChanged:      false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := mkGitDir(t)
			full := filepath.Join(dir, localExcludeRelPath)
			if tc.existing != "" {
				require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
				require.NoError(t, os.WriteFile(full, []byte(tc.existing), 0o644))
			}
			changed, err := EnsureLocalExcludes(dir)
			require.NoError(t, err)
			assert.Equal(t, tc.wantChanged, changed, tc.failurePrevented)
			got, err := os.ReadFile(full)
			require.NoError(t, err)
			for _, want := range tc.mustContain {
				assert.Contains(t, string(got), want)
			}
			for _, bad := range tc.mustNotContain {
				assert.NotContains(t, string(got), bad)
			}
			// second call is always a no-op
			changed, err = EnsureLocalExcludes(dir)
			require.NoError(t, err)
			assert.False(t, changed, "second call must be a no-op")
		})
	}
}

// TestEnsureLocalExcludes_NoBlanketRule guards the design choice: the
// exclude set is an explicit list, never `*`. Failure prevented: a future
// "simplification" to `*` that hides any new server-written .sageox/
// subtree from local git status.
func TestEnsureLocalExcludes_NoBlanketRule(t *testing.T) {
	for _, p := range LocalExcludePatterns {
		assert.NotEqual(t, "*", strings.TrimSpace(p))
		assert.NotEqual(t, "/.sageox/*", strings.TrimSpace(p))
		assert.NotEqual(t, "/.sageox/", strings.TrimSpace(p))
		assert.True(t, strings.HasPrefix(p, "/.sageox/"), "patterns must be anchored under /.sageox/: %q", p)
	}
}

func TestEnsureLocalExcludes_Errors(t *testing.T) {
	_, err := EnsureLocalExcludes("")
	assert.Error(t, err)
	_, err = EnsureLocalExcludes(t.TempDir())
	assert.Error(t, err, "must refuse a non-git directory")
}

// TestEnsureLocalExcludes_FailurePaths drives each reachable failure in
// the exclude writer so a broken clone surfaces an error instead of a
// silent no-op (the daemon logs it and continues; a silent nil would hide
// that meta.json will show up as untracked forever).
func TestEnsureLocalExcludes_FailurePaths(t *testing.T) {
	t.Run("info_is_a_file", func(t *testing.T) {
		dir := mkGitDir(t)
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "info"), []byte("x"), 0o644))
		_, err := EnsureLocalExcludes(dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "create info dir")
	})
	t.Run("exclude_is_a_directory", func(t *testing.T) {
		dir := mkGitDir(t)
		require.NoError(t, os.MkdirAll(filepath.Join(dir, localExcludeRelPath), 0o755))
		_, err := EnsureLocalExcludes(dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "read info/exclude")
	})
	t.Run("info_dir_read_only", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permissions")
		}
		dir := mkGitDir(t)
		info := filepath.Join(dir, ".git", "info")
		require.NoError(t, os.MkdirAll(info, 0o755))
		require.NoError(t, os.Chmod(info, 0o555))
		t.Cleanup(func() { _ = os.Chmod(info, 0o755) })
		_, err := EnsureLocalExcludes(dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "write info/exclude")
	})
}

// TestWriteFileAtomic_FailurePaths covers the atomic writer's own error
// branches: an unwritable directory and a rename onto a non-empty
// directory. A successful write must leave no temp file behind.
func TestWriteFileAtomic_FailurePaths(t *testing.T) {
	t.Run("success_leaves_no_temp", func(t *testing.T) {
		dir := t.TempDir()
		full := filepath.Join(dir, "out")
		require.NoError(t, writeFileAtomic(full, "hello\n"))
		got, err := os.ReadFile(full)
		require.NoError(t, err)
		assert.Equal(t, "hello\n", string(got))
		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		assert.Len(t, entries, 1, "temp file must be renamed away, not left behind")
	})
	t.Run("rename_onto_nonempty_dir_fails", func(t *testing.T) {
		dir := t.TempDir()
		full := filepath.Join(dir, "out")
		require.NoError(t, os.MkdirAll(filepath.Join(full, "child"), 0o755))
		err := writeFileAtomic(full, "x")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "rename")
		entries, _ := os.ReadDir(dir)
		assert.Len(t, entries, 1, "temp file must be cleaned up after a failed rename")
	})
	t.Run("unwritable_dir_fails", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permissions")
		}
		dir := t.TempDir()
		require.NoError(t, os.Chmod(dir, 0o555))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		err := writeFileAtomic(filepath.Join(dir, "out"), "x")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "create temp")
	})
	t.Run("closed_file_write_fails", func(t *testing.T) {
		f, err := os.CreateTemp(t.TempDir(), "closed-*")
		require.NoError(t, err)
		require.NoError(t, f.Close())
		assert.Error(t, fillAndClose(f, "x"), "writing to a closed file must report an error")
	})
}

// TestEnsureMergeAttributes_WriteFailure covers the merge-attrs writer's
// error branch now that it shares writeFileAtomic.
func TestEnsureMergeAttributes_WriteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := mkGitDir(t)
	info := filepath.Join(dir, ".git", "info")
	require.NoError(t, os.MkdirAll(info, 0o755))
	require.NoError(t, os.Chmod(info, 0o555))
	t.Cleanup(func() { _ = os.Chmod(info, 0o755) })
	_, err := EnsureMergeAttributes(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write info/attributes")
}
