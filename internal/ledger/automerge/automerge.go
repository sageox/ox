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
// only calls `git rebase --continue` once all conflicts have been staged.
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

// conflictMarkerStart is git's textual conflict marker. We look for it as
// a line prefix to determine whether union (or any other in-place driver)
// has fully resolved the working-tree content.
const conflictMarkerStart = "<<<<<<<"

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

// Resolve attempts to resolve conflicts in a repo that's mid-rebase.
//
// Returns (true, nil) if all conflicts were resolved AND `git rebase
// --continue` succeeded.
// Returns (false, ErrNoConflicts) if there's nothing to do.
// Returns (false, err) if any tier failed and the caller should abort the
// rebase. The rebase is NOT aborted by this function.
func (r *Resolver) Resolve(ctx context.Context, repoPath string) (bool, error) {
	conflicts, err := listConflictedPaths(ctx, repoPath)
	if err != nil {
		return false, fmt.Errorf("list conflicts: %w", err)
	}
	if len(conflicts) == 0 {
		return false, ErrNoConflicts
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
		// ResolveRebaseAcceptTheirs already runs `git rebase --continue`.
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

// continueRebase runs `git rebase --continue` with GIT_EDITOR=true so git
// doesn't try to open an editor for the commit message.
func (r *Resolver) continueRebase(ctx context.Context, repoPath string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "rebase", "--continue")
	cmd.Dir = repoPath
	cmd.Env = append(cmd.Environ(), "GIT_EDITOR=true")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("rebase --continue: %s: %w", strings.TrimSpace(string(out)), err)
	}
	r.logger.Info("automerge.done", "repo", repoPath)
	return true, nil
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
		if hasConflictMarkers(data) {
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

// hasConflictMarkers returns true if the file content has a git conflict
// marker at the start of any line.
func hasConflictMarkers(data []byte) bool {
	if !strings.Contains(string(data), conflictMarkerStart) {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, conflictMarkerStart) {
			return true
		}
	}
	return false
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
