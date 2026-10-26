package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/config"
)

// instanceStoreRoot returns the project root whose .sageox/ holds the agent
// instance store for projectRoot: the repository's main worktree, so every
// linked worktree reads and writes one store.
//
// An AI coworker's commands do not all run in the checkout that primed it. The
// Claude desktop app starts Claude Code with --project-config-root set to the
// main checkout, which runs hooks — and so `ox agent prime` — there, while the
// coworker's shell works in a linked worktree under .claude/worktrees/. With a
// store per checkout, every later `ox agent <id>` call failed with "instance
// not found" (#1407).
//
// Returns projectRoot itself when it is the main worktree, when the main
// worktree is not an initialized SageOx project (a store there would leave an
// untracked .sageox/ in that checkout), or when git cannot name a main
// worktree (not a repository, a bare repository, a submodule).
func instanceStoreRoot(projectRoot string) string {
	// a .git directory, rather than a linked worktree's .git file, marks the
	// main worktree: skip the git subprocess on the common path
	if info, err := os.Lstat(filepath.Join(projectRoot, ".git")); err == nil && info.IsDir() {
		return projectRoot
	}

	out, err := exec.Command("git", "-C", projectRoot, "rev-parse",
		"--path-format=absolute", "--show-toplevel", "--git-common-dir").Output()
	if err != nil {
		return projectRoot
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 || filepath.Base(lines[1]) != ".git" {
		return projectRoot // bare repository, separate git dir, or submodule
	}

	// .sageox/ need not sit at the top of the repository: keep projectRoot's
	// place in it
	rel, err := filepath.Rel(resolvePathForCompare(lines[0]), resolvePathForCompare(projectRoot))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return projectRoot
	}
	mainRoot := resolvePathForCompare(filepath.Join(filepath.Dir(lines[1]), rel))
	if mainRoot == resolvePathForCompare(projectRoot) || !config.IsInitialized(mainRoot) {
		return projectRoot
	}
	return mainRoot
}

// checkoutInstanceStore returns projectRoot's own instance store when the
// shared store lives elsewhere and this checkout's store has a file. ox before
// #1407 kept a linked worktree's instances there, and an AI coworker primed by
// such a version must still resolve. Returns nil otherwise, so a lookup never
// creates an empty store in the worktree.
func checkoutInstanceStore(projectRoot, sharedRoot, userSlug string) *agentinstance.Store {
	if resolvePathForCompare(projectRoot) == resolvePathForCompare(sharedRoot) {
		return nil
	}
	if _, err := os.Stat(agentinstance.StorePath(projectRoot, userSlug)); err != nil {
		return nil
	}
	store, err := agentinstance.NewStoreForUser(projectRoot, userSlug)
	if err != nil {
		return nil
	}
	return store
}

// resolvePathForCompare makes paths from git, findProjectRoot, and the
// filesystem comparable: absolute, cleaned, and with symlinks resolved
// (macOS temp dirs live under the /var -> /private/var symlink).
func resolvePathForCompare(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}
