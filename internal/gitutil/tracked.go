package gitutil

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
)

// PathTracked reports whether git tracks rel inside repoRoot.
//
// `git ls-files --error-unmatch` exits 1 for the ordinary "no tracked path
// matched" answer and reserves every other outcome for a real failure: a
// canceled context, a git that is missing or too old, an unreadable index.
// Collapsing those into "untracked" is what let Team Rule reconcile overwrite or
// delete a TRACKED projection, report it applied, and clear the pending state —
// leaving an uncommitted change with nothing scheduled to revisit it. So only exit
// 1 means untracked; anything else is returned as an error, for the caller to
// treat as retryable rather than settled.
//
// The context check comes first on purpose: killing the child on cancellation
// surfaces as a signal on Unix and as exit code 1 on Windows, so an expired
// deadline would otherwise be indistinguishable from a genuine "not tracked".
//
// It lives here rather than in teamrules because Team SKILLS need the same
// question answered. A team skill installs under its real name plus a "-team"
// suffix, so ox can no longer tell its own projection from a hand-authored
// `notify-team` by the name alone — and "git already tracks this path" is the one
// signal that proves a human committed it deliberately.
func PathTracked(ctx context.Context, repoRoot, rel string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "ls-files", "--error-unmatch", "--", rel)
	cmd.Dir = repoRoot
	runErr := cmd.Run()
	if runErr == nil {
		return true, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, fmt.Errorf("check whether %s is tracked: %w", rel, ctxErr)
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("check whether %s is tracked: %w", rel, runErr)
}
