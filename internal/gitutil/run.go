package gitutil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// RunGit executes a git command with context for timeout/cancellation.
// Output is auto-sanitized to remove credentials. Use repoPath="" for
// commands that don't need -C.
func RunGit(ctx context.Context, repoPath string, args ...string) (string, error) {
	if len(args) == 0 {
		return "", errors.New("git: missing subcommand")
	}
	output, err := localGitCommand(ctx, repoPath, args...).CombinedOutput()
	sanitized := SanitizeOutput(strings.TrimSpace(string(output)))
	if err != nil {
		return sanitized, fmt.Errorf("git %s: %s: %w", args[0], sanitized, err)
	}
	return sanitized, nil
}

// RunGitFileOutput is RunGit with git's output sent to a file instead of a
// pipe. Use it for a command that must finish even if this process dies under
// it, such as a push. Through a pipe, git's next write after this process died
// kills git with SIGPIPE; a session push orphaned at Claude Code's SessionEnd
// died that way after authenticating, before it sent anything
// (sageox/ox#1936). A file has no reader to lose.
func RunGitFileOutput(ctx context.Context, repoPath string, args ...string) (string, error) {
	if len(args) == 0 {
		return "", errors.New("git: missing subcommand")
	}
	output, err := runWithFileOutput(localGitCommand(ctx, repoPath, args...))
	sanitized := SanitizeOutput(strings.TrimSpace(string(output)))
	if err != nil {
		return sanitized, fmt.Errorf("git %s: %s: %w", args[0], sanitized, err)
	}
	return sanitized, nil
}

// runWithFileOutput runs cmd with stdout and stderr in one temporary file and
// returns what it wrote, like CombinedOutput. The file is unlinked as soon as
// cmd starts: cmd keeps its descriptor, so a parent that dies leaves no file
// behind (Windows cannot unlink an open file; it is removed after the read).
func runWithFileOutput(cmd *exec.Cmd) ([]byte, error) {
	f, err := os.CreateTemp("", "ox-git-output-*")
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = f.Close()
		_ = os.Remove(f.Name())
	}()
	cmd.Stdout, cmd.Stderr = f, f
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	_ = os.Remove(f.Name())
	runErr := cmd.Wait()
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, errors.Join(runErr, err)
	}
	output, err := io.ReadAll(f)
	if err != nil {
		return output, errors.Join(runErr, err)
	}
	return output, runErr
}

// RunGitStdout returns Git's stdout without stderr diagnostics or sanitization.
// Use it to parse machine-readable output. Error messages sanitize stderr.
func RunGitStdout(ctx context.Context, repoPath string, args ...string) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("git: missing subcommand")
	}
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
