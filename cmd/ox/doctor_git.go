package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sageox/ox/internal/repotools"
)

// sageoxCommittableFiles lists the files in .sageox/ that should be tracked in VCS.
// These are the core configuration files that define the project's SageOx setup.
var sageoxCommittableFiles = []string{
	"config.json",
	"README.md",
	".gitignore",
}

// sageoxCommittablePatterns lists glob patterns for additional files that should be tracked.
// These patterns catch any additional configuration or customization files.
// Note: Machine-specific files like health.json are stored in .sageox/cache/ which is gitignored.
var sageoxCommittablePatterns = []string{
	"*.json",
	"*.md",
	".repo_*", // repo marker files created by ox init
}

// GetSageoxFilesToCommit returns the list of .sageox/ files that exist and should be committed.
// Uses both the explicit file list and glob patterns to find files.
// Returns paths relative to the repo root.
func GetSageoxFilesToCommit() []string {
	var files []string
	repoRoot := findRepoRoot()
	if repoRoot == "" {
		return files
	}

	sageoxDir := filepath.Join(repoRoot, ".sageox")

	if _, err := os.Stat(sageoxDir); err != nil {
		return files
	}

	// add explicit files if they exist
	for _, f := range sageoxCommittableFiles {
		absPath := filepath.Join(sageoxDir, f)
		if _, err := os.Stat(absPath); err == nil {
			// return path relative to repo root for VCS commands
			files = append(files, filepath.Join(".sageox", f))
		}
	}

	// add files matching patterns
	for _, pattern := range sageoxCommittablePatterns {
		matches, err := filepath.Glob(filepath.Join(sageoxDir, pattern))
		if err == nil {
			for _, match := range matches {
				// convert to relative path
				relPath, err := filepath.Rel(repoRoot, match)
				if err != nil {
					continue
				}
				// avoid duplicates
				if !slices.Contains(files, relPath) {
					files = append(files, relPath)
				}
			}
		}
	}

	return files
}

// ForceAddSageoxFiles adds all .sageox/ files to the VCS (git or svn).
// For git, uses -f flag in case files were previously ignored.
func ForceAddSageoxFiles() error {
	repoRoot := findRepoRoot()
	if repoRoot == "" {
		return nil
	}
	files := GetSageoxFilesToCommit()
	if len(files) == 0 {
		return nil
	}

	vcs, err := repotools.DetectVCS()
	if err != nil {
		return err
	}

	var cmd *exec.Cmd
	switch vcs {
	case repotools.VCSGit:
		args := append([]string{"add", "-f"}, files...)
		cmd = exec.Command("git", args...)
	case repotools.VCSSvn:
		// svn add --force adds files even if they're already versioned
		args := append([]string{"add", "--force"}, files...)
		cmd = exec.Command("svn", args...)
	default:
		return nil
	}

	cmd.Dir = repoRoot
	return cmd.Run()
}

// findRepoRoot finds the root of the current repository (git or svn)
func findRepoRoot() string {
	// try git first
	if root := findGitRoot(); root != "" {
		return root
	}
	// try svn
	if repotools.IsInstalled(repotools.VCSSvn) {
		if root, err := repotools.FindRepoRoot(repotools.VCSSvn); err == nil {
			return root
		}
	}
	return ""
}

func checkGitStatus() checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return checkResult{
			name:    "Git repository",
			skipped: true,
			message: "not in a git repo",
		}
	}

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if _, err := os.Stat(sageoxDir); err != nil {
		return checkResult{
			name:    ".sageox/ tracked",
			skipped: true,
			message: "directory not found",
		}
	}

	cmd := exec.Command("git", "status", "--porcelain", ".sageox/")
	cmd.Dir = gitRoot // run from git root
	output, err := cmd.Output()
	if err != nil {
		return checkResult{
			name:    ".sageox/ tracked",
			passed:  true,
			message: "",
		}
	}

	if len(output) > 0 {
		// parse porcelain output to distinguish staged vs unstaged
		// format: XY filename (X=index, Y=worktree)
		lines := strings.Split(strings.TrimSpace(string(output)), "\n")
		hasUnstaged := false
		for _, line := range lines {
			if len(line) < 2 {
				continue
			}
			// Y position (worktree) shows unstaged changes
			// ' ' = unchanged, 'M' = modified, 'D' = deleted, '?' = untracked
			y := line[1]
			if y != ' ' {
				hasUnstaged = true
				break
			}
		}

		if hasUnstaged {
			// unstaged changes - warn user to stage and commit
			return checkResult{
				name:    ".sageox/ changes",
				passed:  true,
				warning: true,
				message: "unstaged",
				detail:  "Stage and commit to track convention updates",
			}
		}

		// all changes are staged — informational, ready to commit
		return checkResult{
			name:    ".sageox/ changes",
			passed:  true,
			message: "staged, ready to commit",
		}
	}

	return checkResult{
		name:    ".sageox/ tracked",
		passed:  true,
		message: "committed",
	}
}

// checkSageoxFilesTracked verifies that all .sageox/ files are tracked by VCS (git or svn).
// With fix=true, adds files to VCS.
func checkSageoxFilesTracked(fix bool) checkResult {
	repoRoot := findRepoRoot()
	if repoRoot == "" {
		return checkResult{
			name:    ".sageox/ files staged",
			skipped: true,
			message: "not in a repository",
		}
	}

	vcs, err := repotools.DetectVCS()
	if err != nil {
		return checkResult{
			name:    ".sageox/ files staged",
			skipped: true,
			message: "unknown VCS",
		}
	}

	files := GetSageoxFilesToCommit()
	if len(files) == 0 {
		return checkResult{
			name:    ".sageox/ files staged",
			skipped: true,
			message: "no files found",
		}
	}

	// check which files are untracked
	var untrackedFiles []string
	for _, f := range files {
		tracked := false
		switch vcs {
		case repotools.VCSGit:
			cmd := exec.Command("git", "ls-files", "--error-unmatch", f)
			cmd.Dir = repoRoot
			tracked = cmd.Run() == nil
		case repotools.VCSSvn:
			// svn info returns 0 for versioned files
			cmd := exec.Command("svn", "info", f)
			cmd.Dir = repoRoot
			tracked = cmd.Run() == nil
		}
		if !tracked {
			untrackedFiles = append(untrackedFiles, f)
		}
	}

	if len(untrackedFiles) > 0 {
		if fix {
			if err := ForceAddSageoxFiles(); err != nil {
				return checkResult{
					name:    ".sageox/ files staged",
					passed:  false,
					message: "fix failed",
					detail:  err.Error(),
				}
			}
			return checkResult{
				name:    ".sageox/ files staged",
				passed:  true,
				message: "fixed (added to VCS)",
			}
		}
		return checkResult{
			name:    ".sageox/ files staged",
			passed:  false,
			message: "untracked files",
			detail:  "Run `ox init` to add files to VCS",
		}
	}

	return checkResult{
		name:    ".sageox/ files staged",
		passed:  true,
		message: "all tracked",
	}
}

// checkGitignore ensures .sageox/ is not in .gitignore
func checkGitignore(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return checkResult{
			name:    ".gitignore",
			skipped: true,
			message: "not in git repo",
		}
	}

	gitignorePath := filepath.Join(gitRoot, ".gitignore")
	content, err := os.ReadFile(gitignorePath)
	if err != nil {
		return checkResult{
			name:    ".gitignore",
			skipped: true,
			message: "no .gitignore",
		}
	}

	lines := strings.Split(string(content), "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == ".sageox" || trimmed == ".sageox/" || trimmed == "/.sageox" || trimmed == "/.sageox/" {
			if fix {
				// remove the line from .gitignore
				newLines := append(lines[:i], lines[i+1:]...)
				newContent := strings.Join(newLines, "\n")
				if err := os.WriteFile(gitignorePath, []byte(newContent), 0644); err != nil {
					return checkResult{
						name:    ".gitignore",
						passed:  false,
						message: "fix failed",
						detail:  err.Error(),
					}
				}
				return checkResult{
					name:    ".gitignore",
					passed:  true,
					message: "fixed",
				}
			}
			return checkResult{
				name:    ".gitignore",
				passed:  false,
				message: ".sageox/ is ignored",
				detail:  "Remove from .gitignore to track conventions",
			}
		}
	}
	return checkResult{
		name:    ".gitignore",
		passed:  true,
		message: "not ignored",
	}
}
