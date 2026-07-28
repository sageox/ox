package gitutil

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
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
// A rebase replays commits one at a time, so `rebase --continue` stops at EVERY
// conflicting commit in the range. maxResolvePasses bounds the resulting loop.
// A ledger that has been wedged for weeks can carry hundreds of conflicting
// commits (the production incident had 344 replayed commits, 281 conflicts), so
// this must be generous — but still finite, because a pass that resolves nothing
// would otherwise spin forever.
const maxResolvePasses = 5000

func ResolveRebaseAcceptTheirs(ctx context.Context, repoPath string, safePrefixes []string, denyPrefixes ...[]string) error {
	// Resolve every conflicting commit in the replay range, not just the first.
	//
	// `git rebase --continue` exits NON-ZERO when it commits the current step and
	// then halts on the next conflicting commit. That is progress, not failure —
	// but treating it as an error made the caller abort the rebase, restoring the
	// pre-rebase state and re-wedging the ledger on every single attempt. It only
	// reproduces with multiple SEQUENTIALLY conflicting commits, which is why a
	// single-commit fixture never caught it.
	for pass := 0; ; pass++ {
		if pass >= maxResolvePasses {
			return fmt.Errorf("rebase did not converge after %d resolve passes", maxResolvePasses)
		}
		done, err := resolveOneRebaseStep(ctx, repoPath, safePrefixes, denyPrefixes...)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
	}
}

// resolveOneRebaseStep resolves the conflicts of the current rebase step and
// continues. Returns done=true when the whole rebase has finished.
func resolveOneRebaseStep(ctx context.Context, repoPath string, safePrefixes []string, denyPrefixes ...[]string) (done bool, err error) {
	var denies []string
	if len(denyPrefixes) > 0 {
		denies = denyPrefixes[0]
	}

	// list all unmerged files from the index (handles content, rename, and delete conflicts)
	entries, err := listUnmergedEntries(ctx, repoPath)
	if err != nil {
		return false, fmt.Errorf("list unmerged entries: %w", err)
	}
	if len(entries) == 0 {
		// No conflicts. If a rebase is still running, it halted for some reason
		// OTHER than a conflict — most commonly a replayed commit that became
		// empty because its changes are already upstream (git stops here when
		// rebase.empty=stop, and older git versions stop by default).
		//
		// Returning an error here would be wrong: the caller aborts on error,
		// which restores the pre-rebase state and re-wedges the ledger. There is
		// nothing to resolve, so advance the rebase instead.
		if IsRebaseInProgress(repoPath) {
			return advanceNonConflictRebaseStep(ctx, repoPath)
		}
		return false, fmt.Errorf("no conflicted files found")
	}

	// collect all unique file paths across all stages
	allPaths := make(map[string]bool)
	for _, e := range entries {
		allPaths[e.path] = true
	}

	// verify all conflicted files are under safe prefixes and not denied
	for path := range allPaths {
		if !matchesSafePrefix(path, safePrefixes, denies) {
			return false, fmt.Errorf("conflicted file %q is not under safe auto-resolve prefixes %v", path, safePrefixes)
		}
	}

	// group entries by path to understand conflict type
	fileStages := groupByPath(entries)

	// resolve each conflicted file based on its conflict type
	var toCheckoutTheirs []string // content conflicts: use checkout --theirs
	var toCheckoutOurs []string   // content conflicts where the pointer side must win
	var toAdd []string            // rename conflicts: stage the file as-is
	var toRemove []string         // base-only: file deleted in both branches

	for path, stages := range fileStages {
		hasBase := stages[1]   // stage 1: common ancestor
		hasTheirs := stages[2] // stage 2: version on branch we're rebasing onto
		hasOurs := stages[3]   // stage 3: version from commit being replayed

		switch {
		case hasTheirs && hasOurs:
			// Both sides have content at this path. Two shapes land here:
			//   - content conflict (base present): both modified the same file
			//   - add/add conflict (no base): both CREATED the same file
			//
			// add/add used to fall through to the rename branch below, which
			// stages the working-tree file as-is — conflict markers and all.
			// That committed literal "<<<<<<< HEAD" into session artifacts, and
			// it is the common shape when the cloud summarizer and the local CLI
			// both write a brand-new session.md for the same session.
			//
			// Pointer-wins takes precedence over the positional rule. When one
			// side is an LFS pointer and the other is hydrated bytes, this is
			// not a content disagreement at all — it is a hydration-state
			// disagreement, and the pointer is the only correct committed form
			// (.claude/rules/cache-only-design.md). Committing the hydrated side
			// permanently breaks pushes with "LFS objects are missing".
			//
			// Pointer-wins is also commutative and idempotent, so unlike the
			// positional rule it converges regardless of which replica resolves
			// first — that property is what stops a resolution from re-diverging.
			if pointerWinsStage(ctx, repoPath, path) == 2 {
				toCheckoutOurs = append(toCheckoutOurs, path)
				continue
			}
			// Otherwise the positional rule: checkout --theirs picks stage 3,
			// which during a rebase is the commit being replayed (the LOCAL side).
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
			return false, fmt.Errorf("checkout --theirs: %w", err)
		}
		addArgs := append([]string{"add", "--"}, toCheckoutTheirs...)
		if _, err := RunGit(ctx, repoPath, addArgs...); err != nil {
			return false, fmt.Errorf("git add after checkout --theirs: %w", err)
		}
	}

	// resolve pointer-wins conflicts by taking stage 2 (the branch being rebased
	// onto). Safe here because these are content conflicts, so all three stages
	// exist and --ours cannot hit the modify/delete case where it errors out.
	if len(toCheckoutOurs) > 0 {
		checkoutArgs := append([]string{"checkout", "--ours", "--"}, toCheckoutOurs...)
		if _, err := RunGit(ctx, repoPath, checkoutArgs...); err != nil {
			return false, fmt.Errorf("checkout --ours (pointer wins): %w", err)
		}
		addArgs := append([]string{"add", "--"}, toCheckoutOurs...)
		if _, err := RunGit(ctx, repoPath, addArgs...); err != nil {
			return false, fmt.Errorf("git add after checkout --ours: %w", err)
		}
	}

	// remove files that exist only in base (deleted in both branches)
	if len(toRemove) > 0 {
		rmArgs := append([]string{"rm", "--cached", "--quiet", "--"}, toRemove...)
		if _, err := RunGit(ctx, repoPath, rmArgs...); err != nil {
			return false, fmt.Errorf("git rm --cached: %w", err)
		}
	}

	// stage rename-conflict files that should be kept
	if len(toAdd) > 0 {
		addArgs := append([]string{"add", "--"}, toAdd...)
		if _, err := RunGit(ctx, repoPath, addArgs...); err != nil {
			return false, fmt.Errorf("git add resolved files: %w", err)
		}
	}

	// continue the rebase
	_, contErr := runRebaseStep(ctx, repoPath, "--continue")

	// A non-zero exit here does NOT imply failure. git returns non-zero when it
	// commits this step and then halts on the NEXT conflicting commit. The only
	// reliable signal is the repo state: if the rebase is over, we are done; if
	// it is still running, there is another step to resolve.
	if !IsRebaseInProgress(repoPath) {
		return true, nil
	}
	if contErr == nil {
		return false, nil // advanced cleanly to the next step
	}
	// Still mid-rebase AND continue errored: fresh conflicts mean real progress.
	next, listErr := listUnmergedEntries(ctx, repoPath)
	if listErr == nil && len(next) > 0 {
		return false, nil
	}
	// Halted with nothing to resolve (e.g. the step became empty) — let the
	// non-conflict advancer decide between --continue and --skip rather than
	// erroring out, which would make the caller abort and re-wedge the ledger.
	return advanceNonConflictRebaseStep(ctx, repoPath)
}

// advanceNonConflictRebaseStep moves a rebase past a step that halted with
// nothing to resolve — normally a replayed commit whose changes are already
// upstream, so replaying it would produce an empty commit.
//
// It tries `--continue` first and only escalates to `--skip` when git itself
// says the step is empty. It deliberately never skips blindly: `--skip` DROPS
// the current commit, so guessing here would silently discard a real session.
func advanceNonConflictRebaseStep(ctx context.Context, repoPath string) (done bool, err error) {
	contOut, contErr := runRebaseStep(ctx, repoPath, "--continue")
	if !IsRebaseInProgress(repoPath) {
		return true, nil
	}
	if contErr == nil {
		return false, nil // advanced to the next step
	}

	// Decide "is this step empty?" STRUCTURALLY, not by reading git's prose.
	// Message matching would break the moment git is running under a non-English
	// locale (or rewords a hint), and the failure mode is silent: we would report
	// "nothing to resolve", the caller would abort, and the ledger would re-wedge.
	// runRebaseStep pins LC_ALL=C so the text is stable, but the structural check
	// is what we actually trust; the text is only a fallback for odd git builds.
	//
	// A replayed commit is empty exactly when nothing is staged and nothing is
	// conflicted: git has already applied the changes and found no delta.
	//
	// This is the ONLY signal. An earlier revision also accepted git's prose as a
	// fallback, which was strictly unsafe: a pre-commit hook that rejects
	// --continue while real changes are still staged can easily emit "--skip" or
	// "nothing to commit" in its own output, and that would have overridden the
	// structural check and silently dropped a genuine commit. `--skip` DISCARDS
	// work, so the only tolerable error direction here is refusing to skip.
	if !rebaseStepIsEmpty(ctx, repoPath) {
		return false, fmt.Errorf("rebase halted with nothing to resolve: %s",
			SanitizeOutput(strings.TrimSpace(contOut)))
	}

	skipOut, skipErr := runRebaseStep(ctx, repoPath, "--skip")
	if !IsRebaseInProgress(repoPath) {
		return true, nil
	}
	if skipErr != nil {
		return false, fmt.Errorf("rebase --skip: %s: %w",
			SanitizeOutput(strings.TrimSpace(skipOut)), skipErr)
	}
	return false, nil
}

// rebaseStepIsEmpty reports whether the halted rebase step would produce an
// empty commit: nothing staged AND nothing conflicted. Language-independent.
func rebaseStepIsEmpty(ctx context.Context, repoPath string) bool {
	if unmerged, err := listUnmergedEntries(ctx, repoPath); err != nil || len(unmerged) > 0 {
		return false
	}
	// `diff --cached --quiet` exits 0 when the index matches HEAD (nothing to
	// commit) and 1 when there are staged changes.
	_, err := RunGit(ctx, repoPath, "diff", "--cached", "--quiet")
	return err == nil
}

// runRebaseStep runs a `git rebase <arg>` with the editor disabled so git never
// blocks waiting for a commit message, and with the C locale pinned so any
// diagnostic we do read is stable regardless of the user's language settings.
//
// commit.gpgsign=false / tag.gpgsign=false mirror RunGit's hardening (run.go).
// `--continue` COMMITS, so a repo whose config enables passphrase-protected
// signing makes it hang or die on a TTY-less daemon. The rebase is then left
// halted with a clean, conflict-free index — indistinguishable from the
// upstream-equivalent-commit halt that advanceNonConflictRebaseStep skips, and
// skipping it would discard a resolution that was staged but never recorded.
// Matters more now that this runs in a loop that can fire hundreds of times.
func runRebaseStep(ctx context.Context, repoPath, arg string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath,
		"-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false", "rebase", arg)
	cmd.Dir = repoPath
	cmd.Env = append(cmd.Environ(), "GIT_EDITOR=true", "LC_ALL=C", "LANG=C")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// lfsPointerPrefix is the first line of every Git LFS pointer file.
//
// Duplicated from internal/lfs rather than imported: internal/lfs depends on
// gitutil, so importing it back would create a cycle. The value is fixed by the
// LFS spec (https://github.com/git-lfs/git-lfs/blob/main/docs/spec.md), so it
// cannot drift.
const lfsPointerPrefix = "version https://git-lfs.github.com/spec/v1"

// maxLFSPointerSize bounds how many bytes we read to classify a blob. Real
// pointers are ~130 bytes; anything larger is content by definition.
const maxLFSPointerSize = 200

// isLFSPointerBlob reports whether the given blob content is a COMPLETE, valid
// LFS pointer.
//
// The full shape is required, not just the version prefix. A truncated pointer
// — or a short artifact that merely starts with the spec line — must not win a
// conflict against valid content: downstream LFS parsing would reject it, so
// "winning" would mean committing an unusable blob over a good one. Mirrors the
// validation in lfs.ParsePointer, which gitutil cannot import (lfs depends on
// gitutil, so the import would cycle).
func isLFSPointerBlob(content string) bool {
	if len(content) > maxLFSPointerSize || !strings.HasPrefix(content, lfsPointerPrefix) {
		return false
	}
	var haveOID, haveSize bool
	for _, line := range strings.Split(strings.TrimSpace(content), "\n") {
		switch {
		case strings.HasPrefix(line, "oid "):
			// require a non-empty, hex-shaped digest with its algorithm prefix
			oid := strings.TrimPrefix(line, "oid ")
			if !strings.HasPrefix(oid, "sha256:") {
				return false
			}
			digest := strings.TrimPrefix(oid, "sha256:")
			if digest == "" || strings.ContainsAny(digest, " \t") {
				return false
			}
			haveOID = true
		case strings.HasPrefix(line, "size "):
			size, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "size ")), 10, 64)
			if err != nil || size <= 0 {
				return false
			}
			haveSize = true
		}
	}
	return haveOID && haveSize
}

// pointerWinsStage decides a content conflict where the two sides disagree
// about HYDRATION STATE rather than content: one side is an LFS pointer, the
// other is the hydrated bytes.
//
// Returns the index stage to keep (2 = branch being rebased onto, 3 = commit
// being replayed), or 0 when both sides are the same kind and the caller should
// fall back to its normal rule.
//
// Why the pointer always wins: a hydrated file committed in place breaks the
// LFS linkage, and every subsequent push is rejected with "LFS objects are
// missing" — a permanent, repo-wide wedge (see .claude/rules/cache-only-design.md
// and the 2026-04-25 incident). The pointer side loses nothing, because the
// bytes it references are already in the LFS store.
//
// The rule is a semilattice join — commutative, associative, idempotent — so
// two replicas resolving the same conflict independently reach the same answer.
// The positional rule (always take the replaying side) has no such property and
// therefore has no fixed point.
func pointerWinsStage(ctx context.Context, repoPath, path string) int {
	onto, err := readIndexStage(ctx, repoPath, 2, path)
	if err != nil {
		return 0
	}
	replayed, err := readIndexStage(ctx, repoPath, 3, path)
	if err != nil {
		return 0
	}

	ontoIsPointer := isLFSPointerBlob(onto)
	replayedIsPointer := isLFSPointerBlob(replayed)
	switch {
	case ontoIsPointer && !replayedIsPointer:
		return 2
	case replayedIsPointer && !ontoIsPointer:
		return 3
	default:
		return 0 // same kind on both sides — no opinion
	}
}

// readIndexStage returns the blob content for one stage of a conflicted path.
func readIndexStage(ctx context.Context, repoPath string, stage int, path string) (string, error) {
	return RunGit(ctx, repoPath, "cat-file", "blob", fmt.Sprintf(":%d:%s", stage, path))
}

// unmergedEntry represents one stage of an unmerged file in the git index.
type unmergedEntry struct {
	stage int // 1=base, 2=theirs (target branch), 3=ours (commit being replayed)
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
