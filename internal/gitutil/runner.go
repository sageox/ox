package gitutil

import "context"

// GitRunner abstracts git command execution for testability.
type GitRunner interface {
	RunGit(ctx context.Context, repoPath string, args ...string) (string, error)
}

// RealRunner executes git commands via os/exec (production implementation).
type RealRunner struct{}

// RunGit delegates to the package-level RunGit function.
func (r *RealRunner) RunGit(ctx context.Context, repoPath string, args ...string) (string, error) {
	return RunGit(ctx, repoPath, args...)
}

// DefaultRunner returns the production GitRunner.
func DefaultRunner() GitRunner {
	return &RealRunner{}
}

// compile-time assertion
var _ GitRunner = (*RealRunner)(nil)
