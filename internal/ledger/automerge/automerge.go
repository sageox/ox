// Package automerge resolves git merge conflicts in tiers. It is invoked
// after a `git pull --rebase` halts on conflicts, before the caller falls
// back to aborting the rebase.
//
// Tiers (in order):
//  1. Union — for paths whose .gitattributes already declares merge=union,
//     git's union driver should have produced a clean working-tree result.
//     This tier verifies (no remaining conflict markers) and stages.
//  2. Accept-theirs — for paths under explicit safe prefixes. Delegates to
//     gitutil.ResolveRebaseAcceptTheirs, which handles content/rename/delete
//     conflicts and continues the rebase.
//  3. LLM — semantic merge via the `claude` CLI binary. Bounded by size and
//     timeout. Skipped if the binary is unavailable.
//
// Each tier is conservative: a tier that can't safely resolve every
// remaining conflicted path returns control to the next tier. The Resolver
// only calls `git rebase --continue` once all conflicts of the current step
// have been staged, and repeats the whole cycle until the rebase is over — a
// replay range can hold many sequentially conflicting commits.
package automerge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/gitutil"
)

// ErrLLMUnavailable is returned by tier helpers when no LLM binary is
// configured or discoverable on PATH. It is a sentinel: callers can decide
// whether to treat it as a hard failure or as "skip this tier."
var ErrLLMUnavailable = errors.New("automerge: LLM binary not available")

// ErrNoConflicts is returned by Resolve when the repo isn't actually mid-
// conflict. Callers should generally check earlier (this is a cheap guard).
var ErrNoConflicts = errors.New("automerge: no conflicted files in index")

// Options configures a Resolver. Zero values are sensible defaults.
type Options struct {
	// LLMBinary is the path or name of the LLM CLI to invoke for semantic
	// merges. Empty disables the LLM tier. The binary is expected to behave
	// like `claude --output-format stream-json --permission-mode bypassPermissions -p <prompt>`.
	LLMBinary string

	// LLMTimeout is the per-file deadline for an LLM merge. Default 60s.
	LLMTimeout time.Duration

	// SafePrefixes is forwarded to the accept-theirs tier. A path under any
	// of these prefixes (and not under a deny prefix) may be resolved by
	// taking the incoming version.
	SafePrefixes []string

	// SafeDenyPrefixes carves exceptions out of SafePrefixes (most-specific
	// prefix wins). Optional.
	SafeDenyPrefixes []string

	// MaxLLMFileBytes caps the size of any single conflicted file the LLM
	// tier will attempt. Files above this fall through to error. Default 50KB.
	MaxLLMFileBytes int

	// Logger receives structured progress events. nil → discard.
	Logger *slog.Logger
}

// Resolver drives the tiered conflict-resolution flow.
type Resolver struct {
	opts   Options
	logger *slog.Logger
	// runLLM is the function used to invoke the LLM. Tests inject a fake.
	runLLM llmRunner
}

// llmRunner is the seam between the resolver and a real subprocess. It
// receives the assembled prompt and returns the merged file content.
type llmRunner func(ctx context.Context, binary, prompt string) (string, error)

const (
	defaultLLMTimeout      = 60 * time.Second
	defaultMaxLLMFileBytes = 50 * 1024
)

// maxResolvePasses bounds the resolve loop. A rebase halts once per
// conflicting commit, so a ledger that has been wedged for weeks can carry
// hundreds of them — the production incident behind ResolveRebaseAcceptTheirs
// replayed 344 commits and hit 281 conflicts. Generous, but finite: a pass
// that resolves nothing would otherwise spin forever.
const maxResolvePasses = 5000

// New constructs a Resolver from Options.
func New(opts Options) *Resolver {
	if opts.LLMTimeout <= 0 {
		opts.LLMTimeout = defaultLLMTimeout
	}
	if opts.MaxLLMFileBytes <= 0 {
		opts.MaxLLMFileBytes = defaultMaxLLMFileBytes
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Resolver{
		opts:   opts,
		logger: logger,
		runLLM: runClaudeBinary,
	}
}

// Resolve carries a repo that is mid-rebase all the way through its replay
// range, resolving each conflicting step in turn.
//
// A rebase halts once per conflicting commit, so resolving only the first one
// leaves the rebase in progress. Callers treat that as a wedge, so Resolve
// loops until the rebase is actually over.
//
// Returns (true, nil) only when the rebase has FINISHED — no rebase state
// remains on disk.
// Returns (false, ErrNoConflicts) if there was nothing to do.
// Returns (false, err) if a tier failed and the caller should abort the
// rebase. The rebase is NOT aborted by this function.
func (r *Resolver) Resolve(ctx context.Context, repoPath string) (bool, error) {
	for pass := 0; ; pass++ {
		if pass >= maxResolvePasses {
			return false, fmt.Errorf("rebase did not converge after %d resolve passes", maxResolvePasses)
		}
		done, err := r.resolveOneStep(ctx, repoPath, pass == 0)
		if err != nil {
			return false, err
		}
		if done {
			return true, nil
		}
	}
}

// resolveOneStep resolves the conflicts of the CURRENT rebase step and
// advances past it. done reports whether the WHOLE rebase finished.
//
// first separates the initial call, where an empty index means the caller had
// nothing to resolve (ErrNoConflicts), from a later pass, where it means the
// rebase halted for a reason no tier can act on.
func (r *Resolver) resolveOneStep(ctx context.Context, repoPath string, first bool) (bool, error) {
	conflicts, err := listConflictedPaths(ctx, repoPath)
	if err != nil {
		return false, fmt.Errorf("list conflicts: %w", err)
	}
	if len(conflicts) == 0 {
		if first {
			return false, ErrNoConflicts
		}
		// Halted with a clean index — no tier has anything to stage, so let
		// continueRebase either finish the rebase or fail honestly.
		return r.continueRebase(ctx, repoPath)
	}

	r.logger.Info("automerge.start", "repo", repoPath, "conflicts", len(conflicts))

	// Tier 1: union — git's union driver runs at merge time, so by the
	// time we get here any union-managed file should already lack conflict
	// markers in the working tree. Verify and stage.
	remaining, err := r.tryUnionTier(ctx, repoPath, conflicts)
	if err != nil {
		return false, fmt.Errorf("union tier: %w", err)
	}
	if len(remaining) == 0 {
		return r.continueRebase(ctx, repoPath)
	}

	// Tier 2: accept-theirs. ResolveRebaseAcceptTheirs requires that ALL
	// remaining conflicts fall under the safe prefixes. If even one path
	// doesn't, it returns an error without modifying state — so we only
	// invoke it when every remaining path is safe.
	if r.allUnderSafePrefixes(remaining) && len(r.opts.SafePrefixes) > 0 {
		r.logger.Info("automerge.tier", "tier", "accept-theirs", "paths", len(remaining))
		if err := gitutil.ResolveRebaseAcceptTheirs(ctx, repoPath, r.opts.SafePrefixes, r.opts.SafeDenyPrefixes); err != nil {
			return false, fmt.Errorf("accept-theirs: %w", err)
		}
		// ResolveRebaseAcceptTheirs runs `git rebase --continue` itself, in a
		// loop over the whole replay range. Trust repo state over that
		// assumption anyway: if anything remains, the outer loop picks it up.
		if gitutil.IsRebaseInProgress(repoPath) {
			return false, nil
		}
		return true, nil
	}

	// Tier 3: LLM merge for the remaining semantic conflicts.
	if r.opts.LLMBinary == "" {
		return false, fmt.Errorf("%w: %d conflict(s) need semantic merge: %v", ErrLLMUnavailable, len(remaining), remaining)
	}
	if err := r.tryLLMTier(ctx, repoPath, remaining); err != nil {
		return false, fmt.Errorf("llm tier: %w", err)
	}

	return r.continueRebase(ctx, repoPath)
}

// continueRebase advances the rebase past the current step and reports whether
// the WHOLE rebase finished.
//
// The verdict comes from repo state, never from the exit code. `git rebase
// --continue` exits NON-ZERO when it commits the current step and then halts on
// the next conflicting commit — that is progress, not failure. Reading it as
// failure made the caller abort the rebase, restoring the pre-rebase state and
// re-wedging the ledger on every attempt. ResolveRebaseAcceptTheirs already
// carries that fix for the accept-theirs tier; the union and LLM tiers reach
// this function instead, and never got it.
//
// GIT_EDITOR=true keeps git from opening an editor for the commit message.
// LC_ALL/LANG pin git's output language, and gpgsign is disabled so a signing
// prompt cannot block a loop that may run hundreds of times.
func (r *Resolver) continueRebase(ctx context.Context, repoPath string) (bool, error) {
	before := rebaseStepID(repoPath)
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath,
		"-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false", "rebase", "--continue")
	cmd.Dir = repoPath
	cmd.Env = append(cmd.Environ(), "GIT_EDITOR=true", "LC_ALL=C", "LANG=C")
	out, err := cmd.CombinedOutput()

	if !gitutil.IsRebaseInProgress(repoPath) {
		r.logger.Info("automerge.done", "repo", repoPath)
		return true, nil
	}
	// Progress is "the rebase moved to a different step", nothing else.
	//
	// Unmerged entries are NOT a progress signal. listConflictedPaths reads the
	// working tree (`git diff --diff-filter=U`) while an unmerged-index probe
	// reads the index (`git ls-files --unmerged`), and the two disagree on
	// rename and delete conflicts. When they disagree, this function sees an
	// unmerged index, calls it progress, and hands back a step no tier staged —
	// so the caller re-enters, git refuses the same step again, and the loop
	// spins to maxResolvePasses burning a git subprocess per pass.
	after := rebaseStepID(repoPath)
	if before != "" && after != "" && after != before {
		return false, nil // the rebase genuinely advanced
	}
	if err == nil {
		return false, nil // git reported success; let the next pass re-probe
	}
	// Same step AND git errored: this step cannot advance. Say so, so the
	// caller aborts and restores a clean state instead of spinning.
	return false, fmt.Errorf("rebase --continue made no progress at step %q: %s: %w",
		after, strings.TrimSpace(string(out)), err)
}

// rebaseStepID identifies the rebase's current step. Empty when it cannot be
// read, which callers must treat as "unknown", never as "unchanged".
//
// HEAD is deliberately not used: an empty or skipped commit advances the rebase
// without moving HEAD, so HEAD would report a stall that isn't one.
func rebaseStepID(repoPath string) string {
	for _, rel := range []string{"rebase-merge/msgnum", "rebase-apply/next"} {
		data, err := os.ReadFile(filepath.Join(repoPath, ".git", filepath.FromSlash(rel)))
		if err == nil {
			return rel + ":" + strings.TrimSpace(string(data))
		}
	}
	return ""
}

func (r *Resolver) allUnderSafePrefixes(paths []string) bool {
	if len(r.opts.SafePrefixes) == 0 {
		return false
	}
	for _, p := range paths {
		if !matchesSafePrefix(p, r.opts.SafePrefixes, r.opts.SafeDenyPrefixes) {
			return false
		}
	}
	return true
}

// matchesSafePrefix mirrors gitutil.matchesSafePrefix with most-specific-
// prefix-wins semantics. Reimplemented here to avoid exporting the helper.
func matchesSafePrefix(path string, prefixes, denyPrefixes []string) bool {
	bestLen := 0
	safe := false
	for _, p := range prefixes {
		if strings.HasPrefix(path, p) && len(p) > bestLen {
			bestLen = len(p)
			safe = true
		}
	}
	for _, d := range denyPrefixes {
		if strings.HasPrefix(path, d) && len(d) >= bestLen {
			bestLen = len(d)
			safe = false
		}
	}
	return safe
}

// tryUnionTier walks the conflicted paths and stages those whose working-
// tree content no longer contains conflict markers. This handles paths
// where .gitattributes declared merge=union: git's union driver produces a
// clean concatenation in the working tree, but the index still shows
// stages 1/2/3 until we `git add`.
//
// Returns the subset of paths that still have conflict markers and need a
// later tier.
func (r *Resolver) tryUnionTier(ctx context.Context, repoPath string, paths []string) ([]string, error) {
	var staged []string
	var remaining []string

	for _, p := range paths {
		full := filepath.Join(repoPath, p)
		data, err := os.ReadFile(full)
		if err != nil {
			// missing file is a conflict class we don't handle here
			// (rename/delete) — let a later tier deal with it.
			remaining = append(remaining, p)
			continue
		}
		if gitutil.HasConflictMarkersBytes(data) {
			remaining = append(remaining, p)
			continue
		}
		staged = append(staged, p)
	}

	if len(staged) > 0 {
		args := append([]string{"add", "--"}, staged...)
		if _, err := gitutil.RunGit(ctx, repoPath, args...); err != nil {
			return nil, fmt.Errorf("git add union-resolved: %w", err)
		}
		r.logger.Info("automerge.tier", "tier", "union", "staged", len(staged))
	}

	return remaining, nil
}

// time used in Options.LLMTimeout default — keeps import live.
var _ = time.Second

// listConflictedPaths returns unique paths with unresolved conflicts.
func listConflictedPaths(ctx context.Context, repoPath string) ([]string, error) {
	out, err := gitutil.RunGit(ctx, repoPath, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}
	seen := make(map[string]bool)
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		paths = append(paths, line)
	}
	return paths, nil
}
