package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/kb"
	"github.com/sageox/ox/internal/manifest"
	"github.com/sageox/ox/internal/perf"
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

// Skip reasons reported in ManagedRepoPullResult.SkipReason. Defined as
// constants so consumers can branch on them without fragile string matching.
const (
	// skipReasonRebaseInProgress: the working tree is mid-rebase — UNSAFE to
	// treat as usable (partial/inconsistent state).
	skipReasonRebaseInProgress = "rebase in progress"
	// skipReasonLockFilesPresent: a live git process holds lock files (typically
	// another daemon syncing the same shared context). On-disk files stay
	// consistent, so the context remains usable.
	skipReasonLockFilesPresent = "lock files present"
	// skipReasonRemoteUnchanged / skipReasonRecentlyFetched: already current.
	skipReasonRemoteUnchanged = "remote unchanged"
	skipReasonRecentlyFetched = "recently fetched"
)

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

	// SessionConflictAge is the age of the oldest local commit not yet
	// pushed to the upstream tracking branch, computed via git-plumbing
	// when Issue.Type == IssueTypeSessionConflictWedge (zero otherwise).
	// Grounded in an immutable git commit timestamp, so — unlike
	// IssueTracker.Since — it survives a daemon restart mid-incident.
	// sync.go's escalateSessionConflictSeverity uses it as a floor under
	// the tracker-derived elapsed time so severity never regresses after a
	// restart while the same conflict remains unresolved.
	SessionConflictAge time.Duration
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
	ctx, span := perf.Start(ctx, "daemon:pull_managed_repo")
	defer span.End()

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

	// Rebase-in-progress handling: leave a fresh rebase alone, auto-recover
	// a stale wedge, or surface an unrecoverable one. See recoverPreexistingRebase.
	if stop, res := s.recoverPreexistingRebase(ctx, path, repoName, logger); stop {
		return res
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
				SkipReason: skipReasonLockFilesPresent,
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
		return ManagedRepoPullResult{Skipped: true, SkipReason: skipReasonRemoteUnchanged}
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
			return ManagedRepoPullResult{Skipped: true, SkipReason: skipReasonRecentlyFetched}
		}
	}

	// --- Fetch ---

	// Refresh remote URL if credentials changed
	projectEndpoint := endpoint.GetForProject(opts.ProjectRoot)
	if err := gitserver.RefreshRemoteCredentials(path, projectEndpoint); err != nil {
		logger.Warn("remote credential refresh failed", "path", path, "error", err)
	}

	// git fetch
	fetchCtx, fetchSpan := perf.Start(ctx, "git_fetch")
	fetchArgs := append([]string{"-C", path}, gitHTTPTimeoutFlags()...)
	fetchArgs = append(fetchArgs, "fetch", "--quiet")
	// NewNetworkCmd disables the credential prompt so a lapsed PAT fails fast
	// instead of hanging the daemon's background sync cycle on a TTY-less prompt.
	fetchCmd := gitutil.NewNetworkCmd(fetchCtx, fetchArgs...)
	if output, err := fetchCmd.CombinedOutput(); err != nil {
		perf.RecordError(fetchSpan, err)
		fetchSpan.End()
		detail := gitutil.SanitizeOutput(strings.TrimSpace(string(output)))
		errMsg := "fetch failed"
		if detail != "" {
			errMsg = fmt.Sprintf("fetch failed: %s", detail)
		}
		return ManagedRepoPullResult{Err: fmt.Errorf("%s: %w", errMsg, err)}
	}
	fetchSpan.End()

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

	_, pullSpan := perf.Start(ctx, "git_pull_rebase")
	pullArgs := append([]string{"-C", path}, gitHTTPTimeoutFlags()...)
	pullArgs = append(pullArgs, "pull", "--rebase", "--autostash", "--quiet")
	pullCmd := gitutil.NewNetworkCmd(ctx, pullArgs...)
	output, err := pullCmd.CombinedOutput()
	if err != nil {
		perf.RecordError(pullSpan, err)
	}
	pullSpan.End()
	if err != nil {
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

			// capture conflicted paths before AuditAndAbort discards rebase
			// state — a sessions/*/meta.json conflict gets its own issue
			// type (IssueTypeSessionConflictWedge) instead of the generic
			// IssueTypeDiverged below: the abort clears the rebase mechanics
			// but never resolves the underlying content, so the next cycle
			// hits the identical conflict. Distinguishing it lets sync.go
			// escalate severity by elapsed time instead of treating it like
			// ordinary lag.
			//
			// NB: an earlier version of this comment claimed sessions/ is
			// "hard-denied from auto-resolve by design". That is true of
			// manifest-parsed repos (internal/manifest/auto_resolve.go) but
			// NOT of the ledger, which appends its own {Auto, "sessions/"}
			// rule and never runs it through ValidateResolveRules — see
			// internal/ledger/ledger.go. So the ledger DOES auto-resolve
			// sessions/, to the local side. Anything that reaches here is a
			// conflict auto-resolve could not handle.
			conflictedSessionPaths := sessionConflictPaths(ctx, path)

			// AuditAndAbort: structured pre/post logs capture HEAD SHA,
			// unmerged file list, and stash count BEFORE discarding state,
			// so silent recovery from a wedged rebase leaves an audit
			// trail. See ox-ooy3 and .claude/rules/daemon-git.md.
			abortErr := gitutil.AuditAndAbort(ctx, path, gitutil.AuditOpRebase, "auto-resolve exhausted", logger)
			switch {
			case abortErr != nil:
				logger.Error("rebase abort failed, repo stuck in rebase state", "op", "rebase_abort_failed", "repo", repoName, "error", abortErr)
				result.Issue = &DaemonIssue{
					Type:            IssueTypeRebaseStuck,
					Severity:        SeverityError,
					Repo:            repoName,
					Summary:         fmt.Sprintf("%s is stuck in a broken rebase state. Run 'git -C %s rebase --abort' manually or 'ox doctor --fix' to recover.", repoName, path),
					RequiresConfirm: true,
				}
			case len(conflictedSessionPaths) > 0:
				// Severity/RequiresConfirm are left unset here — sync.go's
				// doPull escalates them based on how long this specific
				// (type, repo) issue has existed.
				result.Issue = &DaemonIssue{
					Type:    IssueTypeSessionConflictWedge,
					Repo:    repoName,
					Summary: fmt.Sprintf("%d session(s) have unresolvable meta.json conflicts; local session data is not syncing to the team ledger", len(conflictedSessionPaths)),
				}
				// Restart-durable signal: the age of the oldest unpushed
				// commit is grounded in an immutable git timestamp, unlike
				// IssueTracker.Since which resets on every daemon restart.
				// See sync.go's escalateSessionConflictSeverity.
				if age, ok := oldestUnpushedCommitAge(ctx, path); ok {
					result.SessionConflictAge = age
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

// sessionConflictPaths returns the subset of unmerged (conflicted) paths
// under sessions/ in the given repo. Best-effort: returns nil on any git
// error rather than failing the caller, since this only refines issue
// classification and never gates control flow.
func sessionConflictPaths(ctx context.Context, repoPath string) []string {
	out, err := exec.CommandContext(ctx, "git", "-C", repoPath, "diff", "--name-only", "--diff-filter=U").Output()
	if err != nil {
		return nil
	}
	var paths []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if isSessionMetaPath(line) {
			paths = append(paths, line)
		}
	}
	return paths
}

// isSessionMetaPath reports whether path has the exact shape
// sessions/<session-id>/meta.json — one session-directory component, not
// any file anywhere under sessions/ (session.md, summary.json, and other
// per-session artifacts are conflicts too, but not the specific
// content-shape IssueTypeSessionConflictWedge exists to describe).
func isSessionMetaPath(path string) bool {
	parts := strings.Split(path, "/")
	return len(parts) == 3 && parts[0] == "sessions" && parts[1] != "" && parts[2] == "meta.json"
}

// oldestUnpushedCommitAge returns the age of the oldest local commit not yet
// on the upstream tracking branch (falling back to origin/main). Local
// git-plumbing only — no network fetch, since pullManagedRepo already
// fetched earlier in this same cycle. Returns ok=false when there are no
// unpushed commits or the age can't be determined (e.g. no upstream and no
// origin/main).
//
// This mirrors the timestamp half of ledgerSyncWedged's wedge detection
// (sync_gc.go): git commit timestamps are immutable and survive a daemon
// restart, unlike IssueTracker's in-memory Since field. Called at
// session-conflict classification time so sync.go's
// escalateSessionConflictSeverity has a restart-durable floor under the
// tracker-derived elapsed time — see ManagedRepoPullResult.SessionConflictAge.
func oldestUnpushedCommitAge(ctx context.Context, path string) (time.Duration, bool) {
	ahead, err := revListCount(ctx, path, "@{upstream}..HEAD", "origin/main..HEAD")
	if err != nil || ahead <= 0 {
		return 0, false
	}

	oldestOutput, err := gitutil.RunGit(ctx, path, "log", "@{upstream}..HEAD", "--format=%ct")
	if err != nil {
		oldestOutput, err = gitutil.RunGit(ctx, path, "log", "origin/main..HEAD", "--format=%ct")
	}
	if err != nil {
		return 0, false
	}
	timestamps := strings.Fields(strings.TrimSpace(oldestOutput))
	if len(timestamps) == 0 {
		return 0, false
	}
	// git log lists newest-first; the last line is the oldest unpushed commit.
	oldestUnix, err := strconv.ParseInt(timestamps[len(timestamps)-1], 10, 64)
	if err != nil {
		return 0, false
	}

	return time.Since(time.Unix(oldestUnix, 0)), true
}

// staleRebaseThreshold separates a fresh, in-flight rebase from an abandoned
// wedge. The daemon's own `git pull --rebase` completes in seconds, so 5
// minutes cleanly distinguishes "someone is actively rebasing" from "a rebase
// has been stuck here for a while" and guarantees we never abort a rebase out
// from under an in-progress operation.
const staleRebaseThreshold = 5 * time.Minute

// recoverPreexistingRebase handles a rebase that is ALREADY in progress when a
// sync cycle starts (as opposed to one this call's own pull --rebase just
// triggered, which the post-pull recovery ladder handles).
//
//   - No rebase, or a FRESH one (younger than staleRebaseThreshold): a fresh
//     rebase is almost always the daemon's own concurrent pull or a human/CLI
//     mid-operation — leave it alone. Returns stop=false for "no rebase" so the
//     caller proceeds to pull; stop=true with a Skipped result for a fresh one.
//   - A STALE rebase: a genuine wedge (abandoned by a crash, or stopped at an
//     "edit"/conflict and never continued). Bare-skipping it forever was a
//     deadlock — the post-pull ladder only fires for a rebase THIS call
//     started, so a pre-existing wedge never got recovered, stranding every
//     new session behind it and churning the sync loop. Auto-recover via
//     AuditAndAbort (logs everything discarded before resetting; session data
//     is the store's to re-publish, per .claude/rules/daemon-git.md), then
//     return stop=false so the caller falls through to a clean pull and
//     re-syncs this same cycle.
//   - If the abort itself fails the repo is genuinely stuck: return stop=true
//     with a confirm-required IssueTypeRebaseStuck so doctor / the scheduled
//     doctor agent task picks it up instead of silently re-looping.
func (s *SyncScheduler) recoverPreexistingRebase(ctx context.Context, path, repoName string, logger *slog.Logger) (stop bool, result ManagedRepoPullResult) {
	age, inProgress := gitutil.RebaseAge(path)
	if !inProgress {
		return false, ManagedRepoPullResult{}
	}
	if age < staleRebaseThreshold {
		logger.Debug("repo in fresh rebase, skipping pull this cycle", "path", path, "age", age)
		return true, ManagedRepoPullResult{Skipped: true, SkipReason: skipReasonRebaseInProgress}
	}
	logger.Warn("recovering stale wedged rebase",
		"op", "stale_rebase_recover", "repo", repoName, "age", age.Round(time.Second))
	// AbortOrClearRebase first tries the reversible `git rebase --abort`, then
	// escalates to `git rebase --quit` for a structurally-incomplete "zombie"
	// state dir (a process killed mid-rebase leaving only an autostash entry, no
	// head-name/orig-head) that --abort alone cannot clear. See
	// gitutil.AbortOrClearRebase and bd ox-j3cl.
	if abortErr := gitutil.AbortOrClearRebase(ctx, path, "stale wedged rebase auto-recovery", logger); abortErr != nil {
		logger.Error("stale rebase recovery failed",
			"op", "stale_rebase_recover_failed", "repo", repoName, "error", abortErr)
		return true, ManagedRepoPullResult{
			Skipped:    true,
			SkipReason: "stale rebase recovery failed",
			Issue: &DaemonIssue{
				Type:            IssueTypeRebaseStuck,
				Severity:        SeverityError,
				Repo:            repoName,
				Summary:         fmt.Sprintf("%s is stuck in a wedged rebase and auto-recovery failed. Run 'ox doctor --fix' to recover.", repoName),
				RequiresConfirm: true,
			},
		}
	}
	logger.Info("recovered stale wedged rebase", "op", "stale_rebase_recovered", "repo", repoName)
	return false, ManagedRepoPullResult{}
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
