package gitutil

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// StaleRebaseThreshold is how long a rebase must have been in progress before
// automated recovery treats it as a wedge rather than an in-flight operation.
// A fresh rebase (younger than this) is almost always a live `pull --rebase`
// or a human mid-operation and must be left alone. Matches the daemon's own
// staleness gate so the CLI (doctor) and daemon agree on what "stuck" means.
const StaleRebaseThreshold = 5 * time.Minute

// AbortOrClearRebase clears an in-progress rebase state that is blocking sync.
//
// It first tries the reversible, audited `git rebase --abort` (via
// AuditAndAbort), which is correct whenever the rebase state directory is
// intact. When abort FAILS because the state directory is structurally
// incomplete — a "zombie" left by a process killed mid-rebase, e.g. a
// .git/rebase-merge containing only an `autostash` entry with no
// head-name/orig-head — abort cannot determine where to reset HEAD, so it
// escalates to `git rebase --quit`, which removes the state directory WITHOUT
// moving HEAD.
//
// The quit escalation is gated on two conditions that together make it safe:
//
//  1. The state directory is missing the metadata `--abort` needs (head-name /
//     orig-head). A complete directory that still failed to abort is a
//     different, unknown problem — surface it, don't guess.
//  2. HEAD is on a real branch (not detached). A detached HEAD means the
//     rebase had already rewound and was mid-replay, where --quit would strand
//     HEAD at a partial-replay commit. A zombie killed during rebase init never
//     rewound HEAD, so the branch still holds every original commit and --quit
//     is a pure no-op on history.
//
// Any parked working tree recorded in the state's `autostash` entry is logged
// (object id) before the directory is dropped so it stays recoverable as a
// dangling object — never silently discarded (.claude/rules/daemon-git.md).
//
// Returns nil when no rebase remains in progress afterward; otherwise the error
// from the last recovery attempt. Logger MUST be non-nil in normal use; a nil
// logger falls back to a discard handler rather than panicking.
func AbortOrClearRebase(ctx context.Context, repoPath, reason string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(discardWriter{}, nil))
	}

	// Rung 1: the reversible, audited abort. Clears any intact rebase state.
	abortErr := AuditAndAbort(ctx, repoPath, AuditOpRebase, reason, logger)
	if abortErr == nil {
		return nil
	}

	// Abort failed. Decide whether the crash-mid-init signature holds, where
	// `git rebase --quit` is the safe escalation.
	stateDir, ok := rebaseStateDir(repoPath)
	if !ok {
		// No state dir yet abort failed. If nothing is in progress the
		// post-condition is already satisfied; otherwise surface the abort error.
		if !IsRebaseInProgress(repoPath) {
			return nil
		}
		return abortErr
	}
	if rebaseStateAbortable(stateDir) {
		// The directory carries the metadata abort needs, yet abort still
		// failed — an unknown fault. Don't quit blindly; surface it.
		return abortErr
	}
	if headDetached(ctx, repoPath) {
		// Rebase had rewound HEAD; quitting would strand a partial replay.
		return abortErr
	}

	// Record any parked autostash before dropping the state directory.
	autostash := readAutostashOID(stateDir)

	quitCtx, cancel := context.WithTimeout(context.Background(), abortTimeout)
	defer cancel()
	quitCmd := exec.CommandContext(quitCtx, "git", "-C", repoPath, "rebase", "--quit")
	out, quitErr := quitCmd.CombinedOutput()
	if quitErr != nil {
		// `git rebase --quit` can exit non-zero while STILL removing the state
		// directory (e.g. a stale autostash object failed to re-apply). The
		// wedge is what we care about — only treat it as a failure if a rebase
		// genuinely persists.
		if IsRebaseInProgress(repoPath) {
			logger.Error("git_rebase_quit_failed",
				"op", "rebase_quit_failed", "repo", repoPath,
				"error", quitErr,
				"output", SanitizeOutput(strings.TrimSpace(string(out))))
			return fmt.Errorf("git rebase --quit after failed abort: %w", quitErr)
		}
		logger.Warn("git_rebase_quit_nonzero_but_cleared",
			"op", "rebase_quit_nonzero_but_cleared", "repo", repoPath,
			"output", SanitizeOutput(strings.TrimSpace(string(out))))
	}

	if IsRebaseInProgress(repoPath) {
		return fmt.Errorf("rebase state persists after git rebase --quit at %s", repoPath)
	}

	logger.Warn("git_rebase_quit_recovered",
		"op", "rebase_quit_recovered", "repo", repoPath,
		"reason", reason,
		"head_sha", captureHeadSHA(ctx, repoPath),
		"autostash_oid", autostash,
		"abort_error", SanitizeOutput(abortErr.Error()))
	return nil
}

// rebaseStateDir returns the active rebase state directory (.git/rebase-merge
// or .git/rebase-apply) and whether one exists.
func rebaseStateDir(repoPath string) (string, bool) {
	gitDir := filepath.Join(repoPath, ".git")
	for _, d := range []string{"rebase-merge", "rebase-apply"} {
		p := filepath.Join(gitDir, d)
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
	}
	return "", false
}

// rebaseStateAbortable reports whether a rebase state directory carries the
// metadata `git rebase --abort` needs to reset HEAD: a non-empty head-name AND
// orig-head. A directory missing either is a zombie abort cannot clear.
func rebaseStateAbortable(stateDir string) bool {
	return nonEmptyFile(filepath.Join(stateDir, "head-name")) &&
		nonEmptyFile(filepath.Join(stateDir, "orig-head"))
}

// nonEmptyFile reports whether path exists and has a non-zero size.
func nonEmptyFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0
}

// readAutostashOID returns the object id recorded in the rebase state's
// autostash file, or "" if none. git writes the stashed working tree's commit
// id here; logging it keeps the parked changes recoverable after --quit.
func readAutostashOID(stateDir string) string {
	data, err := os.ReadFile(filepath.Join(stateDir, "autostash"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// headDetached reports whether HEAD is NOT on a branch. `git symbolic-ref -q
// HEAD` exits 0 when on a branch and non-zero when detached. Best-effort: on
// any error it returns false (treat as on-branch) so a transient git hiccup
// doesn't over-eagerly refuse the quit path.
func headDetached(ctx context.Context, repoPath string) bool {
	subCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(subCtx, "git", "-C", repoPath, "symbolic-ref", "-q", "HEAD")
	return cmd.Run() != nil
}
