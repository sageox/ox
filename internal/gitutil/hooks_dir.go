package gitutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// HooksDir returns the directory git runs hooks from for the working tree
// whose top level is gitRoot. Every ox reader and writer of git hooks resolves
// through here, so install, detection, uninstall and doctor agree on one place.
//
//   - core.hooksPath set: used as git uses it. "~" is expanded (`--path`), and a
//     relative value is joined to gitRoot, the top level of the working tree
//     the hook runs in.
//   - otherwise: <git common dir>/hooks. In a linked worktree, `.git` is a file
//     and git runs the main repository's hooks, not <worktree>/.git/hooks.
//   - git unavailable or not a repository: <gitRoot>/.git/hooks, as before.
//
// The common dir is used only when it is an existing directory: a git too old
// to know --git-common-dir echoes the flag back with exit 0, and that must not
// become a hooks directory inside the working tree.
func HooksDir(gitRoot string) string {
	out, err := exec.Command("git", "-C", gitRoot, "config", "--path", "--get", "core.hooksPath").Output()
	if err == nil {
		if hooksPath := strings.TrimSpace(string(out)); hooksPath != "" {
			if filepath.IsAbs(hooksPath) {
				return hooksPath
			}
			return filepath.Join(gitRoot, hooksPath)
		}
	}

	out, err = exec.Command("git", "-C", gitRoot, "rev-parse", "--git-common-dir").Output()
	if err == nil {
		if commonDir := strings.TrimSpace(string(out)); commonDir != "" {
			if !filepath.IsAbs(commonDir) {
				commonDir = filepath.Join(gitRoot, commonDir)
			}
			if info, statErr := os.Stat(commonDir); statErr == nil && info.IsDir() {
				return filepath.Join(commonDir, "hooks")
			}
		}
	}
	return filepath.Join(gitRoot, ".git", "hooks")
}
