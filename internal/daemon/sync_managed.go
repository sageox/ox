package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/kb"
	"github.com/sageox/ox/internal/manifest"
)

// ManagedRepoPullOpts configures how pullManagedRepo behaves for a given repo.
// Both ledger and team context repos use this — behavioral differences are
// expressed through these options, not through separate code paths.
type ManagedRepoPullOpts struct {
	// RepoPath is the local filesystem path to the git repo.
	RepoPath string

	// RepoName identifies the repo in issues and logs (e.g., "ledger", "team-context-foo").
	RepoName string

	// ProjectRoot is the user's project root, used to resolve the endpoint
	// for credential refresh.
	ProjectRoot string

	// SyncInterval is the base sync interval. Used to compute FETCH_HEAD
	// dedup threshold (SyncInterval / 2).
	SyncInterval time.Duration

	// MinFetchAge overrides the minimum FETCH_HEAD age for dedup.
	// Zero means use gitutil.MinFetchHeadAge.
	MinFetchAge time.Duration

	// ValidateIntegrity runs isValidGitRepo before pulling. If false (or repo
	// is corrupt), returns a CorruptRepo result so the caller can handle reclone.
	ValidateIntegrity bool

	// DetectDivergence runs rev-list divergence check after fetch, before pull.
	// Purely informational — pull --rebase handles it either way.
	DetectDivergence bool

	// ResolveRules maps path prefixes to conflict resolution modes.
	// Empty means no auto-resolve — conflicts become errors.
	// Most specific prefix wins: `resolve none data/proprietary/` overrides
	// `resolve auto data/` for files under data/proprietary/.
	ResolveRules []manifest.ResolveRule

	// LLMResolver, when non-nil, is called as the third tier of the
	// pull-cycle conflict resolver: union (pre-flight via merge=union)
	// + accept-theirs (post-fail) didn't fully resolve, so escalate to
	// a semantic LLM merge before aborting the rebase. The resolver is
	// expected to be the same automerge.New(...).Resolve shape the
	// CLI's ledgerLLMResolveHook uses, just exposed as a daemon
	// dependency injection.
	//
	// Returning (true, nil) means resolved + rebase already continued
	// inside the resolver (it owns rebase --continue on success).
	// (false, nil) means "nothing the resolver could do" — fall
	// through to abort. Any non-nil error is logged and we abort.
	//
	// Daemon-side LLM tier (ox-21cb): when configured server-side, the
	// daemon spawns the LLM merge directly. When unconfigured, callers
	// can leave this nil and rely on CLI-side escalation
	// (cmd/ox/session_upload.go::ledgerLLMResolveHook) the next time
	// the user invokes a session-stop or push — best-effort, since ox
	// is not always invoked by an LLM.
	LLMResolver func(ctx context.Context, repoPath string, paths []string) (bool, error)

	// EnsureKBMergeAttrs controls the pull pre-flight that installs the
	// shared KB merge=union rules into .git/info/attributes via
	// internal/kb.EnsureMergeAttributes. Both ledger and team-context
	// clones are KB-style repos (multi-writer, multi-coworker, server
	// + CLI seeded) and benefit from the same resilience — concurrent
	// writes to AGENTS.md / CLAUDE.md / README.md / SOUL.md / etc.
	// auto-merge by concatenation instead of wedging the rebase.
	//
	// The list of unioned paths is the canonical kb.MergeUnionPaths and
	// is intentionally narrow: only append-mostly root metadata. Source
	// files, configs, and anything where last-write-wins semantics
	// matter are NOT in scope. See internal/kb/mergeattrs.go for the
	// full list and rationale.
	EnsureKBMergeAttrs bool

	// Logger for structured logging. Required.
	Logger *slog.Logger
}

// ManagedRepoPullResult describes what happened during pullManagedRepo.
type ManagedRepoPullResult struct {
	// Skipped is true if the pull was skipped (repo up-to-date, in rebase, locked, etc).
	Skipped bool

	// SkipReason explains why the pull was skipped.
	SkipReason string

	// CorruptRepo is true if ValidateIntegrity detected a corrupt repo.
	// The caller should handle reclone.
	CorruptRepo bool

	// Diverged is true if branches were diverged before the pull.
	Diverged bool

	// AutoResolved is true if rebase conflicts were auto-resolved.
	AutoResolved bool

	// FetchHeadTime is the FETCH_HEAD mtime after fetch (zero if not fetched).
	FetchHeadTime time.Time

	// Error from fetch or pull. Nil on success or skip.
	Err error

	// Issue to report (nil if none). The caller decides how to persist it.
	Issue *DaemonIssue
}

// pullManagedRepo executes the shared pull pipeline for any daemon-managed git repo.
//
// Pipeline stages:
//  1. Rebase-in-progress check
//  2. Lock file check
//  3. ls-remote dedup (skip if HEAD unchanged)
//  4. FETCH_HEAD mtime dedup (secondary)
//  5. Credential refresh
//  6. git fetch
//  7. Divergence detection (optional)
//  8. git pull --rebase --autostash
//  9. Conflict handling (auto-resolve or report)
//
// Callers wrap this with repo-specific concerns: auto-clone, backoff, mutex,
// metrics, post-pull signals.
func (s *SyncScheduler) pullManagedRepo(ctx context.Context, opts ManagedRepoPullOpts) ManagedRepoPullResult {
	logger := opts.Logger
	if logger == nil {
		logger = s.logger
	}
	path := opts.RepoPath
	repoName := opts.RepoName

	// --- Pre-flight checks ---

	// Integrity validation (catches partial/corrupt clones)
	if opts.ValidateIntegrity && !isValidGitRepo(path) {
		return ManagedRepoPullResult{CorruptRepo: true}
	}

	// Rebase-in-progress: skip — will resolve on its own or needs manual fix
	if gitutil.IsRebaseInProgress(path) {
		logger.Debug("repo in rebase state, skipping pull", "path", path)
		return ManagedRepoPullResult{Skipped: true, SkipReason: "rebase in progress"}
	}

	// Lock files: auto-remove stale ones, skip and report if still present
	gitDir := filepath.Join(path, ".git")
	if locks := gitutil.HasLockFiles(gitDir); len(locks) > 0 {
		// attempt to remove stale locks before giving up
		removed, lockErrs := gitutil.RemoveStaleLockFiles(gitDir)
		for _, err := range lockErrs {
			logger.Warn("failed to remove stale git lock file", "path", path, "error", err)
		}
		if len(removed) > 0 {
			logger.Info("removed stale git lock files", "path", path, "locks", strings.Join(removed, ", "))
		}
		// re-check: if fresh locks remain, a live git process is holding them
		if remaining := gitutil.HasLockFiles(gitDir); len(remaining) > 0 {
			logger.Warn("git lock files detected, skipping pull",
				"path", path, "locks", strings.Join(remaining, ", "))
			return ManagedRepoPullResult{
				Skipped:    true,
				SkipReason: "lock files present",
				Issue: &DaemonIssue{
					Type:     IssueTypeGitLock,
					Severity: SeverityWarning,
					Repo:     repoName,
					Summary: fmt.Sprintf("Lock files blocking sync: %s. If no git commands are running, remove with: rm %s/{%s}",
						strings.Join(remaining, ", "),
						gitDir,
						strings.Join(remaining, ",")),
				},
			}
		}
	}

	// --- Dedup checks ---

	// ls-remote SHA check: cheapest way to skip when nothing changed
	if s.remoteRefCheck(ctx, path) {
		return ManagedRepoPullResult{Skipped: true, SkipReason: "remote unchanged"}
	}

	// FETCH_HEAD mtime dedup (secondary: cross-daemon coordination)
	if age, ok := gitutil.FetchHeadAge(path); ok {
		minAge := opts.MinFetchAge
		if minAge == 0 {
			minAge = gitutil.MinFetchHeadAge
		}
		threshold := max(opts.SyncInterval/2, minAge)
		if age < threshold {
			logger.Debug("repo recently fetched, skipping", "path", path, "age", age)
			return ManagedRepoPullResult{Skipped: true, SkipReason: "recently fetched"}
		}
	}

	// --- Fetch ---

	// Refresh remote URL if credentials changed
	projectEndpoint := endpoint.GetForProject(opts.ProjectRoot)
	if err := gitserver.RefreshRemoteCredentials(path, projectEndpoint); err != nil {
		logger.Warn("remote credential refresh failed", "path", path, "error", err)
	}

	// git fetch
	fetchArgs := append([]string{"-C", path}, gitHTTPTimeoutFlags()...)
	fetchArgs = append(fetchArgs, "fetch", "--quiet")
	fetchCmd := exec.CommandContext(ctx, "git", fetchArgs...)
	if output, err := fetchCmd.CombinedOutput(); err != nil {
		detail := gitutil.SanitizeOutput(strings.TrimSpace(string(output)))
		errMsg := "fetch failed"
		if detail != "" {
			errMsg = fmt.Sprintf("fetch failed: %s", detail)
		}
		return ManagedRepoPullResult{Err: fmt.Errorf("%s: %w", errMsg, err)}
	}

	// Track FETCH_HEAD mtime
	var fetchHeadTime time.Time
	if info, err := os.Stat(filepath.Join(path, ".git", "FETCH_HEAD")); err == nil {
		fetchHeadTime = info.ModTime().UTC()
		s.recordRemoteChange(path, fetchHeadTime)
	}

	// --- Divergence detection ---
	result := ManagedRepoPullResult{FetchHeadTime: fetchHeadTime}

	if opts.DetectDivergence {
		if detectDivergedBranchesAt(ctx, path) {
			logger.Info("branches diverged, rebasing to reconcile", "repo", repoName)
			result.Diverged = true
		}
	}

	// --- Pull pre-flight: ensure shared KB merge=union rules ---
	//
	// Applies to both ledger AND team-context repos: they're both
	// multi-writer KB-style clones (server seed + CLI seed + multiple
	// coworker writes) and have the same wedge failure mode on
	// concurrent writes to root metadata files. Without this rule,
	// add/add or content/content collisions on AGENTS.md / CLAUDE.md /
	// README.md / SOUL.md etc. halt the rebase and surface as "diverged
	// from remote" errors that require manual intervention.
	//
	// kb.EnsureMergeAttributes writes per-clone .git/info/attributes
	// (no working-tree artifact); idempotent, atomic, safe to call on
	// every pull cycle.
	//
	// Best-effort: failure here is degraded mode (rebases may wedge),
	// not a pull failure. Logged at warn level so an operator can spot
	// a permission/IO issue.
	if opts.EnsureKBMergeAttrs {
		if changed, ensureErr := kb.EnsureMergeAttributes(path); ensureErr != nil {
			logger.Warn("ensure merge attributes failed", "repo", repoName, "error", ensureErr)
		} else if changed {
			logger.Info("healed kb merge attributes (auto-repair pre-flight)", "repo", repoName)
		}
	}

	// --- Pull ---

	pullArgs := append([]string{"-C", path}, gitHTTPTimeoutFlags()...)
	pullArgs = append(pullArgs, "pull", "--rebase", "--autostash", "--quiet")
	pullCmd := exec.CommandContext(ctx, "git", pullArgs...)
	if output, err := pullCmd.CombinedOutput(); err != nil {
		detail := gitutil.SanitizeOutput(strings.TrimSpace(string(output)))
		logger.Warn("pull failed", "error", err, "output", detail, "repo", repoName)

		// Try auto-resolving conflicts in safe paths before giving up
		autoPaths := manifest.AutoResolvePaths(opts.ResolveRules)
		denyPaths := manifest.AutoResolveDenyPaths(opts.ResolveRules)
		if len(autoPaths) > 0 && gitutil.IsRebaseInProgress(path) {
			resolveErr := gitutil.ResolveRebaseAcceptTheirs(ctx, path, autoPaths, denyPaths)
			if resolveErr == nil {
				logger.Info("auto-resolved rebase conflicts", "repo", repoName, "strategy", "accept-theirs")
				result.AutoResolved = true
				return result
			}
			logger.Warn("auto-resolve failed, attempting LLM tier", "repo", repoName, "error", resolveErr)

			// Tier 3: LLM merge for paths that union + accept-theirs
			// couldn't handle. The resolver itself decides whether to
			// run the LLM tier — if no binary is configured it returns
			// ErrLLMUnavailable, which we treat as "no escalation
			// available, surface to user". See ox-21cb.
			if opts.LLMResolver != nil {
				llmResolved, llmErr := opts.LLMResolver(ctx, path, autoPaths)
				switch {
				case llmErr == nil && llmResolved:
					logger.Info("auto-resolved rebase conflicts", "repo", repoName, "strategy", "llm")
					result.AutoResolved = true
					return result
				case llmErr == nil:
					// resolver returned (false, nil) — best-effort no-op,
					// e.g. nothing it could do; fall through to abort.
					logger.Info("LLM tier produced no resolution", "repo", repoName)
				default:
					logger.Warn("LLM tier failed", "repo", repoName, "error", llmErr)
				}
			}

			// AuditAndAbort: structured pre/post logs capture HEAD SHA,
			// unmerged file list, and stash count BEFORE discarding state,
			// so silent recovery from a wedged rebase leaves an audit
			// trail. See ox-ooy3 and .claude/rules/daemon-git.md.
			abortErr := gitutil.AuditAndAbort(ctx, path, gitutil.AuditOpRebase, "auto-resolve exhausted", logger)
			if abortErr != nil {
				logger.Error("rebase abort failed, repo stuck in rebase state", "op", "rebase_abort_failed", "repo", repoName, "error", abortErr)
				result.Issue = &DaemonIssue{
					Type:            IssueTypeRebaseStuck,
					Severity:        SeverityError,
					Repo:            repoName,
					Summary:         fmt.Sprintf("%s is stuck in a broken rebase state. Run 'git -C %s rebase --abort' manually or 'ox doctor --fix' to recover.", repoName, path),
					RequiresConfirm: true,
				}
			}
		}

		// only classify further if no higher-priority issue (rebase stuck) was already set
		if result.Issue == nil {
			// Check if it's a merge conflict
			statusCmd := exec.CommandContext(ctx, "git", "-C", path, "status", "--porcelain")
			if statusOutput, _ := statusCmd.Output(); strings.Contains(string(statusOutput), "UU") {
				result.Issue = &DaemonIssue{
					Type:            IssueTypeMergeConflict,
					Severity:        SeverityError,
					Repo:            repoName,
					Summary:         fmt.Sprintf("%s has merge conflicts. Run 'ox doctor --fix' to resolve.", repoName),
					RequiresConfirm: true,
				}
			} else if result.Diverged {
				// Diverged and rebase failed but not a merge conflict
				result.Issue = &DaemonIssue{
					Type:     IssueTypeDiverged,
					Repo:     repoName,
					Severity: SeverityError,
					Summary:  fmt.Sprintf("%s has diverged from remote and rebase failed. Run 'ox doctor --fix' to resolve.", repoName),
				}
			}
		}

		errMsg := "pull failed"
		if detail != "" {
			errMsg = fmt.Sprintf("pull failed: %s", detail)
		}
		result.Err = fmt.Errorf("%s: %w", errMsg, err)
		return result
	}

	return result
}

// detectDivergedBranchesAt checks if local and remote branches have both
// progressed independently at the given repo path. Returns false when the
// repo is shallow — rev-list counts truncate at the shallow boundary and
// would falsely report "diverged" for branches that share ancestry past
// the shallow horizon. False is safe: it just means we don't auto-trigger
// a rebase based on divergence detection; the next sync will retry.
func detectDivergedBranchesAt(ctx context.Context, repoPath string) bool {
	if state, _ := gitutil.InspectRepo(repoPath); state.Shallow {
		slog.Debug("divergence check skipped: shallow clone",
			"repo", repoPath, "reason", state.Reason)
		return false
	}
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath,
		"rev-list", "--left-right", "--count", "origin/main...HEAD")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	parts := strings.Fields(string(output))
	if len(parts) != 2 {
		return false
	}
	return parts[0] != "0" && parts[1] != "0"
}
