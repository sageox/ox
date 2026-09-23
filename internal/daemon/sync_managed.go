package daemon

import (
	"context"
	"errors"
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
	// skipReasonRepoLockBusy: another ox process (daemon goroutine or CLI)
	// holds gitutil's per-clone lock past our wait budget. The clone is
	// healthy — just in use — so this resolves itself next cycle. ADR-030.
	skipReasonRepoLockBusy = "repo lock busy"
	// skipReasonUnconfirmedConflict: autostash recovery reported an error
	// before the pull ran, but a fresh re-read of the index found NO unmerged
	// entries — so the error described the probe, not the repo. The clone is
	// provably healthy; this cycle simply did no work. Deliberately a skip and
	// NOT a zero-value result: a zero value is indistinguishable from a
	// successful pull, so doPull would clear sync failures and stamp lastSync
	// on a cycle that never synced.
	skipReasonUnconfirmedConflict = "unconfirmed conflict"
	// skipReasonUnconfirmedIndex: the re-read was cut short (daemon shutdown, or
	// the confirmation's own budget) and therefore proved NOTHING about the
	// clone — it is not evidence of health and not evidence of damage.
	//
	// It is the reason withheldUnprovenSuccess stamps on every cycle whose index
	// state was never established, whether or not a pull ran: a pull that
	// succeeded may still have failed to re-apply its autostash, and with no
	// completed read nothing can tell that apart from a clean sync.
	//
	// It is a SEPARATE reason from skipReasonUnconfirmedConflict precisely so
	// skipProvesIndexReadable keeps saying false for it: retiring a standing
	// IssueTypeRepoIntegrity needs a read that actually completed.
	skipReasonUnconfirmedIndex = "unconfirmed index read"
)

// fetchHeadRaceSignature is the (version-stable, prefix/suffix-agnostic)
// substring of git's error when FETCH_HEAD carries more than one
// merge-eligible head — the "two fetches interleaved" corruption ADR-030
// closes with a per-clone lock. Matched against pull output so a transient
// hit can be retried once instead of treated as a real conflict. See the
// 2026-09-02 COE for the reproduction.
const fetchHeadRaceSignature = "Cannot rebase onto multiple branches"

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

	// AutoResolved is true if rebase or autostash conflicts were auto-resolved.
	AutoResolved bool

	// PullRan reports whether the fetch+pull sequence actually executed this
	// cycle. A cycle that pulled may have left local side effects that repeat
	// on every retry — most importantly an autostash entry, whose unbounded
	// accumulation is the whole reason doTeamSync's worktree-fingerprint
	// suspension exists (#767). A cycle that returned before the pull cannot
	// have caused any of that.
	//
	// It is a separate field and not something a caller may infer from Err:
	// classifyAutostashFailure JOINS a probe failure onto whatever the pull
	// already reported, so the same gitutil.ErrConflictProbeFailed sentinel
	// appears both when nothing ran and when a pull ran and failed
	// deterministically. Only this bool tells those two apart.
	PullRan bool

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

	// --- Fetch + pull, serialized per clone ---
	//
	// ADR-030 D1: every mutating git operation on a managed clone runs
	// inside gitutil.WithRepoLock so this pull cycle, the daemon's GC/wedge
	// probe, a concurrent doctor pass, and any CLI invocation (ox status,
	// ox doctor --fix) can never interleave a fetch or a rebase against the
	// SAME clone. That interleaving is what corrupted FETCH_HEAD and
	// produced "Cannot rebase onto multiple branches" in the 2026-09-02
	// incident (COE: docs/coes/2026-09-02-daemon-git-sync-race-and-lfs-divergence.md).
	var result ManagedRepoPullResult
	var conflictErr error
	// pullRan records whether fetchAndPullLocked actually executed, which the
	// two ResolveAutostashConflicts call sites below cannot be told apart from
	// conflictErr alone. It decides whether an unconfirmed conflictErr may
	// downgrade the cycle to a skip: before the pull there is nothing to keep,
	// after a successful pull the pull's own verdict is real and must stand.
	var pullRan bool
	lockErr := gitutil.WithRepoLock(ctx, path, func() error {
		autoPaths := manifest.AutoResolvePaths(opts.ResolveRules)
		denyPaths := manifest.AutoResolveDenyPaths(opts.ResolveRules)
		// Check before dedup: a previous successful pull can leave autostash
		// conflicts even when the remote and FETCH_HEAD have not changed.
		recovered, err := gitutil.ResolveAutostashConflicts(ctx, path, autoPaths, denyPaths)
		if err != nil {
			conflictErr = err
			return nil
		}
		if !recovered {
			if s.remoteRefCheck(ctx, path) {
				result = ManagedRepoPullResult{Skipped: true, SkipReason: skipReasonRemoteUnchanged}
				return nil
			}
			if age, ok := gitutil.FetchHeadAge(path); ok {
				minAge := opts.MinFetchAge
				if minAge == 0 {
					minAge = gitutil.MinFetchHeadAge
				}
				if age < max(opts.SyncInterval/2, minAge) {
					logger.Debug("repo recently fetched, skipping", "path", path, "age", age)
					result = ManagedRepoPullResult{Skipped: true, SkipReason: skipReasonRecentlyFetched}
					return nil
				}
			}
		}
		result = s.fetchAndPullLocked(ctx, opts, path, repoName, logger)
		pullRan = true
		result.AutoResolved = result.AutoResolved || recovered
		// Git can return zero after a successful rebase whose autostash failed
		// to apply. Check the index even after success, including auto-resolution.
		if !gitutil.IsRebaseInProgress(path) {
			recovered, conflictErr = gitutil.ResolveAutostashConflicts(ctx, path, autoPaths, denyPaths)
			result.AutoResolved = result.AutoResolved || recovered
		}
		return nil
	})
	if lockErr != nil {
		if gitutil.IsRepoLockBusy(lockErr) {
			logger.Debug("repo locked by a concurrent ox process, skipping this cycle", "path", path, "error", lockErr)
			return ManagedRepoPullResult{Skipped: true, SkipReason: skipReasonRepoLockBusy}
		}
		return ManagedRepoPullResult{Err: fmt.Errorf("acquire repo lock for %s: %w", repoName, lockErr)}
	}
	if conflictErr != nil {
		classifyAutostashFailure(ctx, &result, conflictErr, pullRan, path, repoName, logger)
	}
	// Stamped AFTER classification, which replaces result wholesale on one
	// branch — the field must describe this cycle, not survive by luck.
	result.PullRan = pullRan
	return result
}

// classifyAutostashFailure decides what an error from ResolveAutostashConflicts
// actually proved, and folds that verdict into result.
//
// It never inspects conflictErr's shape, deliberately. The tempting shortcut —
// "if it wraps context.DeadlineExceeded / context.Canceled, it's transient" —
// does not work, because exec.CommandContext only returns the context error
// when the context was ALREADY dead before Start; a deadline that strikes while
// git is running surfaces as an *exec.ExitError reading "signal: killed", which
// errors.Is(err, context.DeadlineExceeded) does NOT match (verified on go1.26).
// So the error text cannot be trusted to name the cause, and an error-shape
// allowlist is guaranteed to have holes. Re-reading the index is the only thing
// that yields a fact, so this always does that (reprobeConfirmAttempts times,
// because one failed read is itself not a fact) and branches on the indexProof
// that read produced — see applyIndexProof, which holds the whole decision and
// the single guarded exit that enforces this function's one invariant.
//
// The re-probe deliberately runs OUTSIDE gitutil.WithRepoLock, against
// ResolveAutostashConflicts' documented "caller must hold the lock" contract.
// Two reasons: `git ls-files --unmerged` is a read-only read of an index git
// replaces atomically (write temp + rename), so a concurrent writer yields the
// old or the new index but never a torn one; and taking the lock would make a
// busy lock (an `ox doctor` running in another process) fail the probe, which
// this function would then have to classify as "unreadable" — reintroducing
// exactly the false alarm it exists to prevent.
func classifyAutostashFailure(ctx context.Context, result *ManagedRepoPullResult, conflictErr error, pullRan bool, path, repoName string, logger *slog.Logger) {
	conflicted, probeErr := reprobeIndexConflicts(ctx, path)
	applyIndexProof(result, classifyIndexProof(conflicted, probeErr), conflictErr, probeErr, pullRan, path, repoName, logger)
}

// indexProof is what the confirmation re-read ESTABLISHED about the clone's
// index. It is deliberately the only thing applyIndexProof branches on: a
// (bool, error) pair invites "err != nil means broken" and "!conflicted means
// clean", and both of those readings are how this bug keeps coming back.
type indexProof int

const (
	// proofUnknown: the ladder stopped before reaching a verdict, because the
	// daemon is shutting down or the confirmation burned its own budget. It is
	// NOT "clean" and NOT "broken" — a read that never finished is evidence in
	// neither direction, which is exactly why it must never leave a cycle
	// looking like a completed sync.
	proofUnknown indexProof = iota
	// proofClean: a read RAN TO COMPLETION and found no unmerged entries, so
	// conflictErr said nothing true about the index (the #962 timeout). This is
	// the only proof under which a successful pull may stand as a success.
	proofClean
	// proofConflicted: a completed read found unmerged entries the resolver
	// refused to auto-merge. The one state a human can adjudicate.
	proofConflicted
	// proofUnreadable: every one of reprobeConfirmAttempts reads ran to
	// completion and failed. The clone is broken and stays broken.
	proofUnreadable
)

// provesIndexState reports whether this proof positively established what the
// index contains. The invariant enforced in withheldUnprovenSuccess is stricter
// still (only proofClean may leave a success standing), but a caller asking
// "did anything at all get read" wants this.
func (p indexProof) provesIndexState() bool { return p != proofUnknown }

// String feeds the index_verdict log field. "clean"/"unconfirmed" are kept
// verbatim from the pre-enum logging so existing log queries keep matching.
func (p indexProof) String() string {
	switch p {
	case proofClean:
		return "clean"
	case proofConflicted:
		return "conflicted"
	case proofUnreadable:
		return "unreadable"
	default:
		return "unconfirmed"
	}
}

// classifyIndexProof turns reprobeIndexConflicts' (bool, error) return into the
// single fact the rest of the classification is allowed to see.
//
// The abandoned check comes FIRST and outranks the error: errProbeAbandoned is
// returned with the last read's failure still wrapped in the chain, so testing
// `probeErr != nil` before it would file a shutdown as a durably broken clone —
// #962's "assert more than the probe proved" mistake, re-entered through the
// shutdown door.
func classifyIndexProof(conflicted bool, probeErr error) indexProof {
	switch {
	case errors.Is(probeErr, errProbeAbandoned):
		return proofUnknown
	case probeErr != nil:
		return proofUnreadable
	case conflicted:
		return proofConflicted
	default:
		return proofClean
	}
}

// applyIndexProof folds one indexProof into result and is the whole of
// classifyAutostashFailure's decision, split out so it can be exercised over
// the full (proof × pullRan × incoming result) cross-product without staging
// real git index states.
//
// It ends in ONE guarded exit — withheldUnprovenSuccess — and that guard, not
// the individual branches, is what upholds the invariant:
//
//	no cycle may leave here readable as a successful sync unless a read that
//	ran to completion proved the index clean.
//
// probeErr is passed alongside proof purely so the log lines can still show
// what the reads were failing with; nothing branches on it.
func applyIndexProof(result *ManagedRepoPullResult, proof indexProof, conflictErr, probeErr error, pullRan bool, path, repoName string, logger *slog.Logger) {
	switch proof {
	case proofUnreadable:
		// A DURABLE failure: git could not read this clone's index on any of
		// reprobeConfirmAttempts fresh, generous reads, each of which ran to
		// completion rather than being cut short. A corrupt .git/index, a
		// permission problem, or a missing git binary fails this way forever, so
		// staying quiet would hide a repo that never syncs again behind a status
		// that reports perfect health. isValidGitRepo only runs
		// `rev-parse --git-dir` and never reads the index, so nothing upstream
		// catches it.
		//
		// "Durable" here is still a judgement, not a proof — which is why the
		// resulting error must stay RETRYABLE downstream. doTeamSync routes it
		// to bounded backoff rather than the permanent fingerprint suspension
		// (see its pre-pull probe branch); doPull already does.
		//
		// Not IssueTypeMergeConflict/RequiresConfirm: we did NOT find conflicts,
		// and the #962 remedy for a confirm-gated conflict — hand-editing the
		// internal clone — is wrong here and unclearable. IssueTypeRepoIntegrity
		// with RequiresConfirm false lets doctor and the agent act on it, and a
		// later successful pull clears it.
		//
		// Skipped is forced off: doPull checks Skipped BEFORE Err, so leaving a
		// stale skip (e.g. fetchAndPullLocked's rebase-in-progress) set here
		// would swallow this error.
		result.Skipped = false
		result.SkipReason = ""
		result.Err = errors.Join(result.Err, fmt.Errorf("read index for %s: %w", repoName, probeErr))
		if result.Issue == nil {
			result.Issue = &DaemonIssue{
				Type:     IssueTypeRepoIntegrity,
				Severity: SeverityError,
				Repo:     repoName,
				// No repoName prefix: FormatLine already renders "<repo>: ".
				// Whitespace is collapsed because git reports index corruption
				// over two lines ("error: bad index file sha1 signature" +
				// "fatal: index file corrupt") and a Summary is rendered as one.
				Summary: fmt.Sprintf("git cannot read the index at %s, so this repo is not syncing: %s",
					filepath.Join(path, ".git", "index"),
					strings.Join(strings.Fields(probeErr.Error()), " ")),
			}
		}
		logger.Error("git index is unreadable, sync cannot proceed",
			"repo", repoName, "path", path, "error", probeErr, "autostash_error", conflictErr)

	case proofConflicted:
		// The probe agreed: there really are unmerged entries the resolver
		// refused to auto-merge. This is the one state a human can adjudicate.
		//
		// Skipped is forced off for the same reason proofUnreadable does it:
		// doPull and pullTeamContext both test Skipped BEFORE Err, so any skip
		// still set from fetchAndPullLocked — skipReasonRebaseInProgress is the
		// reachable one, if an outside git process clears the rebase state
		// between that check and this probe — would send the cycle down the
		// skip path and swallow the conflict error and its issue entirely.
		// Leaving the two branches asymmetric is what made this easy to miss.
		result.Skipped = false
		result.SkipReason = ""
		result.Err = errors.Join(result.Err, conflictErr)
		// Preserve an earlier pull classification, including session-wedge
		// escalation, when autostash recovery reports an additional failure.
		if result.Issue == nil {
			result.Issue = &DaemonIssue{
				Type:            IssueTypeMergeConflict,
				Severity:        SeverityError,
				Repo:            repoName,
				Summary:         fmt.Sprintf("%s has unresolved conflicts: %s", repoName, conflictErr),
				RequiresConfirm: true,
			}
		}

	case proofClean, proofUnknown:
		if !pullRan {
			// conflictErr came from the pre-pull probe, so this cycle did no
			// work at all: every path that assigns result before
			// fetchAndPullLocked returns with conflictErr still nil, so result
			// is zero-valued here and there is nothing to preserve. Returning
			// that zero value is the regression this branch exists to prevent:
			// doPull's success path would clear sync failures, clear issues,
			// record a pull success and stamp lastSync for a cycle that never
			// fetched a byte.
			//
			// The two skip reasons are NOT interchangeable: only the clean one
			// carries a completed read, and skipProvesIndexReadable lets that
			// one — and only that one — retire a standing integrity issue.
			skipReason := skipReasonUnconfirmedIndex
			if proof.provesIndexState() {
				skipReason = skipReasonUnconfirmedConflict
			}
			*result = ManagedRepoPullResult{Skipped: true, SkipReason: skipReason}
			logger.Warn("autostash recovery failed before the pull, retrying next cycle",
				"repo", repoName, "path", path, "error", conflictErr,
				"index_verdict", proof.String(), "probe_error", probeErr)
			break
		}
		// conflictErr came from the post-pull probe and no conflict was
		// confirmed, so the pull's own verdict (success, error, or skip) is the
		// most this function knows — it is deliberately NOT overwritten here.
		//
		// That is only half an answer, and the half that was wrong before: when
		// proof is proofUnknown the pull's "success" is not a verdict about the
		// index at all. `git pull --rebase --autostash` exits zero after a
		// rebase whose autostash re-apply left unmerged entries behind — the
		// exact reason this cycle re-reads the index — so with no completed read
		// a broken worktree and a clean one are the same bytes. The guard below
		// is what stops that from being reported as a healthy sync.
		logger.Warn("post-pull autostash check failed and no conflict was confirmed",
			"repo", repoName, "path", path, "error", conflictErr,
			"index_verdict", proof.String(), "probe_error", probeErr)
	}

	// The single guarded exit. Every branch above funnels through it, so the
	// invariant is enforced in one place rather than re-argued per case.
	if withheldUnprovenSuccess(result, proof) {
		logger.Warn("index state was never confirmed, so this cycle is not reported as a sync",
			"repo", repoName, "path", path, "error", conflictErr,
			"index_verdict", proof.String(), "probe_error", probeErr,
			"skip_reason", result.SkipReason)
	}
}

// resultReadsAsSuccess reports whether a consumer would take its success path
// for this result. All three (doPull, pullTeamContext, syncBubble) branch on
// Skipped first and Err second; a result that is neither IS a success to them,
// whatever else it carries. The first two then clear sync failures, retire
// IssueTypeRepoIntegrity, record a pull success and stamp lastSync — which is
// why a success claimed without evidence is the expensive kind of wrong. The
// bubble path only logs, so for it a downgrade costs a log level.
func resultReadsAsSuccess(result *ManagedRepoPullResult) bool {
	return !result.Skipped && result.Err == nil
}

// withheldUnprovenSuccess is applyIndexProof's single exit point and the only
// place this file's invariant is enforced:
//
//	a result may leave classifyAutostashFailure readable as a completed sync
//	ONLY when a read that ran to completion proved the index clean.
//
// It returns whether it had to intervene, so the caller can say so in the log.
//
// The test is written as a blanket "not proofClean" rather than "== proofUnknown"
// on purpose. proofConflicted and proofUnreadable already set Err in their own
// branches, so for them this is a backstop that changes nothing today — and a
// backstop is the point. Three review rounds of this change each shipped one
// variant of a single bug — a dropped probe failure returning a zero value, a
// joined post-pull probe error routed to backoff, an abandoned confirmation
// preserving a successful pull — and every one was a branch that forgot to fail
// the cycle. Enumerating the proofs that MAY succeed, in one guard, is what the
// next such branch runs into instead of shipping.
//
// The remedy is a SKIP and not an error, deliberately: nothing is known to be
// wrong, so the cycle must stay ordinarily retryable. An error here would feed
// the consecutive-failure backoff and — for a team context whose pull ran —
// teamFailureTakesBackoff's permanent worktree-fingerprint suspension, turning
// every laptop-lid-close into a repo that stops syncing. skipReasonUnconfirmedIndex
// is the reason precisely because skipProvesIndexReadable says false for it: a
// read that never finished must not retire a standing IssueTypeRepoIntegrity
// either.
//
// Fields other than Skipped/SkipReason are left alone: FetchHeadTime, Diverged
// and AutoResolved describe what the pull observed and stay true regardless of
// what the index turned out to hold.
func withheldUnprovenSuccess(result *ManagedRepoPullResult, proof indexProof) bool {
	if proof == proofClean || !resultReadsAsSuccess(result) {
		return false
	}
	result.Skipped = true
	result.SkipReason = skipReasonUnconfirmedIndex
	return true
}

// Bounds on the confirmation re-read. Together they decide when
// classifyAutostashFailure is allowed to call a clone durably broken.
const (
	// reprobeConfirmAttempts is how many INDEPENDENT reads must fail before the
	// index counts as unreadable. One failed read is not evidence of a durable
	// fault: a fork that hits EAGAIN under resource pressure, a transient
	// filesystem error, and a git killed mid-run all fail exactly the way a
	// corrupt .git/index does, and the error text cannot tell them apart (see
	// gitutil.ErrConflictProbeFailed). Concluding "durable" from one sample is
	// #962's mistake pointed the other way — asserting more than the probe
	// proved — and for a team context it used to end in permanent sync
	// suspension (doTeamSync now backs off instead, but this is the first of
	// the two guards). A corrupt index fails all three reads identically and in
	// milliseconds, so the durable case still goes loud on the first cycle.
	reprobeConfirmAttempts = 3

	// reprobeRetryDelay paces those reads. Short on purpose: it only has to
	// outlast an instantaneous fork/IO blip, and it is paid inside the sync
	// cycle. Anything longer-lived is NOT this loop's job — the caller's
	// bounded sync backoff retries the whole cycle minutes later. Skipped
	// entirely once shutdown is observed: waiting out a blip is no longer worth
	// anyone's time when the daemon is going away.
	reprobeRetryDelay = 250 * time.Millisecond

	// reprobeAttemptTimeout caps ONE read. Still generous — a local
	// `git ls-files --unmerged` normally returns in milliseconds, so only a
	// genuinely wedged git or a stalled filesystem can burn it. It is also the
	// granularity at which this function ignores cancellation: a read already in
	// flight is never abandoned, so this is the longest a daemon shutdown can
	// wait on one read.
	reprobeAttemptTimeout = 5 * time.Second

	// reprobeTotalBudget caps the confirmation AS A WHOLE while the daemon is
	// healthy. It is deliberately larger than the ladder's own arithmetic
	// (reprobeConfirmAttempts×reprobeAttemptTimeout + the delays ≈ 15.5s) so it
	// never truncates a healthy run — it is a ceiling, not a plan, and it exists
	// because a per-attempt timeout is not a hard bound on wall time:
	// exec.CommandContext kills the git process, but the command still waits for
	// its output pipes to close, which a surviving grandchild (a credential
	// helper, a filter process) can hold open past the deadline.
	reprobeTotalBudget = 20 * time.Second

	// reprobeShutdownDrain is all that is left of the budget once the sync
	// context is observed canceled between two reads. Daemon shutdown waits on
	// the scheduler goroutine, which waits on this call, so the full budget is
	// the wrong thing to spend there — but returning immediately would be wrong
	// too: a corrupt index fails every read in microseconds, and cutting the
	// ladder short would hide it behind "unconfirmed" on the very cycle that
	// should report it. A drain lets the fast, informative ladder finish and
	// stops only the slow, uninformative one.
	reprobeShutdownDrain = time.Second
)

// errProbeAbandoned marks a confirmation that stopped before reaching any
// verdict, because the daemon is shutting down or the confirmation burned its
// own budget. It is NOT evidence the index is broken, and callers MUST NOT
// treat it as the durable failure a completed ladder reports — that would
// reintroduce, from the shutdown side, exactly the "assert more than the probe
// proved" mistake #962 was about.
var errProbeAbandoned = errors.New("index confirmation abandoned before a verdict")

// reprobeIndexConflicts re-reads repoPath's index on a context decoupled from
// the sync cycle's, so a cancellation that killed the original probe cannot
// also kill the confirmation. It retries a bounded number of times and returns
// the LAST error only when every attempt failed. A non-nil error therefore
// means the index could not be READ on any of them — never that it is clean.
//
// Cancellation is discarded for ONE read at a time, never for the whole
// sequence. Per read that is the entire point: a dead parent context must not
// get to decide "durable" (#962). For the sequence it would be a bug — team
// sync waits on every bubble's confirmation serially and daemon shutdown waits
// on the scheduler goroutine behind them, so an unbounded ladder is shutdown
// latency multiplied by the number of wedged clones. The confirmation therefore
// carries its own budget (reprobeTotalBudget), collapsed to
// reprobeShutdownDrain the moment shutdown is observed between two reads.
//
// A ladder cut short by either deadline returns errProbeAbandoned: it proved
// nothing, and the caller must not read it as a durable fault.
func reprobeIndexConflicts(ctx context.Context, repoPath string) (bool, error) {
	// Detached once: every attempt runs on it, so no attempt inherits the
	// cancellation that may have killed the probe we are here to confirm.
	detached := context.WithoutCancel(ctx)
	deadline := time.Now().Add(reprobeTotalBudget)
	draining := false

	var lastErr error
	for attempt := 1; attempt <= reprobeConfirmAttempts; attempt++ {
		if attempt > 1 {
			// lastErr is necessarily non-nil from here on: attempt 1 runs
			// unconditionally and only a failed read reaches a second attempt.
			if !draining && ctx.Err() != nil {
				draining = true
				deadline = time.Now().Add(reprobeShutdownDrain)
			}
			if !draining {
				time.Sleep(reprobeRetryDelay)
			}
			if !time.Now().Before(deadline) {
				return false, abandonedProbe(lastErr, draining)
			}
		}
		attemptDeadline := time.Now().Add(reprobeAttemptTimeout)
		if attemptDeadline.After(deadline) {
			attemptDeadline = deadline
		}
		probeCtx, cancel := context.WithDeadline(detached, attemptDeadline)
		conflicted, err := gitutil.HasUnmergedEntries(probeCtx, repoPath)
		cancel()
		if err == nil {
			return conflicted, nil
		}
		lastErr = err
	}
	if !time.Now().Before(deadline) {
		// The deadline struck DURING the final read, so that read's failure is
		// the deadline's doing and proves no more than a cut-short ladder does.
		return false, abandonedProbe(lastErr, draining)
	}
	return false, lastErr
}

// abandonedProbe tags why the confirmation stopped while keeping the last read
// error in the chain, so a log line still shows what the reads were failing
// with. lastErr is never nil at either call site.
func abandonedProbe(lastErr error, draining bool) error {
	why := "confirmation budget expired"
	if draining {
		why = "sync context canceled"
	}
	return fmt.Errorf("%w (%s): %w", errProbeAbandoned, why, lastErr)
}

// skipProvesIndexReadable reports whether a skip reason was only reachable
// AFTER something successfully read this clone's index and found no unmerged
// entries in it. "remote unchanged" and "recently fetched" are decided after
// ResolveAutostashConflicts returned (false, nil), which it does only for an
// index with no unmerged entries; "unconfirmed conflict" is decided by a fresh
// re-read that found none. The other skips prove nothing — the lock and rebase
// reasons return before any index is read, and "unconfirmed index read" is the
// case where the re-read was abandoned before it finished.
func skipProvesIndexReadable(reason string) bool {
	switch reason {
	case skipReasonRemoteUnchanged, skipReasonRecentlyFetched, skipReasonUnconfirmedConflict:
		return true
	default:
		return false
	}
}

// clearDisprovedBySkip retires the errors and issues that a skip's own checks
// just disproved. Skips return before the clear-on-success blocks in doPull
// and pullTeamContext, so without this a repo fixed while its remote stood
// still would keep reporting the failure until the remote next changed.
func (s *SyncScheduler) clearDisprovedBySkip(repo, reason string) {
	if reason == skipReasonRemoteUnchanged {
		// HEAD is already the remote tip, where a successful pull leaves it, so
		// no local commit is left to rebase. "recently fetched" proves less: the
		// fetch that wrote FETCH_HEAD may belong to a pull that then failed.
		s.clearErrors(repo)
		if s.issues != nil {
			s.issues.ClearIssue(IssueTypeDiverged, repo)
			s.issues.ClearIssue(IssueTypeSessionConflictWedge, repo)
		}
	}
	if s.issues != nil && skipProvesIndexReadable(reason) {
		// recoverPreexistingRebase left no rebase in progress, and the index
		// has no unmerged entries.
		s.issues.ClearIssue(IssueTypeRepoIntegrity, repo)
		s.issues.ClearIssue(IssueTypeMergeConflict, repo)
		s.issues.ClearIssue(IssueTypeRebaseStuck, repo)
	}
}

// fetchAndPullLocked runs the fetch-then-pull sequence for pullManagedRepo.
// The caller MUST already hold gitutil.WithRepoLock(path) — this method
// performs no locking of its own and must not be called re-entrantly for
// the same clone.
func (s *SyncScheduler) fetchAndPullLocked(ctx context.Context, opts ManagedRepoPullOpts, path, repoName string, logger *slog.Logger) ManagedRepoPullResult {
	// Refresh remote URL if credentials changed
	projectEndpoint := endpoint.GetForProject(opts.ProjectRoot)
	if err := gitserver.RefreshRemoteCredentials(path, projectEndpoint); err != nil {
		logger.Warn("remote credential refresh failed", "path", path, "error", err)
	}

	// ADR-030 D2: a pull that fails purely because two fetches interleaved
	// (fetchHeadRaceSignature) is retried once — under the same lock, so
	// the second fetch cannot race anything — before it is ever treated as
	// a conflict. Any other failure, or a retry that hits the same wall,
	// falls straight through to the ladder below on its final attempt.
	const maxFetchPullAttempts = 2
	var (
		fetchHeadTime time.Time
		diverged      bool
		pullOutput    []byte
		pullErr       error
	)
	for attempt := 1; attempt <= maxFetchPullAttempts; attempt++ {
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
		if info, err := os.Stat(filepath.Join(path, ".git", "FETCH_HEAD")); err == nil {
			fetchHeadTime = info.ModTime().UTC()
			s.recordRemoteChange(path, fetchHeadTime)
		}

		// --- Divergence detection ---
		diverged = false
		if opts.DetectDivergence && detectDivergedBranchesAt(ctx, path) {
			logger.Info("branches diverged, rebasing to reconcile", "repo", repoName)
			diverged = true
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

		// ADR-030 D3: never run `pull --rebase` on top of a rebase this
		// call did not start. Under the repo lock nothing else can be
		// mid-pull against this clone, so in the normal case this is
		// false — recoverPreexistingRebase already handled anything that
		// predates this cycle. It stays as defense in depth for a writer
		// outside the lock (a human running raw git, or a not-yet-locked
		// ox path): skip rather than touch state that isn't this call's
		// to recover, exactly the failure mode that collided on
		// index.lock in the 2026-09-02 incident.
		if gitutil.IsRebaseInProgress(path) {
			logger.Warn("rebase already in progress before pull, not started by this call — skipping", "path", path, "repo", repoName)
			return ManagedRepoPullResult{FetchHeadTime: fetchHeadTime, Diverged: diverged, Skipped: true, SkipReason: skipReasonRebaseInProgress}
		}

		// --- Pull ---
		_, pullSpan := perf.Start(ctx, "git_pull_rebase")
		pullArgs := append([]string{"-C", path}, gitHTTPTimeoutFlags()...)
		pullArgs = append(pullArgs, "pull", "--rebase", "--autostash", "--quiet")
		pullCmd := gitutil.NewNetworkCmd(ctx, pullArgs...)
		pullOutput, pullErr = pullCmd.CombinedOutput()
		if pullErr != nil {
			perf.RecordError(pullSpan, pullErr)
		}
		pullSpan.End()

		if pullErr == nil {
			break
		}
		if attempt < maxFetchPullAttempts && strings.Contains(string(pullOutput), fetchHeadRaceSignature) {
			logger.Warn("FETCH_HEAD race detected (interleaved fetch), re-fetching and retrying once",
				"repo", repoName, "attempt", attempt)
			continue
		}
		break
	}

	result := ManagedRepoPullResult{FetchHeadTime: fetchHeadTime, Diverged: diverged}
	if pullErr == nil {
		return result
	}

	{
		detail := gitutil.SanitizeOutput(strings.TrimSpace(string(pullOutput)))
		err := pullErr
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
