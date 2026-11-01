package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func gitHooksWorktree(t *testing.T) (string, string) {
	t.Helper()
	mainRoot, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	mustRunGit(t, mainRoot, "init")
	mustRunGit(t, mainRoot, "commit", "--allow-empty", "-m", "initial")
	worktreeParent, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	worktreeRoot := filepath.Join(worktreeParent, "linked")
	mustRunGit(t, mainRoot, "worktree", "add", "-b", "hooks-fixture", worktreeRoot)
	return mainRoot, worktreeRoot
}

// Failure prevented: init and doctor cannot install or detect shared hooks from a linked worktree.
func TestInstallGitHooks_LinkedWorktree(t *testing.T) {
	skipIntegration(t) // Real git init, commit, and worktree creation.
	for _, installFromWorktree := range []bool{false, true} {
		name := "main"
		if installFromWorktree {
			name = "worktree"
		}
		t.Run(name, func(t *testing.T) {
			mainRoot, worktreeRoot := gitHooksWorktree(t)
			installRoot := mainRoot
			if installFromWorktree {
				installRoot = worktreeRoot
			}
			require.NoError(t, InstallGitHooks(installRoot))
			for _, spec := range allHookSpecs() {
				content, err := os.ReadFile(filepath.Join(mainRoot, ".git", "hooks", spec.name))
				require.NoError(t, err, spec.name)
				assert.Contains(t, string(content), spec.probe)
			}
			assert.True(t, HasGitHooks(mainRoot))
			assert.True(t, HasGitHooks(worktreeRoot))
			info, err := os.Stat(filepath.Join(worktreeRoot, ".git"))
			require.NoError(t, err)
			assert.True(t, info.Mode().IsRegular(), "the worktree's gitfile must remain intact")
		})
	}
}

// Failure prevented: uninstall in a linked worktree leaves shared ox sections behind or deletes user content.
func TestUninstallGitHooks_LinkedWorktree(t *testing.T) {
	skipIntegration(t) // Real git init, commit, and worktree creation.
	for _, preserveUserContent := range []bool{false, true} {
		name := "ox_only"
		if preserveUserContent {
			name = "user_content"
		}
		t.Run(name, func(t *testing.T) {
			mainRoot, worktreeRoot := gitHooksWorktree(t)
			const userHook = "#!/bin/sh\n# user hook\nexit 0\n"
			if preserveUserContent {
				for _, spec := range allHookSpecs() {
					require.NoError(t, os.WriteFile(filepath.Join(mainRoot, ".git", "hooks", spec.name), []byte(userHook), 0755))
				}
			}
			require.NoError(t, InstallGitHooks(mainRoot))
			require.NoError(t, UninstallGitHooks(worktreeRoot))
			for _, spec := range allHookSpecs() {
				path := filepath.Join(mainRoot, ".git", "hooks", spec.name)
				if preserveUserContent {
					content, err := os.ReadFile(path)
					require.NoError(t, err)
					assert.Equal(t, userHook, string(content))
				} else {
					assert.NoFileExists(t, path)
				}
			}
			assert.False(t, HasGitHooks(mainRoot))
			assert.False(t, HasGitHooks(worktreeRoot))
		})
	}
}

// Failure prevented: fixing the default directory changes explicit hooksPath resolution in either checkout.
func TestInstallGitHooks_HooksPathAcrossWorktrees(t *testing.T) {
	skipIntegration(t) // Multiple real git worktree fixtures.
	for _, hooksPathKind := range []string{"default", "absolute", "relative"} {
		t.Run(hooksPathKind, func(t *testing.T) {
			mainRoot, worktreeRoot := gitHooksWorktree(t)
			customPath := "custom-hooks"
			if hooksPathKind == "absolute" {
				customPath = filepath.Join(t.TempDir(), "custom-hooks")
			}
			if hooksPathKind != "default" {
				mustRunGit(t, mainRoot, "config", "core.hooksPath", customPath)
			}
			for _, root := range []string{mainRoot, worktreeRoot} {
				want := filepath.Join(mainRoot, ".git", "hooks")
				switch hooksPathKind {
				case "absolute":
					want = customPath
				case "relative":
					want = filepath.Join(root, customPath)
				}
				assert.Equal(t, want, resolveHooksDir(root))
				require.NoError(t, InstallGitHooks(root))
				for _, spec := range allHookSpecs() {
					assert.FileExists(t, filepath.Join(want, spec.name))
				}
				assert.True(t, HasGitHooks(root))
			}
		})
	}
}

// Failure prevented: Git discovery failure removes the existing non-repository fallback.
func TestHooksDir_FallbackOutsideRepository(t *testing.T) {
	root := t.TempDir()
	assert.Equal(t, filepath.Join(root, ".git", "hooks"), resolveHooksDir(root))
}
