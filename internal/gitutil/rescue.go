package gitutil

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// rescueBranchPrefix names branches created by RescueThenAbort. The prefix is
// stable and greppable so an operator (or a later doctor run) can find every
// rescue point on a machine with `git branch --list 'rescue-wedge-*'`.
const rescueBranchPrefix = "rescue-wedge-"

// ErrNoStrandedCommits reports that HEAD carries nothing that is not already
// reachable from a branch or remote — so there is nothing to rescue and the
// caller should use the ordinary recovery path.
var ErrNoStrandedCommits = fmt.Errorf("no stranded commits: nothing to rescue")

// RescueThenAbort recovers a ledger whose HEAD carries commits that exist
// NOWHERE else, by creating a verified rescue branch BEFORE it touches the
// rebase state.
//
// # Why this exists
//
// The ledger holds the user's only copy of unpushed session data. `git rebase
// --abort` resets HEAD to orig-head and `--quit` drops the state directory;
// either way, commits that were only reachable from a detached HEAD become
// unreferenced and survive solely in the reflog until gc prunes them.
//
// AbortOrClearRebase deliberately REFUSES to escalate to --quit when HEAD is
// detached, precisely because quitting there would strand a partial replay.
// That refusal is correct and this function does not weaken it. The gap it
// leaves is that nothing then recovers the wedge at all: bd ox-akab stranded
// roughly six weeks of sessions on a detached HEAD, invisible to the user,
// because every automated path correctly declined to act and no path made the
// commits safe first.
//
// # The ordering, which is the whole point
//
//  1. Count commits reachable from HEAD but from no branch or remote.
//     Zero means nothing is at risk: return ErrNoStrandedCommits so the caller
//     falls back to the ordinary path.
//  2. Create rescue-wedge-<UTC> at HEAD and VERIFY the ref resolves. A branch
//     that was not created is not a rescue, and discovering that after the
//     abort is discovering it too late.
//  3. Only now attempt AbortOrClearRebase.
//  4. Re-verify the rescue ref still resolves AND still carries the same commit
//     count. Recovery that quietly moved the safety net is not recovery.
//
// This function NEVER runs git gc, --prune, or reflog expire, and no caller may
// run them in the same doctor pass: unreferenced commits live in the reflog
// only until a gc, so pruning collapses the recovery window to zero.
//
// Returns the rescue branch name so the caller can print it FIRST, before any
// other output. On any failure after step 2 the rescue branch is left in place
// on purpose — an orphaned rescue branch is cheap, and losing the commits is not.
func RescueThenAbort(ctx context.Context, repoPath, reason string, logger *slog.Logger) (string, error) {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(discardWriter{}, nil))
	}

	// Step 1: is anything actually at risk?
	stranded, err := StrandedCommitCount(ctx, repoPath)
	if err != nil {
		return "", fmt.Errorf("counting stranded commits: %w", err)
	}
	if stranded == 0 {
		return "", ErrNoStrandedCommits
	}

	// Step 2: create the safety net and prove it exists.
	rescueRef, err := CreateRescueBranch(ctx, repoPath, reason, logger)
	if err != nil {
		return "", err
	}
	rescueSHA, err := resolveRef(ctx, repoPath, rescueRef)
	if err != nil {
		return rescueRef, fmt.Errorf("rescue branch %s did not resolve after creation: %w", rescueRef, err)
	}

	// Step 3: now, and only now, clear the wedge.
	abortErr := AbortOrClearRebase(ctx, repoPath, reason, logger)

	// Step 4: the safety net must have survived, unchanged.
	afterSHA, resolveErr := resolveRef(ctx, repoPath, rescueRef)
	if resolveErr != nil {
		return rescueRef, fmt.Errorf("rescue branch %s no longer resolves after recovery: %w", rescueRef, resolveErr)
	}
	if afterSHA != rescueSHA {
		return rescueRef, fmt.Errorf("rescue branch %s moved during recovery (%s to %s)", rescueRef, rescueSHA, afterSHA)
	}
	afterCount, countErr := commitCount(ctx, repoPath, rescueRef)
	if countErr == nil {
		beforeCount, beforeErr := commitCount(ctx, repoPath, rescueSHA)
		if beforeErr == nil && afterCount != beforeCount {
			return rescueRef, fmt.Errorf("rescue branch %s changed length during recovery (%d to %d commits)",
				rescueRef, beforeCount, afterCount)
		}
	}

	if abortErr != nil {
		// The wedge persists, but the commits are safe on rescueRef. Say both.
		return rescueRef, fmt.Errorf("rescue branch %s holds %d stranded commit(s); clearing the wedge still failed: %w",
			rescueRef, stranded, abortErr)
	}

	logger.Warn("ledger_rescue_completed",
		"op", "rescue_completed", "repo", repoPath,
		"rescue_ref", rescueRef, "stranded_commits", stranded)

	return rescueRef, nil
}

// CreateRescueBranch points a new rescue-wedge-<UTC> branch at HEAD and verifies
// it resolves, so commits reachable only from HEAD stop being one checkout away
// from unreferenced.
//
// This is deliberately separable from RescueThenAbort because the two halves have
// very different risk profiles. Creating a branch is purely ADDITIVE — it adds a
// ref and mutates nothing else — so it is safe to run unattended, in an agent
// context, without a human present. Clearing the wedge is destructive and is not.
// Splitting them means an automated pass can always make the data safe even when
// it must leave the wedge for a human.
//
// Returns ErrNoStrandedCommits when nothing is at risk, so callers do not litter
// the repo with rescue branches on healthy repos.
func CreateRescueBranch(ctx context.Context, repoPath, reason string, logger *slog.Logger) (string, error) {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(discardWriter{}, nil))
	}

	stranded, err := StrandedCommitCount(ctx, repoPath)
	if err != nil {
		return "", fmt.Errorf("counting stranded commits: %w", err)
	}
	if stranded == 0 {
		return "", ErrNoStrandedCommits
	}

	headSHA := captureHeadSHA(ctx, repoPath)
	rescueRef := rescueBranchPrefix + time.Now().UTC().Format("20060102-150405")
	if out, err := rescueGit(ctx, repoPath, "branch", rescueRef, "HEAD"); err != nil {
		return "", fmt.Errorf("creating rescue branch %s: %w (%s)", rescueRef, err, out)
	}
	rescueSHA, err := resolveRef(ctx, repoPath, rescueRef)
	if err != nil {
		return "", fmt.Errorf("rescue branch %s did not resolve after creation: %w", rescueRef, err)
	}

	logger.Warn("ledger_rescue_branch_created",
		"op", "rescue_branch_created", "repo", repoPath, "reason", reason,
		"rescue_ref", rescueRef, "rescue_sha", rescueSHA,
		"stranded_commits", stranded, "head_sha", headSHA)

	return rescueRef, nil
}

// StrandedCommitCount returns how many commits are reachable from HEAD but from
// no branch and no remote-tracking ref — that is, commits that would become
// unreferenced if HEAD moved.
//
// This is the alarm that went unrung for six weeks in bd ox-akab: session
// commits kept landing on a detached HEAD while every ref that anyone reads
// stayed behind. A non-zero value here means data exists in exactly one place.
func StrandedCommitCount(ctx context.Context, repoPath string) (int, error) {
	out, err := rescueGit(ctx, repoPath, "rev-list", "--count", "HEAD", "--not", "--branches", "--remotes")
	if err != nil {
		return 0, fmt.Errorf("git rev-list: %w (%s)", err, out)
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("parsing rev-list count %q: %w", strings.TrimSpace(out), err)
	}
	return n, nil
}

// resolveRef returns the object id a ref points at, or an error if it does not
// resolve. Used to prove a rescue branch exists rather than assuming it does.
func resolveRef(ctx context.Context, repoPath, ref string) (string, error) {
	out, err := rescueGit(ctx, repoPath, "rev-parse", "--verify", "-q", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("git rev-parse %s: %w", ref, err)
	}
	sha := strings.TrimSpace(out)
	if sha == "" {
		return "", fmt.Errorf("ref %s resolved to nothing", ref)
	}
	return sha, nil
}

// commitCount returns the number of commits reachable from ref.
func commitCount(ctx context.Context, repoPath, ref string) (int, error) {
	out, err := rescueGit(ctx, repoPath, "rev-list", "--count", ref)
	if err != nil {
		return 0, fmt.Errorf("git rev-list --count %s: %w (%s)", ref, err, out)
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("parsing count %q: %w", strings.TrimSpace(out), err)
	}
	return n, nil
}

// rescueGit runs a git command in repoPath and returns its combined output.
func rescueGit(ctx context.Context, repoPath string, args ...string) (string, error) {
	full := append([]string{"-C", repoPath}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
