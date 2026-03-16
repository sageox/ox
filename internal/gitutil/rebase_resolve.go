package gitutil

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// ResolveRebaseAcceptTheirs attempts to resolve a rebase conflict by accepting
// the remote ("theirs") version of all conflicted files, but ONLY if every
// conflicted file is under one of the given safe prefixes.
//
// This is safe for data directories (like data/github/) where the content is
// derived from an external source and the next sync cycle will re-fetch the
// latest version anyway. Last-write-wins is the correct strategy.
//
// Returns nil if the rebase was successfully continued after resolution.
// Returns an error if any conflicted file is outside the safe prefixes (the
// rebase is NOT aborted — caller should abort if needed).
func ResolveRebaseAcceptTheirs(ctx context.Context, repoPath string, safePrefixes []string) error {
	// list conflicted files (unmerged)
	conflicted, err := listConflictedFiles(ctx, repoPath)
	if err != nil {
		return fmt.Errorf("list conflicted files: %w", err)
	}
	if len(conflicted) == 0 {
		return fmt.Errorf("no conflicted files found")
	}

	// verify all conflicted files are under safe prefixes
	for _, f := range conflicted {
		if !matchesSafePrefix(f, safePrefixes) {
			return fmt.Errorf("conflicted file %q is not under safe auto-resolve prefixes %v", f, safePrefixes)
		}
	}

	// accept theirs for all conflicted files
	checkoutArgs := append([]string{"checkout", "--theirs", "--"}, conflicted...)
	if _, err := RunGit(ctx, repoPath, checkoutArgs...); err != nil {
		return fmt.Errorf("checkout --theirs: %w", err)
	}

	// stage resolved files
	addArgs := append([]string{"add", "--"}, conflicted...)
	if _, err := RunGit(ctx, repoPath, addArgs...); err != nil {
		return fmt.Errorf("git add resolved files: %w", err)
	}

	// continue the rebase
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "rebase", "--continue")
	cmd.Dir = repoPath
	// GIT_EDITOR=true prevents git from opening an editor for the commit message
	cmd.Env = append(cmd.Environ(), "GIT_EDITOR=true")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("rebase --continue: %s: %w", strings.TrimSpace(string(output)), err)
	}

	return nil
}

// listConflictedFiles returns the list of files with unresolved merge conflicts.
func listConflictedFiles(ctx context.Context, repoPath string) ([]string, error) {
	out, err := RunGit(ctx, repoPath, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// matchesSafePrefix checks if a file path starts with any of the safe prefixes.
func matchesSafePrefix(path string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}
