package gitutil

import (
	"context"
	"fmt"
	"os"
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
	// GIT_TERMINAL_PROMPT=0: ox runs git non-interactively (daemon, CLI
	// fallbacks). Without this, a network op missing credentials prompts for
	// a username on a TTY that isn't there and EOFs into a confusing
	// "could not read Username ... Input/output error". Disabling the prompt
	// makes any credential gap fail fast with a clear error. Tests already
	// set this via internal/testenv, so their behavior is unchanged.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.CombinedOutput()
	sanitized := SanitizeOutput(strings.TrimSpace(string(output)))

	if err != nil {
		return sanitized, fmt.Errorf("git %s: %s: %w", args[0], sanitized, err)
	}
	return sanitized, nil
}
