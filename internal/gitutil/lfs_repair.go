package gitutil

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RepairMissingLFSObjects detects LFS pointer files whose backing objects are
// missing locally (lost during GC reclone, interrupted push, etc.) and replaces
// them with empty content so future pushes aren't blocked by the remote's
// pre-receive hook rejecting missing LFS objects.
//
// The repair:
//  1. Detects missing LFS objects via `git lfs ls-files`
//  2. Replaces each orphaned pointer with empty content
//  3. Commits the fix
//  4. Sets lfs.allowincompletepush=true so the local pre-push hook doesn't block
//     (the server may still reject — caller should retry via rebase)
//
// Returns the number of repaired files and any error. Safe to call on repos
// without LFS — returns (0, nil) immediately.
func RepairMissingLFSObjects(ctx context.Context, repoPath string) (int, error) {
	// quick check: is LFS configured?
	gitAttrPath := filepath.Join(repoPath, ".gitattributes")
	if _, err := os.Stat(gitAttrPath); os.IsNotExist(err) {
		return 0, nil
	}

	missingFiles, err := findMissingLFSFiles(ctx, repoPath)
	if err != nil {
		return 0, err
	}
	if len(missingFiles) == 0 {
		return 0, nil
	}

	slog.Info("repairing missing LFS objects", "count", len(missingFiles), "repo", repoPath)

	// for each missing file, replace the LFS pointer with empty content
	// and stage the change
	repaired := 0
	for _, relPath := range missingFiles {
		absPath := filepath.Join(repoPath, relPath)

		// verify it's actually an LFS pointer (small file starting with "version https://git-lfs")
		content, err := os.ReadFile(absPath)
		if err != nil {
			slog.Debug("lfs repair: cannot read file", "path", relPath, "error", err)
			continue
		}
		if !strings.HasPrefix(string(content), "version https://git-lfs") {
			continue // not a pointer file
		}

		// replace with empty content — the original data is irrecoverably lost
		if err := os.WriteFile(absPath, []byte{}, 0o644); err != nil {
			slog.Warn("lfs repair: cannot write empty file", "path", relPath, "error", err)
			continue
		}

		// stage the change
		addCtx, addCancel := context.WithTimeout(ctx, 5*time.Second)
		_, addErr := RunGit(addCtx, repoPath, "add", "--sparse", relPath)
		addCancel()
		if addErr != nil {
			slog.Warn("lfs repair: cannot stage file", "path", relPath, "error", addErr)
			continue
		}

		repaired++
		slog.Info("lfs repair: replaced missing pointer with empty stub", "path", relPath)
	}

	if repaired == 0 {
		return 0, nil
	}

	// allow incomplete pushes — the old LFS OIDs still exist in git history
	// even though the pointer files are now empty. Without this, the pre-push
	// hook will still reject the push for the historical references.
	cfgCtx, cfgCancel := context.WithTimeout(ctx, 5*time.Second)
	_, _ = RunGit(cfgCtx, repoPath, "config", "lfs.allowincompletepush", "true")
	cfgCancel()

	// commit the repairs
	commitCtx, commitCancel := context.WithTimeout(ctx, 10*time.Second)
	defer commitCancel()
	msg := fmt.Sprintf("fix: replace %d missing LFS pointers with empty stubs", repaired)
	_, commitErr := RunGit(commitCtx, repoPath, "commit", "-m", msg, "--no-verify")
	if commitErr != nil {
		return repaired, fmt.Errorf("commit LFS repairs: %w", commitErr)
	}

	// squash all unpushed commits into one to eliminate historical LFS references.
	// GitLab's pre-receive hook checks ALL commits in the push range, not just HEAD.
	// Without squashing, old commits still reference the missing LFS OIDs.
	if sqErr := squashUnpushedCommits(ctx, repoPath, msg); sqErr != nil {
		slog.Warn("lfs repair: squash failed, push may still fail due to historical LFS references", "error", sqErr)
	}

	slog.Info("lfs repair complete", "repaired", repaired)
	return repaired, nil
}

// squashUnpushedCommits collapses all unpushed local commits into a single
// commit. This is necessary after LFS repair because the repair only fixes
// HEAD — historical commits still reference missing LFS OIDs that would cause
// the remote's pre-receive hook to reject the push.
func squashUnpushedCommits(ctx context.Context, repoPath, commitMsg string) error {
	// find upstream tracking ref
	upCtx, upCancel := context.WithTimeout(ctx, 5*time.Second)
	upstream, err := RunGit(upCtx, repoPath, "rev-parse", "--verify", "@{upstream}")
	upCancel()
	if err != nil {
		return fmt.Errorf("no upstream tracking ref: %w", err)
	}
	upstream = strings.TrimSpace(upstream)

	// check there are unpushed commits
	countCtx, countCancel := context.WithTimeout(ctx, 5*time.Second)
	countOut, err := RunGit(countCtx, repoPath, "rev-list", "--count", upstream+"..HEAD")
	countCancel()
	count := strings.TrimSpace(countOut)
	if err != nil || count == "0" || count == "1" {
		return nil // nothing to squash or already a single commit
	}

	slog.Info("lfs repair: squashing unpushed commits", "count", count, "upstream", upstream[:12])

	// soft reset to upstream (preserves working tree and index)
	resetCtx, resetCancel := context.WithTimeout(ctx, 5*time.Second)
	_, err = RunGit(resetCtx, repoPath, "reset", "--soft", upstream)
	resetCancel()
	if err != nil {
		return fmt.Errorf("reset --soft: %w", err)
	}

	// create a single squashed commit
	squashCtx, squashCancel := context.WithTimeout(ctx, 10*time.Second)
	_, err = RunGit(squashCtx, repoPath, "commit", "-m", commitMsg, "--no-verify")
	squashCancel()
	if err != nil {
		return fmt.Errorf("squash commit: %w", err)
	}

	return nil
}

// findMissingLFSFiles returns paths of LFS-tracked files whose backing objects
// are missing locally. Returns nil if LFS is not installed or no files are missing.
func findMissingLFSFiles(ctx context.Context, repoPath string) ([]string, error) {
	lsCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	output, err := RunGit(lsCtx, repoPath, "lfs", "ls-files", "--long")
	if err != nil {
		// git-lfs not installed or not an LFS repo — skip silently
		if strings.Contains(output, "not found") || strings.Contains(err.Error(), "not found") {
			return nil, nil
		}
		return nil, fmt.Errorf("git lfs ls-files: %w", err)
	}

	if strings.TrimSpace(output) == "" {
		return nil, nil
	}

	// parse output: each line is "<oid> <status> <path>"
	// status: '*' = present locally, '-' = missing locally
	var missing []string
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, " ", 3)
		if len(parts) < 3 {
			continue
		}
		if parts[1] == "-" {
			missing = append(missing, parts[2])
		}
	}
	return missing, nil
}

// HasMissingLFSObjects returns true if the repo has LFS-tracked files whose
// backing objects are missing locally.
func HasMissingLFSObjects(ctx context.Context, repoPath string) bool {
	missing, err := findMissingLFSFiles(ctx, repoPath)
	return err == nil && len(missing) > 0
}

// IsLFSPushError returns true if the error output indicates a push failure
// caused by missing LFS objects (either local pre-push hook or server-side).
// "failed to store" alone is NOT sufficient — macOS Keychain errors like
// "failed to store: -25300" (errSecItemNotFound) would false-positive.
func IsLFSPushError(output string) bool {
	return strings.Contains(output, "LFS objects are missing") ||
		strings.Contains(output, "missing or corrupt local objects") ||
		(strings.Contains(output, "LFS upload") && strings.Contains(output, "missing")) ||
		(strings.Contains(output, "LFS") && strings.Contains(output, "failed to store"))
}
