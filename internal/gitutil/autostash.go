package gitutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// leftoverAutostashMessage names the stash-list entry a conflicting leftover
// is parked under, so a human can find it with `git stash list`.
const leftoverAutostashMessage = "ox: autostash restored after an interrupted pull"

// RestoreLeftoverAutostash puts back a working-tree change that an
// interrupted `pull --autostash` stranded. git creates the autostash, writes
// MERGE_AUTOSTASH, and only on completion applies it and deletes the ref; a
// pull killed in between (a deadline, a crash) leaves the ref and the change
// exists nowhere else. Every later `pull --autostash` then fails with
// "cannot lock ref 'MERGE_AUTOSTASH': reference already exists".
//
// The change is applied back into the tree and the ref deleted. When it no
// longer applies cleanly it is stored in the stash list instead, under
// leftoverAutostashMessage, and any unmerged entries the attempt left are
// the caller's to resolve. Nothing is discarded in either case. A ref that
// belongs to an active rebase or merge is left alone. Returns whether a
// leftover was found.
func RestoreLeftoverAutostash(ctx context.Context, repoPath string) (bool, error) {
	refCtx, refCancel := context.WithTimeout(ctx, 10*time.Second)
	sha, err := RunGit(refCtx, repoPath, "rev-parse", "-q", "--verify", "MERGE_AUTOSTASH")
	refCancel()
	if err != nil {
		return false, nil // no leftover
	}
	sha = strings.TrimSpace(sha)
	for _, marker := range []string{"rebase-merge", "rebase-apply", "MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD"} {
		if _, statErr := os.Stat(filepath.Join(repoPath, ".git", marker)); !errors.Is(statErr, os.ErrNotExist) {
			return false, nil // the autostash belongs to the operation in flight
		}
	}

	applyCtx, applyCancel := context.WithTimeout(ctx, 2*time.Minute)
	_, applyErr := RunGit(applyCtx, repoPath, "stash", "apply", sha)
	applyCancel()
	if applyErr != nil {
		// keep the bytes reachable by name before the ref goes
		storeCtx, storeCancel := context.WithTimeout(ctx, 10*time.Second)
		_, storeErr := RunGit(storeCtx, repoPath, "stash", "store", "-m", leftoverAutostashMessage, sha)
		storeCancel()
		if storeErr != nil {
			return true, fmt.Errorf("leftover autostash %s did not apply (%w) and could not be stored: %w", sha[:12], applyErr, storeErr)
		}
	}
	delCtx, delCancel := context.WithTimeout(ctx, 10*time.Second)
	_, delErr := RunGit(delCtx, repoPath, "update-ref", "-d", "MERGE_AUTOSTASH", sha)
	delCancel()
	if delErr != nil {
		return true, fmt.Errorf("clear MERGE_AUTOSTASH: %w", delErr)
	}
	return true, nil
}
