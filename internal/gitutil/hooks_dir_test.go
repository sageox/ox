package gitutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// isolateHooksGitConfig keeps a developer's or runner's global core.hooksPath
// out of HooksDir's own git calls, and gives "~" a known expansion.
func isolateHooksGitConfig(t *testing.T) string {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	return home
}

// hooksDirRepo creates a repository with one commit and a linked worktree.
func hooksDirRepo(t *testing.T) (mainRoot, worktreeRoot string) {
	t.Helper()
	var err error
	mainRoot, err = filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	runGit(t, mainRoot, "init", "-b", "main")
	runGit(t, mainRoot, "commit", "--allow-empty", "-m", "initial")
	parent, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	worktreeRoot = filepath.Join(parent, "linked")
	runGit(t, mainRoot, "worktree", "add", "-b", "linked", worktreeRoot)
	return mainRoot, worktreeRoot
}

// Failure prevented: ox reads or writes hooks somewhere git never runs them
// (a linked worktree's .git file, a literal "~" directory), so install, check
// and uninstall disagree with git about which hooks exist.
func TestHooksDir_ResolvesWhereGitRunsHooks(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repositories and worktrees")
	}
	for _, tt := range []struct {
		name      string
		hooksPath string // core.hooksPath value; "" leaves it unset
		want      func(home, mainRoot, root string) string
	}{
		{
			name: "default",
			want: func(_, mainRoot, _ string) string { return filepath.Join(mainRoot, ".git", "hooks") },
		},
		{
			name:      "relative_hooks_path",
			hooksPath: "custom-hooks",
			want:      func(_, _, root string) string { return filepath.Join(root, "custom-hooks") },
		},
		{
			name:      "home_hooks_path",
			hooksPath: "~/shared-hooks",
			want:      func(home, _, _ string) string { return filepath.Join(home, "shared-hooks") },
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := isolateHooksGitConfig(t)
			mainRoot, worktreeRoot := hooksDirRepo(t)
			if tt.hooksPath != "" {
				runGit(t, mainRoot, "config", "core.hooksPath", tt.hooksPath)
			}
			for _, root := range []string{mainRoot, worktreeRoot} {
				assert.Equal(t, tt.want(home, mainRoot, root), HooksDir(root), root)
			}
		})
	}
}

// Failure prevented: a path git cannot resolve stops resolving to the
// directory ox used before git discovery was added.
func TestHooksDir_FallbackOutsideRepository(t *testing.T) {
	isolateHooksGitConfig(t)
	root := t.TempDir()
	assert.Equal(t, filepath.Join(root, ".git", "hooks"), HooksDir(root))
}

// Failure prevented: a git too old to know --git-common-dir echoes the flag
// back with exit 0, and ox writes hooks into "<repo>/--git-common-dir/hooks"
// inside the working tree.
func TestHooksDir_IgnoresCommonDirThatIsNotADirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake git is a POSIX shell script")
	}
	bin := t.TempDir()
	fakeGit := "#!/bin/sh\n" +
		"for a in \"$@\"; do\n" +
		"  case \"$a\" in config) exit 1 ;; --git-common-dir) echo \"$a\"; exit 0 ;; esac\n" +
		"done\n" +
		"exit 1\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(fakeGit), 0o755))
	t.Setenv("PATH", bin)

	root := t.TempDir()
	assert.Equal(t, filepath.Join(root, ".git", "hooks"), HooksDir(root))
}
