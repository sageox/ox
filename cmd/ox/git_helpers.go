package main

import "github.com/sageox/ox/internal/gitutil"

// isGitRepo delegates to gitutil.IsGitRepo for backward compatibility
// across the many cmd/ox callers.
func isGitRepo(path string) bool {
	return gitutil.IsGitRepo(path)
}
