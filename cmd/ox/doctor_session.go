package main

import (
	"context"

	"github.com/sageox/ox/internal/doctor"
	"github.com/sageox/ox/internal/session"
)

// checkSessionHealth returns health checks for the session system.
// The opts parameter provides fix flags for checks that support auto-remediation.
func checkSessionHealth(opts doctorOptions) []checkResult {
	gitRoot := findGitRoot()
	ctx := context.Background()

	// compute health status once (runs multiple git commands internally)
	// and share across all session checks to avoid redundant work
	var healthStatus *session.HealthStatus
	if gitRoot != "" {
		healthStatus = session.CheckHealth(gitRoot)
	}

	var results []checkResult

	// retry failed session uploads first (auto-fix: creates ledger files
	// that downstream auto-stage/commit/push checks operate on)
	uploadRetryResult := checkSessionUploadRetry()
	if !uploadRetryResult.passed || uploadRetryResult.message != "no pending uploads" {
		results = append(results, uploadRetryResult)
	}

	registrationRetryResult := checkSessionLifecycleRegistrationRetry(gitRoot)
	if !registrationRetryResult.passed || registrationRetryResult.message != "no pending registrations" {
		results = append(results, registrationRetryResult)
	}

	// transcripts stranded in the content store with no local copy (GH #710).
	// Quiet on the overwhelmingly common healthy case — dehydration is the
	// normal steady state, so only surface sessions that actually need
	// their transcript and cannot get it.
	// Only surface a real finding: a skipped result (no ledger, no sessions
	// dir) has passed=false too, and including it would put a permanent
	// "skipped" line in doctor output for every user without a ledger.
	dehydratedResult := checkSessionDehydrated(opts.shouldFix(CheckSlugSessionDehydrated))
	if !dehydratedResult.skipped && (!dehydratedResult.passed || dehydratedResult.warning) {
		results = append(results, dehydratedResult)
	}

	// create session checks from internal/doctor package
	checks := []doctor.Check{
		doctor.NewSessionModeCheck(gitRoot),   // show effective mode and source
		doctor.NewSessionLedgerCheck(gitRoot), // verify ledger when mode requires it
		doctor.NewSessionStorageCheck(gitRoot),
		doctor.NewSessionRepoCheck(gitRoot),
		doctor.NewSessionRecordingCheck(gitRoot),
		doctor.NewSessionStaleCheck(gitRoot),
		doctor.NewSessionOrphanedCheck(gitRoot, opts.shouldFix(CheckSlugSessionOrphaned)), // detect orphaned recordings
		doctor.NewSessionStopIncompleteCheck(gitRoot),                                     // detect stuck stop-incomplete recordings
		doctor.NewSessionQuarantineCheck(gitRoot),                                         // recordings held back for crossing repositories
		doctor.NewSessionPendingCheck(gitRoot),
		doctor.NewSessionSyncCheck(gitRoot),
		doctor.NewSessionAutoStageCheck(gitRoot), // auto-stage session files (FixLevelAuto)
	}

	// inject cached health status into checks that support it
	if healthStatus != nil {
		for _, check := range checks {
			if cacheable, ok := check.(doctor.SessionHealthCacheable); ok {
				cacheable.SetHealthStatus(healthStatus)
			}
		}
	}

	// run checks and convert to checkResult format
	for _, check := range checks {
		result := check.Run(ctx, false)

		// skip empty results (StatusSkip with no message)
		if result.Status == doctor.StatusSkip && result.Message == "" {
			continue
		}

		results = append(results, convertDoctorResult(result))
	}

	// add registered DoctorCheck for session commit (supports --fix)
	sessionCommitResult := checkSessionCommit(opts.shouldFix(CheckSlugSessionCommit))
	// only include if not a pass with "no staged sessions" (reduce noise)
	if !sessionCommitResult.passed || sessionCommitResult.message != "no staged sessions" {
		results = append(results, sessionCommitResult)
	}

	// session files written but never committed (a partial session stop)
	uncommittedResult := checkSessionUncommittedViaRegistry(opts)
	if !uncommittedResult.skipped && (!uncommittedResult.passed || uncommittedResult.message != "no uncommitted session files") {
		results = append(results, uncommittedResult)
	}

	// session repairs normally already ran before the Ledger branch-status push (see
	// runSessionRepairs); this returns their recorded results
	results = append(results, opts.runSessionRepairs()...)

	// add session push check (runs after commit, supports --fix)
	// this check pushes committed session data to remote when local is ahead
	sessionPushCheck := doctor.NewSessionPushCheck(gitRoot, opts.shouldFix(CheckSlugSessionPush))
	if healthStatus != nil {
		sessionPushCheck.SetHealthStatus(healthStatus)
	}
	pushResult := sessionPushCheck.Run(ctx, opts.shouldFix(CheckSlugSessionPush))
	// only include if not skipped without message
	if pushResult.Status != doctor.StatusSkip || pushResult.Message != "" {
		results = append(results, convertDoctorResult(pushResult))
	}

	// add incomplete sessions check (context-aware: human vs agent guidance)
	incompleteResult := checkSessionIncomplete(opts.shouldFix(CheckSlugSessionIncomplete))
	// only include if not a pass with "all sessions complete" (reduce noise)
	if !incompleteResult.passed || incompleteResult.message != "all sessions complete" {
		results = append(results, incompleteResult)
	}

	// draft placeholders whose recording is gone
	orphanResult := checkSessionDraftOrphanViaRegistry(opts)
	if !orphanResult.skipped && (!orphanResult.passed || orphanResult.message != "no orphaned drafts") {
		results = append(results, orphanResult)
	}

	// identity integrity: meta.json session_id vs raw.jsonl header
	// session_id. Detect-only (see checkSessionIDDivergence doc comment
	// for why); always shown rather than filtered on a boring-pass
	// message, since a genuine divergence is meant to be rare and loud.
	results = append(results, checkSessionIDDivergence())

	// linkage soft signals: trailer coverage on recent commits, reachability
	// of closed-session ProducedCommits SHAs, and PR-body attribution
	// coverage. None block; all are diagnostic.
	results = append(results, checkSessionTrailerRatio())
	results = append(results, checkSessionProducedCommitsStaleness())
	results = append(results, checkPRAttributionCoverage())

	// native session ids: report-only, so a SessionStart path that stopped
	// recording the agent's own session id is visible instead of silently
	// producing recordings that cannot be matched to the agent's transcript.
	results = append(results, checkSessionNativeSessions())

	return results
}

// sessionRepairState records the session repair results of one doctor run so the
// repairs execute once, at the earliest point that matters, and the Sessions
// category still reports them.
type sessionRepairState struct {
	ran     bool
	results []checkResult
}

// runSessionRepairs runs the repairs that must precede any phase that pushes the
// Ledger: committed conflict markers, then hydrated content where LFS pointers
// belong. The Ledger branch-status auto-fix pushes unpushed commits, and without
// this order it publishes markers the next phase was about to fix. Within one
// doctor run the work happens once; later calls return the recorded results.
func (opts doctorOptions) runSessionRepairs() []checkResult {
	if opts.sessionRepairs != nil && opts.sessionRepairs.ran {
		return opts.sessionRepairs.results
	}
	var results []checkResult

	// committed conflict markers in session files block every push and make the pointer-restore
	// validator below refuse the tip, so they are resolved first
	conflictMarkersResult := checkSessionConflictMarkers(opts.shouldFix(CheckSlugSessionConflictMarkers))
	if !conflictMarkersResult.skipped && (!conflictMarkersResult.passed || conflictMarkersResult.message != "no conflict markers in unpushed session files") {
		results = append(results, conflictMarkersResult)
	}

	// hydrated session content committed where LFS pointers belong blocks every push (#1174);
	// repair runs before the push check so a fix here lets that push proceed
	pointerRestoreResult := checkSessionPointerRestore(opts.shouldFix(CheckSlugSessionPointerRestore))
	if !pointerRestoreResult.skipped && (!pointerRestoreResult.passed || pointerRestoreResult.message != "no raw session content in unpushed commits") {
		results = append(results, pointerRestoreResult)
	}

	if opts.sessionRepairs != nil {
		opts.sessionRepairs.ran = true
		opts.sessionRepairs.results = results
	}
	return results
}

// checkSessionUncommittedViaRegistry and checkSessionDraftOrphanViaRegistry run
// the registered checks, so the registry entry is the one thing --fix-slug
// validates and the one thing the Sessions phase runs.
func checkSessionUncommittedViaRegistry(opts doctorOptions) checkResult {
	check := GetDoctorCheck(CheckSlugSessionUncommitted)
	if check == nil {
		return SkippedCheck(CheckSlugSessionUncommitted, "check is not registered", "")
	}
	return check.Run(opts.shouldFix(CheckSlugSessionUncommitted))
}

func checkSessionDraftOrphanViaRegistry(opts doctorOptions) checkResult {
	check := GetDoctorCheck(CheckSlugSessionDraftOrphan)
	if check == nil {
		return SkippedCheck(CheckSlugSessionDraftOrphan, "check is not registered", "")
	}
	return check.Run(opts.shouldFix(CheckSlugSessionDraftOrphan))
}

// convertDoctorResult converts a doctor.CheckResult to the CLI's checkResult format.
func convertDoctorResult(dr doctor.CheckResult) checkResult {
	switch dr.Status {
	case doctor.StatusPass:
		return PassedCheck(dr.Name, dr.Message)
	case doctor.StatusFail:
		return FailedCheck(dr.Name, dr.Message, dr.Fix)
	case doctor.StatusWarn:
		return WarningCheck(dr.Name, dr.Message, dr.Fix)
	case doctor.StatusSkip:
		return SkippedCheck(dr.Name, dr.Message, dr.Fix)
	default:
		return FailedCheck(dr.Name, "unknown status", "")
	}
}
