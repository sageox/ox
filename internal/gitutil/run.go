package gitutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// RunGit executes a git command with context for timeout/cancellation.
// Output is auto-sanitized to remove credentials. Use repoPath="" for
// commands that don't need -C.
func RunGit(ctx context.Context, repoPath string, args ...string) (string, error) {
	output, err := localGitCommand(ctx, repoPath, args...).CombinedOutput()
	sanitized := SanitizeOutput(strings.TrimSpace(string(output)))
	if err != nil {
		return sanitized, fmt.Errorf("git %s: %s: %w", args[0], sanitized, err)
	}
	return sanitized, nil
}

// RunGitStdout returns Git's stdout without stderr diagnostics or sanitization.
// Use it to parse machine-readable output. Error messages sanitize stderr.
func RunGitStdout(ctx context.Context, repoPath string, args ...string) ([]byte, error) {
	output, err := localGitCommand(ctx, repoPath, args...).Output()
	if err == nil {
		return output, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && len(exitErr.Stderr) != 0 {
		return nil, fmt.Errorf("git %s: %s: %w", args[0], SanitizeOutput(strings.TrimSpace(string(exitErr.Stderr))), err)
	}
	return nil, fmt.Errorf("git %s: %w", args[0], err)
}

func localGitCommand(ctx context.Context, repoPath string, args ...string) *exec.Cmd {
	var cmdArgs []string
	if repoPath != "" {
		cmdArgs = append(cmdArgs, "-C", repoPath)
	}
	// commit.gpgsign=false / tag.gpgsign=false: ox only ever commits its own
	// machine-managed repos (ledgers, team contexts, murmurs) and always does
	// so non-interactively. If the user's git config enables SSH/GPG commit
	// signing with a passphrase-protected key, signing prompts on a TTY that
	// the daemon and CLI fallbacks don't have, and the commit dies with
	// "fatal: failed to write commit object" — silently wedging ledger sync.
	// Overriding per-invocation makes ox's commits immune regardless of the
	// repo's persisted config. Harmless no-op for non-commit subcommands.
	// filter.lfs.*: see NewNetworkCmd's doc comment (cmd.go) — this is the
	// other chokepoint git subprocesses go through, and it needs the same
	// dehydrated-clone override so a caller through RunGit doesn't reopen
	// the nested-LFS-pointer wedge (ox-baz5.4) that NewNetworkCmd alone closed.
	cmdArgs = append(cmdArgs,
		"-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false",
		"-c", "filter.lfs.smudge=cat", "-c", "filter.lfs.clean=cat",
		"-c", "filter.lfs.process=", "-c", "filter.lfs.required=false",
	)
	cmdArgs = append(cmdArgs, args...)

	cmd := commandContext(ctx, "git", cmdArgs...)
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
	//
	// LC_ALL=C / LANG=C: several call sites (isNonFastForwardErr, LFS-error
	// detection, "project not initialized", "403"/"forbidden" checks, etc.)
	// substring-match git's stderr/stdout to classify a failure. Those
	// messages are translatable in git's own gettext catalogs — on a host
	// with a non-English locale and the matching catalog installed, the
	// same failure renders in that language and every one of those checks
	// silently stops matching. Pinning the C locale here, once, for every
	// git subprocess this codebase spawns, closes that whole class instead
	// of patching each match site individually. Does not affect git's
	// handling of file content or paths, which are UTF-8 regardless.
	cmd.Env = append(oxIdentityEnv(ctx, repoPath, os.Environ()), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C", "LANG=C")
	return cmd
}
