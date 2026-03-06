package gitutil

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// RunGit executes a git command with context for timeout/cancellation.
// Output is auto-sanitized to remove credentials. Use repoPath="" for
// commands that don't need -C.
func RunGit(ctx context.Context, repoPath string, args ...string) (string, error) {
	var cmdArgs []string
	if repoPath != "" {
		cmdArgs = append(cmdArgs, "-C", repoPath)
	}
	cmdArgs = append(cmdArgs, args...)

	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	// set cmd.Dir so git doesn't fail on getcwd() when the process CWD
	// has been deleted (e.g. daemon started from a tmpdir that was cleaned)
	if repoPath != "" {
		cmd.Dir = repoPath
	}
	output, err := cmd.CombinedOutput()
	sanitized := SanitizeOutput(strings.TrimSpace(string(output)))

	if err != nil {
		return sanitized, fmt.Errorf("git %s: %s: %w", args[0], sanitized, err)
	}
	return sanitized, nil
}
