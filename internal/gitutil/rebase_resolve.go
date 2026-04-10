package gitutil

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// ResolveRebaseAcceptTheirs attempts to resolve a rebase conflict by accepting
// the incoming version of all conflicted files, but ONLY if every conflicted
// file is under one of the given safe prefixes and NOT under any deny prefix.
//
// This is safe for data directories (like data/) where the content is derived
// from an external source and the next sync cycle will re-fetch the latest
// version anyway. Last-write-wins is the correct strategy.
//
// The denyPrefixes parameter is optional — pass nil for no exclusions.
// Deny prefixes carve out exceptions from the safe set: e.g., safePrefixes
// ["data/"] with denyPrefixes ["data/proprietary/"] means data/github/prs.json
// is safe but data/proprietary/keys.json is not.
//
// Handles rename/rename and rename/delete conflicts by using git ls-files
// --unmerged to detect index stages, then resolving via git rm + git add
// instead of git checkout --theirs (which fails for rename conflicts).
//
// Returns nil if the rebase was successfully continued after resolution.
// Returns an error if any conflicted file fails the safety check (the
// rebase is NOT aborted — caller should abort if needed).
func ResolveRebaseAcceptTheirs(ctx context.Context, repoPath string, safePrefixes []string, denyPrefixes ...[]string) error {
	var denies []string
	if len(denyPrefixes) > 0 {
		denies = denyPrefixes[0]
	}

	// list all unmerged files from the index (handles content, rename, and delete conflicts)
	entries, err := listUnmergedEntries(ctx, repoPath)
	if err != nil {
		return fmt.Errorf("list unmerged entries: %w", err)
	}
	if len(entries) == 0 {
		return fmt.Errorf("no conflicted files found")
	}

	// collect all unique file paths across all stages
	allPaths := make(map[string]bool)
	for _, e := range entries {
		allPaths[e.path] = true
	}

	// verify all conflicted files are under safe prefixes and not denied
	for path := range allPaths {
		if !matchesSafePrefix(path, safePrefixes, denies) {
			return fmt.Errorf("conflicted file %q is not under safe auto-resolve prefixes %v", path, safePrefixes)
		}
	}

	// group entries by path to understand conflict type
	fileStages := groupByPath(entries)

	// resolve each conflicted file based on its conflict type
	var toCheckoutTheirs []string // content conflicts: use checkout --theirs
	var toAdd []string            // rename conflicts: stage the file as-is
	var toRemove []string         // base-only: file deleted in both branches

	for path, stages := range fileStages {
		hasBase := stages[1]   // stage 1: common ancestor
		hasTheirs := stages[2] // stage 2: version on branch we're rebasing onto
		hasOurs := stages[3]   // stage 3: version from commit being replayed

		switch {
		case hasBase && hasTheirs && hasOurs:
			// content conflict: all three stages exist for the same path.
			// working tree has conflict markers — use checkout --theirs to
			// pick a clean version (during rebase, "theirs" = commit being replayed)
			toCheckoutTheirs = append(toCheckoutTheirs, path)
		case hasOurs && !hasBase:
			// rename conflict: ours exists but no base at this path.
			// this is the new name from our rename — just stage it
			toAdd = append(toAdd, path)
		case hasOurs:
			// rename/rename variant: ours exists with base — stage it
			toAdd = append(toAdd, path)
		case hasTheirs && !hasOurs:
			// rename/delete: theirs exists but ours was deleted/renamed away.
			// accept theirs (the version on the target branch)
			toAdd = append(toAdd, path)
		case hasBase && !hasTheirs && !hasOurs:
			// base-only: file was deleted in both branches (common in rename scenarios
			// where the original file no longer exists in either branch)
			toRemove = append(toRemove, path)
		}
	}

	// resolve content conflicts by checking out the "theirs" version
	if len(toCheckoutTheirs) > 0 {
		checkoutArgs := append([]string{"checkout", "--theirs", "--"}, toCheckoutTheirs...)
		if _, err := RunGit(ctx, repoPath, checkoutArgs...); err != nil {
			return fmt.Errorf("checkout --theirs: %w", err)
		}
		addArgs := append([]string{"add", "--"}, toCheckoutTheirs...)
		if _, err := RunGit(ctx, repoPath, addArgs...); err != nil {
			return fmt.Errorf("git add after checkout --theirs: %w", err)
		}
	}

	// remove files that exist only in base (deleted in both branches)
	if len(toRemove) > 0 {
		rmArgs := append([]string{"rm", "--cached", "--quiet", "--"}, toRemove...)
		if _, err := RunGit(ctx, repoPath, rmArgs...); err != nil {
			return fmt.Errorf("git rm --cached: %w", err)
		}
	}

	// stage rename-conflict files that should be kept
	if len(toAdd) > 0 {
		addArgs := append([]string{"add", "--"}, toAdd...)
		if _, err := RunGit(ctx, repoPath, addArgs...); err != nil {
			return fmt.Errorf("git add resolved files: %w", err)
		}
	}

	// continue the rebase
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "rebase", "--continue")
	cmd.Dir = repoPath
	// GIT_EDITOR=true prevents git from opening an editor for the commit message
	cmd.Env = append(cmd.Environ(), "GIT_EDITOR=true")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("rebase --continue: %s: %w", strings.TrimSpace(string(output)), err)
	}

	return nil
}

// unmergedEntry represents one stage of an unmerged file in the git index.
type unmergedEntry struct {
	stage int    // 1=base, 2=theirs (target branch), 3=ours (commit being replayed)
	path  string
}

// listUnmergedEntries returns all unmerged entries from the git index.
// Uses git ls-files --unmerged which correctly reports rename/rename,
// rename/delete, and content conflicts — unlike git diff --diff-filter=U
// which can miss files in rename conflict scenarios.
func listUnmergedEntries(ctx context.Context, repoPath string) ([]unmergedEntry, error) {
	out, err := RunGit(ctx, repoPath, "ls-files", "--unmerged")
	if err != nil {
		return nil, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}

	var entries []unmergedEntry
	for _, line := range strings.Split(out, "\n") {
		// format: <mode> <hash> <stage>\t<path>
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		path := parts[1]

		// parse stage from the metadata portion: "<mode> <hash> <stage>"
		meta := strings.Fields(parts[0])
		if len(meta) < 3 {
			continue
		}
		stage := 0
		switch meta[2] {
		case "1":
			stage = 1
		case "2":
			stage = 2
		case "3":
			stage = 3
		default:
			continue
		}

		entries = append(entries, unmergedEntry{stage: stage, path: path})
	}

	return entries, nil
}

// groupByPath groups unmerged entries by file path, returning a map of
// path -> stages present (indexed by stage number).
func groupByPath(entries []unmergedEntry) map[string]map[int]bool {
	result := make(map[string]map[int]bool)
	for _, e := range entries {
		if result[e.path] == nil {
			result[e.path] = make(map[int]bool)
		}
		result[e.path][e.stage] = true
	}
	return result
}

// listConflictedFiles returns the list of files with unresolved merge conflicts.
// Kept for backward compatibility but prefer listUnmergedEntries for rename-aware resolution.
func listConflictedFiles(ctx context.Context, repoPath string) ([]string, error) {
	out, err := RunGit(ctx, repoPath, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// matchesSafePrefix checks if a file path is safe to auto-resolve.
// Uses most-specific-prefix-wins semantics: the longest matching prefix
// determines the outcome. This mirrors manifest.ResolveModeFor — e.g.,
// allow "data/", deny "data/proprietary/", allow "data/proprietary/public/"
// correctly resolves data/proprietary/public/readme.md as safe.
func matchesSafePrefix(path string, prefixes []string, denyPrefixes []string) bool {
	bestLen := 0
	safe := false
	for _, prefix := range prefixes {
		if strings.HasPrefix(path, prefix) && len(prefix) > bestLen {
			bestLen = len(prefix)
			safe = true
		}
	}
	for _, deny := range denyPrefixes {
		if strings.HasPrefix(path, deny) && len(deny) >= bestLen {
			bestLen = len(deny)
			safe = false
		}
	}
	return safe
}
